// Command trafficgen sends the scripted scenarios of contracts/demo/scenarios.v1.json through a
// TrustAgent gateway (SPEC §3.1): the demo stack's live traffic and the S0 exit check.
//
// Each scenario request goes to its /v1 path with the scripted answer as test_response and the
// headers X-TrustAgent-Key (TRAFFICGEN_API_KEY) and X-TrustAgent-Scenario, so the gateway must
// run with TRUTHTABLE_TEST_MODE=true. With --expect, trafficgen checks the gate headers against
// each scenario's expect, then polls GET /api/traces/:id until the deep audit settles and checks
// the outcome, the audit verdict and the claim verdicts. Any mismatch is printed and the exit
// status is 1.
//
//	trafficgen --once --expect --scenarios s0
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
	baseURL      string
	sets         string
	file         string
	once, loop   bool
	expect       bool
	interval     time.Duration
	auditTimeout time.Duration
	timeout      time.Duration
	key          string
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fl := flag.NewFlagSet("trafficgen", flag.ContinueOnError)
	fl.SetOutput(stderr)
	var o options
	fl.StringVar(&o.baseURL, "base-url", "http://127.0.0.1:8080", "gateway base URL")
	fl.StringVar(&o.sets, "scenarios", "demo", "comma-separated scenario sets or ids to send (s0, demo, e2e, all, ...)")
	fl.StringVar(&o.file, "file", "", "scenario pack to read (default: the embedded contracts/demo/scenarios.v1.json)")
	fl.BoolVar(&o.once, "once", false, "send the scenarios once and exit (the default)")
	fl.BoolVar(&o.loop, "loop", false, "send the scenarios every --interval until stopped")
	fl.BoolVar(&o.expect, "expect", false, "check the gate headers and the deep audit against each scenario's expect")
	fl.DurationVar(&o.interval, "interval", 20*time.Second, "pause between rounds with --loop")
	fl.DurationVar(&o.auditTimeout, "audit-timeout", 90*time.Second, "how long --expect waits for a deep audit to settle")
	fl.DurationVar(&o.timeout, "timeout", 30*time.Second, "timeout of one HTTP request")
	fl.Usage = func() {
		fmt.Fprintln(stderr, "usage: trafficgen [--once | --loop [--interval 20s]] [--expect] [--scenarios s0] [--base-url URL] [--file pack.json]")
		fmt.Fprintln(stderr, "The ingest key is read from "+keyEnv+"; the gateway must run with TRUTHTABLE_TEST_MODE=true.")
		fl.PrintDefaults()
	}
	if err := fl.Parse(args); err != nil {
		return exitUsage
	}
	if fl.NArg() > 0 || (o.once && o.loop) || o.interval <= 0 || o.auditTimeout <= 0 || o.timeout <= 0 {
		fl.Usage()
		return exitUsage
	}
	base, err := url.Parse(o.baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		fmt.Fprintf(stderr, "trafficgen: --base-url %q is not an http(s) URL\n", o.baseURL)
		return exitUsage
	}
	o.baseURL = strings.TrimRight(o.baseURL, "/")
	o.key = strings.TrimSpace(getenv(keyEnv))

	all, err := loadScenarios(o.file)
	if err != nil {
		fmt.Fprintln(stderr, "trafficgen:", err)
		return exitUsage
	}
	selected, err := selectScenarios(all, o.sets)
	if err != nil {
		fmt.Fprintln(stderr, "trafficgen:", err)
		return exitUsage
	}
	g := &generator{opts: o, client: &http.Client{Timeout: o.timeout}, out: stdout}
	if o.key == "" {
		fmt.Fprintln(stderr, "trafficgen: "+keyEnv+" is not set; requests carry no X-TrustAgent-Key")
	}

	if !o.loop {
		if g.round(ctx, selected) {
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
}

// round sends every scenario once and, with --expect, checks them. It reports whether every
// scenario passed.
func (g *generator) round(ctx context.Context, scenarios []scenario) bool {
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
			t, err := g.awaitTrace(ctx, r.traceID, deadline)
			if err != nil {
				r.err = err
				continue
			}
			r.fails = append(r.fails, checkTrace(r.s, t)...)
		}
	}
	ok := true
	for _, r := range results {
		switch {
		case r.err != nil:
			ok = false
			fmt.Fprintf(g.out, "FAIL %s: %v\n", r.s.ID, r.err)
			for _, m := range r.fails {
				fmt.Fprintf(g.out, "  %s\n", m)
			}
		case len(r.fails) > 0:
			ok = false
			fmt.Fprintf(g.out, "FAIL %s (trace %s)\n", r.s.ID, r.traceID)
			for _, m := range r.fails {
				fmt.Fprintf(g.out, "  %s\n", m)
			}
		default:
			fmt.Fprintf(g.out, "ok   %s (trace %s, %d ms)\n", r.s.ID, r.traceID, r.ms)
		}
	}
	if g.opts.expect {
		verdict := "all scenarios match their expectations"
		if !ok {
			verdict = "some scenarios do not match their expectations"
		}
		fmt.Fprintf(g.out, "trafficgen: %d scenario(s): %s\n", len(results), verdict)
	}
	return ok
}

// send posts one scenario and, with --expect, checks the response headers.
func (g *generator) send(ctx context.Context, s *scenario) *sent {
	r := &sent{s: s}
	path, err := s.path()
	if err != nil {
		r.err = err
		return r
	}
	body, err := s.body()
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
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	r.ms = time.Since(start).Milliseconds()
	r.traceID = resp.Header.Get(headerTraceID)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		r.err = fmt.Errorf("POST %s: HTTP %d: %s", path, resp.StatusCode, snippet(respBody))
		return r
	}
	if g.opts.expect {
		if r.traceID == "" {
			r.fails = append(r.fails, mismatch{headerTraceID, "a trace id", absent})
		}
		r.fails = append(r.fails, checkHeaders(s, resp.Header)...)
	}
	return r
}

// awaitTrace polls GET /api/traces/:id until the trace is decided and its deep audit settled,
// backing off from 100 ms to 1 s (the API is rate limited per client).
func (g *generator) awaitTrace(ctx context.Context, traceID string, deadline time.Time) (*traceView, error) {
	wait := 100 * time.Millisecond
	var last *traceView
	var lastErr error
	for {
		t, retryAfter, err := g.getTrace(ctx, traceID)
		switch {
		case err != nil && !errors.Is(err, errRetry):
			return nil, err
		case err == nil && t.settled():
			return t, nil
		case err == nil:
			last = t
		default:
			lastErr = err
		}
		pause := max(wait, retryAfter)
		if time.Now().Add(pause).After(deadline) {
			switch {
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
