package daemon

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

var ErrAlreadyRunning = errors.New("an OwnBot daemon already holds this state directory")

// StateLock prevents a second daemon from migrating or recovering the same
// state directory. The lock file is intentionally kept after release so all
// processes contend on the same inode.
type StateLock struct {
	file   *os.File
	unlock func() error
	once   sync.Once
	err    error
}

func AcquireStateLock(dir string) (*StateLock, error) {
	if dir == "" {
		return nil, errors.New("state directory is required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	f, err := openStateLockFile(dir)
	if err != nil {
		return nil, fmt.Errorf("open state lock: %w", err)
	}
	unlock, err := lockOSFile(f)
	if err != nil {
		_ = f.Close()
		if errors.Is(err, errLockBusy) {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, dir)
		}
		return nil, fmt.Errorf("lock state directory: %w", err)
	}
	return &StateLock{file: f, unlock: unlock}, nil
}

func (l *StateLock) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.unlock != nil {
			l.err = errors.Join(l.err, l.unlock())
		}
		if l.file != nil {
			l.err = errors.Join(l.err, l.file.Close())
		}
	})
	return l.err
}
