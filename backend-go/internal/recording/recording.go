// Package recording builds trustagent.recording/v1 recordings of gateway traffic (SPEC §2.13):
// the manifest a static demo replays, derived from trace details, with a label that can only say
// what the traces support, and the sanitizer every published string goes through. DBSource reads
// the trace details from the store. cmd/export-replay writes recordings with it; GET
// /api/export/replay serves the same manifest.
package recording

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/truthtable/backend-go/internal/store"
)

// Recording constants (contracts/schema/recording.v1.schema.json, SPEC §2.13).
const (
	Schema       = "trustagent.recording/v1"
	KindRecorded = "recorded"
	KindMock     = "mock"
	// MockLabel is the only label a mock recording may carry (contracts/ui/copy.v1.json
	// recording.mock).
	MockLabel = "Simulated (mock checks + mock judge): not model output"
)

// Trace is one exported trace: its detail as GET /api/traces/:id serves it, and the deep
// audit's LLM cost when the source knows it.
type Trace struct {
	Detail store.TraceDetail
	LLMUSD *float64
}

// Judge is the part of AuditDetail.judge (api.ts JudgeInfo) a recording names.
type Judge struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	FallbackActive bool   `json:"fallback_active"`
}

// Recording is manifest.json (api.ts Recording).
type Recording struct {
	Schema            string               `json:"schema"`
	Kind              string               `json:"kind"`
	Label             string               `json:"label"`
	RecordedAt        time.Time            `json:"recorded_at"`
	GitSHA            string               `json:"git_sha"`
	TrustAgentVersion string               `json:"trustagent_version"`
	DecisionModel     Model                `json:"decision_model"`
	Judge             Judge                `json:"judge"`
	CalibrationState  string               `json:"calibration_state"`
	AnswerSources     AnswerSources        `json:"answer_sources"`
	Totals            Totals               `json:"totals"`
	ROI               ROI                  `json:"roi"`
	Traces            []store.TraceSummary `json:"traces"`
	Events            []Event              `json:"events"`
}

// Model is Recording.decision_model: the one decision backend and snapshot of the traces.
type Model struct {
	Backend  string  `json:"backend"`
	Snapshot *string `json:"snapshot"`
}

// AnswerSources counts the traces with scripted and generated answers.
type AnswerSources struct {
	Scripted  int `json:"scripted"`
	Generated int `json:"generated"`
}

// Totals are the recording's trace count and spend.
type Totals struct {
	Traces      int     `json:"traces"`
	DecisionUSD float64 `json:"decision_usd"`
	LLMUSD      float64 `json:"llm_usd"`
}

// ROI carries the RoiSummary members a recording can state from its own traces; the full summary
// (costs, counterfactuals, savings) is computed by internal/costs.
type ROI struct {
	Window         string `json:"window"`
	GatedResponses int    `json:"gated_responses"`
	Simulated      bool   `json:"simulated"`
}

// Event is one replayed WebSocket event, t_ms after the first trace started.
type Event struct {
	TMs     int64  `json:"t_ms"`
	Type    string `json:"type"`
	TraceID string `json:"trace_id,omitempty"`
	Data    any    `json:"data"`
}

// Options are what the traces cannot say.
type Options struct {
	// GitSHA and Version identify the build that exported the recording.
	GitSHA, Version string
	// Judge names the judge when no trace has a deep audit (export-replay --judge-provider and
	// --judge-model); when audits name one, it must agree.
	Judge *Judge
	// DefaultJudge names the judge when neither the audits nor Judge do (GET /api/export/replay:
	// the judge the audit engine reports); unlike Judge it is never checked against the audits.
	DefaultJudge *Judge
}

// ErrNotRecordable is returned when the traces cannot be labelled honestly.
var ErrNotRecordable = errors.New("these traces cannot be exported as a recording")

// ErrNoJudge (an ErrNotRecordable) is returned when no deep audit, option or default names the
// judge.
var ErrNoJudge = fmt.Errorf("%w: no deep audit names its judge", ErrNotRecordable)

