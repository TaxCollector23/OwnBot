package search

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewSearXNGAllowsHTTPSAndLoopbackHTTPOnly(t *testing.T) {
	for _, endpoint := range []string{
		"https://search.example.org",
		"http://127.0.0.1:8080/searx",
		"http://[::1]:8080",
	} {
		if _, err := NewSearXNG(Config{Endpoint: endpoint}); err != nil {
			t.Errorf("NewSearXNG(%q): %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{
		"",
		"file:///etc/passwd",
		"http://example.org",
		"http://127.0.0.1.evil.test",
		"http://user:pass@localhost",
		"http://localhost?token=x",
		"http://localhost/#fragment",
	} {
		if _, err := NewSearXNG(Config{Endpoint: endpoint}); !errors.Is(err, ErrInvalidEndpoint) {
			t.Errorf("NewSearXNG(%q) error = %v", endpoint, err)
		}
	}
}

func TestSearXNGSearchReturnsUntrustedSafeResultsAndHonorsLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prefix/search" || r.URL.Query().Get("q") != "safe query" || r.URL.Query().Get("format") != "json" {
			t.Errorf("unexpected request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"title":"one","url":"https://example.org/1","content":"untrusted text","engine":"demo","score":1.2},{"title":"bad","url":"javascript:alert(1)"},{"title":"two","url":"http://example.org/2"},{"title":"three","url":"https://example.org/3"}]}`)
	}))
	defer server.Close()
	client, err := NewSearXNG(Config{Endpoint: server.URL + "/prefix"})
	if err != nil {
		t.Fatal(err)
	}
	results, err := client.Search(context.Background(), Query{Text: " safe query ", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Title != "one" || results[1].Title != "two" {
		t.Fatalf("unexpected results: %+v", results)
	}
	for _, result := range results {
		if !result.Untrusted {
			t.Errorf("result not marked untrusted: %+v", result)
		}
	}
}

func TestSearXNGSearchSanitizesFailuresAndRejectsOversizedBodies(t *testing.T) {
	t.Run("status does not expose body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "secret-cookie-provider-error")
		}))
		defer server.Close()
		client, _ := NewSearXNG(Config{Endpoint: server.URL})
		_, err := client.Search(context.Background(), Query{Text: "q"})
		if err == nil || strings.Contains(err.Error(), "secret-cookie") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results":[]}` + strings.Repeat(" ", maxResponse)))
		}))
		defer server.Close()
		client, _ := NewSearXNG(Config{Endpoint: server.URL})
		_, err := client.Search(context.Background(), Query{Text: "q"})
		if err == nil || !strings.Contains(err.Error(), "size limit") {
			t.Fatalf("expected size cap error, got %v", err)
		}
	})
}

func TestSearXNGRefusesRedirectsAndInvalidQueries(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("redirect target must not be contacted")
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	client, err := NewSearXNG(Config{Endpoint: redirect.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(context.Background(), Query{Text: "q"}); err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("expected redirect status error, got %v", err)
	}
	if _, err := client.Search(context.Background(), Query{Text: "\t"}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("empty query error = %v", err)
	}
	if _, err := client.Search(context.Background(), Query{Text: strings.Repeat("x", maxQueryBytes+1)}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("long query error = %v", err)
	}
}
