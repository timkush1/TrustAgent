// Command trafficgen sends the scripted scenarios of contracts/demo/scenarios.v1.json through a
// TrustAgent gateway (SPEC §3.1): the demo stack's live traffic, the S0 exit check and the
// M-Demo gate.
//
// Each scenario request goes to its /v1 path (chat completions, completions or Anthropic
// messages, streamed when the scenario streams) with the headers X-TrustAgent-Key
// (TRAFFICGEN_API_KEY) and X-TrustAgent-Scenario, and with the scripted answer as test_response,
// so the gateway must run with TRUTHTABLE_TEST_MODE=true. --upstream sends some or all scenarios
// without test_response, so the gateway's upstream answers: a real model when recording
// (generated), or mock-upstream in the e2e stack (all).
//
// Scenarios tagged with a later gateway phase than --phase (default: this build's) are skipped,
// so an older gateway can still be driven with `--phase p2`. With --expect, a skipped scenario
// fails the check (exit status 1), because a check that skips a scenario has not tested it;
// --allow-skip accepts the skips.
//
// With --expect, trafficgen checks the decision the response carries (headers; for streams also
// the trailers and the `: trustagent {...}` comment), the HTTP status and the delivered text
// against each scenario's expect, then polls GET /api/traces/:id until the deep audit settles and
// checks the outcome, the audit verdict, the claim verdicts and, for observe routes (Status:
// deferred), the stored decision. Any mismatch is printed and the exit status is 1. A generated
// scenario answered by a real upstream (--upstream generated) has no expectations: its trace is
// awaited until it settles, then left unchecked. Under --upstream all, mock-upstream answers a
// generated scenario with its stand-in response, so its expectations are checked like any other.
//
// --record is a recording run (make record-demo, D121): it first reads GET /api/status and refuses
// a gateway that is not in test mode, whose decision model is the mock or off, answers from a local
// endpoint such as mock-upstream or is in no state to answer (a rejected key, no credits, an open
// breaker), or whose judge is not up, is the mock or runs on its fallback, since such traffic could
// only be labelled simulated or would hold no real decision. It then checks like --expect but reports a mismatch as drift without failing
// (a real model's verdicts may differ from the scripted ones); a request that fails or a trace that
// does not settle still fails the run. --trace-ids-out writes the trace id of every answered
// request, one per line in send order, so the export can be checked against the run.
//
// --pack faults sends the vendor-fault scenarios of contracts/demo/faults.v1.json (D119) instead:
// mock-upstream, the e2e stack's decision backend and LLM upstream, answers each with the fault its
// mock_faults names. Their expectations also check X-TrustAgent-Degraded and the stored decision's
// degraded (expect.degraded) and decision_model attempts, hedged and late (expect.decision_model);
// a trace that expects a late decision is awaited until it is recorded.
//
//	trafficgen --once --expect --scenarios s0
//	trafficgen --once --expect --scenarios e2e --upstream all
//	trafficgen --once --expect --pack faults --scenarios faults --upstream all
//	trafficgen --record --scenarios demo --exclude knowledge --upstream generated --trace-ids-out ids.txt
//	trafficgen --seed-knowledge --admin-key-file admin.key
//
// --seed-knowledge sends no traffic: it uploads the knowledge documents of the selected scenarios
// (default: set knowledge) to the gateway's knowledge base (POST /api/kb/documents, with the admin
// key read from --admin-key-file), the documents a knowledge stack must hold before its scenarios
// run (D131).
//
//	trafficgen --loop --scenarios demo --base-url http://proxy:8080 --interval 20s
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Exit statuses.
const (
	exitOK       = 0
	exitMismatch = 1
	exitUsage    = 2
)

// keyEnv names the environment variable of the ingest key trafficgen sends.
const keyEnv = "TRAFFICGEN_API_KEY"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

