//go:build !darwin && !linux && !freebsd && !windows

package cli

func availableDiskBytes(string) (uint64, bool) { return 0, false }
