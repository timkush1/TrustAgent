package main

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/proxy"
	"github.com/truthtable/backend-go/internal/store"
)

// The decision long-poll's limits (SPEC §2.4 row 74): wait_ms is clamped to maxDecisionWait, and
// the waiters remember the last decisionEntries traces for decisionTTL.
const (
	maxDecisionWait = 10 * time.Second
	decisionEntries = 4096
	decisionTTL     = 10 * time.Minute
)

// Long-poll states (the response's state).
const (
	decisionPending = "pending"
	decisionDecided = "decided"
)

// decisionWaiters wakes the /v1 decision long-polls: an LRU of the traces this process started or
// decided recently (decisionEntries, decisionTTL), each with the channel its pollers wait on. The
// sink feeds it: TraceStarted registers a pending trace, GateDecided and LateDecided the decision
// of attempt 0. A trace it does not know is read from the database. Safe for concurrent use.
type decisionWaiters struct {
	now func() time.Time

	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List // front is the most recently touched
}

// waitEntry is one trace a long-poll may ask about.
type waitEntry struct {
	traceID  string
	keyLabel string
	// headers is the route's headers setting (full or action_only); doc the decision/v1 document
	// of attempt 0, nil while the trace is pending.
	headers string
	doc     json.RawMessage
	at      time.Time
	// ready is closed once doc is set.
	ready chan struct{}
}

func newDecisionWaiters() *decisionWaiters {
	return &decisionWaiters{now: time.Now, entries: map[string]*list.Element{}, order: list.New()}
}

// started registers a pending trace, unless the trace is known already.
func (w *decisionWaiters) started(traceID, keyLabel string) {
	if w == nil || traceID == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if e := w.lookup(traceID); e != nil {
		return
	}
	w.put(&waitEntry{traceID: traceID, keyLabel: keyLabel, at: w.now(), ready: make(chan struct{})})
}

// decided records the decision of a trace's attempt 0 (a late one replaces it) and wakes its
// pollers.
func (w *decisionWaiters) decided(traceID, keyLabel, headers string, doc json.RawMessage) {
	if w == nil || traceID == "" || len(doc) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	e := w.lookup(traceID)
	if e == nil {
		e = &waitEntry{traceID: traceID, keyLabel: keyLabel, ready: make(chan struct{})}
		w.put(e)
	} else {
		w.order.MoveToFront(w.entries[traceID])
	}
	pending := e.doc == nil
	e.headers, e.doc, e.at = headers, doc, w.now()
	if pending {
		close(e.ready)
	}
}

// get returns a copy of what the waiters know of a trace; ok is false for a trace they do not
// know, or no longer remember.
func (w *decisionWaiters) get(traceID string) (e waitEntry, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p := w.lookup(traceID); p != nil {
		return *p, true
	}
	return waitEntry{}, false
}

// lookup returns the live entry of a trace, dropping it once it is older than decisionTTL. The
// caller holds mu.
func (w *decisionWaiters) lookup(traceID string) *waitEntry {
	el, ok := w.entries[traceID]
	if !ok {
		return nil
	}
	e, _ := el.Value.(*waitEntry)
	if w.now().Sub(e.at) > decisionTTL {
		w.order.Remove(el)
		delete(w.entries, traceID)
		return nil
	}
	return e
}

// put adds an entry and evicts the least recently touched past decisionEntries. The caller holds
// mu.
func (w *decisionWaiters) put(e *waitEntry) {
	w.entries[e.traceID] = w.order.PushFront(e)
	for w.order.Len() > decisionEntries {
		last := w.order.Back()
		old, _ := last.Value.(*waitEntry)
		w.order.Remove(last)
		delete(w.entries, old.traceID)
	}
}

// decisionPoll is the long-poll's answer: {trace_id, state, decision}.
type decisionPoll struct {
	TraceID  string          `json:"trace_id"`
	State    string          `json:"state"`
	Decision json.RawMessage `json:"decision"`
}

// traceDecisionReader is the part of the store the long-poll falls back to.
type traceDecisionReader interface {
	GetTrace(ctx context.Context, traceID string) (*store.TraceRecord, error)
	TraceDecisions(ctx context.Context, traceID string) ([]json.RawMessage, error)
}

