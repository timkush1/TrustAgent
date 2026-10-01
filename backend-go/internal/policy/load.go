// Package policy is the gateway policy engine (SPEC §2.7, §3.1 internal/policy, §7.2): it parses
// and validates policy/v1 documents (yaml.v3 with KnownFields, errors with paths, lines and
// columns), resolves every route's effective settings, matches requests to routes and applies
// the client mode override, computes each route's block readiness and effective mode, keeps
// whole-document versions in Postgres (newest wins) with file divergence, adopt-file, the config
// lock and hot reload (LISTEN/NOTIFY plus a poll), and previews a policy change by replaying
// stored answers through decide().
package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evidence"
)

// Sources of a loaded policy (SystemStatus.policy.source). A database version carries the
// source of its policy_versions row.
const (
	SourceBootstrap = "bootstrap"
	SourceFile      = "file"
	SourceDashboard = "dashboard"
	SourceRollback  = "rollback"
	SourceAdoptFile = "adopt_file"
	// SourceBuiltin is a policy made of the built-in defaults only (no file, no database).
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

// Env is everything validation depends on besides the document.
type Env struct {
	Seeds Seeds
	Specs Resolver
	// Enforcing accepts block and fallback modes. The /v1 handler enforces them since P3, so the
	// gateway, LoadFile and Parse set it; a caller that does not enforce leaves it off, so a policy
	// never claims enforcement nobody does.
	Enforcing bool
	// Knowledge accepts the knowledge evidence source (the knowledge add-on is enabled).
	Knowledge bool
	// FallbackUpstream accepts fallback.upstream: fallback (TRUTHTABLE_FALLBACK_UPSTREAM_URL is
	// set, so the gateway has a fallback upstream to call).
	FallbackUpstream bool
}

// Policy is a loaded, validated policy document.
type Policy struct {
	// Version is the policy version reported as <route>@v<version>: the policy_versions id, or,
	// without a database, 1 for the first policy loaded and one more per reload.
	Version int64
	// Source is the version source (bootstrap, file, dashboard, rollback, adopt_file) or builtin;
	// Path is the file read, if any.
	Source    string
	Path      string
	Overrides Overrides
	// Routes are the configured routes in match order; Default applies when none matches.
	Routes  []*Route
	Default *Route

	// YAML is the document text; Doc is the document as written (PolicyDoc JSON) and SHA256 is
	// sha256(JCS(Doc)).
	YAML   string
	Doc    json.RawMessage
	SHA256 string
	// Warnings are the non-fatal validation findings.
	Warnings []FieldError

	// enforcing records Env.Enforcing: clients may then ask for block and fallback.
	enforcing bool
}

// AllRoutes returns the configured routes followed by the default route.
func (p *Policy) AllRoutes() []*Route {
	return append(slices.Clone(p.Routes), p.Default)
}

// Route returns the route named name ("default" for the default route).
func (p *Policy) Route(name string) (*Route, bool) {
	if name == DefaultRouteName {
		return p.Default, true
	}
	for _, r := range p.Routes {
		if r.Name == name {
			return r, true
		}
	}
	return nil, false
}

// builtinYAML is the document of the built-in policy: the defaults route only.
const builtinYAML = "version: 1\n"

// LoadFile reads and validates the policy at path as this build's gateway does (block and fallback
// accepted, Env.Enforcing). A missing file yields the built-in policy (the defaults route only), so
// a fresh install gates in the seeded mode; any other error is returned.
func LoadFile(path string, seeds Seeds, specs Resolver) (*Policy, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the operator names the policy file (TRUTHTABLE_POLICY_FILE)
	if errors.Is(err, fs.ErrNotExist) {
		p, err := Parse([]byte(builtinYAML), seeds, specs)
		if err != nil {
			return nil, err
		}
		p.Source = SourceBuiltin
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	res := Check(b, Env{Seeds: seeds, Specs: specs, Enforcing: true})
	if res.Policy == nil {
		return nil, fmt.Errorf("%s: %w", path, res.Err())
	}
	res.Policy.Path = path
	return res.Policy, nil
}

// Parse validates a policy document and resolves every route's effective settings. The error is
// a *ValidationError. Block and fallback modes are accepted, as this build enforces them
// (Env.Enforcing); use Check for another Env and the full result.
func Parse(doc []byte, seeds Seeds, specs Resolver) (*Policy, error) {
	res := Check(doc, Env{Seeds: seeds, Specs: specs, Enforcing: true})
	if res.Policy == nil {
		return nil, res.Err()
	}
	return res.Policy, nil
}

// Result is the outcome of Check.
type Result struct {
	// Policy is the loaded policy, nil when there are errors.
	Policy   *Policy
	Errors   []FieldError
	Warnings []FieldError
}

// Err returns the errors as a *ValidationError, or nil.
func (r *Result) Err() error {
	if len(r.Errors) == 0 {
		return nil
	}
	return &ValidationError{Errors: r.Errors, Warnings: r.Warnings}
}

// Check validates a YAML (or JSON) policy document. It never panics on user input.
func Check(src []byte, env Env) *Result {
	return check(src, env, false)
}

// CheckDoc validates a JSON policy document (the {doc} form of the REST API). The policy's YAML
// is generated from the document; errors report paths only.
func CheckDoc(doc json.RawMessage, env Env) *Result {
	text, err := yamlFromDoc(doc)
	if err != nil {
		return &Result{Errors: []FieldError{{Path: "doc", Message: err.Error()}}}
	}
	return check([]byte(text), env, true)
}

func check(src []byte, env Env, fromDoc bool) *Result {
	res := &Result{}
	d, errs := decodeDocument(src)
	if len(errs) > 0 {
		res.Errors = stripPositions(errs, fromDoc)
		return res
	}
	d.fromDoc = fromDoc
	c := newChecker(d, env)
	p := c.run()
	res.Errors = stripPositions(dedupe(c.errs), fromDoc)
	res.Warnings = stripPositions(dedupe(c.warns), fromDoc)
	if len(res.Errors) > 0 {
		return res
	}
	p.YAML = string(src)
	var err error
	if p.Doc, err = json.Marshal(d.doc); err == nil {
		p.SHA256, err = decision.SHA256Hex(d.doc)
	}
	if err != nil {
		res.Errors = []FieldError{{Message: "the document cannot be canonicalized: " + err.Error()}}
		return res
	}
	p.Warnings = res.Warnings
	p.enforcing = env.Enforcing
	res.Policy = p
	return res
}

func stripPositions(errs []FieldError, strip bool) []FieldError {
	if !strip {
		return errs
	}
	out := make([]FieldError, len(errs))
	for i, e := range errs {
		e.Line, e.Col = 0, 0
		out[i] = e
	}
	return out
}

// dedupe drops repeated (path, message) findings, keeping the first; a defaults finding is
// otherwise repeated for every route that inherits the value.
func dedupe(errs []FieldError) []FieldError {
	seen := map[string]bool{}
	out := make([]FieldError, 0, len(errs))
	for _, e := range errs {
		k := e.Path + "\x00" + e.Message
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

// FileSHA256 is the hash of a policy file's bytes (policy_versions.file_sha256).
func FileSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
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
	StreamingBuffer      = "buffer"
	HeadersFull          = "full"
	HeadersActionOnly    = "action_only"
	StoreBodiesAll       = "all"
	StoreBodiesSampled   = "sampled"
	StoreBodiesNone      = "none"
)
