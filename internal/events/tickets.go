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
	mu      sync.Mutex
	tickets map[string]time.Time
	now     func() time.Time
}

func NewTickets() *Tickets {
	return &Tickets{tickets: make(map[string]time.Time), now: time.Now}
}

func (t *Tickets) Issue() (string, error) {
	if t == nil {
		return "", errors.New("ticket store is unavailable")
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
	return ticket, nil
}

func (t *Tickets) Consume(ticket string) bool {
	if t == nil || ticket == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	expires, ok := t.tickets[ticket]
	delete(t.tickets, ticket)
	return ok && t.now().Before(expires)
}

func (t *Tickets) pruneLocked() {
	now := t.now()
	for ticket, expires := range t.tickets {
		if !now.Before(expires) {
			delete(t.tickets, ticket)
		}
	}
}
