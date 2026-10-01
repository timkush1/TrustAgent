package policy

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/evidence"
)

func registry(t *testing.T) *evaluators.Registry {
	t.Helper()
	r, err := registryFor()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func registryFor() (*evaluators.Registry, error) { return evaluators.Builtins() }

func parse(t *testing.T, doc string) *Policy {
	t.Helper()
	p, err := Parse([]byte(doc), DefaultSeeds(), registry(t))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func parseErr(t *testing.T, doc string) string {
	t.Helper()
	_, err := Parse([]byte(doc), DefaultSeeds(), registry(t))
	if err == nil {
		t.Fatalf("Parse succeeded, want an error for:\n%s", doc)
	}
	return err.Error()
}

// TestRepoPolicy loads config/policies.yaml, the policy the proxy and the S0 demo run on.
func TestRepoPolicy(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "policies.yaml")
	p, err := LoadFile(path, DefaultSeeds(), registry(t))
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != SourceFile || p.Version != 1 || p.Path != path {
		t.Errorf("policy %+v", p)
	}
	def := p.Default
	if def.Mode != decision.ModeObserve || def.Fail != decision.FailOpen || def.DeadlineMs != 600 {
		t.Errorf("default route %+v", def.Settings)
	}
	var refs []string
	for _, e := range def.Evaluators {
		refs = append(refs, e.Ref)
	}
	if !reflect.DeepEqual(refs, []string{"groundedness@1", "contradiction@1", "refusal@1", "pii_secret_leak@1"}) {
		t.Errorf("default evaluators %v", refs)
	}
	if def.DeepAudit.PassSampleRate != 0.02 || def.DeepAudit.UniformSampleRate != 0.01 || def.StoreBodies != StoreBodiesSampled {
		t.Errorf("default deep_audit %+v store_bodies %s", def.DeepAudit, def.StoreBodies)
	}

	sb := p.Match(&Request{Path: "/v1/chat/completions", Headers: http.Header{"X-Trustagent-Route": {"support-bot"}}})
	if sb.Name != "support-bot" || p.Label(sb) != "support-bot@v1" {
		t.Fatalf("matched %s", sb.Name)
	}
	if sb.Mode != decision.ModeFlag || len(sb.Evaluators) != 2 || sb.Evaluators[1].Ref != "contradiction@1" {
		t.Errorf("support-bot %+v", sb.Settings)
	}
	// deep_audit merges key by key: the route sets two rates, the rest comes from defaults.
	want := DeepAuditSettings{Enabled: true, PassSampleRate: 1, UniformSampleRate: 0, ErrorSampleRate: 0.1, Verifier: "auto"}
	if sb.DeepAudit != want {
		t.Errorf("support-bot deep_audit %+v, want %+v", sb.DeepAudit, want)
	}
	if sb.StoreBodies != StoreBodiesAll || sb.Evidence.MaxTokens != 8000 || !reflect.DeepEqual(sb.Evidence.Sources, evidence.DefaultSources) {
		t.Errorf("support-bot inherited settings %+v", sb.Settings)
	}
	if other := p.Match(&Request{Path: "/v1/chat/completions", Headers: http.Header{}}); other != p.Default {
		t.Errorf("unmatched request went to %s", other.Name)
	}
}

func TestMissingFileIsBuiltin(t *testing.T) {
	p, err := LoadFile(filepath.Join(t.TempDir(), "nope.yaml"), Seeds{Mode: decision.ModeFlag, Fail: decision.FailOpen, MaxResponseTokens: 4000}, registry(t))
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != SourceBuiltin || len(p.Routes) != 0 || p.Default.Mode != decision.ModeFlag || p.Default.Decision.MaxResponseTokens != 4000 {
		t.Errorf("builtin policy %+v default %+v", p, p.Default.Settings)
	}
}

func TestLoadFileErrorsNamePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(path, []byte("version: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path, DefaultSeeds(), registry(t)); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("err %v, want it to name %s", err, path)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]struct{ doc, want string }{
		"no version":          {"defaults: {}\n", "version must be 1"},
		"empty":               {"", "version must be 1"},
		"unknown field":       {"version: 1\ndefaults: {modes: flag}\n", "line 2"},
		"unknown route field": {"version: 1\nroutes:\n  - name: a\n    match: {}\n    enforce: true\n", "line 5"},
		"bad mode":            {"version: 1\ndefaults: {mode: strict}\n", `unknown value "strict"`},
		"reserved name":       {"version: 1\nroutes:\n  - name: default\n    match: {}\n", "reserved"},
		"duplicate name":      {"version: 1\nroutes:\n  - {name: a, match: {}}\n  - {name: a, match: {}}\n", "duplicate route"},
		"bad name":            {"version: 1\nroutes:\n  - {name: A_b, match: {}}\n", "must match"},
		"no match":            {"version: 1\nroutes:\n  - {name: a}\n", "match: required"},
		"unknown ref":         {"version: 1\ndefaults:\n  evaluators: [{ref: nope@1}]\n", "unknown evaluator nope@1"},
		"latest ref":          {"version: 1\ndefaults:\n  evaluators: [{ref: groundedness@latest}]\n", "evaluators[0].ref"},
		"twice":               {"version: 1\ndefaults:\n  evaluators: [{ref: groundedness@1}, {ref: groundedness@1}]\n", "attached twice"},
		"undeclared param":    {"version: 1\ndefaults:\n  evaluators: [{ref: refusal@1, params: {x: y}}]\n", "declares no such param"},
		"threshold order":     {"version: 1\ndefaults:\n  evaluators: [{ref: groundedness@1, thresholds: {warn_at: 0.99}}]\n", "must not exceed critical_at"},
		"escalate order":      {"version: 1\ndefaults:\n  evaluators: [{ref: groundedness@1, thresholds: {escalate_from: 0.9}}]\n", "must be below warn_at"},
		"knowledge":           {"version: 1\ndefaults:\n  evidence: {sources: [request_system, knowledge]}\n", "knowledge add-on"},
		"tag pattern":         {"version: 1\ndefaults:\n  evidence: {tag_patterns: [{pattern: '('}]}\n", "tag_patterns[0]"},
		"tag flags":           {"version: 1\ndefaults:\n  evidence: {tag_patterns: [{pattern: 'x', flags: g}]}\n", "tag_patterns[0]"},
		"deadline":            {"version: 1\ndefaults: {deadline_ms: 50}\n", "deadline_ms"},
		"hedge":               {"version: 1\ndefaults: {hedge_after_ms: 700}\n", "hedge_after_ms"},
		"client mode":         {"version: 1\noverrides: {client_mode: always}\n", "client_mode"},
		"detector":            {"version: 1\ndefaults:\n  detectors: {flag_on: [nope]}\n", "detectors"},
		"sources subset":      {"version: 1\ndefaults:\n  evidence: {sources: [request_system]}\n  evaluators: [{ref: groundedness@1, evidence_sources: [extension]}]\n", "not a route evidence source"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if msg := parseErr(t, c.doc); !strings.Contains(msg, c.want) {
				t.Errorf("error %q lacks %q", msg, c.want)
			}
		})
	}
}

