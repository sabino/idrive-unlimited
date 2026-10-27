package idrivewebdav

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultCacheBytes int64 = 4 * 1024 * 1024 * 1024
const retentionAge = 7 * 24 * time.Hour

var maintenanceMu sync.Mutex

func configuredBytes(name string) int64 {
	if n, err := strconv.ParseInt(os.Getenv(name), 10, 64); err == nil && n > 0 {
		return n
	}
	return defaultCacheBytes
}

// Prune on use. Open files may temporarily prevent eviction on Windows; the
// current requested file is protected even if it exceeds the cache budget.
func pruneFiles(root, protected string, maxBytes int64, maxAge time.Duration) error {
	maintenanceMu.Lock()
	defer maintenanceMu.Unlock()
	type item struct {
		path  string
		size  int64
		mtime time.Time
	}
	var files []item
	var total int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if strings.HasPrefix(d.Name(), "download-") && time.Since(info.ModTime()) < 24*time.Hour {
			return nil
		}
		if p != protected && time.Since(info.ModTime()) > maxAge {
			if err := os.Remove(p); err == nil {
				return nil
			}
		}
		files = append(files, item{p, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(files, func(a, b int) bool { return files[a].mtime.Before(files[b].mtime) })
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if f.path != protected {
			if err := os.Remove(f.path); err == nil {
				total -= f.size
			}
		}
	}
	return nil
}

func recoveryDir() string {
	if explicit := os.Getenv("IDRIVE_GATEWAY_RECOVERY_DIR"); explicit != "" {
		return explicit
	}
	if base, err := os.UserCacheDir(); err == nil {
		return filepath.Join(base, "idrive-gateway", "recovery")
	}
	return filepath.Join(os.TempDir(), "idrive-gateway-recovery")
}

func retainUpload(local, remote string, cause error) (string, error) {
	root := recoveryDir()
	if err := os.MkdirAll(root, 0700); err != nil {
		return local, err
	}
	info, err := os.Stat(local)
	if err != nil {
		return local, err
	}
	budget := configuredBytes("IDRIVE_GATEWAY_RECOVERY_MAX_BYTES")
	if info.Size() > budget {
		return local, fmt.Errorf("upload exceeds recovery budget; original staging file retained")
	}
	if err := pruneFiles(root, "", budget-info.Size(), retentionAge); err != nil {
		return local, err
	}
	src, err := os.Open(local)
	if err != nil {
		return local, err
	}
	defer src.Close()
	dest, err := os.CreateTemp(root, "upload-*.bin")
	if err != nil {
		return local, err
	}
	if _, err := io.Copy(dest, src); err != nil {
		dest.Close()
		os.Remove(dest.Name())
		return local, err
	}
	if err := dest.Sync(); err != nil {
		dest.Close()
		os.Remove(dest.Name())
		return local, err
	}
	if err := dest.Close(); err != nil {
		os.Remove(dest.Name())
		return local, err
	}
	if err := src.Close(); err != nil {
		return dest.Name(), err
	}
	meta, _ := json.MarshalIndent(map[string]any{"remote_path": remote, "saved_at": time.Now().UTC(), "error": cause.Error()}, "", "  ")
	if err := os.WriteFile(dest.Name()+".json", meta, 0600); err != nil {
		return dest.Name(), err
	}
	return dest.Name(), nil
}
