package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/contracts"
)

// The held-out check (D206): calibrate --holdout FILE --calibration FILE measures the expected
// calibration error and the Brier score of a fitted calibration, before and after it is applied,
// on labels it was not fitted on (the bench's RAGTruth-QA test sample, fitted on the train split).
// The calibrated risk is the decision package's own ApplyCalibration over the entry's table, the
// mapping the gateway applies, unchanged. Nothing is written but the --holdout-out report.

// holdoutSchema names the --holdout-out report that bench publish --calibration-holdout folds.
const holdoutSchema = "trustagent.calibration-holdout/v1"

// holdoutMetrics are the expected calibration error (ten equal-width bins, as the fit's) and the
// Brier score of one set of predictions.
type holdoutMetrics struct {
	ECE   *float64 `json:"ece"`
	Brier *float64 `json:"brier"`
}

// holdoutData describes the held-out labels.
type holdoutData struct {
	File     string                     `json:"file"`
	N        int                        `json:"n"`
	NPos     int                        `json:"n_pos"`
	NNeg     int                        `json:"n_neg"`
	Datasets []string                   `json:"datasets"`
	Sources  decision.LabelSourceCounts `json:"label_source"`
	Hosts    []string                   `json:"endpoint_hosts"`
	Skipped  map[string]int             `json:"skipped"`
}

// holdoutReport is the --holdout-out document.
type holdoutReport struct {
	Schema              string         `json:"schema"`
	Evaluator           string         `json:"evaluator"`
	TargetKey           string         `json:"target_key"`
	Snapshot            string         `json:"snapshot"`
	Backend             string         `json:"backend"`
	CalibrationFile     string         `json:"calibration_file"`
	CalibrationSHA256   string         `json:"calibration_sha256"`
	CalibrationMethod   string         `json:"calibration_method"`
	CalibrationQuality  string         `json:"calibration_quality"`
	CalibrationDatasets []string       `json:"calibration_datasets"`
	CalibrationN        int            `json:"calibration_n"`
	Holdout             holdoutData    `json:"holdout"`
	Before              holdoutMetrics `json:"before"`
	After               holdoutMetrics `json:"after"`
	AUROC               *float64       `json:"auroc"`
	// NonVendorHosts lists the holdout's decision hosts outside the vendor list (D203): the risks
	// came from a stand-in, so the numbers are simulated.
	NonVendorHosts []string `json:"non_vendor_hosts"`
	Warnings       []string `json:"warnings"`
}

