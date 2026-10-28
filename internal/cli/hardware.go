package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"net/url"
	"runtime"
	"strings"
	"time"
)

// HardwareReport keeps measured system facts separate from model capability
// evidence. Probe success is not an accuracy or safety guarantee.
type HardwareReport struct {
	Platform                        string             `json:"platform"`
	Architecture                    string             `json:"architecture"`
	CPU                             string             `json:"cpu,omitempty"`
	Memory                          memoryInfo         `json:"memory"`
	UnifiedMemory                   string             `json:"unified_memory"`
	GPU                             gpuInfo            `json:"gpu"`
	InstalledModels                 []localModel       `json:"installed_local_models"`
	SavedModelProbes                []savedProbeReport `json:"saved_model_probes,omitempty"`
	BenchmarkResults                []hardwareProbe    `json:"benchmark_results,omitempty"`
	PerformanceRecommendation       *hardwareProbe     `json:"performance_recommendation,omitempty"`
	PerformanceRecommendationReason string             `json:"performance_recommendation_reason"`
	QualityRecommendation           string             `json:"quality_recommendation"`
	DaemonError                     string             `json:"daemon_error,omitempty"`
}

type savedProbeReport struct {
	ModelConfigID string         `json:"model_config_id"`
	Model         string         `json:"model"`
	Provider      string         `json:"provider"`
	ProbedAt      string         `json:"probed_at,omitempty"`
	Probe         map[string]any `json:"probe"`
	Pass          bool           `json:"pass"`
	Note          string         `json:"note"`
}

type hardwareProbe struct {
	ModelConfigID string         `json:"model_config_id"`
	Model         string         `json:"model"`
	Provider      string         `json:"provider"`
	LatencyMillis int64          `json:"latency_ms"`
	Probe         map[string]any `json:"probe"`
	Pass          bool           `json:"pass"`
	Note          string         `json:"note"`
}

type hardwareModelResult struct {
	Probe map[string]any `json:"probe"`
}

