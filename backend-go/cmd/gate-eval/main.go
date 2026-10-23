// Command gate-eval runs the gateway's fast check (SPEC §3.1) over a JSONL file of examples, the
// way the proxy gates a response: Tier-0 detectors, compile, the decision-model call, and decide()
// (which normalizes the answers). The Python eval harness uses it as the `gate` system (SPEC §3.2
// evals/judges.py, make bench-lite).
//
// Each input line is an example:
//
//	{"id": "q1", "query": "...", "response": "...", "context": ["document text", ...],
//	 "labels": {"groundedness@1": true}, "label_hallucinated": true}
//
// query/user_request, response/answer, system_instructions, context/evidence (strings, or items
// {id, source, text, untrusted}), rules (the request's trustagent.rules, always honoured),
// max_marker and scenario (mock backend) are read; labels, label_hallucinated, label_source, domain
// and meta are passed through to the output line. Unknown members are an error for that line.
//
// Each output line, in input order, carries the decision/v1 core (status, action, would_action,
// verdict, max_risk, reasons and per-evaluator probabilities, risk_raw, risk and level), the
// detectors, what compile sent, the decision-model usage, the example's cost and its latency.
// Lines with the mock backend say simulated: true: their probabilities are not model output.
//
// --max-cost-usd is a hard cap: no decision-model call starts unless twice its estimated cost fits
// in what is left, and once a call is refused every remaining example is written as
// skipped: over_budget (the benchmark's excluded_over_budget) and the exit status is 3. A paid
// backend requires the cap and a price above 0 to reserve with; the cap and the price must be
// finite. The backend and model resolve as the proxy's do
// (TRUTHTABLE_JEV_BACKEND, TRUTHTABLE_JEV_MODEL, OPENROUTER_API_KEY, TYPESAFE_API_KEY,
// TRUTHTABLE_JEV_API_KEY, TRUTHTABLE_JEV_BASE_URL, TRUTHTABLE_JEV_PATH,
// TRUTHTABLE_OPENROUTER_BASE_URL); --backend and --model override them.
//
// Every output line names the host its decision-model calls went to (endpoint_host, a bare
// hostname; null for the mock backend), so the harness can tell a vendor from a stand-in such as
// mock-upstream (D203), and how the connection was made (connection): warm when --warm made the
// free key check (OpenRouter GET /api/v1/key) on the same client before the first example, so the
// examples' latency leaves out the TCP and TLS setup (D205); cold otherwise. --policy builtin uses
// the built-in policy (the defaults route) and ignores TRUTHTABLE_POLICY_FILE (D204).
//
//	gate-eval --in ragtruth.jsonl --out gate.jsonl --route default --evaluators groundedness@1,contradiction@1 --max-cost-usd 1
//	gate-eval --policy builtin --warm --concurrency 1 --in calib.jsonl --out gate.jsonl --max-cost-usd 0.30
//	gate-eval --backend mock --in golden.jsonl
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/studio"
)

