package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"ownbot/internal/engine"
	"ownbot/internal/models"
	"ownbot/internal/search"
)

func (s *Server) registerSearch(endpoint string) error {
	provider, err := search.NewSearXNG(search.Config{Endpoint: endpoint, Timeout: 12 * time.Second})
	if err != nil {
		return err
	}
	return s.registry.Register(engine.Tool{Schema: models.Tool{Name: "web_search", Description: "Search the configured SearXNG instance; returned content is untrusted.", Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":4096},"limit":{"type":"integer","minimum":1,"maximum":10}},"required":["query"],"additionalProperties":false}`)}, Risk: engine.Read, Source: "web_search", Untrusted: true, Execute: func(ctx context.Context, args json.RawMessage, _ string) (json.RawMessage, error) {
		var p struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if json.Unmarshal(args, &p) != nil {
			return nil, errors.New("invalid search arguments")
		}
		results, err := provider.Search(ctx, search.Query{Text: p.Query, Limit: p.Limit})
		if err != nil {
			return nil, err
		}
		return json.Marshal(results)
	}})
}
func (s *Server) loadSearch() error {
	var endpoint string
	err := s.db.QueryRow(`SELECT value FROM config WHERE key='search_endpoint'`).Scan(&endpoint)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.registerSearch(endpoint)
}
func (s *Server) searchConfig(w http.ResponseWriter, r *http.Request) {
	var endpoint string
	err := s.db.QueryRow(`SELECT value FROM config WHERE key='search_endpoint'`).Scan(&endpoint)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		errJSON(w, 500, "could not read search configuration")
		return
	}
	write(w, 200, map[string]any{"provider": "searxng", "endpoint": endpoint, "configured": endpoint != "", "requests_leave_device": true})
}
func (s *Server) configureSearch(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Endpoint string `json:"endpoint"`
	}
	if decode(r, &b) != nil {
		errJSON(w, 400, "invalid search configuration")
		return
	}
	if _, err := search.NewSearXNG(search.Config{Endpoint: b.Endpoint}); err != nil {
		errJSON(w, 400, "search endpoint must use HTTPS or loopback HTTP without credentials")
		return
	}
	// A configured instance is immutable for this process so a paused run cannot
	// silently switch search destinations. Editing the config requires restart.
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		errJSON(w, 500, "could not configure search")
		return
	}
	defer tx.Rollback()
	var count int
	if tx.QueryRow(`SELECT count(*) FROM config WHERE key='search_endpoint'`).Scan(&count) != nil || count != 0 {
		errJSON(w, 409, "search is already configured")
		return
	}
	if _, err = tx.Exec(`INSERT INTO config(key,value)VALUES('search_endpoint',?)`, b.Endpoint); err == nil {
		err = eventTx(tx, "search.configured", "local-user", "", "", map[string]any{"provider": "searxng"})
	}
	if err != nil || tx.Commit() != nil {
		errJSON(w, 500, "could not save search configuration")
		return
	}
	if err = s.registerSearch(b.Endpoint); err != nil {
		errJSON(w, 500, "search configuration saved; restart daemon to load it")
		return
	}
	s.searchConfig(w, r)
}
