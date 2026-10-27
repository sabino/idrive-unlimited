package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTokenURLValidation(t *testing.T) {
	for _, raw := range []string{"http://evs.idrive.com/login", "https://idrive.com.attacker.invalid/login", "https://attacker@evs.idrive.com/login", "https://evs.idrive.com:444/login", "javascript:alert(1)", "/relative"} {
		if _, err := tokenEndpoint(&tokenLoginPayload{URL: raw}); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	u, err := tokenEndpoint(&tokenLoginPayload{RedirectURL: "https://evs.idrive.com/login", ServerAddress: "evs.idrive.com"})
	if err != nil || u.Host != "evs.idrive.com" {
		t.Fatalf("redirect_url fallback failed: %v", err)
	}
	if _, err := tokenEndpoint(&tokenLoginPayload{URL: "https://evs.idrive.com/login", ServerAddress: "attacker.invalid"}); err == nil {
		t.Fatal("accepted untrusted session server")
	}
}

func TestTokenTransportErrorsDoNotDiscloseCredentials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := exchangeTokenLogin(ctx, &tokenLoginPayload{URL: "https://evs.idrive.com/login", Token: "private-token", SID: "private-session"})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "private-session") {
		t.Fatal("token transport error disclosed credentials")
	}
}
