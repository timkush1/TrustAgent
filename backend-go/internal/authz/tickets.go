package authz

import (
	"crypto/sha256"
	"errors"
	"sync"
	"time"
)

// TicketTTL is the lifetime of a WebSocket ticket (SPEC §2.4: {ticket, expires_in: 30}).
const TicketTTL = 30 * time.Second

// maxTickets bounds the outstanding tickets.
const maxTickets = 10000

// ErrTooManyTickets: the ticket store is full of unexpired tickets.
var ErrTooManyTickets = errors.New("authz: too many outstanding WebSocket tickets")

// Tickets issues single-use WebSocket tickets. Browsers cannot set headers on the WebSocket
// handshake, so a signed-in dashboard exchanges its session (or key) for a ticket and connects with
// /ws?ticket=…. A ticket carries the identity it was issued to, works once and expires after the
// TTL. Only the ticket's SHA-256 is kept. Tickets live in memory: a ticket is redeemed on the
// replica that issued it.
type Tickets struct {
	mu  sync.Mutex
	ttl time.Duration
	now func() time.Time
	m   map[[sha256.Size]byte]ticketEntry
}

type ticketEntry struct {
	id  Identity
	exp time.Time
}

// NewTickets returns a ticket store; ttl <= 0 means TicketTTL and nil now means time.Now.
func NewTickets(ttl time.Duration, now func() time.Time) *Tickets {
	if ttl <= 0 {
		ttl = TicketTTL
	}
	if now == nil {
		now = time.Now
	}
	return &Tickets{ttl: ttl, now: now, m: map[[sha256.Size]byte]ticketEntry{}}
}

// TTL is the ticket lifetime.
func (t *Tickets) TTL() time.Duration { return t.ttl }

// Issue returns a new ticket for id.
func (t *Tickets) Issue(id *Identity) (string, error) {
	if id == nil {
		return "", errors.New("authz: a ticket needs an identity")
	}
	tok := randomToken()
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if len(t.m) >= maxTickets {
		for k, e := range t.m {
			if !now.Before(e.exp) {
				delete(t.m, k)
			}
		}
		if len(t.m) >= maxTickets {
			return "", ErrTooManyTickets
		}
	}
	cp := *id
	cp.Scopes = append([]Scope(nil), id.Scopes...)
	t.m[sha256.Sum256([]byte(tok))] = ticketEntry{id: cp, exp: now.Add(t.ttl)}
	return tok, nil
}

// Consume redeems a ticket: it returns the identity once, while the ticket is unexpired.
func (t *Tickets) Consume(tok string) (*Identity, bool) {
	if tok == "" {
		return nil, false
	}
	k := sha256.Sum256([]byte(tok))
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[k]
	if !ok {
		return nil, false
	}
	delete(t.m, k)
	if !t.now().Before(e.exp) {
		return nil, false
	}
	id := e.id
	id.Kind = KindTicket
	return &id, true
}
