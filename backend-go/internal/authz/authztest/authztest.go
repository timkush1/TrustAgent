// Package authztest provides an in-memory authz.Store for tests. It follows the Postgres store's
// semantics (setup-claim outcomes, revocation, the session epoch), so handler and authz tests run
// without a database; the Postgres behaviour itself is tested in internal/store and, for the
// concurrent setup claim, in internal/authz with storetest.
package authztest

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/store"
)

// Store is an in-memory authz.Store. Err, when set, is returned by every method (a database
// outage).
type Store struct {
	mu      sync.Mutex
	keys    map[string]*store.APIKeyRecord
	setup   *store.SetupState
	epoch   int64
	Audit   []store.AdminAuditEntry
	Err     error
	Lookups int // APIKeyBySHA256 and APIKeyByID calls
	// EpochReads counts SessionEpoch calls; a non-nil EpochGate makes SessionEpoch wait for a
	// receive on it first (a slow database).
	EpochReads int
	EpochGate  chan struct{}
}

// New returns an empty store.
func New() *Store { return &Store{keys: map[string]*store.APIKeyRecord{}} }

func clone(k *store.APIKeyRecord) *store.APIKeyRecord {
	c := *k
	c.Scopes = slices.Clone(k.Scopes)
	return &c
}

// Reads returns EpochReads under the lock.
func (s *Store) Reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.EpochReads
}

// SetGate installs (or removes, with nil) the SessionEpoch gate.
func (s *Store) SetGate(g chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.EpochGate = g
}

// SetErr sets or clears the outage error.
func (s *Store) SetErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Err = err
}

// AuditActions lists the admin audit actions recorded so far.
func (s *Store) AuditActions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, e := range s.Audit {
		out = append(out, e.Action)
	}
	return out
}

// CreateAPIKey implements authz.Store.
func (s *Store) CreateAPIKey(_ context.Context, k *store.APIKeyRecord, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	for _, other := range s.keys {
		if other.SHA256 == k.SHA256 || other.ID == k.ID {
			return errors.New("authztest: duplicate key")
		}
	}
	k.CreatedAt = time.Now().UTC()
	s.keys[k.ID] = clone(k)
	s.Audit = append(s.Audit, store.AdminAuditEntry{Actor: actor, Action: "api_key.create", Target: k.ID})
	return nil
}

// APIKeyBySHA256 implements authz.Store.
func (s *Store) APIKeyBySHA256(_ context.Context, sha string) (*store.APIKeyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Lookups++
	if s.Err != nil {
		return nil, s.Err
	}
	for _, k := range s.keys {
		if k.SHA256 == sha {
			return clone(k), nil
		}
	}
	return nil, store.ErrNotFound
}

// APIKeyByID implements authz.Store.
func (s *Store) APIKeyByID(_ context.Context, id string) (*store.APIKeyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Lookups++
	if s.Err != nil {
		return nil, s.Err
	}
	if k, ok := s.keys[id]; ok {
		return clone(k), nil
	}
	return nil, store.ErrNotFound
}

// ListAPIKeys implements authz.Store.
func (s *Store) ListAPIKeys(context.Context) ([]store.APIKeyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	out := []store.APIKeyRecord{}
	for _, k := range s.keys {
		out = append(out, *clone(k))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// RevokeAPIKey implements authz.Store.
func (s *Store) RevokeAPIKey(_ context.Context, id, actor string) (*store.APIKeyRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, false, s.Err
	}
	k, ok := s.keys[id]
	if !ok {
		return nil, false, store.ErrNotFound
	}
	if k.RevokedAt != nil {
		return clone(k), false, nil
	}
	now := time.Now().UTC()
	k.RevokedAt = &now
	s.Audit = append(s.Audit, store.AdminAuditEntry{Actor: actor, Action: "api_key.revoke", Target: id})
	return clone(k), true, nil
}

// RevokeDirect revokes a key without going through an Authenticator (another replica did it).
func (s *Store) RevokeDirect(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.keys[id]; ok && k.RevokedAt == nil {
		now := time.Now().UTC()
		k.RevokedAt = &now
	}
}

// TouchAPIKey implements authz.Store.
func (s *Store) TouchAPIKey(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	if k, ok := s.keys[id]; ok {
		t := at.UTC()
		k.LastUsedAt = &t
	}
	return nil
}

// AdminKeyExists implements authz.Store.
func (s *Store) AdminKeyExists(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return false, s.Err
	}
	return s.adminExists(), nil
}

func (s *Store) adminExists() bool {
	for _, k := range s.keys {
		if k.RevokedAt == nil && slices.Contains(k.Scopes, "admin") {
			return true
		}
	}
	return false
}

// PutSetupToken implements authz.Store: it adds a pending token (dropping expired ones, keeping
// the newest store.MaxPendingSetupTokens); a claimed state starts over.
func (s *Store) PutSetupToken(_ context.Context, rec store.SetupTokenRecord, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	if s.setup == nil || s.setup.ClaimedAt != nil {
		s.setup = &store.SetupState{Tokens: []store.SetupTokenRecord{rec}}
		return nil
	}
	var keep []store.SetupTokenRecord
	for _, t := range s.setup.Tokens {
		if t.ExpiresAt.After(rec.CreatedAt) {
			keep = append(keep, t)
		}
	}
	if n := store.MaxPendingSetupTokens - 1; len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	s.setup.Tokens = append(keep, rec)
	return nil
}

// SetupToken returns a copy of the setup state (nil when none).
func (s *Store) SetupToken() *store.SetupState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setup == nil {
		return nil
	}
	c := *s.setup
	c.Tokens = slices.Clone(s.setup.Tokens)
	return &c
}

// ClaimSetupToken implements authz.Store with the Postgres store's outcomes.
func (s *Store) ClaimSetupToken(_ context.Context, sha string, now time.Time, envAdmin bool, admin *store.APIKeyRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	if s.setup == nil {
		return store.ErrSetupTokenInvalid
	}
	claimable := false
	for _, t := range s.setup.Tokens {
		if t.SHA256 == sha && t.ExpiresAt.After(now) && (t.Forced || (!envAdmin && !s.adminExists())) {
			claimable = true
		}
	}
	if s.setup.ClaimedAt != nil || !claimable {
		return store.SetupClaimOutcome(s.setup, sha, now)
	}
	t := now.UTC()
	s.setup.ClaimedAt, s.setup.ClaimedKeyID = &t, admin.ID
	admin.CreatedAt = t
	s.keys[admin.ID] = clone(admin)
	s.Audit = append(s.Audit, store.AdminAuditEntry{Actor: "setup", Action: "setup.claim", Target: admin.ID})
	return nil
}

// SessionEpoch implements authz.Store.
func (s *Store) SessionEpoch(ctx context.Context) (int64, error) {
	s.mu.Lock()
	gate := s.EpochGate
	s.EpochReads++
	s.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return 0, s.Err
	}
	return s.epoch, nil
}

// BumpSessionEpoch implements authz.Store.
func (s *Store) BumpSessionEpoch(_ context.Context, actor string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return 0, s.Err
	}
	s.epoch++
	s.Audit = append(s.Audit, store.AdminAuditEntry{Actor: actor, Action: "session.revoke_all"})
	return s.epoch, nil
}

// BumpDirect bumps the epoch without an Authenticator (another replica did it).
func (s *Store) BumpDirect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
}
