//go:build !linux && !darwin

package cli

func availableMemory() (uint64, bool) { return 0, false }
