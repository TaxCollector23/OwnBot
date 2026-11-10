//go:build !windows

package hardware

// ReadWindowsMemoryInfo is kept available to the shared dispatcher on
// non-Windows targets, where it is never called.
func ReadWindowsMemoryInfo() MemoryInfo {
	return MemoryInfo{Kind: "physical RAM", Source: "Windows API unavailable on this operating system"}
}
