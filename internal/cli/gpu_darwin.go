//go:build darwin

package cli

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

func gpuMemoryStatus() string {
	if runtime.GOARCH == "arm64" {
		return "GPU memory: unified with system RAM on Apple silicon; separate VRAM is not reported"
	}
	if out, err := exec.Command("nvidia-smi", "--query-gpu=name,memory.free", "--format=csv,noheader,nounits").Output(); err == nil {
		var rows []string
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			parts := strings.Split(line, ",")
			if len(parts) == 2 {
				if _, e := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64); e == nil {
					rows = append(rows, strings.TrimSpace(parts[0])+": "+strings.TrimSpace(parts[1])+" MiB free")
				}
			}
		}
		if len(rows) > 0 {
			return "GPU memory reported by nvidia-smi: " + strings.Join(rows, "; ")
		}
	}
	return "GPU memory: unavailable (no supported GPU memory utility reported it)"
}
