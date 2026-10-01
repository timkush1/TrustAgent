package decision

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"github.com/truthtable/contracts"
)

// Evaluator validation (SPEC §2.8, P4-PLAN D54): the JSON Schema of evaluator.v1 or
// evaluator-draft.v1, then the hard rules (severity error) and the lints (severity warning).
// LintSpec ports frontend-react/src/lib/decision/lint.ts 1:1, with the same codes, paths and
// messages, and Go is the reference for the lint vectors (contracts/vectors/lint). Built-in
// templates must be lint-clean.
//
// The SPEC names two hard-rule codes (unresolved_state_reference, no_id_reference) and every lint
// id; the other hard rules use the codes defined here. Three lints have no SPEC definition beyond
// their id; their rules are documented where they are checked.

// Lint severities.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Hard-rule codes (severity error).
const (
	CodeUnresolvedStateReference  = "unresolved_state_reference"
	CodeNoIDReference             = "no_id_reference"
	CodeUnknownRiskWeightOption   = "unknown_risk_weight_option"
	CodeRiskLevelOutOfRange       = "risk_level_out_of_range"
	CodeThresholdOrder            = "threshold_order"
	CodeUnknownEvaluatorReference = "unknown_evaluator_reference"
	CodeUnknownDetector           = "unknown_detector"
	// CodeInvalidSpec is a document the spec parser refuses although the schema accepts it.
	CodeInvalidSpec = "invalid_spec"
)

// Lint ids (severity warning), SPEC §2.8.
const (
	LintMissingEscapeOption    = "missing_escape_option"
	LintNonDescriptiveKey      = "non_descriptive_key"
	LintDoubleNegative         = "double_negative"
	LintCountingOrArithmetic   = "counting_or_arithmetic"
	LintNearDuplicateOptions   = "near_duplicate_options"
	LintNumericOnlyScoreLevels = "numeric_only_score_levels"
	LintLongInstructions       = "long_instructions"
	LintScoreUsedForPolicy     = "score_used_for_policy"
	// LintReferenceNotAttached warns that a referenced evaluator is not attached to the route.
	LintReferenceNotAttached = "reference_not_attached"
)

// ErrorCodes lists the hard-rule codes (lint.ts ERROR_CODES).
var ErrorCodes = []string{
	CodeUnresolvedStateReference, CodeNoIDReference, CodeUnknownRiskWeightOption, CodeRiskLevelOutOfRange,
	CodeThresholdOrder, CodeUnknownEvaluatorReference, CodeUnknownDetector,
}

// LintIDs lists the SPEC §2.8 lint ids (lint.ts LINT_CODES).
var LintIDs = []string{
	LintMissingEscapeOption, LintNonDescriptiveKey, LintDoubleNegative, LintCountingOrArithmetic,
	LintNearDuplicateOptions, LintNumericOnlyScoreLevels, LintLongInstructions, LintScoreUsedForPolicy,
}

// Lint is one hard-rule or lint finding (api.ts Lint). Path is a dotted path into the spec
// ("question.criteria.option_a", "question.criteria[2]").
type Lint struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

// FieldError is one JSON Schema finding (api.ts FieldError): a dotted path into the document, ""
// for the document itself, and a message.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// LintOptions are the inputs of the checks that need more than the spec.
type LintOptions struct {
	// Draft validates an evaluator-draft.v1 document (a Studio draft: no id, version, builtin,
	// parent or changelog; plus rationale, split_suggestions and test_samples) instead of an
	// evaluator.v1 spec. The hard rules and lints run on the draft's spec part, and no hashes are
	// computed.
	Draft bool
	// Registry reports whether an evaluator id exists. When set, every requires_corroboration and
	// implies id must exist (error unknown_evaluator_reference).
	Registry func(id string) bool
	// Attached reports whether an evaluator id is attached to the route. When set, a referenced
	// id that exists but is not attached is a warning (reference_not_attached).
	Attached func(id string) bool
	// Thresholds are the route's overrides, merged over the spec before the threshold order check:
	// warn_at falls back when nil; critical_at and escalate_from fall back only when absent (an
	// explicit null overrides). Space is ignored.
	Thresholds *Thresholds
}