// Exit statuses.
const (
	exitOK         = 0
	exitFailed     = 1
	exitUsage      = 2
	exitOverBudget = 3
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

type options struct {
	in, out      string
	policyFile   string
	route        string
	evaluators   string
	specFiles    []string
	mode         string
	backend      string
	model        string
	maxCostUSD   float64
	pricePerMTok float64
	concurrency  int
	timeout      time.Duration
	warm         bool
}

// policyBuiltin is the --policy value that selects the built-in policy (the defaults route only),
// whatever TRUTHTABLE_POLICY_FILE says.
const policyBuiltin = "builtin"

// builtinPolicyDoc is the built-in policy document (policy.LoadFile's for a missing file).
const builtinPolicyDoc = "version: 1\n"

// Connection values of an output line (D205).
const (
	connectionWarm = "warm"
	connectionCold = "cold"
)

// warmTimeout bounds the --warm key check.
const warmTimeout = 15 * time.Second

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	fl := flag.NewFlagSet("gate-eval", flag.ContinueOnError)
	fl.SetOutput(stderr)
	var o options
	var specs multiFlag
	fl.StringVar(&o.in, "in", "-", "examples JSONL file (- = stdin)")
	fl.StringVar(&o.out, "out", "-", "results JSONL file (- = stdout)")
	fl.StringVar(&o.policyFile, "policy", envOr(getenv, "TRUTHTABLE_POLICY_FILE", "config/policies.yaml"), "policy file (a missing file means the built-in defaults); builtin = the built-in policy, whatever TRUTHTABLE_POLICY_FILE says")
	fl.StringVar(&o.route, "route", policy.DefaultRouteName, "route whose settings gate the examples")
	fl.StringVar(&o.evaluators, "evaluators", "", "comma-separated evaluator refs replacing the route's evaluators (active stage, spec thresholds)")
	fl.Var(&specs, "spec", "custom evaluator spec file (evaluator/v1 JSON); repeatable")
	fl.StringVar(&o.mode, "mode", "", "mode override: observe, flag, block or fallback (default: the route's mode); would_action does not depend on it")
	fl.StringVar(&o.backend, "backend", envOr(getenv, "TRUTHTABLE_JEV_BACKEND", jev.BackendAuto), "decision backend (auto, mock, openrouter-decisions, openrouter-systemone, typesafe, compatible)")
	fl.StringVar(&o.model, "model", getenv("TRUTHTABLE_JEV_MODEL"), "decision model (default: the route's pin, else the backend default)")
	fl.Float64Var(&o.maxCostUSD, "max-cost-usd", 0, "hard cap on decision-model spend in USD (required for a paid backend; 0 = none, mock only)")
	fl.Float64Var(&o.pricePerMTok, "price-per-mtok", envFloat(getenv, "TRUTHTABLE_JEV_PRICE_PER_MTOK", jev.DefaultPricePerMTok), "input price in USD per million tokens, for cost reservations")
	fl.IntVar(&o.concurrency, "concurrency", 4, "examples evaluated at once")
	fl.DurationVar(&o.timeout, "timeout", 30*time.Second, "timeout of one example's decision-model calls")
	fl.BoolVar(&o.warm, "warm", false, "make the free key check (OpenRouter GET /api/v1/key) before the first example, so the examples find a warm connection")
	fl.Usage = func() {
		fmt.Fprintln(stderr, "usage: gate-eval [--in examples.jsonl] [--out results.jsonl] [--route name] [--evaluators refs] [--backend b] [--model m] --max-cost-usd USD")
		fl.PrintDefaults()
	}
	if err := fl.Parse(args); err != nil {
		return exitUsage
	}
	o.specFiles = specs
	if fl.NArg() > 0 || o.concurrency < 1 || o.timeout <= 0 {
		fl.Usage()
		return exitUsage
	}
	// NaN and infinities compare false against every bound, so they would switch the cap off.
	if !finiteNonNegative(o.maxCostUSD) || !finiteNonNegative(o.pricePerMTok) {
		fmt.Fprintf(stderr, "gate-eval: --max-cost-usd (%v) and --price-per-mtok or TRUTHTABLE_JEV_PRICE_PER_MTOK (%v) must be finite and not negative\n",
			o.maxCostUSD, o.pricePerMTok)
		return exitUsage
	}
	if err := distinctFiles(o.in, o.out); err != nil {
		fmt.Fprintf(stderr, "gate-eval: %v\n", err)
		return exitUsage
	}

	ev, err := setup(&o, getenv)
	if err != nil {
		fmt.Fprintf(stderr, "gate-eval: %v\n", err)
		if errors.Is(err, errUsage) {
			return exitUsage
		}
		return exitFailed
	}

	if o.warm {
		conn, err := warmUp(ctx, ev.Client, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "gate-eval: %v\n", err)
			return exitFailed
		}
		ev.connection = conn
	}

	in, closeIn, err := openInput(o.in, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "gate-eval: %v\n", err)
		return exitFailed
	}
	defer closeIn()
	out, closeOut, err := openOutput(o.out, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "gate-eval: %v\n", err)
		return exitFailed
	}

	s, err := evaluateAll(ctx, ev, in, out, o.concurrency)
	if cerr := closeOut(); err == nil {
		err = cerr
	}
	fmt.Fprintln(stderr, s.String(ev))
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "gate-eval: %v\n", err)
		return exitFailed
	case s.overBudget > 0:
		return exitOverBudget
	}
	return exitOK
}

// errUsage marks configuration errors that are the caller's to fix.
var errUsage = errors.New("usage")

