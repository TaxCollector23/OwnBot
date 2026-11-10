package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/skip2/go-qrcode"
	"golang.org/x/term"
	"ownbot/internal/hardware"
)

type wizardTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Risk        string `json:"risk"`
	Source      string `json:"source"`
	Server      string `json:"server"`
}

func isInteractiveSetup(stdin io.Reader, stdout io.Writer) bool {
	in, inputOK := stdin.(*os.File)
	out, outputOK := stdout.(*os.File)
	return inputOK && outputOK && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

func runInteractiveSetup(apiURL, dataDir string, stdin io.Reader, stdout, stderr io.Writer) error {
	_, _ = fmt.Fprintln(stdout, "Scanning your computer for local runtimes and available memory…")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	runtimes := detectSetupRuntimes(ctx)
	cancel()
	_, _ = fmt.Fprintln(stdout, terminalAccent(stdout, "How would you like to run your local AI?"))
	_, _ = fmt.Fprintf(stdout, "\n  CPU: %s\n", readCPUName())
	info := hardware.ReadMemoryInfo()
	if info.AvailableBytes > 0 {
		_, _ = fmt.Fprintf(stdout, "  Available RAM: %s\n", formatBytes(info.AvailableBytes))
	} else {
		_, _ = fmt.Fprintln(stdout, "  Available RAM: unavailable from this operating system interface")
	}
	if info.TotalBytes > 0 {
		_, _ = fmt.Fprintf(stdout, "  Total RAM: %s\n", formatBytes(info.TotalBytes))
	} else {
		_, _ = fmt.Fprintln(stdout, "  Total RAM: unavailable from this operating system interface")
	}
	gpu := readGPUInfo()
	if gpu.Name != "" {
		_, _ = fmt.Fprintf(stdout, "  GPU: %s\n", gpu.Name)
	} else {
		_, _ = fmt.Fprintf(stdout, "  GPU: %s\n", gpu.Status)
	}
	if gpu.FreeBytes > 0 {
		_, _ = fmt.Fprintf(stdout, "  Available VRAM: %s (%s)\n", formatBytes(gpu.FreeBytes), gpu.Source)
	} else {
		_, _ = fmt.Fprintf(stdout, "  Available VRAM: not measured (%s)\n", gpu.Source)
	}
	_, _ = fmt.Fprintf(stdout, "  Memory layout: %s\n", unifiedMemoryDescription())
	for _, candidate := range runtimes {
		state := "not detected"
		if candidate.Running {
			state = fmt.Sprintf("ready · %d installed local models", len(candidate.Models))
			if candidate.Unverified > 0 {
				state += fmt.Sprintf(" · %d unverified model IDs excluded", candidate.Unverified)
			}
		} else if candidate.Installed {
			state = "installed, server not running"
		}
		_, _ = fmt.Fprintf(stdout, "  %s: %s\n", candidate.Name, state)
	}

	runtimeOptions := make([]huh.Option[string], 0, len(runtimes))
	for _, item := range runtimes {
		label := item.Name
		switch {
		case item.Running:
			label += " — available"
		case item.Installed:
			label += " — installed, server stopped"
		case item.InstallHelp != "":
			label += " — not installed"
		default:
			label += " — advanced local endpoint"
		}
		runtimeOptions = append(runtimeOptions, huh.NewOption(label, item.Name))
	}
	var runtimeName string
	if err := runSetupForm(stdin, stdout, huh.NewSelect[string]().Title("Select a local runtime").Options(runtimeOptions...).Value(&runtimeName)); err != nil {
		return fmt.Errorf("runtime selection cancelled: %w", err)
	}
	var selectedRuntime *detectedRuntime
	for i := range runtimes {
		if runtimes[i].Name == runtimeName {
			selectedRuntime = &runtimes[i]
			break
		}
	}
	if selectedRuntime == nil {
		return errors.New("selected runtime could not be resolved")
	}
	if !selectedRuntime.Running {
		if err := startOrExplainRuntime(stdin, stdout, selectedRuntime); err != nil {
			return err
		}
		refreshed := detectSingleSetupRuntime(selectedRuntime.Name)
		*selectedRuntime = refreshed
		if !selectedRuntime.Running {
			return fmt.Errorf("%s did not become available; start its local API server and rerun `ownbot setup`", selectedRuntime.Name)
		}
	}
	if selectedRuntime.AuthRequired {
		key, err := askRuntimeKey(stdin, stdout, selectedRuntime.Name)
		if err != nil {
			return err
		}
		if selectedRuntime.Name == "Jan" {
			_ = os.Setenv("OWNBOT_JAN_API_KEY", key)
		} else if selectedRuntime.Name == "LM Studio" {
			_ = os.Setenv("LM_API_TOKEN", key)
		}
		refreshed := detectSingleSetupRuntime(selectedRuntime.Name)
		*selectedRuntime = refreshed
		if !selectedRuntime.Running || selectedRuntime.AuthRequired {
			return errors.New("the runtime did not accept that credential; it was not saved by OwnBot")
		}
	}

	_, _ = fmt.Fprintln(stdout, "\nDetected installed models:")
	if len(selectedRuntime.Models) == 0 {
		_, _ = fmt.Fprintln(stdout, "  None")
	} else {
		for _, model := range selectedRuntime.Models {
			size := "size unavailable"
			if model.SizeBytes > 0 {
				size = formatBytes(uint64(model.SizeBytes))
			}
			_, _ = fmt.Fprintf(stdout, "  %s · %s\n", model.ID, size)
		}
	}
	if selectedRuntime.Unverified > 0 {
		_, _ = fmt.Fprintf(stdout, "  Excluded %d model IDs without proof that their weights are stored locally.\n", selectedRuntime.Unverified)
	}

	availableRAM, haveRAM := availableMemory()
	diskPath := modelStorePath(selectedRuntime.Name)
	availableDisk, haveDisk := freeDiskAtNearestExisting(diskPath)
	candidates := []setupModelCandidate{}
	switch selectedRuntime.Name {
	case "Ollama":
		candidates = ollamaShortlist()
	case "LM Studio":
		candidates = lmStudioShortlist()
	}
	recommendation, reason, hasRecommendation := recommendModel(candidates, availableRAM, gpu.FreeBytes, availableDisk, haveRAM, haveDisk, runtime.GOOS == "darwin" && runtime.GOARCH == "arm64")
	if hasRecommendation {
		_, _ = fmt.Fprintf(stdout, "\nRecommended model: %s\n  Size: %s\n  Estimated memory: %s\n  Reason: %s\n", recommendation.ID, formatBytes(recommendation.SizeBytes), formatBytes(recommendation.EstimatedMemory), reason)
	}

	installed := make([]setupModelCandidate, 0, len(selectedRuntime.Models))
	for _, model := range selectedRuntime.Models {
		installed = append(installed, installedCandidate(model))
	}
	var model setupModelCandidate
	var err error
	if len(candidates) == 0 || !hasRecommendation {
		if len(installed) == 0 {
			if selectedRuntime.Name == "Jan" {
				installed, err = guideJanModelDownload(stdin, stdout)
				if err != nil {
					return err
				}
				if len(installed) == 0 {
					return errors.New("no verified local Jan model was found after checking the local model files")
				}
			} else {
				return errors.New("no verified installed model is available and resource telemetry could not support a recommendation; install a local tool-capable model, then rerun setup")
			}
		}
		model, err = chooseInstalledModel(stdin, stdout, installed)
		if err != nil {
			return err
		}
	} else {
		choiceValues := []string{"install-recommended"}
		choiceLabels := []string{"Install recommended model"}
		for i := range candidates {
			if candidates[i].ID != recommendation.ID {
				choiceValues = append(choiceValues, "alternative:"+candidates[i].ID)
				choiceLabels = append(choiceLabels, fmt.Sprintf("Choose another model: %s · %s download · %s estimated memory", candidates[i].ID, formatBytes(candidates[i].SizeBytes), formatBytes(candidates[i].EstimatedMemory)))
			}
		}
		if len(installed) > 0 {
			choiceValues = append(choiceValues, "installed")
			choiceLabels = append(choiceLabels, fmt.Sprintf("Use an already installed model (%d available)", len(installed)))
		}
		var choiceOptions []huh.Option[string]
		for i, value := range choiceValues {
			choiceOptions = append(choiceOptions, huh.NewOption(choiceLabels[i], value))
		}
		choice := ""
		if err := runSetupForm(stdin, stdout, huh.NewSelect[string]().Title("Choose a model").Options(choiceOptions...).Value(&choice)); err != nil {
			return fmt.Errorf("model selection cancelled: %w", err)
		}
		switch {
		case choice == "install-recommended":
			model = recommendation
		case choice == "installed":
			model, err = chooseInstalledModel(stdin, stdout, installed)
			if err != nil {
				return err
			}
		case strings.HasPrefix(choice, "alternative:"):
			modelID := strings.TrimPrefix(choice, "alternative:")
			for _, candidate := range candidates {
				if candidate.ID == modelID {
					model = candidate
					break
				}
			}
		default:
			return errors.New("invalid model selection")
		}
		if !model.Installed {
			if err := downloadSetupModel(stdin, stdout, stderr, selectedRuntime, model); err != nil {
				return err
			}
		}
	}
	if selectedRuntime.Name == "Jan" && !model.LocalVerified {
		return errors.New("Jan model could not be verified as local; cloud-backed model IDs are not supported")
	}
	agentName := "Assistant"
	if err := runSetupForm(stdin, stdout, huh.NewInput().Title("Agent name").Value(&agentName).Validate(validateAgentName)); err != nil {
		return fmt.Errorf("agent setup cancelled: %w", err)
	}

	if strings.TrimRight(apiURL, "/") != strings.TrimRight(defaultAPIURL(), "/") {
		return errors.New("automatic LAN setup requires OwnBot's default loopback daemon URL; start it with `ownbot daemon start --lan` for this custom URL")
	}
	args, err := setupDaemonArgsWithLAN(apiURL, dataDir, true)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "\nStarting OwnBot with LAN access enabled…")
	setupNotice, err := setupManagedDaemon(args, apiURL, dataDir, stdout)
	if err != nil {
		return err
	}
	client, err := clientFor(apiURL, dataDir)
	if err != nil {
		return err
	}
	modelConfig, err := saveOrReuseModel(client, model, runtimeModelName(selectedRuntime.Name, model.ID))
	if err != nil {
		return err
	}
	key := runtimeAPIKey(selectedRuntime.Name)
	if key != "" {
		if err := client.Do(context.Background(), http.MethodPut, "/api/models/"+urlPath(modelConfig.ID)+"/credentials", map[string]string{"api_key": key}, nil); err != nil {
			return errors.New("could not store the runtime credential in OwnBot's private local state")
		}
	}
	passed, probeReason, probeErr := probeSetupModel(client, modelConfig)
	if probeErr != nil || !passed {
		if probeErr != nil {
			_, _ = fmt.Fprintf(stdout, "\nCapability probe could not complete: %s\n", probeErr)
		} else {
			_, _ = fmt.Fprintf(stdout, "\nCapability probe failed: %s\n", probeReason)
		}
		_, _ = fmt.Fprintln(stdout, "No agent will be created unless a model passes all capability checks.")
		alternative, chooseErr := chooseProbeAlternative(stdin, stdout, model.ID, selectedRuntime.Models, candidates)
		if chooseErr != nil {
			return errors.Join(errors.New("selected model failed the capability probe"), chooseErr)
		}
		if !alternative.Installed {
			if err := downloadSetupModel(stdin, stdout, stderr, selectedRuntime, alternative); err != nil {
				return err
			}
		}
		if selectedRuntime.Name == "Jan" && !alternative.LocalVerified {
			return errors.New("the alternative Jan model is not verified as local; cloud-backed models are not supported")
		}
		model = alternative
		modelConfig, err = saveOrReuseModel(client, model, runtimeModelName(selectedRuntime.Name, model.ID))
		if err != nil {
			return err
		}
		if key != "" {
			if err := client.Do(context.Background(), http.MethodPut, "/api/models/"+urlPath(modelConfig.ID)+"/credentials", map[string]string{"api_key": key}, nil); err != nil {
				return errors.New("could not store the runtime credential for the alternate model")
			}
		}
		passed, probeReason, probeErr = probeSetupModel(client, modelConfig)
		if probeErr != nil || !passed {
			if probeErr != nil {
				return fmt.Errorf("alternative model %q also failed capability probing; no agent was created: %w", model.ID, probeErr)
			}
			return fmt.Errorf("alternative model %q also failed capability checks (%s); no agent was created", model.ID, probeReason)
		}
	}
	var tools []wizardTool
	if err := client.Do(context.Background(), http.MethodGet, "/api/tools", nil, &tools); err != nil {
		return fmt.Errorf("could not inspect available agent permissions: %w", err)
	}
	selectedTools, err := choosePermissions(stdin, stdout, tools)
	if err != nil {
		return err
	}
	agent, err := saveOrReuseAgent(client, agentName, model.ID, "You are a helpful local assistant. Answer clearly and ask before taking actions that are not explicitly authorized.", selectedTools)
	if err != nil {
		return err
	}
	var access struct {
		Enabled bool     `json:"enabled"`
		URLs    []string `json:"urls"`
	}
	if err := client.Do(context.Background(), http.MethodGet, "/api/access", nil, &access); err != nil {
		return fmt.Errorf("agent setup succeeded, but LAN access could not be checked: %w", err)
	}
	if !access.Enabled || len(access.URLs) == 0 {
		return errors.New("agent setup succeeded, but OwnBot has no LAN URL; check the daemon's LAN listener")
	}
	_, _ = fmt.Fprintln(stdout, "\nOwnBot is running!")
	_, _ = fmt.Fprintf(stdout, "Host: %s\nModel: %s\nNetwork: Local only\n", hostLabel(access.URLs[0]), model.ID)
	_, _ = fmt.Fprintf(stdout, "Agent: %s\n", agent.Name)
	if len(selectedTools) == 0 {
		_, _ = fmt.Fprintln(stdout, "Permissions: chat only; no tools enabled")
	} else {
		_, _ = fmt.Fprintf(stdout, "Permissions: %s\n", strings.Join(selectedTools, ", "))
		_, _ = fmt.Fprintln(stdout, "Each action remains subject to OwnBot's runtime approval policy.")
	}
	if setupNotice != "" {
		_, _ = fmt.Fprintln(stdout, strings.TrimSpace(setupNotice))
	}
	return printPairingQR(client, stdout, dataDir, access.URLs)
}

