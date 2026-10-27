//go:build !linux && !darwin

package cli

func gpuMemoryStatus() string { return "GPU memory: unavailable from this operating system interface" }
