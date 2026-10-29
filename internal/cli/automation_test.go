package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func automationAPIServer(t *testing.T, dataDir string, handler http.HandlerFunc) (*httptest.Server, func([]string) error, *strings.Builder) {
	t.Helper()
	var stdout strings.Builder
	api := ownBotTestServer(handler)
	t.Cleanup(api.Close)
	invoke := func(args []string) error {
		return runAutomationSetup(append(args, "--url", api.URL, "--data-dir", dataDir), &stdout, &strings.Builder{})
	}
	return api, invoke, &stdout
}

func TestAutomationAddSendsManualPayloadAndResolvesAgentName(t *testing.T) {
	dataDir := tokenDir(t)
	var got map[string]any
	_, invoke, stdout := automationAPIServer(t, dataDir, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("authorization header missing")
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/agents":
			_, _ = w.Write([]byte(`[{"id":"agent-1","name":"Planner"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/automations":
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode automation payload: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"automation-1","name":"Nightly planning","trigger_type":"manual","enabled":true,"agent_id":"agent-1"}`))
		default:
			http.NotFound(w, r)
		}
	})
	if err := invoke([]string{"add", "--name", "Nightly planning", "--agent", "Planner", "--input", "Plan tomorrow"}); err != nil {
		t.Fatalf("add automation: %v", err)
	}
	want := map[string]any{
		"name": "Nightly planning", "enabled": true, "trigger_type": "manual",
		"agent_id": "agent-1", "input": "Plan tomorrow",
	}
	if !jsonValuesEqual(got, want) {
		t.Fatalf("payload = %#v, want %#v", got, want)
	}
	if !strings.Contains(stdout.String(), "Created \"Nightly planning\"") {
		t.Fatalf("missing concise success output: %s", stdout.String())
	}
}

func TestAutomationScheduleDefaultsDisabledAndExplicitEnableIsHonored(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		wantEnable bool
	}{
		{name: "default disabled", args: []string{"add", "--name", "Every hour", "--agent", "agent-1", "--input", "Check", "--interval-minutes", "60"}},
		{name: "explicit enabled", args: []string{"add", "--name", "Every hour", "--agent", "agent-1", "--input", "Check", "--interval-minutes", "60", "--enabled=true"}, wantEnable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := tokenDir(t)
			var got map[string]any
			_, invoke, _ := automationAPIServer(t, dataDir, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/agents":
					_, _ = w.Write([]byte(`[{"id":"agent-1","name":"Planner"}]`))
				case r.Method == http.MethodPost && r.URL.Path == "/api/automations":
					_ = json.NewDecoder(r.Body).Decode(&got)
					_, _ = w.Write([]byte(`{"id":"automation-1","name":"Every hour","trigger_type":"interval","enabled":false,"agent_id":"agent-1"}`))
				default:
					http.NotFound(w, r)
				}
			})
			if err := invoke(tc.args); err != nil {
				t.Fatalf("add scheduled automation: %v", err)
			}
			if got["trigger_type"] != "interval" || got["interval_minutes"] != float64(60) || got["enabled"] != tc.wantEnable {
				t.Fatalf("scheduled payload = %#v", got)
			}
		})
	}
}

func TestAutomationAddRejectsMissingFieldsAndInvalidSchedulesBeforeWriting(t *testing.T) {
	dataDir := tokenDir(t)
	writes := 0
	_, invoke, _ := automationAPIServer(t, dataDir, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writes++
		}
		http.NotFound(w, r)
	})
	for _, args := range [][]string{
		{"add", "--name", "A", "--input", "hello"},
		{"add", "--name", "A", "--agent", "x"},
		{"add", "--name", "A", "--agent", "x", "--input", "hello", "--interval-minutes", "0"},
		{"add", "--name", "A", "--agent", "x", "--input", "hello", "--daily-at", "29:00"},
		{"add", "--name", "A", "--agent", "x", "--input", "hello", "--interval-minutes", "15", "--daily-at", "12:00"},
	} {
		if err := invoke(args); err == nil {
			t.Errorf("expected validation error for %v", args)
		}
	}
	if writes != 0 {
		t.Fatalf("invalid configuration sent %d create requests", writes)
	}
}

func TestAutomationRunReusesCallerIdempotencyKey(t *testing.T) {
	dataDir := tokenDir(t)
	var posted []map[string]string
	_, invoke, stdout := automationAPIServer(t, dataDir, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/automations":
			_, _ = w.Write([]byte(`[{"id":"automation-1","name":"Research","enabled":true,"trigger_type":"manual"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/automations/automation-1/jobs":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			posted = append(posted, body)
			_, _ = w.Write([]byte(`{"id":"job-1","status":"queued","deduplicated":true}`))
		default:
			http.NotFound(w, r)
		}
	})
	args := []string{"run", "--name", "Research", "--idempotency-key", "retry-2026-10-28"}
	for range 2 {
		if err := invoke(args); err != nil {
			t.Fatalf("queue run: %v", err)
		}
	}
	if len(posted) != 2 || posted[0]["idempotency_key"] != "retry-2026-10-28" || posted[1]["idempotency_key"] != posted[0]["idempotency_key"] {
		t.Fatalf("idempotency payloads = %#v", posted)
	}
	if !strings.Contains(stdout.String(), "Run already queued as job job-1") {
		t.Fatalf("missing deduplication output: %s", stdout.String())
	}
}

func TestAutomationRunRequiresCallerIdempotencyKey(t *testing.T) {
	dataDir := tokenDir(t)
	requests := 0
	_, invoke, _ := automationAPIServer(t, dataDir, func(w http.ResponseWriter, r *http.Request) { requests++ })
	if err := invoke([]string{"run", "--name", "Research"}); err == nil || !strings.Contains(err.Error(), "--idempotency-key is required") {
		t.Fatalf("error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("missing key caused %d API requests", requests)
	}
}

func jsonValuesEqual(a, b map[string]any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}
