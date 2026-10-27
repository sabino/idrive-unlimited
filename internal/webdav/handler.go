package idrivewebdav

import (
	"context"
	"errors"
	"golang.org/x/sync/semaphore"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

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
// not a claim about upstream atomicity: the target backup survives until readback.
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
	state   *writeState
	control *http.ResponseController
}

func (b *requestBody) Read(p []byte) (int, error) {
	if b.control != nil {
		_ = b.control.SetReadDeadline(time.Now().Add(2 * time.Minute))
	}
	n, err := b.ReadCloser.Read(p)
	if err != nil && b.control != nil {
		_ = b.control.SetReadDeadline(time.Time{})
	}
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
func (w statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w statusWriter) Write(p []byte) (int, error) {
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(2 * time.Minute))
	return w.ResponseWriter.Write(p)
}

// NewHandler records body/source failures before a filesystem Close can commit
// an upload. A bare x/net/webdav.Handler cannot provide that guarantee.
func NewHandler(fs *FileSystem, logger func(*http.Request, error)) *Handler {
	next := &netwebdav.Handler{FileSystem: fs, LockSystem: netwebdav.NewMemLS(), Logger: logger}
	const exclusive int64 = 1 << 20
	requests := semaphore.NewWeighted(exclusive)
	return &Handler{next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Keep gateway readers and other mutations outside the replacement's
		// backup/promotion window. This cannot lock external IDrive clients.
		readOnly := r.Method == "GET" || r.Method == "HEAD" || r.Method == "PROPFIND" || r.Method == "OPTIONS"
		weight := exclusive
		if readOnly {
			weight = 1
		}
		if err := requests.Acquire(r.Context(), weight); err != nil {
			http.Error(w, "request canceled while waiting for another operation", http.StatusServiceUnavailable)
			return
		}
		defer requests.Release(weight)
		if r.Method == "OPTIONS" {
			w.Header().Set(SafeMoveHeader, SafeMoveVersion)
		}
		state := &writeState{put: r.Method == http.MethodPut, length: r.ContentLength}
		if r.Method == "MOVE" && r.Header.Get(SafeMoveHeader) == SafeMoveVersion && r.Header.Get("Overwrite") != "F" {
			if dest, err := url.Parse(r.Header.Get("Destination")); err == nil && dest.Path != "" && (dest.Host == "" || dest.Host == r.Host) {
				state.moveDestination = CleanPath(dest.Path)
			}
		}
		limit := int64(1 << 20)
		if state.put {
			limit = evs.MaxUploadSize
		}
		if r.ContentLength > limit {
			http.Error(w, "request body exceeds allowed size", http.StatusRequestEntityTooLarge)
			return
		}
		if !readOnly {
			count, err := fs.client.RecoverPending(r.Context())
			if err == nil && count > 0 {
				err = fs.cache.invalidateAll()
			}
			if err != nil {
				if logger != nil {
					logger(r, err)
				}
				http.Error(w, "pending replacement recovery failed; inspect the gateway log", http.StatusServiceUnavailable)
				return
			}
		}
		if r.Method == http.MethodPut || r.Method == http.MethodDelete || r.Method == "COPY" || r.Method == "MOVE" {
			if !checkPreconditions(w, r, fs) {
				return
			}
		}
		if r.Method == "COPY" || r.Method == "MOVE" {
			if dest, err := url.Parse(r.Header.Get("Destination")); err == nil && dest.Path != "" {
				sourcePath, targetPath := fs.remotePath(r.Context(), r.URL.Path), fs.remotePath(r.Context(), dest.Path)
				if strings.HasPrefix(targetPath, strings.TrimSuffix(sourcePath, "/")+"/") {
					http.Error(w, "cannot copy or move a directory into itself", http.StatusForbidden)
					return
				}
			}
		}
		r = r.Clone(context.WithValue(r.Context(), writeContextKey{}, state))
		if r.Method == "COPY" || (r.Method == "MOVE" && state.moveDestination == "") {
			// x/net/webdav implements Overwrite:T by permanently removing
			// the destination before attempting the operation. Keep these
			// operations collision-safe; replacements use verified PUTs.
			r.Header.Set("Overwrite", "F")
		}
		if r.Body != nil {
			r.Body = &requestBody{ReadCloser: http.MaxBytesReader(w, r.Body, limit), state: state, control: http.NewResponseController(w)}
		}
		if r.Method == "MKCOL" && r.Body != nil && r.ContentLength != 0 {
			var one [1]byte
			n, err := r.Body.Read(one[:])
			if n > 0 {
				http.Error(w, "MKCOL request bodies are unsupported", http.StatusUnsupportedMediaType)
				return
			}
			if err != nil && !errors.Is(err, io.EOF) {
				http.Error(w, "cannot read MKCOL request body", http.StatusBadRequest)
				return
			}
		}
		next.ServeHTTP(statusWriter{w, state}, r)
	})}
}

// Handler tracks requests so shutdown can wait for rollback and local recovery
// after http.Server.Close cancels network connections.
type Handler struct {
	next    http.Handler
	mu      sync.Mutex
	active  int
	closing bool
	idle    chan struct{}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	if h.active == 0 {
		h.idle = make(chan struct{})
	}
	h.active++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.active--
		if h.active == 0 {
			close(h.idle)
		}
		h.mu.Unlock()
	}()
	h.next.ServeHTTP(w, r)
}
func (h *Handler) Drain(ctx context.Context) error {
	h.mu.Lock()
	h.closing = true
	idle := h.idle
	active := h.active
	h.mu.Unlock()
	if active == 0 {
		return nil
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
