package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/metrics"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/websocket"
	"github.com/truthtable/backend-go/internal/worker"
)

// Audit request limits (SPEC §2.2 AuditRequest.context: ≤20 docs × ≤20,000 chars).
const (
	maxAuditDocs     = 20
	maxAuditDocRunes = 20000
)

// traceStore is the part of the store the sink writes (and reads back for a late decision's event).
type traceStore interface {
	StartTrace(ctx context.Context, t *store.TraceRecord) error
	RecordDecision(ctx context.Context, t *store.TraceRecord, d *store.DecisionRecord, b *store.BodyRecord) error
	RecordAttempt(ctx context.Context, d *store.DecisionRecord) error
	RecordLateDecision(ctx context.Context, d *store.DecisionRecord) error
	SetTraceAudit(ctx context.Context, traceID string, u store.TraceAuditUpdate) error
	GetTrace(ctx context.Context, traceID string) (*store.TraceRecord, error)
}

// auditQueue is the part of the worker pool the sink submits to.
type auditQueue interface {
	Submit(job *worker.Job) (string, error)
	HasEngine() bool
}

// traceSink implements gate.Sink: it persists each trace and decision (inline, TRACE_FLUSH=sync
// semantics), broadcasts trace_started and gate_decision over WS v2, and submits the deep audit
// the gate selected. Every piece is optional: without a database it only broadcasts, without an
// engine every selected audit reports disabled.
type traceSink struct {
	store         traceStore
	pub           worker.Publisher
	audits        auditQueue
	bodyRetention time.Duration
	bodyMaxBytes  int
	now           func() time.Time
	log           *slog.Logger
	// waiters wakes the /v1 decision long-polls; nil means none wait.
	waiters *decisionWaiters
	// late orders each late decision after the record of its attempt.
	late lateOrder
}

var _ gate.Sink = (*traceSink)(nil)

// TraceStarted records and broadcasts a pending trace.
func (s *traceSink) TraceStarted(ctx context.Context, t *gate.TraceStart) {
	rec := startRecord(t)
	if s.store != nil {
		if err := s.store.StartTrace(ctx, rec); err != nil {
			metrics.TraceWriteDropped.Inc()
			s.log.Error("trace: recording the start failed", "trace_id", t.TraceID, "err", err)
		}
	}
	sum := rec.Summary()
	type payload struct {
		Trace store.TraceSummary `json:"trace"`
	}
	s.publish("trace_started", t.TraceID, payload{sum}, payload{sum.Redact()})
	s.waiters.started(t.TraceID, t.KeyLabel)
}

