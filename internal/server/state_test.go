package server

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStateDirectoryAndTokenMustBePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); err == nil {
		t.Fatal("public state directory accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(dir, "api.token")
	if err := os.WriteFile(token, []byte("not a token"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); err == nil {
		t.Fatal("public token accepted")
	}
}