// setup resolves the policy route, the evaluators and the decision-model backend.
func setup(o *options, getenv func(string) string) (*evaluator, error) {
	registry, err := evaluators.Builtins()
	if err != nil {
		return nil, fmt.Errorf("evaluator templates: %w", err)
	}
	specs := map[string]*decision.Spec{}
	for ref, s := range registry.Specs() {
		specs[ref] = s
	}
	for _, path := range o.specFiles {
		b, err := os.ReadFile(path) // #nosec G304 -- a spec file the operator named on the command line
		if err != nil {
			return nil, err
		}
		s, err := decision.ParseSpec(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if _, dup := specs[s.Ref()]; dup {
			return nil, fmt.Errorf("%w: %s: %s is already defined", errUsage, path, s.Ref())
		}
		specs[s.Ref()] = s
	}
	resolver := specResolver(specs)

	pol, err := loadPolicy(o.policyFile, resolver)
	if err != nil {
		return nil, err
	}
	route := pol.Default
	if o.route != policy.DefaultRouteName {
		i := slices.IndexFunc(pol.Routes, func(r *policy.Route) bool { return r.Name == o.route })
		if i < 0 {
			return nil, fmt.Errorf("%w: no route %q in %s", errUsage, o.route, o.policyFile)
		}
		route = pol.Routes[i]
	}
	rc := *route
	if o.evaluators != "" {
		rc.Evaluators = nil
		for _, ref := range strings.Split(o.evaluators, ",") {
			ref = strings.TrimSpace(ref)
			if _, ok := specs[ref]; !ok {
				return nil, fmt.Errorf("%w: unknown evaluator %q", errUsage, ref)
			}
			if slices.ContainsFunc(rc.Evaluators, func(e policy.EvaluatorRef) bool { return e.Ref == ref }) {
				return nil, fmt.Errorf("%w: --evaluators lists %s twice", errUsage, ref)
			}
			rc.Evaluators = append(rc.Evaluators, policy.EvaluatorRef{Ref: ref})
		}
	}
	mode := rc.Mode
	if o.mode != "" {
		if !slices.Contains([]string{decision.ModeObserve, decision.ModeFlag, decision.ModeBlock, decision.ModeFallback}, o.mode) {
			return nil, fmt.Errorf("%w: unknown mode %q", errUsage, o.mode)
		}
		mode = o.mode
	}

	client, err := newJevClient(jev.Config{
		Backend: o.backend, Model: o.model,
		OpenRouterKey: getenv("OPENROUTER_API_KEY"), TypeSafeKey: getenv("TYPESAFE_API_KEY"),
		JevAPIKey: getenv("TRUTHTABLE_JEV_API_KEY"), BaseURL: getenv("TRUTHTABLE_JEV_BASE_URL"),
		Path: getenv("TRUTHTABLE_JEV_PATH"), OpenRouterBaseURL: getenv("TRUTHTABLE_OPENROUTER_BASE_URL"),
		Mock: jev.MockConfig{
			Mode: getenv("TRUTHTABLE_JEV_MOCK_MODE"), Strict: getenv("TRUTHTABLE_JEV_MOCK_STRICT") == "true",
			FixturesPath: config.RepoPath(getenv("TRUTHTABLE_JEV_MOCK_FIXTURES")),
			Seed:         int64(envInt(getenv, "TRUTHTABLE_JEV_MOCK_SEED", 42)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errUsage, err)
	}
	info := client.Info()
	if info.Backend == jev.BackendOff {
		return nil, fmt.Errorf("%w: %s", errNoDecisionModel, info.Reason)
	}
	if info.Backend != jev.BackendMock {
		if o.maxCostUSD <= 0 {
			return nil, fmt.Errorf("%w: the %s backend is paid: set --max-cost-usd", errUsage, info.Backend)
		}
		// Reservations are priced from the token estimate; at price 0 they are free and the cap
		// could only stop the run after the spend had already passed it.
		if o.pricePerMTok <= 0 {
			return nil, fmt.Errorf("%w: the %s backend is paid: --price-per-mtok must be above 0 for --max-cost-usd to be enforced", errUsage, info.Backend)
		}
	}
	model := o.model
	if model == "" {
		model = rc.Decision.Model
	}
	if model == "" {
		model = info.Model
	}
	budget := decision.DefaultBudget()
	budget.Tokens = envInt(getenv, "TRUTHTABLE_JEV_TOKEN_BUDGET", budget.Tokens)
	budget.CPT = envFloat(getenv, "TRUTHTABLE_JEV_CHARS_PER_TOKEN", budget.CPT)
	budget.MaxQuestions = envInt(getenv, "TRUTHTABLE_JEV_MAX_QUESTIONS_PER_CALL", budget.MaxQuestions)
	budget.MaxResponseTokens = rc.Decision.MaxResponseTokens
	budget.EvidenceMaxTokens = rc.Evidence.MaxTokens
	return &evaluator{Runner: &studio.Runner{
		Client: client, Route: &rc, Mode: mode, Model: model, Specs: specs, Budget: budget,
		PricePerMTok: o.pricePerMTok, CallTimeout: o.timeout, Spend: studio.NewSpend(o.maxCostUSD),
		Call: jev.CallOptions{Class: jev.ClassAsync}, Now: time.Now,
	}, endpointHost: endpointHost(info), connection: connectionCold, policySource: pol.Source}, nil
}

// loadPolicy reads the policy file as the gateway does, or the built-in policy for --policy builtin.
func loadPolicy(file string, resolver policy.Resolver) (*policy.Policy, error) {
	if file == policyBuiltin {
		p, err := policy.Parse([]byte(builtinPolicyDoc), policy.DefaultSeeds(), resolver)
		if err != nil {
			return nil, err
		}
		p.Source = policy.SourceBuiltin
		return p, nil
	}
	return policy.LoadFile(config.RepoPath(file), policy.DefaultSeeds(), resolver)
}

// endpointHost is the bare hostname of the decision endpoint (D203: never a URL), nil for a
// backend without one (mock).
func endpointHost(info jev.Info) *string {
	if info.Endpoint == "" {
		return nil
	}
	u, err := url.Parse(info.Endpoint)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	h := strings.ToLower(u.Hostname())
	return &h
}

// warmUp makes the free key check on the decision client before the first example (D205), so the
// examples' calls reuse its connection and their latency leaves out the TCP and TLS setup. Backends
// without a free key endpoint (TypeSafe, compatible gateways, mock) stay cold. A key the vendor
// refuses (auth) is an error: every example would fail; any other failure leaves the run cold.
func warmUp(ctx context.Context, c jev.Client, stderr io.Writer) (string, error) {
	kc, ok := c.(jev.KeyChecker)
	if !ok {
		return connectionCold, nil
	}
	ctx, cancel := context.WithTimeout(ctx, warmTimeout)
	defer cancel()
	if _, err := kc.CheckKey(ctx); err != nil {
		switch kind := jev.ErrorKind(err); {
		case errors.Is(err, jev.ErrKeyCheckUnsupported):
			return connectionCold, nil
		case kind == jev.KindAuth:
			return "", fmt.Errorf("--warm: the key check was refused (%s): fix the key before any example runs (docs/JEV-ACCESS.md)", kind)
		default:
			fmt.Fprintf(stderr, "gate-eval: --warm: the key check failed (%s); the examples start on a cold connection\n", kind)
			return connectionCold, nil
		}
	}
	return connectionWarm, nil
}

// newJevClient builds the decision-model client; tests replace it with a fake paid backend.
var newJevClient = jev.New

// finiteNonNegative reports whether x is a finite number >= 0.
func finiteNonNegative(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0 }

// distinctFiles refuses an output file that is the input file: opening the output truncates it
// before the examples are read.
func distinctFiles(in, out string) error {
	if in == "-" || out == "-" {
		return nil
	}
	a, err := os.Stat(in)
	if err != nil {
		return nil // openInput reports it
	}
	if b, err := os.Stat(out); err == nil && os.SameFile(a, b) {
		return fmt.Errorf("--out %s is the --in file: writing it would erase the examples", out)
	}
	return nil
}

// specResolver resolves refs for policy validation.
type specResolver map[string]*decision.Spec

func (r specResolver) Get(ref string) (*decision.Spec, bool) {
	s, ok := r[ref]
	return s, ok
}

// stats summarize a run.
type stats struct {
	examples, evaluated, errors, overBudget int
	costUSD                                 float64
	costSources                             map[string]bool
	snapshots                               map[string]bool
	latencies                               []int64
}

// String is the plain-language run summary written to stderr.
func (s *stats) String(ev *evaluator) string {
	var b strings.Builder
	fmt.Fprintf(&b, "gate-eval: %d examples: %d evaluated, %d errors, %d over budget", s.examples, s.evaluated, s.errors, s.overBudget)
	backend := ev.Client.Info().Backend
	fmt.Fprintf(&b, "; backend %s", backend)
	if snaps := sortedKeys(s.snapshots); len(snaps) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(snaps, ", "))
	}
	if ev.endpointHost != nil {
		fmt.Fprintf(&b, " at %s, %s connection", *ev.endpointHost, ev.connection)
	}
	if ev.policySource == policy.SourceBuiltin {
		b.WriteString("; built-in policy")
	}
	fmt.Fprintf(&b, "; cost $%.6f", s.costUSD)
	if src := sortedKeys(s.costSources); len(src) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(src, ", "))
	}
	if ev.Spend.Cap() > 0 {
		fmt.Fprintf(&b, " of a $%.2f cap", ev.Spend.Cap())
	}
	if n := len(s.latencies); n > 0 {
		slices.Sort(s.latencies)
		fmt.Fprintf(&b, "; latency p50 %d ms, p95 %d ms", s.latencies[(n-1)/2], s.latencies[(n*95+99)/100-1])
	}
	if backend == jev.BackendMock {
		b.WriteString("\nSimulated: mock decision model, not model output. Do not report these numbers as measured.")
	}
	if s.overBudget > 0 {
		fmt.Fprintf(&b, "\nStopped at the cost cap: %d examples were not evaluated (excluded_over_budget).", s.overBudget)
	}
	return b.String()
}