// GateDecided records and broadcasts a decided trace, then submits its deep audit. It returns
// the X-TrustAgent-Audit value.
func (s *traceSink) GateDecided(ctx context.Context, r *gate.Result) string {
	rec := decidedRecord(r, s.now())
	var job *worker.Job
	switch {
	case r.Audit.Selection == "":
		rec.AuditStatus = r.Audit.Status
	case s.audits == nil || !s.audits.HasEngine():
		rec.AuditStatus = gate.AuditDisabled
	default:
		auditID := store.NewID()
		lane, selection := r.Audit.Lane, r.Audit.Selection
		rec.AuditID, rec.AuditLane, rec.AuditSelection, rec.AuditStatus = &auditID, &lane, &selection, gate.AuditQueued
		job = auditJob(r, auditID)
	}

	decisionJSON, err := json.Marshal(r.Decision)
	if err != nil {
		s.log.Error("trace: the decision does not marshal", "trace_id", r.Trace.TraceID, "err", err)
		decisionJSON = nil
	}
	if s.store != nil {
		var body *store.BodyRecord
		if r.Body.Store {
			body = bodyRecord(r, s.now().Add(s.bodyRetention), s.bodyMaxBytes)
		}
		if err := s.store.RecordDecision(ctx, rec, decisionRecord(r, decisionJSON), body); err != nil {
			metrics.TraceWriteDropped.Inc()
			s.log.Error("trace: recording the decision failed", "trace_id", r.Trace.TraceID, "err", err)
		}
	}
	sum := rec.Summary()
	type payload struct {
		Trace    store.TraceSummary `json:"trace"`
		Decision json.RawMessage    `json:"decision"`
	}
	s.publish("gate_decision", r.Trace.TraceID, payload{sum, decisionJSON},
		payload{sum.Redact(), store.RedactDecision(decisionJSON)})
	s.waiters.decided(r.Trace.TraceID, r.Trace.KeyLabel, r.Headers, decisionJSON)
	if r.LateExpected {
		// A late decision's event reads the trace back from the database; without one it reuses
		// the summary published here.
		var published *store.TraceSummary
		if s.store == nil {
			published = &sum
		}
		s.attemptRecorded(r, published)
	}

	if job == nil {
		return rec.AuditStatus
	}
	job.KeyLabel = r.Trace.KeyLabel
	lane, err := s.audits.Submit(job)
	if err != nil {
		s.log.Warn("deep audit: not queued", "trace_id", r.Trace.TraceID, "err", err)
		if s.store != nil {
			if err := dropTraceAudit(ctx, s.store, r.Trace.TraceID, err); err != nil {
				metrics.TraceWriteDropped.Inc()
				s.log.Error("trace: recording a dropped audit failed", "trace_id", r.Trace.TraceID, "err", err)
			}
		}
		if ev, err := worker.DroppedEvent(r.Trace.TraceID, rec.AuditID, worker.DropReason(err)); err == nil && s.pub != nil {
			s.pub.Publish(ev)
		}
		return gate.AuditDropped
	}
	if lane != *rec.AuditLane && s.store != nil {
		// The high lane was at its rate (or full): the audit waits in the low lane.
		if err := s.store.SetTraceAudit(ctx, r.Trace.TraceID, store.TraceAuditUpdate{Lane: &lane}); err != nil {
			metrics.TraceWriteDropped.Inc()
			s.log.Error("trace: recording the audit lane failed", "trace_id", r.Trace.TraceID, "err", err)
		}
	}
	return gate.AuditQueued
}

// flagLLMBudget is the trace flag of an audit the day's LLM cap refused (D48).
const flagLLMBudget = "llm_budget"

// auditDropper records a dropped audit with a trace flag (store.PostgresStore.DropTraceAudit).
type auditDropper interface {
	DropTraceAudit(ctx context.Context, traceID, flag string) error
}

// dropTraceAudit records that a trace's audit was not queued (submitErr says why): its audit
// status becomes dropped and, when the LLM cap refused it, the trace gains the flag llm_budget.
// A store that cannot flag a trace records the status only.
func dropTraceAudit(ctx context.Context, st interface {
	SetTraceAudit(ctx context.Context, traceID string, u store.TraceAuditUpdate) error
}, traceID string, submitErr error) error {
	if errors.Is(submitErr, worker.ErrLLMBudget) {
		if d, ok := st.(auditDropper); ok {
			return d.DropTraceAudit(ctx, traceID, flagLLMBudget)
		}
	}
	return st.SetTraceAudit(ctx, traceID, store.TraceAuditUpdate{Status: gate.AuditDropped})
}

// AttemptDecided records a later attempt of a trace (the fallback re-gate): its gate_decisions
// row only, with no audit, no event and no change to the trace, whose GateDecided carries the
// delivered outcome (Result.Final).
func (s *traceSink) AttemptDecided(ctx context.Context, r *gate.Result) {
	if s.store == nil || r == nil || r.Decision == nil {
		return
	}
	doc, err := json.Marshal(r.Decision)
	if err != nil {
		s.log.Error("trace: the attempt's decision does not marshal", "trace_id", r.Decision.TraceID, "attempt", r.Decision.Attempt, "err", err)
		return
	}
	if err := s.store.RecordAttempt(ctx, decisionRecord(r, doc)); err != nil {
		metrics.TraceWriteDropped.Inc()
		s.log.Error("trace: recording a later attempt failed", "trace_id", r.Decision.TraceID, "attempt", r.Decision.Attempt, "err", err)
	}
	if r.LateExpected {
		s.attemptRecorded(r, nil)
	}
}

