package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
)

// Label values of the labels table (SPEC §2.3) accepted besides booleans.
const (
	labelShouldFire    = "should_fire"
	labelShouldNotFire = "should_not_fire"
)

// labelsInput is what the fit reads from a labelled JSONL file.
type labelsInput struct {
	samples []decision.LabeledScore
	sources decision.LabelSourceCounts
	// snapshot and backend are the decision model the risks came from ("" when no line says).
	snapshot, backend string
	datasets          []string
	// compositions and questionSets count the fit contexts of gate-eval lines.
	compositions map[string]int
	questionSets map[string]int
	// skipped counts lines not used, by reason.
	skipped map[string]int
}

// readOptions say how to read a labelled file.
type readOptions struct {
	target string
	// labelKey, when set, names the top-level member holding the label of a gate-eval line (for
	// example label_hallucinated); otherwise labels[<target>] is read.
	labelKey string
	// labelSource is the label source of lines that do not name one.
	labelSource string
	// snapshot is --snapshot: lines naming another snapshot are an error.
	snapshot string
}

// flatLine is a labelled risk: {risk_raw, label, label_source?, snapshot?, backend?, target?,
// dataset?, id?}.
type flatLine struct {
	ID          string          `json:"id"`
	Target      string          `json:"target"`
	RiskRaw     *float64        `json:"risk_raw"`
	Label       json.RawMessage `json:"label"`
	LabelSource string          `json:"label_source"`
	Snapshot    string          `json:"snapshot"`
	Backend     string          `json:"backend"`
	Dataset     string          `json:"dataset"`
}

// gateEvalLine is the part of a cmd/gate-eval output line calibration reads.
type gateEvalLine struct {
	ID       string  `json:"id"`
	Error    *string `json:"error"`
	Skipped  *string `json:"skipped"`
	Decision *struct {
		Evaluators map[string]struct {
			Status  string   `json:"status"`
			RiskRaw *float64 `json:"risk_raw"`
		} `json:"evaluators"`
	} `json:"decision"`
	DecisionModel *struct {
		Backend  string  `json:"backend"`
		Snapshot *string `json:"snapshot"`
	} `json:"decision_model"`
	Compile *struct {
		StateFields       []string `json:"state_fields"`
		QuestionSetSHA256 string   `json:"question_set_sha256"`
	} `json:"compile"`
	Labels      map[string]json.RawMessage `json:"labels"`
	LabelSource string                     `json:"label_source"`
}

// readLabels reads labelled risks for one target from JSONL: flat lines, or cmd/gate-eval output
// lines (recognized by their decision member).
func readLabels(r io.Reader, opt readOptions) (*labelsInput, error) {
	in := &labelsInput{compositions: map[string]int{}, questionSets: map[string]int{}, skipped: map[string]int{}}
	datasets := map[string]bool{}
	br := bufio.NewReader(r)
	for n := 1; ; n++ {
		b, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(b)) > 0 {
			if lerr := in.readLine(b, opt, datasets); lerr != nil {
				return nil, fmt.Errorf("line %d: %w", n, lerr)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	in.datasets = sortedKeys(datasets)
	return in, nil
}

func (in *labelsInput) readLine(b []byte, opt readOptions, datasets map[string]bool) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return fmt.Errorf("not a JSON object: %w", err)
	}
	if _, isGateEval := probe["decision"]; isGateEval {
		return in.readGateEval(b, opt)
	}
	var l flatLine
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return fmt.Errorf("want {risk_raw, label, label_source?, snapshot?, backend?, target?, dataset?, id?} or a gate-eval line: %w", err)
	}
	if l.Target != "" && l.Target != opt.target {
		in.skipped["other target"]++
		return nil
	}
	if l.RiskRaw == nil {
		return errors.New("risk_raw is required")
	}
	fire, ok, err := parseLabel(l.Label)
	if err != nil {
		return err
	}
	if !ok {
		in.skipped["unlabelled"]++
		return nil
	}
	if err := in.setModel(l.Snapshot, l.Backend, opt); err != nil {
		return err
	}
	if l.Dataset != "" {
		datasets[l.Dataset] = true
	}
	return in.add(*l.RiskRaw, fire, l.LabelSource, opt)
}

