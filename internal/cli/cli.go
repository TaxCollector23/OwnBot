package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"ownbot/internal/mcp"
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

type optionalBool string

func (b *optionalBool) String() string { return string(*b) }
func (b *optionalBool) Set(value string) error {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return errors.New("value must be true or false")
	}
	*b = optionalBool(strconv.FormatBool(parsed))
	return nil
}

type setupFile struct {
	APIURL string `json:"api_url,omitempty" toml:"api_url"`
	Model  struct {
		Name     string `json:"name,omitempty" toml:"name"`
		Provider string `json:"provider,omitempty" toml:"provider"`
		BaseURL  string `json:"base_url,omitempty" toml:"base_url"`
		Model    string `json:"model" toml:"model"`
	} `json:"model" toml:"model"`
	Agent struct {
		Name   string `json:"name" toml:"name"`
		Prompt string `json:"prompt,omitempty" toml:"prompt"`
	} `json:"agent" toml:"agent"`
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
	ID                    string       `json:"id"`
	Transport             string       `json:"transport"`
	Endpoint              string       `json:"endpoint,omitempty"`
	Name                  string       `json:"name"`
	Enabled               bool         `json:"enabled"`
	Command               string       `json:"command"`
	Args                  []string     `json:"args"`
	WorkingDirectory      string       `json:"working_directory"`
	EnvironmentAllowlist  []string     `json:"environment_allowlist"`
	AllowedTools          []string     `json:"allowed_tools"`
	ReadTools             []string     `json:"read_tools"`
	WriteTools            []string     `json:"write_tools"`
	RequestTimeoutSeconds int          `json:"request_timeout_seconds"`
	RuntimeAvailable      bool         `json:"runtime_available"`
	OAuthAuthorized       bool         `json:"oauth_authorized"`
	DiscoveredTools       []toolConfig `json:"discovered_tools,omitempty"`
}

type toolConfig struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Risk        string `json:"risk"`
	Source      string `json:"source"`
	Server      string `json:"server"`
}

type searchConfig struct {
	Provider            string `json:"provider"`
	Endpoint            string `json:"endpoint"`
	Configured          bool   `json:"configured"`
	RequestsLeaveDevice bool   `json:"requests_leave_device"`
}

type runResult struct {
	ID            string `json:"id"`
	AgentID       string `json:"agent_id"`
	Status        string `json:"status"`
	PromptVersion int    `json:"prompt_version"`
	Steps         any    `json:"steps,omitempty"`
	Tainted       bool   `json:"tainted,omitempty"`
	Output        any    `json:"output,omitempty"`
	Error         string `json:"error,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return runHarness(stdin, stdout, stderr)
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return nil
	}
	var err error
	switch args[0] {
	case "setup":
		err = runSetup(args[1:], stdin, stdout, stderr)
	case "daemon":
		if len(args) > 1 && (args[1] == "pair" || args[1] == "devices") {
			return runDaemonAccess(args[1:], stdout, stderr)
		}
		return runDaemon(args[1:], stdout, stderr)
	case "inspect":
		err = runInspect(args[1:], stdin, stdout, stderr)
	case "connect":
		err = runConnect(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q; run `ownbot --help`", args[0])
	}
	if errors.Is(err, errHelpRequested) {
		return nil
	}
	return err
}

func startDaemon(args []string, apiURL, dataDir string, stdout io.Writer, open bool) error {
	if daemonHealthy(apiURL) {
		_, _ = fmt.Fprintln(stdout, "OwnBot daemon is already running.")
		if open {
			return openOwnerDashboard(apiURL, dataDir)
		}
		return nil
	}
	available, err := daemonAddressAvailable(apiURL)
	if err != nil {
		return err
	}
	if !available {
		return errors.New("listener address is occupied by a service that did not identify itself as OwnBot; it was left untouched. Choose another address with --listen")
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate OwnBot CLI: %w", err)
	}
	daemonName := "ownbotd"
	if os.PathSeparator == '\\' {
		daemonName += ".exe"
	}
	daemonPath := filepath.Join(filepath.Dir(executable), daemonName)
	if _, err := os.Stat(daemonPath); err != nil {
		return errors.New("daemon binary is missing; reinstall OwnBot or rebuild both binaries")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	logPath := filepath.Join(dataDir, "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	if err := logFile.Chmod(0600); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("secure daemon log: %w", err)
	}
	cmd := exec.Command(daemonPath, args...)
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = logFile, logFile
	configureDetachedProcess(cmd)
	if err = cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start OwnBot daemon: %w", err)
	}
	_ = logFile.Close()
	_ = cmd.Process.Release()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if daemonHealthy(apiURL) {
			_, _ = fmt.Fprintf(stdout, "OwnBot daemon is ready at %s\n", apiURL)
			if open {
				return openOwnerDashboard(apiURL, dataDir)
			}
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not become ready; inspect %s and check its state directory and listener configuration", logPath)
}

func printWelcome(w io.Writer) {
	file, ok := w.(*os.File)
	color := false
	if ok && os.Getenv("NO_COLOR") == "" && !strings.EqualFold(os.Getenv("TERM"), "dumb") {
		if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			color = true
		}
	}
	if color {
		_, _ = io.WriteString(w, "\x1b[38;5;39mOwnBot\x1b[0m\n\n")
	} else {
		_, _ = io.WriteString(w, "OwnBot\n\n")
	}
}

func printHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage: ownbot <command>

Commands:

  setup         Detect local models, probe one, and create a tool-free agent
  connect       Configure public MCP integrations (disabled by default)
  daemon start  Start the local daemon and open its dashboard
  daemon stop   Stop the local daemon
  daemon pair|devices  Pair and manage dashboard devices
  daemon install|uninstall|status  Manage the current user's login service
  inspect       Review hardware, model probes, agents, and tool permissions
  help          Show this list

Use ownbot <command> --help for command options. Run without a command to enter the task harness.
`)
}