// Build assembles the manifest from the traces, oldest first (the slice itself is not reordered). It fails rather
// than mislabel: kind recorded needs a real decision backend, a judge that is neither a fallback
// nor the mock, and an answer_source on every trace; kind mock needs the mock decision backend.
func Build(traces []Trace, opt Options) (*Recording, error) {
	if len(traces) == 0 {
		return nil, fmt.Errorf("%w: no traces match", ErrNotRecordable)
	}
	traces = slices.Clone(traces) // sorted here; the caller's order is left alone
	slices.SortStableFunc(traces, func(a, b Trace) int {
		if c := a.Detail.CreatedAt.Compare(b.Detail.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.Detail.TraceID, b.Detail.TraceID)
	})
	for _, t := range traces {
		// The id names traces/<id>.json: only the canonical lowercase form (uuid.Parse also takes
		// braces, a urn:uuid: prefix and bare hex) is a plain, portable file name.
		if u, err := uuid.Parse(t.Detail.TraceID); err != nil || u.String() != t.Detail.TraceID {
			return nil, fmt.Errorf("%w: trace %q does not have a canonical UUID id (legacy traces cannot be recorded)", ErrNotRecordable, t.Detail.TraceID)
		}
	}
	model, err := decisionModel(traces)
	if err != nil {
		return nil, err
	}
	judge, err := judgeOf(traces, opt.Judge, opt.DefaultJudge)
	if err != nil {
		return nil, err
	}
	r := &Recording{
		Schema: Schema, RecordedAt: traces[0].Detail.CreatedAt.UTC(), GitSHA: opt.GitSHA,
		TrustAgentVersion: opt.Version, DecisionModel: model, Judge: judge,
		CalibrationState: calibrationState(traces), Traces: make([]store.TraceSummary, len(traces)), Events: []Event{},
	}
	var missing []string
	live := 0
	for i, t := range traces {
		s := t.Detail.TraceSummary
		r.Traces[i] = s
		switch {
		case s.AnswerSource == nil:
			missing = append(missing, s.TraceID)
		case *s.AnswerSource == "scripted":
			r.AnswerSources.Scripted++
		case *s.AnswerSource == "generated":
			r.AnswerSources.Generated++
		default:
			live++
		}
		if s.DecisionModel != nil && s.DecisionModel.CostUSD != nil {
			r.Totals.DecisionUSD += *s.DecisionModel.CostUSD
		}
		if t.LLMUSD != nil {
			r.Totals.LLMUSD += *t.LLMUSD
		}
		if s.GateStatus != store.GateStatusPending && s.GateStatus != "skipped" {
			r.ROI.GatedResponses++
		}
	}
	r.Totals.Traces = len(traces)

	switch model.Backend {
	case "mock":
		r.Kind, r.Label = KindMock, MockLabel
	case "off":
		return nil, fmt.Errorf("%w: no trace was checked by a decision model", ErrNotRecordable)
	default:
		var problems []string
		if judge.FallbackActive {
			problems = append(problems, "the judge ran on its fallback provider")
		}
		if strings.EqualFold(judge.Provider, "mock") {
			problems = append(problems, "the judge is the mock, whose output is simulated")
		}
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("%d traces have no answer_source (first %s)", len(missing), missing[0]))
		}
		if len(problems) > 0 {
			return nil, fmt.Errorf("%w as recorded: %s", ErrNotRecordable, strings.Join(problems, "; "))
		}
		r.Kind = KindRecorded
		r.Label = recordedLabel(r, live)
	}
	r.ROI.Simulated = r.Kind == KindMock
	last := traces[len(traces)-1].Detail.CreatedAt
	r.ROI.Window = window(last.Sub(traces[0].Detail.CreatedAt))
	r.Events = events(traces)
	return r, nil
}

// decisionModel is the single decision backend and snapshot of the traces that reached one.
func decisionModel(traces []Trace) (Model, error) {
	backends, snapshots := map[string]bool{}, map[string]bool{}
	for _, t := range traces {
		if dm := t.Detail.DecisionModel; dm != nil {
			backends[dm.Backend] = true
			if dm.Snapshot != nil {
				snapshots[*dm.Snapshot] = true
			}
		}
	}
	switch {
	case len(backends) == 0:
		return Model{Backend: "off"}, nil
	case len(backends) > 1:
		return Model{}, fmt.Errorf("%w: the traces mix decision backends %s; narrow the time window (since, until) or the route",
			ErrNotRecordable, strings.Join(keys(backends), ", "))
	case len(snapshots) > 1:
		return Model{}, fmt.Errorf("%w: the traces mix decision-model snapshots %s; narrow the time window (since, until)",
			ErrNotRecordable, strings.Join(keys(snapshots), ", "))
	}
	m := Model{Backend: keys(backends)[0]}
	if len(snapshots) == 1 {
		s := keys(snapshots)[0]
		m.Snapshot = &s
	}
	return m, nil
}