// holdout runs --holdout: it never fits and never writes a calibration.
func holdout(o *options, stdin io.Reader, stdout io.Writer) error {
	specs, err := loadSpecs(o.specFiles)
	if err != nil {
		return err
	}
	spec, ok := specs[o.evaluator]
	if !ok {
		return fmt.Errorf("%w: unknown evaluator %q", errUsage, o.evaluator)
	}
	params, err := parseParams(o.params)
	if err != nil {
		return err
	}
	target, err := decision.EvaluatorTarget(spec, params)
	if err != nil {
		return err
	}

	raw, err := os.ReadFile(o.calibration) // #nosec G304 -- the calibration file the operator named on the command line
	if err != nil {
		return err
	}
	file, err := decision.ParseCalibrationFile(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", o.calibration, err)
	}
	if err := file.Check(); err != nil {
		return fmt.Errorf("%s: %w", o.calibration, err)
	}
	i := slices.IndexFunc(file.Entries, func(e decision.CalibrationFileEntry) bool { return e.Target.Key() == target.Key() })
	if i < 0 {
		return fmt.Errorf("%w: %s has no calibration for %s (target %s)", errUsage, o.calibration, o.evaluator, target.Key())
	}
	entry := file.Entries[i]

	r := stdin
	if o.holdout != "-" {
		f, err := os.Open(o.holdout) // #nosec G304 -- the holdout file the operator named on the command line
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	in, err := readLabels(r, readOptions{target: o.evaluator, labelKey: o.labelKey, labelSource: o.labelSource, snapshot: o.snapshot})
	if err != nil {
		return err
	}
	if len(in.samples) == 0 {
		return fmt.Errorf("%w: %s has no labelled %s risks", errUsage, o.holdout, o.evaluator)
	}
	m := file.DecisionModel
	var warnings []string
	switch {
	case in.snapshot != "" && in.snapshot != m.Snapshot:
		return fmt.Errorf("the holdout's risks come from %s, the calibration from %s: one snapshot across calibration and bench (D207)", in.snapshot, m.Snapshot)
	case in.snapshot == "":
		warnings = append(warnings, "the holdout lines name no snapshot: they are taken to be "+m.Snapshot)
	}
	if in.backend != "" && in.backend != m.Backend {
		return fmt.Errorf("the holdout's risks come from backend %s, the calibration from %s", in.backend, m.Backend)
	}
	for _, d := range in.datasets {
		if slices.Contains(entry.Datasets, d) {
			return fmt.Errorf("the holdout's dataset %s is one the calibration was fitted on: an ECE on it would be in-sample, not held out", d)
		}
	}

	rep := holdoutReport{
		Schema: holdoutSchema, Evaluator: o.evaluator, TargetKey: target.Key(), Snapshot: m.Snapshot, Backend: m.Backend,
		CalibrationFile: repoRelative(o.calibration), CalibrationMethod: entry.Method, CalibrationQuality: entry.Quality,
		CalibrationDatasets: nonNil(entry.Datasets), CalibrationN: entry.N, NonVendorHosts: nonVendorHosts(in.hosts, m.Backend),
		Warnings: warnings,
	}
	sum := sha256.Sum256(raw)
	rep.CalibrationSHA256 = hex.EncodeToString(sum[:])
	rep.Holdout = holdoutData{File: o.holdout, N: len(in.samples), Datasets: nonNil(in.datasets), Sources: in.sources,
		Hosts: sortedKeys(in.hosts), Skipped: in.skipped}
	before := make([]float64, len(in.samples))
	after := make([]float64, len(in.samples))
	for i, s := range in.samples {
		before[i] = s.Risk
		after[i] = decision.ApplyCalibration(entry.Table, s.Risk)
		if s.Fire {
			rep.Holdout.NPos++
		} else {
			rep.Holdout.NNeg++
		}
	}
	_, rep.Before.ECE = decision.Reliability(in.samples, before)
	rep.Before.Brier = decision.Brier(in.samples, before)
	_, rep.After.ECE = decision.Reliability(in.samples, after)
	rep.After.Brier = decision.Brier(in.samples, after)
	rep.AUROC = decision.AUROC(in.samples)
	if len(rep.NonVendorHosts) > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("the holdout's risks came from %s, not a vendor host (%s): they are simulated, never measured (D203)",
			strings.Join(rep.NonVendorHosts, ", "), strings.Join(vendorHosts(), ", ")))
	}

	fmt.Fprint(stdout, rep.summary())
	if o.holdoutOut == "" {
		return nil
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(o.holdoutOut, append(b, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Wrote %s.\n", o.holdoutOut)
	return nil
}

// summary is the plain-language held-out report.
func (r *holdoutReport) summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Held-out check of the %s calibration on %s (backend %s)\n", r.Evaluator, r.Snapshot, r.Backend)
	fmt.Fprintf(&b, "  Calibration: %s (%s, quality %s, fitted on %d labels from %s; sha256 %s)\n", r.CalibrationFile, r.CalibrationMethod,
		r.CalibrationQuality, r.CalibrationN, orNone(r.CalibrationDatasets), r.CalibrationSHA256[:12])
	fmt.Fprintf(&b, "  Held out: %d labels (%d should fire, %d should not) from %s\n", r.Holdout.N, r.Holdout.NPos, r.Holdout.NNeg, orNone(r.Holdout.Datasets))
	fmt.Fprintf(&b, "  Held out, not in-sample: ECE %s → %s, Brier %s → %s (before → after calibration), AUROC %s\n",
		opt(r.Before.ECE), opt(r.After.ECE), opt(r.Before.Brier), opt(r.After.Brier), opt(r.AUROC))
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "  Warning: %s\n", w)
	}
	return b.String()
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "no named dataset"
	}
	return strings.Join(xs, ", ")
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// repoRelative names path relative to the repository root when it lies inside it
// (config/calibration/<snapshot>.json, the form measured.json records), else as given.
func repoRelative(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	root := filepath.Dir(outDir("")) // <root>/config
	root = filepath.Dir(root)
	if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return filepath.ToSlash(rel)
	}
	return path
}

// vendorHosts is common.v1 VendorHost: the hosts a measured run may name (D203).
func vendorHosts() []string {
	b, err := contracts.FS.ReadFile("schema/common.v1.schema.json")
	if err != nil {
		return nil
	}
	var doc struct {
		Defs struct {
			VendorHost struct {
				Enum []string `json:"enum"`
			} `json:"VendorHost"`
		} `json:"$defs"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	return doc.Defs.VendorHost.Enum
}

// nonVendorHosts are the decision hosts in hosts that are not vendor hosts. The mock backend has
// no host and is labelled by its snapshot (quality mock) instead.
func nonVendorHosts(hosts map[string]int, backend string) []string {
	if backend == "mock" {
		return []string{}
	}
	vendors := vendorHosts()
	out := []string{}
	for _, h := range sortedKeys(hosts) {
		if h != "" && !slices.Contains(vendors, h) {
			out = append(out, h)
		}
	}
	return out
}