func runSetupForm(input io.Reader, output io.Writer, fields ...huh.Field) error {
	form := huh.NewForm(huh.NewGroup(fields...)).WithInput(input).WithOutput(output)
	return form.Run()
}

func validateAgentName(value string) error {
	if strings.TrimSpace(value) == "" || len(value) > 100 {
		return errors.New("enter a name from 1 to 100 characters")
	}
	return nil
}

func chooseInstalledModel(input io.Reader, output io.Writer, models []setupModelCandidate) (setupModelCandidate, error) {
	if len(models) == 1 {
		return models[0], nil
	}
	options := make([]huh.Option[string], 0, len(models))
	byID := make(map[string]setupModelCandidate, len(models))
	for _, model := range models {
		label := model.ID
		if model.SizeBytes > 0 {
			label += " · " + formatBytes(model.SizeBytes)
		}
		options = append(options, huh.NewOption(label, model.ID))
		byID[model.ID] = model
	}
	selected := ""
	if err := runSetupForm(input, output, huh.NewSelect[string]().Title("Use an installed local model").Options(options...).Value(&selected)); err != nil {
		return setupModelCandidate{}, fmt.Errorf("model selection cancelled: %w", err)
	}
	model, ok := byID[selected]
	if !ok {
		return setupModelCandidate{}, errors.New("selected model was not in the installed local model list")
	}
	return model, nil
}