// IDSet returns a membership test over ids, for LintOptions.Registry and LintOptions.Attached.
func IDSet(ids ...string) func(string) bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return func(id string) bool { return set[id] }
}

// Validation is the result of ValidateSpec.
type Validation struct {
	// Errors are the JSON Schema findings (and a document that is not JSON).
	Errors []FieldError `json:"errors"`
	// Lints are the hard rules (severity error) followed by the lints (severity warning).
	Lints []Lint `json:"lints"`
	// Spec is the parsed spec, nil when the document does not parse as one. For a draft it
	// carries the placeholder id DraftID and version 1.
	Spec *Spec `json:"-"`
	// Hashes are the spec's identities, nil for a draft and while any error remains.
	Hashes *Hashes `json:"-"`
}

// Valid reports whether the document has no schema error and breaks no hard rule.
func (v *Validation) Valid() bool { return len(v.Errors) == 0 && len(v.HardErrors()) == 0 }

// HardErrors returns the findings of severity error.
func (v *Validation) HardErrors() []Lint { return HardErrors(v.Lints) }

// HardErrors returns the findings of severity error among lints.
func HardErrors(lints []Lint) []Lint {
	var out []Lint
	for _, l := range lints {
		if l.Severity == SeverityError {
			out = append(out, l)
		}
	}
	return out
}

// DraftID is the evaluator id a draft is parsed with (drafts have no id of their own).
const DraftID = "studio_draft"

// draftOnly are the evaluator-draft.v1 members that are not part of the spec.
var draftOnly = []string{"rationale", "split_suggestions", "test_samples"}

// maxSchemaFindings caps the schema findings of one document.
const maxSchemaFindings = 50

// ValidateSpec validates an evaluator document: the JSON Schema (evaluator.v1, or
// evaluator-draft.v1 with opts.Draft), then the hard rules and lints of LintSpec. The lints run
// whenever the document parses as a spec, so a schema error does not hide them. A document that
// passes the schema but not the spec parser is reported as the single hard error invalid_spec.
func ValidateSpec(doc []byte, opts LintOptions) Validation {
	v := Validation{Errors: []FieldError{}, Lints: []Lint{}}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		v.Errors = append(v.Errors, FieldError{Message: "not valid JSON: " + err.Error()})
		return v
	}
	schema, err := evaluatorSchema(opts.Draft)
	if err != nil {
		v.Errors = append(v.Errors, FieldError{Message: err.Error()})
		return v
	}
	if err := schema.Validate(inst); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			v.Errors = schemaFindings(ve, inst)
		}
		if len(v.Errors) == 0 {
			v.Errors = append(v.Errors, FieldError{Message: err.Error()})
		}
	}
	specDoc := doc
	if opts.Draft {
		if specDoc, err = draftSpec(doc); err != nil {
			if len(v.Errors) == 0 {
				v.Errors = append(v.Errors, FieldError{Message: err.Error()})
			}
			return v
		}
	}
	s, err := ParseSpec(specDoc)
	if err != nil {
		if len(v.Errors) == 0 {
			v.Lints = append(v.Lints, Lint{Code: CodeInvalidSpec, Severity: SeverityError, Message: err.Error()})
		}
		return v
	}
	v.Spec = s
	v.Lints = LintSpec(s, opts)
	if !opts.Draft && v.Valid() {
		if h, err := s.Hashes(); err == nil {
			v.Hashes = &h
		}
	}
	return v
}

// draftSpec turns a draft into a spec document: the draft-only members go, and the placeholder
// id and version 1 come in. The other members keep their bytes, so the question's order stays.
func draftSpec(doc []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc, &m); err != nil {
		return nil, fmt.Errorf("evaluator draft: %w", err)
	}
	if m == nil {
		return nil, errors.New("evaluator draft: not an object")
	}
	for _, k := range draftOnly {
		delete(m, k)
	}
	m["id"] = json.RawMessage(strconv.Quote(DraftID))
	m["version"] = json.RawMessage("1")
	return json.Marshal(m)
}

