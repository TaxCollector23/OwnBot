package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type diagnosticReport struct {
	OverallStatus string            `json:"overall_status"`
	Checks        []diagnosticCheck `json:"checks"`
}

type diagnosticCheck struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Details []string `json:"details"`
	Action  string   `json:"action"`
}

type diagnosticModel struct {
	ID        string `json:"id"`
	LastProbe *struct {
		Restricted bool `json:"restricted"`
	} `json:"last_probe"`
}

type diagnosticAgent struct {
	Name       string   `json:"name"`
	Model      string   `json:"model"`
	Enabled    bool     `json:"enabled"`
	WebSearch  bool     `json:"web_search"`
	MCPEnabled bool     `json:"mcp_enabled"`
	MCPServers []string `json:"mcp_servers"`
	Tools      []string `json:"tools"`
}

type diagnosticTool struct {
	Name         string `json:"name"`
	Source       string `json:"source"`
	Server       string `json:"server"`
	ProviderTool string `json:"provider_tool"`
}

type diagnosticMCPServer struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Enabled      bool     `json:"enabled"`
	AllowedTools []string `json:"allowed_tools"`
}

type diagnosticApproval struct {
	Status string `json:"status"`
}

// runDiagnostics reports from daemon metadata only. It never probes a model,
// calls an MCP provider, or changes local or daemon state.
func runDiagnostics(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("inspect check", flag.ContinueOnError)
	apiURL, dataDir := commonFlags(fs)
	jsonOutput := fs.Bool("json", false, "print a machine-readable JSON report")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: ownbot inspect check [--json] [--url URL] [--data-dir DIR]")
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("inspect check does not accept positional arguments")
	}

	report := initialDiagnosticReport()
	client, err := clientFor(*apiURL, *dataDir)
	if err != nil {
		for i := range report.Checks {
			report.Checks[i].Status = "unknown"
			report.Checks[i].Summary = "Local daemon metadata could not be verified."
			report.Checks[i].Details = []string{"The daemon identity or local API token could not be verified."}
			report.Checks[i].Action = "Start the daemon and check that --url and --data-dir point to its local state."
		}
		report.OverallStatus = "unknown"
		if err := writeDiagnosticReport(stdout, report, *jsonOutput); err != nil {
			return err
		}
		return errors.New("diagnostics incomplete: local daemon metadata is unavailable")
	}

	ctx := context.Background()
	var status struct {
		OK      bool   `json:"ok"`
		Service string `json:"service"`
		Version string `json:"version"`
	}
	statusOK := diagnosticGet(ctx, client, "/api/status", &status)
	var models []diagnosticModel
	modelsOK := diagnosticGet(ctx, client, "/api/models", &models)
	var agents []diagnosticAgent
	agentsOK := diagnosticGet(ctx, client, "/api/agents", &agents)
	var tools []diagnosticTool
	toolsOK := diagnosticGet(ctx, client, "/api/tools", &tools)
	var servers []diagnosticMCPServer
	serversOK := diagnosticGet(ctx, client, "/api/mcp/servers", &servers)
	var approvals []diagnosticApproval
	approvalsOK := diagnosticGet(ctx, client, "/api/approvals", &approvals)

	if statusOK && status.OK && status.Service == "ownbot" {
		version := strings.TrimSpace(status.Version)
		summary := "OwnBot daemon identity verified."
		if version != "" {
			summary += " Version: " + strconv.Quote(version) + "."
		}
		setDiagnosticCheck(&report, "daemon", "ok", summary, nil, "")
	} else {
		setDiagnosticCheck(&report, "daemon", "unknown", "OwnBot daemon status could not be verified.", []string{"The status endpoint did not return a valid OwnBot identity."}, "Check that --url points to the local OwnBot daemon.")
	}

	if modelsOK {
		probed, restricted := 0, 0
		for _, model := range models {
			if model.LastProbe != nil {
				probed++
				if model.LastProbe.Restricted {
					restricted++
				}
			}
		}
		switch {
		case len(models) == 0:
			setDiagnosticCheck(&report, "models", "info", "No model configurations are saved.", []string{"This is a valid empty setup."}, "Choose and configure a model when you are ready; this check does not download models or contact providers.")
		case probed == 0:
			setDiagnosticCheck(&report, "models", "info", fmt.Sprintf("%d model configuration(s) saved; none has a saved probe result.", len(models)), []string{"A missing probe is not a reachability failure. This check does not run probes."}, "If you need capability evidence, run an explicit model probe from setup.")
		default:
			details := []string{fmt.Sprintf("%d of %d configuration(s) have saved probe metadata.", probed, len(models)), "Probe results are historical observations; they do not verify current endpoint reachability or model behavior."}
			if restricted > 0 {
				details = append(details, fmt.Sprintf("%d saved probe result(s) were marked restricted.", restricted))
			}
			level := "ok"
			if restricted > 0 || probed < len(models) {
				level = "info"
			}
			setDiagnosticCheck(&report, "models", level, fmt.Sprintf("%d model configuration(s) saved.", len(models)), details, "")
		}
	} else {
		setDiagnosticCheck(&report, "models", "unknown", "Model metadata could not be read.", []string{"The local daemon did not return model configuration metadata."}, "Check daemon status and local API access.")
	}

	if agentsOK {
		if len(agents) == 0 {
			setDiagnosticCheck(&report, "agents", "info", "No agents are configured.", []string{"An empty setup is safe; agents are not created by this check."}, "Create a tool-free agent when you are ready to configure one.")
		} else {
			enabled, withTools, disabled := 0, 0, 0
			for _, agent := range agents {
				if agent.Enabled {
					enabled++
					if len(agent.Tools) > 0 || agent.WebSearch {
						withTools++
					}
				} else {
					disabled++
				}
			}
			level := "ok"
			details := []string{fmt.Sprintf("%d enabled; %d disabled.", enabled, disabled)}
			if withTools < enabled {
				details = append(details, fmt.Sprintf("%d enabled agent(s) have no tools selected; tools-off is a valid safe state.", enabled-withTools))
				level = "info"
			}
			if disabled > 0 {
				details = append(details, "Disabled agents are reported for visibility and are not treated as failures.")
				if level == "ok" {
					level = "info"
				}
			}
			webSearchEnabled := false
			for _, agent := range agents {
				webSearchEnabled = webSearchEnabled || agent.WebSearch
			}
			if !webSearchEnabled {
				details = append(details, "Web search is off for all agents; no external search capability is selected.")
				if level == "ok" {
					level = "info"
				}
			}
			setDiagnosticCheck(&report, "agents", level, fmt.Sprintf("%d agent(s) configured.", len(agents)), details, "")
		}
	} else {
		setDiagnosticCheck(&report, "agents", "unknown", "Agent metadata could not be read.", []string{"The local daemon did not return agent configuration metadata."}, "Check daemon status and local API access.")
	}

	if serversOK {
		analyzeDiagnosticMCP(&report, agents, tools, toolsOK, servers)
	} else {
		setDiagnosticCheck(&report, "mcp", "unknown", "MCP configuration metadata could not be read.", []string{"The local daemon did not return its saved MCP configuration metadata."}, "Check daemon status and local API access.")
	}

	if toolsOK && agentsOK {
		analyzeDiagnosticTools(&report, agents, tools, models, modelsOK, servers, serversOK)
	} else if !toolsOK {
		setDiagnosticCheck(&report, "tools", "unknown", "Tool metadata could not be read.", []string{"The local daemon did not return its current tool registry metadata."}, "Check daemon status and local API access.")
	} else {
		setDiagnosticCheck(&report, "tools", "unknown", "Selected tools could not be compared with the local registry.", []string{"Agent metadata is unavailable."}, "Check daemon status and local API access.")
	}

	if approvalsOK {
		pending := 0
		for _, approval := range approvals {
			if approval.Status == "pending" {
				pending++
			}
		}
		if pending == 0 {
			setDiagnosticCheck(&report, "approvals", "ok", "No pending approvals are recorded.", nil, "")
		} else {
			setDiagnosticCheck(&report, "approvals", "info", fmt.Sprintf("%d approval(s) are waiting for review.", pending), []string{"Pending approvals are an action-review state, not a diagnostics failure. Request details are not included."}, "Review each approval in the dashboard before deciding.")
		}
	} else {
		setDiagnosticCheck(&report, "approvals", "unknown", "Approval metadata could not be read.", []string{"The local daemon did not return approval status metadata."}, "Check daemon status and local API access.")
	}

	report.OverallStatus = overallDiagnosticStatus(report.Checks)
	if err := writeDiagnosticReport(stdout, report, *jsonOutput); err != nil {
		return err
	}
	if report.OverallStatus == "warning" || report.OverallStatus == "unknown" {
		return errors.New("diagnostics reported warnings or unknown checks; review the report before treating setup as healthy")
	}
	return nil
}

