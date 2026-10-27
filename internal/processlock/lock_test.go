package processlock

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestExclusiveLockAndCancellation(t *testing.T) {
	name := filepath.Join(t.TempDir(), "lock")
	first, err := Try(name)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := Try(name); !errors.Is(err, ErrLocked) {
		if other != nil {
			other.Close()
		}
		t.Fatal("lock was not exclusive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, name); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	first.Close()
	other, err := Try(name)
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
}
