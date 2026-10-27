package session

import (
	"encoding/json"
	"errors"
	"fmt"
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
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, err
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
	if data == nil {
		return errors.New("session data is nil")
	}
	if data.ServerHost == "" {
		return errors.New("session data is missing server_host")
	}
	data.SavedAt = time.Now().UTC()
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session file: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(s.Path), ".session-*")
	if err != nil {
		return fmt.Errorf("create session file: %w", err)
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		return fmt.Errorf("write session file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync session file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), s.Path); err != nil {
		return fmt.Errorf("replace session file: %w", err)
	}
	return nil
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
