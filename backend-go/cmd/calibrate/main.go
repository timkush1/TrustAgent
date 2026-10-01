// Command calibrate fits a calibration for one evaluator on one decision-model snapshot and writes
// it to config/calibration/<snapshot>.json (trustagent.calibration/v1, SPEC §2.11, §3.1).
//
// Labelled risks come from a JSONL file (--in) or from a running gateway's label store
// (--source api, which calls POST /api/calibration/fit):
//
//	calibrate --evaluator groundedness@1 --in gate.jsonl --label-source public_dataset --dataset ragtruth:qa@<rev>
//	calibrate --evaluator groundedness@1 --in labels.jsonl --snapshot typesafe/jev-1.13-20260917 --backend openrouter-decisions --label-source human --write
//	calibrate --source api --api http://127.0.0.1:8080 --api-key-file admin.key --evaluator groundedness@1 --write
//
// A file line is either a labelled risk {risk_raw, label, label_source?, snapshot?, backend?,
// target?, dataset?} or a cmd/gate-eval output line, whose risk_raw, snapshot and fit context are
// read from the decision and whose label is labels[<evaluator>] (or --label-key, e.g.
// label_hallucinated). A label is true/false or should_fire/should_not_fire; true means the
// evaluator should fire.
//
// The quality is derived, never chosen: measured needs human or public-dataset labels only with
// n >= 200 and 30 of each class; any uniform-audit or judge label makes it proxy; a mock/* snapshot
// is mock. Without --write nothing is written. The summary says what the numbers mean: in-sample
// agreement with the labels, not ground truth, and activating a calibration changes no live
// decision until a route adopts it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
)

// Exit statuses.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

// DecisionBackend values (common.v1).
var decisionBackends = []string{"openrouter-decisions", "openrouter-systemone", "typesafe", "compatible", "mock", "off"}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, env{now: time.Now, getenv: os.Getenv, rev: gitRevision}))
}

// env is what run takes from the process, so tests can fix it.
type env struct {
	now    func() time.Time
	getenv func(string) string
	rev    func() string
	client *http.Client
}

type options struct {
	source, in, evaluator, labelKey, labelSource, snapshot, backend, method string
	datasets, params, specFiles, stateFields, sources                       multiFlag
	questionSet, out, api, apiKeyFile, route                                string
	write, activate                                                         bool
	timeout                                                                 time.Duration
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*m = append(*m, p)
		}
	}
	return nil
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, e env) int {
	fl := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	fl.SetOutput(stderr)
	var o options
	fl.StringVar(&o.source, "source", "file", "where the labels come from: file (--in) or api (POST /api/calibration/fit)")
	fl.StringVar(&o.in, "in", "-", "labelled JSONL (- = stdin): labelled risks or cmd/gate-eval output")
	fl.StringVar(&o.evaluator, "evaluator", "", "evaluator ref to calibrate, e.g. groundedness@1 (required)")
	fl.StringVar(&o.labelKey, "label-key", "", "gate-eval lines: top-level member holding the label (default: labels[<evaluator>])")
	fl.StringVar(&o.labelSource, "label-source", "", "label source of lines that name none: human, public_dataset, uniform_audit or judge")
	fl.StringVar(&o.snapshot, "snapshot", "", "decision-model snapshot the risks came from (default: from the lines)")
	fl.StringVar(&o.backend, "backend", "", "decision backend of the snapshot (default: from the lines; mock for mock/* snapshots)")
	fl.StringVar(&o.method, "method", decision.MethodAuto, "auto, isotonic or platt")
	fl.Var(&o.datasets, "dataset", "dataset the labels came from, e.g. ragtruth:qa@<rev>; repeatable")
	fl.Var(&o.params, "param", "resolved route param name=value for evaluators with params; repeatable")
	fl.Var(&o.specFiles, "spec", "custom evaluator spec file (evaluator/v1 JSON); repeatable")
	fl.Var(&o.stateFields, "state-fields", "state fields the risks were scored with (default: from gate-eval lines, else the evaluator's)")
	fl.StringVar(&o.questionSet, "question-set-sha256", "", "question set the risks were scored with (default: from gate-eval lines, else the evaluator alone)")
	fl.StringVar(&o.out, "out", "", "calibration directory (default: config/calibration at the repository root)")
	fl.BoolVar(&o.write, "write", false, "write the calibration file (default: print the summary only)")
	fl.StringVar(&o.api, "api", "http://127.0.0.1:8080", "gateway base URL for --source api")
	fl.StringVar(&o.apiKeyFile, "api-key-file", "", "file holding an admin API key for --source api")
	fl.StringVar(&o.route, "route", "", "--source api: route whose labels to use")
	fl.Var(&o.sources, "sources", "--source api: label sources, labels or labels,uniform_audit (default labels)")
	fl.BoolVar(&o.activate, "activate", false, "--source api: also activate the fitted calibration on the gateway")
	fl.DurationVar(&o.timeout, "timeout", 2*time.Minute, "--source api: request timeout")
	fl.Usage = func() {
		fmt.Fprintln(stderr, "usage: calibrate --evaluator <id>@<v> [--in labels.jsonl | --source api] [--label-source s] [--snapshot s] [--write]")
		fl.PrintDefaults()
	}
	if err := fl.Parse(args); err != nil {
		return exitUsage
	}
	if fl.NArg() > 0 || o.evaluator == "" || (o.source != "file" && o.source != "api") {
		fl.Usage()
		return exitUsage
	}

	res, err := calibrate(ctx, &o, stdin, e)
	if err != nil {
		fmt.Fprintf(stderr, "calibrate: %v\n", err)
		if errors.Is(err, errUsage) {
			return exitUsage
		}
		return exitFailed
	}
	fmt.Fprint(stdout, res.summary())
	if !o.write {
		fmt.Fprintf(stdout, "Dry run: nothing written. Pass --write to write %s.\n", res.path)
		return exitOK
	}
	if err := writeFile(res); err != nil {
		fmt.Fprintf(stderr, "calibrate: %v\n", err)
		return exitFailed
	}
	verb := "Added"
	if res.replaced {
		verb = "Replaced"
	}
	fmt.Fprintf(stdout, "%s %s in %s.\n", verb, res.entry.Target.Key(), res.path)
	return exitOK
}

