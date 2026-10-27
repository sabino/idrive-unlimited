package idrivewebdav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"idrive-unlimited/internal/evs"
	"idrive-unlimited/internal/testevs"
)

func TestSameMetadataReplacementInvalidatesCachedBytes(t *testing.T) {
	t.Setenv(cacheDirEnv, t.TempDir())
	s := testevs.New(t)
	s.Add("/file", []byte("first"))
	c, _ := evs.NewClient(s.Host, nil)
	f := New(c, "/")
	h := NewHandler(f, nil)
	read := func() string {
		t.Helper()
		file, err := f.OpenFile(context.Background(), "/file", os.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if read() != "first" {
		t.Fatal("initial read failed")
	}
	r := httptest.NewRequest("PUT", "http://gateway/file", strings.NewReader("later"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code >= 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if got := read(); got != "later" {
		t.Fatalf("stale replacement read: %q", got)
	}
}

func TestWriteAtExistingOffsetUsesResultingLength(t *testing.T) {
	temp, err := os.CreateTemp(t.TempDir(), "write-")
	if err != nil {
		t.Fatal(err)
	}
	defer temp.Close()
	if err := temp.Truncate(evs.MaxUploadSize); err != nil {
		t.Fatal(err)
	}
	wf := &writeFile{file: temp, ctx: context.Background()}
	if _, err := wf.Write([]byte("a")); err != nil {
		t.Fatalf("in-place write rejected: %v", err)
	}
}

func TestUnsafeDeviceAliasesDoNotChangePaths(t *testing.T) {
	nodes := []evs.Node{{Name: "raw", Device: &evs.DeviceMeta{Name: "../escape"}}, {Name: "other", Device: &evs.DeviceMeta{Name: "raw"}}}
	aliases := buildRootAliases(nodes)
	if aliases.rawToDisplay["raw"] != "raw" || aliases.rawToDisplay["other"] != "other" {
		t.Fatalf("unsafe aliases: %v", aliases.rawToDisplay)
	}
}

func TestDirectoryCopyIntoItselfIsRejectedBeforeMutation(t *testing.T) {
	s := testevs.New(t)
	s.Directory("/source")
	s.Directory("/source/nested")
	s.Add("/source/file", []byte("keep"))
	c, _ := evs.NewClient(s.Host, nil)
	request := httptest.NewRequest("COPY", "http://gateway/source", nil)
	request.Header.Set("Destination", "http://gateway/source/nested/copied")
	response := httptest.NewRecorder()
	NewHandler(New(c, "/"), nil).ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatalf("recursive copy status=%d", response.Code)
	}
	if _, err := c.StatFresh(context.Background(), "/source/nested/copied"); err == nil {
		t.Fatal("recursive copy created destination")
	}
}

func TestCacheOverrideNeverPrunesUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cacheDirEnv, root)
	t.Setenv("IDRIVE_GATEWAY_RECOVERY_DIR", root)
	t.Setenv("IDRIVE_GATEWAY_CACHE_MAX_BYTES", "1")
	unrelated := filepath.Join(root, "user-file")
	os.WriteFile(unrelated, []byte("keep"), 0600)
	past := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(unrelated, past, past)
	cache := newFileCache("private")
	_, err := cache.Ensure(context.Background(), &fakeDownloader{payload: []byte("x")}, "/file", evs.Node{Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "payload")
	os.WriteFile(local, []byte("x"), 0600)
	if _, err := retainUpload(local, "/file", errors.New("injected")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(unrelated); err != nil || string(got) != "keep" {
		t.Fatal("pruned unrelated user file")
	}
}

func TestConditionalWritesDoNotIgnorePreconditions(t *testing.T) {
	s := testevs.New(t)
	s.Add("/file", []byte("old"))
	c, _ := evs.NewClient(s.Host, nil)
	h := NewHandler(New(c, "/"), nil)
	head := httptest.NewRecorder()
	h.ServeHTTP(head, httptest.NewRequest("HEAD", "http://gateway/file", nil))
	tag := head.Header().Get("ETag")
	if !strings.HasPrefix(tag, "W/") {
		t.Fatalf("metadata ETag claims strong identity: %q", tag)
	}
	for _, headers := range []map[string]string{{"If-None-Match": "*"}, {"If-Match": tag}, {"If-None-Match": tag}, {"If-Match": "\"unknown\""}} {
		req := httptest.NewRequest("PUT", "http://gateway/file", strings.NewReader("new"))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 412 {
			t.Fatalf("ignored %v: status %d", headers, w.Code)
		}
	}
	req := httptest.NewRequest("PUT", "http://gateway/file", strings.NewReader("new"))
	req.Header.Set("If-Match", "*")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code >= 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	newHead := httptest.NewRecorder()
	h.ServeHTTP(newHead, httptest.NewRequest("HEAD", "http://gateway/file", nil))
	if newHead.Header().Get("ETag") == tag {
		t.Fatal("ETag did not change after same-size local write")
	}
}

func TestNonUploadBodyLimit(t *testing.T) {
	s := testevs.New(t)
	c, _ := evs.NewClient(s.Host, nil)
	req := httptest.NewRequest("PROPPATCH", "http://gateway/file", nil)
	req.ContentLength = (1 << 20) + 1
	w := httptest.NewRecorder()
	NewHandler(New(c, "/"), nil).ServeHTTP(w, req)
	if w.Code != 413 || s.Requests() != 0 {
		t.Fatalf("status=%d remote requests=%d", w.Code, s.Requests())
	}
}

func TestHandlerDrainWaitsAndRefusesNewRequests(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	h := &Handler{next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release })}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://gateway/", nil))
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if !errors.Is(h.Drain(ctx), context.DeadlineExceeded) {
		t.Fatal("shutdown did not wait")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://gateway/", nil))
	if w.Code != 503 {
		t.Fatal("draining handler accepted new work")
	}
	close(release)
	<-done
	if err := h.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMkdirExistingAndChunkedBodyDoNotMutate(t *testing.T) {
	s := testevs.New(t)
	s.Add("/file", []byte("keep"))
	c, _ := evs.NewClient(s.Host, nil)
	h := NewHandler(New(c, "/"), nil)
	req := httptest.NewRequest("MKCOL", "http://gateway/file", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code < 400 {
		t.Fatal("MKCOL replaced existing file")
	}
	req = httptest.NewRequest("MKCOL", "http://gateway/new", strings.NewReader("unsupported"))
	req.ContentLength = -1
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 415 {
		t.Fatal("chunked MKCOL body was ignored", w.Code)
	}
	if _, err := c.StatFresh(context.Background(), "/new"); err == nil {
		t.Fatal("MKCOL with unsupported body created directory")
	}
}