type options struct {
	baseURL       string
	sets          string
	exclude       string
	phase         string
	upstream      string
	pack          string
	file          string
	once, loop    bool
	expect        bool
	allowSkip     bool
	record        bool
	traceIDsOut   string
	seedKnowledge bool
	adminKeyFile  string
	interval      time.Duration
	auditTimeout  time.Duration
	timeout       time.Duration
	key           string
}

// --upstream values: which scenarios get their answer from the gateway's upstream instead of
// test_response.
const (
	upstreamNone      = "none"
	upstreamGenerated = "generated"
	upstreamAll       = "all"
)

// fromUpstream reports whether s is sent without test_response.
func (o *options) fromUpstream(s *scenario) bool {
	return o.upstream == upstreamAll || o.upstream == upstreamGenerated && s.AnswerSource == answerGenerated
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fl := flag.NewFlagSet("trafficgen", flag.ContinueOnError)
	fl.SetOutput(stderr)
	var o options
	fl.StringVar(&o.baseURL, "base-url", "http://127.0.0.1:8080", "gateway base URL")
	fl.StringVar(&o.sets, "scenarios", "demo", "comma-separated scenario sets or ids to send (s0, demo, e2e, untagged, generated, p3, knowledge, all, ...)")
	fl.StringVar(&o.exclude, "exclude", "", "comma-separated scenario sets or ids to leave out")
	fl.StringVar(&o.phase, "phase", defaultPhase, "gateway phase the target implements ("+strings.Join(phases, ", ")+"); scenarios tagged with a later phase are skipped")
	fl.StringVar(&o.upstream, "upstream", upstreamNone, "scenarios whose answer comes from the gateway's upstream instead of test_response: none, generated (recording) or all (e2e with mock-upstream)")
	fl.StringVar(&o.pack, "pack", packMain, "embedded scenario pack: main (contracts/demo/scenarios.v1.json) or faults (contracts/demo/faults.v1.json, vendor faults on mock-upstream)")
	fl.StringVar(&o.file, "file", "", "scenario pack file to read instead of an embedded --pack")
	fl.BoolVar(&o.once, "once", false, "send the scenarios once and exit (the default)")
	fl.BoolVar(&o.loop, "loop", false, "send the scenarios every --interval until stopped")
	fl.BoolVar(&o.expect, "expect", false, "check the gate headers and the deep audit against each scenario's expect")
	fl.BoolVar(&o.allowSkip, "allow-skip", false, "with --expect, pass although selected scenarios are skipped because they need a later gateway phase than --phase")
	fl.BoolVar(&o.record, "record", false, "a recording run: refuse a gateway whose decision model or judge is the mock or a fallback (GET /api/status), wait for every trace to settle and report expectation drift without failing")
	fl.StringVar(&o.traceIDsOut, "trace-ids-out", "", "write the trace id of every answered request to this file, one per line in send order")
	fl.BoolVar(&o.seedKnowledge, "seed-knowledge", false, "send no traffic: upload the knowledge documents of the selected scenarios (default set knowledge) to the knowledge base, with the admin key of --admin-key-file")
	fl.StringVar(&o.adminKeyFile, "admin-key-file", "", "file holding the admin key --seed-knowledge uploads with")
	fl.DurationVar(&o.interval, "interval", 20*time.Second, "pause between rounds with --loop")
	fl.DurationVar(&o.auditTimeout, "audit-timeout", 90*time.Second, "how long --expect waits for a deep audit to settle")
	fl.DurationVar(&o.timeout, "timeout", 30*time.Second, "timeout of one HTTP request")
	fl.Usage = func() {
		fmt.Fprintln(stderr, "usage: trafficgen [--once | --loop [--interval 20s]] [--expect [--allow-skip] | --record] [--trace-ids-out FILE] [--scenarios s0] [--exclude sets] [--phase p3] [--upstream none] [--base-url URL] [--pack main | --file pack.json]")
		fmt.Fprintln(stderr, "       trafficgen --seed-knowledge --admin-key-file FILE [--scenarios knowledge] [--base-url URL]")
		fmt.Fprintln(stderr, "The ingest key is read from "+keyEnv+"; the gateway must run with TRUTHTABLE_TEST_MODE=true.")
		fl.PrintDefaults()
	}
	if err := fl.Parse(args); err != nil {
		return exitUsage
	}
	_, knownPack := packFiles[o.pack]
	if fl.NArg() > 0 || (o.once && o.loop) || o.interval <= 0 || o.auditTimeout <= 0 || o.timeout <= 0 ||
		!slices.Contains(phases, o.phase) || !slices.Contains([]string{upstreamNone, upstreamGenerated, upstreamAll}, o.upstream) ||
		!knownPack || (o.file != "" && o.pack != packMain) ||
		// A recording run sends each scenario once, through a real upstream at most for its
		// generated answers; a loop never finishes its list of trace ids.
		(o.record && (o.loop || o.upstream == upstreamAll)) || (o.traceIDsOut != "" && o.loop) ||
		// A seeding run sends no traffic, so nothing about traffic applies to it.
		(o.seedKnowledge && (o.adminKeyFile == "" || o.loop || o.expect || o.record || o.traceIDsOut != "")) ||
		(!o.seedKnowledge && o.adminKeyFile != "") {
		fl.Usage()
		return exitUsage
	}
	if o.record {
		o.expect = true
	}
	base, err := url.Parse(o.baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		fmt.Fprintf(stderr, "trafficgen: --base-url %q is not an http(s) URL\n", o.baseURL)
		return exitUsage
	}
	o.baseURL = strings.TrimRight(o.baseURL, "/")
	o.key = strings.TrimSpace(getenv(keyEnv))

	all, err := loadPack(o.pack, o.file)
	if err != nil {
		fmt.Fprintln(stderr, "trafficgen:", err)
		return exitUsage
	}
	if o.seedKnowledge {
		sets := knowledgeSet
		fl.Visit(func(f *flag.Flag) {
			if f.Name == "scenarios" {
				sets = o.sets
			}
		})
		return seedRun(ctx, o, all, sets, stdout, stderr)
	}
	selected, err := selectScenarios(all, o.sets, o.exclude)
	if err != nil {
		fmt.Fprintln(stderr, "trafficgen:", err)
		return exitUsage
	}
	selected, skipped := byPhase(selected, o.phase)
	for _, s := range skipped {
		fmt.Fprintf(stderr, "trafficgen: skipping %s: it needs gateway phase %s (this run: --phase %s)\n", s.ID, scenarioPhase(s.Sets), o.phase)
	}
	if len(selected) == 0 {
		fmt.Fprintf(stderr, "trafficgen: every scenario in %q needs a later gateway phase than %s\n", o.sets, o.phase)
		return exitUsage
	}
	// A check that skips a selected scenario has not tested it (D18): the skips fail the check,
	// reported like a mismatch, unless --allow-skip accepts them.
	skipFails := o.expect && len(skipped) > 0 && !o.allowSkip
	if skipFails {
		fmt.Fprintf(stderr, "trafficgen: --expect skips %d selected scenario(s) that need a later gateway phase than %s, so the check fails; pass --allow-skip to accept that\n",
			len(skipped), o.phase)
	}
	g := &generator{opts: o, client: &http.Client{Timeout: o.timeout}, out: stdout}
	if o.key == "" {
		fmt.Fprintln(stderr, "trafficgen: "+keyEnv+" is not set; requests carry no X-TrustAgent-Key")
	}
	if o.record {
		if err := g.recordable(ctx); err != nil {
			fmt.Fprintln(stderr, "trafficgen: --record refused:", err)
			return exitUsage
		}
	}

	if !o.loop {
		ok, results := g.round(ctx, selected)
		if o.traceIDsOut != "" {
			if err := writeTraceIDs(o.traceIDsOut, results); err != nil {
				fmt.Fprintln(stderr, "trafficgen: --trace-ids-out:", err)
				return exitMismatch
			}
		}
		if ok && !skipFails {
			return exitOK
		}
		return exitMismatch
	}
	for {
		g.round(ctx, selected)
		select {
		case <-ctx.Done():
			return exitOK
		case <-time.After(o.interval):
		}
	}
}

