package server

import (
	"encoding/json"
	"net/http"
)

const testedHostMemoryBytes uint64 = 16 * 1024 * 1024 * 1024

type setupSuggestion struct {
	Model          string `json:"model"`
	InstallCommand string `json:"install_command"`
	Reason         string `json:"reason"`
	Evidence       string `json:"evidence"`
}

type setupRecommendations struct {
	HostMemoryBytes uint64            `json:"host_memory_bytes"`
	Suggestions     []setupSuggestion `json:"suggestions"`
	Reason          string            `json:"reason,omitempty"`
}

func recommendationsFor(memoryBytes uint64, goos string) setupRecommendations {
	result := setupRecommendations{HostMemoryBytes: memoryBytes, Suggestions: []setupSuggestion{}}
	if goos != "darwin" && goos != "linux" && goos != "windows" {
		if goos == "freebsd" {
			result.Reason = "No Ollama suggestion is tested for FreeBSD. Use ownbot setup for local llama.cpp-compatible runtime guidance."
		} else {
			result.Reason = "No tested model suggestion is available for this operating system. Run ownbot setup for supported local runtime guidance."
		}
		return result
	}
	if memoryBytes == 0 {
		result.Reason = "Host memory is unavailable; run ownbot inspect on the host."
		return result
	}
	if memoryBytes < testedHostMemoryBytes {
		result.Reason = "No tested suggestion is available for this reported memory tier. Run ownbot inspect and choose a local model to probe."
		return result
	}
	result.Suggestions = []setupSuggestion{{
		Model:          "qwen3:1.7b",
		InstallCommand: "ollama pull qwen3:1.7b",
		Reason:         "Tested candidate on a 16 GB host. Run ownbot setup and its local probe before using it; performance and answer quality on this host are unverified.",
		Evidence:       "https://github.com/TaxCollector23/OwnBot/actions/runs/40738088838",
	}}
	return result
}

func (s *Server) setupRecommendations(w http.ResponseWriter, r *http.Request) {
	memoryBytes, goos := s.setupHostInfo()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(recommendationsFor(memoryBytes, goos))
}