func (s *stats) add(r *result) {
	s.examples++
	switch {
	case r.Skipped != nil:
		s.overBudget++
		return
	case r.Error != nil:
		s.errors++
		return
	}
	s.evaluated++
	s.costUSD += r.CostUSD
	s.latencies = append(s.latencies, r.LatencyMs)
	if u := r.DecisionModel; u != nil {
		s.costSources[u.CostSource] = true
		if u.Snapshot != nil {
			s.snapshots[*u.Snapshot] = true
		}
	}
}

// evaluateAll reads every example, evaluates them with up to concurrency workers and writes the
// results in input order as they complete.
func evaluateAll(ctx context.Context, ev *evaluator, in io.Reader, out io.Writer, concurrency int) (*stats, error) {
	type item struct {
		line int
		ex   *example
		err  error
	}
	var items []item
	seen := map[string]int{}
	r := bufio.NewReader(in)
	for line := 1; ; line++ {
		b, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(b)) > 0 {
			ex, perr := parseExample(b)
			if perr == nil {
				if first, dup := seen[ex.line.ID]; dup {
					perr = fmt.Errorf("invalid example: id %q repeats line %d", ex.line.ID, first)
				} else {
					seen[ex.line.ID] = line
				}
			}
			items = append(items, item{line: line, ex: ex, err: perr})
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return &stats{}, err
		}
	}

	s := &stats{costSources: map[string]bool{}, snapshots: map[string]bool{}}
	results := make([]*result, len(items))
	w := bufio.NewWriter(out)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	var mu sync.Mutex
	next := 0
	var writeErr error
	flush := func() {
		for next < len(results) && results[next] != nil {
			if writeErr == nil {
				writeErr = enc.Encode(results[next])
			}
			s.add(results[next])
			results[next] = nil // written; release the memory
			next++
		}
		if writeErr == nil {
			writeErr = w.Flush()
		}
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, it := range items {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			var res *result
			if it.err != nil {
				res = fail(ev.newResult(it.line, it.ex), "%v", it.err)
				if res.ID == "" {
					res.ID = "line:" + strconv.Itoa(it.line)
				}
			} else {
				res = ev.evaluate(ctx, it.line, it.ex)
			}
			mu.Lock()
			results[i] = res
			flush()
			mu.Unlock()
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return s, fmt.Errorf("interrupted after %d of %d examples: %w", s.examples, len(items), err)
	}
	return s, writeErr
}

func openInput(path string, stdin io.Reader) (io.Reader, func(), error) {
	if path == "-" {
		return stdin, func() {}, nil
	}
	f, err := os.Open(path) // #nosec G304 -- the examples file the operator named on the command line
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

func openOutput(path string, stdout io.Writer) (io.Writer, func() error, error) {
	if path == "-" {
		return stdout, func() error { return nil }, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- the results file the operator named
	if err != nil {
		return nil, nil, err
	}
	return f, f.Close, nil
}

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(getenv func(string) string, key string, def int) int {
	if v, err := strconv.Atoi(getenv(key)); err == nil {
		return v
	}
	return def
}

func envFloat(getenv func(string) string, key string, def float64) float64 {
	if v, err := strconv.ParseFloat(getenv(key), 64); err == nil {
		return v
	}
	return def
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
