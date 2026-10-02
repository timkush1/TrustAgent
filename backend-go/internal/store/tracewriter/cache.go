package tracewriter

import (
	"container/list"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/truthtable/backend-go/internal/store"
)

// cache is the memory copy of the last traces written (least recently written first out): each
// trace as the database stores it once its writes land, so a read from memory answers like
// GetTrace and TraceDecisions. The Writer's mutex guards it.
type cache struct {
	max   int
	order *list.List // of *cached, most recently written first
	items map[string]*list.Element
}

// cached is one trace in memory.
type cached struct {
	id string
	// rec is the traces row; nil while only decisions of the trace were written.
	rec *store.TraceRecord
	// decisions are the decision/v1 documents by attempt; late marks a late decision's attempt.
	decisions map[int]json.RawMessage
	late      map[int]bool
	// lost: a write of the trace never reached the database, so memory is ahead of it.
	lost bool
}

func newCache(max int) *cache {
	return &cache{max: max, order: list.New(), items: map[string]*list.Element{}}
}

// entry returns the trace's entry, adding it (and evicting the oldest) when it is new.
func (c *cache) entry(id string) *cached {
	if el, ok := c.items[id]; ok {
		c.order.MoveToFront(el)
		e, _ := el.Value.(*cached)
		return e
	}
	e := &cached{id: id, decisions: map[int]json.RawMessage{}, late: map[int]bool{}}
	c.items[id] = c.order.PushFront(e)
	for c.order.Len() > c.max {
		last := c.order.Back()
		old, _ := last.Value.(*cached)
		c.order.Remove(last)
		delete(c.items, old.id)
	}
	return e
}

// peek returns the trace's entry, or nil, without changing the order.
func (c *cache) peek(id string) *cached {
	if el, ok := c.items[id]; ok {
		e, _ := el.Value.(*cached)
		return e
	}
	return nil
}

// lose marks that a write of the trace did not reach the database.
func (c *cache) lose(id string) {
	if e := c.peek(id); e != nil {
		e.lost = true
	}
}

// apply records a write as its method stores it (store/traces.go).
func (c *cache) apply(w *store.TraceWrite, now time.Time) {
	e := c.entry(w.TraceID())
	switch w.Kind {
	case store.WriteStart:
		// ON CONFLICT DO NOTHING: a trace already written stays as it is.
		if e.rec == nil {
			rec := w.Trace.Stored(now)
			e.rec = &rec
		}
	case store.WriteDecision:
		rec := w.Trace.Stored(now)
		if e.rec != nil {
			rec = store.MergeDecided(e.rec, &rec)
		}
		e.rec = &rec
		e.decide(w.Decision)
	case store.WriteAttempt:
		e.decide(w.Decision)
	case store.WriteLateDecision:
		e.decisions[w.Decision.Attempt] = w.Decision.Decision
		e.late[w.Decision.Attempt] = true
		if e.rec != nil && !slices.Contains(e.rec.Flags, store.FlagLateDecision) {
			e.rec.Flags = append(slices.Clone(e.rec.Flags), store.FlagLateDecision)
			e.rec.UpdatedAt = now.UTC()
		}
	}
}

// decide records an attempt's decision; a late decision already recorded for it stays (the gate
// decision upsert skips a late row).
func (e *cached) decide(d *store.DecisionRecord) {
	if e.late[d.Attempt] {
		return
	}
	e.decisions[d.Attempt] = d.Decision
	e.late[d.Attempt] = d.Late
}

// auditUpdate records SetTraceAudit: nil and empty fields keep their value.
func (e *cached) auditUpdate(u store.TraceAuditUpdate, now time.Time) {
	if e.rec == nil {
		return
	}
	if u.AuditID != nil {
		e.rec.AuditID = u.AuditID
	}
	if u.Status != "" {
		e.rec.AuditStatus = u.Status
	}
	if u.Lane != nil {
		e.rec.AuditLane = u.Lane
	}
	e.rec.UpdatedAt = now.UTC()
}

// auditDropped records DropTraceAudit: the audit status becomes dropped and flag (when set) joins
// the flags once.
func (e *cached) auditDropped(flag string, now time.Time) {
	if e.rec == nil {
		return
	}
	e.rec.AuditStatus = store.TraceAuditDropped
	if flag != "" && !slices.Contains(e.rec.Flags, flag) {
		e.rec.Flags = append(slices.Clone(e.rec.Flags), flag)
	}
	e.rec.UpdatedAt = now.UTC()
}

// auditSaved records SaveAuditResult: the trace points at the audit with its status, and the
// summary joins the audit's verdict, score, grade and claim counts as GetTrace does.
func (e *cached) auditSaved(a *store.AuditRecord, status string, now time.Time) {
	if e.rec == nil || status == "" {
		return
	}
	id := a.AuditID
	e.rec.AuditID, e.rec.AuditStatus, e.rec.UpdatedAt = &id, status, now.UTC()
	counts := store.NewVerdictCounts()
	for _, cl := range a.Claims {
		if _, ok := counts[cl.Status]; ok {
			counts[cl.Status]++
		}
	}
	e.rec.Audit = store.AuditJoin{Verdict: a.Verdict, TrustScore: a.FaithfulnessScore, Grade: a.Grade, Counts: counts}
}

// trace returns a copy of the trace's row, or nil when memory has none.
func (e *cached) trace() *store.TraceRecord {
	if e == nil || e.rec == nil {
		return nil
	}
	rec := *e.rec
	rec.Reasons, rec.Flags = slices.Clone(rec.Reasons), slices.Clone(rec.Flags)
	rec.Audit.Counts = maps.Clone(rec.Audit.Counts)
	return &rec
}

// decisionDocs returns the decision documents by attempt, as TraceDecisions does.
func (e *cached) decisionDocs() []json.RawMessage {
	attempts := slices.Sorted(maps.Keys(e.decisions))
	out := make([]json.RawMessage, 0, len(attempts))
	for _, a := range attempts {
		out = append(out, slices.Clone(e.decisions[a]))
	}
	return out
}
