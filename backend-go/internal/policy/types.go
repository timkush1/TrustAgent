package policy

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evidence"
)

// Settings are a route's effective settings (policy/v1 RouteSettings) after merging the route over
// defaults over the built-in defaults. Field names and tags follow
// contracts/schema/policy.v1.schema.json.
type Settings struct {
	Mode               string            `json:"mode" yaml:"mode"`
	Fail               string            `json:"fail" yaml:"fail"`
	DeadlineMs         int               `json:"deadline_ms" yaml:"deadline_ms"`
	HedgeAfterMs       int               `json:"hedge_after_ms" yaml:"hedge_after_ms"`
	SampleRate         float64           `json:"sample_rate" yaml:"sample_rate"`
	RequireCalibration bool              `json:"require_calibration" yaml:"require_calibration"`
	OnNoEvidence       string            `json:"on_no_evidence" yaml:"on_no_evidence"`
	Streaming          string            `json:"streaming" yaml:"streaming"`
	StreamAnnotations  string            `json:"stream_annotations" yaml:"stream_annotations"`
	BufferKeepaliveS   int               `json:"buffer_keepalive_s" yaml:"buffer_keepalive_s"`
	Headers            string            `json:"headers" yaml:"headers"`
	Evidence           EvidenceSettings  `json:"evidence" yaml:"evidence"`
	Evaluators         []EvaluatorRef    `json:"evaluators" yaml:"evaluators"`
	Detectors          DetectorSettings  `json:"detectors" yaml:"detectors"`
	Decision           DecisionSettings  `json:"decision" yaml:"decision"`
	DeepAudit          DeepAuditSettings `json:"deep_audit" yaml:"deep_audit"`
	Block              BlockSettings     `json:"block" yaml:"block"`
	Fallback           FallbackSettings  `json:"fallback" yaml:"fallback"`
	StoreBodies        string            `json:"store_bodies" yaml:"store_bodies"`
	BodySampleRatePass float64           `json:"body_sample_rate_pass" yaml:"body_sample_rate_pass"`
}

// EvidenceSettings is RouteSettings.evidence.
type EvidenceSettings struct {
	Sources        []string              `json:"sources" yaml:"sources"`
	SystemMode     string                `json:"system_mode" yaml:"system_mode"`
	LineHeaders    bool                  `json:"line_headers" yaml:"line_headers"`
	TagPatterns    []evidence.TagPattern `json:"tag_patterns" yaml:"tag_patterns"`
	MaxTokens      int                   `json:"max_tokens" yaml:"max_tokens"`
	UntrustedTools []string              `json:"untrusted_tools" yaml:"untrusted_tools"`
	Knowledge      KnowledgeSettings     `json:"knowledge" yaml:"knowledge"`
}

// Extraction returns the settings evidence extraction reads.
func (e EvidenceSettings) Extraction() evidence.Settings {
	return evidence.Settings{
		Sources: e.Sources, SystemMode: e.SystemMode, LineHeaders: e.LineHeaders,
		TagPatterns: e.TagPatterns, UntrustedTools: e.UntrustedTools,
	}
}

// KnowledgeSettings is evidence.knowledge (the knowledge add-on, not available in S0).
type KnowledgeSettings struct {
	When       string  `json:"when" yaml:"when"`
	TopK       int     `json:"top_k" yaml:"top_k"`
	DeadlineMs int     `json:"deadline_ms" yaml:"deadline_ms"`
	MinScore   float64 `json:"min_score" yaml:"min_score"`
}