// seedRun is a --seed-knowledge run: the knowledge documents of the scenarios sets selects (phase
// tags do not matter: nothing is sent to /v1), uploaded once.
func seedRun(ctx context.Context, o options, all []scenario, sets string, stdout, stderr io.Writer) int {
	selected, err := selectScenarios(all, sets, o.exclude)
	if err != nil {
		fmt.Fprintln(stderr, "trafficgen:", err)
		return exitUsage
	}
	docs, err := knowledgeDocs(selected)
	if err != nil {
		fmt.Fprintln(stderr, "trafficgen: --seed-knowledge:", err)
		return exitUsage
	}
	key, err := readAdminKey(o.adminKeyFile)
	if err != nil {
		fmt.Fprintln(stderr, "trafficgen: --admin-key-file:", err)
		return exitUsage
	}
	g := &generator{opts: o, client: &http.Client{Timeout: o.timeout}, out: stdout}
	res, err := g.seedKnowledge(ctx, docs, key)
	if err != nil {
		fmt.Fprintln(stderr, "trafficgen: --seed-knowledge:", err)
		return exitMismatch
	}
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	fmt.Fprintf(stdout, "trafficgen: seeded %d knowledge document(s) (%s): %d ingested, %d claims accepted, %d quarantined, %d conflicts\n",
		len(docs), strings.Join(ids, ", "), res.DocumentsIngested, res.ClaimsAccepted, res.ClaimsQuarantined, res.ConflictsDetected)
	if res.DocumentsIngested != len(docs) {
		fmt.Fprintf(stderr, "trafficgen: --seed-knowledge: the engine ingested %d of %d documents\n", res.DocumentsIngested, len(docs))
		return exitMismatch
	}
	return exitOK
}

