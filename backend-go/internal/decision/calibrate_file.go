package decision

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The calibration.v1 file ("thresholds JSON", SPEC §2.11, contracts/schema/calibration.v1.schema.json)
// and quality derivation.

// Calibration qualities besides QualityMeasured and QualityMock (CalibrationQuality).
const (
	QualityProvisional = "provisional"
	QualityProxy       = "proxy"
)

// MeasuredMinLabels and MeasuredMinPerClass are the label counts quality measured needs (§2.11,
// §0 row 13): n >= 200 with at least 30 of each class.
const (
	MeasuredMinLabels   = 200
	MeasuredMinPerClass = 30
)

// Label sources (labels.label_source, plus the proxy sources a fit may use).
const (
	LabelSourceHuman         = "human"
	LabelSourcePublicDataset = "public_dataset"
	LabelSourceUniformAudit  = "uniform_audit"
	LabelSourceJudge         = "judge"
)

// Calibration file constants.
const (
	CalibrationSchemaID         = "trustagent.calibration/v1"
	CalibrationTargetEvaluator  = "evaluator"
	CalibrationPartitionSingle  = "single"
	calibrationKeyPrefixLength  = 16
	calibrationNoParamsKeyShard = "-"
)

// LabelSourceCounts is calibration.v1 label_source: how many labels came from each source.
type LabelSourceCounts struct {
	Human         int `json:"human"`
	PublicDataset int `json:"public_dataset"`
	UniformAudit  int `json:"uniform_audit"`
	Judge         int `json:"judge,omitempty"`
}

// Add counts one label from source.
func (c *LabelSourceCounts) Add(source string) error {
	switch source {
	case LabelSourceHuman:
		c.Human++
	case LabelSourcePublicDataset:
		c.PublicDataset++
	case LabelSourceUniformAudit:
		c.UniformAudit++
	case LabelSourceJudge:
		c.Judge++
	default:
		return fmt.Errorf("unknown label source %q (want %s, %s, %s or %s)", source,
			LabelSourceHuman, LabelSourcePublicDataset, LabelSourceUniformAudit, LabelSourceJudge)
	}
	return nil
}

// Total is the number of labels counted.
func (c LabelSourceCounts) Total() int { return c.Human + c.PublicDataset + c.UniformAudit + c.Judge }

// DeriveQuality derives a calibration's quality (SPEC §2.11, re-derived by the server on import):
//
//  1. mock for a mock/* snapshot, whatever the labels;
//  2. proxy when any label came from a uniform-sample audit or a judge;
//  3. provisional for a family fallback (family true: the snapshot's family, not the snapshot);
//  4. measured with human and public-dataset labels only, n >= 200, n_pos >= 30 and n_neg >= 30;
//  5. provisional otherwise.
func DeriveQuality(snapshot string, src LabelSourceCounts, nPos, nNeg int, family bool) string {
	switch {
	case strings.HasPrefix(snapshot, "mock/"):
		return QualityMock
	case src.UniformAudit > 0 || src.Judge > 0:
		return QualityProxy
	case family:
		return QualityProvisional
	case nPos+nNeg >= MeasuredMinLabels && nPos >= MeasuredMinPerClass && nNeg >= MeasuredMinPerClass:
		return QualityMeasured
	}
	return QualityProvisional
}

// snapshotDateSuffix is the -YYYYMMDD suffix a snapshot's family drops.
var snapshotDateSuffix = regexp.MustCompile(`-[0-9]{8}$`)

// SnapshotFamily returns the snapshot minus its -YYYYMMDD suffix (typesafe/jev-1.13-20260917 ->
// typesafe/jev-1.13); a snapshot without the suffix is its own family.
func SnapshotFamily(snapshot string) string {
	return snapshotDateSuffix.ReplaceAllString(snapshot, "")
}

// calibrationFileName allows the characters a snapshot file name may keep.
var calibrationFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// CalibrationFileName is the file name of a snapshot's calibration in config/calibration: the
// snapshot with "/" replaced by "__", plus ".json" (mock/jev-heuristic-v1 ->
// mock__jev-heuristic-v1.json). Snapshots that would not make a plain file name are rejected.
func CalibrationFileName(snapshot string) (string, error) {
	name := strings.ReplaceAll(snapshot, "/", "__")
	if !calibrationFileName.MatchString(name) || strings.Contains(name, "..") || len(name) > 200 {
		return "", fmt.Errorf("calibration: snapshot %q does not make a file name", snapshot)
	}
	return name + ".json", nil
}

// CalibrationModel is calibration.v1 decision_model.
type CalibrationModel struct {
	Backend  string `json:"backend"`
	Snapshot string `json:"snapshot"`
	Family   string `json:"family"`
}

