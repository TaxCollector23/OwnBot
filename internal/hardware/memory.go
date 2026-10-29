package hardware

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// MemoryInfo contains host physical-memory measurements when the OS exposes them.
type MemoryInfo struct {
	TotalBytes     uint64 `json:"total_bytes,omitempty"`
	AvailableBytes uint64 `json:"available_bytes,omitempty"`
	Kind           string `json:"kind"`
	Source         string `json:"source,omitempty"`
}

func ReadMemoryInfo() MemoryInfo {
	switch runtime.GOOS {
	case "linux":
		total, available, err := ParseLinuxMeminfo("/proc/meminfo")
		if err != nil {
			return MemoryInfo{Kind: "physical RAM", Source: "unavailable"}
		}
		return MemoryInfo{TotalBytes: total, AvailableBytes: available, Kind: "physical RAM", Source: "/proc/meminfo"}
	case "darwin":
		totalOut, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return MemoryInfo{Kind: "physical RAM", Source: "sysctl unavailable"}
		}
		total, _ := strconv.ParseUint(strings.TrimSpace(string(totalOut)), 10, 64)
		// vm_stat reports pages. This available value is an estimate, not memory pressure.
		available := uint64(0)
		out, vmErr := exec.Command("vm_stat").Output()
		if vmErr == nil {
			pageSize := uint64(4096)
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "Mach Virtual Memory Statistics:") {
					if start := strings.Index(line, "(page size of "); start >= 0 {
						var size uint64
						if _, scanErr := fmt.Sscanf(line[start:], "(page size of %d bytes)", &size); scanErr == nil && size > 0 {
							pageSize = size
						}
					}
				}
				for _, key := range []string{"Pages free:", "Pages inactive:", "Pages speculative:"} {
					if strings.HasPrefix(line, key) {
						fields := strings.Fields(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(line, key)), "."))
						if len(fields) > 0 {
							if n, e := strconv.ParseUint(fields[0], 10, 64); e == nil {
								available += n * pageSize
							}
						}
					}
				}
			}
		}
		return MemoryInfo{TotalBytes: total, AvailableBytes: available, Kind: "physical RAM", Source: "sysctl hw.memsize; available estimate from vm_stat"}
	default:
		return MemoryInfo{Kind: "physical RAM", Source: "unavailable on this platform"}
	}
}

func ParseLinuxMeminfo(path string) (uint64, uint64, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	values := map[string]uint64{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		parts := strings.Fields(s.Text())
		if len(parts) < 2 {
			continue
		}
		n, parseErr := strconv.ParseUint(parts[1], 10, 64)
		if parseErr == nil {
			values[strings.TrimSuffix(parts[0], ":")] = n * 1024
		}
	}
	if err := s.Err(); err != nil {
		return 0, 0, err
	}
	if values["MemTotal"] == 0 || values["MemAvailable"] == 0 {
		return values["MemTotal"], values["MemAvailable"], fmt.Errorf("memory values missing from meminfo")
	}
	return values["MemTotal"], values["MemAvailable"], nil
}