func downloadSetupModel(input io.Reader, stdout, stderr io.Writer, runtimeInfo *detectedRuntime, model setupModelCandidate) error {
	ctx, stop := signalContext()
	defer stop()
	switch runtimeInfo.Name {
	case "Ollama":
		_, _ = fmt.Fprintf(stdout, "Downloading %s. Press Ctrl+C to cancel; Ollama keeps completed layers for a later retry.\n", model.ID)
		return pullOllamaModel(ctx, directHTTPClient(0), runtimeInfo.BaseURL, model.ID, stdout)
	case "LM Studio":
		_, _ = fmt.Fprintf(stdout, "Downloading and loading %s with LM Studio's `lms get` and `lms load` commands. Press Ctrl+C to cancel.\n", model.ID)
		return downloadAndLoadLMStudio(ctx, model.ID, stdout, stderr)
	case "Jan":
		return errors.New("download and load the model in Jan Hub, then confirm it in the setup prompt")
	default:
		return fmt.Errorf("automated model downloads are not available for %s; install a local model in that runtime and rerun setup", runtimeInfo.Name)
	}
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func startOrExplainRuntime(input io.Reader, stdout io.Writer, runtimeInfo *detectedRuntime) error {
	if !runtimeInfo.Installed {
		if runtimeInfo.InstallHelp != "" {
			_, _ = fmt.Fprintf(stdout, "%s is not installed. Install it from %s, enable its local API, then rerun setup. OwnBot will not install third-party runtime software.\n", runtimeInfo.Name, runtimeInfo.InstallHelp)
			openURL(runtimeInfo.InstallHelp)
			return fmt.Errorf("%s must be installed and its local API started before setup can continue", runtimeInfo.Name)
		}
		return fmt.Errorf("start your local %s API server, then rerun `ownbot setup`", runtimeInfo.Name)
	}
	if runtimeInfo.Name == "Jan" {
		_, _ = fmt.Fprintln(stdout, "Open Jan, select a model stored on this computer, then enable its Local API Server in Settings → Local API Server. OwnBot will verify the model against Jan's local model files.")
		return errors.New("Jan's local API server is not running; start it in Jan and rerun setup")
	}
	var command *exec.Cmd
	switch runtimeInfo.Name {
	case "Ollama":
		path, err := exec.LookPath("ollama")
		if err != nil {
			return errors.New("Ollama is installed but its CLI is unavailable; start the Ollama app and rerun setup")
		}
		command = exec.Command(path, "serve")
	case "LM Studio":
		path, err := exec.LookPath("lms")
		if err != nil {
			return errors.New("LM Studio is installed but its `lms` command is unavailable; start the local server in the LM Studio app")
		}
		command = exec.Command(path, "server", "start")
	default:
		return fmt.Errorf("start the local %s server, then rerun setup", runtimeInfo.Name)
	}
	command.Stdin, command.Stdout, command.Stderr = nil, io.Discard, io.Discard
	if err := command.Start(); err != nil {
		return fmt.Errorf("could not start %s: %w", runtimeInfo.Name, err)
	}
	_ = command.Process.Release()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if detectSingleSetupRuntime(runtimeInfo.Name).Running {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("%s did not start its local API within 20 seconds", runtimeInfo.Name)
}

func detectSingleSetupRuntime(name string) detectedRuntime {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, item := range detectSetupRuntimes(ctx) {
		if item.Name == name {
			return item
		}
	}
	return detectedRuntime{Name: name}
}

func askRuntimeKey(input io.Reader, output io.Writer, runtimeName string) (string, error) {
	key := ""
	field := huh.NewInput().Title(runtimeName + " local API key (stored privately on this host)").EchoMode(huh.EchoModePassword).Value(&key).Validate(func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("enter the local runtime key")
		}
		return nil
	})
	if err := runSetupForm(input, output, field); err != nil {
		return "", fmt.Errorf("runtime credential input cancelled: %w", err)
	}
	return key, nil
}

