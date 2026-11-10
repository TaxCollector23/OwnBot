package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRecommendModelUsesAvailableResourcesAndAvoidsOptimalityClaims(t *testing.T) {
	candidates := ollamaShortlist()
	recommended, reason, ok := recommendModel(candidates, 3*gibibyte, 0, 2*gibibyte, true, true, false)
	if !ok || recommended.ID != "qwen3:1.7b" {
		t.Fatalf("recommended %+v reason %q ok %v", recommended, reason, ok)
	}
	if !strings.Contains(strings.ToLower(reason), "not an optimality") {
		t.Fatalf("recommendation must state that no benchmark claim is made: %q", reason)
	}
	if _, _, ok := recommendModel(candidates, 0, 0, 2*gibibyte, false, true, false); ok {
		t.Fatal("recommended a model without available RAM telemetry")
	}
	if _, _, ok := recommendModel(candidates, 8*gibibyte, 0, 100, true, true, false); ok {
		t.Fatal("recommended a model without enough disk space")
	}
}

func TestRecommendModelDoesNotAddDiscreteVRAMToAppleUnifiedMemory(t *testing.T) {
	candidate := setupModelCandidate{ID: "model", SizeBytes: 2, EstimatedMemory: 2, ToolUseSupported: true}
	if _, _, ok := recommendModel([]setupModelCandidate{candidate}, 1, 10, 10, true, true, true); ok {
		t.Fatal("discrete VRAM was incorrectly counted as separate Apple Silicon memory")
	}
}

func TestOllamaPullReportsHTTPAndStreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		want string
	}{{"http", http.StatusInternalServerError, "failed", "HTTP 500"}, {"stream", http.StatusOK, `{"error":"disk full"}`, "disk full"}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/pull" {
					t.Errorf("pull path = %q", r.URL.Path)
				}
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			err := pullOllamaModel(context.Background(), server.Client(), server.URL+"/v1", "qwen3:0.6b", io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("pull error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestOllamaPullSupportsCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"pulling model"}`+"\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pullOllamaModel(ctx, server.Client(), server.URL, "model", io.Discard) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("pull request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pull did not stop after cancellation")
	}
}