// LateDecided records a decision recomputed after a late decision-model answer (SPEC §2.9 late
// decisions): the late gate_decisions row of its attempt and the trace flag late_decision, then,
// for attempt 0, the WS gate_decision event with late=true. The trace keeps what was enforced; the
// decision is never audited. A late decision that arrives before its attempt was recorded waits
// for that record (lateOrder).
func (s *traceSink) LateDecided(ctx context.Context, r *gate.Result) {
	if r == nil || r.Decision == nil {
		return
	}
	sum, recorded := s.late.arrive(lateKeyOf(r), r, s.now())
	if !recorded {
		return
	}
	s.recordLate(ctx, r, sum)
}

// attemptRecorded notes that an attempt a late decision may follow was recorded (sum is the trace
// as GateDecided published it, nil for a later attempt), and records the late decision that
// arrived before it, off the request's path.
func (s *traceSink) attemptRecorded(r *gate.Result, sum *store.TraceSummary) {
	if late := s.late.recorded(lateKeyOf(r), sum, s.now()); late != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), lateRecordTimeout)
			defer cancel()
			s.recordLate(ctx, late, sum)
		}()
	}
}

// recordLate writes a late decision and, for attempt 0, broadcasts it with the trace as stored
// (its audit may have moved on since GateDecided), or as GateDecided published it without a
// database.
func (s *traceSink) recordLate(ctx context.Context, r *gate.Result, sum *store.TraceSummary) {
	d := r.Decision
	doc, err := json.Marshal(d)
	if err != nil {
		s.log.Error("trace: the late decision does not marshal", "trace_id", d.TraceID, "attempt", d.Attempt, "err", err)
		return
	}
	if s.store != nil {
		if err := s.store.RecordLateDecision(ctx, decisionRecord(r, doc)); err != nil {
			metrics.TraceWriteDropped.Inc()
			s.log.Error("trace: recording a late decision failed", "trace_id", d.TraceID, "attempt", d.Attempt, "err", err)
		}
	}
	if d.Attempt != 0 {
		return
	}
	s.waiters.decided(d.TraceID, r.Trace.KeyLabel, r.Headers, doc)
	var view store.TraceSummary
	switch rec, err := s.storedTrace(ctx, d.TraceID); {
	case err == nil:
		view = rec.Summary()
	case sum != nil:
		view = *sum
		view.Flags = appendNew(slices.Clone(view.Flags), gate.FlagLateDecision)
		view.UpdatedAt = s.now().UTC()
	default:
		return
	}
	type payload struct {
		Trace    store.TraceSummary `json:"trace"`
		Decision json.RawMessage    `json:"decision"`
	}
	s.publish("gate_decision", d.TraceID, payload{view, doc}, payload{view.Redact(), store.RedactDecision(doc)})
}

// storedTrace reads a trace back; store.ErrNotFound without a database.
func (s *traceSink) storedTrace(ctx context.Context, traceID string) (*store.TraceRecord, error) {
	if s.store == nil {
		return nil, store.ErrNotFound
	}
	return s.store.GetTrace(ctx, traceID)
}

// lateRecordTimeout bounds the writes of a late decision recorded after its attempt's record.
const lateRecordTimeout = 10 * time.Second

// Late decisions wait at most lateHold for the record of their attempt, and an attempt's record
// waits as long for its late decision; lateSlots bounds how many wait at once.
const (
	lateHold  = 2 * time.Minute
	lateSlots = 16384
)

// lateKey is one attempt of one trace.
type lateKey struct {
	trace   string
	attempt int
}

func lateKeyOf(r *gate.Result) lateKey {
	return lateKey{r.Decision.TraceID, r.Decision.Attempt}
}

// lateSlot is one attempt waiting: recorded, with the trace summary GateDecided published (nil for
// a later attempt), until its late decision comes; or a late decision parked until its attempt is
// recorded.
type lateSlot struct {
	at       time.Time
	recorded bool
	sum      *store.TraceSummary
	late     *gate.Result
}