// TestOnlyAnEnforcingEnvAcceptsBlockAndFallback: a caller that does not enforce (Env.Enforcing
// off) gets block and fallback refused, so a policy never claims enforcement nobody does; Parse
// and LoadFile check as this build's gateway, which enforces both.
func TestOnlyAnEnforcingEnvAcceptsBlockAndFallback(t *testing.T) {
	for _, mode := range []string{"block", "fallback"} {
		doc := "version: 1\nroutes:\n  - name: pay\n    match: {}\n    mode: " + mode + "\n    fallback: {upstream: same}\n"
		if res := Check([]byte(doc), Env{Seeds: DefaultSeeds(), Specs: registry(t)}); res.Policy != nil ||
			!strings.Contains(res.Err().Error(), mode+" is not enforced by this build") {
			t.Errorf("%s without Enforcing: %v", mode, res.Errors)
		}
		if _, err := Parse([]byte(doc), DefaultSeeds(), registry(t)); err != nil {
			t.Errorf("Parse %s: %v", mode, err)
		}
	}
}

func TestMergeArraysReplaceObjectsMerge(t *testing.T) {
	p := parse(t, `version: 1
defaults:
  evaluators: [{ref: refusal@1}]
  evidence: {sources: [request_system, extension], untrusted_tools: ["web_*"]}
  detectors: {min_integer: 20}
routes:
  - name: a
    match: {paths: ["/v1/*"]}
    evidence: {sources: [extension]}
    evaluators:
      - ref: groundedness@1
        thresholds: {space: raw, critical_at: null, escalate_from: 0.3}
      - {ref: commitments@1, stage: shadow, enabled: false, params: {allowed_commitments: "None."}}
`)
	a := p.Routes[0]
	if !reflect.DeepEqual(a.Evidence.Sources, []string{"extension"}) || !reflect.DeepEqual(a.Evidence.UntrustedTools, []string{"web_*"}) {
		t.Errorf("evidence %+v: sources replace, untrusted_tools inherited", a.Evidence)
	}
	if a.Detectors.MinInteger != 20 || len(a.Detectors.FlagOn) != 5 {
		t.Errorf("detectors %+v", a.Detectors)
	}
	g := a.Evaluators[0]
	th := g.Thresholds.Decision()
	if !th.CriticalAt.Set || th.CriticalAt.Value != nil || !th.EscalateFrom.Set || *th.EscalateFrom.Value != 0.3 || th.WarnAt != nil {
		t.Errorf("thresholds %+v: critical_at explicit null, escalate_from 0.3, warn_at absent", th)
	}
	c := a.Evaluators[1]
	if c.IsEnabled() || c.StageOrDefault() != decision.StageShadow || c.Params["allowed_commitments"] != "None." {
		t.Errorf("commitments %+v", c)
	}
	if g.StageOrDefault() != decision.StageActive || !g.IsEnabled() {
		t.Errorf("groundedness defaults %+v", g)
	}
	if (*Thresholds)(nil).Decision().Space != decision.SpaceRaw {
		t.Error("no overrides means raw space")
	}
}

