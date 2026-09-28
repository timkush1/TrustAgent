package decision

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Question types (SPEC §2.8).
const (
	QuestionNoul   = "noul"
	QuestionChoice = "choice"
	QuestionScore  = "score"
)

// Verdict roles (SPEC §2.8 verdict_role).
const (
	RoleUnsupported  = "unsupported"
	RoleContradicted = "contradicted"
)

// Evidence requirements (SPEC §2.8 evidence.requires).
const (
	EvidenceRequired = "required"
	EvidenceOptional = "optional"
	EvidenceNone     = "none"
)

// State fields (SPEC §2.8 state_fields).
const (
	FieldUserRequest        = "user_request"
	FieldAssistantResponse  = "assistant_response"
	FieldEvidence           = "evidence"
	FieldSystemInstructions = "system_instructions"
	FieldRules              = "rules"
)

// defaultMinChars is the schema default of evidence.min_chars.
const defaultMinChars = 40

// Spec is an evaluator spec (contracts/schema/evaluator.v1.schema.json).
//
// The typed fields are what compile() and decide() read; schema defaults are applied to them.
// The spec as written is kept as well: hashes, the question sent to the decision model and
// MarshalJSON all use it, so a spec round-trips unchanged and hashes never depend on defaults.
type Spec struct {
	ID          string
	Version     int
	Name        string
	Category    string
	VerdictRole string // "", RoleUnsupported or RoleContradicted
	StateFields []string
	Params      map[string]Param
	Question    Question
	Evidence    EvidenceRequirement
	Signal      Signal
	Outcomes    Outcomes
	Escalation  Escalation

	raw map[string]any
}

// Param is one declared rules parameter.
type Param struct {
	Default string `json:"default"`
	Label   string `json:"label,omitempty"`
	Help    string `json:"help,omitempty"`
}

// Question is the decision-model question. Instructions and Criteria are text trees (strings,
// arrays and objects).
type Question struct {
	Type         string
	Instructions any
	Criteria     any
}

// EvidenceRequirement is spec.evidence with the min_chars default applied.
type EvidenceRequirement struct {
	Requires string
	MinChars int
}

// Signal is spec.signal: how an answer becomes a risk in [0,1] (high = risky).
type Signal struct {
	Invert      bool
	RiskWeights map[string]float64
	RiskLevels  []int
}

// Outcomes is spec.outcomes with defaults applied.
type Outcomes struct {
	WarnAt                float64
	CriticalAt            *float64
	MinConfidence         *float64
	BlockEligible         bool
	RequiresCorroboration []string
	Implies               []string
}

// Escalation is spec.escalation with defaults applied (on_error defaults to escalate).
type Escalation struct {
	EscalateFrom  *float64
	NumericOrDate bool
	OnError       string
}

// wireSpec is the typed view of the JSON document.
type wireSpec struct {
	ID          string           `json:"id"`
	Version     int              `json:"version"`
	Name        string           `json:"name"`
	Category    string           `json:"category"`
	VerdictRole *string          `json:"verdict_role"`
	StateFields []string         `json:"state_fields"`
	Params      map[string]Param `json:"params"`
	Question    struct {
		Type         string `json:"type"`
		Instructions any    `json:"instructions"`
		Criteria     any    `json:"criteria"`
	} `json:"question"`
	Evidence struct {
		Requires string `json:"requires"`
		MinChars *int   `json:"min_chars"`
	} `json:"evidence"`
	Signal struct {
		Invert      bool               `json:"invert"`
		RiskWeights map[string]float64 `json:"risk_weights"`
		RiskLevels  []int              `json:"risk_levels"`
	} `json:"signal"`
	Outcomes struct {
		WarnAt                *float64 `json:"warn_at"`
		CriticalAt            *float64 `json:"critical_at"`
		MinConfidence         *float64 `json:"min_confidence"`
		BlockEligible         bool     `json:"block_eligible"`
		RequiresCorroboration []string `json:"requires_corroboration"`
		Implies               []string `json:"implies"`
	} `json:"outcomes"`
	Escalation *struct {
		EscalateFrom  *float64 `json:"escalate_from"`
		NumericOrDate bool     `json:"numeric_or_date"`
		OnError       string   `json:"on_error"`
	} `json:"escalation"`
}

var evaluatorIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,47}$`)

// ParseSpec parses an evaluator spec document. It checks what compile() and decide() rely on;
// the full schema, hard rules and lints belong to validation.
func ParseSpec(doc []byte) (*Spec, error) {
	s := new(Spec)
	if err := json.Unmarshal(doc, s); err != nil {
		return nil, err
	}
	return s, nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (s *Spec) UnmarshalJSON(doc []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(doc, &raw); err != nil {
		return fmt.Errorf("evaluator spec: %w", err)
	}
	var w wireSpec
	if err := json.Unmarshal(doc, &w); err != nil {
		return fmt.Errorf("evaluator spec: %w", err)
	}
	if !evaluatorIDPattern.MatchString(w.ID) {
		return fmt.Errorf("evaluator spec: invalid id %q", w.ID)
	}
	if w.Version < 1 {
		return fmt.Errorf("evaluator spec %s: version must be >= 1", w.ID)
	}
	if w.Outcomes.WarnAt == nil {
		return fmt.Errorf("evaluator spec %s: outcomes.warn_at is required", w.ID)
	}
	out := Spec{
		ID:          w.ID,
		Version:     w.Version,
		Name:        w.Name,
		Category:    w.Category,
		StateFields: w.StateFields,
		Params:      w.Params,
		Question: Question{
			Type:         w.Question.Type,
			Instructions: w.Question.Instructions,
			Criteria:     w.Question.Criteria,
		},
		Evidence: EvidenceRequirement{Requires: w.Evidence.Requires, MinChars: defaultMinChars},
		Signal: Signal{
			Invert:      w.Signal.Invert,
			RiskWeights: w.Signal.RiskWeights,
			RiskLevels:  w.Signal.RiskLevels,
		},
		Outcomes: Outcomes{
			WarnAt:                *w.Outcomes.WarnAt,
			CriticalAt:            w.Outcomes.CriticalAt,
			MinConfidence:         w.Outcomes.MinConfidence,
			BlockEligible:         w.Outcomes.BlockEligible,
			RequiresCorroboration: w.Outcomes.RequiresCorroboration,
			Implies:               w.Outcomes.Implies,
		},
		Escalation: Escalation{OnError: OnErrorEscalate},
		raw:        raw,
	}
	if w.VerdictRole != nil {
		out.VerdictRole = *w.VerdictRole
	}
	if w.Evidence.MinChars != nil {
		out.Evidence.MinChars = *w.Evidence.MinChars
	}
	if w.Escalation != nil {
		out.Escalation.EscalateFrom = w.Escalation.EscalateFrom
		out.Escalation.NumericOrDate = w.Escalation.NumericOrDate
		if w.Escalation.OnError != "" {
			out.Escalation.OnError = w.Escalation.OnError
		}
	}
	switch out.Question.Type {
	case QuestionNoul:
	case QuestionChoice:
		if _, ok := out.Question.Criteria.(map[string]any); !ok {
			return fmt.Errorf("evaluator spec %s: choice criteria must be an object", w.ID)
		}
	case QuestionScore:
		if _, ok := out.Question.Criteria.([]any); !ok {
			return fmt.Errorf("evaluator spec %s: score criteria must be an array", w.ID)
		}
	default:
		return fmt.Errorf("evaluator spec %s: unknown question type %q", w.ID, out.Question.Type)
	}
	switch out.Evidence.Requires {
	case EvidenceRequired, EvidenceOptional, EvidenceNone:
	default:
		return fmt.Errorf("evaluator spec %s: unknown evidence.requires %q", w.ID, out.Evidence.Requires)
	}
	switch out.Escalation.OnError {
	case OnErrorEscalate, OnErrorIgnore:
	default:
		return fmt.Errorf("evaluator spec %s: unknown escalation.on_error %q", w.ID, out.Escalation.OnError)
	}
	*s = out
	return nil
}

// MarshalJSON implements json.Marshaler: the spec as written.
func (s *Spec) MarshalJSON() ([]byte, error) {
	if s.raw == nil {
		return nil, errors.New("evaluator spec: not parsed")
	}
	return json.Marshal(s.raw)
}

// Ref returns "<id>@<version>".
func (s *Spec) Ref() string { return s.ID + "@" + strconv.Itoa(s.Version) }

// QuestionID returns the question id "<id>__v<version>" (SPEC §2.9 compile step 5).
func (s *Spec) QuestionID() string { return s.ID + "__v" + strconv.Itoa(s.Version) }

// QuestionBody returns the question exactly as the spec writes it: {type, instructions, criteria}.
func (s *Spec) QuestionBody() any { return s.raw["question"] }

// HasStateField reports whether the spec lists field in state_fields.
func (s *Spec) HasStateField(field string) bool {
	for _, f := range s.StateFields {
		if f == field {
			return true
		}
	}
	return false
}

// Options returns the answer keys of the question: the option keys of a choice question in
// UTF-16 order, "0".."n-1" for a score question with n levels, and nil for noul.
func (s *Spec) Options() []string {
	switch s.Question.Type {
	case QuestionChoice:
		m, _ := s.Question.Criteria.(map[string]any)
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		SortUTF16(keys)
		return keys
	case QuestionScore:
		levels, _ := s.Question.Criteria.([]any)
		keys := make([]string, len(levels))
		for i := range levels {
			keys[i] = strconv.Itoa(i)
		}
		return keys
	}
	return nil
}

// Hashes are the identities of an evaluator version (SPEC §2.3 evaluator_versions, §2.11).
type Hashes struct {
	// SpecSHA256 is sha256(JCS(spec)).
	SpecSHA256 string `json:"spec_sha256"`
	// QuestionSHA256 is sha256(JCS(spec.question)).
	QuestionSHA256 string `json:"question_sha256"`
	// CalibrationKey is sha256(JCS({question, signal})), the calibration target key.
	CalibrationKey string `json:"calibration_key"`
}

// Hashes computes the spec's hashes over the spec as written (defaults are not filled in).
func (s *Spec) Hashes() (Hashes, error) {
	var h Hashes
	var err error
	if h.SpecSHA256, err = SHA256Hex(s.raw); err != nil {
		return h, err
	}
	if h.QuestionSHA256, err = SHA256Hex(s.raw["question"]); err != nil {
		return h, err
	}
	h.CalibrationKey, err = SHA256Hex(map[string]any{"question": s.raw["question"], "signal": s.raw["signal"]})
	return h, err
}

// ParseRef splits "<id>@<version>" into its parts.
func ParseRef(ref string) (id string, version int, err error) {
	id, v, ok := strings.Cut(ref, "@")
	if !ok || !evaluatorIDPattern.MatchString(id) {
		return "", 0, fmt.Errorf("invalid evaluator ref %q", ref)
	}
	version, err = strconv.Atoi(v)
	if err != nil || version < 1 || strconv.Itoa(version) != v {
		return "", 0, fmt.Errorf("invalid evaluator ref %q: the version must be a positive integer", ref)
	}
	return id, version, nil
}
