package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultAPIURL = "http://127.0.0.1:8765"

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(apiURL, dataDir string) (*Client, error) {
	if strings.TrimSpace(apiURL) == "" {
		apiURL = DefaultAPIURL
	}
	u, err := url.Parse(strings.TrimRight(apiURL, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("API URL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	if strings.ToLower(u.Hostname()) != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("this daemon build accepts loopback API URLs only")
	}
	if dataDir == "" {
		var err error
		dataDir, err = defaultDataDir()
		if err != nil {
			return nil, err
		}
	}
	tokenBytes, err := os.ReadFile(filepath.Join(dataDir, "api.token"))
	if err != nil {
		return nil, errors.New("could not read local API token; start ownbotd first and check the selected data directory")
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return nil, errors.New("local API token file is empty")
	}
	return &Client{baseURL: strings.TrimRight(u.String(), "/"), token: token, http: &http.Client{Timeout: 5 * time.Minute}}, nil
}

func defaultDataDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("OWNBOT_DATA_DIR")); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("could not determine home directory for the local API token")
	}
	return filepath.Join(home, ".ownbot"), nil
}

func (c *Client) Do(ctx context.Context, method, path string, body any, result any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode API request: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return errors.New("could not create local API request")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("local API request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiError struct {
			Error string `json:"error"`
		}
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		_ = json.Unmarshal(limited, &apiError)
		if apiError.Error != "" {
			return fmt.Errorf("local API returned %s: %s", resp.Status, apiError.Error)
		}
		return fmt.Errorf("local API returned %s", resp.Status)
	}
	if result == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(result); err != nil {
		return fmt.Errorf("local API returned an invalid response: %w", err)
	}
	return nil
}

func backgroundContext() context.Context { return context.Background() }

func writeJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func writeJSONLines(w io.Writer, items []json.RawMessage) error {
	for _, item := range items {
		var compact bytes.Buffer
		if err := json.Compact(&compact, item); err != nil {
			return fmt.Errorf("audit response contains invalid JSON: %w", err)
		}
		if _, err := w.Write(compact.Bytes()); err != nil {
			return err
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}
	return nil
}