func defaultAPIURL() string {
	if value := strings.TrimSpace(os.Getenv("OWNBOT_API_URL")); value != "" {
		return value
	}
	return DefaultAPIURL
}

func commonFlags(fs *flag.FlagSet) (*string, *string) {
	apiURL := fs.String("url", defaultAPIURL(), "loopback URL of the local daemon")
	dataDirDefault, _ := defaultDataDir()
	dataDir := fs.String("data-dir", dataDirDefault, "daemon state directory containing api.token")
	return apiURL, dataDir
}

func parseFlags(fs *flag.FlagSet, args []string, stderr io.Writer) error {
	fs.SetOutput(stderr)
	if fs.Usage == nil {
		fs.Usage = func() { fs.PrintDefaults() }
	}
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
	if len(args) > 0 && (args[0] == "automation" || args[0] == "automations") {
		return runAutomationSetup(args[1:], stdout, stderr)
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "model":
			return runModel(args[1:], stdout, stderr)
		case "agent":
			return runAgent(args[1:], stdin, stdout, stderr)
		case "search":
			return runSearch(args[1:], stdout, stderr)
		}
	}
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "loopback URL of the local daemon")
	dataDirDefault, _ := defaultDataDir()
	dataDir := fs.String("data-dir", dataDirDefault, "daemon state directory containing api.token")
	configPath := fs.String("config", "", "TOML or JSON setup file for unattended setup")
	yes := fs.Bool("yes", false, "accept only unambiguous local setup choices without prompting")
	provider := fs.String("provider", "", "local provider: ollama, lm_studio, llama.cpp, or vllm")
	baseURL := fs.String("base-url", "", "local loopback OpenAI-compatible endpoint")
	modelName := fs.String("model-name", "", "display name for the model configuration")
	modelID := fs.String("model", "", "model identifier (required)")
	agentName := fs.String("agent-name", "", "name for the new tool-free agent")
	prompt := fs.String("prompt", "", "initial system prompt (optional)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: ownbot setup [--yes] [--model INSTALLED_ID] [--config setup.toml]")
		_, _ = fmt.Fprintln(stderr, "TOML config: [model] with optional provider/base_url/model, then [agent] with name/prompt.")
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("setup does not accept positional arguments")
	}

	config := setupFile{}
	var err error
	if *configPath != "" {
		file, err := os.Open(*configPath)
		if err != nil {
			return errors.New("could not open setup configuration file")
		}
		content, readErr := io.ReadAll(io.LimitReader(file, 1<<20))
		closeErr := file.Close()
		if readErr != nil {
			return errors.New("could not read setup configuration file")
		}
		if closeErr != nil {
			return errors.New("could not close setup configuration file")
		}
		if strings.HasPrefix(strings.TrimSpace(string(content)), "{") {
			decoder := json.NewDecoder(strings.NewReader(string(content)))
			decoder.DisallowUnknownFields()
			err = decoder.Decode(&config)
		} else {
			meta, decodeErr := toml.Decode(string(content), &config)
			err = decodeErr
			if err == nil && len(meta.Undecoded()) != 0 {
				keys := meta.Undecoded()
				var fields []string
				for _, key := range keys {
					fields = append(fields, key.String())
				}
				err = fmt.Errorf("unknown setup fields: %s", strings.Join(fields, ", "))
			}
		}
		if err != nil {
			return fmt.Errorf("invalid setup configuration: %w", err)
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
	input := bufio.NewReader(stdin)
	if memory, ok := availableMemory(); ok {
		_, _ = fmt.Fprintf(stdout, "Available memory reported by the operating system: %.1f GiB\n", float64(memory)/(1024*1024*1024))
	} else {
		_, _ = fmt.Fprintln(stdout, "Available memory: unavailable from this operating system interface")
	}
	_, _ = fmt.Fprintln(stdout, gpuMemoryStatus())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var installed []localModel
	if config.Model.BaseURL != "" {
		installed, err = discoverAtBaseURL(ctx, config.Model.BaseURL)
		if err != nil {
			return err
		}
		if config.Model.Provider != "" {
			providerName, normalizeErr := normalizeSetupProvider(config.Model.Provider)
			if normalizeErr != nil {
				return normalizeErr
			}
			for i := range installed {
				installed[i].Provider = providerName
			}
		}
	} else {
		installed = detectLocalModels(ctx)
	}
	if len(installed) == 0 {
		return offerOllama(input, stdout, *yes)
	}
	_, _ = fmt.Fprintln(stdout, "Detected installed local models:")
	for _, candidate := range installed {
		_, _ = fmt.Fprintf(stdout, "  %s: %s\n", candidate.Runtime, candidate.ID)
	}
	if config.Model.Provider != "" {
		selectedProvider, normalizeErr := normalizeSetupProvider(config.Model.Provider)
		if normalizeErr != nil {
			return normalizeErr
		}
		filtered := installed[:0]
		for _, candidate := range installed {
			if candidate.Provider == selectedProvider {
				filtered = append(filtered, candidate)
			}
		}
		installed = filtered
	}
	if config.Model.Model != "" {
		var selected *localModel
		for i := range installed {
			if installed[i].ID == config.Model.Model {
				selected = &installed[i]
				break
			}
		}
		if selected == nil {
			return fmt.Errorf("model %q is not installed in a detected local service", config.Model.Model)
		}
		config.Model.Provider, config.Model.BaseURL = selected.Provider, selected.BaseURL
	} else if len(installed) == 1 {
		config.Model.Model, config.Model.Provider, config.Model.BaseURL = installed[0].ID, installed[0].Provider, installed[0].BaseURL
	} else {
		_, _ = fmt.Fprintln(stdout, "Choose an installed model by number:")
		for i, candidate := range installed {
			_, _ = fmt.Fprintf(stdout, "  %d) %s — %s (%s)\n", i+1, candidate.ID, candidate.Runtime, candidate.Provider)
		}
		choice := "1"
		if *yes {
			_, _ = fmt.Fprintf(stdout, "Using first detected installed model for --yes: %s\n", installed[0].ID)
		} else {
			choice, err = ask(input, stdout, "Model number", "1")
			if err != nil {
				return errors.New("choose an installed model with --model or complete setup interactively")
			}
		}
		index, parseErr := strconv.Atoi(choice)
		if parseErr != nil || index < 1 || index > len(installed) {
			return errors.New("invalid model selection")
		}
		selected := installed[index-1]
		config.Model.Model, config.Model.Provider, config.Model.BaseURL = selected.ID, selected.Provider, selected.BaseURL
	}
	if config.Agent.Name == "" {
		if *yes {
			config.Agent.Name = "Assistant"
		} else {
			config.Agent.Name, err = ask(input, stdout, "Agent name", "Assistant")
			if err != nil {
				return errors.New("agent name required; use --agent-name or a complete --config file for unattended setup")
			}
		}
	}
	setupNotice := ""
	if !daemonHealthy(config.APIURL) {
		if strings.TrimRight(config.APIURL, "/") != strings.TrimRight(defaultAPIURL(), "/") {
			return errors.New("local daemon is not running at the configured API URL; start it with `ownbot daemon start`")
		}
		args, err := setupDaemonArgs(config.APIURL, *dataDir)
		if err != nil {
			return err
		}
		setupNotice, err = setupManagedDaemon(args, config.APIURL, *dataDir, stdout)
		if err != nil {
			return err
		}
	}
	client, err := clientFor(config.APIURL, *dataDir)
	if err != nil {
		return err
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
	var probe struct {
		Probe struct {
			Score            int  `json:"score"`
			JSONOutput       bool `json:"json_output"`
			SingleTool       bool `json:"single_tool"`
			ToolChoice       bool `json:"tool_choice"`
			ArgumentAccuracy bool `json:"argument_accuracy"`
			Restricted       bool `json:"restricted"`
		} `json:"probe"`
	}
	if err := client.Do(backgroundContext(), "POST", "/api/models/"+urlPath(model.ID)+"/probe", map[string]any{}, &probe); err != nil {
		return fmt.Errorf("model %q was saved but capability probing failed: %w", model.Name, err)
	}
	if probe.Probe.Restricted || probe.Probe.Score != 100 || !probe.Probe.JSONOutput || !probe.Probe.SingleTool || !probe.Probe.ToolChoice || !probe.Probe.ArgumentAccuracy {
		return fmt.Errorf("model %q failed one or more required capability checks; no agent was created", model.Name)
	}

	agent := agentConfig{}
	if err := client.Do(backgroundContext(), "POST", "/api/agents", map[string]any{
		"name":    config.Agent.Name,
		"model":   config.Model.Model,
		"enabled": true,
		"tools":   []string{},
		"prompt":  config.Agent.Prompt,
	}, &agent); err != nil {
		return fmt.Errorf("model %q was saved, but tool-free agent creation failed: %w", model.Name, err)
	}
	_, _ = fmt.Fprintf(stdout, "Setup saved and probed model %q. Created enabled, text-only agent %q with no tools or external integrations. No task was run.\n%sNext: ownbot daemon start\n", model.Name, agent.Name, setupNotice)
	return nil
}

func normalizeSetupProvider(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "ollama":
		return "ollama", nil
	case "lm_studio", "lm studio", "lm-studio":
		return "lm_studio", nil
	case "llama.cpp", "llama_cpp", "llamacpp", "vllm", "vllm-server", "openai_compatible":
		return "openai_compatible", nil
	default:
		return "", errors.New("setup supports Ollama, llama.cpp, LM Studio, and vLLM on loopback")
	}
}

func offerOllama(input *bufio.Reader, stdout io.Writer, yes bool) error {
	if yes {
		return errors.New("no local model runtime or installed model was detected; install Ollama from https://ollama.com/download, run `ollama pull <model-id>` for a model you choose, then rerun `ownbot setup --yes`; no model was selected or downloaded")
	}
	answer, err := ask(input, stdout, "No local runtime/model found. Would you like to set up Ollama? (y/N)", "N")
	if err != nil {
		answer = "N"
	}
	if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
		return errors.New("no local model runtime detected; setup stopped without installing or selecting a model")
	}
	_, _ = fmt.Fprintln(stdout, "Install Ollama from https://ollama.com/download, start it, then run `ollama pull <model-id>` for a model you choose. Rerun `ownbot setup` after it appears in Ollama's installed model list.")
	openURL("https://ollama.com/download")
	return errors.New("Ollama installation is user-run; no model was downloaded or selected")
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
			return errors.New("setup model list does not accept positional arguments")
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
			return errors.New("setup model add does not accept positional arguments")
		}
		if strings.TrimSpace(*modelID) == "" {
			return errors.New("setup model add requires --model")
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
			return errors.New("usage: ownbot setup model probe MODEL_CONFIG_ID [--url URL] [--data-dir DIR]")
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
		return fmt.Errorf("unknown model command %q; run `ownbot setup model --help`", args[0])
	}
}

func printModelHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Model commands:
  ownbot setup model list [--url URL] [--data-dir DIR]
  ownbot setup model add --provider ollama|lm_studio|openai_compatible --model ID [--name NAME] [--base-url URL]
  ownbot setup model probe [--url URL] [--data-dir DIR] MODEL_CONFIG_ID

Probe records observed capability checks only; this CLI does not attach a model to a running agent.
No API key is collected. Hosted providers requiring credentials cannot be probed by this command.
`)
}

func runAgent(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
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
			return errors.New("setup agent list does not accept positional arguments")
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
			return errors.New("setup agent add requires --name")
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
			return errors.New("usage: ownbot setup agent prompts AGENT_ID")
		}
		fs := flag.NewFlagSet("agent prompts", flag.ContinueOnError)
		apiURL, dataDir := commonFlags(fs)
		if err := parseFlags(fs, args[2:], stderr); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("setup agent prompts does not accept extra positional arguments")
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
	case "run":
		return runAgentStart(args[1:], stdin, stdout, stderr)
	case "update":
		return runAgentUpdate(args[1:], stdout, stderr)
	case "prompt":
		return runAgentPrompt(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown agent command %q; run `ownbot setup agent --help`", args[0])
	}
}

func runAgentStart(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("agent run", flag.ContinueOnError)
	apiURL, dataDir := commonFlags(fs)
	id := fs.String("id", "", "enabled agent ID (required)")
	input := fs.String("input", "", "user input, or - to read stdin (required)")
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("setup agent run does not accept positional arguments")
	}
	if *id == "" || *input == "" {
		return errors.New("setup agent run requires --id and --input")
	}
	if *input == "-" {
		contents, err := io.ReadAll(io.LimitReader(stdin, 64001))
		if err != nil || len(contents) == 0 || len(contents) > 64000 {
			return errors.New("stdin input must contain 1 to 64000 bytes")
		}
		*input = string(contents)
	}
	client, err := clientFor(*apiURL, *dataDir)
	if err != nil {
		return err
	}
	var result runResult
	path := "/api/agents/" + urlPath(*id) + "/runs"
	if err := client.Do(backgroundContext(), "POST", path, map[string]string{"input": *input}, &result); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "Run %s: %s (prompt version %d)\n", result.ID, result.Status, result.PromptVersion)
	return writeJSON(stdout, result)
}

func runAgentUpdate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("agent update", flag.ContinueOnError)
	apiURL, dataDir := commonFlags(fs)
	id := fs.String("id", "", "agent ID (required)")
	name := fs.String("name", "", "new display name")
	model := fs.String("model", "", "configured model ID")
	enabled := fs.Bool("enabled", false, "enable the agent for new runs")
	disabled := fs.Bool("disabled", false, "disable the agent for new runs")
	webSearch := optionalBool("")
	mcpEnabled := optionalBool("")
	fs.Var(&webSearch, "web-search", "set web search capability true or false")
	fs.Var(&mcpEnabled, "mcp", "set MCP capability true or false")
	var tools, servers stringList
	fs.Var(&tools, "tool", "allowed tool name (repeatable; exact discovered name)")
	fs.Var(&servers, "mcp-server", "connected MCP server ID to allow (repeatable)")
	clearTools := fs.Bool("clear-tools", false, "remove all tool permissions")
	clearServers := fs.Bool("clear-mcp-servers", false, "remove all MCP server permissions")
	steps := fs.Int("max-steps", 0, "run step limit")
	tokens := fs.Int("max-tokens", 0, "run token limit")
	timeout := fs.Int("timeout", 0, "run timeout in seconds")
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("setup agent update does not accept positional arguments")
	}
	if *id == "" || (*enabled && *disabled) {
		return errors.New("agent update requires --id and cannot use both --enabled and --disabled")
	}
	body := map[string]any{}
	if *name != "" {
		body["name"] = *name
	}
	if *model != "" {
		body["model"] = *model
	}
	if *enabled {
		body["enabled"] = true
	}
	if *disabled {
		body["enabled"] = false
	}
	if webSearch != "" {
		body["web_search"], _ = strconv.ParseBool(string(webSearch))
	}
	if mcpEnabled != "" {
		body["mcp_enabled"], _ = strconv.ParseBool(string(mcpEnabled))
	}
	if len(tools) > 0 || *clearTools {
		body["tools"] = []string(tools)
	}
	if len(servers) > 0 || *clearServers {
		body["mcp_servers"] = []string(servers)
	}
	if *steps != 0 {
		body["max_steps"] = *steps
	}
	if *tokens != 0 {
		body["max_tokens"] = *tokens
	}
	if *timeout != 0 {
		body["timeout_seconds"] = *timeout
	}
	if len(body) == 0 {
		return errors.New("agent update requires at least one configuration flag")
	}
	client, err := clientFor(*apiURL, *dataDir)
	if err != nil {
		return err
	}
	var agent agentConfig
	if err := client.Do(backgroundContext(), "PATCH", "/api/agents/"+urlPath(*id), body, &agent); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "Updated agent %q. Configuration applies to future runs; no provider call was made.\n", agent.Name)
	return writeJSON(stdout, agent)
}

func runRuns(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, _ = fmt.Fprintln(stdout, "Usage: ownbot runs list [flags] | show [flags] RUN_ID | resume [flags] RUN_ID")
		return nil
	}
	fs := flag.NewFlagSet("runs "+args[0], flag.ContinueOnError)
	apiURL, dataDir := commonFlags(fs)
	if err := parseFlags(fs, args[1:], stderr); err != nil {
		return err
	}
	client, err := clientFor(*apiURL, *dataDir)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if fs.NArg() != 0 {
			return errors.New("runs list does not accept positional arguments")
		}
		var results []runResult
		if err := client.Do(backgroundContext(), "GET", "/api/runs", nil, &results); err != nil {
			return err
		}
		return writeJSON(stdout, results)
	case "show":
		return runByID(client, "GET", fs, stdout)
	case "resume":
		return runByID(client, "POST", fs, stdout)
	default:
		return fmt.Errorf("unknown runs command %q; run `ownbot runs --help`", args[0])
	}
}

func runByID(client *Client, method string, fs *flag.FlagSet, stdout io.Writer) error {
	if fs.NArg() != 1 {
		return errors.New("expected exactly one run ID")
	}
	id := urlPath(fs.Arg(0))
	path := "/api/runs/" + id
	var body any
	if method == "POST" {
		path += "/resume"
		body = map[string]any{}
	}
	var result runResult
	if err := client.Do(backgroundContext(), method, path, body, &result); err != nil {
		return err
	}
	if method == "POST" {
		_, _ = fmt.Fprintf(stdout, "Run %s: %s\n", result.ID, result.Status)
	}
	return writeJSON(stdout, result)
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
			return errors.New("setup agent prompt set does not accept positional arguments")
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
		_, _ = fmt.Fprintf(stdout, "Saved prompt version %v. Newly started runs will pin this version.\n", result["version"])
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
			return errors.New("setup agent prompt rollback does not accept positional arguments")
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
		return fmt.Errorf("unknown prompt command %q; run `ownbot setup agent --help`", args[0])
	}
}

func printAgentHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Agent commands:
  ownbot setup agent list
  ownbot setup agent run --id AGENT_ID --input TEXT
  ownbot setup agent add --name NAME [--model MODEL_ID] [--prompt TEXT]
  ownbot setup agent update --id ID [--enabled|--disabled] [--web-search=true|false] [--mcp=true|false] [--tool NAME] [--mcp-server ID]
  ownbot setup agent prompts AGENT_ID
  ownbot setup agent prompt set --id AGENT_ID --text PROMPT
  ownbot setup agent prompt rollback --id AGENT_ID --version VERSION

Agents created here are disabled. Configure and enable an agent before running it.
Runs require a saved prompt and a model capability probe that passes the unrestricted threshold. MCP server IDs and tools must be connected and discovered first.
`)
}

