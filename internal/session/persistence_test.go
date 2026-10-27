package session

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistOnlyCurrentSessionRevision(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "session.json"))
	first := &Data{ServerHost: "evs.idrive.com", ID: "first"}
	if err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	stale := *first
	first.Cookies = []Cookie{{Name: "SESSION", Value: "refreshed"}}
	if err := store.SaveCurrent(first); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCurrent(&stale); !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("stale writer accepted: %v", err)
	}
	different := &Data{ServerHost: "evs.idrive.com", ID: "new-login"}
	if err := store.Save(different); err != nil {
		t.Fatal(err)
	}
	first.SavedAt = time.Now()
	if err := store.SaveCurrent(first); !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("old login overwrote new login: %v", err)
	}
	loaded, _ := store.Load()
	if loaded.ID != "new-login" {
		t.Fatal("new login lost")
	}
}