var evaluatorSchemas = sync.OnceValues(func() (map[bool]*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	entries, err := fs.ReadDir(contracts.FS, "schema")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".schema.json") {
			continue
		}
		b, err := fs.ReadFile(contracts.FS, "schema/"+e.Name())
		if err != nil {
			return nil, err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("schema %s: %w", e.Name(), err)
		}
		if err := c.AddResource(contracts.SchemaBaseURI+e.Name(), doc); err != nil {
			return nil, err
		}
	}
	out := map[bool]*jsonschema.Schema{}
	for draft, name := range map[bool]string{false: "evaluator.v1.schema.json", true: "evaluator-draft.v1.schema.json"} {
		if out[draft], err = c.Compile(contracts.SchemaBaseURI + name); err != nil {
			return nil, fmt.Errorf("schema %s: %w", name, err)
		}
	}
	return out, nil
})

func evaluatorSchema(draft bool) (*jsonschema.Schema, error) {
	schemas, err := evaluatorSchemas()
	if err != nil {
		return nil, fmt.Errorf("evaluator schema: %w", err)
	}
	return schemas[draft], nil
}

// schemaFindings flattens a validation error into its leaf findings, deduplicated and capped.
// Inside oneOf and anyOf only the alternatives that fit the value's shape are reported: an
// alternative that fails because the value has another type, or because a discriminating const
// (question.type) differs, says nothing when another alternative fits. When none fits, one
// finding lists what is allowed.
func schemaFindings(root *jsonschema.ValidationError, inst any) []FieldError {
	out := []FieldError{}
	seen := map[FieldError]bool{}
	add := func(loc []string, msg string) {
		f := FieldError{Path: instancePath(inst, loc), Message: msg}
		if !seen[f] && len(out) < maxSchemaFindings {
			seen[f] = true
			out = append(out, f)
		}
	}
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		switch k := e.ErrorKind.(type) {
		case *kind.PropertyNames:
			// The causes validate the key as a value of its own, so their location is empty, and
			// jsonschema v6.0.3 does not copy the object's location (it aliases a buffer that later
			// properties overwrite): the object is found in the instance instead.
			loc := append(keyOwner(inst, k.Property, e.SchemaURL), k.Property)
			for _, l := range leaves(e, nil) {
				switch {
				case l == e:
				case isNot(l):
					add(loc, "invalid key: '"+k.Property+"' is reserved")
				default:
					add(loc, "invalid key: "+leafMessage(l))
				}
			}
			if len(e.Causes) > 0 {
				return
			}
		case *kind.Contains:
			// The causes are each item's mismatch; the finding is about the list.
			add(e.InstanceLocation, containsMessage(e))
			return
		case *kind.OneOf, *kind.AnyOf:
			if len(e.Causes) == 0 {
				break
			}
			var fitting []*jsonschema.ValidationError
			for _, c := range e.Causes {
				if !shapeMismatch(e.InstanceLocation, c) {
					fitting = append(fitting, c)
				}
			}
			if len(fitting) > 0 {
				for _, c := range fitting {
					walk(c)
				}
				return
			}
			if loc, msg, ok := allowedShapes(e); ok {
				add(loc, msg)
				return
			}
		}
		if len(e.Causes) == 0 {
			add(e.InstanceLocation, leafMessage(e))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(root)
	return out
}

// leaves returns the leaf errors under e.
func leaves(e *jsonschema.ValidationError, out []*jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(e.Causes) == 0 {
		return append(out, e)
	}
	for _, c := range e.Causes {
		out = leaves(c, out)
	}
	return out
}

// shapeMismatch reports whether an alternative of a oneOf/anyOf at loc fails on the value's type
// or on a const one level below it.
func shapeMismatch(loc []string, alt *jsonschema.ValidationError) bool {
	for _, l := range leaves(alt, nil) {
		switch l.ErrorKind.(type) {
		case *kind.Type:
			if slices.Equal(l.InstanceLocation, loc) {
				return true
			}
		case *kind.Const:
			if len(l.InstanceLocation) == len(loc)+1 && slices.Equal(l.InstanceLocation[:len(loc)], loc) {
				return true
			}
		}
	}
	return false
}

// allowedShapes summarises a oneOf/anyOf none of whose alternatives fits: the allowed values of
// the discriminating const, or the allowed types.
func allowedShapes(e *jsonschema.ValidationError) ([]string, string, bool) {
	var constLoc []string
	var wants, types []string
	consts, typed := 0, 0
	for _, c := range e.Causes {
		hasConst, hasType := false, false
		for _, l := range leaves(c, nil) {
			switch k := l.ErrorKind.(type) {
			case *kind.Const:
				if !hasConst && len(l.InstanceLocation) == len(e.InstanceLocation)+1 && (constLoc == nil || slices.Equal(constLoc, l.InstanceLocation)) {
					constLoc, hasConst = l.InstanceLocation, true
					if b, err := json.Marshal(k.Want); err == nil {
						wants = append(wants, string(b))
					}
				}
			case *kind.Type:
				if slices.Equal(l.InstanceLocation, e.InstanceLocation) {
					hasType = true
					for _, w := range k.Want {
						if !slices.Contains(types, w) {
							types = append(types, w)
						}
					}
				}
			}
		}
		if hasConst {
			consts++
		}
		if hasType {
			typed++
		}
	}
	switch {
	case consts == len(e.Causes) && len(wants) > 0:
		return constLoc, "must be one of " + strings.Join(wants, ", "), true
	case typed == len(e.Causes) && len(types) > 0:
		return e.InstanceLocation, "must be " + strings.Join(types, " or "), true
	}
	return nil, "", false
}

// keyOwner returns the location of the object that has the key: the one the schema location
// names ("…#/properties/question/oneOf/1/properties/criteria/propertyNames" names
// question.criteria) when it has the key, otherwise the first such object in key order.
func keyOwner(inst any, key, schemaURL string) []string {
	var owners [][]string
	var find func(v any, loc []string)
	find = func(v any, loc []string) {
		switch x := v.(type) {
		case map[string]any:
			if _, ok := x[key]; ok {
				owners = append(owners, slices.Clone(loc))
			}
			names := slices.Sorted(maps.Keys(x))
			for _, k := range names {
				find(x[k], append(loc, k))
			}
		case []any:
			for i, e := range x {
				find(e, append(loc, strconv.Itoa(i)))
			}
		}
	}
	find(inst, nil)
	var named []string
	if _, frag, ok := strings.Cut(schemaURL, "#"); ok {
		toks := strings.Split(strings.TrimPrefix(frag, "/"), "/")
		for i := 0; i+1 < len(toks); i++ {
			if toks[i] == "properties" {
				named = append(named, toks[i+1])
				i++
			}
		}
	}
	for _, o := range owners {
		if slices.Equal(o, named) {
			return o
		}
	}
	if len(owners) > 0 {
		return owners[0]
	}
	return nil
}

func isNot(e *jsonschema.ValidationError) bool {
	_, ok := e.ErrorKind.(*kind.Not)
	return ok
}

// containsMessage names the value a list must include when every item failed the same const.
func containsMessage(e *jsonschema.ValidationError) string {
	var want []byte
	for _, l := range leaves(e, nil) {
		c, ok := l.ErrorKind.(*kind.Const)
		if !ok {
			return "must include an item the schema requires"
		}
		b, err := json.Marshal(c.Want)
		if err != nil || (want != nil && !bytes.Equal(b, want)) {
			return "must include an item the schema requires"
		}
		want = b
	}
	if want == nil {
		return "must include an item the schema requires"
	}
	return "must include " + string(want)
}

func leafMessage(e *jsonschema.ValidationError) string {
	if isNot(e) {
		return "is not allowed together with the rest of the spec"
	}
	if out := e.BasicOutput(); out != nil && out.Error != nil {
		return out.Error.String()
	}
	return e.Error()
}

// instancePath renders a JSON Pointer's tokens as a dotted path, with [i] for array items.
func instancePath(inst any, loc []string) string {
	var b strings.Builder
	cur := inst
	for _, tok := range loc {
		if arr, ok := cur.([]any); ok {
			b.WriteString("[" + tok + "]")
			cur = nil
			if i, err := strconv.Atoi(tok); err == nil && i >= 0 && i < len(arr) {
				cur = arr[i]
			}
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(tok)
		if m, ok := cur.(map[string]any); ok {
			cur = m[tok]
		} else {
			cur = nil
		}
	}
	return b.String()
}

const (
	maxInstructions = 600
	// jsDot is JavaScript's "." without the s flag: any character but a line terminator.
	jsDot = `[^\n\r\x{2028}\x{2029}]`
)

var (
	escapeKey = regexp.MustCompile(`^(no_|nothing_|not_|too_short|too_generic)`)
	// escapeWhat is lint.ts ESCAPE_WHAT without its i flag; it runs over asciiLower(text), which
	// is what a non-unicode JavaScript /i does for an all-ASCII pattern.
	escapeWhat     = regexp.MustCompile(`greeting|too short|nothing to|not about|no ` + jsDot + `*(subject|facts|content)`)
	doubleNegative = regexp.MustCompile(`\bnot\b` + jsDot + `*\b(no|not|never|without)\b`)
	counting       = regexp.MustCompile(`more than|at least|fewer than|how many|[0-9]`)
	questionIDRef  = regexp.MustCompile(`\b[a-z][a-z0-9_]{1,47}__v[0-9]+\b`)
	backticked     = regexp.MustCompile("`([^`]*)`")
	keySeparators  = regexp.MustCompile(`[_0-9]+`)
	anyLetter      = regexp.MustCompile(`\pL`)
)

// genericWords say nothing about an option on their own.
var genericWords = map[string]bool{
	"option": true, "choice": true, "answer": true, "item": true, "value": true, "class": true, "category": true,
	"label": true, "type": true, "case": true, "other": true, "misc": true, "level": true, "result": true,
}

// textAt is one string of a text tree and its path.
type textAt struct{ path, text string }

// texts lists every string of a text tree (strings, arrays and objects) in document order.
func texts(v any, path string, out []textAt) []textAt {
	switch x := v.(type) {
	case string:
		out = append(out, textAt{path, x})
	case []any:
		for i, e := range x {
			out = texts(e, path+"["+strconv.Itoa(i)+"]", out)
		}
	case *jsObject:
		for _, k := range x.entries() {
			out = texts(x.vals[k], path+"."+k, out)
		}
	}
	return out
}

func joinTexts(ts []textAt, sep string) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = t.text
	}
	return strings.Join(parts, sep)
}

