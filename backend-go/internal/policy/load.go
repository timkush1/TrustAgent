// Package policy loads the gateway policy (config/policies.yaml, schema policy/v1, SPEC §2.7) and
// resolves a request to its route.
//
// S0 is the minimal loader: parse with KnownFields, merge every route over defaults over the
// built-in defaults (objects merge key by key, arrays replace), validate what the S0 gate relies
// on, match routes, and apply the client mode override. Modes are limited to observe and flag
// until block and fallback enforcement land (P3). Database versions, hot reload, file divergence,
// readiness and dry-run are P3.
package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evidence"
)

// Sources of a loaded policy (SystemStatus.policy.source).
const (
	SourceFile    = "file"
	SourceBuiltin = "builtin"
)

// Seeds are the environment values the built-in defaults take (SPEC §2.7: mode and fail seed from
// TRUTHTABLE_GATE_DEFAULT_MODE and TRUTHTABLE_GATE_FAIL_MODE; decision.max_response_tokens from
// TRUTHTABLE_JEV_MAX_RESPONSE_TOKENS).
type Seeds struct {
	Mode              string
	Fail              string
	MaxResponseTokens int
}

// DefaultSeeds are the seeds with every environment key at its default.
func DefaultSeeds() Seeds {
	return Seeds{Mode: decision.ModeObserve, Fail: decision.FailOpen, MaxResponseTokens: 6000}
}

// Resolver resolves evaluator refs; *evaluators.Registry implements it.
type Resolver interface {
	Get(ref string) (*decision.Spec, bool)
}

// Policy is a loaded, validated policy document.
type Policy struct {
	// Version is the policy version reported as <route>@v<version>. Versions are database ids from
	// P3; a file or built-in policy is version 1 until then.
	Version int64
	// Source is file or builtin; Path is the file read (empty for builtin).
	Source    string
	Path      string
	Overrides Overrides
	// Routes are the configured routes in match order; Default applies when none matches.
	Routes  []*Route
	Default *Route
}

// LoadFile reads and validates the policy at path. A missing file yields the built-in policy (the
// defaults route only), so a fresh install gates in the seeded mode; any other error is returned.
func LoadFile(path string, seeds Seeds, specs Resolver) (*Policy, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the operator names the policy file (TRUTHTABLE_POLICY_FILE)
	if errors.Is(err, fs.ErrNotExist) {
		p, err := Parse([]byte("version: 1\n"), seeds, specs)
		if err != nil {
			return nil, err
		}
		p.Source = SourceBuiltin
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	p, err := Parse(b, seeds, specs)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p.Path = path
	return p, nil
}

// Parse validates a policy document and resolves every route's effective settings.
func Parse(doc []byte, seeds Seeds, specs Resolver) (*Policy, error) {
	var typed docYAML
	dec := yaml.NewDecoder(bytes.NewReader(doc))
	dec.KnownFields(true)
	if err := dec.Decode(&typed); err != nil && !errors.Is(err, io.EOF) { // empty: fails the version check
		return nil, fmt.Errorf("policy: %w", err)
	}
	if typed.Version == nil || *typed.Version != 1 {
		return nil, errors.New("policy: version must be 1")
	}

	var raw struct {
		Defaults map[string]any   `yaml:"defaults"`
		Routes   []map[string]any `yaml:"routes"`
	}
	if err := yaml.Unmarshal(doc, &raw); err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}

	p := &Policy{Version: 1, Source: SourceFile, Overrides: Overrides{ClientMode: ClientModeNone}}
	if typed.Overrides != nil {
		p.Overrides = *typed.Overrides
		if p.Overrides.ClientMode == "" {
			p.Overrides.ClientMode = ClientModeNone
		}
	}
	var errs []string
	if !slices.Contains([]string{ClientModeNone, ClientModeStricterOnly, ClientModeAny}, p.Overrides.ClientMode) {
		errs = append(errs, fmt.Sprintf("overrides.client_mode: unknown value %q", p.Overrides.ClientMode))
	}

	base := merge(builtinDefaults(seeds), raw.Defaults)
	def, err := resolve(DefaultRouteName, base, nil)
	if err != nil {
		return nil, fmt.Errorf("policy: defaults: %w", err)
	}
	errs = append(errs, validate("defaults", &def.Settings, specs)...)
	p.Default = def

	if len(raw.Routes) > maxRoutes {
		errs = append(errs, fmt.Sprintf("routes: at most %d routes", maxRoutes))
	}
	names := map[string]bool{}
	for i, rr := range raw.Routes {
		t := typed.Routes[i]
		where := fmt.Sprintf("routes[%d]", i)
		switch {
		case !routeName.MatchString(t.Name):
			errs = append(errs, fmt.Sprintf("%s.name: %q must match %s", where, t.Name, routeName))
		case t.Name == DefaultRouteName:
			errs = append(errs, fmt.Sprintf("%s.name: %q is reserved", where, t.Name))
		case names[t.Name]:
			errs = append(errs, fmt.Sprintf("%s.name: duplicate route %q", where, t.Name))
		}
		names[t.Name] = true
		if t.Match == nil {
			errs = append(errs, where+".match: required")
		}
		overlay := map[string]any{}
		for k, v := range rr {
			if k != "name" && k != "description" && k != "match" {
				overlay[k] = v
			}
		}
		r, err := resolve(t.Name, merge(base, overlay), t.Match)
		if err != nil {
			return nil, fmt.Errorf("policy: %s: %w", where, err)
		}
		r.Description = t.Description
		errs = append(errs, validate(where+" ("+t.Name+")", &r.Settings, specs)...)
		p.Routes = append(p.Routes, r)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("policy: %s", strings.Join(errs, "; "))
	}
	return p, nil
}