func TestMatchers(t *testing.T) {
	p := parse(t, `version: 1
routes:
  - name: by-key
    match: {key_labels: [trafficgen]}
  - name: by-model-and-path
    match: {models: ["gpt-4o*"], paths: ["/v1/chat/*"]}
  - name: by-header
    match: {headers: {x-trustagent-route: support-bot}}
`)
	cases := []struct {
		req  Request
		want string
	}{
		{Request{KeyLabel: "trafficgen", Headers: http.Header{}}, "by-key"},
		{Request{Model: "gpt-4o-mini", Path: "/v1/chat/completions", Headers: http.Header{}}, "by-model-and-path"},
		{Request{Model: "gpt-4o-mini", Path: "/v1/completions", Headers: http.Header{}}, "default"},
		{Request{Headers: http.Header{"X-Trustagent-Route": {"support-bot"}}}, "by-header"},
		{Request{Headers: http.Header{}, BodyRoute: "support-bot"}, "by-header"},
		{Request{Headers: http.Header{"X-Trustagent-Route": {"other"}}, BodyRoute: "support-bot"}, "default"},
		{Request{Headers: http.Header{"X-Trustagent-Route": {"Support-Bot"}}}, "default"},
	}
	for _, c := range cases {
		if got := p.Match(&c.req).Name; got != c.want {
			t.Errorf("%+v matched %s, want %s", c.req, got, c.want)
		}
	}
}

func TestClientMode(t *testing.T) {
	observe := &Route{Name: "r", Settings: Settings{Mode: decision.ModeObserve}}
	flag := &Route{Name: "r", Settings: Settings{Mode: decision.ModeFlag}}
	cases := []struct {
		clientMode, route, requested, want string
		applied                            bool
	}{
		{ClientModeNone, "observe", "flag", "observe", false},
		{ClientModeStricterOnly, "observe", "flag", "flag", true},
		{ClientModeStricterOnly, "flag", "observe", "flag", false},
		{ClientModeStricterOnly, "observe", "block", "observe", false}, // not enforceable in this build
		{ClientModeAny, "flag", "Observe", "observe", true},
		{ClientModeAny, "flag", "", "flag", false},
		{ClientModeAny, "flag", "bogus", "flag", false},
	}
	for _, c := range cases {
		p := &Policy{Overrides: Overrides{ClientMode: c.clientMode}}
		r := observe
		if c.route == "flag" {
			r = flag
		}
		got, applied := p.Mode(r, c.requested)
		if got != c.want || applied != c.applied {
			t.Errorf("%s %s<-%q: got %s/%v, want %s/%v", c.clientMode, c.route, c.requested, got, applied, c.want, c.applied)
		}
	}
}
