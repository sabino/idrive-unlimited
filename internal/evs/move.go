package evs

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"time"
)

// MoveReplace uses EVS's native full-path rename. It never separately deletes
// the source, so another writer recreating that path cannot lose its new file.
// Existing targets are preserved until the renamed bytes pass readback. Native
// moves do not upload file content and are not subject to the web upload limit.
func (c *Client) MoveReplace(ctx context.Context, source, dest string) (resultErr error) {
	source, dest = CleanPath(source), CleanPath(dest)
	if err := errors.Join(mutablePath(source), mutablePath(dest)); err != nil {
		return err
	}
	if source == dest {
		return nil
	}
	if source == "/" || dest == "/" || stringsWithin(dest, source) {
		return os.ErrInvalid
	}
	if err := errors.Join(c.requireWritable(ctx, source, true), c.requireWritable(ctx, dest, true), c.protectDeviceRoot(ctx, source)); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	c.uploadMu.Lock()
	defer c.uploadMu.Unlock()
	node, err := c.StatFresh(ctx, source)
	if err != nil {
		return err
	}
	target, targetErr := c.StatFresh(ctx, dest)
	if targetErr != nil && !errors.Is(targetErr, ErrNotFound) {
		return targetErr
	}
	if node.IsDir {
		if targetErr == nil {
			return fmt.Errorf("existing directory targets are unsupported")
		}
		return c.Rename(ctx, source, dest)
	}
	if targetErr == nil && target.IsDir {
		return ErrIsDirectory
	}
	hash, err := c.remoteSHA256(ctx, source, node.Size)
	if err != nil {
		return err
	}
	id, err := randomUploadID()
	if err != nil {
		return err
	}
	tx := &uploadRecord{Version: 1, ID: id, Source: source, Dest: dest, Stage: source, Backup: joinClean(ParentDir(dest), ".idrive-gateway-backup-"+id+path.Ext(dest)), Size: node.Size, Hash: hash, Ready: true, HadOriginal: targetErr == nil}
	if tx.HadOriginal {
		tx.OriginalSize = target.Size
		tx.OriginalHash, err = c.remoteSHA256(ctx, dest, target.Size)
		if err != nil {
			return err
		}
	}
	if err = c.saveUpload(tx); err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			committed, recoveryErr := c.recoverUpload(cleanup, tx)
			if committed && recoveryErr == nil {
				resultErr = nil
			} else {
				resultErr = errors.Join(resultErr, recoveryErr)
			}
		}
	}()
	if tx.HadOriginal {
		if err = c.Rename(ctx, dest, tx.Backup); err != nil {
			return err
		}
	}
	if err = c.Rename(ctx, source, dest); err != nil {
		return err
	}
	expected, err := hex.DecodeString(tx.Hash)
	if err != nil {
		return err
	}
	if err = c.verifyUpload(ctx, dest, node.Size, expected); err != nil {
		return fmt.Errorf("renamed destination verification failed; original recovery path %s: %w", tx.Backup, err)
	}
	tx.Committed = true
	if err = c.saveUpload(tx); err != nil {
		return err
	}
	if tx.HadOriginal {
		if err = c.removeKnown(ctx, tx.Backup, tx.OriginalSize, tx.OriginalHash); err != nil {
			return err
		}
	}
	return c.forgetUpload(tx)
}
func stringsWithin(name, parent string) bool {
	return len(name) > len(parent) && name[:len(parent)] == parent && name[len(parent)] == '/'
}
