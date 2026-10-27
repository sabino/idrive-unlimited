package session

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"idrive-unlimited/internal/atomicfile"
	"idrive-unlimited/internal/processlock"
	"io"
	"net/http"

	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultFileName = "session.json"

type Cookie struct {
	Name       string    `json:"name"`
	Value      string    `json:"value"`
	Path       string    `json:"path,omitempty"`
	Domain     string    `json:"domain,omitempty"`
	Expires    time.Time `json:"expires,omitempty"`
	Secure     bool      `json:"secure,omitempty"`
	HTTPOnly   bool      `json:"http_only,omitempty"`
	OriginHost string    `json:"origin_host,omitempty"`
}

type Data struct {
	ID         string    `json:"session_id,omitempty"`
	ServerHost string    `json:"server_host"`
	SavedAt    time.Time `json:"saved_at"`
	Cookies    []Cookie  `json:"cookies"`
}

type Store struct {
	Path string
}

func NewStore(path string) (*Store, error) {
	if path == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("resolve config dir: %w", err)
		}
		path = filepath.Join(configDir, "idrive-gateway", defaultFileName)
	}
	return &Store{Path: path}, nil
}

func (s *Store) Load() (*Data, error) {
	info, err := os.Stat(s.Path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("session file must be regular")
	}
	f, err := os.Open(s.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 4<<20 {
		return nil, errors.New("session file exceeds 4 MiB")
	}
	var data Data
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("decode session file: %w", err)
	}
	if data.ServerHost == "" {
		return nil, errors.New("session file is missing server_host")
	}
	return &data, nil
}

func (s *Store) Save(data *Data) error {
	return s.save(data, false)
}

// SaveCurrent prevents a running server from overwriting a newer login.
func (s *Store) SaveCurrent(data *Data) error { return s.save(data, true) }

var ErrSessionChanged = errors.New("saved session changed; old server session was not persisted")

func (s *Store) save(data *Data, current bool) error {
	if data == nil {
		return errors.New("session data is nil")
	}
	if data.ServerHost == "" {
		return errors.New("session data is missing server_host")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := processlock.Acquire(ctx, s.Path+".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	if current {
		old, err := s.Load()
		if err != nil {
			return err
		}
		if old.ID != data.ID || old.ServerHost != data.ServerHost || !old.SavedAt.Equal(data.SavedAt) {
			return ErrSessionChanged
		}
	}
	if data.ID == "" {
		raw, _ := json.Marshal([]any{data.ServerHost, data.Cookies})
		sum := sha256.Sum256(raw)
		data.ID = fmt.Sprintf("%x", sum[:])
	}
	data.SavedAt = time.Now().UTC()
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session file: %w", err)
	}
	return atomicfile.Write(s.Path, payload)
}

func (d *Data) NewCookieJar() (http.CookieJar, error) {
	jar, err := NewJar()
	if err != nil {
		return nil, err
	}
	if err := d.ApplyToJar(jar); err != nil {
		return nil, err
	}
	return jar, nil
}

func (d *Data) ApplyToJar(jar http.CookieJar) error {
	for _, saved := range d.Cookies {
		cookie := &http.Cookie{Name: saved.Name, Value: saved.Value, Path: saved.Path, Domain: saved.Domain, Expires: saved.Expires, Secure: saved.Secure, HttpOnly: saved.HTTPOnly}
		requestPath := saved.Path
		if !strings.HasPrefix(requestPath, "/") {
			requestPath = "/"
		}
		host := saved.OriginHost
		if host == "" {
			host = d.ServerHost
		}
		u, err := url.Parse("https://" + host + requestPath)
		if err != nil {
			return err
		}
		jar.SetCookies(u, []*http.Cookie{cookie})
	}
	return nil
}

func (d *Data) BaseURL() (*url.URL, error) {
	if d.ServerHost == "" {
		return nil, errors.New("session data is missing server_host")
	}
	return url.Parse("https://" + d.ServerHost)
}

func FromCookieJar(serverHost string, jar http.CookieJar) (*Data, error) {
	if serverHost == "" {
		return nil, errors.New("server host is required")
	}
	persistent, ok := jar.(*Jar)
	if !ok {
		return nil, errors.New("cookie jar does not preserve cookie attributes; use session.NewJar")
	}
	return &Data{ServerHost: serverHost, Cookies: persistent.snapshot(serverHost)}, nil
}
