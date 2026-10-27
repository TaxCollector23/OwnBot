package server

import (
	"errors"
	"os"
	"runtime"
)

func privateDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("state directory must be a real private directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return errors.New("state directory must have private permissions (0700)")
	}
	return nil
}
func privateToken(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("API token must be a regular private file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return errors.New("API token must have private permissions (0600)")
	}
	return nil
}
