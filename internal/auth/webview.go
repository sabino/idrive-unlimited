//go:build cgo

package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"idrive-unlimited/internal/session"

	webview "github.com/webview/webview_go"
)

const loginURL = "https://www.idrive.com/idrive/login/loginForm"

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
	nonce, err := callbackNonce()
	if err != nil {
		return nil, err
	}
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

	if err := w.Bind("idriveGatewayTokenReady", func(raw string) bool {
		payload, err := tokenCallback(raw, nonce)
		if err != nil {
			return false
		}
		complete(payload, nil)
		return true
	}); err != nil {
		return nil, fmt.Errorf("bind token callback: %w", err)
	}
	if err := w.Bind("idriveGatewayAuthError", func(raw string) bool {
		if len(raw) > 4096 {
			return false
		}
		var payload struct {
			Nonce string `json:"callback_nonce"`
		}
		if json.Unmarshal([]byte(raw), &payload) != nil || subtle.ConstantTimeCompare([]byte(payload.Nonce), []byte(nonce)) != 1 {
			return false
		}
		complete(nil, fmt.Errorf("native IDrive login failed; retry the login window"))
		return true
	}); err != nil {
		return nil, fmt.Errorf("bind error callback: %w", err)
	}
	encodedNonce, _ := json.Marshal(nonce)
	w.Init(strings.ReplaceAll(authBootstrapJS, "__IDRIVE_GATEWAY_NONCE__", string(encodedNonce)))

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

const authBootstrapJS = `
(function () {
  if (window.location.origin !== "https://www.idrive.com" || window.top !== window) { return; }
  const callbackNonce = __IDRIVE_GATEWAY_NONCE__;
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
    if (window.location.origin !== "https://www.idrive.com" || !path.startsWith("/idrive/home")) {
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
      payload.callback_nonce = callbackNonce;
      window.idriveGatewayTokenReady(JSON.stringify(payload));
    } catch (err) {
      console.error("idrive gateway token exchange failed");
    }
  }

  const notify = () => {
    tryTokenLogin().catch(function (err) {
      window.idriveGatewayAuthError(JSON.stringify({callback_nonce: callbackNonce}));
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
