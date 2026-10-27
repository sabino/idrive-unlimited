package evs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"
)

// Use a conservative decimal limit matching the documented 2 GB web flow.
const MaxUploadSize int64 = 2_000_000_000

var ErrFileTooLarge = errors.New("idrive: file exceeds the 2 GB web upload limit")

// UploadFile never deletes the destination to make room for an upload. New
// bytes are uploaded and checksum-verified under a unique name first. EVS has
// no demonstrated atomic replace, so an existing destination is renamed to a
// recovery name until the promoted replacement has also been verified.
func (c *Client) UploadFile(ctx context.Context, localPath, remotePath string, modTime time.Time) error {
	clean := CleanPath(remotePath)
	if IsReadOnlyPath(clean) {
		return ErrReadOnly
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return ErrIsDirectory
	}
	if info.Size() > MaxUploadSize {
		return ErrFileTooLarge
	}
	if clean == "/" {
		return ErrIsDirectory
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	// Serialize commits made by this client. This also prevents two local
	// replacements from mistaking each other's recovery names for originals.
	c.uploadMu.Lock()
	defer c.uploadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if node, err := c.StatFresh(ctx, clean); err == nil && node.IsDir {
		return ErrIsDirectory
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	hash, err := fileSHA256(localPath)
	if err != nil {
		return err
	}
	id, err := randomUploadID()
	if err != nil {
		return err
	}
	stage := joinClean(ParentDir(clean), ".idrive-gateway-upload-"+id+path.Ext(clean))
	upload := c.uploadSmall
	if info.Size() >= chunkEnableFileSize {
		upload = c.uploadChunked
	}
	if err := upload(ctx, localPath, stage, modTime); err != nil {
		return c.cleanupStage(stage, err)
	}
	c.invalidatePaths(ParentDir(stage), stage)
	if err := c.verifyUpload(ctx, stage, info.Size(), hash); err != nil {
		return c.cleanupStage(stage, err)
	}

	backup := ""
	if node, err := c.StatFresh(ctx, clean); err == nil {
		if node.IsDir {
			return c.cleanupStage(stage, ErrIsDirectory)
		}
		backup = joinClean(ParentDir(clean), ".idrive-gateway-backup-"+id+path.Ext(clean))
		if err := c.Rename(ctx, clean, backup); err != nil {
			// A transport error can arrive after the server performed the rename.
			// Preserve both unique names when the outcome cannot be established.
			return fmt.Errorf("preserve original for replacement: %w; recovery paths: %s and %s", err, backup, stage)
		}
	} else if !errors.Is(err, ErrNotFound) {
		return c.cleanupStage(stage, err)
	}
	if err := c.Rename(ctx, stage, clean); err != nil {
		return c.recoverReplacement(clean, stage, backup, info.Size(), hash, err)
	}
	if err := c.verifyUpload(ctx, clean, info.Size(), hash); err != nil {
		// Do not delete or overwrite uncertain remote bytes. The caller gets
		// both recovery names, and the original stays available at backup.
		return fmt.Errorf("replacement verification failed: %w; original recovery path: %s", err, backup)
	}
	if backup != "" {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.Delete(cleanup, backup); err != nil {
			return fmt.Errorf("replacement saved; remove original recovery file %s: %w", backup, err)
		}
	}
	c.invalidatePaths(ParentDir(clean), clean)
	return nil
}

func fileSHA256(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func (c *Client) verifyUpload(ctx context.Context, remote string, size int64, expected []byte) error {
	node, err := c.StatFresh(ctx, remote)
	if err != nil {
		return err
	}
	if node.IsDir || node.Size != size {
		return fmt.Errorf("uploaded file size mismatch for %s", remote)
	}
	r, err := c.Download(ctx, remote)
	if err != nil {
		return err
	}
	defer r.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, size+1))
	if err != nil {
		return err
	}
	if n != size || !equalHash(h.Sum(nil), expected) {
		return fmt.Errorf("uploaded file checksum mismatch for %s", remote)
	}
	return nil
}
func equalHash(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (c *Client) cleanupStage(stage string, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.StatFresh(ctx, stage); errors.Is(err, ErrNotFound) {
		return cause
	} else if err != nil {
		return fmt.Errorf("%w; inspect temporary upload %s (cleanup lookup: %v)", cause, stage, err)
	}
	if err := c.Delete(ctx, stage); err != nil {
		return fmt.Errorf("%w; remove temporary upload %s: %v", cause, stage, err)
	}
	return cause
}

func (c *Client) recoverReplacement(dest, stage, backup string, size int64, hash []byte, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// A lost rename response may mean promotion succeeded. Establish that
	// before attempting rollback; otherwise leave all recovery bytes intact.
	if err := c.verifyUpload(ctx, dest, size, hash); err == nil {
		return fmt.Errorf("replacement appears saved after uncertain rename: %w; original recovery path: %s", cause, backup)
	}
	if _, err := c.StatFresh(ctx, dest); errors.Is(err, ErrNotFound) && backup != "" {
		if err := c.Rename(ctx, backup, dest); err == nil {
			return c.cleanupStage(stage, fmt.Errorf("replacement failed; original restored: %w", cause))
		}
	}
	return fmt.Errorf("replacement failed: %w; recovery paths: original=%s uploaded=%s", cause, backup, stage)
}
