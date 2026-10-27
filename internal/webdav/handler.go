package idrivewebdav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
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
	moveDestination  string
}

// SafeMoveHeader is negotiated by the companion rclone build before it skips
// its otherwise destructive destination DELETE. The protocol is recoverable,
// not an upstream atomic rename: the source is removed only after readback.
const SafeMoveHeader = "X-IDrive-Safe-Move"
const SafeMoveVersion = "verified-copy-v1"

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
	var requests sync.RWMutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Keep gateway readers and other mutations outside the replacement's
		// backup/promotion window. This cannot lock external IDrive clients.
		if r.Method == "GET" || r.Method == "HEAD" || r.Method == "PROPFIND" || r.Method == "OPTIONS" {
			requests.RLock()
			defer requests.RUnlock()
		} else {
			requests.Lock()
			defer requests.Unlock()
		}
		if r.Method == "OPTIONS" {
			w.Header().Set(SafeMoveHeader, SafeMoveVersion)
		}
		state := &writeState{put: r.Method == http.MethodPut, length: r.ContentLength}
		if r.Method == "MOVE" && r.Header.Get(SafeMoveHeader) == SafeMoveVersion && r.Header.Get("Overwrite") != "F" {
			if dest, err := url.Parse(r.Header.Get("Destination")); err == nil && dest.Path != "" && (dest.Host == "" || dest.Host == r.Host) {
				state.moveDestination = CleanPath(dest.Path)
			}
		}
		if state.put && r.ContentLength > evs.MaxUploadSize {
			http.Error(w, evs.ErrFileTooLarge.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		r = r.Clone(context.WithValue(r.Context(), writeContextKey{}, state))
		if r.Method == "COPY" || (r.Method == "MOVE" && state.moveDestination == "") {
			// x/net/webdav implements Overwrite:T by permanently removing
			// the destination before attempting the operation. Keep these
			// operations collision-safe; replacements use verified PUTs.
			r.Header.Set("Overwrite", "F")
		}
		if state.put {
			r.Body = &requestBody{ReadCloser: http.MaxBytesReader(w, r.Body, evs.MaxUploadSize), state: state}
		}
		next.ServeHTTP(statusWriter{w, state}, r)
	})
}
