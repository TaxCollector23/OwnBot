package cli

import (
	"encoding/csv"
	"os/exec"
	"strconv"
	"strings"
)

type gpuInfo struct {
	Name      string `json:"name,omitempty"`
	VRAMBytes uint64 `json:"vram_bytes,omitempty"`
	FreeBytes uint64 `json:"free_vram_bytes,omitempty"`
	Source    string `json:"source"`
	Status    string `json:"status"`
}

func readGPUInfo() gpuInfo {
	cmd := exec.Command("nvidia-smi", "--query-gpu=name,memory.total,memory.free", "--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		return gpuInfo{Source: "nvidia-smi", Status: "no NVIDIA VRAM telemetry detected; other GPU memory is not measured"}
	}
	rows, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil || len(rows) == 0 {
		return gpuInfo{Source: "nvidia-smi", Status: "telemetry returned no GPU rows"}
	}
	var result gpuInfo
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		if len(row) < 3 {
			continue
		}
		names = append(names, strings.TrimSpace(row[0]))
		total, _ := strconv.ParseUint(strings.TrimSpace(row[1]), 10, 64)
		free, _ := strconv.ParseUint(strings.TrimSpace(row[2]), 10, 64)
		result.VRAMBytes += total * 1024 * 1024
		result.FreeBytes += free * 1024 * 1024
	}
	result.Name = strings.Join(names, ", ")
	result.Source = "nvidia-smi"
	result.Status = "NVIDIA VRAM reported; values aggregate all detected devices"
	return result
}
