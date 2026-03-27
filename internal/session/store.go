package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultFileName = "session.json"

type Cookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Path     string    `json:"path,omitempty"`
	Domain   string    `json:"domain,omitempty"`
	Expires  time.Time `json:"expires,omitempty"`
	Secure   bool      `json:"secure,omitempty"`
	HTTPOnly bool      `json:"http_only,omitempty"`
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
	if err := os.WriteFile(s.Path, payload, 0o600); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}
	return nil
}

func (d *Data) NewCookieJar() (http.CookieJar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	if err := d.ApplyToJar(jar); err != nil {
		return nil, err
	}
	return jar, nil
}

func (d *Data) ApplyToJar(jar http.CookieJar) error {
	grouped := map[string][]*http.Cookie{}
	for _, saved := range d.Cookies {
		cookie := &http.Cookie{
			Name:     saved.Name,
			Value:    saved.Value,
			Path:     saved.Path,
			Domain:   saved.Domain,
			Expires:  saved.Expires,
			Secure:   saved.Secure,
			HttpOnly: saved.HTTPOnly,
		}
		requestPath := "/"
		if strings.TrimSpace(saved.Path) != "" {
			requestPath = saved.Path
		}
		grouped[requestPath] = append(grouped[requestPath], cookie)
	}
	for requestPath, cookies := range grouped {
		u, err := url.Parse("https://" + d.ServerHost + requestPath)
		if err != nil {
			return err
		}
		jar.SetCookies(u, cookies)
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
	requestPaths := []string{"/", "/evs", "/evs/"}
	seen := map[string]struct{}{}
	saved := make([]Cookie, 0)
	for _, requestPath := range requestPaths {
		u, err := url.Parse("https://" + serverHost + requestPath)
		if err != nil {
			return nil, err
		}
		items := jar.Cookies(u)
		for _, item := range items {
			key := item.Name + "\x00" + item.Path + "\x00" + item.Domain
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			saved = append(saved, Cookie{
				Name:     item.Name,
				Value:    item.Value,
				Path:     item.Path,
				Domain:   item.Domain,
				Expires:  item.Expires,
				Secure:   item.Secure,
				HTTPOnly: item.HttpOnly,
			})
		}
	}
	return &Data{
		ServerHost: serverHost,
		Cookies:    saved,
	}, nil
}
