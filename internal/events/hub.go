package events

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const subscriberBuffer = 16

// Event contains only explicitly selected operational metadata. It has no
// arbitrary data field, so callers cannot accidentally broadcast prompts,
// tool arguments, provider responses, or credentials.
type Event struct {
	Type       string    `json:"type"`
	At         time.Time `json:"at"`
	RunID      string    `json:"run_id,omitempty"`
	ApprovalID string    `json:"approval_id,omitempty"`
	Status     string    `json:"status,omitempty"`
}

type Hub struct {
	mu             sync.Mutex
	closed         bool
	clients        map[chan Event]struct{}
	done           chan struct{}
	once           sync.Once
	wg             sync.WaitGroup
	tickets        *Tickets
	validPrincipal func(string) bool
}

func NewHub(tickets *Tickets) *Hub {
	if tickets == nil {
		tickets = NewTickets()
	}
	return &Hub{clients: make(map[chan Event]struct{}), done: make(chan struct{}), tickets: tickets}
}

func (h *Hub) Tickets() *Tickets { return h.tickets }

// SetPrincipalValidator installs a check used before a ticket is accepted and
// while websocket connections remain open. Empty principals are never valid.
func (h *Hub) SetPrincipalValidator(validate func(string) bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.validPrincipal = validate
	h.mu.Unlock()
}

// Publish delivers a notification without blocking on slow clients. Overflow
// drops the newest notification for that subscriber.
func (h *Hub) Publish(event Event) {
	if h == nil {
		return
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for ch := range h.clients {
		select {
		case ch <- event:
		default:
		}
	}
}

// Close prevents new subscriptions and wakes existing clients. It is safe to
// call more than once.
func (h *Hub) Close() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		h.mu.Lock()
		h.closed = true
		close(h.done)
		h.mu.Unlock()
	})
}

func (h *Hub) subscribe() (<-chan Event, func(), error) {
	ch := make(chan Event, subscriberBuffer)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, nil, errors.New("event hub is closed")
	}
	h.clients[ch] = struct{}{}
	h.wg.Add(1)
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.clients, ch)
			h.mu.Unlock()
			h.wg.Done()
		})
	}, nil
}

// ServeHTTP is intended to be mounted behind the caller's authenticated HTTP
// middleware and origin policy. Clients must present a one-use ticket using a
// `ownbot-ticket.<ticket>` offered subprotocol; the negotiated protocol is
// always `ownbot` and the long-lived bearer token never enters the handshake.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.tickets == nil {
		http.Error(w, "events unavailable", http.StatusServiceUnavailable)
		return
	}
	ticket := offeredTicket(r.Header.Values("Sec-WebSocket-Protocol"))
	if !offersProtocol(r.Header.Values("Sec-WebSocket-Protocol"), "ownbot") {
		http.Error(w, "ownbot websocket protocol required", http.StatusBadRequest)
		return
	}
	principal, valid := h.tickets.ConsumeFor(ticket)
	if ticket == "" || !valid || !h.principalValid(principal) {
		http.Error(w, "invalid websocket ticket", http.StatusUnauthorized)
		return
	}
	ch, unsubscribe, err := h.subscribe()
	if err != nil {
		http.Error(w, "events unavailable", http.StatusServiceUnavailable)
		return
	}
	defer unsubscribe()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"ownbot"}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1024)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, readErr := conn.Read(ctx); readErr != nil {
				cancel()
				return
			}
		}
	}()

	for {
		// Revocation is observed promptly for already-open sockets.
		ticker := time.NewTimer(time.Second)
		select {
		case <-h.done:
			ticker.Stop()
			_ = conn.Close(websocket.StatusGoingAway, "server shutting down")
			return
		case <-ctx.Done():
			ticker.Stop()
			return
		case <-readDone:
			ticker.Stop()
			return
		case <-ticker.C:
			if !h.principalValid(principal) {
				_ = conn.Close(websocket.StatusPolicyViolation, "access revoked")
				return
			}
		case event := <-ch:
			ticker.Stop()
			writeCtx, writeCancel := context.WithTimeout(ctx, 5*time.Second)
			payload, marshalErr := json.Marshal(event)
			if marshalErr == nil {
				marshalErr = conn.Write(writeCtx, websocket.MessageText, payload)
			}
			writeCancel()
			if marshalErr != nil {
				return
			}
		}
	}
}

func (h *Hub) principalValid(principal string) bool {
	if principal == "" {
		return false
	}
	h.mu.Lock()
	validate := h.validPrincipal
	h.mu.Unlock()
	return validate == nil || validate(principal)
}

func offeredTicket(values []string) string {
	for _, value := range values {
		for _, protocol := range strings.Split(value, ",") {
			protocol = strings.TrimSpace(protocol)
			if strings.HasPrefix(protocol, "ownbot-ticket.") {
				return strings.TrimPrefix(protocol, "ownbot-ticket.")
			}
		}
	}
	return ""
}

func offersProtocol(values []string, wanted string) bool {
	for _, value := range values {
		for _, protocol := range strings.Split(value, ",") {
			if strings.TrimSpace(protocol) == wanted {
				return true
			}
		}
	}
	return false
}
