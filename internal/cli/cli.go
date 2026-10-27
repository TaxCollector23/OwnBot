package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type command struct {
	apiURL  string
	dataDir string
}

var errHelpRequested = errors.New("help requested")

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("value cannot be empty")
	}
	*s = append(*s, value)
	return nil
}

type setupFile struct {
	APIURL string `json:"api_url,omitempty"`
	Model  struct {
		Name     string `json:"name,omitempty"`
		Provider string `json:"provider,omitempty"`
		BaseURL  string `json:"base_url,omitempty"`
		Model    string `json:"model"`
	} `json:"model"`
	Agent struct {
		Name   string `json:"name"`
		Prompt string `json:"prompt,omitempty"`
	} `json:"agent"`
}

type modelConfig struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Provider  string         `json:"provider"`
	BaseURL   string         `json:"base_url"`
	Model     string         `json:"model"`
	LastProbe map[string]any `json:"last_probe,omitempty"`
	ProbedAt  string         `json:"probed_at,omitempty"`
	CreatedAt string         `json:"created_at,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"`
}

type agentConfig struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Model          string   `json:"model"`
	Enabled        bool     `json:"enabled"`
	WebSearch      bool     `json:"web_search"`
	MCPEnabled     bool     `json:"mcp_enabled"`
	Tools          []string `json:"tools"`
	MaxSteps       int      `json:"max_steps"`
	MaxTokens      int      `json:"max_tokens"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	PromptVersion  int      `json:"prompt_version"`
	Prompt         string   `json:"prompt,omitempty"`
	CreatedAt      string   `json:"created_at,omitempty"`
	UpdatedAt      string   `json:"updated_at,omitempty"`
}

type mcpConfig struct {
	ID                    string   `json:"id"`
	Transport             string   `json:"transport"`
	Endpoint              string   `json:"endpoint,omitempty"`
	Name                  string   `json:"name"`
	Enabled               bool     `json:"enabled"`
	Command               string   `json:"command"`
	Args                  []string `json:"args"`
	WorkingDirectory      string   `json:"working_directory"`
	EnvironmentAllowlist  []string `json:"environment_allowlist"`
	AllowedTools          []string `json:"allowed_tools"`
	ReadTools             []string `json:"read_tools"`
	WriteTools            []string `json:"write_tools"`
	RequestTimeoutSeconds int      `json:"request_timeout_seconds"`
	RuntimeAvailable      bool     `json:"runtime_available"`
}

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return nil
	}
	var err error
	switch args[0] {
	case "setup":
		err = runSetup(args[1:], stdin, stdout, stderr)
	case "model":
		err = runModel(args[1:], stdout, stderr)
	case "agent":
		err = runAgent(args[1:], stdout, stderr)
	case "connect":
		err = runConnect(args[1:], stdout, stderr)
	case "audit":
		err = runAudit(args[1:], stdout, stderr)
	case "logs":
		err = runLogs(args[1:], stdout, stderr)
	case "pair", "devices", "update", "backup", "restore":
		return fmt.Errorf("%s is unavailable in this daemon build; no corresponding API endpoint exists", args[0])
	default:
		return fmt.Errorf("unknown command %q; run `ownbot --help`", args[0])
	}
	if errors.Is(err, errHelpRequested) {
		return nil
	}
	return err
}

func printHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Ownbot CLI (connects to a running local ownbotd)

Usage:
  ownbot setup [flags]                 Create a local model and disabled agent
  ownbot model list|add|probe          Manage model configuration and probes
  ownbot agent list|add|prompts|prompt Manage disabled agent metadata and prompts
  ownbot connect list|add|remove       Manage disabled MCP server metadata
  ownbot audit export [--out FILE]     Export currently available audit events as JSONL
  ownbot logs                          Unavailable: daemon has no logs API

Unavailable in this build: pair, devices, backup, restore, update.
These commands require daemon APIs that are not implemented. The CLI does not
collect API keys or provider credentials. Remote pairing is not available.

Common flags: --url URL (default http://127.0.0.1:8765), --data-dir DIR
The API token is read from DIR/api.token (default ~/.ownbot/api.token).
`)
}

func defaultAPIURL() string {
	if value := strings.TrimSpace(os.Getenv("OWNBOT_API_URL")); value != "" {
		return value
	}
	return DefaultAPIURL
}

