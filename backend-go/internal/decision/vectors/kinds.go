package vectors

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strconv"

	"github.com/truthtable/backend-go/internal/decision"
)

// Kind describes how the Go reference handles one vector kind.
type Kind struct {
	// Mechanical kinds have expected values that cmd/vectorgen generates from the reference.
	Mechanical bool
	// Skip, when set, is why the Go runner does not run this kind (yet).
	Skip string
	run  func(in json.RawMessage, contracts fs.FS) (any, error)
}

// Kinds lists every kind of contracts/schema/vector.v1.schema.json. A vector of any other kind
// fails every runner.
var Kinds = map[string]Kind{
	"canonical":         {Mechanical: true, run: runCanonical},
	"tokens":            {Mechanical: true, run: runTokens},
	"question_hash":     {Mechanical: true, run: runQuestionHash},
	"normalize":         {Mechanical: true, run: runNormalize},
	"compile":           {Mechanical: true, run: runCompile},
	"calibration_apply": {Mechanical: true, run: runCalibrationApply},
	"decide":            {run: runDecide},
	"calibrate_fit":     {Mechanical: true, Skip: "calibration fitting (PAV, Platt, Wilson) lands in the P2 decision package"},
	"detectors":         {Skip: "the Tier-0 detectors land in the P2 decision package"},
	"evidence":          {Skip: "evidence extraction lands in the P2 evidence package"},
	"lint":              {Skip: "evaluator validation and lints land in the P2 decision package"},
	"cost":              {Mechanical: true, Skip: "the cost calculator lands with internal/costs in P4"},
	"pkce":              {Skip: "PKCE vectors are TypeScript-only (SPEC §7.6: no Go runner)"},
}

// ErrUnknownKind is returned by Run for a kind outside Kinds.
var ErrUnknownKind = errors.New("unknown vector kind")

// Run computes the Go reference output for a vector input, as a generic JSON value (the output
// marshalled and decoded, so JSON tags are part of what is compared). contracts is the contracts
// directory, used to resolve built-in evaluator templates.
func Run(kind string, input json.RawMessage, contracts fs.FS) (any, error) {
	k, ok := Kinds[kind]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownKind, kind)
	}
	if k.run == nil {
		return nil, fmt.Errorf("vector kind %q has no Go runner: %s", kind, k.Skip)
	}
	out, err := k.run(input, contracts)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return decodeJSON(b)
}

// GeneratedExpected trims a mechanical kind's output to what cmd/vectorgen records as expected:
// compile requests keep their model and state, since request_sha256 and qid_request pin the
// questions.
func GeneratedExpected(kind string, output any) any {
	if kind != "compile" {
		return output
	}
	m, ok := output.(map[string]any)
	if !ok {
		return output
	}
	reqs, _ := m["requests"].([]any)
	for _, r := range reqs {
		if rm, ok := r.(map[string]any); ok {
			delete(rm, "questions")
		}
	}
	return m
}

