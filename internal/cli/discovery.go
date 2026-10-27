package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type localModel struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	ID       string `json:"id"`
	Runtime  string `json:"runtime"`
}

type localEndpoint struct {
	Runtime, Provider, BaseURL string
	Ollama                     bool
}

func detectLocalModels(ctx context.Context) []localModel {
	client := directHTTPClient(700 * time.Millisecond)
	return detectLocalModelsAt(ctx, client, []localEndpoint{
		{Runtime: "Ollama", Provider: "ollama", BaseURL: "http://127.0.0.1:11434/v1", Ollama: true},
		{Runtime: "LM Studio", Provider: "lm_studio", BaseURL: "http://127.0.0.1:1234/v1"},
		{Runtime: "llama.cpp", Provider: "openai_compatible", BaseURL: "http://127.0.0.1:8080/v1"},
		{Runtime: "vLLM", Provider: "openai_compatible", BaseURL: "http://127.0.0.1:8000/v1"},
	})
}

func detectLocalModelsAt(ctx context.Context, client *http.Client, endpoints []localEndpoint) []localModel {
	var found []localModel
	for _, candidate := range endpoints {
		if candidate.Ollama {
			var tags struct {
				Models []struct {
					Name string `json:"name"`
				} `json:"models"`
			}
			if getLocalJSON(ctx, client, strings.TrimSuffix(candidate.BaseURL, "/v1")+"/api/tags", &tags) == nil {
				for _, model := range tags.Models {
					if strings.TrimSpace(model.Name) != "" {
						found = append(found, localModel{Provider: candidate.Provider, BaseURL: candidate.BaseURL, ID: model.Name, Runtime: candidate.Runtime})
					}
				}
			}
			continue
		}
		var response struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if getLocalJSON(ctx, client, candidate.BaseURL+"/models", &response) != nil {
			continue
		}
		for _, model := range response.Data {
			if strings.TrimSpace(model.ID) != "" {
				found = append(found, localModel{Provider: candidate.Provider, BaseURL: candidate.BaseURL, ID: model.ID, Runtime: candidate.Runtime})
			}
		}
	}
	return found
}

func discoverAtBaseURL(ctx context.Context, raw string) ([]localModel, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("model endpoint must be a valid HTTP(S) URL without credentials, query, or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("setup detects local providers only; HTTP endpoints must use loopback")
	}
	if u.Scheme == "https" {
		return nil, errors.New("setup detects local providers only; configure a local HTTP loopback endpoint")
	}
	base := strings.TrimRight(u.String(), "/")
	path := base + "/models"
	if !strings.HasSuffix(base, "/v1") {
		path = base + "/v1/models"
		base += "/v1"
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	client := directHTTPClient(time.Second)
	if err := getLocalJSON(ctx, client, path, &response); err != nil {
		return nil, errors.New("could not detect a local model service at --base-url")
	}
	models := make([]localModel, 0, len(response.Data))
	for _, m := range response.Data {
		if strings.TrimSpace(m.ID) != "" {
			models = append(models, localModel{Provider: "openai_compatible", BaseURL: base, ID: m.ID, Runtime: "OpenAI-compatible local service"})
		}
	}
	return models, nil
}

func getLocalJSON(ctx context.Context, client *http.Client, address string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("local provider returned %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(target)
}
