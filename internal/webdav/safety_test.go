package idrivewebdav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"idrive-unlimited/internal/evs"
	"idrive-unlimited/internal/testevs"
)

type brokenBody struct{ sent bool }

func (b *brokenBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, io.ErrUnexpectedEOF
	}
	b.sent = true
	return copy(p, "partial"), nil
}
func (b *brokenBody) Close() error { return nil }

func TestPutOnlyCommitsCompleteBodies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   io.ReadCloser
		length int64
		want   int
	}{
		{"broken", &brokenBody{}, 100, 405},
		{"short", io.NopCloser(strings.NewReader("short")), 100, 405},
		{"complete", io.NopCloser(strings.NewReader("complete")), 8, 201},
		{"chunked", io.NopCloser(strings.NewReader("chunked")), -1, 201},
		{"empty", io.NopCloser(strings.NewReader("")), 0, 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testevs.New(t)
			s.Add("/file.txt", []byte("original"))
			c, _ := evs.NewClient(s.Host, nil)
			h := NewHandler(New(c, "/"), nil)
			r := httptest.NewRequest("PUT", "http://gateway/file.txt", nil)
			r.Body = tc.body
			r.ContentLength = tc.length
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got HTTP %d: %s", w.Code, w.Body.String())
			}
			if tc.want != 201 {
				data, _ := s.Read("/file.txt")
				if string(data) != "original" {
					t.Fatalf("aborted body replaced original: %q", data)
				}
				if len(s.Paths()) != 1 {
					t.Fatal("aborted body uploaded remote staging file")
				}
			}
		})
	}
}

