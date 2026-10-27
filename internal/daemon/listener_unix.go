//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func socketOwner(fi os.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

func checkSocketDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New("Unix socket parent is not a directory")
	}
	uid, ok := socketOwner(fi)
	if !ok {
		return errors.New("cannot verify Unix socket parent ownership")
	}
	mode := fi.Mode()
	if uid != uint32(os.Getuid()) && mode&os.ModeSticky == 0 {
		return errors.New("Unix socket parent must be owned by this user or sticky")
	}
	if mode.Perm()&0022 != 0 && mode&os.ModeSticky == 0 {
		return errors.New("Unix socket parent must not be group- or world-writable")
	}
	return nil
}

func listenUnix(path string) (net.Listener, func(), error) {
	if !filepath.IsAbs(path) {
		return nil, func() {}, errors.New("Unix socket path must be absolute")
	}
	if err := checkSocketDirectory(filepath.Dir(path)); err != nil {
		return nil, func() {}, err
	}
	old, err := os.Lstat(path)
	if err == nil {
		if old.Mode()&os.ModeSocket == 0 {
			return nil, func() {}, errors.New("refusing to replace a non-socket Unix path")
		}
		uid, ok := socketOwner(old)
		if !ok || uid != uint32(os.Getuid()) {
			return nil, func() {}, errors.New("refusing to replace a Unix socket not owned by this user")
		}
		conn, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return nil, func() {}, errors.New("Unix socket is already active")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, func() {}, fmt.Errorf("cannot verify stale Unix socket: %w", dialErr)
		}
		current, e := os.Lstat(path)
		if e != nil || !os.SameFile(old, current) {
			return nil, func() {}, errors.New("Unix socket changed while checking stale path")
		}
		if e = os.Remove(path); e != nil {
			return nil, func() {}, e
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, func() {}, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, func() {}, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, func() {}, err
	}
	created, err := os.Lstat(path)
	if err != nil {
		_ = ln.Close()
		return nil, func() {}, err
	}
	cleanup := func() {
		_ = ln.Close()
		if current, e := os.Lstat(path); e == nil && os.SameFile(created, current) {
			if uid, ok := socketOwner(current); ok && uid == uint32(os.Getuid()) {
				_ = os.Remove(path)
			}
		}
	}
	return ln, cleanup, nil
}