func initialDiagnosticReport() diagnosticReport {
	names := []string{"daemon", "models", "agents", "tools", "mcp", "approvals"}
	checks := make([]diagnosticCheck, 0, len(names))
	for _, name := range names {
		checks = append(checks, diagnosticCheck{Name: name, Status: "unknown", Summary: "Not checked.", Details: []string{}})
	}
	return diagnosticReport{OverallStatus: "unknown", Checks: checks}
}

func diagnosticGet(parent context.Context, client *Client, path string, value any) bool {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	var raw json.RawMessage
	if err := client.Do(ctx, http.MethodGet, path, nil, &raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	return json.Unmarshal(raw, value) == nil
}

func setDiagnosticCheck(report *diagnosticReport, name, status, summary string, details []string, action string) {
	for i := range report.Checks {
		if report.Checks[i].Name == name {
			if details == nil {
				details = []string{}
			}
			report.Checks[i] = diagnosticCheck{Name: name, Status: status, Summary: summary, Details: details, Action: action}
			return
		}
	}
}

func addDiagnosticFinding(report *diagnosticReport, name, level, detail, action string) {
	for i := range report.Checks {
		check := &report.Checks[i]
		if check.Name != name {
			continue
		}
		if level == "warning" || (level == "info" && check.Status == "ok") {
			check.Status = level
		}
		check.Details = append(check.Details, detail)
		if action != "" && (check.Action == "" || level == "warning") {
			check.Action = action
		}
		return
	}
}

func overallDiagnosticStatus(checks []diagnosticCheck) string {
	result := "ok"
	for _, check := range checks {
		switch check.Status {
		case "warning":
			return "warning"
		case "unknown":
			result = "unknown"
		case "info":
			if result == "ok" {
				result = "info"
			}
		}
	}
	return result
}

func writeDiagnosticReport(w io.Writer, report diagnosticReport, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(w, report)
	}
	if _, err := fmt.Fprintf(w, "OwnBot diagnostics: %s\n", report.OverallStatus); err != nil {
		return err
	}
	for _, check := range report.Checks {
		if _, err := fmt.Fprintf(w, "[%s] %s: %s\n", check.Status, check.Name, check.Summary); err != nil {
			return err
		}
		for _, detail := range check.Details {
			if _, err := fmt.Fprintf(w, "  %s\n", detail); err != nil {
				return err
			}
		}
		if check.Action != "" {
			if _, err := fmt.Fprintf(w, "  Next: %s\n", check.Action); err != nil {
				return err
			}
		}
	}
	return nil
}