// optionWhat is the description of a choice option: its what, or the option text itself.
func optionWhat(option any) string {
	if o, ok := option.(*jsObject); ok {
		if w, ok := o.vals["what"].(string); ok {
			return w
		}
	}
	return joinTexts(texts(option, "", nil), " ")
}

func hasNotFor(option any) bool {
	o, ok := option.(*jsObject)
	if !ok {
		return false
	}
	s, ok := o.vals["not_for"].(string)
	return ok && s != ""
}

// lowerTokens returns the [a-z0-9]+ tokens of s.toLowerCase(), as JavaScript computes them. Only
// two non-ASCII characters lowercase to ASCII letters: KELVIN SIGN to "k", and LATIN CAPITAL
// LETTER I WITH DOT ABOVE to "i" followed by a combining dot, which ends the token.
func lowerTokens(s string) map[string]bool {
	out := map[string]bool{}
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			out[string(cur)] = true
			cur = cur[:0]
		}
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			cur = append(cur, byte(r-'A'+'a'))
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur = append(cur, byte(r))
		case r == 'K':
			cur = append(cur, 'k')
		case r == 'İ':
			cur = append(cur, 'i')
			flush()
		default:
			flush()
		}
	}
	flush()
	return out
}

func jaccard(a, b map[string]bool) float64 {
	common := 0
	for t := range a {
		if b[t] {
			common++
		}
	}
	union := len(a) + len(b) - common
	if union == 0 {
		return 0
	}
	return float64(common) / float64(union)
}

