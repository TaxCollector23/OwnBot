package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelProbePassRequiresEveryObservedCapability(t *testing.T) {
	passed := map[string]any{"json_output": true, "single_tool": true, "tool_choice": true, "argument_accuracy": true, "restricted": false}
	if ok, _ := modelProbePass(passed); !ok {
		t.Fatal("complete passing result rejected")
	}
	for _, key := range []string{"json_output", "single_tool", "tool_choice", "argument_accuracy"} {
		probe := map[string]any{}
		for k, value := range passed {
			probe[k] = value
		}
		probe[key] = false
		if ok, _ := modelProbePass(probe); ok {
			t.Fatalf("accepted failed %s check", key)
		}
	}
	if ok, _ := modelProbePass(map[string]any{"json_output": true}); ok {
		t.Fatal("accepted incomplete probe")
	}
}

func TestHardwareBenchmarkMeasuresConfiguredLocalCandidatesOnly(t *testing.T) {
	dataDir := tokenDir(t)
	var probeCalls int
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/status" && r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("request missing daemon token")
		}
		switch r.URL.Path {
		case "/api/status":
			_, _ = io.WriteString(w, `{"ok":true,"service":"ownbot"}`)
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"qwen-fast"},{"id":"qwen-slow"}]}`)
		case "/api/models":
			_, _ = io.WriteString(w, `[{"id":"fast","provider":"openai_compatible","model":"qwen-fast","base_url":"`+api.URL+`/v1"},{"id":"slow","provider":"openai_compatible","model":"qwen-slow","base_url":"`+api.URL+`/v1"},{"id":"remote","provider":"openai_compatible","model":"hosted","base_url":"https://models.example/v1"}]`)
		case "/api/models/fast/probe", "/api/models/slow/probe":
			probeCalls++
			if r.Method != http.MethodPost {
				t.Errorf("probe method=%s", r.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 0 {
				t.Errorf("probe body=%v err=%v", body, err)
			}
			if strings.Contains(r.URL.Path, "/fast/") {
				_, _ = io.WriteString(w, `{"probe":{"json_output":true,"single_tool":true,"tool_choice":true,"argument_accuracy":true,"restricted":false}}`)
			} else {
				_, _ = io.WriteString(w, `{"probe":{"json_output":true,"single_tool":true,"tool_choice":true,"argument_accuracy":false,"restricted":true}}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	args := []string{"--url", api.URL, "--data-dir", dataDir}
	var out strings.Builder
	if err := runHardwareChecks(args, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if probeCalls != 0 {
		t.Fatal("hardware check ran unrequested probe")
	}
	if err := runHardwareChecks(append(args, "--benchmark", "--model", "qwen-fast"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if probeCalls != 1 {
		t.Fatalf("probe calls=%d", probeCalls)
	}
	if !strings.Contains(out.String(), `"performance_recommendation"`) || !strings.Contains(out.String(), `"latency_ms"`) {
		t.Fatalf("benchmark report missing evidence: %s", out.String())
	}
	if strings.Contains(out.String(), `"tokens_per_second"`) {
		t.Fatal("reported tokens/sec without token counts")
	}
}

func TestHardwareFiltersOutRemoteConfiguredModelAndRejectsLongProbeLimit(t *testing.T) {
	if isLocalModelConfig(modelConfig{Provider: "openai_compatible", BaseURL: "https://api.example/v1"}) {
		t.Fatal("accepted remote model as local")
	}
	if !isLocalModelConfig(modelConfig{Provider: "ollama", BaseURL: "http://localhost:11434/v1"}) {
		t.Fatal("rejected local Ollama endpoint")
	}
	err := runHardwareChecks([]string{"--max-probe-seconds", "601"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "between 1 and 600") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMemoryFixtureParsesLinuxMeminfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meminfo")
	contents := "MemTotal:       8192000 kB\nMemAvailable:   2048000 kB\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	total, available, err := parseLinuxMeminfo(path)
	if err != nil || total != 8192000*1024 || available != 2048000*1024 {
		t.Fatalf("total=%d available=%d err=%v", total, available, err)
	}
}
