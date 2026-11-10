//go:build windows

package cli

import "golang.org/x/sys/windows"

func availableDiskBytes(path string) (uint64, bool) {
	var available, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(windows.StringToUTF16Ptr(path), &available, &total, &totalFree); err != nil {
		return 0, false
	}
	return available, true
}