// judgeOf is the judge of the deep audits (effective provider and model); every audit must name
// the same one. Without audits the flags name it, else the default.
func judgeOf(traces []Trace, flags, def *Judge) (Judge, error) {
	seen := map[string]Judge{}
	fallback := false
	for _, t := range traces {
		a := t.Detail.AuditDetail
		if a == nil || len(a.Judge) == 0 || string(a.Judge) == "null" {
			continue
		}
		var j struct {
			ConfiguredProvider string `json:"configured_provider"`
			EffectiveProvider  string `json:"effective_provider"`
			Model              string `json:"model"`
			FallbackActive     bool   `json:"fallback_active"`
		}
		if err := json.Unmarshal(a.Judge, &j); err != nil {
			return Judge{}, fmt.Errorf("audit %s: judge: %w", a.AuditID, err)
		}
		provider := j.EffectiveProvider
		if provider == "" {
			provider = j.ConfiguredProvider
		}
		if provider == "" || j.Model == "" {
			continue
		}
		seen[provider+"\x00"+j.Model] = Judge{Provider: provider, Model: j.Model}
		fallback = fallback || j.FallbackActive
	}
	switch len(seen) {
	case 0:
		switch {
		case flags != nil:
			return *flags, nil
		case def != nil:
			return *def, nil
		}
		return Judge{}, fmt.Errorf("%w: pass --judge-provider and --judge-model", ErrNoJudge)
	case 1:
		for _, j := range seen {
			if flags != nil && (flags.Provider != j.Provider || flags.Model != j.Model) {
				return Judge{}, fmt.Errorf("%w: the audits were judged by %s %s, not %s %s", ErrNotRecordable, j.Provider, j.Model, flags.Provider, flags.Model)
			}
			j.FallbackActive = fallback
			return j, nil
		}
	}
	names := make([]string, 0, len(seen))
	for _, j := range seen {
		names = append(names, j.Provider+" "+j.Model)
	}
	sort.Strings(names)
	return Judge{}, fmt.Errorf("%w: the audits mix judges (%s); narrow the time window (since, until)", ErrNotRecordable, strings.Join(names, ", "))
}

// calibrationStates are the CalibrationState values from weakest to strongest.
var calibrationStates = []string{"none", "mock", "proxy", "family", "provisional", "measured"}

// calibrationState is the weakest calibration state of the active, assessed evaluators across the
// recorded decisions ("none" when there are none; an unknown state counts as none): a recording
// claims no more than its weakest check.
func calibrationState(traces []Trace) string {
	weakest, found := len(calibrationStates)-1, false
	for _, t := range traces {
		for _, raw := range t.Detail.Decisions {
			var d struct {
				Evaluators map[string]struct {
					Stage            string `json:"stage"`
					Internal         bool   `json:"internal"`
					Status           string `json:"status"`
					CalibrationState string `json:"calibration_state"`
				} `json:"evaluators"`
			}
			if json.Unmarshal(raw, &d) != nil {
				continue
			}
			for _, e := range d.Evaluators {
				if e.Stage != "active" || e.Internal || e.Status != "assessed" {
					continue
				}
				found = true
				weakest = min(weakest, max(slices.Index(calibrationStates, e.CalibrationState), 0))
			}
		}
	}
	if !found {
		return "none"
	}
	return calibrationStates[weakest]
}

// recordedLabel is "Recorded {date} · {snapshot} + {judge} · {answer sources} answers · replayed,
// not live" (contracts/ui/copy.v1.json recording.recorded).
func recordedLabel(r *Recording, live int) string {
	snapshot := r.DecisionModel.Backend
	if r.DecisionModel.Snapshot != nil {
		snapshot = *r.DecisionModel.Snapshot
	}
	judge := r.Judge.Model
	if !strings.Contains(judge, "/") {
		judge = r.Judge.Provider + " " + judge
	}
	var sources []string
	if r.AnswerSources.Scripted > 0 {
		sources = append(sources, "scripted")
	}
	if r.AnswerSources.Generated > 0 {
		sources = append(sources, "generated")
	}
	if live > 0 {
		sources = append(sources, "live")
	}
	return fmt.Sprintf("Recorded %s · %s + %s · %s answers · replayed, not live",
		r.RecordedAt.Format("2006-01-02"), snapshot, judge, strings.Join(sources, " and "))
}

