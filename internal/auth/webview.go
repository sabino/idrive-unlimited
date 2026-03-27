//go:build cgo

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"idrive-unlimited/internal/evs"
	"idrive-unlimited/internal/session"

	webview "github.com/webview/webview_go"
)

const loginURL = "https://www.idrive.com/idrive/login/loginForm"

type tokenLoginPayload struct {
	ServerAddress string `json:"serverAddress"`
	URL           string `json:"url"`
	RedirectURL   string `json:"redirect_url"`
	Token         string `json:"token"`
	SID           string `json:"sid"`
	RM            string `json:"rm"`
}

func Login(ctx context.Context) (*session.Data, error) {
	if err := ensureNativeLoginRuntime(); err != nil {
		return nil, err
	}
	payload, err := waitForTokenLogin(ctx)
	if err != nil {
		return nil, err
	}
	return exchangeTokenLogin(ctx, payload)
}

func waitForTokenLogin(ctx context.Context) (*tokenLoginPayload, error) {
	resultCh := make(chan *tokenLoginPayload, 1)
	errCh := make(chan error, 1)
	done := make(chan struct{})

	w := webview.New(false)
	if w == nil {
		return nil, fmt.Errorf("create native webview: failed")
	}
	defer w.Destroy()

	w.SetTitle("IDrive Login")
	w.SetSize(1120, 820, webview.HintNone)

	var once sync.Once
	complete := func(payload *tokenLoginPayload, err error) {
		once.Do(func() {
			if err != nil {
				errCh <- err
			} else {
				resultCh <- payload
			}
			select {
			case <-done:
			default:
				w.Dispatch(func() {
					w.Terminate()
				})
			}
		})
	}

	if err := w.Bind("idriveGatewayTokenReady", func(raw string) {
		var payload tokenLoginPayload
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			complete(nil, fmt.Errorf("decode token login payload: %w", err))
			return
		}
		if payload.Token == "" || payload.SID == "" {
			complete(nil, fmt.Errorf("received incomplete token login payload"))
			return
		}
		complete(&payload, nil)
	}); err != nil {
		return nil, fmt.Errorf("bind token callback: %w", err)
	}

	if err := w.Bind("idriveGatewayAuthError", func(message string) {
		if strings.TrimSpace(message) == "" {
			return
		}
		complete(nil, fmt.Errorf("%s", message))
	}); err != nil {
		return nil, fmt.Errorf("bind error callback: %w", err)
	}

	w.Init(authBootstrapJS)
	w.Navigate(loginURL)

	go func() {
		select {
		case <-ctx.Done():
			complete(nil, ctx.Err())
		case <-done:
		}
	}()

	w.Run()
	close(done)

	select {
	case payload := <-resultCh:
		return payload, nil
	case err := <-errCh:
		return nil, err
	default:
		return nil, fmt.Errorf("login window closed before token exchange completed")
	}
}

func exchangeTokenLogin(ctx context.Context, payload *tokenLoginPayload) (*session.Data, error) {
	if payload == nil {
		return nil, fmt.Errorf("token payload is nil")
	}
	if payload.URL == "" {
		return nil, fmt.Errorf("token payload is missing url")
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{
		Jar:     jar,
		Timeout: 60 * time.Second,
	}

	tokenURL, err := url.Parse(payload.URL)
	if err != nil {
		return nil, fmt.Errorf("parse token login url: %w", err)
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

	return evsClient.SessionData(), nil
}

const authBootstrapJS = `
(function () {
  if (window.__idriveGatewayAuthHookInstalled) {
    return;
  }
  window.__idriveGatewayAuthHookInstalled = true;

  let sent = false;

  async function tryTokenLogin() {
    if (sent) {
      return;
    }

    const path = window.location && window.location.pathname ? window.location.pathname : "";
    if (!path.startsWith("/idrive/home")) {
      return;
    }

    try {
      const res = await fetch("/idrive/home/getTokenLogin", {
        method: "POST",
        credentials: "include"
      });
      const text = await res.text();
      const payload = JSON.parse(text.trim());
      if (!payload || !payload.token || !payload.sid || !(payload.url || payload.redirect_url)) {
        return;
      }
      sent = true;
      window.idriveGatewayTokenReady(JSON.stringify(payload));
    } catch (err) {
      console.error("idrive gateway token exchange failed", err);
    }
  }

  const notify = () => {
    tryTokenLogin().catch(function (err) {
      const message = err && err.message ? err.message : String(err);
      window.idriveGatewayAuthError(message);
    });
  };

  setInterval(notify, 1000);
  window.addEventListener("load", notify);
  document.addEventListener("readystatechange", function () {
    if (document.readyState === "interactive" || document.readyState === "complete") {
      notify();
    }
  });
  notify();
})();
`