func TestFailedPutRetainsCompleteLocalRecoveryCopy(t *testing.T) {
	t.Setenv("IDRIVE_GATEWAY_RECOVERY_DIR", t.TempDir())
	s := testevs.New(t)
	s.Add("/file.txt", []byte("original"))
	s.Configure(testevs.Options{FailUpload: true})
	c, _ := evs.NewClient(s.Host, nil)
	h := NewHandler(New(c, "/"), nil)
	r := httptest.NewRequest("PUT", "http://gateway/file.txt", strings.NewReader("replacement"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code < 400 {
		t.Fatal("failed upload returned success")
	}
	files, err := filepath.Glob(filepath.Join(recoveryDir(), "*.bin"))
	if err != nil || len(files) != 1 {
		t.Fatalf("recovery files=%v err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil || string(data) != "replacement" {
		t.Fatalf("recovery payload %q %v", data, err)
	}
	data, _ = s.Read("/file.txt")
	if string(data) != "original" {
		t.Fatal("original was altered")
	}
}

func TestPutRejectsOversizedBodyBeforeRemoteAccess(t *testing.T) {
	s := testevs.New(t)
	c, _ := evs.NewClient(s.Host, nil)
	h := NewHandler(New(c, "/"), nil)
	r := httptest.NewRequest("PUT", "http://gateway/large", nil)
	r.ContentLength = evs.MaxUploadSize + 1
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 413 || s.Requests() != 0 {
		t.Fatalf("status=%d requests=%d", w.Code, s.Requests())
	}
}

func TestCacheSeparatesAccountsOnSameServer(t *testing.T) {
	t.Setenv(cacheDirEnv, t.TempDir())
	newAccount := func(value string) *FileSystem {
		jar, _ := cookiejar.New(nil)
		u, _ := url.Parse("https://same-server.idrive.com/")
		jar.SetCookies(u, []*http.Cookie{{Name: "EVSID", Value: value}})
		c, _ := evs.NewClient(u.Host, jar)
		return New(c, "/")
	}
	a, b := newAccount("account-a"), newAccount("account-b")
	node := evs.Node{Path: "/test.txt", Name: "test.txt", Size: 5, Version: "1", ModTime: time.Unix(1700000000, 0)}
	a.cache.Ensure(context.Background(), &fakeDownloader{payload: []byte("ALICE")}, node.Path, node)
	dl := &fakeDownloader{payload: []byte("BOB!!")}
	p, err := b.cache.Ensure(context.Background(), dl, node.Path, node)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, dl.payload) || dl.calls != 1 {
		t.Fatalf("cross-account cache data: %q calls=%d", got, dl.calls)
	}
}

func TestCachePrunesOldFilesAndBudget(t *testing.T) {
	root := t.TempDir()
	for i, name := range []string{"old", "recent", "keep"} {
		p := filepath.Join(root, name)
		os.WriteFile(p, []byte("12345"), 0600)
		mtime := time.Now().Add(-time.Duration(3-i) * time.Hour)
		os.Chtimes(p, mtime, mtime)
	}
	if err := pruneFiles(root, filepath.Join(root, "keep"), 5, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(root)
	if len(files) != 1 || files[0].Name() != "keep" {
		t.Fatalf("files=%v", files)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(filepath.Join(root, "keep"), old, old)
	pruneFiles(root, "", 100, retentionAge)
	files, _ = os.ReadDir(root)
	if len(files) != 0 {
		t.Fatal("expired file retained")
	}
}

type canceledDownloader struct{}

func (canceledDownloader) DownloadToFile(ctx context.Context, _, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}
func TestReadPropagatesRequestCancellation(t *testing.T) {
	t.Setenv(cacheDirEnv, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	node := evs.Node{Path: "/file", Size: 1}
	reader, _ := newReadFile(canceledDownloader{}, newFileCache("cancel"), node.Path, node, infoFromNode(node, node.Path))
	reader.ctx = ctx
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

type blockingDownload struct{ started, finished chan struct{} }

func (d blockingDownload) DownloadToFile(ctx context.Context, _, _ string) error {
	close(d.started)
	<-ctx.Done()
	close(d.finished)
	return ctx.Err()
}
func TestCancellationFinishesOwnerCleanupBeforeReturning(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cacheDirEnv, root)
	cache := newFileCache("cancel-in-flight")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dl := blockingDownload{started: make(chan struct{}), finished: make(chan struct{})}
	result := make(chan error, 1)
	go func() { _, err := cache.Ensure(ctx, dl, "/file", evs.Node{Size: 1}); result <- err }()
	<-dl.started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-dl.finished:
	default:
		t.Fatal("download still running after canceled owner returned")
	}
	var remaining []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			remaining = append(remaining, p)
		}
		return err
	})
	if len(remaining) != 0 {
		t.Fatalf("temporary files left after cancellation: %v", remaining)
	}
}

func TestEmptyDirectoryPROPFIND(t *testing.T) {
	s := testevs.New(t)
	s.Directory("/empty")
	c, _ := evs.NewClient(s.Host, nil)
	h := NewHandler(New(c, "/"), nil)
	r := httptest.NewRequest("PROPFIND", "http://gateway/empty/", nil)
	r.Header.Set("Depth", "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 207 {
		t.Fatalf("empty PROPFIND returned HTTP %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Responses []struct {
			Href string `xml:"href"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Responses) != 1 || result.Responses[0].Href != "/empty/" {
		t.Fatalf("unexpected directory listing: %+v", result.Responses)
	}
}

func TestDirectoryReaddirEndSemantics(t *testing.T) {
	for _, size := range []int{0, 1} {
		entries := make([]evs.Node, size)
		if size > 0 {
			entries[0] = evs.Node{Path: "/file", Name: "file"}
		}
		dir := newDirFile(fileInfo{}, entries, nil)
		got, err := dir.Readdir(-1)
		if err != nil || len(got) != size {
			t.Fatalf("initial listing size=%d got=%d err=%v", size, len(got), err)
		}
		for _, count := range []int{-1, 0} {
			got, err = dir.Readdir(count)
			if err != nil || len(got) != 0 {
				t.Fatalf("exhausted Readdir(%d) = %v, %v", count, got, err)
			}
		}
		if _, err := dir.Readdir(1); !errors.Is(err, io.EOF) {
			t.Fatalf("positive Readdir at end: %v", err)
		}
	}
}