func runtimeAPIKey(runtimeName string) string {
	if runtimeName == "Jan" {
		return strings.TrimSpace(os.Getenv("OWNBOT_JAN_API_KEY"))
	}
	if runtimeName == "LM Studio" {
		return strings.TrimSpace(os.Getenv("LM_API_TOKEN"))
	}
	return ""
}

func guideJanModelDownload(input io.Reader, output io.Writer) ([]setupModelCandidate, error) {
	_, _ = fmt.Fprintln(output, "Jan's current Local API documents model listing and chat requests, not a model download endpoint. In Jan, download an open-weight model, load it as a local model, then keep the Local API Server running. Setup will check its local model metadata after you continue.")
	_, _ = fmt.Fprintln(output, "Jan model guide: https://docs.jan.ai/desktop/manage-models")
	continueSetup := false
	if err := runSetupForm(input, output, huh.NewConfirm().Title("Have you downloaded and loaded a local model in Jan?").Affirmative("Yes, check again").Negative("Not yet").Value(&continueSetup)); err != nil {
		return nil, fmt.Errorf("Jan setup cancelled: %w", err)
	}
	if !continueSetup {
		return nil, errors.New("download and load a local Jan model, then rerun `ownbot setup`")
	}
	refreshed := detectSingleSetupRuntime("Jan")
	if len(refreshed.Models) == 0 {
		return nil, errors.New("no Jan API model matched a model.yml file and local model weights under Jan's llamacpp/models or mlx/models directory; check JAN_DATA_DIR if Jan stores its data elsewhere")
	}
	result := make([]setupModelCandidate, 0, len(refreshed.Models))
	for _, model := range refreshed.Models {
		result = append(result, installedCandidate(model))
	}
	return result, nil
}