// generator sends scenarios and checks their outcomes.
type generator struct {
	opts   options
	client *http.Client
	out    io.Writer
	seq    int
}

// sent is a scenario whose request was answered.
type sent struct {
	s       *scenario
	traceID string
	ms      int64
	fails   []mismatch
	err     error
	// storedDecision is set when the response did not carry the whole decision (Status: deferred
	// in observe mode, or a stream), so the decision is also checked on the stored trace.
	storedDecision bool
	// unchecked is set for a generated answer that came from a real upstream (--upstream
	// generated): a real model's answer has no expectations. Its trace is still awaited.
	unchecked bool
}

// round sends every scenario once and, with --expect, waits for each trace to settle and checks
// it (a generated upstream answer is only awaited). It reports whether every scenario passed, and
// the results in send order. In a recording run a mismatch is drift: printed, not failing.
func (g *generator) round(ctx context.Context, scenarios []scenario) (bool, []*sent) {
	results := make([]*sent, len(scenarios))
	for i := range scenarios {
		results[i] = g.send(ctx, &scenarios[i])
	}
	if g.opts.expect {
		deadline := time.Now().Add(g.opts.auditTimeout)
		for _, r := range results {
			if r.err != nil || r.traceID == "" {
				continue
			}
			// An unchecked trace is still awaited (D121): the run ends when every trace it
			// started is decided and audited, so an export right after it is complete.
			t, err := g.awaitTrace(ctx, r.s, r.traceID, deadline)
			switch {
			case err != nil:
				r.err = err
			case !r.unchecked:
				r.fails = append(r.fails, checkTrace(r.s, t, r.storedDecision)...)
			}
		}
	}
	ok, drift := true, 0
	for _, r := range results {
		switch {
		case r.err != nil:
			ok = false
			fmt.Fprintf(g.out, "FAIL %s: %v\n", r.s.ID, r.err)
			for _, m := range r.fails {
				fmt.Fprintf(g.out, "  %s\n", m)
			}
		case len(r.fails) > 0 && g.opts.record:
			drift++
			fmt.Fprintf(g.out, "drift %s (trace %s, %d ms)\n", r.s.ID, r.traceID, r.ms)
			for _, m := range r.fails {
				fmt.Fprintf(g.out, "  %s\n", m)
			}
		case len(r.fails) > 0:
			ok = false
			fmt.Fprintf(g.out, "FAIL %s (trace %s)\n", r.s.ID, r.traceID)
			for _, m := range r.fails {
				fmt.Fprintf(g.out, "  %s\n", m)
			}
		case g.opts.expect && r.unchecked:
			fmt.Fprintf(g.out, "sent %s (trace %s, %d ms; generated upstream answer, settled, not checked)\n", r.s.ID, r.traceID, r.ms)
		default:
			fmt.Fprintf(g.out, "ok   %s (trace %s, %d ms)\n", r.s.ID, r.traceID, r.ms)
		}
	}
	switch {
	case g.opts.record:
		verdict := "every trace settled"
		if !ok {
			verdict = "some requests failed or did not settle"
		}
		fmt.Fprintf(g.out, "trafficgen: %d scenario(s) recorded: %s; %d drift from their expectations (reported, not failing)\n",
			len(results), verdict, drift)
	case g.opts.expect:
		verdict := "all scenarios match their expectations"
		if !ok {
			verdict = "some scenarios do not match their expectations"
		}
		fmt.Fprintf(g.out, "trafficgen: %d scenario(s): %s\n", len(results), verdict)
	}
	return ok, results
}

