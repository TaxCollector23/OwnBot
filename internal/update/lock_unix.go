//go:build !windows

package update

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockUpdateFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX)
}

func unlockUpdateFile(file *os.File) {
	_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
