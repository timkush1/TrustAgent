package policy

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evidence"
)

// Limits and patterns of contracts/schema/policy.v1.schema.json that the typed walk does not
// cover, plus the cross-field rules the schema delegates to the Go loader.
const (
	maxRoutes         = 64
	maxEvaluators     = 16
	maxParams         = 8
	maxTagPatterns    = 16
	maxUntrustedTools = 32
	maxMatchHeaders   = 16
	maxMatchItems     = 32
)

var (
	routeName      = regexp.MustCompile(`^[a-z0-9-]{1,48}$`)
	evaluatorRef   = regexp.MustCompile(`^[a-z][a-z0-9_]{1,47}@[0-9]+$`)
	paramName      = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
	headerName     = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	tagFlags       = regexp.MustCompile(`^[ims]{0,3}$`)
	validModes     = []string{decision.ModeObserve, decision.ModeFlag, decision.ModeBlock, decision.ModeFallback}
	validSources   = []string{evidence.SourceRequestSystem, evidence.SourceRequestContext, evidence.SourceToolResults, evidence.SourceExtension, evidence.SourceKnowledge}
	blockStatuses  = []int{400, 403, 409, 422, 451}
	clientModes    = []string{ClientModeNone, ClientModeStricterOnly, ClientModeAny}
	systemModes    = []string{evidence.SystemModeOff, evidence.SystemModeDocumentsOnly, evidence.SystemModeWhole}
	enforcingModes = []string{decision.ModeBlock, decision.ModeFallback}
)

// scope is where a route's settings are validated: the defaults (index -1) or routes[index].
type scope struct {
	name  string
	index int
}

// checker validates a decoded document and builds the Policy.
type checker struct {
	d            *document
	env          Env
	defaultsNode *yaml.Node
	routeNodes   []*yaml.Node
	errs, warns  []FieldError
}

func newChecker(d *document, env Env) *checker {
	c := &checker{d: d, env: env}
	if d.root != nil {
		c.defaultsNode = mappingValue(d.root, "defaults")
		if rs := mappingValue(d.root, "routes"); rs != nil && rs.Kind == yaml.SequenceNode {
			c.routeNodes = rs.Content
		}
	}
	return c
}

// locate finds where field of a route's effective settings is written: in the route, or
// inherited from the defaults or the built-in defaults (then the route is where the finding is
// reported unless it is local, see finding).
func (c *checker) locate(sc scope, field string) (path string, n *yaml.Node, inherited bool) {
	segs := parsePath(field)
	if sc.index >= 0 && sc.index < len(c.routeNodes) {
		rn := c.routeNodes[sc.index]
		prefix := fmt.Sprintf("routes[%d]", sc.index)
		if node, ok := find(rn, segs); ok {
			return join(prefix, field), node, false
		}
		if c.defaultsNode != nil {
			if node, ok := find(c.defaultsNode, segs); ok {
				return join("defaults", field), node, true
			}
		}
		node, _ := find(rn, segs)
		return join(prefix, field), node, true
	}
	if c.defaultsNode != nil {
		node, _ := find(c.defaultsNode, segs)
		return join("defaults", field), node, false
	}
	return join("defaults", field), c.d.root, false
}

