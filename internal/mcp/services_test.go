package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicServiceCatalogIsCredentialsOnlyAndDoesNotGrantAgents(t *testing.T) {
	want := map[string]string{
		"google_calendar": "https://calendarmcp.googleapis.com/mcp/v1",
		"email":           "https://gmailmcp.googleapis.com/mcp/v1",
		"notion":          "https://mcp.notion.com/mcp",
		"calendly":        "https://mcp.calendly.com",
	}
	services := Services()
	if len(services) != 4 {
		t.Fatalf("catalog has %d services, want four OAuth providers", len(services))
	}
	for _, service := range services {
		if !service.NoAgentGrants {
			t.Errorf("service %q could grant agent tools during connect", service.ID)
		}
		endpoint, ok := want[service.ID]
		if !ok || endpoint != service.Endpoint {
			t.Errorf("unexpected service endpoint: %+v", service)
		}
		if !service.RequiresOAuth || service.OAuthAvailable || service.Status != ServiceOAuthUnavailable {
			t.Errorf("service claims unsupported OAuth capability: %+v", service)
		}
		notice := strings.ToLower(service.Notice)
		if !strings.Contains(notice, "not wired") && !strings.Contains(notice, "neither is wired") {
			t.Errorf("service notice omits OAuth limitation: %q", service.Notice)
		}
		b, _ := json.Marshal(service)
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(b, &fields)
		for _, secretField := range []string{"token", "client_secret", "api_key", "authorization"} {
			if _, exists := fields[secretField]; exists {
				t.Errorf("catalog contains a credential field %q: %s", secretField, b)
			}
		}
	}
}

func TestLookupServiceStableIDsAndAliases(t *testing.T) {
	for alias, id := range map[string]string{
		"calendar": "google_calendar", "Google Calendar": "google_calendar", "gcal": "google_calendar",
		"gmail": "email", "email": "email", "notion": "notion", "calendly": "calendly",
	} {
		service, ok := LookupService(alias)
		if !ok || service.ID != id {
			t.Errorf("LookupService(%q) = %+v, %v", alias, service, ok)
		}
	}
	if _, ok := LookupService("unknown"); ok {
		t.Fatal("unknown provider resolved")
	}
	if _, ok := LookupService("alexa"); ok {
		t.Fatal("removed Alexa workflow resolved")
	}
}