func choosePermissions(input io.Reader, output io.Writer, available []wizardTool) ([]string, error) {
	if len(available) == 0 {
		_, _ = fmt.Fprintln(output, "No connected tools are available. This agent will start in chat-only mode; integrations can be configured later.")
		return []string{}, nil
	}
	sort.Slice(available, func(i, j int) bool {
		if available[i].Risk != available[j].Risk {
			return riskOrder(available[i].Risk) < riskOrder(available[j].Risk)
		}
		return available[i].Name < available[j].Name
	})
	options := make([]huh.Option[string], 0, len(available))
	known := make(map[string]bool, len(available))
	for _, tool := range available {
		label := fmt.Sprintf("[%s] %s — %s", permissionRiskLabel(tool.Risk), tool.Name, tool.Description)
		options = append(options, huh.NewOption(label, tool.Name))
		known[tool.Name] = true
	}
	selected := []string{}
	if err := runSetupForm(input, output, huh.NewMultiSelect[string]().Title("What should your agent be allowed to do? (Space toggles; Enter confirms)").Options(options...).Value(&selected)); err != nil {
		return nil, fmt.Errorf("permission selection cancelled: %w", err)
	}
	for _, tool := range selected {
		if !known[tool] {
			return nil, errors.New("permission list included a tool that is no longer available")
		}
	}
	_, _ = fmt.Fprintln(output, "\nPermission review:")
	if len(selected) == 0 {
		_, _ = fmt.Fprintln(output, "  Chat only; no tools enabled")
	} else {
		for _, tool := range selected {
			for _, item := range available {
				if item.Name == tool {
					_, _ = fmt.Fprintf(output, "  [%s] %s — %s\n", permissionRiskLabel(item.Risk), item.Name, item.Description)
				}
			}
		}
		_, _ = fmt.Fprintln(output, "Tool calls remain subject to the server's execution-time policy and approval checks.")
	}
	return selected, nil
}