func analyzeDiagnosticTools(report *diagnosticReport, agents []diagnosticAgent, tools []diagnosticTool, models []diagnosticModel, modelsOK bool, servers []diagnosticMCPServer, serversOK bool) {
	toolByName := make(map[string]diagnosticTool, len(tools))
	for _, tool := range tools {
		toolByName[tool.Name] = tool
	}
	modelByID := make(map[string]diagnosticModel, len(models))
	for _, model := range models {
		modelByID[model.ID] = model
	}
	serverByID := make(map[string]diagnosticMCPServer, len(servers))
	for _, server := range servers {
		serverByID[server.ID] = server
	}
	selectedCount, missingCount, enabledMissingCount := 0, 0, 0
	for _, agent := range agents {
		for _, name := range agent.Tools {
			selectedCount++
			if _, ok := toolByName[name]; !ok {
				missingCount++
				if agent.Enabled {
					enabledMissingCount++
				}
			}
		}
	}
	switch {
	case selectedCount == 0:
		setDiagnosticCheck(report, "tools", "info", "No agent has tools selected.", []string{"Tools-off is a valid safe state."}, "")
	case enabledMissingCount > 0:
		setDiagnosticCheck(report, "tools", "warning", fmt.Sprintf("%d selected tool reference(s) for enabled agents are absent from the daemon's current registry.", enabledMissingCount), []string{"The registry is a local snapshot; this check does not dispatch tools."}, "Review those exact tool references and their integration state; diagnostics will not enable or reconnect anything.")
	case missingCount > 0:
		setDiagnosticCheck(report, "tools", "info", fmt.Sprintf("%d selected tool reference(s) for disabled agents are absent from the current registry.", missingCount), []string{"Disabled agents are not treated as failures."}, "")
	default:
		setDiagnosticCheck(report, "tools", "ok", fmt.Sprintf("%d selected tool reference(s) match the daemon's current registry.", selectedCount), []string{"Registry metadata is a local snapshot; this check does not dispatch tools."}, "")
	}
	for _, agent := range agents {
		for _, name := range agent.Tools {
			tool, ok := toolByName[name]
			if !ok {
				if agent.Enabled {
					addDiagnosticFinding(report, "tools", "warning", fmt.Sprintf("Enabled agent %q selects tool %q, which is absent from the daemon's current tool registry.", agent.Name, name), "Review that exact tool and its integration state; diagnostics will not enable or reconnect it.")
				} else {
					addDiagnosticFinding(report, "tools", "info", fmt.Sprintf("Disabled agent %q selects tool %q, which is absent from the current tool registry.", agent.Name, name), "")
				}
				if agent.Enabled && looksLikeMCPAlias(name) {
					if !agent.MCPEnabled {
						addDiagnosticFinding(report, "mcp", "warning", fmt.Sprintf("Enabled agent %q selects an MCP tool while MCP is disabled for that agent.", agent.Name), "Review the agent's MCP setting and exact server/tool access before changing it.")
					} else if serversOK {
						server, found := diagnosticServerForAlias(name, servers)
						if found && agent.MCPServers != nil && !containsString(agent.MCPServers, server.ID) {
							addDiagnosticFinding(report, "mcp", "warning", fmt.Sprintf("Enabled agent %q selects an MCP tool from server %q, but that server is not in the agent's MCP server allowlist.", agent.Name, server.Name), "Review the exact server binding and provider tool permissions; no access was granted.")
						} else if found && !server.Enabled {
							addDiagnosticFinding(report, "mcp", "warning", fmt.Sprintf("Enabled agent %q selects an MCP tool from server %q, which is not currently listed as connected by the daemon.", agent.Name, server.Name), "Review that integration's connection and exact tool permissions; this check does not reconnect it.")
						} else if found && agent.MCPServers == nil {
							addDiagnosticFinding(report, "mcp", "info", fmt.Sprintf("The daemon response does not expose the MCP server allowlist for agent %q; this binding was not checked.", agent.Name), "Use a daemon version that exposes per-agent MCP server metadata to verify this binding.")
						} else if !found {
							addDiagnosticFinding(report, "mcp", "warning", fmt.Sprintf("Enabled agent %q selects an MCP tool, but its server could not be matched to saved MCP configuration.", agent.Name), "Review the selected tool and saved MCP integrations; no access was changed.")
						}
					}
				}
				continue
			}
			if tool.Source == "web_search" && !agent.WebSearch {
				if agent.Enabled {
					addDiagnosticFinding(report, "tools", "warning", fmt.Sprintf("Enabled agent %q selects web search while web search is disabled for that agent.", agent.Name), "Review the agent's web search setting before running it; no provider call was made.")
				} else {
					addDiagnosticFinding(report, "tools", "info", fmt.Sprintf("Disabled agent %q selects web search while web search is disabled for that agent.", agent.Name), "")
				}
				continue
			}
			if tool.Source != "mcp" {
				continue
			}
			if !agent.Enabled {
				addDiagnosticFinding(report, "mcp", "info", fmt.Sprintf("Disabled agent %q selects MCP tool %q.", agent.Name, name), "")
				continue
			}
			if !agent.MCPEnabled {
				addDiagnosticFinding(report, "mcp", "warning", fmt.Sprintf("Enabled agent %q selects MCP tool %q while MCP is disabled for that agent.", agent.Name, name), "Review the agent's MCP setting and exact server/tool access before changing it.")
			}
			if !serversOK {
				continue
			}
			server, found := serverByID[tool.Server]
			if !found {
				addDiagnosticFinding(report, "mcp", "warning", fmt.Sprintf("Selected MCP tool %q references a server missing from saved MCP configuration.", name), "Review the selected tool and saved MCP integrations; no access was changed.")
				continue
			}
			if agent.MCPServers == nil {
				addDiagnosticFinding(report, "mcp", "info", fmt.Sprintf("The daemon response does not expose the MCP server allowlist for agent %q; binding for server %q was not checked.", agent.Name, server.Name), "Use a daemon version that exposes per-agent MCP server metadata to verify this binding.")
			} else if !containsString(agent.MCPServers, server.ID) {
				addDiagnosticFinding(report, "mcp", "warning", fmt.Sprintf("Enabled agent %q selects MCP tool %q, but server %q is not in the agent's MCP server allowlist.", agent.Name, name, server.Name), "Review the exact server binding and provider tool permissions; no access was granted.")
			}
			if !server.Enabled {
				addDiagnosticFinding(report, "mcp", "warning", fmt.Sprintf("Enabled agent %q selects MCP tool %q, but server %q is not listed as connected by the daemon.", agent.Name, name, server.Name), "Review that integration's connection and exact tool permissions; this check does not reconnect it.")
			}
			if tool.ProviderTool != "" && !containsString(server.AllowedTools, tool.ProviderTool) {
				addDiagnosticFinding(report, "mcp", "warning", fmt.Sprintf("Selected MCP tool %q is absent from server %q's saved tool allowlist.", tool.ProviderTool, server.Name), "Review the exact provider tool permission and agent binding; no access was changed.")
			}
		}
		if modelsOK {
			modelID := strings.TrimSpace(agent.Model)
			if modelID == "" {
				if agent.Enabled {
					addDiagnosticFinding(report, "agents", "warning", fmt.Sprintf("Enabled agent %q has no model configuration selected.", agent.Name), "Choose a saved model configuration before running this agent.")
				} else {
					addDiagnosticFinding(report, "agents", "info", fmt.Sprintf("Disabled agent %q has no model configuration selected.", agent.Name), "")
				}
				continue
			}
			model, found := modelByID[modelID]
			if !found {
				if agent.Enabled {
					addDiagnosticFinding(report, "agents", "warning", fmt.Sprintf("Enabled agent %q references a model configuration that is not saved.", agent.Name), "Review the agent's model selection before running it.")
				} else {
					addDiagnosticFinding(report, "agents", "info", fmt.Sprintf("Disabled agent %q references a model configuration that is not saved.", agent.Name), "")
				}
			} else if model.LastProbe == nil {
				if agent.Enabled {
					addDiagnosticFinding(report, "agents", "warning", fmt.Sprintf("Enabled agent %q uses a model configuration with no saved capability probe.", agent.Name), "Review the model configuration; run an explicit probe only if you want new capability evidence.")
				} else {
					addDiagnosticFinding(report, "agents", "info", fmt.Sprintf("Disabled agent %q uses a model configuration with no saved capability probe.", agent.Name), "")
				}
			} else if model.LastProbe.Restricted {
				if agent.Enabled {
					addDiagnosticFinding(report, "agents", "warning", fmt.Sprintf("Enabled agent %q uses a model whose saved probe was marked restricted.", agent.Name), "Review the historical probe and model choice before running this agent; the probe was not repeated.")
				} else {
					addDiagnosticFinding(report, "agents", "info", fmt.Sprintf("Disabled agent %q uses a model whose saved probe was marked restricted.", agent.Name), "")
				}
			}
		}
	}
}

