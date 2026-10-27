//go:build !windows

package processlock

import (
	"golang.org/x/sys/unix"
	"os"
)

func tryLock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