func commonFlags(fs *flag.FlagSet) (*string, *string) {
	apiURL := fs.String("url", defaultAPIURL(), "loopback URL of a running ownbotd")
	dataDirDefault, _ := defaultDataDir()
	dataDir := fs.String("data-dir", dataDirDefault, "daemon state directory containing api.token")
	return apiURL, dataDir
}

func parseFlags(fs *flag.FlagSet, args []string, stderr io.Writer) error {
	fs.SetOutput(stderr)
	fs.Usage = func() { fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return errHelpRequested
		}
		return err
	}
	return nil
}

func clientFor(apiURL, dataDir string) (*Client, error) { return NewClient(apiURL, dataDir) }

func runSetup(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "loopback URL of a running ownbotd")
	dataDirDefault, _ := defaultDataDir()
	dataDir := fs.String("data-dir", dataDirDefault, "daemon state directory containing api.token")
	configPath := fs.String("config", "", "JSON setup file for unattended setup")
	provider := fs.String("provider", "", "local provider: ollama or lm_studio")
	baseURL := fs.String("base-url", "", "local OpenAI-compatible endpoint; optional for built-in local providers")
	modelName := fs.String("model-name", "", "display name for the model configuration")
	modelID := fs.String("model", "", "model identifier (required)")
	agentName := fs.String("agent-name", "", "name for the new disabled agent")
	prompt := fs.String("prompt", "", "initial system prompt (optional)")
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("setup does not accept positional arguments")
	}

	config := setupFile{}
	if *configPath != "" {
		file, err := os.Open(*configPath)
		if err != nil {
			return errors.New("could not open setup configuration file")
		}
		decoder := json.NewDecoder(file)
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&config)
		closeErr := file.Close()
		if err != nil {
			return fmt.Errorf("invalid setup configuration: %w", err)
		}
		if closeErr != nil {
			return errors.New("could not close setup configuration file")
		}
	}
	if *urlFlag != "" {
		config.APIURL = *urlFlag
	}
	if config.APIURL == "" {
		config.APIURL = defaultAPIURL()
	}
	if *provider != "" {
		config.Model.Provider = *provider
	}
	if *baseURL != "" {
		config.Model.BaseURL = *baseURL
	}
	if *modelName != "" {
		config.Model.Name = *modelName
	}
	if *modelID != "" {
		config.Model.Model = *modelID
	}
	if *agentName != "" {
		config.Agent.Name = *agentName
	}
	if *prompt != "" {
		config.Agent.Prompt = *prompt
	}
	if config.Model.Provider == "" {
		config.Model.Provider = "ollama"
	}

	client, err := clientFor(config.APIURL, *dataDir)
	if err != nil {
		return err
	}
	input := bufio.NewReader(stdin)
	if config.Model.Model == "" {
		config.Model.Model, err = ask(input, stdout, "Local model identifier", "")
		if err != nil {
			return errors.New("model identifier required; use --model or a complete --config file for unattended setup")
		}
	}
	if config.Model.Provider != "ollama" && config.Model.Provider != "lm_studio" {
		return errors.New("setup supports local providers ollama and lm_studio only")
	}
	if config.Agent.Name == "" {
		config.Agent.Name, err = ask(input, stdout, "Agent name", "Assistant")
		if err != nil {
			return errors.New("agent name required; use --agent-name or a complete --config file for unattended setup")
		}
	}
	if config.Model.Name == "" {
		config.Model.Name = config.Model.Provider + " · " + config.Model.Model
	}
	model := modelConfig{}
	if err := client.Do(backgroundContext(), "POST", "/api/models", map[string]string{
		"provider": config.Model.Provider,
		"name":     config.Model.Name,
		"base_url": config.Model.BaseURL,
		"model":    config.Model.Model,
	}, &model); err != nil {
		return fmt.Errorf("could not save model configuration: %w", err)
	}

	agent := agentConfig{}
	if err := client.Do(backgroundContext(), "POST", "/api/agents", map[string]any{
		"name":    config.Agent.Name,
		"model":   config.Model.Model,
		"enabled": false,
		"tools":   []string{},
		"prompt":  config.Agent.Prompt,
	}, &agent); err != nil {
		return fmt.Errorf("model %q was saved, but disabled agent creation failed: %w", model.Name, err)
	}
	_, _ = fmt.Fprintf(stdout, "Setup saved model %q and disabled agent %q. No model was probed and no agent was run.\n", model.Name, agent.Name)
	return nil
}

