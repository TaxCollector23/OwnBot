//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package daemon

import (
	"errors"
	"os"
)

var errLockBusy = errors.New("state lock is held")

func openStateLockFile(string) (*os.File, error) {
	return nil, errors.New("state directory locking is unsupported on this operating system")
}

func lockOSFile(*os.File) (func() error, error) {
	return nil, errors.New("state directory locking is unsupported on this operating system")
}