// resolve decodes merged settings strictly into a Route.
func resolve(name string, merged map[string]any, match *Match) (*Route, error) {
	b, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	r := &Route{Name: name}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r.Settings); err != nil {
		return nil, err
	}
	if match != nil {
		r.Match = *match
	}
	return r, nil
}

// merge returns base with overlay merged in: objects merge key by key, everything else (arrays
// included) replaces. A null in overlay leaves the base value. Neither input is modified.
func merge(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		if v == nil {
			continue
		}
		bm, bok := out[k].(map[string]any)
		om, ook := v.(map[string]any)
		if bok && ook {
			out[k] = merge(bm, om)
			continue
		}
		out[k] = v
	}
	return out
}

// builtinDefaults are the RouteSettings defaults of SPEC §2.7.
func builtinDefaults(s Seeds) map[string]any {
	return map[string]any{
		"mode": s.Mode, "fail": s.Fail,
		"deadline_ms": 600, "hedge_after_ms": 0, "sample_rate": 1.0, "require_calibration": true,
		"on_no_evidence": decision.OnNoEvidenceIgnore, "streaming": StreamingPassthrough,
		"stream_annotations": "comment", "buffer_keepalive_s": 15, "headers": HeadersFull,
		"evidence": map[string]any{
			"sources":      toAny(evidence.DefaultSources),
			"system_mode":  evidence.SystemModeDocumentsOnly,
			"line_headers": true, "tag_patterns": []any{}, "max_tokens": 8000, "untrusted_tools": []any{},
			"knowledge": map[string]any{"when": "fallback", "top_k": 5, "deadline_ms": 150, "min_score": 0.3},
		},
		"evaluators": []any{
			map[string]any{"ref": "groundedness@1"}, map[string]any{"ref": "contradiction@1"},
			map[string]any{"ref": "refusal@1"}, map[string]any{"ref": "pii_secret_leak@1"},
		},
		"detectors": map[string]any{
			"flag_on":     toAny([]string{decision.DetURLs, decision.DetQuotes, decision.DetCitation, decision.DetSecret, decision.DetPromptLeak}),
			"block_on":    []any{},
			"min_integer": decision.DefaultMinInteger,
		},
		"decision": map[string]any{"max_response_tokens": s.MaxResponseTokens},
		"deep_audit": map[string]any{
			"enabled": true, "pass_sample_rate": 0.02, "uniform_sample_rate": 0.01, "error_sample_rate": 0.1, "verifier": "auto",
		},
		"block": map[string]any{
			"style": "message", "message": "I can't provide a verified answer to that right now.",
			"status": 422, "anthropic_stop_reason": "refusal",
		},
		"fallback": map[string]any{
			"upstream": "same", "model": "", "on_fail": "block",
			"system_prompt": "Answer using only the information in CONTEXT. If CONTEXT does not contain the answer, say you don't have that information. Do not add facts, numbers, dates, names or links that are not in CONTEXT.",
		},
		"store_bodies": StoreBodiesSampled, "body_sample_rate_pass": 0.2,
	}
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// Route setting values the gate reads.
const (
	StreamingPassthrough = "passthrough"
	HeadersFull          = "full"
	HeadersActionOnly    = "action_only"
	StoreBodiesAll       = "all"
	StoreBodiesSampled   = "sampled"
	StoreBodiesNone      = "none"
)

const (
	maxRoutes     = 64
	maxEvaluators = 16
)

var (
	routeName    = regexp.MustCompile(`^[a-z0-9-]{1,48}$`)
	tagFlags     = regexp.MustCompile(`^[ims]{0,3}$`)
	validSources = []string{
		evidence.SourceRequestSystem, evidence.SourceRequestContext, evidence.SourceToolResults,
		evidence.SourceExtension, evidence.SourceKnowledge,
	}
)

// validate checks the effective settings of one route.
func validate(where string, s *Settings, specs Resolver) []string {
	var errs []string
	bad := func(field, format string, args ...any) {
		errs = append(errs, fmt.Sprintf("%s.%s: %s", where, field, fmt.Sprintf(format, args...)))
	}
	enum := func(field, v string, allowed ...string) {
		if !slices.Contains(allowed, v) {
			bad(field, "unknown value %q (want one of %s)", v, strings.Join(allowed, ", "))
		}
	}
	unit := func(field string, v float64) {
		if v < 0 || v > 1 {
			bad(field, "%v is outside [0, 1]", v)
		}
	}

	enum("mode", s.Mode, decision.ModeObserve, decision.ModeFlag, decision.ModeBlock, decision.ModeFallback)
	if s.Mode == decision.ModeBlock || s.Mode == decision.ModeFallback {
		bad("mode", "%s is not available yet: this build enforces observe and flag only (block and fallback land in P3)", s.Mode)
	}
	enum("fail", s.Fail, decision.FailOpen, decision.FailClosed)
	if s.DeadlineMs < 100 || s.DeadlineMs > 5000 {
		bad("deadline_ms", "%d is outside [100, 5000]", s.DeadlineMs)
	}
	if s.HedgeAfterMs != 0 && (s.HedgeAfterMs < 100 || s.HedgeAfterMs >= s.DeadlineMs) {
		bad("hedge_after_ms", "%d must be 0 or in [100, deadline_ms)", s.HedgeAfterMs)
	}
	unit("sample_rate", s.SampleRate)
	enum("on_no_evidence", s.OnNoEvidence, decision.OnNoEvidenceIgnore, decision.OnNoEvidenceFlag, decision.OnNoEvidenceBlock)
	enum("streaming", s.Streaming, StreamingPassthrough, "buffer")
	enum("stream_annotations", s.StreamAnnotations, "comment", "none")
	enum("headers", s.Headers, HeadersFull, HeadersActionOnly)
	enum("store_bodies", s.StoreBodies, StoreBodiesAll, StoreBodiesSampled, StoreBodiesNone)
	unit("body_sample_rate_pass", s.BodySampleRatePass)

	ev := &s.Evidence
	for i, src := range ev.Sources {
		enum(fmt.Sprintf("evidence.sources[%d]", i), src, validSources...)
		if slices.Index(ev.Sources, src) != i {
			bad("evidence.sources", "%s is listed twice", src)
		}
		if src == evidence.SourceKnowledge {
			bad("evidence.sources", "knowledge needs the knowledge add-on, which this build does not include")
		}
	}
	enum("evidence.system_mode", ev.SystemMode, evidence.SystemModeOff, evidence.SystemModeDocumentsOnly, evidence.SystemModeWhole)
	if len(ev.TagPatterns) > 16 {
		bad("evidence.tag_patterns", "at most 16 patterns")
	}
	for i, tp := range ev.TagPatterns {
		if !tagFlags.MatchString(tp.Flags) || len(tp.Pattern) == 0 || len(tp.Pattern) > 256 {
			bad(fmt.Sprintf("evidence.tag_patterns[%d]", i), "needs a pattern of 1-256 characters and flags within \"ims\"")
		} else if _, err := tp.Compile(); err != nil {
			bad(fmt.Sprintf("evidence.tag_patterns[%d]", i), "%v", err)
		}
	}
	if ev.MaxTokens < 500 || ev.MaxTokens > 20000 {
		bad("evidence.max_tokens", "%d is outside [500, 20000]", ev.MaxTokens)
	}

	for _, list := range [][]string{s.Detectors.FlagOn, s.Detectors.BlockOn} {
		for _, d := range list {
			enum("detectors", d, decision.DetectorIDs...)
		}
	}
	if s.Detectors.MinInteger < 0 {
		bad("detectors.min_integer", "must be >= 0")
	}
	if mrt := s.Decision.MaxResponseTokens; mrt < 500 || mrt > 12000 {
		bad("decision.max_response_tokens", "%d is outside [500, 12000]", mrt)
	}
	unit("deep_audit.pass_sample_rate", s.DeepAudit.PassSampleRate)
	unit("deep_audit.uniform_sample_rate", s.DeepAudit.UniformSampleRate)
	unit("deep_audit.error_sample_rate", s.DeepAudit.ErrorSampleRate)
	enum("deep_audit.verifier", s.DeepAudit.Verifier, "auto", "llm")

	errs = append(errs, validateEvaluators(where, s, specs)...)
	return errs
}

func validateEvaluators(where string, s *Settings, specs Resolver) []string {
	var errs []string
	if len(s.Evaluators) > maxEvaluators {
		errs = append(errs, fmt.Sprintf("%s.evaluators: at most %d evaluators", where, maxEvaluators))
	}
	ids := map[string]bool{}
	params := map[string]string{}
	for i, e := range s.Evaluators {
		at := fmt.Sprintf("%s.evaluators[%d]", where, i)
		id, _, err := decision.ParseRef(e.Ref)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s.ref: %v", at, err))
			continue
		}
		spec, ok := specs.Get(e.Ref)
		if !ok {
			errs = append(errs, fmt.Sprintf("%s.ref: unknown evaluator %s", at, e.Ref))
			continue
		}
		if ids[id] {
			errs = append(errs, fmt.Sprintf("%s.ref: evaluator %s is attached twice", at, id))
		}
		ids[id] = true
		if st := e.StageOrDefault(); st != decision.StageActive && st != decision.StageShadow {
			errs = append(errs, fmt.Sprintf("%s.stage: unknown value %q", at, e.Stage))
		}
		for name := range e.Params {
			if _, declared := spec.Params[name]; !declared {
				errs = append(errs, fmt.Sprintf("%s.params.%s: %s declares no such param", at, name, e.Ref))
			}
			if other, dup := params[name]; dup && other != e.Ref {
				errs = append(errs, fmt.Sprintf("%s.params.%s: also set by %s (param names are unique across a route)", at, name, other))
			}
			params[name] = e.Ref
		}
		for _, src := range e.EvidenceSources {
			if !slices.Contains(s.Evidence.Sources, src) {
				errs = append(errs, fmt.Sprintf("%s.evidence_sources: %s is not a route evidence source", at, src))
			}
		}
		errs = append(errs, validateThresholds(at, e.Thresholds, spec)...)
	}
	return errs
}