// CalibrationTarget is a calibration entry's target (kind evaluator; other kinds are v2.1).
type CalibrationTarget struct {
	Kind             string  `json:"kind"`
	EvaluatorID      string  `json:"evaluator_id"`
	EvaluatorVersion int     `json:"evaluator_version"`
	CalibrationKey   string  `json:"calibration_key"`
	ParamsSHA256     *string `json:"params_sha256"`
}

// Key is the calibrations.target_key of the target:
// "<id>@<v>#<calibration_key[:16]>#<params_sha256[:16]|->".
func (t CalibrationTarget) Key() string {
	params := calibrationNoParamsKeyShard
	if t.ParamsSHA256 != nil {
		params = prefix(*t.ParamsSHA256, calibrationKeyPrefixLength)
	}
	return t.EvaluatorID + "@" + strconv.Itoa(t.EvaluatorVersion) + "#" +
		prefix(t.CalibrationKey, calibrationKeyPrefixLength) + "#" + params
}

func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ResolvedParams returns the evaluator's params as a route resolves them: each declared param's
// route value, else its default. Undeclared route params are ignored. It is nil when the spec
// declares none.
func ResolvedParams(spec *Spec, routeParams map[string]string) map[string]string {
	if len(spec.Params) == 0 {
		return nil
	}
	out := make(map[string]string, len(spec.Params))
	for name, p := range spec.Params {
		out[name] = p.Default
		if v, ok := routeParams[name]; ok {
			out[name] = v
		}
	}
	return out
}

// EvaluatorTarget returns the calibration target of an evaluator with the given route params:
// calibration_key = sha256(JCS({question, signal})) and, when the spec declares params,
// params_sha256 = sha256(JCS(resolved params)).
func EvaluatorTarget(spec *Spec, routeParams map[string]string) (CalibrationTarget, error) {
	h, err := spec.Hashes()
	if err != nil {
		return CalibrationTarget{}, err
	}
	t := CalibrationTarget{
		Kind: CalibrationTargetEvaluator, EvaluatorID: spec.ID, EvaluatorVersion: spec.Version,
		CalibrationKey: h.CalibrationKey,
	}
	if params := ResolvedParams(spec, routeParams); params != nil {
		sum, err := SHA256Hex(params)
		if err != nil {
			return CalibrationTarget{}, err
		}
		t.ParamsSHA256 = &sum
	}
	return t, nil
}

// FitContext is calibration.v1 fit_context: the state composition and question set the labels
// were scored with.
type FitContext struct {
	StateFields       []string `json:"state_fields"`
	Partition         string   `json:"partition"`
	QuestionSetSHA256 string   `json:"question_set_sha256"`
}

// SoloQuestionSetSHA256 is the question_set_sha256 compile() reports when the evaluator is the only
// question asked: sha256(JCS([[question_id, question]])).
func SoloQuestionSetSHA256(spec *Spec) (string, error) {
	return SHA256Hex([]any{[]any{spec.QuestionID(), spec.QuestionBody()}})
}

// CalibrationFileEntry is one calibration.v1 entry.
type CalibrationFileEntry struct {
	Target      CalibrationTarget `json:"target"`
	FitContext  FitContext        `json:"fit_context"`
	Method      string            `json:"method"`
	Table       []float64         `json:"table"`
	Platt       *PlattParams      `json:"platt"`
	Quality     string            `json:"quality"`
	N           int               `json:"n"`
	NPos        int               `json:"n_pos"`
	NNeg        int               `json:"n_neg"`
	LabelSource LabelSourceCounts `json:"label_source"`
	Datasets    []string          `json:"datasets"`
	Metrics     FitMetrics        `json:"metrics"`
	Reliability []ReliabilityBin  `json:"reliability"`
	Recommended Recommended       `json:"recommended"`
}

// NewCalibrationEntry assembles an entry from a fit. The quality is derived from snapshot and the
// label sources (DeriveQuality, no family fallback); src must count exactly fit.N labels.
func NewCalibrationEntry(target CalibrationTarget, ctx FitContext, fit *Fit, src LabelSourceCounts, datasets []string, snapshot string) (*CalibrationFileEntry, error) {
	if src.Total() != fit.N {
		return nil, fmt.Errorf("calibration: label sources count %d labels, the fit has %d", src.Total(), fit.N)
	}
	fields := slices.Clone(ctx.StateFields)
	slices.Sort(fields)
	ctx.StateFields = slices.Compact(fields)
	if ctx.Partition == "" {
		ctx.Partition = CalibrationPartitionSingle
	}
	if datasets == nil {
		datasets = []string{}
	}
	return &CalibrationFileEntry{
		Target: target, FitContext: ctx, Method: fit.Method, Table: slices.Clone(fit.Table), Platt: fit.Platt,
		Quality: DeriveQuality(snapshot, src, fit.NPos, fit.NNeg, false),
		N:       fit.N, NPos: fit.NPos, NNeg: fit.NNeg, LabelSource: src, Datasets: slices.Clone(datasets),
		Metrics: fit.Metrics, Reliability: fit.Reliability, Recommended: fit.Recommended,
	}, nil
}

