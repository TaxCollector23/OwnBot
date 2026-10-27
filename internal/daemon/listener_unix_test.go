//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package daemon

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixSocketPermissionsAndOwnedCleanup(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ob-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "ownbot.sock")
	ln, cleanup, err := listenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("socket mode=%o, want 600", fi.Mode().Perm())
	}
	if _, _, err = listenUnix(path); err == nil {
		t.Fatal("active socket was replaced")
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("active socket was removed: %v", err)
	}
	_ = conn.Close()
	_ = ln.Close()
	cleanup()
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket remains after owned cleanup: %v", err)
	}
}
func TestUnixSocketRefusesRegularFileAndRemovesOwnedStaleSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ob-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "ownbot.sock")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listenUnix(path); err == nil {
		t.Fatal("regular file replaced")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "keep" {
		t.Fatalf("regular file changed: %q %v", b, err)
	}
	_ = os.Remove(path)
	oldListener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	old := oldListener.(*net.UnixListener)
	old.SetUnlinkOnClose(false)
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(path); err != nil {
		t.Fatalf("expected stale socket file: %v", err)
	}
	fresh, cleanup, err := listenUnix(path)
	if err != nil {
		t.Fatalf("stale socket was not safely removed: %v", err)
	}
	cleanup()
	_ = fresh.Close()
}