// lateOrder puts each late decision after the record of its attempt. A late answer can arrive
// while the handler still delivers the answer (a fallback call, say), before GateDecided or
// AttemptDecided; recorded in that order, the regular write would bury the late one. Slots
// expire after lateHold, oldest first.
type lateOrder struct {
	mu    sync.Mutex
	slots map[lateKey]lateSlot
	queue []lateKey // insertion order, for expiry
}

// recorded notes that k was recorded and returns the late decision parked for it, if any.
func (o *lateOrder) recorded(k lateKey, sum *store.TraceSummary, now time.Time) *gate.Result {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.expire(now)
	if sl, ok := o.slots[k]; ok && !sl.recorded {
		delete(o.slots, k)
		return sl.late
	}
	o.put(k, lateSlot{at: now, recorded: true, sum: sum})
	return nil
}

// arrive takes the record of a late decision's attempt; recorded is false when the attempt was not
// recorded yet, and the late decision then waits for it.
func (o *lateOrder) arrive(k lateKey, late *gate.Result, now time.Time) (sum *store.TraceSummary, recorded bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.expire(now)
	if sl, ok := o.slots[k]; ok && sl.recorded {
		delete(o.slots, k)
		return sl.sum, true
	}
	o.put(k, lateSlot{at: now, late: late})
	return nil, false
}

// put adds a slot; past lateSlots the oldest slot goes. The caller holds mu.
func (o *lateOrder) put(k lateKey, sl lateSlot) {
	if o.slots == nil {
		o.slots = map[lateKey]lateSlot{}
	}
	o.slots[k] = sl
	o.queue = append(o.queue, k)
	for len(o.queue) > lateSlots {
		delete(o.slots, o.queue[0])
		o.queue = o.queue[1:]
	}
}

// expire drops the slots older than lateHold; the caller holds mu.
func (o *lateOrder) expire(now time.Time) {
	cut := now.Add(-lateHold)
	for len(o.queue) > 0 {
		k := o.queue[0]
		if sl, ok := o.slots[k]; ok {
			if sl.at.After(cut) {
				break
			}
			delete(o.slots, k)
		}
		o.queue = o.queue[1:]
	}
	if len(o.queue) == 0 {
		o.queue = nil
	}
}

func (s *traceSink) publish(typ, traceID string, data, redacted any) {
	if s.pub == nil {
		return
	}
	ev, err := websocket.NewEvent(typ, traceID, data, redacted)
	if err != nil {
		s.log.Error("ws: event does not marshal", "type", typ, "trace_id", traceID, "err", err)
		return
	}
	s.pub.Publish(ev)
}

// startRecord is the traces row of a trace that has not been gated yet.
func startRecord(t *gate.TraceStart) *store.TraceRecord {
	pv := t.PolicyVersion
	return &store.TraceRecord{
		TraceID: t.TraceID, ClientRequestID: t.ClientRequestID, CreatedAt: t.CreatedAt, UpdatedAt: t.CreatedAt,
		Source: t.Source, APIFormat: t.APIFormat, Path: t.Path, Route: t.Route, PolicyVersion: &pv, Mode: t.Mode,
		KeyLabel: nonEmpty(t.KeyLabel), Model: nonEmpty(t.Model), Stream: t.Stream, Scenario: nonEmpty(t.Scenario),
		AnswerSource: nonEmpty(t.AnswerSource), GateStatus: store.GateStatusPending, AuditStatus: store.TraceAuditNone,
		QuestionPreview: store.Preview(t.UserRequest, store.QuestionPreviewRunes), Reasons: []string{},
		Flags: append([]string{}, t.Flags...),
	}
}

