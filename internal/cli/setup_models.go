package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const gibibyte = uint64(1024 * 1024 * 1024)

type setupModelCandidate struct {
	ID                  string
	Name                string
	SizeBytes           uint64
	EstimatedMemory     uint64
	Runtime             string
	Provider            string
	BaseURL             string
	ToolUseSupported    bool
	Installed           bool
	LocalVerified       bool
	DownloadDescription string
}

func ollamaShortlist() []setupModelCandidate {
	baseURL := "http://127.0.0.1:11434/v1"
	items := []struct {
		id   string
		size uint64
	}{
		{"qwen3:0.6b", 523_000_000},
		{"qwen3:1.7b", 1_400_000_000},
		{"qwen3:4b", 2_500_000_000},
		{"qwen3:8b", 5_200_000_000},
	}
	result := make([]setupModelCandidate, 0, len(items))
	for _, item := range items {
		result = append(result, setupModelCandidate{ID: item.id, Name: item.id, SizeBytes: item.size, EstimatedMemory: item.size * 3 / 2, Runtime: "Ollama", Provider: "ollama", BaseURL: baseURL, ToolUseSupported: true, DownloadDescription: "qwen3 models in Ollama's local library advertise tool calling; OwnBot still runs its own capability probe before creating an agent"})
	}
	return result
}

func lmStudioShortlist() []setupModelCandidate {
	baseURL := "http://127.0.0.1:1234/v1"
	items := []struct {
		id, name string
		size     uint64
	}{
		{"qwen/qwen3-4b-2507", "Qwen3 4B Instruct 2507", 2_300_000_000},
		{"ibm/granite-4-micro", "Granite 4 Micro", 2_250_000_000},
	}
	result := make([]setupModelCandidate, 0, len(items))
	for _, item := range items {
		result = append(result, setupModelCandidate{ID: item.id, Name: item.name, SizeBytes: item.size, EstimatedMemory: item.size * 4 / 3, Runtime: "LM Studio", Provider: "lm_studio", BaseURL: baseURL, ToolUseSupported: true, DownloadDescription: "local model catalog entry with documented tool-use support; OwnBot still runs its own capability probe before creating an agent"})
	}
	return result
}

func installedCandidate(model localModel) setupModelCandidate {
	return setupModelCandidate{ID: model.ID, Name: model.ID, SizeBytes: uint64(maxInt64(model.SizeBytes, 0)), EstimatedMemory: uint64(maxInt64(model.SizeBytes, 0)) * 3 / 2, Runtime: model.Runtime, Provider: model.Provider, BaseURL: model.BaseURL, ToolUseSupported: model.ToolUse, Installed: true, LocalVerified: model.LocalVerified}
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}

func recommendModel(candidates []setupModelCandidate, availableRAM, freeVRAM, availableDisk uint64, haveRAM, haveDisk, unifiedMemory bool) (setupModelCandidate, string, bool) {
	if !haveRAM || availableRAM == 0 {
		return setupModelCandidate{}, "available RAM telemetry is unavailable", false
	}
	if !haveDisk || availableDisk == 0 {
		return setupModelCandidate{}, "free storage telemetry is unavailable for the model download location", false
	}
	budget := availableRAM
	if !unifiedMemory {
		budget += freeVRAM
	}
	var best setupModelCandidate
	for _, candidate := range candidates {
		if !candidate.ToolUseSupported || candidate.SizeBytes == 0 || candidate.EstimatedMemory == 0 {
			continue
		}
		if candidate.EstimatedMemory > budget || candidate.SizeBytes+candidate.SizeBytes/5 > availableDisk {
			continue
		}
		if best.ID == "" || candidate.SizeBytes > best.SizeBytes {
			best = candidate
		}
	}
	if best.ID == "" {
		return setupModelCandidate{}, "no shortlisted model fits the reported available memory and free storage", false
	}
	reason := fmt.Sprintf("Largest shortlisted local model whose %s memory estimate and 20%% download-space reserve fit the currently reported resources. This fit estimate is not an optimality or answer-quality claim.", formatBytes(best.EstimatedMemory))
	if best.ID == "qwen3:1.7b" {
		reason += " OwnBot's 1.7B capability check has passed on a separate 16 GB Linux CPU runner; this host has not been benchmarked."
	}
	return best, reason, true
}

