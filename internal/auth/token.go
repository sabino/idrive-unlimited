package auth

import (
	"context"
	"errors"
	"fmt"
	"idrive-unlimited/internal/evs"
	"idrive-unlimited/internal/session"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type tokenLoginPayload struct {
	ServerAddress string `json:"serverAddress"`
	URL           string `json:"url"`
	RedirectURL   string `json:"redirect_url"`
	Token         string `json:"token"`
	SID           string `json:"sid"`
	RM            string `json:"rm"`
}

func exchangeTokenLogin(ctx context.Context, payload *tokenLoginPayload) (*session.Data, error) {
	if payload == nil {
		return nil, fmt.Errorf("token payload is nil")
	}
	tokenURL, err := tokenEndpoint(payload)
	if err != nil {
		return nil, err
	}

	jar, err := session.NewJar()
	if err != nil {
		return nil, err
	}

	client := &http.Client{
		Jar:     jar,
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many authentication redirects")
			}
			return validateTokenURL(req.URL)
		},
	}

	query := tokenURL.Query()
	query.Set("token", payload.Token)
	query.Set("sid", payload.SID)
	rm := payload.RM
	if rm == "" {
		rm = "null"
	}
	query.Set("rm", rm)
	query.Set("content_type", "img")
	tokenURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			err = requestErr.Err
		}
		return nil, fmt.Errorf("exchange token login: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("exchange token login: unexpected status %s", resp.Status)
	}

	serverHost := payload.ServerAddress
	if serverHost == "" {
		serverHost = tokenURL.Host
	}

	sessionData, err := session.FromCookieJar(serverHost, jar)
	if err != nil {
		return nil, err
	}

	evsClient, err := evs.NewClientFromSession(sessionData)
	if err != nil {
		return nil, err
	}
	if err := evsClient.Validate(ctx); err != nil {
		return nil, fmt.Errorf("validate exchanged EVS session: %w", err)
	}

	return evsClient.SessionData()
}

func validateTokenURL(u *url.URL) error {
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") || (host != "idrive.com" && !strings.HasSuffix(host, ".idrive.com")) {
		return fmt.Errorf("authentication URL must use HTTPS on an IDrive host")
	}
	return nil
}
func tokenEndpoint(payload *tokenLoginPayload) (*url.URL, error) {
	raw := payload.URL
	if raw == "" {
		raw = payload.RedirectURL
	}
	if raw == "" {
		return nil, fmt.Errorf("token payload is missing url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid authentication URL")
	}
	if err := validateTokenURL(u); err != nil {
		return nil, err
	}
	if payload.ServerAddress != "" {
		server, err := url.Parse("https://" + payload.ServerAddress)
		if err != nil || server.Host != payload.ServerAddress || server.Path != "" {
			return nil, fmt.Errorf("invalid authentication server")
		}
		if err := validateTokenURL(server); err != nil {
			return nil, err
		}
	}
	return u, nil
}
