package idrivewebdav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"

	netwebdav "golang.org/x/net/webdav"
	"idrive-unlimited/internal/evs"
)

type writeContextKey struct{}
type writeState struct {
	mu               sync.Mutex
	put              bool
	length, received int64
	eof              bool
	err              error
}

func (s *writeState) fail(err error) {
	if s == nil || err == nil || errors.Is(err, io.EOF) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = errors.Join(s.err, err)
}
func (s *writeState) complete() error {
	if s == nil {
		return errors.New("writes require the request-aware WebDAV handler")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.put && (!s.eof || (s.length >= 0 && s.received != s.length)) {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func stateFrom(ctx context.Context) *writeState {
	state, _ := ctx.Value(writeContextKey{}).(*writeState)
	return state
}

type requestBody struct {
	io.ReadCloser
	state *writeState
}

func (b *requestBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.state.mu.Lock()
	b.state.received += int64(n)
	if errors.Is(err, io.EOF) {
		b.state.eof = true
	}
	b.state.mu.Unlock()
	b.state.fail(err)
	return n, err
}

type statusWriter struct {
	http.ResponseWriter
	state *writeState
}

func (w statusWriter) WriteHeader(code int) {
	w.state.mu.Lock()
	err := w.state.err
	w.state.mu.Unlock()
	var maxErr *http.MaxBytesError
	if errors.Is(err, evs.ErrFileTooLarge) || errors.As(err, &maxErr) {
		code = http.StatusRequestEntityTooLarge
	}
	w.ResponseWriter.WriteHeader(code)
}

// NewHandler records body/source failures before a filesystem Close can commit
// an upload. A bare x/net/webdav.Handler cannot provide that guarantee.
func NewHandler(fs *FileSystem, logger func(*http.Request, error)) http.Handler {
	next := &netwebdav.Handler{FileSystem: fs, LockSystem: netwebdav.NewMemLS(), Logger: logger}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := &writeState{put: r.Method == http.MethodPut, length: r.ContentLength}
		if state.put && r.ContentLength > evs.MaxUploadSize {
			http.Error(w, evs.ErrFileTooLarge.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), writeContextKey{}, state))
		if state.put {
			r.Body = &requestBody{ReadCloser: http.MaxBytesReader(w, r.Body, evs.MaxUploadSize), state: state}
		}
		next.ServeHTTP(statusWriter{w, state}, r)
	})
}