func ask(input *bufio.Reader, output io.Writer, label, defaultValue string) (string, error) {
	if defaultValue != "" {
		_, _ = fmt.Fprintf(output, "%s [%s]: ", label, defaultValue)
	} else {
		_, _ = fmt.Fprintf(output, "%s: ", label)
	}
	line, err := input.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" && defaultValue != "" {
		value = defaultValue
	}
	if value == "" {
		return "", io.EOF
	}
	return value, nil
}

func runModel(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printModelHelp(stdout)
		return nil
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("model list", flag.ContinueOnError)
		apiURL, dataDir := commonFlags(fs)
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("model list does not accept positional arguments")
		}
		client, err := clientFor(*apiURL, *dataDir)
		if err != nil {
			return err
		}
		var configs []modelConfig
		if err := client.Do(backgroundContext(), "GET", "/api/models", nil, &configs); err != nil {
			return err
		}
		return writeJSON(stdout, configs)
	case "add":
		fs := flag.NewFlagSet("model add", flag.ContinueOnError)
		apiURL, dataDir := commonFlags(fs)
		provider := fs.String("provider", "ollama", "ollama, lm_studio, or openai_compatible")
		name := fs.String("name", "", "optional display name")
		baseURL := fs.String("base-url", "", "endpoint URL; HTTP must be loopback, remote endpoints require HTTPS")
		modelID := fs.String("model", "", "model identifier (required)")
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("model add does not accept positional arguments")
		}
		if strings.TrimSpace(*modelID) == "" {
			return errors.New("model add requires --model")
		}
		if *provider != "ollama" && *provider != "lm_studio" && *provider != "openai_compatible" {
			return errors.New("provider must be ollama, lm_studio, or openai_compatible")
		}
		client, err := clientFor(*apiURL, *dataDir)
		if err != nil {
			return err
		}
		var model modelConfig
		body := map[string]string{"provider": *provider, "name": *name, "base_url": *baseURL, "model": *modelID}
		if err := client.Do(backgroundContext(), "POST", "/api/models", body, &model); err != nil {
			return err
		}
		return writeJSON(stdout, model)
	case "probe":
		fs := flag.NewFlagSet("model probe", flag.ContinueOnError)
		apiURL, dataDir := commonFlags(fs)
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: ownbot model probe MODEL_CONFIG_ID [--url URL] [--data-dir DIR]")
		}
		client, err := clientFor(*apiURL, *dataDir)
		if err != nil {
			return err
		}
		var result map[string]any
		if err := client.Do(backgroundContext(), "POST", "/api/models/"+urlPath(fs.Arg(0))+"/probe", map[string]any{}, &result); err != nil {
			return err
		}
		_, _ = io.WriteString(stdout, "Capability probe result (diagnostic only; it does not run an agent):\n")
		return writeJSON(stdout, result)
	default:
		return fmt.Errorf("unknown model command %q; run `ownbot model --help`", args[0])
	}
}

func printModelHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Model commands:
  ownbot model list [--url URL] [--data-dir DIR]
  ownbot model add --provider ollama|lm_studio|openai_compatible --model ID [--name NAME] [--base-url URL]
  ownbot model probe [--url URL] [--data-dir DIR] MODEL_CONFIG_ID