// handleDecisionLongPoll serves GET /v1/trustagent/decisions/:trace_id (SPEC §2.4 row 74): the
// decision of a trace's attempt 0 for clients that cannot read the decision headers (observe
// routes, or streams whose trailers were dropped). It waits up to wait_ms (0 by default, clamped
// to 10,000) for a pending trace and answers {trace_id, state: pending|decided, decision}. Only the
// key label that created the trace may ask; any other caller, like an unknown trace, gets 404. A
// route with headers: action_only answers only the decision's action and status. Errors use the
// /v1 error shape.
func (s *server) handleDecisionLongPoll(c *gin.Context) {
	traceID := c.Param("trace_id")
	wait, ok := parseWaitMs(c.Query("wait_ms"))
	if !ok {
		proxy.Deny(c, http.StatusBadRequest, codeBadRequest, "wait_ms must be a whole number of milliseconds from 0 to 10000.")
		c.Abort()
		return
	}
	label := authz.KeyLabel(c)
	e, known := s.waiters.get(traceID)
	if !known {
		var err error
		if e, known, err = s.storedDecision(c.Request.Context(), traceID); err != nil {
			s.log.Error("decision long-poll: reading the trace failed", "trace_id", traceID, "err", err)
			proxy.Deny(c, http.StatusInternalServerError, codeInternal, "The decision could not be read.")
			c.Abort()
			return
		}
	}
	if !known || e.keyLabel != label {
		proxy.Deny(c, http.StatusNotFound, codeNotFound, "No decision is known for this trace id and key.")
		c.Abort()
		return
	}
	if e.doc == nil && wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-e.ready:
			e, _ = s.waiters.get(traceID)
		case <-t.C:
		case <-c.Request.Context().Done():
			return
		}
	}
	c.JSON(http.StatusOK, pollAnswer(traceID, e))
}

// storedDecision reads a trace the waiters do not remember from the database: a pending trace is
// registered with the waiters, so its decision wakes the poll; a decided one answers with its
// attempt-0 decision. known is false for an unknown trace, and without a database.
func (s *server) storedDecision(ctx context.Context, traceID string) (e waitEntry, known bool, err error) {
	rd, ok := s.status.db.(traceDecisionReader)
	if !ok || s.status.db == nil {
		return e, false, nil
	}
	rec, err := rd.GetTrace(ctx, traceID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return e, false, nil
	case err != nil:
		return e, false, err
	}
	label := ""
	if rec.KeyLabel != nil {
		label = *rec.KeyLabel
	}
	if rec.GateStatus == store.GateStatusPending {
		s.waiters.started(traceID, label)
		e, known = s.waiters.get(traceID)
		return e, known, nil
	}
	docs, err := rd.TraceDecisions(ctx, traceID)
	if err != nil {
		return e, false, err
	}
	if len(docs) == 0 {
		return e, false, nil
	}
	s.waiters.decided(traceID, label, s.routeHeaders(rec.Route), docs[0])
	e, known = s.waiters.get(traceID)
	return e, known, nil
}

// routeHeaders is the headers setting of a route in the policy in effect (full when the route is
// gone).
func (s *server) routeHeaders(route string) string {
	if s.policies == nil || s.policies.Engine == nil {
		return policy.HeadersFull
	}
	if p := s.policies.Engine.Current(); p != nil {
		if r, ok := p.Route(route); ok && r.Headers != "" {
			return r.Headers
		}
	}
	return policy.HeadersFull
}

// pollAnswer is the long-poll's answer for an entry: pending with a null decision, or decided with
// the decision/v1 document (only action and status on an action_only route).
func pollAnswer(traceID string, e waitEntry) decisionPoll {
	if e.doc == nil {
		return decisionPoll{TraceID: traceID, State: decisionPending, Decision: json.RawMessage("null")}
	}
	doc := e.doc
	if e.headers == policy.HeadersActionOnly {
		var d struct {
			Action string `json:"action"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(e.doc, &d); err == nil {
			doc, _ = json.Marshal(d)
		}
	}
	return decisionPoll{TraceID: traceID, State: decisionDecided, Decision: doc}
}

// parseWaitMs reads wait_ms: absent is 0, a whole number of milliseconds is clamped to
// maxDecisionWait, anything else is refused.
func parseWaitMs(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return min(time.Duration(min(n, maxDecisionWait.Milliseconds()))*time.Millisecond, maxDecisionWait), true
}
