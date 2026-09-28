// Package policytest provides an in-memory policy version store for tests: the same contract as
// the policy_versions queries of *store.PostgresStore (write lock, newest-first keyset pages,
// NOTIFY on insert), plus recorded decisions for dry-run tests.
package policytest

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/store"
)

// Store is an in-memory policy version store. The zero value is not usable; call New.
type Store struct {
	lock sync.Mutex // the policy write lock

	mu        sync.Mutex
	versions  []store.PolicyVersionRecord
	listeners map[int]chan string
	nextL     int
	now       func() time.Time

	// Rows are the recorded decisions ForEachReplayDecision serves, newest first.
	Rows []store.ReplayRow
	// Bodies are the trace bodies GetTraceBody serves.
	Bodies map[string]*store.BodyRecord
	// FailHead makes PolicyHeadID fail (a lost database).
	FailHead error
}

// New returns an empty store.
func New() *Store {
	return &Store{listeners: map[int]chan string{}, now: time.Now, Bodies: map[string]*store.BodyRecord{}}
}

// Versions returns a copy of every stored version, oldest first.
func (s *Store) Versions() []store.PolicyVersionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.versions)
}

// WithPolicyLock implements the policy write lock; fn's error discards its inserts.
func (s *Store) WithPolicyLock(ctx context.Context, channel string, fn func(store.PolicyTx) error) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	tx := &memTx{s: s}
	if err := fn(tx); err != nil {
		return err
	}
	s.mu.Lock()
	for _, v := range tx.pending {
		s.versions = append(s.versions, v)
	}
	s.mu.Unlock()
	for _, v := range tx.pending {
		s.notify("policy_versions:" + strconv.FormatInt(v.ID, 10))
	}
	return nil
}

type memTx struct {
	s       *Store
	pending []store.PolicyVersionRecord
}

func (t *memTx) all() []store.PolicyVersionRecord {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	return append(slices.Clone(t.s.versions), t.pending...)
}

func newest(vs []store.PolicyVersionRecord, keep func(*store.PolicyVersionRecord) bool) *store.PolicyVersionRecord {
	for i := len(vs) - 1; i >= 0; i-- {
		if keep(&vs[i]) {
			v := vs[i]
			return &v
		}
	}
	return nil
}

func (t *memTx) Head(context.Context) (*store.PolicyVersionRecord, error) {
	return newest(t.all(), func(*store.PolicyVersionRecord) bool { return true }), nil
}

func (t *memTx) LastFileVersion(context.Context) (*store.PolicyVersionRecord, error) {
	return newest(t.all(), func(v *store.PolicyVersionRecord) bool { return v.FileSHA256 != nil }), nil
}

func (t *memTx) LastDashboardVersion(context.Context) (*store.PolicyVersionRecord, error) {
	return newest(t.all(), func(v *store.PolicyVersionRecord) bool {
		return v.Source == store.PolicySourceDashboard || v.Source == store.PolicySourceRollback
	}), nil
}

func (t *memTx) Insert(_ context.Context, v *store.PolicyVersionRecord) error {
	switch v.Source {
	case store.PolicySourceBootstrap, store.PolicySourceFile, store.PolicySourceDashboard, store.PolicySourceRollback, store.PolicySourceAdoptFile:
	default:
		return errors.New("policytest: invalid source " + v.Source)
	}
	if len(v.Doc) == 0 || v.YAML == "" || v.SHA256 == "" || v.Author == "" {
		return errors.New("policytest: a policy version needs doc, yaml, sha256 and author")
	}
	all := t.all()
	v.ID = int64(len(all) + 1)
	v.CreatedAt = t.s.now().UTC()
	t.pending = append(t.pending, *v)
	return nil
}

// Insert adds a version outside the engine (another writer).
func (s *Store) Insert(v store.PolicyVersionRecord) int64 {
	var id int64
	_ = s.WithPolicyLock(context.Background(), "", func(tx store.PolicyTx) error {
		err := tx.Insert(context.Background(), &v)
		id = v.ID
		return err
	})
	return id
}

// LatestPolicyVersion returns the newest version or store.ErrNotFound.
func (s *Store) LatestPolicyVersion(context.Context) (*store.PolicyVersionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.versions) == 0 {
		return nil, store.ErrNotFound
	}
	v := s.versions[len(s.versions)-1]
	return &v, nil
}

// PolicyHeadID returns the newest id (0 when empty).
func (s *Store) PolicyHeadID(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailHead != nil {
		return 0, s.FailHead
	}
	return int64(len(s.versions)), nil
}

// GetPolicyVersion returns one version or store.ErrNotFound.
func (s *Store) GetPolicyVersion(_ context.Context, id int64) (*store.PolicyVersionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 1 || id > int64(len(s.versions)) {
		return nil, store.ErrNotFound
	}
	v := s.versions[id-1]
	return &v, nil
}

// ListPolicyVersions returns a newest-first keyset page.
func (s *Store) ListPolicyVersions(_ context.Context, limit int, cursor string) ([]store.PolicyVersionRecord, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	before := int64(1 << 62)
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		id, perr := strconv.ParseInt(strings.TrimPrefix(string(raw), "id|"), 10, 64)
		if err != nil || perr != nil {
			return nil, "", store.ErrBadCursor
		}
		before = id
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []store.PolicyVersionRecord{}
	for i := len(s.versions) - 1; i >= 0 && len(out) <= limit; i-- {
		if s.versions[i].ID < before {
			out = append(out, s.versions[i])
		}
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = base64.RawURLEncoding.EncodeToString([]byte("id|" + strconv.FormatInt(out[limit-1].ID, 10)))
	}
	return out, next, nil
}

// Listen delivers insert notifications until ctx ends.
func (s *Store) Listen(ctx context.Context, _ string, ready func(), fn func(string)) error {
	ch := make(chan string, 64)
	s.mu.Lock()
	id := s.nextL
	s.nextL++
	s.listeners[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}()
	if ready != nil {
		ready()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case p := <-ch:
			fn(p)
		}
	}
}

func (s *Store) notify(payload string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.listeners {
		select {
		case ch <- payload:
		default:
		}
	}
}

// ForEachReplayDecision serves Rows within the filter, in order.
func (s *Store) ForEachReplayDecision(_ context.Context, f store.ReplayFilter, fn func(*store.ReplayRow) error) error {
	n := 0
	for i := range s.Rows {
		r := s.Rows[i]
		if r.CreatedAt.Before(f.Since) || (len(f.Routes) > 0 && !slices.Contains(f.Routes, r.Route)) {
			continue
		}
		if f.Limit > 0 && n >= f.Limit {
			break
		}
		n++
		if err := fn(&r); err != nil {
			return err
		}
	}
	return nil
}

// GetTraceBody serves Bodies.
func (s *Store) GetTraceBody(_ context.Context, traceID string) (*store.BodyRecord, error) {
	if b, ok := s.Bodies[traceID]; ok {
		return b, nil
	}
	return nil, store.ErrNotFound
}