// finding records an error or warning about field. A local finding depends only on the value
// of field, so when a route inherits that value from the defaults it was already reported there
// and is not repeated.
func (c *checker) finding(warn, local bool, sc scope, field, format string, args ...any) {
	path, n, inherited := c.locate(sc, field)
	if inherited && local {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if inherited {
		msg += fmt.Sprintf(" (in route %q)", sc.name)
	}
	fe := FieldError{Path: path, Message: msg}
	if n != nil {
		fe.Line, fe.Col = n.Line, n.Column
	}
	if warn {
		c.warns = append(c.warns, fe)
	} else {
		c.errs = append(c.errs, fe)
	}
}

func (c *checker) bad(sc scope, field, format string, args ...any) {
	c.finding(false, true, sc, field, format, args...)
}

func (c *checker) badCross(sc scope, field, format string, args ...any) {
	c.finding(false, false, sc, field, format, args...)
}

func (c *checker) warn(sc scope, field, format string, args ...any) {
	c.finding(true, true, sc, field, format, args...)
}

func (c *checker) warnCross(sc scope, field, format string, args ...any) {
	c.finding(true, false, sc, field, format, args...)
}

// topError records an error about a top-level path of the document.
func (c *checker) topError(path string, n *yaml.Node, format string, args ...any) {
	fe := FieldError{Path: path, Message: fmt.Sprintf(format, args...)}
	if n == nil {
		n = c.d.root
	}
	if n != nil {
		fe.Line, fe.Col = n.Line, n.Column
	}
	c.errs = append(c.errs, fe)
}

// run validates the document and returns the resolved policy (meaningful only without errors).
func (c *checker) run() *Policy {
	d := c.d
	if d.typed.Version == nil || *d.typed.Version != 1 {
		c.topError("version", mappingValue(d.root, "version"), "version must be 1")
		return nil
	}
	p := &Policy{Version: 1, Source: SourceFile, Overrides: Overrides{ClientMode: ClientModeNone}}
	if d.typed.Overrides != nil {
		p.Overrides = *d.typed.Overrides
		if p.Overrides.ClientMode == "" {
			p.Overrides.ClientMode = ClientModeNone
		}
		if !slices.Contains(clientModes, p.Overrides.ClientMode) {
			n, _ := find(d.root, parsePath("overrides.client_mode"))
			c.topError("overrides.client_mode", n, "unknown value %q (want one of %s)", p.Overrides.ClientMode, strings.Join(clientModes, ", "))
		}
	}

	defaults, _ := d.doc["defaults"].(map[string]any)
	base := merge(builtinDefaults(c.env.Seeds), defaults)
	def, err := resolve(DefaultRouteName, base, nil)
	if err != nil {
		c.topError("defaults", c.defaultsNode, "%v", err)
		return nil
	}
	p.Default = def
	c.settings(scope{name: DefaultRouteName, index: -1}, &def.Settings)

	rawRoutes, _ := d.doc["routes"].([]any)
	if len(rawRoutes) > maxRoutes {
		c.topError("routes", mappingValue(d.root, "routes"), "at most %d routes", maxRoutes)
	}
	names := map[string]bool{}
	for i, raw := range rawRoutes {
		rr, _ := raw.(map[string]any)
		t := d.typed.Routes[i]
		sc := scope{name: t.Name, index: i}
		prefix := fmt.Sprintf("routes[%d]", i)
		nameNode := mappingValue(c.routeNodes[i], "name")
		switch {
		case !routeName.MatchString(t.Name):
			c.topError(prefix+".name", orNode(nameNode, c.routeNodes[i]), "%q must match %s", t.Name, routeName)
		case t.Name == DefaultRouteName:
			c.topError(prefix+".name", nameNode, "%q is reserved for the defaults route", t.Name)
		case names[t.Name]:
			c.topError(prefix+".name", nameNode, "duplicate route %q", t.Name)
		}
		names[t.Name] = true
		if utf8.RuneCountInString(t.Description) > 600 {
			c.topError(prefix+".description", mappingValue(c.routeNodes[i], "description"), "at most 600 characters")
		}
		if t.Match == nil {
			c.topError(prefix+".match", c.routeNodes[i], "match: required (use match: {} to match every request)")
		} else {
			c.match(prefix, i, t.Match)
		}
		overlay := map[string]any{}
		for k, v := range rr {
			if k != "name" && k != "description" && k != "match" {
				overlay[k] = v
			}
		}
		r, err := resolve(t.Name, merge(base, overlay), t.Match)
		if err != nil {
			c.topError(prefix, c.routeNodes[i], "%v", err)
			continue
		}
		r.Description = t.Description
		c.settings(sc, &r.Settings)
		p.Routes = append(p.Routes, r)
	}
	if p.Overrides.ClientRules {
		for _, r := range p.AllRoutes() {
			if slices.Contains(enforcingModes, r.Mode) {
				n, _ := find(d.root, parsePath("overrides.client_rules"))
				c.warns = append(c.warns, FieldError{Path: "overrides.client_rules", Line: n.Line, Col: n.Column,
					Message: fmt.Sprintf("client_rules lets end users rewrite rules on the enforcing route %q", r.Name)})
				break
			}
		}
	}
	return p
}

func orNode(n, fallback *yaml.Node) *yaml.Node {
	if n != nil {
		return n
	}
	return fallback
}

// match checks a route's match block.
func (c *checker) match(prefix string, i int, m *Match) {
	at := func(field string) *yaml.Node {
		n, _ := find(c.routeNodes[i], parsePath("match."+field))
		return n
	}
	if len(m.Headers) > maxMatchHeaders {
		c.topError(prefix+".match.headers", at("headers"), "at most %d headers", maxMatchHeaders)
	}
	for name, v := range m.Headers {
		if !headerName.MatchString(name) {
			c.topError(prefix+".match.headers."+name, at("headers"), "header names must match %s", headerName)
		}
		if len(v) > 256 {
			c.topError(prefix+".match.headers."+name, at("headers"), "at most 256 characters")
		}
	}
	lists := []struct {
		field  string
		values []string
		max    int
		check  func(string) string
	}{
		{"paths", m.Paths, 256, func(s string) string {
			if !strings.HasPrefix(s, "/") {
				return "paths start with /"
			}
			return ""
		}},
		{"models", m.Models, 256, nil},
		{"key_labels", m.KeyLabels, 64, nil},
	}
	for _, l := range lists {
		if len(l.values) > maxMatchItems {
			c.topError(prefix+".match."+l.field, at(l.field), "at most %d entries", maxMatchItems)
		}
		for j, v := range l.values {
			path := fmt.Sprintf("%s.match.%s[%d]", prefix, l.field, j)
			switch {
			case v == "" || len(v) > l.max:
				c.topError(path, at(fmt.Sprintf("%s[%d]", l.field, j)), "must have 1 to %d characters", l.max)
			case slices.Index(l.values, v) != j:
				c.topError(path, at(fmt.Sprintf("%s[%d]", l.field, j)), "%q is listed twice", v)
			case l.check != nil && l.check(v) != "":
				c.topError(path, at(fmt.Sprintf("%s[%d]", l.field, j)), "%s", l.check(v))
			}
		}
	}
}

// settings validates one route's effective settings.
func (c *checker) settings(sc scope, s *Settings) {
	enum := func(field, v string, allowed ...string) {
		if !slices.Contains(allowed, v) {
			c.bad(sc, field, "unknown value %q (want one of %s)", v, strings.Join(allowed, ", "))
		}
	}
	unit := func(field string, v float64) {
		if v < 0 || v > 1 {
			c.bad(sc, field, "%v is outside [0, 1]", v)
		}
	}
	between := func(field string, v, lo, hi int) {
		if v < lo || v > hi {
			c.bad(sc, field, "%d is outside [%d, %d]", v, lo, hi)
		}
	}
	text := func(field, v string, lo, hi int) {
		if n := utf8.RuneCountInString(v); n < lo || n > hi {
			c.bad(sc, field, "must have %d to %d characters", lo, hi)
		}
	}

	enum("mode", s.Mode, validModes...)
	if slices.Contains(enforcingModes, s.Mode) && !c.env.Enforcing {
		c.bad(sc, "mode", "%s is not enforced by this build yet: use observe or flag (block and fallback need the enforcing gateway)", s.Mode)
	}
	enum("fail", s.Fail, decision.FailOpen, decision.FailClosed)
	between("deadline_ms", s.DeadlineMs, 100, 5000)
	if s.HedgeAfterMs != 0 && (s.HedgeAfterMs < 100 || s.HedgeAfterMs >= s.DeadlineMs) {
		c.badCross(sc, "hedge_after_ms", "%d must be 0 or in [100, deadline_ms %d)", s.HedgeAfterMs, s.DeadlineMs)
	}
	unit("sample_rate", s.SampleRate)
	enum("on_no_evidence", s.OnNoEvidence, decision.OnNoEvidenceIgnore, decision.OnNoEvidenceFlag, decision.OnNoEvidenceBlock)
	enum("streaming", s.Streaming, StreamingPassthrough, StreamingBuffer)
	enum("stream_annotations", s.StreamAnnotations, "comment", "none")
	between("buffer_keepalive_s", s.BufferKeepaliveS, 0, 60)
	enum("headers", s.Headers, HeadersFull, HeadersActionOnly)
	enum("store_bodies", s.StoreBodies, StoreBodiesAll, StoreBodiesSampled, StoreBodiesNone)
	unit("body_sample_rate_pass", s.BodySampleRatePass)

	ev := &s.Evidence
	for i, src := range ev.Sources {
		field := fmt.Sprintf("evidence.sources[%d]", i)
		enum(field, src, validSources...)
		if slices.Index(ev.Sources, src) != i {
			c.bad(sc, field, "%s is listed twice", src)
		}
		if src == evidence.SourceKnowledge && !c.env.Knowledge {
			c.bad(sc, field, "knowledge needs the knowledge add-on (COMPOSE_PROFILES=knowledge and TRUTHTABLE_KNOWLEDGE_ENABLED=true)")
		}
	}
	enum("evidence.system_mode", ev.SystemMode, systemModes...)
	if len(ev.TagPatterns) > maxTagPatterns {
		c.bad(sc, "evidence.tag_patterns", "at most %d patterns", maxTagPatterns)
	}
	for i, tp := range ev.TagPatterns {
		field := fmt.Sprintf("evidence.tag_patterns[%d]", i)
		switch {
		case len(tp.Pattern) == 0 || utf8.RuneCountInString(tp.Pattern) > 256:
			c.bad(sc, field+".pattern", "needs 1 to 256 characters")
		case !tagFlags.MatchString(tp.Flags):
			c.bad(sc, field+".flags", "flags are a subset of \"ims\"")
		default:
			if _, err := tp.Compile(); err != nil {
				c.bad(sc, field+".pattern", "not a valid RE2 pattern: %v", err)
			}
		}
	}
	between("evidence.max_tokens", ev.MaxTokens, 500, 20000)
	if len(ev.UntrustedTools) > maxUntrustedTools {
		c.bad(sc, "evidence.untrusted_tools", "at most %d globs", maxUntrustedTools)
	}
	for i, g := range ev.UntrustedTools {
		field := fmt.Sprintf("evidence.untrusted_tools[%d]", i)
		if g == "" || utf8.RuneCountInString(g) > 128 {
			c.bad(sc, field, "must have 1 to 128 characters")
		} else if slices.Index(ev.UntrustedTools, g) != i {
			c.bad(sc, field, "%q is listed twice", g)
		}
	}
	enum("evidence.knowledge.when", ev.Knowledge.When, "fallback", "always")
	between("evidence.knowledge.top_k", ev.Knowledge.TopK, 1, 50)
	between("evidence.knowledge.deadline_ms", ev.Knowledge.DeadlineMs, 1, 5000)
	unit("evidence.knowledge.min_score", ev.Knowledge.MinScore)

	for _, list := range []struct {
		field string
		ids   []string
	}{{"detectors.flag_on", s.Detectors.FlagOn}, {"detectors.block_on", s.Detectors.BlockOn}} {
		for i, d := range list.ids {
			field := fmt.Sprintf("%s[%d]", list.field, i)
			enum(field, d, decision.DetectorIDs...)
			if slices.Index(list.ids, d) != i {
				c.bad(sc, field, "%s is listed twice", d)
			}
		}
	}
	if s.Detectors.MinInteger < 0 {
		c.bad(sc, "detectors.min_integer", "must be >= 0")
	}
	if m := s.Decision.Model; m != "" && utf8.RuneCountInString(m) > 128 {
		c.bad(sc, "decision.model", "at most 128 characters")
	}
	if _, n, _ := c.locate(sc, "decision.model"); c.written(sc, "decision.model") && n.Value == "" {
		c.bad(sc, "decision.model", "must not be empty (omit it to use TRUTHTABLE_JEV_MODEL)")
	}
	between("decision.max_response_tokens", s.Decision.MaxResponseTokens, 500, 12000)
	unit("deep_audit.pass_sample_rate", s.DeepAudit.PassSampleRate)
	unit("deep_audit.uniform_sample_rate", s.DeepAudit.UniformSampleRate)
	unit("deep_audit.error_sample_rate", s.DeepAudit.ErrorSampleRate)
	enum("deep_audit.verifier", s.DeepAudit.Verifier, "auto", "llm")
	enum("block.style", s.Block.Style, "message", "error")
	text("block.message", s.Block.Message, 1, 2000)
	if !slices.Contains(blockStatuses, s.Block.Status) {
		c.bad(sc, "block.status", "%d is not one of 400, 403, 409, 422, 451", s.Block.Status)
	}
	enum("block.anthropic_stop_reason", s.Block.AnthropicStopReason, "refusal", "end_turn")
	enum("fallback.upstream", s.Fallback.Upstream, "same", "fallback")
	text("fallback.model", s.Fallback.Model, 0, 128)
	text("fallback.system_prompt", s.Fallback.SystemPrompt, 1, 4000)
	enum("fallback.on_fail", s.Fallback.OnFail, "block", "flag", "original")

	c.evaluators(sc, s)

	enforcing := slices.Contains(enforcingModes, s.Mode)
	if s.Fail == decision.FailClosed && !enforcing {
		c.warnCross(sc, "fail", "fail: closed only matters in block and fallback modes (this route is %s)", s.Mode)
	}
	if s.SampleRate < 1 && s.Mode != decision.ModeObserve {
		c.warnCross(sc, "sample_rate", "sample_rate applies to observe routes only; this %s route checks every request", s.Mode)
	}
	if enforcing && slices.Contains(s.Evidence.Sources, evidence.SourceRequestContext) {
		c.warnCross(sc, "evidence.sources", "the %s route trusts user-message context: an end user could paste text that makes a false answer look supported (drop request_context)", s.Mode)
	}
}

// written reports whether field is written for sc (in the route or in the defaults).
func (c *checker) written(sc scope, field string) bool {
	segs := parsePath(field)
	if sc.index >= 0 && sc.index < len(c.routeNodes) {
		if _, ok := find(c.routeNodes[sc.index], segs); ok {
			return true
		}
	}
	if c.defaultsNode != nil {
		_, ok := find(c.defaultsNode, segs)
		return ok
	}
	return false
}

// evaluators validates the route's evaluator list.
func (c *checker) evaluators(sc scope, s *Settings) {
	if len(s.Evaluators) > maxEvaluators {
		c.bad(sc, "evaluators", "at most %d evaluators", maxEvaluators)
	}
	ids := map[string]string{}
	params := map[string]string{}
	attached := map[string]bool{}
	for _, e := range s.Evaluators {
		if id, _, err := decision.ParseRef(e.Ref); err == nil && e.IsEnabled() && e.StageOrDefault() == decision.StageActive {
			attached[id] = true
		}
	}
	for i, e := range s.Evaluators {
		at := fmt.Sprintf("evaluators[%d]", i)
		if !evaluatorRef.MatchString(e.Ref) {
			c.bad(sc, at+".ref", "%q is not an evaluator ref <id>@<version> (\"@latest\" is not allowed)", e.Ref)
			continue
		}
		id, _, err := decision.ParseRef(e.Ref)
		if err != nil {
			c.bad(sc, at+".ref", "%v", err)
			continue
		}
		var spec *decision.Spec
		if c.env.Specs != nil {
			spec, _ = c.env.Specs.Get(e.Ref)
		}
		if spec == nil {
			c.bad(sc, at+".ref", "unknown evaluator %s", e.Ref)
			continue
		}
		if other, dup := ids[id]; dup {
			c.bad(sc, at+".ref", "evaluator %s is attached twice (also as %s)", id, other)
		}
		ids[id] = e.Ref
		if st := e.StageOrDefault(); st != decision.StageActive && st != decision.StageShadow {
			c.bad(sc, at+".stage", "unknown value %q (want active or shadow)", e.Stage)
		}
		if len(e.Params) > maxParams {
			c.bad(sc, at+".params", "at most %d params", maxParams)
		}
		for _, name := range sortedKeys(e.Params) {
			field := at + ".params." + name
			switch {
			case !paramName.MatchString(name):
				c.bad(sc, field, "param names must match %s", paramName)
			case utf8.RuneCountInString(e.Params[name]) > 2000:
				c.bad(sc, field, "at most 2000 characters")
			}
			if _, declared := spec.Params[name]; !declared {
				c.bad(sc, field, "%s declares no such param", e.Ref)
			}
			if other, dup := params[name]; dup && other != e.Ref {
				c.bad(sc, field, "also set by %s (param names are unique across a route)", other)
			}
			params[name] = e.Ref
		}
		for j, src := range e.EvidenceSources {
			field := fmt.Sprintf("%s.evidence_sources[%d]", at, j)
			switch {
			case !slices.Contains(validSources, src):
				c.bad(sc, field, "unknown value %q", src)
			case slices.Index(e.EvidenceSources, src) != j:
				c.bad(sc, field, "%s is listed twice", src)
			case !slices.Contains(s.Evidence.Sources, src):
				c.badCross(sc, field, "%s is not a route evidence source: evidence_sources can narrow evidence.sources, never widen it", src)
			}
		}
		c.thresholds(sc, at, e.Thresholds, spec)
		if !e.IsEnabled() {
			continue
		}
		for _, x := range spec.Outcomes.RequiresCorroboration {
			if strings.HasPrefix(x, "det:") || attached[x] {
				continue
			}
			c.warn(sc, at+".ref", "%s is corroborated by %s, which this route does not attach", e.Ref, x)
		}
		for _, x := range spec.Outcomes.Implies {
			if !attached[x] {
				c.warn(sc, at+".ref", "%s implies %s, which this route does not attach, so their disagreement is never checked", e.Ref, x)
			}
		}
	}
}

// thresholds checks escalate_from < warn_at <= critical_at after the route overrides are merged
// over the spec (SPEC §2.8 hard rules).
func (c *checker) thresholds(sc scope, at string, t *Thresholds, spec *decision.Spec) {
	dt := t.Decision()
	if dt.Space != decision.SpaceRaw && dt.Space != decision.SpaceCalibrated {
		c.bad(sc, at+".thresholds.space", "unknown value %q (want raw or calibrated)", dt.Space)
	}
	warn, crit, esc := resolvedThresholds(dt, spec)
	for _, v := range []struct {
		name string
		v    *float64
	}{{"warn_at", &warn}, {"critical_at", crit}, {"escalate_from", esc}} {
		if v.v != nil && (*v.v < 0 || *v.v > 1) {
			c.bad(sc, at+".thresholds."+v.name, "%v is outside [0, 1]", *v.v)
		}
	}
	if esc != nil && !(*esc < warn) {
		c.bad(sc, at+".thresholds", "escalate_from %v must be below warn_at %v", *esc, warn)
	}
	if crit != nil && !(warn <= *crit) {
		c.bad(sc, at+".thresholds", "warn_at %v must not exceed critical_at %v", warn, *crit)
	}
}

// resolvedThresholds merges route overrides over the spec: warn_at falls back when absent;
// critical_at and escalate_from fall back only when absent (an explicit null overrides).
func resolvedThresholds(dt decision.Thresholds, spec *decision.Spec) (warn float64, crit, esc *float64) {
	warn = spec.Outcomes.WarnAt
	if dt.WarnAt != nil {
		warn = *dt.WarnAt
	}
	crit = spec.Outcomes.CriticalAt
	if dt.CriticalAt.Set {
		crit = dt.CriticalAt.Value
	}
	esc = spec.Escalation.EscalateFrom
	if dt.EscalateFrom.Set {
		esc = dt.EscalateFrom.Value
	}
	return warn, crit, esc
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