func formatBytes(value uint64) string {
	if value >= gibibyte {
		return fmt.Sprintf("%.1f GiB", float64(value)/float64(gibibyte))
	}
	return fmt.Sprintf("%.0f MiB", float64(value)/(1024*1024))
}

func modelStorePath(runtimeName string) string {
	if runtimeName == "Ollama" {
		if override := strings.TrimSpace(os.Getenv("OLLAMA_MODELS")); override != "" {
			return override
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	switch runtimeName {
	case "Ollama":
		return filepath.Join(home, ".ollama", "models")
	case "LM Studio":
		return filepath.Join(home, ".lmstudio", "models")
	case "Jan":
		if dirs := janDataDirectories(); len(dirs) > 0 {
			return dirs[0]
		}
	}
	return home
}

func pullOllamaModel(ctx context.Context, client *http.Client, baseURL, model string, stdout io.Writer) error {
	requestBody, _ := json.Marshal(map[string]any{"name": model, "stream": true})
	address := strings.TrimSuffix(strings.TrimSuffix(baseURL, "/v1"), "/") + "/api/pull"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, strings.NewReader(string(requestBody)))
	if err != nil {
		return errors.New("could not prepare the local model download")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("model download cancelled; rerun setup to resume or choose another model")
		}
		return errors.New("could not reach the local Ollama API; check that Ollama is running")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Ollama model download failed with HTTP %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	finished := false
	for scanner.Scan() {
		if ctx.Err() != nil {
			return errors.New("model download cancelled; rerun setup to resume or choose another model")
		}
		var progress struct {
			Status    string `json:"status"`
			Error     string `json:"error"`
			Total     uint64 `json:"total"`
			Completed uint64 `json:"completed"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &progress); err != nil {
			return errors.New("Ollama returned an invalid download progress update")
		}
		if progress.Error != "" {
			return fmt.Errorf("Ollama model download failed: %s", progress.Error)
		}
		if progress.Status == "success" {
			finished = true
			break
		}
		if progress.Total > 0 {
			percent := progress.Completed * 100 / progress.Total
			if percent > 100 {
				percent = 100
			}
			_, _ = fmt.Fprintf(stdout, "\r  %s · %3d%% · %s / %s", progress.Status, percent, formatBytes(progress.Completed), formatBytes(progress.Total))
		} else if progress.Status != "" {
			_, _ = fmt.Fprintf(stdout, "\r  %s", progress.Status)
		}
	}
	_, _ = fmt.Fprintln(stdout)
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return errors.New("model download cancelled; rerun setup to resume or choose another model")
		}
		return errors.New("could not read Ollama model download progress")
	}
	if !finished {
		return errors.New("Ollama stopped the model download before it completed")
	}
	return nil
}

func downloadAndLoadLMStudio(ctx context.Context, modelID string, stdout, stderr io.Writer) error {
	lms, err := exec.LookPath("lms")
	if err != nil {
		return errors.New("LM Studio's lms command is unavailable; download a local model in LM Studio, then return to setup")
	}
	for _, args := range [][]string{{"get", modelID}, {"load", modelID}} {
		command := exec.CommandContext(ctx, lms, args...)
		command.Stdout, command.Stderr = stdout, stderr
		if err := command.Run(); err != nil {
			if ctx.Err() != nil {
				return errors.New("LM Studio model setup cancelled; rerun setup to resume")
			}
			return fmt.Errorf("LM Studio command `lms %s` failed; inspect its output and retry setup", strings.Join(args, " "))
		}
	}
	return nil
}
