//go:build !linux && !darwin && !windows

package cli

func availableMemory() (uint64, bool) { return 0, false }