func analyzeDiagnosticMCP(report *diagnosticReport, agents []diagnosticAgent, tools []diagnosticTool, toolsOK bool, servers []diagnosticMCPServer) {
	selectedMCP := 0
	for _, tool := range tools {
		if tool.Source == "mcp" {
			selectedMCP++
		}
	}
	missingVisibility := false
	for _, agent := range agents {
		if agent.MCPServers == nil {
			missingVisibility = true
			break
		}
	}
	if len(servers) == 0 {
		if selectedMCP == 0 {
			setDiagnosticCheck(report, "mcp", "info", "No MCP integrations are configured.", []string{"Disconnected or absent integrations are valid when no enabled agent depends on them."}, "")
		} else {
			setDiagnosticCheck(report, "mcp", "info", "No saved MCP integrations were returned, though MCP tools appear in the current registry.", []string{"The tool registry may be stale; diagnostics does not call a provider or change the registry."}, "Review the local daemon's saved MCP configuration.")
		}
		return
	}
	connected := 0
	for _, server := range servers {
		if server.Enabled {
			connected++
		}
	}
	level := "ok"
	details := []string{fmt.Sprintf("The daemon lists %d of %d configured integration(s) in its current tool registry.", connected, len(servers)), "This snapshot does not check provider reachability or authentication freshness."}
	action := ""
	if missingVisibility {
		level = "info"
		details = append(details, "At least one agent response omits mcp_servers; that daemon version does not expose per-agent MCP server bindings, so they were not checked.")
		action = "Use a daemon version that exposes per-agent MCP server metadata to verify each binding."
	}
	if connected < len(servers) {
		depends := false
		if toolsOK {
			for _, tool := range tools {
				if tool.Source != "mcp" {
					continue
				}
				for _, agent := range agents {
					if agent.Enabled && containsString(agent.Tools, tool.Name) && tool.Server != "" {
						server, ok := findDiagnosticServer(servers, tool.Server)
						if ok && !server.Enabled {
							depends = true
						}
					}
				}
			}
		}
		if !depends && level == "ok" {
			level = "info"
			details = append(details, "One or more integrations are not listed as connected, but no enabled selected MCP tool currently depends on them.")
		}
	}
	if selectedMCP == 0 {
		if level == "ok" {
			level = "info"
		}
		details = append(details, "No MCP tool is currently listed in the local tool registry; that is a valid tool-free state.")
	}
	setDiagnosticCheck(report, "mcp", level, fmt.Sprintf("%d MCP integration(s) configured.", len(servers)), details, action)
}

func findDiagnosticServer(servers []diagnosticMCPServer, id string) (diagnosticMCPServer, bool) {
	for _, server := range servers {
		if server.ID == id {
			return server, true
		}
	}
	return diagnosticMCPServer{}, false
}

func diagnosticServerForAlias(alias string, servers []diagnosticMCPServer) (diagnosticMCPServer, bool) {
	var candidate diagnosticMCPServer
	count := 0
	for _, server := range servers {
		prefix := diagnosticAliasPart(server.ID)
		if len(prefix) > 12 {
			prefix = prefix[:12]
		}
		if prefix != "" && strings.HasPrefix(alias, "mcp_"+prefix+"_") {
			candidate = server
			count++
		}
	}
	return candidate, count == 1
}

func diagnosticAliasPart(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func looksLikeMCPAlias(name string) bool { return strings.HasPrefix(name, "mcp_") }

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
