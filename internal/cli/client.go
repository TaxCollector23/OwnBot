package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const DefaultAPIURL = "http://127.0.0.1:8765"

type Client struct {
	baseURL  string
	token    string
	http     *http.Client
	verified bool
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
	dirInfo, err := os.Lstat(dataDir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && dirInfo.Mode().Perm()&0077 != 0) {
		return nil, errors.New("local state directory is missing or insecure")
	}
	tokenPath := filepath.Join(dataDir, "api.token")
	info, err := os.Lstat(tokenPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("local API token file is missing or insecure")
	}
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, errors.New("could not read local API token; start `ownbot daemon start` and check the selected data directory")
	}
	token := strings.TrimSpace(string(tokenBytes))
	if len(token) != 64 {
		return nil, errors.New("local API token file is malformed")
	}
	if _, err := hex.DecodeString(token); err != nil {
		return nil, errors.New("local API token file is malformed")
	}
	baseURL := strings.TrimRight(u.String(), "/")
	if err := verifyDaemonIdentity(baseURL); err != nil {
		return nil, err
	}
	return &Client{baseURL: baseURL, token: token, http: directHTTPClient(5 * time.Minute), verified: true}, nil
}

func verifyDaemonIdentity(baseURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/status", nil)
	if err != nil {
		return errors.New("could not verify local daemon identity")
	}
	// This preflight intentionally carries no credentials. Do not send the local
	// owner token until the listener proves it is this application's daemon.
	resp, err := directHTTPClient(2 * time.Second).Do(req)
	if err != nil {
		return errors.New("could not verify OwnBot daemon identity; authenticated request was not sent")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("target does not identify as an OwnBot daemon; authenticated request was not sent")
	}
	var identity struct {
		OK      bool   `json:"ok"`
		Service string `json:"service"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&identity) != nil || !identity.OK || identity.Service != "ownbot" {
		return errors.New("target does not identify as an OwnBot daemon; authenticated request was not sent")
	}
	return nil
}

func directHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
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
			Code  string `json:"code"`
		}
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		_ = json.Unmarshal(limited, &apiError)
		if apiError.Error != "" {
			if apiError.Code == "auth_required" {
				return errors.New("MCP server requires sign-in; run `ownbot connect authorize SERVER_ID`")
			}
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
