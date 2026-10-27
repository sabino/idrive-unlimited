//go:build windows

package atomicfile

// Windows does not provide Unix directory fsync through os.File.Sync.
func SyncDir(string) error { return nil }
