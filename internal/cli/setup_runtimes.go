package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type detectedRuntime struct {
	Name         string
	Provider     string
	BaseURL      string
	Running      bool
	Installed    bool
	AuthRequired bool
	Unverified   int
	Models       []localModel
	InstallHelp  string
}

type janModelInfo struct {
	ID        string
	Name      string
	ModelPath string
	SizeBytes int64
}

type lmStudioModelResponse struct {
	Models []struct {
		Type         string `json:"type"`
		Key          string `json:"key"`
		DisplayName  string `json:"display_name"`
		Format       string `json:"format"`
		SizeBytes    int64  `json:"size_bytes"`
		Capabilities struct {
			TrainedForToolUse bool `json:"trained_for_tool_use"`
		} `json:"capabilities"`
	} `json:"models"`
}

func detectSetupRuntimes(ctx context.Context) []detectedRuntime {
	client := directHTTPClient(1200 * time.Millisecond)
	ollama := detectedRuntime{Name: "Ollama", Provider: "ollama", BaseURL: "http://127.0.0.1:11434/v1", InstallHelp: "https://ollama.com/download"}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if _, err := getLocalJSONStatus(ctx, client, "http://127.0.0.1:11434/api/tags", "", &tags); err == nil {
		ollama.Running, ollama.Installed = true, true
		for _, model := range tags.Models {
			if strings.TrimSpace(model.Name) != "" {
				ollama.Models = append(ollama.Models, localModel{Provider: "ollama", BaseURL: ollama.BaseURL, ID: model.Name, Runtime: ollama.Name, LocalVerified: true})
			}
		}
	} else if _, err := exec.LookPath("ollama"); err == nil {
		ollama.Installed = true
	}

	lm := detectedRuntime{Name: "LM Studio", Provider: "lm_studio", BaseURL: "http://127.0.0.1:1234/v1", InstallHelp: "https://lmstudio.ai/download"}
	if _, err := exec.LookPath("lms"); err == nil {
		lm.Installed = true
	}
	var lmModels lmStudioModelResponse
	status, err := getLocalJSONStatus(ctx, client, "http://127.0.0.1:1234/api/v1/models", os.Getenv("LM_API_TOKEN"), &lmModels)
	if err == nil {
		lm.Running, lm.Installed = true, true
	} else if status == http.StatusUnauthorized {
		lm.Running, lm.Installed, lm.AuthRequired = true, true, true
	}
	for _, model := range lmModels.Models {
		format := strings.ToLower(strings.TrimSpace(model.Format))
		if strings.ToLower(model.Type) != "llm" || (format != "gguf" && format != "mlx") || strings.TrimSpace(model.Key) == "" {
			continue
		}
		name := model.DisplayName
		if name == "" {
			name = model.Key
		}
		lm.Models = append(lm.Models, localModel{Provider: "lm_studio", BaseURL: lm.BaseURL, ID: model.Key, Runtime: name, SizeBytes: model.SizeBytes, Format: format, ToolUse: model.Capabilities.TrainedForToolUse, LocalVerified: true})
	}

	jan := detectedRuntime{Name: "Jan", Provider: "jan", BaseURL: "http://127.0.0.1:1337/v1", InstallHelp: "https://jan.ai/download"}
	var janModels struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	janKey := os.Getenv("OWNBOT_JAN_API_KEY")
	status, err = getLocalJSONStatus(ctx, client, jan.BaseURL+"/models", janKey, &janModels)
	if err == nil {
		jan.Running, jan.Installed = true, true
	} else if status == http.StatusUnauthorized {
		jan.Running, jan.Installed, jan.AuthRequired = true, true, true
	}
	localInfo := janInstalledModelIndex(janDataDirectories())
	for _, model := range janModels.Data {
		info, ok := localInfo[canonicalJanModelID(model.ID)]
		if !ok {
			jan.Unverified++
			continue
		}
		jan.Models = append(jan.Models, localModel{Provider: "jan", BaseURL: jan.BaseURL, ID: model.ID, Runtime: jan.Name + " · local model", SizeBytes: info.SizeBytes, LocalVerified: true})
	}

	llama := detectAdvancedRuntime(ctx, client, "llama.cpp", "openai_compatible", "http://127.0.0.1:8080/v1")
	vllm := detectAdvancedRuntime(ctx, client, "vLLM", "openai_compatible", "http://127.0.0.1:8000/v1")
	return []detectedRuntime{ollama, lm, jan, llama, vllm}
}

func detectAdvancedRuntime(ctx context.Context, client *http.Client, name, provider, baseURL string) detectedRuntime {
	result := detectedRuntime{Name: name, Provider: provider, BaseURL: baseURL}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if _, err := getLocalJSONStatus(ctx, client, baseURL+"/models", "", &response); err == nil {
		result.Running, result.Installed = true, true
		for _, model := range response.Data {
			if strings.TrimSpace(model.ID) != "" {
				result.Models = append(result.Models, localModel{Provider: provider, BaseURL: baseURL, ID: model.ID, Runtime: name})
			}
		}
	}
	return result
}

