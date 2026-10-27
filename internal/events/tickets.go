package events

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

const (
	ticketTTL  = 30 * time.Second
	maxTickets = 128
)

var ErrTicketCapacity = errors.New("websocket ticket capacity reached")

// Tickets are short-lived, one-use credentials for a single websocket upgrade.
type Tickets struct {
	mu         sync.Mutex
	tickets    map[string]time.Time
	principals map[string]string
	now        func() time.Time
}

func NewTickets() *Tickets {
	return &Tickets{tickets: make(map[string]time.Time), principals: make(map[string]string), now: time.Now}
}

func (t *Tickets) Issue() (string, error) {
	return t.IssueFor("admin")
}

// IssueFor binds a short-lived websocket ticket to the authenticated principal
// that requested it. The principal is never sent in the ticket itself.
func (t *Tickets) IssueFor(principal string) (string, error) {
	if t == nil {
		return "", errors.New("ticket store is unavailable")
	}
	if principal == "" {
		return "", errors.New("ticket principal is required")
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	ticket := base64.RawURLEncoding.EncodeToString(b[:])
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked()
	if len(t.tickets) >= maxTickets {
		return "", ErrTicketCapacity
	}
	t.tickets[ticket] = t.now().Add(ticketTTL)
	t.principals[ticket] = principal
	return ticket, nil
}

func (t *Tickets) Consume(ticket string) bool {
	_, ok := t.ConsumeFor(ticket)
	return ok
}

// ConsumeFor returns the principal bound to a one-use ticket.
func (t *Tickets) ConsumeFor(ticket string) (string, bool) {
	if t == nil || ticket == "" {
		return "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	expires, ok := t.tickets[ticket]
	principal := t.principals[ticket]
	delete(t.tickets, ticket)
	delete(t.principals, ticket)
	return principal, ok && t.now().Before(expires)
}

func (t *Tickets) pruneLocked() {
	now := t.now()
	for ticket, expires := range t.tickets {
		if !now.Before(expires) {
			delete(t.tickets, ticket)
			delete(t.principals, ticket)
		}
	}
}