func decodeJSON(b []byte) (any, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func strictUnmarshal(in json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// canonical: {value} -> {jcs, sha256}
func runCanonical(in json.RawMessage, _ fs.FS) (any, error) {
	var x struct {
		Value json.RawMessage `json:"value"`
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	b, err := decision.CanonicalizeJSON(x.Value)
	if err != nil {
		return nil, err
	}
	sum, err := decision.SHA256Hex(json.RawMessage(x.Value))
	if err != nil {
		return nil, err
	}
	return map[string]any{"jcs": string(b), "sha256": sum}, nil
}

// tokens: {value, cpt} -> {narrow, wide, est}
func runTokens(in json.RawMessage, _ fs.FS) (any, error) {
	var x struct {
		Value json.RawMessage `json:"value"`
		CPT   float64         `json:"cpt"`
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	return decision.CountTokens(x.Value, x.CPT)
}

// specSource is the evaluator of a question_hash or normalize vector: a built-in template ref or
// an inline spec.
type specSource struct {
	Template string          `json:"template,omitempty"`
	Spec     json.RawMessage `json:"spec,omitempty"`
}

func (s specSource) resolve(contracts fs.FS) (*decision.Spec, error) {
	switch {
	case s.Template != "" && s.Spec != nil:
		return nil, errors.New("give either template or spec, not both")
	case s.Template != "":
		return Template(contracts, s.Template)
	case s.Spec != nil:
		return decision.ParseSpec(s.Spec)
	}
	return nil, errors.New("missing template or spec")
}

// Template loads the built-in evaluator template for ref ("<id>@<version>") from
// evaluators/<id>.v<version>.json in the contracts directory.
func Template(contracts fs.FS, ref string) (*decision.Spec, error) {
	id, version, err := decision.ParseRef(ref)
	if err != nil {
		return nil, err
	}
	b, err := fs.ReadFile(contracts, "evaluators/"+id+".v"+strconv.Itoa(version)+".json")
	if err != nil {
		return nil, fmt.Errorf("template %s: %w", ref, err)
	}
	return decision.ParseSpec(b)
}

// question_hash: {template|spec, cpt} -> {question_id, spec_sha256, question_sha256, calibration_key, question_tokens}
func runQuestionHash(in json.RawMessage, contracts fs.FS) (any, error) {
	var x struct {
		specSource
		CPT float64 `json:"cpt"`
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	spec, err := x.resolve(contracts)
	if err != nil {
		return nil, err
	}
	h, err := spec.Hashes()
	if err != nil {
		return nil, err
	}
	tokens, err := decision.Est(spec.QuestionBody(), x.CPT)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"question_id":     spec.QuestionID(),
		"spec_sha256":     h.SpecSHA256,
		"question_sha256": h.QuestionSHA256,
		"calibration_key": h.CalibrationKey,
		"question_tokens": tokens,
	}, nil
}

// normalize: {template|spec, answer} -> {error, probabilities, risk_raw, top, vendor_confidence, confidence_source, anomalies}
func runNormalize(in json.RawMessage, contracts fs.FS) (any, error) {
	var x struct {
		specSource
		Answer *decision.RawAnswer `json:"answer"`
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	spec, err := x.resolve(contracts)
	if err != nil {
		return nil, err
	}
	n := decision.Normalize(x.Answer, spec)
	out := map[string]any{
		"error": nil, "probabilities": nil, "risk_raw": nil, "top": nil,
		"vendor_confidence": nil, "confidence_source": nil, "anomalies": []string{},
	}
	if n.Error != "" {
		out["error"] = n.Error
		return out, nil
	}
	if n.Probabilities != nil {
		out["probabilities"] = n.Probabilities
	}
	out["risk_raw"] = n.RiskRaw
	if n.Top != "" {
		out["top"] = n.Top
	}
	if n.VendorConfidence != nil {
		out["vendor_confidence"] = *n.VendorConfidence
	}
	if n.ConfidenceSource != "" {
		out["confidence_source"] = n.ConfidenceSource
	}
	if n.Anomalies != nil {
		out["anomalies"] = n.Anomalies
	}
	return out, nil
}

// compile: {route, specs?, inputs, budget?} -> Compiled
func runCompile(in json.RawMessage, contracts fs.FS) (any, error) {
	var x struct {
		Route  decision.CompileRoute      `json:"route"`
		Specs  map[string]json.RawMessage `json:"specs,omitempty"`
		Inputs decision.CompileInputs     `json:"inputs"`
		Budget *decision.Budget           `json:"budget,omitempty"`
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	specs := map[string]*decision.Spec{}
	for ref, raw := range x.Specs {
		s, err := decision.ParseSpec(raw)
		if err != nil {
			return nil, fmt.Errorf("specs[%s]: %w", ref, err)
		}
		specs[ref] = s
	}
	refs := []string{decision.FactualClaimsRef}
	for _, e := range x.Route.Evaluators {
		refs = append(refs, e.Ref)
	}
	for _, ref := range refs {
		if specs[ref] != nil {
			continue
		}
		s, err := Template(contracts, ref)
		if err != nil {
			return nil, err
		}
		specs[ref] = s
	}
	budget := decision.DefaultBudget()
	if x.Budget != nil {
		budget = *x.Budget
	}
	return decision.Compile(&x.Route, specs, &x.Inputs, budget)
}

// calibration_apply: {table, x} -> {y}
func runCalibrationApply(in json.RawMessage, _ fs.FS) (any, error) {
	var x struct {
		Table []float64 `json:"table"`
		X     float64   `json:"x"`
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	if err := decision.CheckCalibrationTable(x.Table); err != nil {
		return nil, err
	}
	return map[string]any{"y": decision.ApplyCalibration(x.Table, x.X)}, nil
}

// decide: decide-input.v1 -> the decision/v1 core
func runDecide(in json.RawMessage, _ fs.FS) (any, error) {
	var x decision.Input
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	return decision.Decide(&x)
}