// window is the smallest RoiSummary window that covers a span.
func window(span time.Duration) string {
	for _, w := range []struct {
		name string
		d    time.Duration
	}{{"15m", 15 * time.Minute}, {"1h", time.Hour}, {"24h", 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}} {
		if span <= w.d {
			return w.name
		}
	}
	return "30d"
}

// audit_failed kinds (WsEvent audit_failed).
var auditFailedKinds = []string{"engine_unavailable", "timeout", "judge_unavailable", "decomposer_failed", "internal"}

// events replays each trace as the WebSocket v2 stream showed it: trace_started when the request
// arrived, gate_decision when it was decided, audit_queued with the decision when an audit was
// planned, and audit_completed or audit_failed when the audit ended. t_ms counts from the first
// trace; events are ordered by time, then by trace order.
func events(traces []Trace) []Event {
	start := traces[0].Detail.CreatedAt
	ms := func(t time.Time) int64 { return max(t.Sub(start).Milliseconds(), 0) }
	var out []Event
	for _, t := range traces {
		d := t.Detail
		s := d.TraceSummary
		at := ms(s.CreatedAt)
		out = append(out, Event{TMs: at, Type: "trace_started", TraceID: s.TraceID, Data: map[string]any{"trace": pendingSummary(s)}})
		decided := at
		for _, p := range []*int64{s.Timings.TotalMs, s.Timings.GateMs} {
			if p != nil {
				decided = at + *p
				break
			}
		}
		if len(d.Decisions) > 0 {
			out = append(out, Event{TMs: decided, Type: "gate_decision", TraceID: s.TraceID,
				Data: map[string]any{"trace": decidedSummary(s), "decision": d.Decisions[0]}})
		}
		if s.Audit.AuditID != nil && s.Audit.Lane != nil {
			out = append(out, Event{TMs: decided, Type: "audit_queued", TraceID: s.TraceID,
				Data: map[string]any{"audit_id": *s.Audit.AuditID, "lane": *s.Audit.Lane, "queue_depth": 0}})
		}
		a := d.AuditDetail
		if a == nil {
			continue
		}
		done := max(ms(a.CreatedAt)+a.ProcessingMs, decided)
		switch a.Status {
		case "completed", "degraded":
			out = append(out, Event{TMs: done, Type: "audit_completed", TraceID: s.TraceID, Data: map[string]any{"audit": a}})
		case "failed":
			kind, message := "internal", ""
			if a.Error != nil {
				message = a.Error.Message
				if slices.Contains(auditFailedKinds, a.Error.Kind) {
					kind = a.Error.Kind
				}
			}
			out = append(out, Event{TMs: done, Type: "audit_failed", TraceID: s.TraceID,
				Data: map[string]any{"audit_id": a.AuditID, "kind": kind, "message": message, "retryable": false}})
		}
	}
	slices.SortStableFunc(out, func(a, b Event) int { return cmp.Compare(a.TMs, b.TMs) })
	return out
}

// pendingSummary is the summary as trace_started showed it: before the upstream answer and the
// gate decision.
func pendingSummary(s store.TraceSummary) store.TraceSummary {
	p := s
	p.UpdatedAt = s.CreatedAt
	p.EffectiveMode, p.UpstreamStatus = nil, nil
	p.Action, p.WouldAction, p.SkipReason, p.Verdict, p.MaxRisk = nil, nil, nil, nil, nil
	p.GateStatus = store.GateStatusPending
	p.Reasons = []string{}
	p.CheckedAgainst = json.RawMessage("null")
	p.Timings = store.Timings{}
	p.DecisionModel = nil
	p.Audit = store.TraceAudit{Status: store.TraceAuditNone}
	p.ResponsePreview = nil
	return p
}

// decidedSummary is the summary as gate_decision showed it: decided, with a planned deep audit
// still queued (its verdict arrives with audit_completed).
func decidedSummary(s store.TraceSummary) store.TraceSummary {
	switch s.Audit.Status {
	case "running", "completed", "degraded", "failed":
		s.Audit = store.TraceAudit{AuditID: s.Audit.AuditID, Status: "queued", Lane: s.Audit.Lane, Selection: s.Audit.Selection}
	}
	return s
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
