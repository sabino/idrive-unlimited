// Package testevs supplies a local EVS protocol fixture. It never contacts IDrive.
package testevs

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
)

type Options struct {
	FailUpload         bool
	FailPromotion      bool
	LostPromotionReply bool
	CorruptStage       bool
	FailBackup         bool
	LostBackupReply    bool
	FailDownload       bool
	HTMLUpload         bool
	CorruptCopy        bool
}

type Server struct {
	mu       sync.Mutex
	server   *httptest.Server
	Host     string
	files    map[string][]byte
	dirs     map[string]bool
	chunks   map[string][]byte
	options  Options
	requests int
	deletes  []string
}

func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{files: map[string][]byte{}, dirs: map[string]bool{"/": true}, chunks: map[string][]byte{}}
	s.server = httptest.NewTLSServer(http.HandlerFunc(s.handle))
	s.Host = strings.TrimPrefix(s.server.URL, "https://")
	old := http.DefaultTransport
	http.DefaultTransport = s.server.Client().Transport
	t.Cleanup(func() { s.server.Close(); http.DefaultTransport = old })
	return s
}
func (s *Server) Configure(opts Options) { s.mu.Lock(); defer s.mu.Unlock(); s.options = opts }
func (s *Server) Add(name string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[name] = append([]byte{}, data...)
	s.dirs[path.Dir(name)] = true
}
func (s *Server) Directory(name string) { s.mu.Lock(); defer s.mu.Unlock(); s.dirs[name] = true }
func (s *Server) Read(name string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[name]
	return append([]byte{}, data...), ok
}
func (s *Server) Paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var paths []string
	for p := range s.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}
func (s *Server) Requests() int { s.mu.Lock(); defer s.mu.Unlock(); return s.requests }
func (s *Server) Deletes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.deletes...)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	w.Header().Set("Content-Type", "application/json")
	reply := func(message, desc string) {
		json.NewEncoder(w).Encode(map[string]any{"message": message, "desc": desc})
	}
	if r.URL.Path == "/evs/v1/downloadFile" {
		if s.options.FailDownload {
			http.Error(w, "injected download failure", 503)
			return
		}
		data, ok := s.files[r.URL.Query().Get("p")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		if s.options.CorruptStage && strings.Contains(r.URL.Query().Get("p"), ".idrive-gateway-upload-") {
			data = append([]byte{}, data...)
			if len(data) > 0 {
				data[0] ^= 0xff
			}
		}
		w.Write(data)
		return
	}
	if r.URL.Path == "/evs/web/upload_session" {
		json.NewEncoder(w).Encode(map[string]string{"message": "SUCCESS", "upload_session_id": "fixture-session"})
		return
	}
	if r.URL.Path == "/evs/web/upload" {
		if s.options.FailUpload {
			http.Error(w, "injected upload failure", 503)
			return
		}
		if s.options.HTMLUpload {
			fmt.Fprint(w, "<html>login required</html>")
			return
		}
		if err := r.ParseMultipartForm(4 * 1024 * 1024); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		name := path.Join(r.FormValue("p"), r.FormValue("file_name"))
		if r.FormValue("close_session") == "true" {
			s.files[name] = s.chunks[name]
			delete(s.chunks, name)
		} else {
			file, _, err := r.FormFile("file")
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			defer file.Close()
			data, _ := io.ReadAll(file)
			if r.FormValue("upload_session_id") != "" {
				s.chunks[name] = append(s.chunks[name], data...)
			} else {
				s.files[name] = data
			}
		}
		reply("SUCCESS", "")
		return
	}
	r.ParseForm()
	switch r.URL.Path {
	case "/evs/browseFolder":
		p := r.FormValue("p")
		if !s.dirs[p] {
			reply("ERROR", "INVALID PATH")
			return
		}
		contents := []map[string]any{}
		for name, data := range s.files {
			if path.Dir(name) == p {
				contents = append(contents, map[string]any{"name": path.Base(name), "size": fmt.Sprint(len(data)), "lmd": "2026/10/27 12:00:00", "ver": "1"})
			}
		}
		for name := range s.dirs {
			if name != "/" && path.Dir(name) == p {
				contents = append(contents, map[string]any{"name": path.Base(name), "size": "0", "is_dir": true})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"message": "SUCCESS", "path": p, "contents": contents})
	case "/evs/createFolder":
		s.dirs[path.Join(r.FormValue("p"), r.FormValue("foldername"))] = true
		reply("SUCCESS", "")
	case "/evs/renameFileFolder":
		old, newName := r.FormValue("oldpath"), r.FormValue("newpath")
		promotion := strings.Contains(path.Base(old), ".idrive-gateway-upload-")
		backup := strings.Contains(path.Base(newName), ".idrive-gateway-backup-")
		if (promotion && s.options.FailPromotion) || (backup && s.options.FailBackup) {
			http.Error(w, "injected rename failure", 503)
			return
		}
		if _, ok := s.files[newName]; ok || s.dirs[newName] {
			reply("ERROR", "destination exists")
			return
		}
		if data, ok := s.files[old]; ok {
			s.files[newName] = data
			delete(s.files, old)
		} else if s.dirs[old] {
			s.dirs[newName] = true
			delete(s.dirs, old)
			for p, data := range s.files {
				if strings.HasPrefix(p, old+"/") {
					s.files[newName+strings.TrimPrefix(p, old)] = data
					delete(s.files, p)
				}
			}
		} else {
			reply("ERROR", "NOT FOUND")
			return
		}
		if (promotion && s.options.LostPromotionReply) || (backup && s.options.LostBackupReply) {
			http.Error(w, "injected lost reply", 503)
			return
		}
		reply("SUCCESS", "")
	case "/evs/v1/deleteFile":
		p := r.FormValue("p")
		s.deletes = append(s.deletes, p)
		delete(s.files, p)
		delete(s.dirs, p)
		for name := range s.files {
			if strings.HasPrefix(name, p+"/") {
				delete(s.files, name)
			}
		}
		for name := range s.dirs {
			if strings.HasPrefix(name, p+"/") {
				delete(s.dirs, name)
			}
		}
		reply("SUCCESS", "")
	case "/evs/copyPasteFileFolder", "/evs/move":
		old := r.FormValue("fileFolderPaths")
		target := path.Join(r.FormValue("p"), path.Base(old))
		data, ok := s.files[old]
		if !ok {
			reply("ERROR", "NOT FOUND")
			return
		}
		s.files[target] = append([]byte{}, data...)
		if r.URL.Path == "/evs/copyPasteFileFolder" && s.options.CorruptCopy && len(s.files[target]) > 0 {
			s.files[target][0] ^= 0xff
		}
		if r.URL.Path == "/evs/move" {
			delete(s.files, old)
		}
		reply("SUCCESS", "")
	default:
		http.Error(w, "unexpected endpoint "+r.URL.Path, 500)
	}
}
