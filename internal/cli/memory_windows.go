//go:build windows

package cli

import "ownbot/internal/hardware"

func availableMemory() (uint64, bool) {
	info := hardware.ReadMemoryInfo()
	return info.AvailableBytes, info.AvailableBytes > 0
}