// errUsage marks errors the caller fixes with other arguments.
var errUsage = errors.New("usage")

// outcome is a fitted calibration ready to write.
type outcome struct {
	ref      string
	file     *decision.CalibrationFile
	entry    *decision.CalibrationFileEntry
	fit      *decision.Fit // nil for --source api
	samples  []decision.LabeledScore
	skipped  map[string]int
	warnings []string
	path     string
	replaced bool
}

func calibrate(ctx context.Context, o *options, stdin io.Reader, e env) (*outcome, error) {
	specs, err := loadSpecs(o.specFiles)
	if err != nil {
		return nil, err
	}
	spec, ok := specs[o.evaluator]
	if !ok {
		return nil, fmt.Errorf("%w: unknown evaluator %q", errUsage, o.evaluator)
	}
	generatedBy := "cmd/calibrate@" + e.rev()
	var res *outcome
	if o.source == "api" {
		res, err = fromAPI(ctx, o, e, generatedBy)
	} else {
		res, err = fromFile(o, stdin, spec, e, generatedBy)
	}
	if err != nil {
		return nil, err
	}
	res.ref = o.evaluator
	name, err := decision.CalibrationFileName(res.file.DecisionModel.Snapshot)
	if err != nil {
		return nil, err
	}
	res.path = filepath.Join(outDir(o.out), name)
	return res, merge(res)
}

// defaultOutDir is where committed calibrations live (TRUTHTABLE_CALIBRATION_DIR's default).
const defaultOutDir = "config/calibration"

// outDir resolves --out: as given when set, else config/calibration under the repository root
// (the nearest parent holding VERSION), so the command writes to the same place from backend-go/
// or the root.
func outDir(out string) string {
	if out != "" {
		return out
	}
	dir, err := os.Getwd()
	if err != nil {
		return defaultOutDir
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "VERSION")); err == nil {
			return filepath.Join(dir, defaultOutDir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return defaultOutDir
		}
		dir = parent
	}
}

