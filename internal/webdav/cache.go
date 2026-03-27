package idrivewebdav

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sync/singleflight"
	"idrive-unlimited/internal/evs"
)

const cacheDirEnv = "IDRIVE_GATEWAY_CACHE_DIR"

type downloader interface {
	DownloadToFile(ctx context.Context, remotePath, localPath string) error
}

type fileCache struct {
	rootDir   string
	namespace string
	group     singleflight.Group
}

func newFileCache(namespace string) *fileCache {
	return &fileCache{
		rootDir:   defaultCacheDir(),
		namespace: cacheNamespace(namespace),
	}
}

func defaultCacheDir() string {
	if explicit := strings.TrimSpace(os.Getenv(cacheDirEnv)); explicit != "" {
		return explicit
	}
	if base, err := os.UserCacheDir(); err == nil && strings.TrimSpace(base) != "" {
		return filepath.Join(base, "idrive-gateway", "files")
	}
	return filepath.Join(os.TempDir(), "idrive-gateway-files")
}

func cacheNamespace(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "default"
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:8])
}

func (c *fileCache) Ensure(ctx context.Context, dl downloader, remotePath string, node evs.Node) (string, error) {
	target := c.targetPath(remotePath, node)
	if ok, err := cacheFileValid(target, node.Size); ok || err != nil {
		return target, err
	}

	result, err, _ := c.group.Do(target, func() (any, error) {
		if ok, err := cacheFileValid(target, node.Size); ok || err != nil {
			return target, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return "", err
		}

		temp, err := os.CreateTemp(filepath.Dir(target), "download-*"+safeExt(remotePath))
		if err != nil {
			return "", err
		}
		tempPath := temp.Name()
		if err := temp.Close(); err != nil {
			_ = os.Remove(tempPath)
			return "", err
		}

		if err := dl.DownloadToFile(ctx, remotePath, tempPath); err != nil {
			_ = os.Remove(tempPath)
			return "", err
		}
		if ok, err := cacheFileValid(tempPath, node.Size); err != nil {
			_ = os.Remove(tempPath)
			return "", err
		} else if !ok {
			_ = os.Remove(tempPath)
			return "", fmt.Errorf("cached download size mismatch for %s", remotePath)
		}

		if err := os.Rename(tempPath, target); err != nil {
			if ok, statErr := cacheFileValid(target, node.Size); statErr == nil && ok {
				_ = os.Remove(tempPath)
				return target, nil
			}
			_ = os.Remove(tempPath)
			return "", err
		}
		return target, nil
	})
	if err != nil {
		return "", err
	}
	return result.(string), nil
}

func (c *fileCache) targetPath(remotePath string, node evs.Node) string {
	key := strings.Join([]string{
		CleanPath(remotePath),
		strconv.FormatInt(node.Size, 10),
		strconv.FormatInt(node.ModTime.UTC().Unix(), 10),
		node.Version,
	}, "|")
	sum := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(sum[:])
	return filepath.Join(c.rootDir, c.namespace, hash[:2], hash+safeExt(remotePath))
}

func safeExt(remotePath string) string {
	ext := path.Ext(CleanPath(remotePath))
	if ext == "" || len(ext) > 16 || strings.ContainsAny(ext, `/\`) {
		return ""
	}
	return ext
}

func cacheFileValid(localPath string, expectedSize int64) (bool, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() {
		return false, fmt.Errorf("cache path is a directory: %s", localPath)
	}
	return info.Size() == expectedSize, nil
}
