// Command probe runs the production model capability probe against one local
// OpenAI-compatible runtime without creating daemon state or model records.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"ownbot/internal/models"
)

type result struct {
	Runtime      string             `json:"runtime"`
	Model        string             `json:"model"`
	ElapsedMS    int64              `json:"elapsed_ms"`
	Probe        models.ProbeResult `json:"probe"`
	Pass         bool               `json:"pass"`
	FailureClass string             `json:"failure_class,omitempty"`
	FailureStage string             `json:"failure_stage"`
	ErrorType    string             `json:"error_type,omitempty"`
}

func main() {
	runtimeName := flag.String("runtime", "", "runtime name recorded in evidence")
	model := flag.String("model", "", "exact model identifier served by the runtime")
	baseURL := flag.String("base-url", "", "OpenAI-compatible API root")
	flag.Parse()

	out := result{Runtime: *runtimeName, Model: *model, FailureStage: "probe", FailureClass: "probe_or_transport_failure"}
	if strings.TrimSpace(*runtimeName) == "" || strings.TrimSpace(*model) == "" || strings.TrimSpace(*baseURL) == "" || flag.NArg() != 0 {
		out.FailureStage = "arguments"
		out.ErrorType = "invalid_arguments"
		writeResult(out)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	client := &models.OpenAICompatible{
		BaseURL: strings.TrimRight(*baseURL, "/"),
		Client:  &http.Client{Timeout: 4 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	started := time.Now()
	probe, err := models.Probe(ctx, client, *model, 100)
	out.ElapsedMS = time.Since(started).Milliseconds()
	out.Probe = probe
	if err != nil {
		out.ErrorType = "probe_error"
		writeResult(out)
		os.Exit(1)
	}
	out.Pass = probe.Score == 100 && probe.JSONOutput && probe.SingleTool && probe.ToolChoice && probe.ArgumentAccuracy && !probe.Restricted
	out.FailureStage = "complete"
	out.FailureClass = "model_capability_failure"
	if out.Pass {
		out.FailureClass = "none"
	}
	writeResult(out)
	if !out.Pass {
		os.Exit(1)
	}
}

func writeResult(out result) {
	encoded, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not encode model probe result")
		return
	}
	fmt.Println(string(encoded))
}
