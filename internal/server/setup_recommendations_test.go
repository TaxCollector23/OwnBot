package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRecommendationsForHardware(t *testing.T) {
	const testedHost = uint64(16 * 1024 * 1024 * 1024)
	tests := []struct {
		name       string
		memory     uint64
		goos       string
		wantCount  int
		wantReason string
	}{
		{"unknown memory", 0, "darwin", 0, "unavailable"},
		{"below tested tier", testedHost - 1, "linux", 0, "memory tier"},
		{"unsupported system", testedHost, "freebsd", 0, "llama.cpp-compatible"},
		{"tested tier", testedHost, "darwin", 1, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := recommendationsFor(tc.memory, tc.goos)
			if len(result.Suggestions) != tc.wantCount || result.HostMemoryBytes != tc.memory {
				t.Fatalf("unexpected result: %+v", result)
			}
			if tc.wantReason != "" && !strings.Contains(result.Reason, tc.wantReason) {
				t.Fatalf("reason %q does not mention %q", result.Reason, tc.wantReason)
			}
			if tc.wantCount == 1 {
				suggestion := result.Suggestions[0]
				if suggestion.Model != "qwen3:1.7b" || suggestion.InstallCommand != "ollama pull qwen3:1.7b" || !strings.Contains(suggestion.Reason, "quality and M1 performance are unverified") {
					t.Fatalf("unexpected suggestion: %+v", suggestion)
				}
				if strings.Contains(strings.ToLower(suggestion.Reason), "guarantee") {
					t.Fatalf("suggestion overstates evidence: %q", suggestion.Reason)
				}
			}
		})
	}
}

func TestSetupRecommendationsEndpointUsesInjectedHostDataAndRequiresAuth(t *testing.T) {
	s, token := testApp(t)
	s.setupHostInfo = func() (uint64, string) { return 16 * 1024 * 1024 * 1024, "linux" }
	if w := request(t, s.Handler(), "GET", "/api/setup/recommendations", "", "127.0.0.1:8765", "", ""); w.Code != 401 {
		t.Fatalf("unauthenticated endpoint status=%d", w.Code)
	}
	w := request(t, s.Handler(), "GET", "/api/setup/recommendations", "", "127.0.0.1:8765", "", token)
	var got setupRecommendations
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("endpoint response status=%d body=%s", w.Code, w.Body.String())
	}
	if got.HostMemoryBytes != 16*1024*1024*1024 || len(got.Suggestions) != 1 || got.Suggestions[0].InstallCommand != "ollama pull qwen3:1.7b" {
		t.Fatalf("unexpected endpoint data: %+v", got)
	}
}
