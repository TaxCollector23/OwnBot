//go:build windows

package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

var errLockBusy = errors.New("state lock is held")

func openStateLockFile(dir string) (*os.File, error) {
	dst, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !dst.IsDir() || dst.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("state path must be a real directory")
	}
	path := filepath.Join(dir, "ownbotd.lock")
	if st, statErr := os.Lstat(path); statErr == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return nil, errors.New("state lock path must be a regular file")
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("state lock path must be a regular file")
	}
	return f, nil
}

func lockOSFile(f *os.File) (func() error, error) {
	overlapped := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return nil, errLockBusy
	}
	if err != nil {
		return nil, err
	}
	return func() error { return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, overlapped) }, nil
}