// decidedRecord is the traces row of a gated trace. Its action is what the client got
// (Result.Final): fallback when the fallback answer went out, block when it failed and blocked.
// The delivery's reasons (fallback_failed, fallback_format_mismatch) and flags (fallback_used)
// join the decision's on the trace only, never in decision/v1.
func decidedRecord(r *gate.Result, now time.Time) *store.TraceRecord {
	rec := startRecord(r.Trace)
	d := r.Decision
	action := finalAction(r)
	rec.UpdatedAt = now.UTC()
	rec.EffectiveMode, rec.Action, rec.GateStatus = &d.EffectiveMode, &action, d.Status
	rec.WouldAction = nonEmpty(d.WouldAction)
	rec.SkipReason, rec.Verdict, rec.MaxRisk = d.SkipReason, d.Verdict, d.MaxRisk
	rec.Reasons = append([]string{}, d.Reasons...)
	if d.EvidenceSummary != nil {
		rec.EvidenceSummary, _ = json.Marshal(d.EvidenceSummary)
	}
	if dm := d.DecisionModel; dm != nil {
		rec.DecisionBackend, rec.DecisionSnapshot, rec.DecisionCostUSD = &dm.Backend, dm.Snapshot, dm.CostUSD
	}
	if r.Upstream.Status != 0 {
		st := r.Upstream.Status
		rec.UpstreamStatus = &st
	}
	rec.UpstreamMs, rec.GateMs, rec.TotalMs, rec.OverheadMs = r.Upstream.Ms, d.GateMs, r.TotalMs, r.OverheadMs
	rec.TTFTMs = r.Upstream.TTFTMs
	rec.PromptTokens, rec.CompletionTokens = r.Upstream.PromptTokens, r.Upstream.CompletionTokens
	rec.ResponsePreview = store.Preview(r.ResponseText, store.ResponsePreviewRunes)
	if r.ResponseText != "" {
		// The rune count of the gated text (0005 traces.response_chars): fitted_from_audits
		// applies its cost-per-length line to it (roi_api.go).
		n := utf8.RuneCountInString(r.ResponseText)
		rec.ResponseChars = &n
	}
	rec.Flags = append([]string{}, r.Flags...)
	if f := r.Final; f != nil {
		rec.Reasons = appendNew(rec.Reasons, f.Reasons...)
		rec.Flags = appendNew(rec.Flags, f.Flags...)
	}
	return rec
}

// finalAction is the action the client got: Result.Final's, else the decision's.
func finalAction(r *gate.Result) string {
	if r.Final != nil && r.Final.Action != "" {
		return r.Final.Action
	}
	return r.Decision.Action
}

// delivered is what the client got (original, fallback or blocked); without Result.Final, the
// original answer.
func delivered(r *gate.Result) string {
	if r.Final != nil && r.Final.Delivered != "" {
		return r.Final.Delivered
	}
	return gate.DeliveredOriginal
}

// finalText is the answer text the client got (trace_bodies.final_response_text): the original
// unless Result.Final delivered another text; "" when a fallback or block delivered no answer
// text (an error-style block sends an error body).
func finalText(r *gate.Result) string {
	switch f := r.Final; {
	case f == nil:
		return r.ResponseText
	case f.Text != "":
		return f.Text
	case f.Delivered == "" || f.Delivered == gate.DeliveredOriginal:
		return r.ResponseText
	}
	return ""
}

// appendNew appends the values of add that list does not hold yet.
func appendNew(list []string, add ...string) []string {
	for _, v := range add {
		if v != "" && !slices.Contains(list, v) {
			list = append(list, v)
		}
	}
	return list
}

