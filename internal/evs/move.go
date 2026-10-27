package evs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// MoveReplace implements a recoverable file MOVE without deleting the target
// to make room. On interruption, the source remains until UploadFile has staged,
// read back and promoted the destination. A crash may leave duplicates or the
// existing remote recovery names; it never requires deleting unverified bytes.
// Callers must serialize gateway mutations; external writers cannot be locked
// by the EVS API. Directory replacement is deliberately unsupported.
func (c *Client) MoveReplace(ctx context.Context, source, dest string) error {
	source, dest = CleanPath(source), CleanPath(dest)
	if err := errors.Join(mutablePath(source), mutablePath(dest)); err != nil {
		return err
	}
	if source == dest {
		return nil
	}
	if source == "/" || dest == "/" {
		return ErrReadOnly
	}
	if err := errors.Join(c.requireWritable(ctx, source, true), c.requireWritable(ctx, dest, true)); err != nil {
		return err
	}
	node, err := c.StatFresh(ctx, source)
	if err != nil {
		return err
	}
	if node.IsDir {
		// Existing directory targets must never be recursively deleted. A
		// non-colliding directory MOVE can use the normal EVS path.
		if _, err := c.StatFresh(ctx, dest); err == nil {
			return fmt.Errorf("directory replacement is unsupported")
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		return c.Rename(ctx, source, dest)
	}
	if node.Size > MaxUploadSize {
		return ErrFileTooLarge
	}
	r, err := c.Download(ctx, source)
	if err != nil {
		return err
	}
	defer r.Close()
	f, err := os.CreateTemp("", "idrive-move-*.bin")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	n, copyErr := io.Copy(f, io.LimitReader(r, node.Size+1))
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if n != node.Size {
		return fmt.Errorf("move source size changed during download")
	}
	if err := c.UploadFile(ctx, f.Name(), dest, node.ModTime); err != nil {
		return fmt.Errorf("move source retained at %s: %w", source, err)
	}
	// Recheck the source immediately before removal. Avoid deleting an external
	// edit observed after download (EVS provides no conditional-delete/CAS).
	hash, err := fileSHA256(f.Name())
	if err != nil {
		return err
	}
	if err := c.verifyUpload(ctx, source, node.Size, hash); err != nil {
		return fmt.Errorf("move destination saved; source changed or unavailable and retained at %s: %w", source, err)
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := c.Delete(cleanup, source); err != nil {
		return fmt.Errorf("move destination saved; remove retained source %s: %w", source, err)
	}
	return nil
}