func (in *labelsInput) readGateEval(b []byte, opt readOptions) error {
	var l gateEvalLine
	if err := json.Unmarshal(b, &l); err != nil {
		return fmt.Errorf("gate-eval line: %w", err)
	}
	switch {
	case l.Error != nil:
		in.skipped["gate-eval error"]++
		return nil
	case l.Skipped != nil:
		in.skipped["gate-eval "+*l.Skipped]++
		return nil
	case l.Decision == nil:
		in.skipped["no decision"]++
		return nil
	}
	e, ok := l.Decision.Evaluators[opt.target]
	if !ok || e.Status != decision.EvalAssessed || e.RiskRaw == nil {
		in.skipped["not assessed"]++
		return nil
	}
	var raw json.RawMessage
	if opt.labelKey != "" {
		var top map[string]json.RawMessage
		if err := json.Unmarshal(b, &top); err != nil {
			return err
		}
		raw = top[opt.labelKey]
	} else {
		raw = l.Labels[opt.target]
	}
	fire, ok, err := parseLabel(raw)
	if err != nil {
		return err
	}
	if !ok {
		in.skipped["unlabelled"]++
		return nil
	}
	var snapshot, backend string
	if l.DecisionModel != nil {
		backend = l.DecisionModel.Backend
		if l.DecisionModel.Snapshot != nil {
			snapshot = *l.DecisionModel.Snapshot
		}
	}
	if err := in.setModel(snapshot, backend, opt); err != nil {
		return err
	}
	if l.Compile != nil {
		fields := slices.Clone(l.Compile.StateFields)
		slices.Sort(fields)
		in.compositions[strings.Join(fields, ",")]++
		if l.Compile.QuestionSetSHA256 != "" {
			in.questionSets[l.Compile.QuestionSetSHA256]++
		}
	}
	return in.add(*e.RiskRaw, fire, l.LabelSource, opt)
}

// setModel records the decision model of a line; every line must agree.
func (in *labelsInput) setModel(snapshot, backend string, opt readOptions) error {
	if snapshot != "" {
		if opt.snapshot != "" && snapshot != opt.snapshot {
			return fmt.Errorf("snapshot %q, but --snapshot is %q", snapshot, opt.snapshot)
		}
		if in.snapshot != "" && snapshot != in.snapshot {
			return fmt.Errorf("snapshot %q differs from %q: a calibration covers one snapshot", snapshot, in.snapshot)
		}
		in.snapshot = snapshot
	}
	if backend != "" {
		if in.backend != "" && backend != in.backend {
			return fmt.Errorf("backend %q differs from %q", backend, in.backend)
		}
		in.backend = backend
	}
	return nil
}

func (in *labelsInput) add(risk float64, fire bool, source string, opt readOptions) error {
	if math.IsNaN(risk) || risk < 0 || risk > 1 {
		return fmt.Errorf("risk_raw %v is outside [0,1]", risk)
	}
	if source == "" {
		source = opt.labelSource
	}
	if source == "" {
		return errors.New("the line names no label_source: pass --label-source (human, public_dataset, uniform_audit or judge)")
	}
	if err := in.sources.Add(source); err != nil {
		return err
	}
	in.samples = append(in.samples, decision.LabeledScore{Risk: risk, Fire: fire})
	return nil
}

// parseLabel reads a label: true/false or should_fire/should_not_fire. ok is false for a missing
// or null label.
func parseLabel(raw json.RawMessage) (fire, ok bool, err error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, false, nil
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b, true, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch s {
		case labelShouldFire:
			return true, true, nil
		case labelShouldNotFire:
			return false, true, nil
		}
	}
	return false, false, fmt.Errorf("label %s: want true, false, %q or %q", raw, labelShouldFire, labelShouldNotFire)
}

// mostCommon returns the key with the highest count (ties to the smaller key) and whether more
// than one key was seen.
func mostCommon(counts map[string]int) (string, bool) {
	best, n := "", -1
	for _, k := range sortedKeys(counts) {
		if counts[k] > n {
			best, n = k, counts[k]
		}
	}
	return best, len(counts) > 1
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
