package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"strings"
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

// traceStore is the part of the store the sink writes.
type traceStore interface {
	StartTrace(ctx context.Context, t *store.TraceRecord) error
	RecordDecision(ctx context.Context, t *store.TraceRecord, d *store.DecisionRecord, b *store.BodyRecord) error
	SetTraceAudit(ctx context.Context, traceID string, u store.TraceAuditUpdate) error
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

	if job == nil {
		return rec.AuditStatus
	}
	lane, err := s.audits.Submit(job)
	if err != nil {
		s.log.Warn("deep audit: not queued", "trace_id", r.Trace.TraceID, "err", err)
		if s.store != nil {
			if err := s.store.SetTraceAudit(ctx, r.Trace.TraceID, store.TraceAuditUpdate{Status: gate.AuditDropped}); err != nil {
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

// AttemptDecided records a later attempt of a trace (the fallback re-gate): its gate_decisions
// row only, with no audit and no change to the trace. Not recorded yet (GD-5).
func (s *traceSink) AttemptDecided(ctx context.Context, r *gate.Result) {}

// LateDecided records a decision recomputed after a late decision-model answer: the late row,
// the trace flag late_decision and a WS event, never enforced or audited. Not recorded yet (GD-6).
func (s *traceSink) LateDecided(ctx context.Context, r *gate.Result) {}

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

// decidedRecord is the traces row of a gated trace.
func decidedRecord(r *gate.Result, now time.Time) *store.TraceRecord {
	rec := startRecord(r.Trace)
	d := r.Decision
	rec.UpdatedAt = now.UTC()
	rec.EffectiveMode, rec.Action, rec.GateStatus = &d.EffectiveMode, &d.Action, d.Status
	rec.WouldAction = nonEmpty(d.WouldAction)
	rec.SkipReason, rec.Verdict, rec.MaxRisk, rec.Reasons = d.SkipReason, d.Verdict, d.MaxRisk, append([]string{}, d.Reasons...)
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
	rec.PromptTokens, rec.CompletionTokens = r.Upstream.PromptTokens, r.Upstream.CompletionTokens
	rec.ResponsePreview = store.Preview(r.ResponseText, store.ResponsePreviewRunes)
	rec.Flags = append([]string{}, r.Flags...)
	return rec
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
	texts := []string{r.UserRequest, r.SystemInstructions, r.ResponseText}
	size := func() int {
		n := 2 * len(texts[2]) // response_text and final_response_text
		n += len(texts[0]) + len(texts[1])
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
		ResponseText: nonEmpty(texts[2]), FinalResponseText: nonEmpty(texts[2]), Evidence: ev, Rules: rules,
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
			Delivered: "original"},
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
		Reasons: d.Reasons, Calibration: d.Calibration, Delivered: "original",
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
