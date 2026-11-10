//go:build windows

package hardware

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func ReadWindowsMemoryInfo() MemoryInfo {
	status := memoryStatusEx{}
	status.Length = uint32(unsafe.Sizeof(status))
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	if err := proc.Find(); err != nil {
		return MemoryInfo{Kind: "physical RAM", Source: "GlobalMemoryStatusEx unavailable"}
	}
	result, _, _ := proc.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 || status.TotalPhys == 0 || status.AvailPhys == 0 {
		return MemoryInfo{Kind: "physical RAM", Source: "GlobalMemoryStatusEx returned no usable values"}
	}
	return MemoryInfo{TotalBytes: status.TotalPhys, AvailableBytes: status.AvailPhys, Kind: "physical RAM", Source: "GlobalMemoryStatusEx"}
}