func riskOrder(risk string) int {
	switch risk {
	case "read":
		return 0
	case "write_reversible":
		return 1
	case "write_irreversible":
		return 2
	default:
		return 3
	}
}

func permissionRiskLabel(risk string) string {
	switch risk {
	case "read":
		return "read-only"
	case "write_reversible":
		return "changes data"
	case "write_irreversible":
		return "irreversible"
	default:
		return "restricted"
	}
}

func saveOrReuseModel(client *Client, selected setupModelCandidate, name string) (modelConfig, error) {
	var models []modelConfig
	if err := client.Do(context.Background(), http.MethodGet, "/api/models", nil, &models); err != nil {
		return modelConfig{}, err
	}
	for _, item := range models {
		if item.Provider == selected.Provider && strings.TrimRight(item.BaseURL, "/") == strings.TrimRight(selected.BaseURL, "/") && item.Model == selected.ID {
			return item, nil
		}
	}
	var result modelConfig
	if err := client.Do(context.Background(), http.MethodPost, "/api/models", map[string]string{"provider": selected.Provider, "name": name, "base_url": selected.BaseURL, "model": selected.ID}, &result); err != nil {
		return modelConfig{}, fmt.Errorf("could not save model configuration: %w", err)
	}
	return result, nil
}

type setupProbeResult struct {
	Score            int  `json:"score"`
	JSONOutput       bool `json:"json_output"`
	SingleTool       bool `json:"single_tool"`
	ToolChoice       bool `json:"tool_choice"`
	ArgumentAccuracy bool `json:"argument_accuracy"`
	Restricted       bool `json:"restricted"`
}

