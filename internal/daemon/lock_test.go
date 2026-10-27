package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStateLockBlocksSecondDaemonAndReleases(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	first, err := AcquireStateLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = AcquireStateLock(dir); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second state lock got %v, want ErrAlreadyRunning", err)
	}
	info, err := os.Stat(filepath.Join(dir, "ownbotd.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("lock mode=%o, want 600", info.Mode().Perm())
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireStateLock(dir)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "ownbotd.lock")); err != nil {
		t.Fatalf("persistent lock file was removed: %v", err)
	}
}

func TestStateLockRejectsSymlinkStateDirectory(t *testing.T) {
	base := t.TempDir()
	actual := filepath.Join(base, "actual")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := AcquireStateLock(alias); err == nil {
		t.Fatal("accepted symlink state directory")
	}
	if _, err := os.Lstat(filepath.Join(actual, "ownbotd.lock")); !os.IsNotExist(err) {
		t.Fatalf("lock was touched through state symlink: %v", err)
	}
}
