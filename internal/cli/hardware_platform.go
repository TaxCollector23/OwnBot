package cli

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func readCPUName() string {
	if runtime.GOOS == "linux" {
		if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(strings.ToLower(line), "model name") || strings.HasPrefix(strings.ToLower(line), "hardware") {
					if i := strings.IndexByte(line, ':'); i >= 0 {
						return strings.TrimSpace(line[i+1:])
					}
				}
			}
		}
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			return strings.TrimSpace(string(out))
		}
		if runtime.GOARCH == "arm64" {
			return "Apple Silicon (model name unavailable)"
		}
	}
	if out, err := exec.Command("uname", "-p").Output(); err == nil && strings.TrimSpace(string(out)) != "unknown" {
		return strings.TrimSpace(string(out))
	}
	return "model name unavailable"
}

func unifiedMemoryDescription() string {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return "Apple Silicon uses system unified memory; GPU allocation is not separately measured"
	}
	return "not detected; physical RAM and GPU VRAM are reported separately when available"
}
