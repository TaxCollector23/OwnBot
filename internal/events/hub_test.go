package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestTicketOneTimeExpiryAndBound(t *testing.T) {
	tickets := NewTickets()
	now := time.Now()
	tickets.now = func() time.Time { return now }
	ticket, err := tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}
	if !tickets.Consume(ticket) {
		t.Fatal("fresh ticket was rejected")
	}
	if tickets.Consume(ticket) {
		t.Fatal("ticket replay was accepted")
	}
	expired, err := tickets.Issue()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(ticketTTL)
	if tickets.Consume(expired) {
		t.Fatal("expired ticket was accepted")
	}
	for i := 0; i < maxTickets; i++ {
		if _, err := tickets.Issue(); err != nil {
			t.Fatalf("ticket %d: %v", i, err)
		}
	}
	if _, err := tickets.Issue(); err != ErrTicketCapacity {
		t.Fatalf("capacity error=%v", err)
	}
}

func TestHubWebSocketReceivesMetadataAndCloses(t *testing.T) {
	hub := NewHub(nil)
	server := httptest.NewServer(hub)
	defer server.Close()
	defer hub.Close()
	ticket, err := hub.Tickets().Issue()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/events", &websocket.DialOptions{
		Subprotocols: []string{"ownbot-ticket." + ticket, "ownbot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := conn.Subprotocol(); got != "ownbot" {
		t.Fatalf("subprotocol=%q", got)
	}
	hub.Publish(Event{Type: "run.updated", RunID: "run-1", Status: "running"})
	_, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got["type"] != "run.updated" || got["run_id"] != "run-1" || got["status"] != "running" {
		t.Fatalf("event=%v", got)
	}
	if len(got) != 4 {
		t.Fatalf("unexpected event fields may leak data: %v", got)
	}

	hub.Close()
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Fatalf("close status=%v err=%v", websocket.CloseStatus(err), err)
	}
	_ = conn.CloseNow()
}

func TestHubTicketReplayRejectedBeforeUpgrade(t *testing.T) {
	hub := NewHub(nil)
	server := httptest.NewServer(hub)
	defer server.Close()
	defer hub.Close()
	ticket, err := hub.Tickets().Issue()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/events", &websocket.DialOptions{
		Subprotocols: []string{"ownbot-ticket." + ticket, "ownbot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.CloseNow()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Protocol", "ownbot-ticket."+ticket+", ownbot")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay HTTP status=%d", resp.StatusCode)
	}
}

func TestHubExpiredTicketRejectedBeforeUpgrade(t *testing.T) {
	hub := NewHub(nil)
	server := httptest.NewServer(hub)
	defer server.Close()
	defer hub.Close()
	ticket, err := hub.Tickets().Issue()
	if err != nil {
		t.Fatal(err)
	}
	hub.Tickets().mu.Lock()
	hub.Tickets().tickets[ticket] = time.Now().Add(-time.Second)
	hub.Tickets().mu.Unlock()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Sec-WebSocket-Protocol", "ownbot-ticket."+ticket+", ownbot")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired ticket HTTP status=%d", resp.StatusCode)
	}
}