// runHardwareChecks is the local, read-only hardware inspection entry point.
// It contacts the local daemon only when a valid token is available. Model
// probes are explicitly opt-in with --benchmark.
func runHardwareChecks(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("inspect hardware", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL, dataDir := commonFlags(fs)
	benchmark := fs.Bool("benchmark", false, "explicitly run capability probes against configured local models")
	modelFilter := fs.String("model", "", "benchmark only this exact model name or model config ID")
	maxProbeSeconds := fs.Int("max-probe-seconds", 120, "maximum time for each explicit model probe")
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("inspect hardware does not accept positional arguments")
	}
	if *maxProbeSeconds < 1 || *maxProbeSeconds > 600 {
		return errors.New("--max-probe-seconds must be between 1 and 600")
	}

	report := HardwareReport{
		Platform:                        runtime.GOOS,
		Architecture:                    runtime.GOARCH,
		CPU:                             readCPUName(),
		Memory:                          readMemoryInfo(),
		UnifiedMemory:                   unifiedMemoryDescription(),
		GPU:                             readGPUInfo(),
		InstalledModels:                 detectLocalModels(context.Background()),
		SavedModelProbes:                []savedProbeReport{},
		BenchmarkResults:                []hardwareProbe{},
		PerformanceRecommendationReason: "No performance recommendation: run --benchmark to compare observed capability and latency for configured local models.",
		QualityRecommendation:           "unavailable: no task-quality evaluation has been run",
	}
	if report.InstalledModels == nil {
		report.InstalledModels = []localModel{}
	}
	if *modelFilter != "" {
		filtered := report.InstalledModels[:0]
		for _, m := range report.InstalledModels {
			if strings.EqualFold(m.ID, *modelFilter) {
				filtered = append(filtered, m)
			}
		}
		report.InstalledModels = filtered
	}

	if !daemonHealthy(*apiURL) {
		report.DaemonError = "local API is unavailable or did not identify itself as OwnBot; no authenticated request was sent"
		return writeJSON(stdout, report)
	}
	client, err := clientFor(*apiURL, *dataDir)
	if err != nil {
		report.DaemonError = err.Error()
		return writeJSON(stdout, report)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var configs []modelConfig
	if err := client.Do(ctx, "GET", "/api/models", nil, &configs); err != nil {
		report.DaemonError = err.Error()
		return writeJSON(stdout, report)
	}
	configuredEndpoints := make([]localEndpoint, 0, len(configs))
	for _, config := range configs {
		if !isLocalModelConfig(config) {
			continue
		}
		base := config.BaseURL
		if strings.TrimSpace(base) == "" {
			base = "http://127.0.0.1:11434/v1"
		}
		configuredEndpoints = append(configuredEndpoints, localEndpoint{Runtime: config.Provider, Provider: config.Provider, BaseURL: base, Ollama: config.Provider == "ollama"})
	}
	if len(configuredEndpoints) > 0 {
		client := directHTTPClient(time.Second)
		for _, found := range detectLocalModelsAt(context.Background(), client, configuredEndpoints) {
			if !hasInstalledModel(report.InstalledModels, found) {
				report.InstalledModels = append(report.InstalledModels, found)
			}
		}
	}
	for _, config := range configs {
		if !isLocalModelConfig(config) {
			continue
		}
		if *modelFilter != "" && !strings.EqualFold(config.ID, *modelFilter) && !strings.EqualFold(config.Model, *modelFilter) && !strings.EqualFold(config.Name, *modelFilter) {
			continue
		}
		if config.LastProbe != nil {
			passed, note := modelProbePass(config.LastProbe)
			report.SavedModelProbes = append(report.SavedModelProbes, savedProbeReport{ModelConfigID: config.ID, Model: config.Model, Provider: config.Provider, ProbedAt: config.ProbedAt, Probe: config.LastProbe, Pass: passed, Note: note + "; saved result has no latency measurement"})
		}
		if !*benchmark {
			continue
		}
		if !hasInstalledConfiguredModel(report.InstalledModels, config) {
			report.BenchmarkResults = append(report.BenchmarkResults, hardwareProbe{ModelConfigID: config.ID, Model: config.Model, Provider: config.Provider, Note: "not benchmarked: model was not found in the local provider's installed-model list"})
			continue
		}
		probeCtx, probeCancel := context.WithTimeout(context.Background(), time.Duration(*maxProbeSeconds)*time.Second)
		started := time.Now()
		var result hardwareModelResult
		probeErr := client.Do(probeCtx, "POST", "/api/models/"+urlPath(config.ID)+"/probe", map[string]any{}, &result)
		latency := time.Since(started).Milliseconds()
		probeCancel()
		if probeErr != nil {
			report.BenchmarkResults = append(report.BenchmarkResults, hardwareProbe{ModelConfigID: config.ID, Model: config.Model, Provider: config.Provider, LatencyMillis: latency, Pass: false, Note: "probe failed: " + probeErr.Error()})
			continue
		}
		passed, note := modelProbePass(result.Probe)
		report.BenchmarkResults = append(report.BenchmarkResults, hardwareProbe{ModelConfigID: config.ID, Model: config.Model, Provider: config.Provider, LatencyMillis: latency, Probe: result.Probe, Pass: passed, Note: note})
	}
	if *benchmark {
		var best *hardwareProbe
		for i := range report.BenchmarkResults {
			candidate := &report.BenchmarkResults[i]
			if candidate.Pass && (best == nil || candidate.LatencyMillis < best.LatencyMillis) {
				best = candidate
			}
		}
		if best != nil {
			copy := *best
			report.PerformanceRecommendation = &copy
			report.PerformanceRecommendationReason = "Fastest configured local model that passed this invocation's capability probe. This is a task-agnostic capability and latency comparison, not a task-quality or hallucination rating; model memory requirements are unknown."
		} else {
			report.PerformanceRecommendationReason = "No performance recommendation: no configured local model passed every capability check during this benchmark."
		}
	}
	return writeJSON(stdout, report)
}

func hasInstalledModel(installed []localModel, candidate localModel) bool {
	for _, model := range installed {
		if strings.EqualFold(model.Provider, candidate.Provider) && strings.EqualFold(model.ID, candidate.ID) && strings.TrimRight(model.BaseURL, "/") == strings.TrimRight(candidate.BaseURL, "/") {
			return true
		}
	}
	return false
}

func hasInstalledConfiguredModel(installed []localModel, config modelConfig) bool {
	for _, model := range installed {
		if strings.EqualFold(model.Provider, config.Provider) && strings.EqualFold(model.ID, config.Model) {
			return true
		}
	}
	return false
}

func modelProbePass(probe map[string]any) (bool, string) {
	if len(probe) == 0 {
		return false, "no capability result was returned"
	}
	checks := []string{"json_output", "single_tool", "tool_choice", "argument_accuracy"}
	for _, key := range checks {
		value, ok := probe[key].(bool)
		if !ok || !value {
			return false, "failed or missing capability check: " + key
		}
	}
	if restricted, ok := probe["restricted"].(bool); !ok || restricted {
		return false, "model is marked restricted"
	}
	return true, "all recorded capability checks passed"
}

func isLocalModelProvider(provider string) bool {
	switch provider {
	case "ollama", "lm_studio", "openai_compatible":
		return true
	default:
		return false
	}
}

func isLocalModelConfig(config modelConfig) bool {
	if !isLocalModelProvider(config.Provider) {
		return false
	}
	base := strings.TrimSpace(config.BaseURL)
	if base == "" {
		switch config.Provider {
		case "ollama":
			base = "http://127.0.0.1:11434/v1"
		case "lm_studio":
			base = "http://127.0.0.1:1234/v1"
		default:
			return false
		}
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}
