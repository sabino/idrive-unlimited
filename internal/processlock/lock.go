// Package processlock provides an exclusive OS lock, released on process exit.
package processlock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

var ErrLocked = errors.New("process lock held")

type Lock struct{ file *os.File }

func Try(name string) (*Lock, error) {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = tryLock(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("another process owns %s: %w (%v)", name, ErrLocked, err)
	}
	return &Lock{f}, nil
}
func Acquire(ctx context.Context, name string) (*Lock, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lock, err := Try(name)
		if !errors.Is(err, ErrLocked) {
			return lock, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
func (l *Lock) Close() error { return l.file.Close() }
