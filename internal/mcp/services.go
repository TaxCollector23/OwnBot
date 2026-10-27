package mcp

import "strings"

type ServiceStatus string

const (
	ServiceOAuthUnavailable ServiceStatus = "oauth_sign_in_not_wired"
	ServiceExperimentalAuth ServiceStatus = "experimental_reauthentication_required"
)

// Service describes a public MCP provider without storing credentials or
// granting its tools to any agent. OAuthAvailable remains false until OwnBot
// has an OAuth redirect flow and a secure token store.
type Service struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Provider       string        `json:"provider"`
	Endpoint       string        `json:"endpoint,omitempty"`
	Transport      string        `json:"transport,omitempty"`
	AuthMethod     string        `json:"auth_method"`
	Status         ServiceStatus `json:"status"`
	RequiresOAuth  bool          `json:"requires_oauth"`
	OAuthAvailable bool          `json:"oauth_available"`
	Preview        bool          `json:"preview,omitempty"`
	Experimental   bool          `json:"experimental,omitempty"`
	ReauthRequired bool          `json:"reauth_required,omitempty"`
	NoAgentGrants  bool          `json:"no_agent_grants"`
	Notice         string        `json:"notice"`
}

var publicServices = []Service{
	{
		ID: "google_calendar", Name: "Google Calendar", Provider: "Google",
		Endpoint: "https://calendarmcp.googleapis.com/mcp/v1", Transport: "streamable_http",
		AuthMethod: "Google OAuth 2.0", Status: ServiceOAuthUnavailable, RequiresOAuth: true,
		Preview: true, NoAgentGrants: true,
		Notice: "Google Workspace Developer Preview; OAuth sign-in is not wired in this build. Connecting will not grant tools to agents.",
	},
	{
		ID: "email", Name: "Email (Gmail)", Provider: "Google",
		Endpoint: "https://gmailmcp.googleapis.com/mcp/v1", Transport: "streamable_http",
		AuthMethod: "Google OAuth 2.0", Status: ServiceOAuthUnavailable, RequiresOAuth: true,
		Preview: true, NoAgentGrants: true,
		Notice: "Gmail MCP is in Google Workspace Developer Preview; OAuth sign-in is not wired in this build. Connecting will not grant tools to agents.",
	},
	{
		ID: "notion", Name: "Notion", Provider: "Notion",
		Endpoint: "https://mcp.notion.com/mcp", Transport: "streamable_http",
		AuthMethod: "Notion OAuth sign-in", Status: ServiceOAuthUnavailable, RequiresOAuth: true,
		NoAgentGrants: true,
		Notice:        "Notion OAuth sign-in is not wired in this build; workspace policy may also need to approve this client. Connecting will not grant tools to agents.",
	},
	{
		ID: "calendly", Name: "Calendly", Provider: "Calendly",
		Endpoint: "https://mcp.calendly.com", Transport: "streamable_http",
		AuthMethod: "OAuth 2.1 PKCE and dynamic client registration", Status: ServiceOAuthUnavailable,
		RequiresOAuth: true, NoAgentGrants: true,
		Notice: "Calendly requires OAuth 2.1 PKCE and dynamic client registration; neither is wired in this build. API keys and personal access tokens are not accepted here. Connecting will not grant tools to agents.",
	},
	{
		ID: "alexa", Name: "Alexa", Provider: "Amazon Alexa",
		AuthMethod: "Amazon account sign-in", Status: ServiceExperimentalAuth,
		Experimental: true, ReauthRequired: true, NoAgentGrants: true,
		Notice: "Alexa support is experimental and requires account re-authentication. No Alexa service connection or action is available in this build.",
	},
}

// Services returns a copy of the built-in public-provider catalog.
func Services() []Service { return append([]Service(nil), publicServices...) }

// LookupService resolves stable IDs and common user-facing aliases.
func LookupService(id string) (Service, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	switch id {
	case "calendar", "google-calendar", "google calendar", "gcal":
		id = "google_calendar"
	case "gmail", "google-mail", "google mail":
		id = "email"
	}
	for _, service := range publicServices {
		if service.ID == id {
			return service, true
		}
	}
	return Service{}, false
}