// decisionRecord is the gate_decisions row of a result.
func decisionRecord(r *gate.Result, doc json.RawMessage) *store.DecisionRecord {
	d := r.Decision
	rec := &store.DecisionRecord{
		TraceID: d.TraceID, Attempt: d.Attempt, PolicyVersion: d.PolicyVersion, Route: d.Route, Mode: d.Mode,
		EffectiveMode: d.EffectiveMode, Status: d.Status, Action: d.Action, WouldAction: nonEmpty(d.WouldAction),
		Verdict: d.Verdict, MaxRisk: d.MaxRisk, Calibration: d.Calibration, Degraded: d.Degraded,
		Reasons: append([]string{}, d.Reasons...), Decision: doc, Late: d.Late, GateMs: d.GateMs,
	}
	rec.Detectors, _ = json.Marshal(d.Detectors)
	rec.Excluded, _ = json.Marshal(d.Excluded)
	if d.EvidenceSummary != nil {
		rec.EvidenceSummary, _ = json.Marshal(d.EvidenceSummary)
	}
	if c := d.Compile; c != nil {
		cpt := c.CPT
		rec.QuestionSetSHA256, rec.RequestSHA256, rec.CPT = nonEmpty(c.QuestionSetSHA256), c.RequestSHA256, &cpt
		rec.PackedEvidenceIDs, rec.TruncatedIDs = c.PackedEvidenceIDs, c.TruncatedIDs
	}
	if dm := d.DecisionModel; dm != nil {
		calls, attempts, hedged := dm.Calls, dm.Attempts, dm.Hedged
		rec.DecisionBackend, rec.DecisionModelRequested, rec.DecisionSnapshot = &dm.Backend, dm.ModelRequested, dm.Snapshot
		rec.DecisionMs, rec.DecisionCalls, rec.DecisionAttempts, rec.DecisionHedged = dm.LatencyMs, &calls, &attempts, &hedged
		rec.ConnectMs, rec.InputTokens, rec.CostUSD = dm.ConnectMs, dm.InputTokens, dm.CostUSD
	}
	switch {
	case r.Status == decision.StatusTimeout:
		kind := decision.StatusTimeout
		rec.ErrorKind = &kind
	case strings.HasPrefix(r.Status, "error:"):
		kind := strings.TrimPrefix(r.Status, "error:")
		rec.ErrorKind = &kind
	}
	return rec
}

// bodyRecord is the trace_bodies row of a result, capped at maxBytes (TRUTHTABLE_BODY_MAX_BYTES):
// when the texts do not fit, every text is cut to an equal share and the body is marked
// truncated.
func bodyRecord(r *gate.Result, expires time.Time, maxBytes int) *store.BodyRecord {
	msgs := append([]gate.ChatMessage{}, r.Messages...)
	items := append([]evidence.Item{}, r.Evidence...)
	texts := []string{r.UserRequest, r.SystemInstructions, r.ResponseText, finalText(r)}
	size := func() int {
		n := 0
		for _, t := range texts {
			n += len(t)
		}
		for _, m := range msgs {
			n += len(m.Content)
		}
		for _, it := range items {
			n += len(it.Text)
		}
		return n
	}
	truncated := false
	if maxBytes > 0 && size() > maxBytes {
		truncated = true
		share := max(maxBytes/(4+len(msgs)+len(items)), 1)
		for i := range texts {
			texts[i] = cut(texts[i], share)
		}
		for i := range msgs {
			msgs[i].Content = cut(msgs[i].Content, share)
		}
		for i := range items {
			if c := cut(items[i].Text, share); c != items[i].Text {
				items[i].Text, items[i].Truncated = c, true
			}
		}
	}
	messages, _ := json.Marshal(msgs)
	ev, _ := json.Marshal(items)
	rules := make(map[string]string, len(r.Rules))
	for k, v := range r.Rules {
		rules[k] = v
	}
	return &store.BodyRecord{
		TraceID: r.Trace.TraceID, Messages: messages, UserRequest: nonEmpty(texts[0]), SystemInstructions: nonEmpty(texts[1]),
		ResponseText: nonEmpty(texts[2]), FinalResponseText: nonEmpty(texts[3]), Evidence: ev, Rules: rules,
		Bytes: size(), Truncated: truncated, InclusionProb: r.Body.InclusionProb, ExpiresAt: expires.UTC(),
	}
}