// writeTraceIDs writes the trace id of every answered request to path, one per line in send order,
// replacing the file whole (a temporary file renamed into place), so a reader never sees half a list.
func writeTraceIDs(path string, results []*sent) error {
	var b strings.Builder
	for _, r := range results {
		if r.traceID != "" {
			b.WriteString(r.traceID + "\n")
		}
	}
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	f, err := os.CreateTemp(dir, "."+base+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.WriteString(b.String())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, 0o644) // #nosec G302 -- a list of trace ids, read by the recording tooling
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
}

// send posts one scenario and, with --expect, checks the response headers.
func (g *generator) send(ctx context.Context, s *scenario) *sent {
	r := &sent{s: s}
	path, err := s.path()
	if err != nil {
		r.err = err
		return r
	}
	upstream := g.opts.fromUpstream(s)
	body, err := s.body(upstream)
	if err != nil {
		r.err = err
		return r
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.opts.baseURL+path, bytes.NewReader(body))
	if err != nil {
		r.err = err
		return r
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}
	g.seq++
	req.Header.Set("X-TrustAgent-Scenario", s.ID)
	req.Header.Set("X-Request-ID", "trafficgen:"+s.ID+":"+strconv.Itoa(g.seq))
	if g.opts.key != "" {
		req.Header.Set("X-TrustAgent-Key", g.opts.key)
	}
	start := time.Now()
	resp, err := g.client.Do(req)
	if err != nil {
		r.err = fmt.Errorf("POST %s: %w", path, err)
		return r
	}
	defer resp.Body.Close()
	// Reading to EOF also receives the trailers of a streamed answer.
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	r.ms = time.Since(start).Milliseconds()
	r.traceID = resp.Header.Get(headerTraceID)
	e := expectation(s.Expect)
	wantStatus := 0
	if present, null := e.has("http_status"); present && !null {
		_ = json.Unmarshal(e["http_status"], &wantStatus)
	}
	if (resp.StatusCode < 200 || resp.StatusCode > 299) && resp.StatusCode != wantStatus {
		r.err = fmt.Errorf("POST %s: HTTP %d: %s", path, resp.StatusCode, snippet(respBody))
		return r
	}
	if g.opts.expect {
		// Under --upstream all the answer is mock-upstream's stand-in (the scenario's response), so
		// the expectations hold; only a real upstream's generated answer has none.
		if upstream && s.AnswerSource == answerGenerated && g.opts.upstream != upstreamAll {
			r.unchecked = true
			return r
		}
		if r.traceID == "" {
			r.fails = append(r.fails, mismatch{headerTraceID, "a trace id", absent})
		}
		a := readAnswer(s.APIFormat, resp.StatusCode, resp.Header, resp.Trailer, respBody)
		view := newDecisionView(a)
		r.storedDecision = view.deferred() || a.stream
		r.fails = append(r.fails, checkDecision(s, view, a.stream)...)
		r.fails = append(r.fails, checkAnswer(s, a, true)...)
	}
	return r
}

