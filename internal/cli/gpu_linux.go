//go:build linux

package cli

import (
	"os/exec"
	"strconv"
	"strings"
)

func gpuMemoryStatus() string {
	if out, err := exec.Command("nvidia-smi", "--query-gpu=name,memory.free", "--format=csv,noheader,nounits").Output(); err == nil {
		var rows []string
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			parts := strings.Split(line, ",")
			if len(parts) != 2 {
				continue
			}
			if _, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64); err == nil {
				rows = append(rows, strings.TrimSpace(parts[0])+": "+strings.TrimSpace(parts[1])+" MiB free")
			}
		}
		if len(rows) > 0 {
			return "GPU memory reported by nvidia-smi: " + strings.Join(rows, "; ")
		}
	}
	if out, err := exec.Command("rocm-smi", "--showmeminfo", "vram").Output(); err == nil {
		var rows []string
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(strings.ToLower(line), "vram free memory") || strings.Contains(strings.ToLower(line), "vram free") {
				rows = append(rows, strings.TrimSpace(line))
			}
		}
		if len(rows) > 0 {
			return "GPU memory reported by rocm-smi: " + strings.Join(rows, "; ")
		}
	}
	return "GPU memory: unavailable (no supported NVIDIA or AMD memory utility reported it)"
}
