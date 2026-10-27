package evs

import (
	"io"
	"strings"
	"testing"
)

func TestRejectServerURLInjection(t *testing.T) {
	for _, host := range []string{"user:pass@host.idrive.com", "host.idrive.com/path", "host.idrive.com?q=1", "host.idrive.com#fragment", "https://host.idrive.com", "host.idrive.com/../other"} {
		if _, err := NewClient(host, nil); err == nil {
			t.Errorf("accepted %q", host)
		}
	}
	if _, err := NewClient("evs.idrive.com", nil); err != nil {
		t.Fatal(err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestAPIResponseLimitAndRedaction(t *testing.T) {
	if _, err := readAPIResponse(io.LimitReader(zeros{}, (64<<20)+1)); err == nil {
		t.Fatal("accepted oversized response")
	}
	if err := checkHTTPStatus(500, []byte("secret-cookie")); err == nil || strings.Contains(err.Error(), "secret-cookie") {
		t.Fatalf("raw response disclosed: %v", err)
	}
}
