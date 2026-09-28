// Command vectorgen maintains the golden vectors that the Go reference in internal/decision runs
// (SPEC §7.6).
//
// Mechanical kinds (canonical, tokens, question_hash, normalize, compile, calibration_apply): vector
// inputs are written by hand under contracts/vectors/<kind>/<name>.json with status draft (and
// expected null). vectorgen fills in expected and generated_by; a person reviews the diff and then
// freezes the vector (status frozen, by hand or with -freeze).
//
// Semantic kinds with a Go runner (decide, detectors): expected is written by hand from the SPEC
// and may be a subset of the output. vectorgen never writes it; it checks that the reference
// agrees, and -freeze marks an agreeing draft frozen. Semantic vectors are frozen only when their
// kind is named in -kinds, because freezing one records that a person reviewed it.
//
// Frozen vectors are never rewritten: when the reference no longer produces a frozen expected
// value, vectorgen fails, since a behaviour change needs a new vector. Only the expected,
// generated_by and status members are touched; the rest of each file keeps its formatting byte for
// byte. Kinds without a Go runner (see vectors.Kinds) are left alone.
//
// Usage (from backend-go/, or `make vectors`):
//
//	go run ./cmd/vectorgen [-contracts ../contracts] [-kinds compile,detectors] [-check] [-freeze]
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/truthtable/backend-go/internal/decision/vectors"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type options struct {
	contracts string
	kinds     map[string]bool
	check     bool
	freeze    bool
	rev       string
}

func run(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("vectorgen", flag.ContinueOnError)
	fl.SetOutput(stderr)
	contracts := fl.String("contracts", "../contracts", "path of the contracts directory")
	kinds := fl.String("kinds", "", "comma-separated kinds to process (default: every kind with a Go runner); semantic kinds are frozen only when named here")
	check := fl.Bool("check", false, "write nothing; fail when a draft vector is stale or a frozen one differs")
	freeze := fl.Bool("freeze", false, "mark the processed draft vectors frozen once the reference agrees (after reviewing their expected values)")
	rev := fl.String("rev", "", "revision recorded in generated_by (default: git rev-parse --short=12 HEAD)")
	fl.Usage = func() {
		fmt.Fprintln(stderr, "usage: vectorgen [-contracts dir] [-kinds k1,k2] [-check] [-freeze] [-rev sha]")
		fl.PrintDefaults()
	}
	if err := fl.Parse(args); err != nil {
		return 2
	}
	if fl.NArg() > 0 || (*check && *freeze) {
		fl.Usage()
		return 2
	}
	opt := options{contracts: *contracts, check: *check, freeze: *freeze, rev: *rev}
	if *kinds != "" {
		opt.kinds = map[string]bool{}
		for _, k := range strings.Split(*kinds, ",") {
			info, ok := vectors.Kinds[k]
			if !ok || info.Skip != "" {
				fmt.Fprintf(stderr, "vectorgen: %q is not a vector kind with a Go runner\n", k)
				return 2
			}
			opt.kinds[k] = true
		}
	}
	if opt.rev == "" && !opt.check {
		opt.rev = gitRevision(opt.contracts)
	}
	if err := generate(opt, stdout); err != nil {
		fmt.Fprintf(stderr, "vectorgen: %v\n", err)
		return 1
	}
	return 0
}

// tally counts what happened to each kind's vectors.
type tally struct {
	written, frozen, current int
}

func generate(opt options, stdout io.Writer) error {
	// Every read and write stays inside the contracts directory.
	root, err := os.OpenRoot(opt.contracts)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	fsys := root.FS()
	all, err := vectors.Load(fsys, "vectors")
	if err != nil {
		return err
	}
	counts := map[string]*tally{}
	var problems []string
	for _, v := range all {
		info, known := vectors.Kinds[v.Dir]
		if !known {
			problems = append(problems, fmt.Sprintf("%s: unknown vector kind %q", v.Path, v.Dir))
			continue
		}
		if info.Skip != "" || (opt.kinds != nil && !opt.kinds[v.Dir]) {
			continue
		}
		if counts[v.Dir] == nil {
			counts[v.Dir] = &tally{}
		}
		action, err := process(opt, root, v)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", v.Path, err))
			continue
		}
		switch action {
		case actionWritten:
			counts[v.Dir].written++
		case actionFrozen:
			counts[v.Dir].frozen++
		default:
			counts[v.Dir].current++
		}
	}

	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	verb := "written"
	if opt.check {
		verb = "stale"
	}
	for _, k := range kinds {
		c := counts[k]
		fmt.Fprintf(stdout, "%-18s %3d %s, %3d frozen now, %3d up to date\n", k, c.written, verb, c.frozen, c.current)
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n"))
	}
	if opt.check && slices.ContainsFunc(kinds, func(k string) bool { return counts[k].written > 0 }) {
		return errors.New("stale vectors: run make vectors and review the diff")
	}
	return nil
}

