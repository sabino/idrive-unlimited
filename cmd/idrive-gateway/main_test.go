package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoopbackOnly(t *testing.T) {
	for _, addr := range []string{":8787", "0.0.0.0:8787", "[::]:8787", "192.0.2.1:8787", "example.com:8787", "127.0.0.1:bad", "127.0.0.1:65536"} {
		if _, err := loopbackAddress(addr); err == nil {
			t.Errorf("accepted %s", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8787", "127.0.0.2:0", "[::1]:8787", "localhost:8787"} {
		if _, err := loopbackAddress(addr); err != nil {
			t.Errorf("rejected %s: %v", addr, err)
		}
	}
	if err := runServe(context.Background(), []string{"--listen", "0.0.0.0:8787", "--config", filepath.Join(t.TempDir(), "missing")}); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("invalid bind did not fail before auth: %v", err)
	}
}
func TestNoninteractiveSessionErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session.json")
	if _, _, err := ensureClient(context.Background(), file, false); err == nil || !strings.Contains(err.Error(), "login") {
		t.Fatalf("got %v", err)
	}
	os.WriteFile(file, []byte("invalid json"), 0600)
	if _, _, err := ensureClient(context.Background(), file, false); err == nil || !strings.Contains(err.Error(), "decode session") {
		t.Fatalf("lost session error: %v", err)
	}
}
