package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLocalPasswordFile(t *testing.T) {
	name := filepath.Join(t.TempDir(), "password")
	for _, content := range []string{"", "first\nsecond", string(make([]byte, 4097))} {
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := localPassword("", name); err == nil {
			t.Fatal("accepted invalid password file")
		}
	}
	if err := os.WriteFile(name, []byte("a-private-password\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := localPassword("", name); err != nil || got != "a-private-password" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := localPassword("also supplied", name); err == nil {
		t.Fatal("accepted conflicting password options")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(name, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := localPassword("", name); err == nil {
			t.Fatal("accepted public password file")
		}
	}
}
