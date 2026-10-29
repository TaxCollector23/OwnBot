package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// withUpdateLock serializes every operation that can alter the active or
// previous release. The OS lock survives process crashes and is shared by
// independent CLI and service processes using the same data directory.
func withUpdateLock(dataDir string, operation func() error) error {
	if err := secureDirectory(dataDir); err != nil {
		return fmt.Errorf("secure state directory: %w", err)
	}
	updatesDir := filepath.Join(dataDir, "updates")
	if err := secureDirectory(updatesDir); err != nil {
		return fmt.Errorf("secure update state directory: %w", err)
	}
	path := filepath.Join(updatesDir, ".operation.lock")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return errors.New("update lock is not a secure regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open update lock: %w", err)
	}
	defer file.Close()
	if runtime.GOOS != "windows" {
		if err := file.Chmod(0600); err != nil {
			return err
		}
	}
	if err := lockUpdateFile(file); err != nil {
		return fmt.Errorf("lock update state: %w", err)
	}
	defer unlockUpdateFile(file)
	return operation()
}
