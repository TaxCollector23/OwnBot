//go:build linux

package cli

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

func availableMemory() (uint64, bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) >= 2 && fields[0] == "MemAvailable:" {
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			return kb * 1024, err == nil
		}
	}
	return 0, false
}
