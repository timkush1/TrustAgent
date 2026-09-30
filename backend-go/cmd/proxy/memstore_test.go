package main

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/store"
)

// memStore is an in-memory store.Store for tests that need no Postgres: it keeps what the sink
// and the worker write and serves the REST readers from it. The Postgres store's own tests cover
// the SQL; the e2e test runs the same flow against Postgres when TEST_DATABASE_URL is set.
type memStore struct {
	mu     sync.Mutex
	traces map[string]*store.TraceRecord
	order  []string // trace ids, oldest first
	bodies map[string]*store.BodyRecord
	// decisions holds each trace's decision/v1 documents by attempt (gate_decisions is unique on
	// trace_id and attempt).
	decisions map[string]map[int]json.RawMessage
	audits    map[string]*store.AuditRecord
	auditIDs  []string
}

var _ store.Store = (*memStore)(nil)

func newMemStore() *memStore {
	return &memStore{
		traces: map[string]*store.TraceRecord{}, bodies: map[string]*store.BodyRecord{},
		decisions: map[string]map[int]json.RawMessage{}, audits: map[string]*store.AuditRecord{},
	}
}

func (m *memStore) StartTrace(_ context.Context, t *store.TraceRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.traces[t.TraceID]; ok {
		return nil
	}
	cp := *t
	if cp.UpdatedAt.IsZero() {
		cp.UpdatedAt = cp.CreatedAt
	}
	m.traces[t.TraceID] = &cp
	m.order = append(m.order, t.TraceID)
	return nil
}

func (m *memStore) RecordDecision(_ context.Context, t *store.TraceRecord, d *store.DecisionRecord, b *store.BodyRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *t
	cp.UpdatedAt = time.Now().UTC()
	if old, ok := m.traces[t.TraceID]; ok {
		if cp.QuestionPreview == nil {
			cp.QuestionPreview = old.QuestionPreview
		}
		cp.CreatedAt = old.CreatedAt
		// A late decision recorded before this write keeps its flag, as in the Postgres store.
		if slices.Contains(old.Flags, gate.FlagLateDecision) && !slices.Contains(cp.Flags, gate.FlagLateDecision) {
			cp.Flags = append(slices.Clone(cp.Flags), gate.FlagLateDecision)
		}
	} else {
		m.order = append(m.order, t.TraceID)
	}
	m.traces[t.TraceID] = &cp
	m.putDecision(d)
	if b != nil {
		bc := *b
		m.bodies[t.TraceID] = &bc
	}
	return nil
}