// validateThresholds checks escalate_from < warn_at <= critical_at after the route overrides are
// merged over the spec (SPEC §2.8 hard rules).
func validateThresholds(at string, t *Thresholds, spec *decision.Spec) []string {
	var errs []string
	dt := t.Decision()
	if dt.Space != decision.SpaceRaw && dt.Space != decision.SpaceCalibrated {
		errs = append(errs, fmt.Sprintf("%s.thresholds.space: unknown value %q", at, dt.Space))
	}
	warn := spec.Outcomes.WarnAt
	if dt.WarnAt != nil {
		warn = *dt.WarnAt
	}
	crit := spec.Outcomes.CriticalAt
	if dt.CriticalAt.Set {
		crit = dt.CriticalAt.Value
	}
	esc := spec.Escalation.EscalateFrom
	if dt.EscalateFrom.Set {
		esc = dt.EscalateFrom.Value
	}
	inUnit := func(name string, v *float64) {
		if v != nil && (*v < 0 || *v > 1) {
			errs = append(errs, fmt.Sprintf("%s.thresholds.%s: %v is outside [0, 1]", at, name, *v))
		}
	}
	inUnit("warn_at", &warn)
	inUnit("critical_at", crit)
	inUnit("escalate_from", esc)
	if esc != nil && !(*esc < warn) {
		errs = append(errs, fmt.Sprintf("%s.thresholds: escalate_from %v must be below warn_at %v", at, *esc, warn))
	}
	if crit != nil && !(warn <= *crit) {
		errs = append(errs, fmt.Sprintf("%s.thresholds: warn_at %v must not exceed critical_at %v", at, warn, *crit))
	}
	return errs
}