func getLocalJSONStatus(ctx context.Context, client *http.Client, address, apiKey string, target any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return 0, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("local runtime returned HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 2<<20)).Decode(target); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

func janDataDirectories() []string {
	if configured := strings.TrimSpace(os.Getenv("JAN_DATA_DIR")); configured != "" {
		return []string{filepath.Clean(configured)}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var defaults []string
	switch runtime.GOOS {
	case "darwin":
		defaults = append(defaults, filepath.Join(home, "Library", "Application Support", "Jan", "data"))
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			defaults = append(defaults, filepath.Join(appData, "Jan", "data"))
		}
	case "linux":
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			defaults = append(defaults, filepath.Join(xdg, "Jan", "data"))
		}
		defaults = append(defaults, filepath.Join(home, ".local", "share", "Jan", "data"))
	}
	if custom := configuredJanDataDirectory(defaults); len(custom) > 0 {
		return custom
	}
	return defaults
}

func configuredJanDataDirectory(defaults []string) []string {
	var candidates []string
	for _, root := range defaults {
		parent := filepath.Dir(root)
		candidates = append(candidates, filepath.Join(parent, "settings.json"), filepath.Join(root, "settings.json"))
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil || len(data) > 1<<20 {
			continue
		}
		var settings map[string]any
		if json.Unmarshal(data, &settings) != nil {
			continue
		}
		for _, key := range []string{"data_folder", "dataFolder", "data_dir"} {
			if folder, ok := settings[key].(string); ok && strings.TrimSpace(folder) != "" {
				return []string{filepath.Clean(folder)}
			}
		}
	}
	return nil
}

func janInstalledModelIndex(directories []string) map[string]janModelInfo {
	result := make(map[string]janModelInfo)
	for _, dataDirectory := range directories {
		for _, engine := range []string{"llamacpp/models", "mlx/models"} {
			root := filepath.Join(dataDirectory, filepath.FromSlash(engine))
			_ = filepath.WalkDir(root, func(filename string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return nil
				}
				if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || entry.Name() != "model.yml" {
					return nil
				}
				info := readJanModelInfo(filename)
				if info.Name == "" || !janModelWeightsPresent(dataDirectory, filename, info.ModelPath) {
					return nil
				}
				info.ID = info.Name
				for _, alias := range []string{info.Name, filepath.Base(filepath.Dir(filename)), filepath.Base(info.ID)} {
					canonical := canonicalJanModelID(alias)
					if canonical != "" {
						result[canonical] = info
					}
				}
				return nil
			})
		}
	}
	return result
}

func readJanModelInfo(filename string) janModelInfo {
	file, err := os.Open(filename)
	if err != nil {
		return janModelInfo{}
	}
	defer file.Close()
	var result janModelInfo
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 256<<10)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		switch strings.TrimSpace(key) {
		case "name":
			result.Name = value
		case "size_bytes":
			result.SizeBytes, _ = strconv.ParseInt(value, 10, 64)
		case "model_path":
			result.ModelPath = value
		}
	}
	return result
}

func janModelWeightsPresent(dataDirectory, metadataFile, modelPath string) bool {
	modelPath = strings.TrimSpace(modelPath)
	if modelPath == "" || strings.ContainsRune(modelPath, 0) {
		return false
	}
	root, err := filepath.Abs(filepath.Clean(dataDirectory))
	if err != nil {
		return false
	}
	candidates := []string{}
	if filepath.IsAbs(modelPath) {
		candidates = append(candidates, modelPath)
	} else {
		candidates = append(candidates, filepath.Join(root, filepath.Clean(modelPath)), filepath.Join(filepath.Dir(metadataFile), filepath.Clean(modelPath)))
	}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(filepath.Clean(candidate))
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(root, absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		info, err := os.Lstat(absolute)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if info.Mode().IsRegular() && info.Size() > 0 {
			return true
		}
		if !info.IsDir() {
			continue
		}
		found := false
		_ = filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() || entry.Name() == "model.yml" {
				return nil
			}
			itemInfo, statErr := entry.Info()
			if statErr == nil && itemInfo.Mode().IsRegular() && itemInfo.Size() > 0 {
				found = true
				return filepath.SkipAll
			}
			return nil
		})
		if found {
			return true
		}
	}
	return false
}

func canonicalJanModelID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimSuffix(value, ".gguf")
	return strings.TrimSpace(value)
}

func janInstalledModelInfo(id string, directories []string) (janModelInfo, bool) {
	info, ok := janInstalledModelIndex(directories)[canonicalJanModelID(id)]
	return info, ok
}
