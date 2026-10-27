package session

import (
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

// Jar delegates request-cookie selection to Go's jar and separately retains
// the Set-Cookie attributes needed for a lossless session-file round trip.
type Jar struct {
	mu      sync.Mutex
	jar     *cookiejar.Jar
	cookies map[string]Cookie
}

func NewJar() (*Jar, error) {
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, err
	}
	return &Jar{jar: jar, cookies: make(map[string]Cookie)}, nil
}

func (j *Jar) Cookies(u *url.URL) []*http.Cookie { return j.jar.Cookies(u) }

func (j *Jar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.jar.SetCookies(u, cookies)
	if u.Scheme != "https" && u.Scheme != "http" {
		return
	}
	host := strings.ToLower(u.Hostname())
	for _, c := range cookies {
		domain := strings.ToLower(strings.TrimPrefix(c.Domain, "."))
		if domain == "" {
			domain = host
		}
		if host != domain && (net.ParseIP(host) != nil || !strings.HasSuffix(host, "."+domain)) {
			continue
		}
		if suffix, _ := publicsuffix.PublicSuffix(domain); c.Domain != "" && domain == suffix && host != domain {
			continue
		}
		cookiePath := c.Path
		if !strings.HasPrefix(cookiePath, "/") {
			cookiePath = "/"
			if i := strings.LastIndex(u.Path, "/"); i > 0 {
				cookiePath = u.Path[:i]
			}
		}
		key := domain + "\x00" + cookiePath + "\x00" + c.Name
		expires := c.Expires
		if c.MaxAge > 0 {
			if int64(c.MaxAge) > int64(1<<63-1)/int64(time.Second) {
				expires = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
			} else {
				expires = time.Now().Add(time.Duration(c.MaxAge) * time.Second)
			}
		}
		if c.MaxAge < 0 || (!expires.IsZero() && !expires.After(time.Now())) {
			delete(j.cookies, key)
			continue
		}
		savedDomain := domain
		if c.Domain == "" || (domain == host && domainIsPublicSuffix(domain)) {
			savedDomain = ""
		}
		j.cookies[key] = Cookie{Name: c.Name, Value: c.Value, Path: cookiePath, Domain: savedDomain, OriginHost: host, Expires: expires, Secure: c.Secure, HTTPOnly: c.HttpOnly}
	}
}

func domainIsPublicSuffix(domain string) bool {
	suffix, _ := publicsuffix.PublicSuffix(domain)
	return suffix == domain
}

func (j *Jar) snapshot(serverHost string) []Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	host := strings.ToLower(serverHost)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	var out []Cookie
	for key, c := range j.cookies {
		if !c.Expires.IsZero() && !c.Expires.After(time.Now()) {
			delete(j.cookies, key)
			continue
		}
		if c.Domain == "" {
			if host != c.OriginHost {
				continue
			}
		} else if host != c.Domain && !strings.HasSuffix(host, "."+c.Domain) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(a, b int) bool {
		return out[a].OriginHost+out[a].Domain+out[a].Path+out[a].Name < out[b].OriginHost+out[b].Domain+out[b].Path+out[b].Name
	})
	return out
}