Probe records observed capability checks only; this CLI does not attach a model to a running agent.
No API key is collected. Hosted providers requiring credentials cannot be probed by this command.
`)
}

func runAgent(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printAgentHelp(stdout)
		return nil
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("agent list", flag.ContinueOnError)
		apiURL, dataDir := commonFlags(fs)
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("agent list does not accept positional arguments")
		}
		client, err := clientFor(*apiURL, *dataDir)
		if err != nil {
			return err
		}
		var agents []agentConfig
		if err := client.Do(backgroundContext(), "GET", "/api/agents", nil, &agents); err != nil {
			return err
		}
		return writeJSON(stdout, agents)
	case "add":
		fs := flag.NewFlagSet("agent add", flag.ContinueOnError)
		apiURL, dataDir := commonFlags(fs)
		name := fs.String("name", "", "agent name (required)")
		modelID := fs.String("model", "", "model identifier")
		prompt := fs.String("prompt", "", "initial system prompt")
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return err
		}
		if strings.TrimSpace(*name) == "" {
			return errors.New("agent add requires --name")
		}
		client, err := clientFor(*apiURL, *dataDir)
		if err != nil {
			return err
		}
		var agent agentConfig
		body := map[string]any{"name": *name, "model": *modelID, "enabled": false, "tools": []string{}, "prompt": *prompt}
		if err := client.Do(backgroundContext(), "POST", "/api/agents", body, &agent); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Created disabled agent %q. It has not been run.\n", agent.Name)
		return writeJSON(stdout, agent)
	case "prompts":
		if len(args) < 2 {
			return errors.New("usage: ownbot agent prompts AGENT_ID")
		}
		fs := flag.NewFlagSet("agent prompts", flag.ContinueOnError)
		apiURL, dataDir := commonFlags(fs)
		if err := parseFlags(fs, args[2:], stderr); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("agent prompts does not accept extra positional arguments")
		}
		client, err := clientFor(*apiURL, *dataDir)
		if err != nil {
			return err
		}
		var prompts []map[string]any
		if err := client.Do(backgroundContext(), "GET", "/api/agents/"+urlPath(args[1])+"/prompts", nil, &prompts); err != nil {
			return err
		}
		return writeJSON(stdout, prompts)
	case "prompt":
		return runAgentPrompt(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown agent command %q; run `ownbot agent --help`", args[0])
	}
}

func runAgentPrompt(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printAgentHelp(stdout)
		return nil
	}
	switch args[0] {
	case "set":
		fs := flag.NewFlagSet("agent prompt set", flag.ContinueOnError)
		apiURL, dataDir := commonFlags(fs)
		id := fs.String("id", "", "agent ID (required)")
		prompt := fs.String("text", "", "new system prompt (required)")
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("agent prompt set does not accept positional arguments")
		}
		if *id == "" || strings.TrimSpace(*prompt) == "" {
			return errors.New("agent prompt set requires --id and --text")
		}
		client, err := clientFor(*apiURL, *dataDir)
		if err != nil {
			return err
		}
		var result map[string]any
		if err := client.Do(backgroundContext(), "POST", "/api/agents/"+urlPath(*id)+"/prompts", map[string]string{"prompt": *prompt}, &result); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Saved prompt version %v. It is not applied to an agent runtime in this build.\n", result["version"])
		return writeJSON(stdout, result)
	case "rollback":
		fs := flag.NewFlagSet("agent prompt rollback", flag.ContinueOnError)
		apiURL, dataDir := commonFlags(fs)
		id := fs.String("id", "", "agent ID (required)")
		version := fs.Int("version", 0, "prompt version to restore (required)")
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("agent prompt rollback does not accept positional arguments")
		}
		if *id == "" || *version < 1 {
			return errors.New("agent prompt rollback requires --id and a positive --version")
		}
		client, err := clientFor(*apiURL, *dataDir)
		if err != nil {
			return err
		}
		var result map[string]any
		path := "/api/agents/" + urlPath(*id) + "/prompts/rollback"
		if err := client.Do(backgroundContext(), "POST", path, map[string]int{"version": *version}, &result); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Restored prompt version %d as the current prompt metadata. No agent runtime was changed.\n", *version)
		return writeJSON(stdout, result)
	default:
		return fmt.Errorf("unknown prompt command %q; run `ownbot agent --help`", args[0])
	}
}

func printAgentHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Agent commands:
  ownbot agent list
  ownbot agent add --name NAME [--model MODEL_ID] [--prompt TEXT]
  ownbot agent prompts AGENT_ID
  ownbot agent prompt set --id AGENT_ID --text PROMPT
  ownbot agent prompt rollback --id AGENT_ID --version VERSION

Agents created here are disabled. Prompts are versioned metadata; no agent runtime is available.
`)
}

