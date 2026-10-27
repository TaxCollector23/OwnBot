//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package daemon

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

var errLockBusy = errors.New("state lock is held")

func openStateLockFile(dir string) (*os.File, error) {
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("state directory must be a real directory: %w", err)
	}
	dirFile := os.NewFile(uintptr(dfd), dir)
	defer dirFile.Close()
	st, err := dirFile.Stat()
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, errors.New("state path is not a directory")
	}
	if st.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("state directory permissions %o are accessible by group or others", st.Mode().Perm())
	}
	fd, err := unix.Openat(dfd, "ownbotd.lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "ownbotd.lock")
	st, err = f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, errors.New("state lock path is not a regular file")
	}
	if err = f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("secure state lock: %w", err)
	}
	return f, nil
}

func lockOSFile(f *os.File) (func() error, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return nil, errLockBusy
	}
	if err != nil {
		return nil, err
	}
	return func() error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }, nil
}
