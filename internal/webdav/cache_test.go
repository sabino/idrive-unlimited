package idrivewebdav

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"idrive-unlimited/internal/evs"
)

type fakeDownloader struct {
	payload []byte
	calls   int
}

func (f *fakeDownloader) DownloadToFile(_ context.Context, _ string, localPath string) error {
	f.calls++
	return os.WriteFile(localPath, f.payload, 0o600)
}

func TestReadFileLazyDownloadAndCacheReuse(t *testing.T) {
	t.Setenv(cacheDirEnv, t.TempDir())

	downloader := &fakeDownloader{payload: []byte("hello world")}
	cache := newFileCache("test")
	node := evs.Node{
		Path:    "/folder/file.txt",
		Name:    "file.txt",
		Size:    int64(len(downloader.payload)),
		ModTime: time.Unix(1700000000, 0).UTC(),
	}
	info := fileInfo{
		name:    "file.txt",
		size:    node.Size,
		mode:    0o644,
		modTime: node.ModTime,
	}

	first, err := newReadFile(downloader, cache, node.Path, node, info)
	if err != nil {
		t.Fatalf("newReadFile() error = %v", err)
	}
	if downloader.calls != 0 {
		t.Fatalf("expected lazy open to avoid downloads, got %d calls", downloader.calls)
	}
	if _, err := first.Stat(); err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if downloader.calls != 0 {
		t.Fatalf("expected Stat to avoid downloads, got %d calls", downloader.calls)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if downloader.calls != 0 {
		t.Fatalf("expected Close without reads to avoid downloads, got %d calls", downloader.calls)
	}

	reader, err := newReadFile(downloader, cache, node.Path, node, info)
	if err != nil {
		t.Fatalf("newReadFile() error = %v", err)
	}
	buf := make([]byte, len(downloader.payload))
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatalf("ReadFull() error = %v", err)
	}
	if got := string(buf); got != string(downloader.payload) {
		t.Fatalf("Read() = %q, want %q", got, string(downloader.payload))
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if downloader.calls != 1 {
		t.Fatalf("expected one download after first read, got %d", downloader.calls)
	}

	cachedReader, err := newReadFile(downloader, cache, node.Path, node, info)
	if err != nil {
		t.Fatalf("newReadFile() error = %v", err)
	}
	buf = make([]byte, 5)
	if _, err := io.ReadFull(cachedReader, buf); err != nil {
		t.Fatalf("ReadFull() error = %v", err)
	}
	if got := string(buf); got != "hello" {
		t.Fatalf("Read() = %q, want %q", got, "hello")
	}
	if err := cachedReader.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if downloader.calls != 1 {
		t.Fatalf("expected cached read to avoid redownload, got %d calls", downloader.calls)
	}
}

func TestReadFileSeekBeforeRead(t *testing.T) {
	t.Setenv(cacheDirEnv, t.TempDir())

	downloader := &fakeDownloader{payload: []byte("abcdef")}
	cache := newFileCache("test")
	node := evs.Node{
		Path:    "/folder/file.txt",
		Name:    "file.txt",
		Size:    int64(len(downloader.payload)),
		ModTime: time.Unix(1700000000, 0).UTC(),
	}
	info := fileInfo{
		name:    "file.txt",
		size:    node.Size,
		mode:    0o644,
		modTime: node.ModTime,
	}

	reader, err := newReadFile(downloader, cache, node.Path, node, info)
	if err != nil {
		t.Fatalf("newReadFile() error = %v", err)
	}
	if _, err := reader.Seek(2, io.SeekStart); err != nil {
		t.Fatalf("Seek() error = %v", err)
	}
	if downloader.calls != 0 {
		t.Fatalf("expected Seek before Read to avoid downloads, got %d calls", downloader.calls)
	}

	buf := make([]byte, 2)
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatalf("ReadFull() error = %v", err)
	}
	if got := string(buf); got != "cd" {
		t.Fatalf("Read() = %q, want %q", got, "cd")
	}
	if downloader.calls != 1 {
		t.Fatalf("expected exactly one download, got %d", downloader.calls)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
