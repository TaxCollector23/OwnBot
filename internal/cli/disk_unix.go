//go:build darwin || linux || freebsd

package cli

import (
	"os"
	"path/filepath"
	"syscall"
)

func availableDiskBytes(path string) (uint64, bool) {
	for candidate := filepath.Clean(path); candidate != "." && candidate != string(filepath.Separator); candidate = filepath.Dir(candidate) {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		var stats syscall.Statfs_t
		if err := syscall.Statfs(candidate, &stats); err == nil {
			return uint64(stats.Bavail) * uint64(stats.Bsize), true
		}
	}
	return 0, false
}
