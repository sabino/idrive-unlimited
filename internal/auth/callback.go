package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
)

func callbackNonce() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
func tokenCallback(raw, nonce string) (*tokenLoginPayload, error) {
	if len(raw) > 64<<10 {
		return nil, errors.New("login callback exceeds 64 KiB")
	}
	var payload tokenLoginPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, errors.New("invalid login callback")
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(payload.CallbackNonce), []byte(nonce)) != 1 || payload.Token == "" || payload.SID == "" {
		return nil, errors.New("untrusted or incomplete login callback")
	}
	return &payload, nil
}
