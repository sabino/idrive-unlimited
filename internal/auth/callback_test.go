package auth

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLoginCallbackRequiresPerWindowCapability(t *testing.T) {
	nonce, err := callbackNonce()
	if err != nil {
		t.Fatal(err)
	}
	payload := tokenLoginPayload{Token: "synthetic", SID: "synthetic", CallbackNonce: nonce}
	raw, _ := json.Marshal(payload)
	if _, err := tokenCallback(string(raw), nonce); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", `{"token":"other-account","sid":"other-session"}`, string(raw)} {
		expected := nonce
		if bad == string(raw) {
			expected = "different-window"
		}
		if _, err := tokenCallback(bad, expected); err == nil {
			t.Fatal("accepted a callback without this window's capability")
		}
	}
	if _, err := tokenCallback(strings.Repeat("x", (64<<10)+1), nonce); err == nil {
		t.Fatal("unbounded callback accepted")
	}
}
