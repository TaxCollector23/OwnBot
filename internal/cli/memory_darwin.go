//go:build darwin

package cli

import (
	"os/exec"
	"strconv"
	"strings"
)

func availableMemory() (uint64, bool) {
	pageOut, err := exec.Command("/usr/sbin/sysctl", "-n", "hw.pagesize").Output()
	if err != nil {
		return 0, false
	}
	pageSize, err := strconv.ParseUint(strings.TrimSpace(string(pageOut)), 10, 64)
	if err != nil || pageSize == 0 {
		return 0, false
	}
	stats, err := exec.Command("/usr/bin/vm_stat").Output()
	if err != nil {
		return 0, false
	}
	var pages uint64
	for _, line := range strings.Split(string(stats), "\n") {
		for _, key := range []string{"Pages free:", "Pages inactive:", "Pages speculative:"} {
			if strings.HasPrefix(line, key) {
				value := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, key)), ".")
				count, parseErr := strconv.ParseUint(value, 10, 64)
				if parseErr != nil {
					return 0, false
				}
				pages += count
			}
		}
	}
	if pages == 0 {
		return 0, false
	}
	return pages * pageSize, true
}