func runConnect(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printConnectHelp(stdout)
		return nil
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("connect list", flag.ContinueOnError)
		apiURL, dataDir := commonFlags(fs)
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("connect list does not accept positional arguments")
		}
		client, err := clientFor(*apiURL, *dataDir)
		if err != nil {
			return err
		}
		var configs []mcpConfig
		if err := client.Do(backgroundContext(), "GET", "/api/mcp/servers", nil, &configs); err != nil {
			return err
		}
		return writeJSON(stdout, configs)
	case "add":
		fs := flag.NewFlagSet("connect add", flag.ContinueOnError)
		apiURL, dataDir := commonFlags(fs)
		name := fs.String("name", "", "server name (required)")
		transport := fs.String("transport", "stdio", "stdio or streamable_http")
		command := fs.String("command", "", "local server command for stdio transport")
		endpoint := fs.String("endpoint", "", "HTTPS or loopback endpoint for streamable_http")
		workdir := fs.String("working-directory", "", "working directory for stdio process")
		timeout := fs.Int("timeout", 30, "request timeout in seconds (1-300)")
		var argsList, envList, tools, readTools, writeTools stringList
		fs.Var(&argsList, "arg", "stdio argument (repeatable; credentials are not accepted)")
		fs.Var(&envList, "env", "environment variable name to allow (repeatable; values are not accepted)")
		fs.Var(&tools, "tool", "allowed tool name (repeatable)")
		fs.Var(&readTools, "read-tool", "read tool name (repeatable)")
		fs.Var(&writeTools, "write-tool", "write tool name (repeatable)")
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("connect add does not accept positional arguments")
		}
		if strings.TrimSpace(*name) == "" {
			return errors.New("connect add requires --name")
		}
		if *transport != "stdio" && *transport != "streamable_http" {
			return errors.New("--transport must be stdio or streamable_http")
		}
		client, err := clientFor(*apiURL, *dataDir)
		if err != nil {
			return err
		}
		body := map[string]any{
			"name": *name, "transport": *transport, "command": *command,
			"endpoint": *endpoint, "working_directory": *workdir,
			"args": []string(argsList), "environment_allowlist": []string(envList),
			"allowed_tools": []string(tools), "read_tools": []string(readTools), "write_tools": []string(writeTools),
			"request_timeout_seconds": *timeout, "enabled": false,
		}
		var config mcpConfig
		if err := client.Do(backgroundContext(), "POST", "/api/mcp/servers", body, &config); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Saved disabled metadata for %q. No provider connection or tool is active.\n", config.Name)
		return writeJSON(stdout, config)
	case "remove":
		fs := flag.NewFlagSet("connect remove", flag.ContinueOnError)
		apiURL, dataDir := commonFlags(fs)
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: ownbot connect remove [--url URL] [--data-dir DIR] SERVER_ID")
		}
		client, err := clientFor(*apiURL, *dataDir)
		if err != nil {
			return err
		}
		if err := client.Do(backgroundContext(), "DELETE", "/api/mcp/servers/"+urlPath(fs.Arg(0)), nil, nil); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, "Removed saved MCP server metadata. No running process was stopped.")
		return nil
	default:
		return fmt.Errorf("unknown connect command %q; run `ownbot connect --help`", args[0])
	}
}

func printConnectHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Connect commands manage saved, disabled MCP metadata only:
  ownbot connect list
  ownbot connect add --name NAME [--transport stdio --command PATH] [repeatable --arg, --tool, --read-tool, --write-tool, --env]
  ownbot connect add --name NAME --transport streamable_http --endpoint URL
  ownbot connect remove SERVER_ID

OAuth, provider sign-in, credential collection, starting MCP servers, and tool calls are unavailable.
`)
}

func runAudit(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, _ = fmt.Fprintln(stdout, "Usage: ownbot audit export [--out FILE] [--url URL] [--data-dir DIR]")
		return nil
	}
	if args[0] != "export" {
		return fmt.Errorf("unknown audit command %q; run `ownbot audit --help`", args[0])
	}
	fs := flag.NewFlagSet("audit export", flag.ContinueOnError)
	apiURL, dataDir := commonFlags(fs)
	outputPath := fs.String("out", "", "write JSONL to this file instead of stdout")
	if err := parseFlags(fs, args[1:], stderr); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("audit export does not accept positional arguments")
	}
	client, err := clientFor(*apiURL, *dataDir)
	if err != nil {
		return err
	}
	var events []json.RawMessage
	if err := client.Do(backgroundContext(), "GET", "/api/audit", nil, &events); err != nil {
		return err
	}
	var dst io.Writer = stdout
	var file *os.File
	if *outputPath != "" {
		file, err = os.OpenFile(filepath.Clean(*outputPath), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return errors.New("could not open audit export file")
		}
		defer file.Close()
		dst = file
	}
	if _, err := fmt.Fprintln(stderr, "Exporting audit events available from the daemon; tool-call audit history is not implemented."); err != nil {
		return err
	}
	return writeJSONLines(dst, events)
}

func runLogs(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	commonFlags(fs)
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("logs does not accept positional arguments")
	}
	_, _ = fmt.Fprintln(stdout, "Daemon logs are not available through the current API; inspect the ownbotd process stdout/stderr.")
	return errors.New("logs command unavailable: daemon has no /api/logs endpoint")
}

func urlPath(value string) string { return url.PathEscape(value) }
