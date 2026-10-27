// Package search provides explicitly configured web search providers. Results
// are untrusted external data and must not be treated as instructions.
package search

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

var (
	ErrInvalidEndpoint = errors.New("invalid search endpoint")
	ErrInvalidQuery    = errors.New("invalid search query")
)

const (
	defaultTimeout = 12 * time.Second
	maxResponse    = 4 << 20
	maxQueryBytes  = 4096
	maxResults     = 50
)

// Query is a caller's search request. Limit is clamped to [1, 50].
type Query struct {
	Text  string
	Limit int
}

// Result contains untrusted data returned by an external search engine.
type Result struct {
	Title         string  `json:"title"`
	URL           string  `json:"url"`
	Content       string  `json:"content,omitempty"`
	Engine        string  `json:"engine,omitempty"`
	Score         float64 `json:"score,omitempty"`
	PublishedDate string  `json:"published_date,omitempty"`
	Untrusted     bool    `json:"untrusted"`
}

// Provider is the common search-provider interface.
type Provider interface {
	Search(context.Context, Query) ([]Result, error)
}

type Config struct {
	// Endpoint is an explicitly supplied SearXNG instance URL, optionally with
	// a path prefix. HTTPS is required except for localhost/loopback development.
	Endpoint string
	Timeout  time.Duration
}

type SearXNG struct {
	endpoint *url.URL
	client   *http.Client
}

// NewSearXNG constructs a client for one explicitly configured instance.
// Redirects are refused so a trusted instance cannot forward requests to an
// untrusted host. No provider response bodies are included in returned errors.
func NewSearXNG(c Config) (*SearXNG, error) {
	u, err := url.Parse(strings.TrimSpace(c.Endpoint))
	if err != nil || !safeEndpoint(u) {
		return nil, ErrInvalidEndpoint
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout > time.Minute {
		timeout = time.Minute
	}
	return &SearXNG{
		endpoint: u,
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func safeEndpoint(u *url.URL) bool {
	if u == nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type response struct {
	Results []struct {
		Title         string  `json:"title"`
		URL           string  `json:"url"`
		Content       string  `json:"content"`
		Engine        string  `json:"engine"`
		Score         float64 `json:"score"`
		PublishedDate string  `json:"publishedDate"`
	} `json:"results"`
}

func (s *SearXNG) Search(ctx context.Context, q Query) ([]Result, error) {
	if s == nil || s.endpoint == nil || s.client == nil {
		return nil, ErrInvalidEndpoint
	}
	text := strings.TrimSpace(q.Text)
	if text == "" || len(text) > maxQueryBytes {
		return nil, ErrInvalidQuery
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > maxResults {
		limit = maxResults
	}
	u := *s.endpoint
	path := strings.TrimRight(u.Path, "/") + "/search"
	if u.Path == "" || u.Path == "/" {
		path = "/search"
	}
	u.Path = path
	u.RawPath = ""
	params := url.Values{}
	params.Set("q", text)
	params.Set("format", "json")
	u.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrInvalidEndpoint
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, errors.New("search provider request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("search provider returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(body) > maxResponse {
		return nil, errors.New("search provider response exceeds size limit")
	}
	var decoded response
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, errors.New("decode search provider response failed")
	}
	results := make([]Result, 0, min(limit, len(decoded.Results)))
	for _, item := range decoded.Results {
		if len(results) >= limit {
			break
		}
		if !safeResultURL(item.URL) {
			continue
		}
		results = append(results, Result{
			Title: item.Title, URL: item.URL, Content: item.Content, Engine: item.Engine,
			Score: item.Score, PublishedDate: item.PublishedDate, Untrusted: true,
		})
	}
	return results, nil
}

func safeResultURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return false
	}
	return u.Scheme == "https" || u.Scheme == "http"
}
