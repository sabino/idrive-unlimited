package atomicfile

import (
	"os"
	"path/filepath"
)

// Write replaces a file privately and syncs its contents and parent directory.
func Write(name string, data []byte) error {
	dir := filepath.Dir(name)
	temp, err := os.CreateTemp(dir, ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(temp.Name(), name); err != nil {
		return err
	}
	return SyncDir(dir)
}
