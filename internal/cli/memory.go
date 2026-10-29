package cli

import "ownbot/internal/hardware"

type memoryInfo = hardware.MemoryInfo

func readMemoryInfo() memoryInfo { return hardware.ReadMemoryInfo() }

func parseLinuxMeminfo(path string) (uint64, uint64, error) {
	return hardware.ParseLinuxMeminfo(path)
}