func runConnect(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printConnectHelp(stdout)
		return nil
	}
	switch args[0] {
	case "notion", "calendly", "calendar", "google_calendar", "gmail", "email":
		return runPublicMCPConnect(args, stdout, stderr)
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
		var tools []toolConfig
		if err := client.Do(backgroundContext(), "GET", "/api/tools", nil, &tools); err != nil {
			return fmt.Errorf("could not list discovered tools: %w", err)
		}
		for i := range configs {
			if !configs[i].RuntimeAvailable {
				continue
			}
			for _, tool := range tools {
				if tool.Server == configs[i].ID || tool.Server == configs[i].Name {
					configs[i].DiscoveredTools = append(configs[i].DiscoveredTools, tool)
				}
			}
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
		if *transport == "streamable_http" && (len(tools)+len(readTools)+len(writeTools) > 0) {
			if err := validateMCPToolClasses([]string(tools), []string(readTools), []string(writeTools)); err != nil {
				return err
			}
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
	case "discover":
		return runConnectDiscover(args[1:], stdout, stderr)
	case "allow":
		return runConnectAllow(args[1:], stdout, stderr)
	case "connect", "disconnect", "authorize", "clear-auth":
		fs := flag.NewFlagSet("connect "+args[0], flag.ContinueOnError)
		apiURL, dataDir := commonFlags(fs)
		sshTarget := fs.String("ssh-target", "", "SSH target to use when forwarding the loopback OAuth callback")
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: ownbot connect %s [--url URL] [--data-dir DIR] SERVER_ID", args[0])
		}
		client, err := clientFor(*apiURL, *dataDir)
		if err != nil {
			return err
		}
		id := urlPath(fs.Arg(0))
		var config mcpConfig
		path := "/api/mcp/servers/" + id + "/" + args[0]
		if args[0] == "authorize" {
			var auth struct {
				AuthorizationURL string `json:"authorization_url"`
				CallbackPort     int    `json:"callback_port"`
			}
			if err := client.Do(backgroundContext(), "POST", "/api/mcp/servers/"+id+"/oauth/start", map[string]any{}, &auth); err != nil {
				return err
			}
			if command, isSSH := oauthCallbackForward(auth.CallbackPort, *sshTarget); isSSH {
				_, _ = fmt.Fprintf(stdout, "Before opening the sign-in URL on your browser machine, start this additional loopback callback tunnel:\n%s\n", command)
			}
			_, _ = fmt.Fprintf(stdout, "Open this URL to sign in. The callback returns to loopback; no tools are granted to agents.\n%s\n", auth.AuthorizationURL)
			return nil
		}
		if args[0] == "clear-auth" {
			if err := client.Do(backgroundContext(), "POST", "/api/mcp/servers/"+id+"/oauth/clear", map[string]any{}, nil); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(stdout, "Cleared local OAuth authorization for %q.\n", id)
			return nil
		}
		if err := client.Do(backgroundContext(), "POST", path, map[string]any{}, &config); err != nil {
			return err
		}
		if args[0] == "connect" {
			var tools []toolConfig
			if err := client.Do(backgroundContext(), "GET", "/api/tools", nil, &tools); err != nil {
				return fmt.Errorf("server connected but discovered tool list is unavailable: %w", err)
			}
			for _, tool := range tools {
				if tool.Server == config.ID || tool.Server == config.Name {
					config.DiscoveredTools = append(config.DiscoveredTools, tool)
				}
			}
			_, _ = fmt.Fprintf(stdout, "Connected %q and discovered %d tools. No tools were called.\n", config.Name, len(config.DiscoveredTools))
		} else {
			_, _ = fmt.Fprintf(stdout, "Disconnected %q. Its tools are no longer available to agent runs.\n", config.Name)
		}
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
		_, _ = fmt.Fprintln(stdout, "Removed saved MCP server configuration.")
		return nil
	default:
		return fmt.Errorf("unknown connect command %q; run `ownbot connect --help`", args[0])
	}
}