type action int

const (
	actionNone action = iota
	actionWritten
	actionFrozen
)

// process regenerates one vector's expected value and rewrites its file (under root, the
// contracts directory) when needed.
func process(opt options, root *os.Root, v *vectors.Vector) (action, error) {
	if err := v.Check(); err != nil {
		return actionNone, err
	}
	out, err := vectors.Run(v.Kind, v.Input, root.FS())
	if err != nil {
		return actionNone, err
	}
	// Hand-written kinds (decide, detectors) are checked against the reference, never generated.
	if !vectors.Kinds[v.Kind].Mechanical {
		return checkSemantic(opt, root, v, out)
	}
	want := vectors.GeneratedExpected(v.Kind, out)
	same, err := sameJSON(v.Expected, want)
	if err != nil {
		return actionNone, err
	}
	if v.Status == vectors.StatusFrozen {
		if !same {
			return actionNone, errors.New("frozen vector no longer matches the reference; add a new vector instead of changing it")
		}
		return actionNone, nil
	}
	stale := !same || v.GeneratedBy == ""
	if opt.check {
		if stale {
			return actionWritten, nil
		}
		return actionNone, nil
	}
	if !stale && !opt.freeze {
		return actionNone, nil
	}
	name := filepath.FromSlash(v.Path)
	info, err := root.Stat(name)
	if err != nil {
		return actionNone, err
	}
	src, err := root.ReadFile(name)
	if err != nil {
		return actionNone, err
	}
	edit := fileEdit{}
	if stale {
		edit.expected = &want
		gen := "cmd/vectorgen@" + opt.rev
		edit.generatedBy = &gen
	}
	if opt.freeze {
		status := vectors.StatusFrozen
		edit.status = &status
	}
	updated, err := rewrite(src, edit)
	if err != nil {
		return actionNone, err
	}
	// The vector file is a repository source: it keeps its permissions.
	if err := root.WriteFile(name, updated, info.Mode().Perm()); err != nil {
		return actionNone, err
	}
	if opt.freeze {
		return actionFrozen, nil
	}
	return actionWritten, nil
}

// checkSemantic finishes process for a hand-written vector: out, the reference output, must agree
// with expected as the runners compare it (a subset, numbers within tolerance). A disagreement is
// an error for draft and frozen vectors alike, as in TestVectors. With -freeze, an agreeing draft of
// a kind named in -kinds is marked frozen (freezing records that a person reviewed it); expected
// itself is never written.
func checkSemantic(opt options, root *os.Root, v *vectors.Vector, out any) (action, error) {
	var expected any
	if err := json.Unmarshal(v.Expected, &expected); err != nil {
		return actionNone, fmt.Errorf("expected: %w", err)
	}
	if expected == nil {
		return actionNone, errors.New("expected is null: semantic vectors are written by hand (contracts/vectors/README.md)")
	}
	if diffs := vectors.Compare(expected, out, v.Tol()); len(diffs) > 0 {
		return actionNone, fmt.Errorf("the reference disagrees with this %s vector:\n  %s", v.Status, strings.Join(diffs, "\n  "))
	}
	if v.Status == vectors.StatusFrozen || !opt.freeze || !opt.kinds[v.Kind] {
		return actionNone, nil
	}
	name := filepath.FromSlash(v.Path)
	info, err := root.Stat(name)
	if err != nil {
		return actionNone, err
	}
	src, err := root.ReadFile(name)
	if err != nil {
		return actionNone, err
	}
	status := vectors.StatusFrozen
	updated, err := rewrite(src, fileEdit{status: &status})
	if err != nil {
		return actionNone, err
	}
	// The vector file is a repository source: it keeps its permissions.
	if err := root.WriteFile(name, updated, info.Mode().Perm()); err != nil {
		return actionNone, err
	}
	return actionFrozen, nil
}

// gitRevision returns the short HEAD revision of the repository holding dir, or "unknown".
func gitRevision(dir string) string {
	// #nosec G204 -- a fixed git command; dir is the -contracts directory the developer passed.
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