// EvaluatorRef is a route's reference to an evaluator (RouteEvaluatorRef).
type EvaluatorRef struct {
	Ref             string            `json:"ref" yaml:"ref"`
	Stage           string            `json:"stage,omitempty" yaml:"stage,omitempty"`
	Params          map[string]string `json:"params,omitempty" yaml:"params,omitempty"`
	Thresholds      *Thresholds       `json:"thresholds,omitempty" yaml:"thresholds,omitempty"`
	Enabled         *bool             `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	EvidenceSources []string          `json:"evidence_sources,omitempty" yaml:"evidence_sources,omitempty"`
}

// IsEnabled reports enabled (default true).
func (e EvaluatorRef) IsEnabled() bool { return e.Enabled == nil || *e.Enabled }

// StageOrDefault returns the stage, active when unset (the YAML default).
func (e EvaluatorRef) StageOrDefault() string {
	if e.Stage == "" {
		return decision.StageActive
	}
	return e.Stage
}

// Thresholds are the route's threshold overrides for an evaluator. critical_at and escalate_from
// distinguish an explicit null (it overrides the spec value) from an absent key.
type Thresholds struct {
	Space        string   `json:"space,omitempty" yaml:"space,omitempty"`
	WarnAt       *float64 `json:"warn_at,omitempty" yaml:"warn_at,omitempty"`
	CriticalAt   OptFloat `json:"critical_at,omitzero" yaml:"critical_at,omitempty"`
	EscalateFrom OptFloat `json:"escalate_from,omitzero" yaml:"escalate_from,omitempty"`
}

// Decision returns the decide() form of the overrides (raw space when unset).
func (t *Thresholds) Decision() decision.Thresholds {
	if t == nil {
		return decision.Thresholds{Space: decision.SpaceRaw}
	}
	out := decision.Thresholds{Space: t.Space, WarnAt: t.WarnAt, CriticalAt: t.CriticalAt.Decision(), EscalateFrom: t.EscalateFrom.Decision()}
	if out.Space == "" {
		out.Space = decision.SpaceRaw
	}
	return out
}

// OptFloat is a number that may be absent (Set false), null (Set true, Value nil) or set.
type OptFloat struct {
	Set   bool
	Value *float64
}

// Decision converts to decision.OptFloat.
func (o OptFloat) Decision() decision.OptFloat {
	return decision.OptFloat{Set: o.Set, Value: o.Value}
}

// IsZero reports an absent value (omitzero).
func (o OptFloat) IsZero() bool { return !o.Set }

// MarshalJSON implements json.Marshaler.
func (o OptFloat) MarshalJSON() ([]byte, error) { return json.Marshal(o.Value) }

// UnmarshalJSON implements json.Unmarshaler; it runs only when the key is present.
func (o *OptFloat) UnmarshalJSON(b []byte) error {
	o.Set, o.Value = true, nil
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// UnmarshalYAML implements yaml.Unmarshaler for the typed validation pass (yaml.v3 does not call
// it for null, which the merge pass handles).
func (o *OptFloat) UnmarshalYAML(n *yaml.Node) error {
	o.Set, o.Value = true, nil
	var v float64
	if err := n.Decode(&v); err != nil {
		return fmt.Errorf("line %d: want a number or null", n.Line)
	}
	o.Value = &v
	return nil
}

// DetectorSettings is RouteSettings.detectors.
type DetectorSettings struct {
	FlagOn     []string `json:"flag_on" yaml:"flag_on"`
	BlockOn    []string `json:"block_on" yaml:"block_on"`
	MinInteger int      `json:"min_integer" yaml:"min_integer"`
}

// DecisionSettings is RouteSettings.decision.
type DecisionSettings struct {
	// Model pins the decision model; empty means TRUTHTABLE_JEV_MODEL.
	Model             string `json:"model,omitempty" yaml:"model,omitempty"`
	MaxResponseTokens int    `json:"max_response_tokens" yaml:"max_response_tokens"`
}

// DeepAuditSettings is RouteSettings.deep_audit.
type DeepAuditSettings struct {
	Enabled           bool    `json:"enabled" yaml:"enabled"`
	PassSampleRate    float64 `json:"pass_sample_rate" yaml:"pass_sample_rate"`
	UniformSampleRate float64 `json:"uniform_sample_rate" yaml:"uniform_sample_rate"`
	ErrorSampleRate   float64 `json:"error_sample_rate" yaml:"error_sample_rate"`
	Verifier          string  `json:"verifier" yaml:"verifier"`
}

// BlockSettings is RouteSettings.block (enforced from P3).
type BlockSettings struct {
	Style               string `json:"style" yaml:"style"`
	Message             string `json:"message" yaml:"message"`
	Status              int    `json:"status" yaml:"status"`
	AnthropicStopReason string `json:"anthropic_stop_reason" yaml:"anthropic_stop_reason"`
}

// FallbackSettings is RouteSettings.fallback (enforced from P3).
type FallbackSettings struct {
	Upstream     string `json:"upstream" yaml:"upstream"`
	Model        string `json:"model" yaml:"model"`
	SystemPrompt string `json:"system_prompt" yaml:"system_prompt"`
	OnFail       string `json:"on_fail" yaml:"on_fail"`
}

// Match is a route's match block: every matcher given must match.
type Match struct {
	Headers   map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	Paths     []string          `json:"paths,omitempty" yaml:"paths,omitempty"`
	Models    []string          `json:"models,omitempty" yaml:"models,omitempty"`
	KeyLabels []string          `json:"key_labels,omitempty" yaml:"key_labels,omitempty"`
}

// Overrides is the policy's overrides block.
type Overrides struct {
	ClientMode  string `json:"client_mode" yaml:"client_mode"`
	ClientRules bool   `json:"client_rules" yaml:"client_rules"`
}

// Client mode overrides (overrides.client_mode).
const (
	ClientModeNone         = "none"
	ClientModeStricterOnly = "stricter_only"
	ClientModeAny          = "any"
)

// Route is a matched route with its effective settings.
type Route struct {
	// Name is the route name; "default" for the defaults route.
	Name        string
	Description string
	Match       Match
	Settings
}

// DefaultRouteName is the name of the route that applies when no route matches.
const DefaultRouteName = "default"

// docYAML is the typed shape of the file, decoded with KnownFields for line-numbered errors.
type docYAML struct {
	Version   *int        `yaml:"version"`
	Defaults  *Settings   `yaml:"defaults"`
	Overrides *Overrides  `yaml:"overrides"`
	Routes    []routeYAML `yaml:"routes"`
}

// routeYAML is one routes[] entry: name, description and match, plus any RouteSettings field.
type routeYAML struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Match       *Match `yaml:"match"`
	Settings    `yaml:",inline"`
}