// awaitTrace polls GET /api/traces/:id until the trace of s is decided and its deep audit settled
// (and its late decision recorded, when s expects one), backing off from 100 ms to 1 s (the API is
// rate limited per client).
func (g *generator) awaitTrace(ctx context.Context, s *scenario, traceID string, deadline time.Time) (*traceView, error) {
	wait := 100 * time.Millisecond
	var last *traceView
	var lastErr error
	for {
		t, retryAfter, err := g.getTrace(ctx, traceID)
		switch {
		case err != nil && !errors.Is(err, errRetry):
			return nil, err
		case err == nil && t.settledFor(s):
			return t, nil
		case err == nil:
			last = t
		default:
			lastErr = err
		}
		pause := max(wait, retryAfter)
		if time.Now().Add(pause).After(deadline) {
			switch {
			case last != nil && last.settled():
				return nil, fmt.Errorf("trace %s: no late decision was recorded within --audit-timeout (expect.decision_model.late)", traceID)
			case last != nil:
				return nil, fmt.Errorf("trace %s did not settle within --audit-timeout (gate %s, audit %s)",
					traceID, last.GateStatus, last.Audit.Status)
			case lastErr != nil:
				return nil, fmt.Errorf("trace %s: %w", traceID, lastErr)
			}
			return nil, fmt.Errorf("trace %s did not settle within --audit-timeout", traceID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pause):
		}
		wait = min(2*wait, time.Second)
	}
}

// errRetry marks a trace read worth retrying: not stored yet, or rate limited.
var errRetry = errors.New("retry")

func (g *generator) getTrace(ctx context.Context, traceID string) (*traceView, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.opts.baseURL+"/api/traces/"+url.PathEscape(traceID), nil)
	if err != nil {
		return nil, 0, err
	}
	if g.opts.key != "" {
		req.Header.Set("X-TrustAgent-Key", g.opts.key)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: GET /api/traces/%s: %v", errRetry, traceID, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		var t traceView
		if err := json.Unmarshal(body, &t); err != nil {
			return nil, 0, fmt.Errorf("GET /api/traces/%s: not a TraceDetail: %w", traceID, err)
		}
		return &t, 0, nil
	case http.StatusNotFound:
		return nil, 0, fmt.Errorf("%w: trace %s is not stored yet", errRetry, traceID)
	case http.StatusTooManyRequests:
		retry := time.Second
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			retry = time.Duration(s) * time.Second
		}
		return nil, retry, fmt.Errorf("%w: GET /api/traces/%s is rate limited", errRetry, traceID)
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, 0, fmt.Errorf("GET /api/traces/%s: HTTP %d: %s is not a key with the read scope (%s)",
			traceID, resp.StatusCode, keyEnv, snippet(body))
	}
	return nil, 0, fmt.Errorf("GET /api/traces/%s: HTTP %d: %s", traceID, resp.StatusCode, snippet(body))
}

// snippet is the start of a response body, for error messages.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
