package evs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"idrive-unlimited/internal/atomicfile"
	"idrive-unlimited/internal/processlock"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type uploadJournal struct {
	root string
	lock *processlock.Lock
}
type uploadRecord struct {
	Source       string `json:"move_source,omitempty"`
	Version      int    `json:"version"`
	ID           string `json:"id"`
	Dest         string `json:"destination"`
	Stage        string `json:"stage"`
	Backup       string `json:"backup"`
	Size         int64  `json:"size"`
	Hash         string `json:"sha256"`
	HadOriginal  bool   `json:"had_original"`
	OriginalSize int64  `json:"original_size"`
	OriginalHash string `json:"original_sha256"`
	Ready        bool   `json:"ready"`
	Committed    bool   `json:"committed"`
}

// EnableRecovery locks this saved session's transaction namespace and reconciles
// interrupted replacements before the server accepts requests. CloseRecovery is
// required after requests drain. Different logins cannot share recovery state.
func (c *Client) EnableRecovery(ctx context.Context, root string) error {
	root = filepath.Join(root, c.CacheIdentity())
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	lock, err := processlock.Try(filepath.Join(root, "server.lock"))
	if err != nil {
		return err
	}
	c.journal = &uploadJournal{root, lock}
	if _, err = c.RecoverPending(ctx); err != nil {
		c.CloseRecovery()
		return err
	}
	return nil
}
func (c *Client) CloseRecovery() error {
	if c.journal == nil {
		return nil
	}
	err := c.journal.lock.Close()
	c.journal = nil
	return err
}
func (c *Client) saveUpload(r *uploadRecord) error {
	if c.journal == nil {
		return nil
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(c.journal.root, r.ID+".json"), data)
}
func (c *Client) forgetUpload(r *uploadRecord) error {
	if c.journal == nil {
		return nil
	}
	err := os.Remove(filepath.Join(c.journal.root, r.ID+".json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return atomicfile.SyncDir(c.journal.root)
}

// RecoverPending leaves unknown bytes untouched. The caller must exclude other
// gateway mutations; uploadMu also excludes direct UploadFile callers.
func (c *Client) RecoverPending(ctx context.Context) (int, error) {
	if c.journal == nil {
		return 0, nil
	}
	c.uploadMu.Lock()
	defer c.uploadMu.Unlock()
	entries, err := os.ReadDir(c.journal.root)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := filepath.Join(c.journal.root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return count, err
		}
		if !info.Mode().IsRegular() || info.Size() > 16384 {
			return count, fmt.Errorf("invalid recovery journal %s", name)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return count, err
		}
		var r uploadRecord
		if err = json.Unmarshal(data, &r); err != nil {
			return count, fmt.Errorf("decode recovery journal %s", name)
		}
		if err = validateUploadRecord(r, entry.Name()); err != nil {
			return count, fmt.Errorf("invalid recovery journal %s: %w", name, err)
		}
		if _, err = c.recoverUpload(ctx, &r); err != nil {
			return count, fmt.Errorf("recovery needs attention: %s (stage %s, backup %s): %w", r.Dest, r.Stage, r.Backup, err)
		}
		count++
	}
	return count, nil
}
func validateUploadRecord(r uploadRecord, name string) error {
	id := strings.ReplaceAll(r.ID, "-", "")
	raw, err := hex.DecodeString(id)
	if err != nil || len(raw) != 16 || len(r.ID) != 36 || name != r.ID+".json" || r.Version != 1 {
		return errors.New("unknown record identity or version")
	}
	if r.Dest != CleanPath(r.Dest) || r.Dest == "/" || (r.Source == "" && r.Stage != joinClean(ParentDir(r.Dest), ".idrive-gateway-upload-"+r.ID+path.Ext(r.Dest))) || r.Backup != joinClean(ParentDir(r.Dest), ".idrive-gateway-backup-"+r.ID+path.Ext(r.Dest)) {
		return errors.New("invalid transaction paths")
	}
	if r.Source != "" && (r.Source != CleanPath(r.Source) || r.Source == "/" || r.Source == r.Dest || r.Source == r.Backup || r.Stage != r.Source || !r.Ready || mutablePath(r.Source) != nil) {
		return errors.New("invalid move source")
	}
	hash, err := hex.DecodeString(r.Hash)
	if err != nil || len(hash) != sha256.Size || r.Size < 0 || (r.Source == "" && r.Size > MaxUploadSize) {
		return errors.New("invalid payload fingerprint")
	}
	if r.Ready && r.HadOriginal {
		hash, err = hex.DecodeString(r.OriginalHash)
		if err != nil || len(hash) != sha256.Size || r.OriginalSize < 0 {
			return errors.New("invalid original fingerprint")
		}
	}
	return nil
}
func (c *Client) removeKnown(ctx context.Context, name string, size int64, hash string) error {
	if _, err := c.StatFresh(ctx, name); errors.Is(err, ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	expected, err := hex.DecodeString(hash)
	if err != nil {
		return err
	}
	if err = c.verifyUpload(ctx, name, size, expected); err != nil {
		return fmt.Errorf("recovery file changed; retained %s: %w", name, err)
	}
	return c.Delete(ctx, name)
}
func (c *Client) recoverUpload(ctx context.Context, r *uploadRecord) (bool, error) {
	if !r.Ready {
		// No destination mutation is attempted until Ready is durable. A failed
		// staging upload may be incomplete, so remove only its recorded unique name.
		if _, err := c.StatFresh(ctx, r.Stage); err == nil {
			if err = c.Delete(ctx, r.Stage); err != nil {
				return false, err
			}
		} else if !errors.Is(err, ErrNotFound) {
			return false, err
		}
		return false, c.forgetUpload(r)
	}
	expected, _ := hex.DecodeString(r.Hash)
	if !r.Committed {
		if err := c.verifyUpload(ctx, r.Dest, r.Size, expected); err == nil {
			r.Committed = true
			if err = c.saveUpload(r); err != nil {
				return false, err
			}
		} else {
			if r.Source != "" {
				if err := c.verifyUpload(ctx, r.Source, r.Size, expected); err != nil {
					return false, fmt.Errorf("move outcome is uncertain; source and destination were not verified; backup retained at %s: %w", r.Backup, err)
				}
			}
			_, destErr := c.StatFresh(ctx, r.Dest)
			if destErr != nil && !errors.Is(destErr, ErrNotFound) {
				return false, destErr
			}
			if r.HadOriginal {
				oldHash, _ := hex.DecodeString(r.OriginalHash)
				if errors.Is(destErr, ErrNotFound) {
					if err = c.verifyUpload(ctx, r.Backup, r.OriginalSize, oldHash); err != nil {
						return false, err
					}
					if err = c.Rename(ctx, r.Backup, r.Dest); err != nil {
						return false, err
					}
				}
				if err = c.verifyUpload(ctx, r.Dest, r.OriginalSize, oldHash); err != nil {
					return false, fmt.Errorf("destination differs from both recorded versions; all files retained: %w", err)
				}
			} else if destErr == nil {
				return false, errors.New("unexpected destination bytes; recovery files retained")
			}
			if r.Source == "" {
				if err = c.removeKnown(ctx, r.Stage, r.Size, r.Hash); err != nil {
					return false, err
				}
			}
			if r.HadOriginal {
				if err = c.removeKnown(ctx, r.Backup, r.OriginalSize, r.OriginalHash); err != nil {
					return false, err
				}
			}
			return false, c.forgetUpload(r)
		}
	}
	// A committed record never restores or deletes its canonical destination;
	// it may have been edited after success while backup cleanup was unavailable.
	if err := c.saveUpload(r); err != nil {
		return true, err
	}
	if r.HadOriginal {
		if err := c.removeKnown(ctx, r.Backup, r.OriginalSize, r.OriginalHash); err != nil {
			return true, err
		}
	}
	if r.Source == "" {
		if err := c.removeKnown(ctx, r.Stage, r.Size, r.Hash); err != nil {
			return true, err
		}
	}
	return true, c.forgetUpload(r)
}
func (c *Client) remoteSHA256(ctx context.Context, name string, size int64) (string, error) {
	r, err := c.Download(ctx, name)
	if err != nil {
		return "", err
	}
	defer r.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, size+1))
	if err != nil {
		return "", err
	}
	if n != size {
		return "", errors.New("remote size changed while preserving original")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
