// Package atomicfile writes files atomically: a temporary file in the same
// directory is written, flushed and then renamed over the target.
//
// Every manifest and lockfile write goes through here. A half-written cppm.lock
// is worse than no cppm.lock, and rename(2) within a directory is atomic on all
// platforms cppm supports.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write creates or replaces path with data, atomically.
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()

	// On any failure past this point the temp file must not be left behind.
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	tmp = "" // renamed; nothing to clean up
	return nil
}

// WriteString is Write for string data with default file permissions.
func WriteString(path, data string) error {
	return Write(path, []byte(data), 0o644)
}
