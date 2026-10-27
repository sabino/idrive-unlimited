package probe

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"idrive-unlimited/internal/evs"
)

type Step struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type Report struct {
	RootPath    string    `json:"root_path"`
	ScratchPath string    `json:"scratch_path"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	Success     bool      `json:"success"`
	Steps       []Step    `json:"steps"`
}

func Run(ctx context.Context, client *evs.Client, rootPath, scratchPath string) (report *Report, runErr error) {
	root := evs.CleanPath(rootPath)
	scratch := allocateScratch(resolveScratch(root, scratchPath))

	report = &Report{
		RootPath:    root,
		ScratchPath: scratch,
		StartedAt:   time.Now().UTC(),
	}
	defer func() {
		report.FinishedAt = time.Now().UTC()
	}()

	record := func(name string, err error, detail string) error {
		step := Step{Name: name, OK: err == nil}
		if detail != "" {
			step.Detail = detail
		} else if err != nil {
			step.Detail = err.Error()
		}
		report.Steps = append(report.Steps, step)
		return err
	}

	if _, err := client.Browse(ctx, root); err != nil {
		record("browse-root", err, "")
		report.Success = false
		return report, err
	}
	record("browse-root", nil, root)

	if err := removeTree(ctx, client, scratch); err != nil {
		record("cleanup-stale-scratch", err, scratch)
		report.Success = false
		return report, err
	}

	if err := client.Mkdir(ctx, scratch); err != nil {
		record("mkdir-scratch", err, "")
		report.Success = false
		return report, err
	}
	record("mkdir-scratch", nil, scratch)
	cleaned := false
	defer func() {
		if cleaned {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := client.StatFresh(cleanup, scratch)
		if errors.Is(err, evs.ErrNotFound) {
			return
		}
		if err == nil {
			err = client.Delete(cleanup, scratch)
		}
		if err != nil {
			record("cleanup-after-failure", err, scratch)
			report.Success = false
			runErr = errors.Join(runErr, fmt.Errorf("scratch cleanup %s: %w", scratch, err))
		}
	}()

	smallLocal, smallHash, err := writeTempFile("idrive-gateway-small-", 256*1024+321)
	if err != nil {
		record("prepare-small-file", err, "")
		report.Success = false
		return report, err
	}
	defer os.Remove(smallLocal)
	record("prepare-small-file", nil, path.Base(smallLocal))

	chunkLocal, chunkHash, err := writeTempFile("idrive-gateway-chunk-", 2*1024*1024+12345)
	if err != nil {
		record("prepare-chunked-file", err, "")
		report.Success = false
		return report, err
	}
	defer os.Remove(chunkLocal)
	record("prepare-chunked-file", nil, path.Base(chunkLocal))

	smallRemote := evs.CleanPath(path.Join(scratch, "small.bin"))
	chunkRemote := evs.CleanPath(path.Join(scratch, "chunk.bin"))

	if err := client.UploadFile(ctx, smallLocal, smallRemote, time.Now()); err != nil {
		record("upload-small", err, "")
		report.Success = false
		return report, err
	}
	record("upload-small", nil, smallRemote)

	if err := client.UploadFile(ctx, chunkLocal, chunkRemote, time.Now()); err != nil {
		record("upload-chunked", err, "")
		report.Success = false
		return report, err
	}
	record("upload-chunked", nil, chunkRemote)

	renamedSmall := evs.CleanPath(path.Join(scratch, "renamed-small.bin"))
	if err := client.Rename(ctx, smallRemote, renamedSmall); err != nil {
		record("rename-small", err, "")
		report.Success = false
		return report, err
	}
	record("rename-small", nil, renamedSmall)

	destDir := evs.CleanPath(path.Join(scratch, "dest"))
	if err := client.Mkdir(ctx, destDir); err != nil {
		record("mkdir-dest", err, "")
		report.Success = false
		return report, err
	}
	record("mkdir-dest", nil, destDir)

	if err := client.Copy(ctx, renamedSmall, destDir); err != nil {
		record("copy-small", err, "")
		report.Success = false
		return report, err
	}
	copiedSmall := evs.CleanPath(path.Join(destDir, path.Base(renamedSmall)))
	record("copy-small", nil, copiedSmall)

	if err := client.Move(ctx, chunkRemote, destDir); err != nil {
		record("move-chunked", err, "")
		report.Success = false
		return report, err
	}
	movedChunk := evs.CleanPath(path.Join(destDir, path.Base(chunkRemote)))
	record("move-chunked", nil, movedChunk)

	downloadPath := strings.TrimSuffix(chunkLocal, path.Ext(chunkLocal)) + ".download"
	defer os.Remove(downloadPath)
	if err := client.DownloadToFile(ctx, movedChunk, downloadPath); err != nil {
		record("download-chunked", err, "")
		report.Success = false
		return report, err
	}
	downloadHash, err := md5File(downloadPath)
	if err != nil {
		record("download-verify", err, "")
		report.Success = false
		return report, err
	}
	if downloadHash != chunkHash {
		err := fmt.Errorf("checksum mismatch: got %s want %s", downloadHash, chunkHash)
		record("download-verify", err, "")
		report.Success = false
		return report, err
	}
	record("download-verify", nil, movedChunk)

	copiedDownload := smallLocal + ".download"
	defer os.Remove(copiedDownload)
	if err := client.DownloadToFile(ctx, copiedSmall, copiedDownload); err != nil {
		record("verify-copied-small", err, "")
		return report, err
	}
	copiedHash, err := md5File(copiedDownload)
	if err == nil && copiedHash != smallHash {
		err = fmt.Errorf("copied small file checksum mismatch")
	}
	if err != nil {
		record("verify-copied-small", err, "")
		return report, err
	}
	record("verify-copied-small", nil, copiedSmall)

	cleanupTargets := []string{copiedSmall, movedChunk, destDir, renamedSmall, scratch}
	for _, target := range cleanupTargets {
		if err := client.Delete(ctx, target); err != nil {
			record("cleanup-"+path.Base(target), err, target)
			report.Success = false
			return report, err
		}
		record("cleanup-"+path.Base(target), nil, target)
	}

	if _, err := client.StatFresh(ctx, scratch); !errors.Is(err, evs.ErrNotFound) {
		if err == nil {
			err = fmt.Errorf("scratch path still exists after cleanup")
		}
		record("cleanup-verify", err, "")
		return report, err
	}
	cleaned = true
	record("cleanup-verify", nil, scratch)

	if smallHash == "" || chunkHash == "" {
		report.Success = false
		return report, fmt.Errorf("unexpected empty checksum state")
	}

	report.Success = true
	return report, nil
}

func resolveScratch(root, scratch string) string {
	root = evs.CleanPath(root)
	scratch = evs.CleanPath(scratch)
	if root == "/" {
		return scratch
	}
	if scratch == root || strings.HasPrefix(scratch, root+"/") {
		return scratch
	}
	return evs.CleanPath(path.Join(root, strings.TrimPrefix(scratch, "/")))
}

func allocateScratch(base string) string {
	clean := evs.CleanPath(base)
	suffix := time.Now().UTC().Format("20060102t150405")

	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err == nil {
		suffix += "-" + hex.EncodeToString(buf)
	}

	return evs.CleanPath(clean + "-" + suffix)
}

func removeTree(ctx context.Context, client *evs.Client, remotePath string) error {
	clean := evs.CleanPath(remotePath)
	if clean == "/" {
		return fmt.Errorf("refusing to remove the root directory")
	}

	info, err := client.Stat(ctx, clean)
	if errors.Is(err, evs.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	if info.IsDir {
		children, err := client.Browse(ctx, clean)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := removeTree(ctx, client, child.Path); err != nil {
				return err
			}
		}
	}

	return client.Delete(ctx, clean)
}

func writeTempFile(prefix string, size int) (string, string, error) {
	file, err := os.CreateTemp("", prefix)
	if err != nil {
		return "", "", err
	}
	defer file.Close()

	hash := md5.New()
	buf := make([]byte, 32*1024)
	remaining := size
	seed := byte(0x41)

	for remaining > 0 {
		chunk := len(buf)
		if remaining < chunk {
			chunk = remaining
		}
		for i := 0; i < chunk; i++ {
			buf[i] = seed + byte((i+remaining)%23)
		}
		if _, err := file.Write(buf[:chunk]); err != nil {
			return "", "", err
		}
		if _, err := hash.Write(buf[:chunk]); err != nil {
			return "", "", err
		}
		remaining -= chunk
		seed++
	}

	return file.Name(), hex.EncodeToString(hash.Sum(nil)), nil
}

func md5File(localPath string) (string, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