func runPublicMCPConnect(args []string, stdout, stderr io.Writer) error {
	service, ok := mcp.LookupService(args[0])
	if !ok {
		return fmt.Errorf("unknown public MCP service %q", args[0])
	}
	if !service.OAuthAvailable {
		return errors.New(service.Name + " sign-in is deferred. " + service.Notice)
	}
	fs := flag.NewFlagSet("connect "+args[0], flag.ContinueOnError)
	apiURL, dataDir := commonFlags(fs)
	sshTarget := fs.String("ssh-target", "", "SSH target to use when forwarding the loopback OAuth callback")
	if err := parseFlags(fs, args[1:], stderr); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: ownbot connect %s [--ssh-target USER@HOST] [--url URL] [--data-dir DIR]", args[0])
	}
	client, err := clientFor(*apiURL, *dataDir)
	if err != nil {
		return err
	}
	var configs []mcpConfig
	if err := client.Do(backgroundContext(), "GET", "/api/mcp/servers", nil, &configs); err != nil {
		return err
	}
	var selected *mcpConfig
	for i := range configs {
		if configs[i].Transport == "streamable_http" && configs[i].Endpoint == service.Endpoint {
			selected = &configs[i]
			break
		}
	}
	if selected == nil {
		body := map[string]any{
			"name": service.Name, "transport": service.Transport, "endpoint": service.Endpoint,
			"allowed_tools": []string{}, "read_tools": []string{}, "write_tools": []string{},
			"request_timeout_seconds": 30, "enabled": false,
		}
		var config mcpConfig
		if err := client.Do(backgroundContext(), "POST", "/api/mcp/servers", body, &config); err != nil {
			return err
		}
		selected = &config
	}
	var auth struct {
		AuthorizationURL string `json:"authorization_url"`
		CallbackPort     int    `json:"callback_port"`
	}
	if err := client.Do(backgroundContext(), "POST", "/api/mcp/servers/"+urlPath(selected.ID)+"/oauth/start", map[string]any{}, &auth); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "Started sign-in for %s (server %s). This saves credentials only; no tools are connected or granted.\n", service.Name, selected.ID)
	if command, isSSH := oauthCallbackForward(auth.CallbackPort, *sshTarget); isSSH {
		_, _ = fmt.Fprintf(stdout, "Before opening the sign-in URL on your browser machine, start this additional loopback callback tunnel:\n%s\n", command)
	}
	_, _ = fmt.Fprintf(stdout, "Open this URL to sign in.\n%s\n", auth.AuthorizationURL)
	return nil
}

