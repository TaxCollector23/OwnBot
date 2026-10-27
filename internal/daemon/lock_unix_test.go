//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateLockRejectsInsecureDirectoryBeforeLockCreation(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireStateLock(dir); err == nil {
		t.Fatal("accepted group/other-accessible state directory")
	}
	if _, err := os.Lstat(filepath.Join(dir, "ownbotd.lock")); !os.IsNotExist(err) {
		t.Fatalf("created lock in insecure directory: %v", err)
	}
}

func TestStateLockRejectsSymlinkLockPathWithoutTouchingTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "ownbotd.lock")
	if err := os.Symlink(target, lockPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := AcquireStateLock(dir); err == nil {
		t.Fatal("accepted symlink lock path")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("target changed: %q", data)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("target mode changed to %o", info.Mode().Perm())
	}
}

func TestStateLockRejectsNonRegularLockPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "ownbotd.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireStateLock(dir); err == nil {
		t.Fatal("accepted directory as lock file")
	}
}