// asciiLower lowercases A-Z only.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// utf16Len is JavaScript's String.prototype.length.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}

// jsNumber formats f as JavaScript's String(f) does.
func jsNumber(f float64) string {
	var b bytes.Buffer
	if err := writeCanonicalNumber(&b, f); err != nil {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return b.String()
}

// member returns a member of the spec as written, in JavaScript property order.
func (s *Spec) member(key string) any {
	if s.ordered == nil {
		return nil
	}
	v, _ := s.ordered.get(key)
	return v
}

// LintSpec runs the hard rules and the lints on a parsed spec: errors first, then warnings, each
// in the order lint.ts finds them.
func LintSpec(s *Spec, opts LintOptions) []Lint {
	errs, warns := []Lint{}, []Lint{}
	fail := func(code, path, msg string) {
		errs = append(errs, Lint{Code: code, Severity: SeverityError, Path: path, Message: msg})
	}
	warn := func(code, path, msg string) {
		warns = append(warns, Lint{Code: code, Severity: SeverityWarning, Path: path, Message: msg})
	}

	var instructions, criteria any
	if q, ok := s.member("question").(*jsObject); ok {
		instructions, _ = q.get("instructions")
		criteria, _ = q.get("criteria")
	}
	instructionTexts := texts(instructions, "question.instructions", nil)
	questionTexts := texts(criteria, "question.criteria", slices.Clone(instructionTexts))

	// Hard rule: backticked references resolve to a declared state field or to rules.<param>.
	for _, t := range questionTexts {
		for _, m := range backticked.FindAllStringSubmatch(t.text, -1) {
			ref := m[1]
			var ok bool
			if param, isRule := strings.CutPrefix(ref, "rules."); isRule {
				_, declared := s.Params[param]
				ok = s.HasStateField(FieldRules) && declared
			} else {
				ok = s.HasStateField(ref)
			}
			if !ok {
				fail(CodeUnresolvedStateReference, t.path, "`"+ref+"` is neither a declared state field nor rules.<declared param>")
			}
		}
	}
	// Hard rule: question ids never appear in text (questions are independent).
	for _, t := range questionTexts {
		if id := questionIDRef.FindString(t.text); id != "" {
			fail(CodeNoIDReference, t.path, "mentions the question id "+id+"; questions are answered independently")
		}
	}

	options, _ := criteria.(*jsObject)
	var optionKeys []string
	if s.Question.Type == QuestionChoice && options != nil {
		optionKeys = options.entries()
	}
	if s.Signal.RiskWeights != nil && s.Question.Type == QuestionChoice {
		for _, k := range riskWeightKeys(s) {
			if !slices.Contains(optionKeys, k) {
				fail(CodeUnknownRiskWeightOption, "signal.risk_weights."+k, k+" is not an option of the question")
			}
		}
	}
	if levels, ok := criteria.([]any); ok && s.Signal.RiskLevels != nil && s.Question.Type == QuestionScore {
		for _, l := range s.Signal.RiskLevels {
			if l >= len(levels) {
				fail(CodeRiskLevelOutOfRange, "signal.risk_levels",
					fmt.Sprintf("level %d does not exist: the question has %d levels", l, len(levels)))
			}
		}
	}

	// Hard rule: escalate_from < warn_at <= critical_at, after the route overrides are merged.
	warnAt, criticalAt, escalateFrom := s.Outcomes.WarnAt, s.Outcomes.CriticalAt, s.Escalation.EscalateFrom
	if th := opts.Thresholds; th != nil {
		if th.WarnAt != nil {
			warnAt = *th.WarnAt
		}
		if th.CriticalAt.Set {
			criticalAt = th.CriticalAt.Value
		}
		if th.EscalateFrom.Set {
			escalateFrom = th.EscalateFrom.Value
		}
	}
	if escalateFrom != nil && !(*escalateFrom < warnAt-EPS) {
		fail(CodeThresholdOrder, "escalation.escalate_from",
			"escalate_from "+jsNumber(*escalateFrom)+" must be below warn_at "+jsNumber(warnAt))
	}
	if criticalAt != nil && !(warnAt <= *criticalAt+EPS) {
		fail(CodeThresholdOrder, "outcomes.critical_at",
			"critical_at "+jsNumber(*criticalAt)+" must be at least warn_at "+jsNumber(warnAt))
	}

	// Hard rule: referenced evaluators exist; a warning when the route does not attach them.
	for _, ref := range []struct {
		path string
		ids  []string
	}{
		{"outcomes.requires_corroboration", s.Outcomes.RequiresCorroboration},
		{"outcomes.implies", s.Outcomes.Implies},
	} {
		for _, x := range ref.ids {
			if det, ok := strings.CutPrefix(x, "det:"); ok {
				if !slices.Contains(DetectorIDs, det) {
					fail(CodeUnknownDetector, ref.path, x+" is not a detector")
				}
				continue
			}
			if opts.Registry != nil && !opts.Registry(x) {
				fail(CodeUnknownEvaluatorReference, ref.path, x+" is not a known evaluator")
			} else if opts.Attached != nil && !opts.Attached(x) {
				warn(LintReferenceNotAttached, ref.path, x+" is not attached to this route, so it can never agree")
			}
		}
	}

	// Lints.
	if s.Question.Type == QuestionChoice && options != nil {
		escape := false
		for _, k := range optionKeys {
			if escapeKey.MatchString(k) || escapeWhat.MatchString(asciiLower(optionWhat(options.vals[k]))) {
				escape = true
				break
			}
		}
		if len(optionKeys) >= 3 && !escape {
			warn(LintMissingEscapeOption, "question.criteria", "add an option for answers with nothing to judge (a greeting, a very short reply)")
		}
		// non_descriptive_key: no word of three or more letters besides generic ones (option_a, choice_2).
		for _, k := range optionKeys {
			descriptive := false
			for _, w := range keySeparators.Split(k, -1) {
				if w != "" && utf16Len(w) >= 3 && !genericWords[w] {
					descriptive = true
					break
				}
			}
			if !descriptive {
				warn(LintNonDescriptiveKey, "question.criteria."+k, k+" does not say what the option means; the key is part of the question")
			}
		}
		for i := 0; i < len(optionKeys); i++ {
			for j := i + 1; j < len(optionKeys); j++ {
				ka, kb := optionKeys[i], optionKeys[j]
				a, b := options.vals[ka], options.vals[kb]
				if hasNotFor(a) || hasNotFor(b) {
					continue
				}
				if jaccard(lowerTokens(optionWhat(a)), lowerTokens(optionWhat(b))) > 0.5 {
					warn(LintNearDuplicateOptions, "question.criteria."+kb, ka+" and "+kb+" are described almost the same way; add not_for to separate them")
				}
			}
		}
	}
	for _, t := range instructionTexts {
		if doubleNegative.MatchString(t.text) {
			warn(LintDoubleNegative, t.path, "rephrase without a double negative")
		}
		if counting.MatchString(t.text) {
			warn(LintCountingOrArithmetic, t.path, "the decision model is weak at counting and arithmetic; consider escalation.numeric_or_date")
		}
	}
	if utf8.RuneCountInString(joinTexts(instructionTexts, "")) > maxInstructions {
		warn(LintLongInstructions, "question.instructions",
			"instructions over "+strconv.Itoa(maxInstructions)+" characters; move detail into the options")
	}
	if s.Question.Type == QuestionScore {
		// numeric_only_score_levels: a level described by digits and punctuation only.
		if levels, ok := criteria.([]any); ok {
			for i, level := range levels {
				if !anyLetter.MatchString(joinTexts(texts(level, "", nil), " ")) {
					warn(LintNumericOnlyScoreLevels, "question.criteria["+strconv.Itoa(i)+"]", "describe each level in words, not only a number")
				}
			}
		}
		// score_used_for_policy: every attached evaluator drives policy, and score answers are poorly calibrated.
		warn(LintScoreUsedForPolicy, "question.type", "score answers are poorly calibrated; prefer noul or choice for policy decisions")
	}
	return append(errs, warns...)
}

// riskWeightKeys returns the keys of signal.risk_weights in document order.
func riskWeightKeys(s *Spec) []string {
	if sig, ok := s.member("signal").(*jsObject); ok {
		if rw, ok := sig.vals["risk_weights"].(*jsObject); ok {
			return rw.entries()
		}
	}
	keys := make([]string, 0, len(s.Signal.RiskWeights))
	for k := range s.Signal.RiskWeights {
		keys = append(keys, k)
	}
	SortUTF16(keys)
	return keys
}

// LintCodes returns the distinct codes of the error and of the warning findings, each sorted: the
// shape of a lint vector's expected value (lint.ts lintCodes).
func LintCodes(lints []Lint) (errorCodes, warningCodes []string) {
	codes := func(severity string) []string {
		out := []string{}
		for _, l := range lints {
			if l.Severity == severity && !slices.Contains(out, l.Code) {
				out = append(out, l.Code)
			}
		}
		slices.Sort(out)
		return out
	}
	return codes(SeverityError), codes(SeverityWarning)
}