func loadSpecs(files []string) (map[string]*decision.Spec, error) {
	reg, err := evaluators.Builtins()
	if err != nil {
		return nil, err
	}
	specs := map[string]*decision.Spec{}
	for ref, s := range reg.Specs() {
		specs[ref] = s
	}
	for _, path := range files {
		b, err := os.ReadFile(path) // #nosec G304 -- a spec file the operator named on the command line
		if err != nil {
			return nil, err
		}
		s, err := decision.ParseSpec(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		// A version is immutable: a file cannot redefine a built-in template or another file's spec.
		if _, dup := specs[s.Ref()]; dup {
			return nil, fmt.Errorf("%w: %s: %s is already defined", errUsage, path, s.Ref())
		}
		specs[s.Ref()] = s
	}
	return specs, nil
}

func fromFile(o *options, stdin io.Reader, spec *decision.Spec, e env, generatedBy string) (*outcome, error) {
	r := stdin
	if o.in != "-" {
		f, err := os.Open(o.in) // #nosec G304 -- the labels file the operator named on the command line
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	in, err := readLabels(r, readOptions{target: o.evaluator, labelKey: o.labelKey, labelSource: o.labelSource, snapshot: o.snapshot})
	if err != nil {
		return nil, err
	}
	snapshot := o.snapshot
	if snapshot == "" {
		snapshot = in.snapshot
	}
	if snapshot == "" {
		return nil, fmt.Errorf("%w: no line names its snapshot: pass --snapshot", errUsage)
	}
	backend := o.backend
	if backend == "" {
		backend = in.backend
	}
	if backend == "" && strings.HasPrefix(snapshot, "mock/") {
		backend = "mock"
	}
	switch {
	case backend == "":
		return nil, fmt.Errorf("%w: no line names its backend: pass --backend", errUsage)
	case in.backend != "" && o.backend != "" && in.backend != o.backend:
		return nil, fmt.Errorf("%w: the lines come from backend %q, not %q", errUsage, in.backend, o.backend)
	case !slices.Contains(decisionBackends, backend):
		return nil, fmt.Errorf("%w: unknown backend %q", errUsage, backend)
	}

	fit, err := decision.FitCalibration(in.samples, decision.FitOptions{Method: o.method})
	if err != nil {
		return nil, err
	}
	params, err := parseParams(o.params)
	if err != nil {
		return nil, err
	}
	target, err := decision.EvaluatorTarget(spec, params)
	if err != nil {
		return nil, err
	}
	var warnings []string
	fields := []string(o.stateFields)
	if len(fields) == 0 {
		composition, mixed := mostCommon(in.compositions)
		if composition != "" {
			fields = strings.Split(composition, ",")
		}
		if mixed {
			warnings = append(warnings, "labels were scored with different state compositions; fit_context records the most common one")
		}
	}
	if len(fields) == 0 {
		fields = slices.Clone(spec.StateFields)
	}
	qs := o.questionSet
	if qs == "" {
		var mixed bool
		if qs, mixed = mostCommon(in.questionSets); mixed {
			warnings = append(warnings, "labels were scored with different question sets; fit_context records the most common one")
		}
	}
	if qs == "" {
		if qs, err = decision.SoloQuestionSetSHA256(spec); err != nil {
			return nil, err
		}
	}
	datasets := slices.Clone(in.datasets)
	for _, d := range o.datasets {
		if !slices.Contains(datasets, d) {
			datasets = append(datasets, d)
		}
	}
	slices.Sort(datasets)
	entry, err := decision.NewCalibrationEntry(target, decision.FitContext{StateFields: fields, QuestionSetSHA256: qs}, fit, in.sources, datasets, snapshot)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, fit.Warnings...)
	return &outcome{
		file:  decision.NewCalibrationFile(backend, snapshot, generatedBy, e.now()),
		entry: entry, fit: fit, samples: in.samples, skipped: in.skipped, warnings: warnings,
	}, nil
}

func fromAPI(ctx context.Context, o *options, e env, generatedBy string) (*outcome, error) {
	var key string
	if o.apiKeyFile != "" {
		b, err := os.ReadFile(o.apiKeyFile) // #nosec G304 -- the key file the operator named on the command line
		if err != nil {
			return nil, err
		}
		key = strings.TrimSpace(string(b))
	}
	sources := []string(o.sources)
	if len(sources) == 0 {
		sources = []string{"labels"}
	}
	client := e.client
	if client == nil {
		client = &http.Client{Timeout: o.timeout}
	}
	rec, err := fitViaAPI(ctx, client, o.api, key, fitRequest{
		Target: o.evaluator, Route: o.route, Snapshot: o.snapshot, Sources: sources, Method: o.method, Activate: o.activate,
	})
	if err != nil {
		return nil, err
	}
	target := rec.Entry.Target
	if ref := fmt.Sprintf("%s@%d", target.EvaluatorID, target.EvaluatorVersion); ref != o.evaluator {
		return nil, fmt.Errorf("the gateway fitted %s, not %s", ref, o.evaluator)
	}
	entry := rec.Entry
	return &outcome{
		file:  decision.NewCalibrationFile(rec.Backend, rec.Snapshot, generatedBy+" via api/calibration/fit", e.now()),
		entry: &entry, skipped: map[string]int{},
	}, nil
}

func parseParams(kv []string) (map[string]string, error) {
	if len(kv) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, p := range kv {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("%w: --param %q: want name=value", errUsage, p)
		}
		out[k] = v
	}
	return out, nil
}

// merge folds the entry into the snapshot's existing file, if any: the entry with the same target
// key is replaced, other entries are kept, and the file's warnings about this evaluator are
// replaced by the new ones.
func merge(res *outcome) error {
	prefix := res.ref + ": "
	b, err := os.ReadFile(res.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		old, err := decision.ParseCalibrationFile(b)
		if err != nil {
			return fmt.Errorf("%s: %w", res.path, err)
		}
		if old.DecisionModel.Snapshot != res.file.DecisionModel.Snapshot || old.DecisionModel.Backend != res.file.DecisionModel.Backend {
			return fmt.Errorf("%s holds %s on %s, not %s on %s", res.path, old.DecisionModel.Snapshot, old.DecisionModel.Backend,
				res.file.DecisionModel.Snapshot, res.file.DecisionModel.Backend)
		}
		res.file.Entries = old.Entries
		for _, w := range old.Warnings {
			if !strings.HasPrefix(w, prefix) {
				res.file.Warnings = append(res.file.Warnings, w)
			}
		}
	}
	res.replaced = res.file.Upsert(*res.entry)
	for _, w := range res.warnings {
		res.file.Warnings = append(res.file.Warnings, prefix+w)
	}
	return res.file.Check()
}

func writeFile(res *outcome) error {
	b, err := json.MarshalIndent(res.file, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(res.path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(res.path), ".calibration-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil { // #nosec G302 -- a committed, non-secret config file
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), res.path)
}

// gitRevision is the short HEAD revision for generated_by, or "unknown".
func gitRevision() string {
	// #nosec G204 -- a fixed git command
	out, err := exec.Command("git", "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
