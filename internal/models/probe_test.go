package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeValidatesExactClosedSchemas(t *testing.T) {
	for _, tc := range []struct {
		name      string
		multi     string
		wantScore int
	}{
		{name: "good", multi: `{"value":"beta"}`, wantScore: 100},
		{name: "extra property", multi: `{"value":"beta","extra":true}`, wantScore: 75},
		{name: "wrong type", multi: `{"value":42}`, wantScore: 75},
		{name: "bad tool choice", multi: `{"name":"probe_one","arguments":{"value":"alpha"}}`, wantScore: 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				prompt := ""
				if len(req.Messages) > 0 {
					prompt = req.Messages[0].Content
				}
				message := map[string]any{"role": "assistant", "content": ""}
				switch {
				case strings.Contains(prompt, "Return only JSON"):
					message["content"] = `{"ok":true}`
				case strings.Contains(prompt, "probe_one"):
					message["tool_calls"] = []any{map[string]any{"id": "one", "type": "function", "function": map[string]any{"name": "probe_one", "arguments": `{"value":"alpha"}`}}}
				default:
					if strings.Contains(tc.multi, "\"name\"") {
						message["tool_calls"] = []any{map[string]any{"id": "wrong", "type": "function", "function": map[string]any{"name": "probe_one", "arguments": `{"value":"alpha"}`}}}
					} else {
						message["tool_calls"] = []any{map[string]any{"id": "two", "type": "function", "function": map[string]any{"name": "probe_two", "arguments": tc.multi}}}
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
			}))
			defer server.Close()
			got, err := Probe(context.Background(), &OpenAICompatible{BaseURL: server.URL}, "fake", 100)
			if err != nil {
				t.Fatal(err)
			}
			if got.Score != tc.wantScore || got.Restricted != (tc.wantScore < 100) {
				t.Fatalf("probe = %+v", got)
			}
		})
	}
}

func TestProbeFailsClosedAndStaysRestrictedOnErrors(t *testing.T) {
	client := &OpenAICompatible{BaseURL: ""}
	got, err := Probe(context.Background(), client, "fake", 100)
	if err == nil {
		t.Fatal("empty endpoint should fail")
	}
	if !got.Restricted {
		t.Fatalf("probe error must remain restricted: %+v", got)
	}
}