func resolveMCPConfig(client *Client, selector string) (*mcpConfig, error) {
	var configs []mcpConfig
	if err := client.Do(backgroundContext(), "GET", "/api/mcp/servers", nil, &configs); err != nil {
		return nil, err
	}
	requested := strings.TrimSpace(selector)
	selector = requested
	if service, ok := mcp.LookupService(selector); ok {
		selector = service.Endpoint
	}
	for i := range configs {
		if configs[i].ID == selector || configs[i].Endpoint == selector || strings.EqualFold(configs[i].Name, selector) {
			return &configs[i], nil
		}
	}
	return nil, fmt.Errorf("MCP connection %q was not found; run `ownbot connect list`", requested)
}

func runConnectDiscover(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("connect discover", flag.ContinueOnError)
	apiURL, dataDir := commonFlags(fs)
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: ownbot connect discover [--url URL] [--data-dir DIR] SERVICE_OR_SERVER_ID")
	}
	client, err := clientFor(*apiURL, *dataDir)
	if err != nil {
		return err
	}
	config, err := resolveMCPConfig(client, fs.Arg(0))
	if err != nil {
		return err
	}
	var result struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	path := "/api/mcp/servers/" + urlPath(config.ID) + "/tools/discover"
	if err := client.Do(backgroundContext(), "POST", path, map[string]any{}, &result); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "Discovered %d tools for %s. Metadata only; no tools were called or enabled.\n", len(result.Tools), config.Name)
	return writeJSON(stdout, result.Tools)
}