// CalibrationFile is a trustagent.calibration/v1 document.
type CalibrationFile struct {
	Schema        string                 `json:"schema"`
	CreatedAt     time.Time              `json:"created_at"`
	GeneratedBy   string                 `json:"generated_by"`
	DecisionModel CalibrationModel       `json:"decision_model"`
	Entries       []CalibrationFileEntry `json:"entries"`
	Warnings      []string               `json:"warnings"`
}

// NewCalibrationFile returns an empty calibration file for a snapshot.
func NewCalibrationFile(backend, snapshot, generatedBy string, createdAt time.Time) *CalibrationFile {
	return &CalibrationFile{
		Schema: CalibrationSchemaID, CreatedAt: createdAt.UTC(), GeneratedBy: generatedBy,
		DecisionModel: CalibrationModel{Backend: backend, Snapshot: snapshot, Family: SnapshotFamily(snapshot)},
		Entries:       []CalibrationFileEntry{}, Warnings: []string{},
	}
}

// Upsert adds e, replacing the entry with the same target key. It reports whether one was replaced.
func (f *CalibrationFile) Upsert(e CalibrationFileEntry) bool {
	key := e.Target.Key()
	for i := range f.Entries {
		if f.Entries[i].Target.Key() == key {
			f.Entries[i] = e
			return true
		}
	}
	f.Entries = append(f.Entries, e)
	return false
}

// ParseCalibrationFile decodes a calibration file strictly (unknown members are errors) and checks
// it with Check.
func ParseCalibrationFile(b []byte) (*CalibrationFile, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var f CalibrationFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("calibration file: %w", err)
	}
	if dec.More() {
		return nil, errors.New("calibration file: trailing data after the document")
	}
	if err := f.Check(); err != nil {
		return nil, err
	}
	return &f, nil
}

// Check reports what the schema cannot: the family matches the snapshot, each table is a valid
// calibration table, Platt parameters are present exactly for Platt entries, the counts agree
// (n = n_pos + n_neg = label_source total), target keys are unique, and each entry's quality is
// the one DeriveQuality gives. Importers re-derive quality instead of trusting it; Check fails on
// a mismatch so that a file never claims more than its labels support.
func (f *CalibrationFile) Check() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if f.Schema != CalibrationSchemaID {
		add("schema is %q, want %q", f.Schema, CalibrationSchemaID)
	}
	if f.DecisionModel.Snapshot == "" {
		add("decision_model.snapshot is empty")
	}
	if want := SnapshotFamily(f.DecisionModel.Snapshot); f.DecisionModel.Family != want {
		add("decision_model.family is %q, want %q", f.DecisionModel.Family, want)
	}
	seen := map[string]bool{}
	for i, e := range f.Entries {
		at := fmt.Sprintf("entries[%d] (%s@%d)", i, e.Target.EvaluatorID, e.Target.EvaluatorVersion)
		if key := e.Target.Key(); seen[key] {
			add("%s: duplicate target %s", at, key)
		} else {
			seen[key] = true
		}
		if err := CheckCalibrationTable(e.Table); err != nil {
			add("%s: %v", at, err)
		}
		switch {
		case e.Method == MethodPlatt && e.Platt == nil:
			add("%s: a platt entry needs platt parameters", at)
		case e.Method == MethodIsotonic && e.Platt != nil:
			add("%s: an isotonic entry has platt parameters", at)
		case e.Method != MethodPlatt && e.Method != MethodIsotonic:
			add("%s: method %q, want isotonic or platt", at, e.Method)
		}
		if e.N != e.NPos+e.NNeg || e.N != e.LabelSource.Total() {
			add("%s: n = %d, but n_pos + n_neg = %d and label_source counts %d", at, e.N, e.NPos+e.NNeg, e.LabelSource.Total())
		}
		if want := DeriveQuality(f.DecisionModel.Snapshot, e.LabelSource, e.NPos, e.NNeg, false); e.Quality != want {
			add("%s: quality %q, but its labels support %q", at, e.Quality, want)
		}
	}
	if len(problems) > 0 {
		return errors.New("calibration file: " + strings.Join(problems, "; "))
	}
	return nil
}