// auditJob is the deep audit of a result (SPEC §2.2 AuditRequest; M5).
func auditJob(r *gate.Result, auditID string) *worker.Job {
	t, d := r.Trace, r.Decision
	docs, items := auditContext(r.Evidence)
	req := &pb.AuditRequest{
		RequestId: t.TraceID, TraceId: t.TraceID, AuditId: auditID,
		Query: lastUserTurn(r), Response: r.ResponseText, Context: docs, Model: t.Model, Provider: t.APIFormat,
		TimestampMs: t.CreatedAt.UnixMilli(), Gate: gateDecision(r), Route: t.Route, Source: t.Source, Priority: r.Audit.Lane,
		Options: &pb.AuditOptions{Verifier: r.Audit.Verifier, UniformSample: r.Audit.UniformSample, MockScenario: r.Audit.MockScenario},
	}
	job := &worker.Job{
		Request: req, Lane: r.Audit.Lane, Selection: r.Audit.Selection, InclusionProb: r.Audit.InclusionProb,
		ClientRequestID: t.ClientRequestID,
		Gate: &store.AuditGate{Action: d.Action, WouldAction: nonEmpty(d.WouldAction), Verdict: d.Verdict, MaxRisk: d.MaxRisk,
			Delivered: delivered(r)},
	}
	job.ContextUsed, _ = json.Marshal(items)
	if s := d.EvidenceSummary; s != nil {
		req.EvidenceSummary = evidenceSummary(s)
		job.EvidenceSummary, _ = json.Marshal(s)
	}
	return job
}

// lastUserTurn is the audit query: the last user turn without its evidence blocks, or the whole
// last user message when the request's evidence extraction did not run.
func lastUserTurn(r *gate.Result) string {
	if r.UserRequest != "" {
		return r.UserRequest
	}
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "user" {
			return r.Messages[i].Content
		}
	}
	return ""
}

// auditContext is the request evidence the audit checks against: the trusted items, full
// (pre-packing), at most maxAuditDocs of at most maxAuditDocRunes each.
func auditContext(items []evidence.Item) ([]*pb.ContextDocument, []evidence.Item) {
	docs := []*pb.ContextDocument{}
	used := []evidence.Item{}
	for _, it := range items {
		if it.Untrusted || len(docs) == maxAuditDocs {
			continue
		}
		text, truncated := it.Text, false
		if utf8.RuneCountInString(text) > maxAuditDocRunes {
			text, truncated = string([]rune(text)[:maxAuditDocRunes]), true
		}
		docs = append(docs, &pb.ContextDocument{Id: it.ID, Content: text, Source: it.Source, Label: it.Label, Truncated: truncated})
		it.Text, it.Truncated = text, it.Truncated || truncated
		used = append(used, it)
	}
	return docs, used
}

// gateDecision is the decision the audit ran behind, with the canonical decision/v1 document.
func gateDecision(r *gate.Result) *pb.GateDecision {
	d := r.Decision
	g := &pb.GateDecision{
		Action: d.Action, Mode: d.Mode, Route: d.Route, WouldAction: d.WouldAction, Status: d.Status,
		Reasons: d.Reasons, Calibration: d.Calibration, Delivered: delivered(r),
	}
	if d.PolicyVersion != nil {
		g.PolicyVersion = *d.PolicyVersion
	}
	if d.Verdict != nil {
		g.Verdict = *d.Verdict
	}
	if d.MaxRisk != nil {
		v := float32(*d.MaxRisk)
		g.MaxRisk = &v
	}
	if dm := d.DecisionModel; dm != nil {
		g.Backend = dm.Backend
		if dm.Snapshot != nil {
			g.DecisionModelSnapshot = *dm.Snapshot
		}
		if dm.LatencyMs != nil {
			g.LatencyMs = *dm.LatencyMs
		}
		if dm.CostUSD != nil {
			g.CostUsd = *dm.CostUSD
		}
	}
	if doc, err := decision.Canonicalize(d); err == nil {
		g.DecisionJson = doc
	}
	return g
}

func evidenceSummary(s *evidence.Summary) *pb.EvidenceSummary {
	out := &pb.EvidenceSummary{Configured: s.Configured, Empty: s.Empty, Knowledge: s.Knowledge, Truncated: s.Truncated,
		Tokens: clampInt32(s.Tokens)}
	for _, u := range s.Used {
		out.Used = append(out.Used, &pb.EvidenceSourceCount{Source: u.Source, Items: clampInt32(u.Items),
			Labels: u.Labels, Tokens: clampInt32(u.Tokens)})
	}
	return out
}

// clampInt32 bounds a count before its int32 conversion.
func clampInt32(v int) int32 {
	if v < 0 {
		return 0
	}
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v)
}

// cut returns the longest prefix of s of at most n bytes that ends on a rune boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