func runConnectAllow(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("connect allow", flag.ContinueOnError)
	apiURL, dataDir := commonFlags(fs)
	var readTools, writeTools stringList
	fs.Var(&readTools, "read-tool", "explicit read-only tool name (repeatable)")
	fs.Var(&writeTools, "write-tool", "explicit write tool name (repeatable; every call requires approval)")
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: ownbot connect allow [flags] SERVICE_OR_SERVER_ID")
	}
	client, err := clientFor(*apiURL, *dataDir)
	if err != nil {
		return err
	}
	config, err := resolveMCPConfig(client, fs.Arg(0))
	if err != nil {
		return err
	}
	reads, writes := []string(readTools), []string(writeTools)
	if err := validateMCPToolClasses(append(append([]string(nil), reads...), writes...), reads, writes); err != nil && len(reads)+len(writes) > 0 {
		return fmt.Errorf("invalid tool permissions: %w", err)
	}
	var result map[string]any
	path := "/api/mcp/servers/" + urlPath(config.ID) + "/permissions"
	if err := client.Do(backgroundContext(), "PATCH", path, map[string]any{"read_tools": reads, "write_tools": writes}, &result); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "Saved %d read and %d write tools for %s. This does not grant tools to any agent. Write calls still require approval.\n", len(reads), len(writes), config.Name)
	return writeJSON(stdout, result)
}

func validateMCPToolClasses(allowed, reads, writes []string) error {
	if len(allowed) == 0 {
		return errors.New("HTTP MCP requires an explicit --tool allowlist")
	}
	allowSet := map[string]bool{}
	for _, name := range allowed {
		if allowSet[name] {
			return fmt.Errorf("duplicate allowed tool %q", name)
		}
		allowSet[name] = true
	}
	readSet, writeSet := map[string]bool{}, map[string]bool{}
	for _, name := range reads {
		if readSet[name] {
			return fmt.Errorf("duplicate read tool %q", name)
		}
		readSet[name] = true
	}
	for _, name := range writes {
		if writeSet[name] {
			return fmt.Errorf("duplicate write tool %q", name)
		}
		writeSet[name] = true
	}
	for name := range allowSet {
		if !readSet[name] && !writeSet[name] {
			return fmt.Errorf("allowed tool %q needs a --read-tool or --write-tool access class", name)
		}
	}
	for name := range readSet {
		if !allowSet[name] || writeSet[name] {
			return errors.New("read and write tools must be disjoint subsets of the allowlist")
		}
	}
	for name := range writeSet {
		if !allowSet[name] {
			return errors.New("read and write tools must be disjoint subsets of the allowlist")
		}
	}
	return nil
}

func printConnectHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Connect commands manage HTTP MCP connections and their saved configuration:
  ownbot connect notion|calendly [--ssh-target USER@HOST]
  ownbot connect list
  ownbot connect add --name NAME --transport streamable_http --endpoint URL
  ownbot connect discover [flags] SERVICE_OR_SERVER_ID
  ownbot connect allow [flags] SERVICE_OR_SERVER_ID
  ownbot connect connect SERVER_ID
  ownbot connect authorize [--ssh-target USER@HOST] SERVER_ID
  ownbot connect disconnect SERVER_ID
  ownbot connect clear-auth SERVER_ID
  ownbot connect remove SERVER_ID

Google Calendar and Gmail sign-in are deferred pending local Google Cloud OAuth setup; see TODO.md.
Use connect discover SERVICE_OR_SERVER_ID to review provider tool metadata without calling tools. Then use connect allow with explicit read/write classes; omitted tools stay blocked. Write calls always need approval. An agent must separately allow selected connected tools.
For custom HTTP endpoints, connect add saves disabled metadata; discover and allow before connect. stdio can be saved as metadata but cannot connect in this daemon.
Use connect notion or connect calendly to save/reuse disabled server metadata and start public-provider OAuth sign-in. The flow uses PKCE and a one-use loopback callback. Connect authorize SERVER_ID starts sign-in for an existing config; clear-auth removes a saved grant and requests provider revocation. No API keys are collected; connecting discovers tools but never calls them or grants them to agents.
OAuth tokens stay in the private local OwnBot database and are not encrypted separately. When you run OwnBot over SSH, authorize prints the additional callback tunnel for that sign-in.
`)
}

func oauthCallbackForward(port int, target string) (string, bool) {
	if port < 1 || port > 65535 {
		return "", false
	}
	target = strings.TrimSpace(target)
	sshPort := ""
	if target == "" {
		connection := strings.Fields(os.Getenv("SSH_CONNECTION"))
		if len(connection) == 4 {
			serverAddr := connection[2]
			if ip := net.ParseIP(serverAddr); ip != nil && ip.To4() == nil {
				serverAddr = "[" + serverAddr + "]"
			}
			if user := strings.TrimSpace(os.Getenv("USER")); user != "" {
				target = user + "@" + serverAddr
			} else {
				target = serverAddr
			}
			if parsedPort, err := strconv.Atoi(connection[3]); err == nil && parsedPort > 0 && parsedPort <= 65535 && parsedPort != 22 {
				sshPort = strconv.Itoa(parsedPort)
			}
		}
	}
	if target == "" {
		if os.Getenv("SSH_CONNECTION") == "" {
			return "", false
		}
		target = "<same-SSH-target>"
	}
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, port)
	command := "ssh"
	if sshPort != "" {
		command += " -p " + sshPort
	}
	command += " -L " + forward + " -N " + shellQuote(target)
	return command, true
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func runSearch(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, _ = fmt.Fprintln(stdout, "Usage: ownbot setup search show [flags] | configure --endpoint URL [flags]")
		return nil
	}
	fs := flag.NewFlagSet("search "+args[0], flag.ContinueOnError)
	apiURL, dataDir := commonFlags(fs)
	endpoint := fs.String("endpoint", "", "SearXNG endpoint (HTTPS or loopback HTTP)")
	if err := parseFlags(fs, args[1:], stderr); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("setup search %s does not accept positional arguments", args[0])
	}
	client, err := clientFor(*apiURL, *dataDir)
	if err != nil {
		return err
	}
	var config searchConfig
	switch args[0] {
	case "show":
		if *endpoint != "" {
			return errors.New("setup search show does not accept --endpoint")
		}
		if err := client.Do(backgroundContext(), "GET", "/api/search", nil, &config); err != nil {
			return err
		}
	case "configure":
		if strings.TrimSpace(*endpoint) == "" {
			return errors.New("search configure requires --endpoint")
		}
		if err := client.Do(backgroundContext(), "POST", "/api/search", map[string]string{"endpoint": strings.TrimSpace(*endpoint)}, &config); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Configured %s. Search requests leave this device.\n", config.Provider)
	default:
		return fmt.Errorf("unknown search command %q; run `ownbot setup search --help`", args[0])
	}
	return writeJSON(stdout, config)
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
	_, _ = fmt.Fprintln(stdout, "Daemon logs are not available through the current API; inspect the daemon process stdout/stderr.")
	return errors.New("logs command unavailable: daemon has no /api/logs endpoint")
}

func urlPath(value string) string { return url.PathEscape(value) }