// RecordAttempt writes a later attempt's decision only, as the Postgres store does.
func (m *memStore) RecordAttempt(_ context.Context, d *store.DecisionRecord) error {
	if d == nil || d.TraceID == "" || d.Attempt < 1 {
		return errors.New("memstore: RecordAttempt needs a trace id and attempt 1 or later")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putDecision(d)
	return nil
}

// RecordLateDecision replaces an attempt's decision with its late one and flags the trace
// late_decision, leaving every other trace field as it was, as the Postgres store does.
func (m *memStore) RecordLateDecision(_ context.Context, d *store.DecisionRecord) error {
	if d == nil || d.TraceID == "" || !d.Late {
		return errors.New("memstore: RecordLateDecision needs a late decision with a trace id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putDecision(d)
	if t, ok := m.traces[d.TraceID]; ok && !slices.Contains(t.Flags, gate.FlagLateDecision) {
		cp := *t
		cp.Flags = append(slices.Clone(t.Flags), gate.FlagLateDecision)
		cp.UpdatedAt = time.Now().UTC()
		m.traces[d.TraceID] = &cp
	}
	return nil
}

// putDecision upserts d's attempt; the caller holds m.mu.
func (m *memStore) putDecision(d *store.DecisionRecord) {
	byAttempt := m.decisions[d.TraceID]
	if byAttempt == nil {
		byAttempt = map[int]json.RawMessage{}
		m.decisions[d.TraceID] = byAttempt
	}
	byAttempt[d.Attempt] = d.Decision
}

func (m *memStore) SetTraceAudit(_ context.Context, traceID string, u store.TraceAuditUpdate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.traces[traceID]
	if !ok {
		return nil
	}
	if u.AuditID != nil {
		t.AuditID = u.AuditID
	}
	if u.Status != "" {
		t.AuditStatus = u.Status
	}
	if u.Lane != nil {
		t.AuditLane = u.Lane
	}
	t.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *memStore) SaveAudit(ctx context.Context, r *store.AuditRecord) error {
	return m.SaveAuditResult(ctx, r, "")
}

func (m *memStore) SaveAuditResult(_ context.Context, r *store.AuditRecord, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.audits[r.AuditID]; ok {
		return store.ErrDuplicate
	}
	cp := *r
	if cp.TraceID == "" {
		cp.TraceID = cp.AuditID
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	cp.Counts = store.NewVerdictCounts()
	for _, c := range cp.Claims {
		cp.Counts[c.Status]++
	}
	m.audits[r.AuditID] = &cp
	m.auditIDs = append(m.auditIDs, r.AuditID)
	if t, ok := m.traces[cp.TraceID]; ok && status != "" {
		id := cp.AuditID
		t.AuditID, t.AuditStatus, t.UpdatedAt = &id, status, time.Now().UTC()
	}
	return nil
}

// withAudit joins a trace's audit outcome, as the Postgres readers do.
func (m *memStore) withAudit(t *store.TraceRecord) store.TraceRecord {
	cp := *t
	if t.AuditID != nil {
		if a, ok := m.audits[*t.AuditID]; ok {
			cp.Audit = store.AuditJoin{Verdict: a.Verdict, TrustScore: a.FaithfulnessScore, Grade: a.Grade, Counts: a.Counts}
		}
	}
	return cp
}

// ListTraces filters on route and outcome only (what the tests use) and pages by position.
func (m *memStore) ListTraces(_ context.Context, f store.TraceFilter) ([]store.TraceRecord, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []store.TraceRecord
	for i := len(m.order) - 1; i >= 0; i-- {
		t := m.withAudit(m.traces[m.order[i]])
		if f.Route != "" && t.Route != f.Route {
			continue
		}
		if f.Outcome != "" && store.Outcome(outcomeFields(&t)) != f.Outcome {
			continue
		}
		all = append(all, t)
	}
	start := 0
	if f.Cursor != "" {
		n, err := strconv.Atoi(f.Cursor)
		if err != nil || n < 0 || n > len(all) {
			return nil, "", store.ErrBadCursor
		}
		start = n
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	end := min(start+limit, len(all))
	next := ""
	if end < len(all) {
		next = strconv.Itoa(end)
	}
	return slices.Clone(all[start:end]), next, nil
}

func outcomeFields(t *store.TraceRecord) store.OutcomeFields {
	return store.OutcomeFields{GateStatus: t.GateStatus, Action: deref(t.Action), WouldAction: deref(t.WouldAction)}
}

func (m *memStore) GetTrace(_ context.Context, id string) (*store.TraceRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.traces[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := m.withAudit(t)
	return &cp, nil
}

func (m *memStore) GetTraceBody(_ context.Context, id string) (*store.BodyRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bodies[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *b
	return &cp, nil
}

func (m *memStore) TraceDecisions(_ context.Context, id string) ([]json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	attempts := slices.Sorted(maps.Keys(m.decisions[id]))
	out := make([]json.RawMessage, 0, len(attempts))
	for _, a := range attempts {
		out = append(out, m.decisions[id][a])
	}
	return out, nil
}

func (m *memStore) ListAudits(_ context.Context, f store.AuditFilter) ([]store.AuditRecord, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.AuditRecord
	for i := len(m.auditIDs) - 1; i >= 0; i-- {
		a := m.audits[m.auditIDs[i]]
		if f.Status != "" && a.Status != f.Status {
			continue
		}
		cp := *a
		cp.Claims = nil
		out = append(out, cp)
	}
	return out, "", nil
}

func (m *memStore) GetAudit(_ context.Context, id string) (*store.AuditRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.audits[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *a
	return &cp, nil
}

func (m *memStore) LatestAuditForTrace(_ context.Context, traceID string) (*store.AuditRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.auditIDs) - 1; i >= 0; i-- {
		if a := m.audits[m.auditIDs[i]]; a.TraceID == traceID {
			cp := *a
			return &cp, nil
		}
	}
	return nil, store.ErrNotFound
}

func (m *memStore) Ping(context.Context) error { return nil }

func (m *memStore) Migration(context.Context) (string, error) { return "0002_traces.up.sql", nil }

func (m *memStore) Close() {}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