func probeSetupModel(client *Client, model modelConfig) (bool, string, error) {
	var response struct {
		Probe setupProbeResult `json:"probe"`
	}
	if err := client.Do(context.Background(), http.MethodPost, "/api/models/"+urlPath(model.ID)+"/probe", map[string]any{}, &response); err != nil {
		return false, "", err
	}
	probe := response.Probe
	passed := !probe.Restricted && probe.Score == 100 && probe.JSONOutput && probe.SingleTool && probe.ToolChoice && probe.ArgumentAccuracy
	if passed {
		return true, "", nil
	}
	var failed []string
	if probe.Restricted {
		failed = append(failed, "restricted output")
	}
	if !probe.JSONOutput {
		failed = append(failed, "JSON output")
	}
	if !probe.SingleTool {
		failed = append(failed, "single-tool calling")
	}
	if !probe.ToolChoice {
		failed = append(failed, "tool choice")
	}
	if !probe.ArgumentAccuracy {
		failed = append(failed, "tool argument accuracy")
	}
	if probe.Score != 100 {
		failed = append(failed, fmt.Sprintf("score %d/100", probe.Score))
	}
	return false, strings.Join(failed, ", "), nil
}

func chooseProbeAlternative(input io.Reader, output io.Writer, current string, installedModels []localModel, candidates []setupModelCandidate) (setupModelCandidate, error) {
	options := []huh.Option[string]{}
	byID := map[string]setupModelCandidate{}
	for _, local := range installedModels {
		if local.ID == current {
			continue
		}
		candidate := installedCandidate(local)
		if _, exists := byID[candidate.ID]; exists {
			continue
		}
		options = append(options, huh.NewOption("Installed: "+candidate.ID, candidate.ID))
		byID[candidate.ID] = candidate
	}
	for _, candidate := range candidates {
		if candidate.ID == current {
			continue
		}
		if _, exists := byID[candidate.ID]; exists {
			continue
		}
		options = append(options, huh.NewOption("Download: "+candidate.ID+" · "+formatBytes(candidate.SizeBytes), candidate.ID))
		byID[candidate.ID] = candidate
	}
	if len(options) == 0 {
		return setupModelCandidate{}, errors.New("no verified alternative models are available; install a different local tool-calling model and rerun setup")
	}
	selected := ""
	if err := runSetupForm(input, output, huh.NewSelect[string]().Title("Choose a different model to probe").Options(options...).Value(&selected)); err != nil {
		return setupModelCandidate{}, fmt.Errorf("alternative model selection cancelled: %w", err)
	}
	candidate, ok := byID[selected]
	if !ok {
		return setupModelCandidate{}, errors.New("selected alternative is not in the verified model list")
	}
	return candidate, nil
}

func runtimeModelName(runtimeName, modelID string) string {
	return runtimeName + " · " + modelID
}

func saveOrReuseAgent(client *Client, name, model, prompt string, tools []string) (agentConfig, error) {
	var agents []agentConfig
	if err := client.Do(context.Background(), http.MethodGet, "/api/agents", nil, &agents); err != nil {
		return agentConfig{}, err
	}
	var agent agentConfig
	for _, existing := range agents {
		if strings.EqualFold(strings.TrimSpace(existing.Name), strings.TrimSpace(name)) {
			agent = existing
			break
		}
	}
	if agent.ID == "" {
		if err := client.Do(context.Background(), http.MethodPost, "/api/agents", map[string]any{"name": name, "model": model, "enabled": true, "tools": tools, "prompt": prompt}, &agent); err != nil {
			return agentConfig{}, fmt.Errorf("could not create the agent: %w", err)
		}
		return agent, nil
	}
	var updated agentConfig
	if err := client.Do(context.Background(), http.MethodPatch, "/api/agents/"+urlPath(agent.ID), map[string]any{"model": model, "enabled": true, "tools": tools}, &updated); err != nil {
		return agentConfig{}, fmt.Errorf("could not save the selected permissions: %w", err)
	}
	updated.Name, updated.ID = agent.Name, agent.ID
	return updated, nil
}

func printPairingQR(client *Client, output io.Writer, dataDir string, dashboardURLs []string) error {
	var pairing struct {
		PairingURL  string `json:"pairing_url"`
		ExpiresAt   string `json:"expires_at"`
		Fingerprint string `json:"certificate_fingerprint"`
	}
	if err := client.Do(context.Background(), http.MethodPost, "/api/devices/pairing", map[string]any{}, &pairing); err != nil {
		return fmt.Errorf("setup is complete, but OwnBot could not create a LAN pairing link: %w", err)
	}
	code, err := qrcode.New(pairing.PairingURL, qrcode.Medium)
	if err != nil {
		return fmt.Errorf("setup is complete, but the one-time pairing URL could not be encoded as a QR code: %w", err)
	}
	_, _ = fmt.Fprintln(output, "\nScan this one-use QR code from the device you want to pair:")
	printTerminalQR(output, code.Bitmap())
	_, _ = fmt.Fprintf(output, "Pairing link expires: %s\n", pairing.ExpiresAt)
	for _, address := range dashboardURLs {
		_, _ = fmt.Fprintf(output, "LAN dashboard URL: %s\n", address)
	}
	_, _ = fmt.Fprintf(output, "One-use pairing URL fallback: %s\n", pairing.PairingURL)
	if pairing.Fingerprint != "" {
		_, _ = fmt.Fprintf(output, "Certificate fingerprint: %s\n", pairing.Fingerprint)
		_, _ = fmt.Fprintf(output, "Trust certificate file: %s\n", filepath.Join(dataDir, "lan.cert.cer"))
		_, _ = fmt.Fprintf(output, "iPhone/iPad trust profile: %s\n", filepath.Join(dataDir, "lan.cert.mobileconfig"))
		_, _ = fmt.Fprintln(output, "Before scanning, transfer the public certificate/profile from the host using a trusted method, compare its SHA-256 fingerprint with the host terminal, then install it as a root CA. On iPhone/iPad, install the profile and enable full trust in Settings → General → About → Certificate Trust Settings. On Android, install the certificate as a CA in Security & privacy → Encryption & credentials. Never continue through a browser certificate warning.")
	}
	_, _ = fmt.Fprintln(output, "The QR code contains a short-lived, one-use pairing link. The permanent host API token is not included.")
	_, _ = fmt.Fprintln(output, "Open the dashboard and submit a task to begin. Task execution and tool approval happen on this host.")
	return nil
}

func printTerminalQR(output io.Writer, bitmap [][]bool) {
	if len(bitmap) == 0 {
		return
	}
	quiet := 4
	width := len(bitmap[0]) + quiet*2
	black := "\x1b[40m  \x1b[0m"
	white := "\x1b[47m  \x1b[0m"
	for row := -quiet; row < len(bitmap)+quiet; row += 2 {
		for col := -quiet; col < width-quiet; col++ {
			top, bottom := false, false
			if row >= 0 && row < len(bitmap) && col >= 0 && col < len(bitmap[row]) {
				top = bitmap[row][col]
			}
			if row+1 >= 0 && row+1 < len(bitmap) && col >= 0 && col < len(bitmap[row+1]) {
				bottom = bitmap[row+1][col]
			}
			if top {
				_, _ = io.WriteString(output, black)
			} else {
				_, _ = io.WriteString(output, white)
			}
			if bottom {
				_, _ = io.WriteString(output, black)
			} else {
				_, _ = io.WriteString(output, white)
			}
		}
		_, _ = fmt.Fprintln(output)
	}
}

func hostLabel(apiURL string) string {
	address := apiURL
	if u, err := url.Parse(apiURL); err == nil {
		address = u.Hostname()
	}
	return address
}

func freeDiskAtNearestExisting(path string) (uint64, bool) {
	path = filepath.Clean(path)
	for {
		if _, err := os.Stat(path); err == nil {
			return availableDiskBytes(path)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return 0, false
		}
		path = parent
	}
}
