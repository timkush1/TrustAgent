package main

// jevcheck --capture DIR (D211): one sanitized fixture per vendor response the gateway decodes,
// captured from the real vendor with the owner's key, so the synthetic fixtures under
// contracts/fixtures/vendor/openrouter can be checked against what openrouter.ai really sends.
//
//	jevcheck --capture contracts/fixtures/vendor/captured [--max-cost-usd 0.01] [--dry-run]
//
// It captures, in this order (the free calls first, the largest paid call last):
//
//   - GET /api/v1/key (free): the key check's shape, with every amount and the label replaced;
//   - a bogus key (free): the 401 the gateway classifies as auth;
//   - a 200 from each decision path, openrouter-decisions and openrouter-systemone: body parity,
//     key order, the rounding of the probabilities, confidence and the snapshot fields;
//   - a ZDR request (TRUTHTABLE_JEV_ZDR): what OpenRouter answers with provider.zdr set;
//   - a Haiku chat with usage.include: whether usage.cost is present;
//   - a probe of about 30,000 tokens: whether the 32k prompt limit holds.
//
// Every decision call goes through the production client (internal/jev: the same encoder, the same
// decoder) with retries off; a recording transport keeps the bytes it sent and received. The chat
// is decoded with llmapi.ParseChatResponse. Each step's cost is estimated from the price table
// before it is sent, and a step whose estimate exceeds what is left (of --max-cost-usd and of the
// key's own limit, read for free first) is refused, with everything after it. The spend is the
// sum of the usage.cost the vendor reports (the estimate where it reports none).
//
// Sanitizing: the key becomes "sk-or-v1-<redacted>", generation and request ids "<prefix>-
// <redacted>", the key label "sk-or-…xxxx", the key's usage and limit amounts 0 (each listed in
// the fixture's placeholders), long strings (the 30k filler) "<elided: N chars>", and only a short
// list of response headers is kept. Each fixture says source "captured <date> via jevcheck
// --capture" and sanitized true; a file that still matches a secret pattern is deleted and the run
// fails. 402, 429 and 413 answers are never written: those fixtures stay synthetic.
//
// Without a key, or with --dry-run, it prints the plan and its estimate and sends nothing
// (make jev-capture is a dry run unless CONFIRM=1). The last line is TRUSTAGENT_P6_RESULT {json}.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
)

// Capture defaults.
const (
	defaultCaptureCapUSD = 0.01
	// captureChatModel is the judge model the chat capture asks (usage.include: is cost present?).
	captureChatModel = "anthropic/claude-haiku-4.5"
	// captureBogusKey is a key no vendor issued: the 401 capture (mock-upstream answers 401 to any
	// key containing "invalid").
	captureBogusKey = "sk-or-v1-invalid-jevcheck-capture"
	// capture30kTokens is the size of the long probe: under the 32k prompt limit with room for the
	// question.
	capture30kTokens = 30_000
	// captureAttemptTimeout bounds one capture call (the long probe takes longer than a gate call).
	captureAttemptTimeout = 60 * time.Second
	// elideAbove is the longest string a fixture keeps whole.
	elideAbove = 2_000
	// estimateMargin over-estimates every step, so the cap holds when the tokenizer counts more.
	estimateMargin = 1.25
)

// captureFiller is the long probe's evidence, repeated to its size: plain policy text.
const captureFiller = "Refunds are accepted within 30 days of purchase with the original receipt. "

// MaskedKey is what a fixture holds instead of the key.
const MaskedKey = "sk-or-v1-<redacted>"

type captureOptions struct {
	dir        string
	maxCostUSD float64
	dryRun     bool
	timeout    time.Duration
}

// exchange is one HTTP round trip as the recording transport saw it.
type exchange struct {
	method, url           string
	reqHeader, respHeader http.Header
	reqBody, respBody     []byte
	status                int
}

// recorder is an http.RoundTripper that keeps the bytes of every round trip.
type recorder struct {
	base http.RoundTripper
	mu   sync.Mutex
	log  []*exchange
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	x := &exchange{method: req.Method, url: req.URL.String(), reqHeader: req.Header.Clone()}
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		x.reqBody = b
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	resp, err := r.base.RoundTrip(req)
	if err == nil {
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if rerr != nil {
			return nil, rerr
		}
		x.status, x.respHeader, x.respBody = resp.StatusCode, resp.Header.Clone(), b
		resp.Body = io.NopCloser(bytes.NewReader(b))
	}
	r.mu.Lock()
	r.log = append(r.log, x)
	r.mu.Unlock()
	return resp, err
}

// last returns the newest round trip since mark, and the new mark.
func (r *recorder) since(mark int) (*exchange, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.log) <= mark {
		return nil, len(r.log)
	}
	return r.log[len(r.log)-1], len(r.log)
}

// captureStep is one capture.
type captureStep struct {
	// stem names the fixture: <stem>.<status>[_variant].json.
	stem, variant string
	what          string
	method, path  string
	// estimate is the step's cost estimate in USD (0: free).
	estimate float64
	// run sends the step through the production client (which decodes the answer) and returns
	// the cost the vendor reported, if any.
	run func(ctx context.Context, c *capturer) (cost *float64, err error)
	// want is the HTTP status the step expects.
	want int
}

// capturer runs the steps against one OpenRouter origin.
type capturer struct {
	base, key, model string
	getenv           func(string) string
	rec              *recorder
	// snapshot is the snapshot the first decision 200 named.
	snapshot string
}

// noteSnapshot keeps the first snapshot a decision answered with.
func (c *capturer) noteSnapshot(resp *jev.Response) {
	if resp != nil && resp.Snapshot != "" && c.snapshot == "" {
		c.snapshot = resp.Snapshot
	}
}

// client builds the production decision client for backend, with retries off.
func (c *capturer) client(backend, key string, zdr bool) (jev.Client, error) {
	h := jev.DefaultHTTPConfig()
	h.KeepAlive, h.MaxRetries, h.MaxRetriesAsync = 0, 0, 0
	h.AttemptTimeout = captureAttemptTimeout
	h.SendSessionID = false
	h.ZDR = zdr
	h.SpendCapUSDDay = 0
	h.Transport = c.rec
	return jev.New(jev.Config{Backend: backend, Model: c.model, OpenRouterKey: key, OpenRouterBaseURL: c.base, HTTP: &h})
}

// probe sends the connectivity probe through backend.
func (c *capturer) probe(backend string, zdr bool) func(ctx context.Context, _ *capturer) (*float64, error) {
	return func(ctx context.Context, _ *capturer) (*float64, error) {
		cl, err := c.client(backend, c.key, zdr)
		if err != nil {
			return nil, err
		}
		resp, err := jev.Probe(ctx, cl)
		c.noteSnapshot(resp)
		if resp != nil && resp.CostSource == jev.CostReported {
			return resp.CostUSD, err
		}
		return nil, err
	}
}

// longRequest is the probe with about n tokens of evidence.
func longRequest(model string, tokens int) (*jev.Request, error) {
	req, err := jev.ProbeRequest(model)
	if err != nil {
		return nil, err
	}
	chars := int(float64(tokens) * decision.DefaultCPT)
	filler := strings.Repeat(captureFiller, chars/len(captureFiller)+1)[:chars]
	state := make(map[string]any, len(req.State)+1)
	for k, v := range req.State {
		state[k] = v
	}
	state["evidence"] = []any{filler}
	req.State = state
	return req, nil
}

// requestTokens estimates the input tokens of a decision request (its JSON at the default
// chars-per-token).
func requestTokens(req *jev.Request) int {
	b, _ := json.Marshal(req)
	return int(math.Ceil(float64(len(b)) / decision.DefaultCPT))
}

// steps are the captures, free ones first.
func (c *capturer) steps() ([]captureStep, error) {
	probe, err := jev.ProbeRequest(c.model)
	if err != nil {
		return nil, err
	}
	long, err := longRequest(c.model, capture30kTokens)
	if err != nil {
		return nil, err
	}
	jevUSD := func(tokens int) float64 {
		usd, _ := jev.EstimateUSD(jev.Info{Backend: jev.BackendOpenRouterDecisions, Model: c.model}, tokens)
		return usd * estimateMargin
	}
	chatIn, chatOut := 40, 5
	chatUSD := estimateMargin * chatPrice(chatIn, chatOut)
	return []captureStep{
		{stem: "key", what: "the key check (free)", method: http.MethodGet, path: "/api/v1/key", want: http.StatusOK,
			run: func(ctx context.Context, _ *capturer) (*float64, error) {
				cl, err := c.client(jev.BackendOpenRouterDecisions, c.key, false)
				if err != nil {
					return nil, err
				}
				kc, ok := cl.(jev.KeyChecker)
				if !ok {
					return nil, errors.New("the decisions client has no key check")
				}
				_, err = kc.CheckKey(ctx)
				return nil, err
			}},
		{stem: "decisions", what: "a bogus key (free)", method: http.MethodPost, path: "/api/alpha/decisions", want: http.StatusUnauthorized,
			run: func(ctx context.Context, _ *capturer) (*float64, error) {
				cl, err := c.client(jev.BackendOpenRouterDecisions, captureBogusKey, false)
				if err != nil {
					return nil, err
				}
				_, err = jev.Probe(ctx, cl)
				return nil, err
			}},
		{stem: "decisions", what: "the decisions path", method: http.MethodPost, path: "/api/alpha/decisions", want: http.StatusOK,
			estimate: jevUSD(requestTokens(probe)), run: c.probe(jev.BackendOpenRouterDecisions, false)},
		{stem: "systemone", what: "the systemone path", method: http.MethodPost, path: "/api/v1/systemone", want: http.StatusOK,
			estimate: jevUSD(requestTokens(probe)), run: c.probe(jev.BackendOpenRouterSystemOne, false)},
		{stem: "decisions", variant: "zdr", what: "a ZDR request (provider.zdr)", method: http.MethodPost, path: "/api/alpha/decisions", want: http.StatusOK,
			estimate: jevUSD(requestTokens(probe)), run: c.probe(jev.BackendOpenRouterDecisions, true)},
		{stem: "chat", variant: "usage_cost", what: "a Haiku chat with usage.include", method: http.MethodPost, path: "/api/v1/chat/completions", want: http.StatusOK,
			estimate: chatUSD, run: func(ctx context.Context, _ *capturer) (*float64, error) { return c.chat(ctx, chatOut) }},
		{stem: "decisions", variant: "30k", what: "a 30,000-token probe (the 32k limit)", method: http.MethodPost, path: "/api/alpha/decisions", want: http.StatusOK,
			estimate: jevUSD(requestTokens(long)), run: func(ctx context.Context, _ *capturer) (*float64, error) {
				cl, err := c.client(jev.BackendOpenRouterDecisions, c.key, false)
				if err != nil {
					return nil, err
				}
				resp, err := cl.Decide(ctx, long, jev.CallOptions{Class: jev.ClassSync, Component: jev.ComponentProbe})
				c.noteSnapshot(resp)
				if resp != nil && resp.CostSource == jev.CostReported {
					return resp.CostUSD, err
				}
				return nil, err
			}},
	}, nil
}

// chatPrice is the list price of a chat on captureChatModel (contracts/pricing/prices.v1.json).
func chatPrice(in, out int) float64 {
	t, err := decision.ContractsPrices()
	if err != nil {
		return 0.001 // no table: a generous stand-in, so the cap still bounds the step
	}
	e, ok := decision.LookupPrice(t.Entries, "openrouter", captureChatModel)
	if !ok {
		return 0.001
	}
	return decision.TokenCostUSD(int64(in), int64(out), e.InputPerMTok, e.OutputPerMTok)
}

// chat sends the Haiku chat with usage.include and decodes it with llmapi.
func (c *capturer) chat(ctx context.Context, maxTokens int) (*float64, error) {
	body, _ := json.Marshal(map[string]any{
		"model":      captureChatModel,
		"messages":   []map[string]string{{"role": "user", "content": "Reply with the single word: ok"}},
		"max_tokens": maxTokens,
		"usage":      map[string]bool{"include": true},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Title", jev.DefaultAppTitle)
	resp, err := (&http.Client{Transport: c.rec, Timeout: captureAttemptTimeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippetOf(b))
	}
	cost, err := chatCost(b)
	return cost, err
}

// chatCost decodes a chat completion with the gateway's decoder and returns usage.cost, if any.
func chatCost(body []byte) (*float64, error) {
	r, err := llmapi.ParseChatResponse(body)
	if err != nil {
		return nil, err
	}
	if len(r.RawUsage) == 0 {
		return nil, nil
	}
	var u struct {
		Cost *float64 `json:"cost"`
	}
	if err := json.Unmarshal(r.RawUsage, &u); err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	return u.Cost, nil
}

func snippetOf(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// captureResult is one step's outcome.
type captureResult struct {
	step    captureStep
	status  int
	file    string
	costUSD float64
	note    string
	failed  bool
}

// runCapture is jevcheck --capture.
func runCapture(ctx context.Context, o captureOptions, getenv func(string) string, now time.Time, transport http.RoundTripper, stdout, stderr io.Writer) int {
	base := strings.TrimRight(getenv("TRUTHTABLE_OPENROUTER_BASE_URL"), "/")
	if base == "" {
		base = jev.DefaultOpenRouterBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && jev.LocalHost(u.Hostname()))) {
		fmt.Fprintf(stderr, "jevcheck: --capture: TRUTHTABLE_OPENROUTER_BASE_URL %q is not an https URL (http only for a local host)\n", base)
		return exitUsage
	}
	model := getenv("TRUTHTABLE_JEV_MODEL")
	if model == "" {
		model = jev.DefaultOpenRouterModel
	}
	key := strings.TrimSpace(getenv("OPENROUTER_API_KEY"))
	if transport == nil {
		transport = http.DefaultTransport
	}
	c := &capturer{base: base, key: key, model: model, getenv: getenv, rec: &recorder{base: transport}}
	steps, err := c.steps()
	if err != nil {
		fmt.Fprintln(stderr, "jevcheck: --capture:", err)
		return exitUsage
	}
	host := u.Hostname()
	res := p6Result{Step: "jev-capture", RunID: "capture-" + now.UTC().Format("20060102T150405Z"), CapUSD: o.maxCostUSD,
		Label: "captured", Hosts: p6Hosts{Decision: host, Judge: host}}
	if !vendorHost(host) {
		res.Label = "simulated"
	}

	fmt.Fprintln(stdout, "TrustAgent jevcheck --capture: sanitized vendor fixtures (D211)")
	fmt.Fprintln(stdout)
	row := func(k, v string) { fmt.Fprintf(stdout, "  %-9s %s\n", k, v) }
	row("Output", o.dir+" (sanitized; gitleaks runs over it)")
	row("Endpoint", host+endpointNote(host))
	switch {
	case key == "":
		row("Key", "OPENROUTER_API_KEY missing: no key, plan only")
	default:
		row("Key", "OPENROUTER_API_KEY set (never shown)")
	}
	row("Cap", "$"+formatUSD(o.maxCostUSD)+" (--max-cost-usd): a step whose estimate exceeds what is left is refused")
	fmt.Fprintln(stdout)
	total := 0.0
	fmt.Fprintf(stdout, "  %-2s %-38s %-34s %s\n", "#", "Capture", "Request", "Estimate")
	for i, s := range steps {
		est := "free"
		if s.estimate > 0 {
			est = "$" + formatUSD(s.estimate)
			total += s.estimate
		}
		fmt.Fprintf(stdout, "  %-2d %-38s %-34s %s\n", i+1, s.what, s.method+" "+s.path, est)
	}
	fmt.Fprintf(stdout, "  Total estimate $%s of the $%s cap (estimate, at list prices)\n", formatUSD(total), formatUSD(o.maxCostUSD))
	if total > o.maxCostUSD {
		fmt.Fprintln(stdout, "  The estimate exceeds the cap: the run stops at the first step that does not fit.")
	}
	fmt.Fprintln(stdout)
	if o.dryRun || key == "" {
		why := "Dry run"
		if key == "" && !o.dryRun {
			why = "No key"
		}
		fmt.Fprintf(stdout, "%s: nothing was sent and nothing was spent. To capture: make jev-capture CONFIRM=1\n", why)
		res.DryRun = true
		if res.Label == "captured" {
			res.Label = "plan" // nothing was captured: the line names the plan, not a capture
		}
		res.Counts = p6Counts{N: len(steps)}
		res.print(stdout)
		return exitOK
	}

	if err := os.MkdirAll(o.dir, 0o750); err != nil {
		fmt.Fprintln(stderr, "jevcheck: --capture:", err)
		return exitFailed
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	date := now.UTC().Format(time.DateOnly)
	spent := 0.0
	var limitLeft *float64
	var results []captureResult
	refused := false
	mark := 0
	for _, s := range steps {
		r := captureResult{step: s}
		if refused {
			r.note, r.failed = "not sent (the budget refused an earlier step)", true
			results = append(results, r)
			continue
		}
		left := o.maxCostUSD - spent
		if limitLeft != nil {
			left = math.Min(left, *limitLeft-spent)
		}
		if s.estimate > 0 && s.estimate > left {
			r.note, r.failed, refused = fmt.Sprintf("refused: the estimate $%s exceeds the $%s left", formatUSD(s.estimate), formatUSD(math.Max(left, 0))), true, true
			results = append(results, r)
			continue
		}
		cost, runErr := s.run(ctx, c)
		var x *exchange
		x, mark = c.rec.since(mark)
		switch {
		case cost != nil:
			r.costUSD = *cost
		case s.estimate > 0 && (x == nil || x.status == 0 || x.status >= 500 || x.status/100 == 2):
			// No reported cost on a call that may have been billed: count the estimate.
			r.costUSD = s.estimate
		}
		spent += r.costUSD
		if x == nil {
			r.note, r.failed = "no response: "+errText(runErr), true
			results = append(results, r)
			continue
		}
		r.status = x.status
		if s.stem == "key" && x.status == http.StatusOK {
			limitLeft = keyLimitLeft(x.respBody)
			if u, ok := keyUsage(x.respBody); ok {
				res.KeyUsageUSD = &u
			}
			res.KeyLimitRemainingUSD = limitLeft
		}
		if x.status != s.want {
			r.failed = true
			r.note = fmt.Sprintf("HTTP %d, expected %d", x.status, s.want)
			if runErr != nil {
				r.note += ": " + errText(runErr)
			}
		} else if runErr != nil && s.want/100 == 2 {
			r.failed, r.note = true, "decoding failed: "+errText(runErr)
		}
		switch x.status {
		case http.StatusPaymentRequired, http.StatusTooManyRequests, http.StatusRequestEntityTooLarge:
			r.note = strings.TrimPrefix(r.note+"; not written (402, 429 and 413 stay synthetic)", "; ")
			results = append(results, r)
			continue
		}
		file, err := writeFixture(o.dir, s, x, fixtureSource(host, date), key)
		if err != nil {
			r.failed, r.note = true, strings.TrimPrefix(r.note+"; "+err.Error(), "; ")
		}
		r.file = file
		results = append(results, r)
	}

	failed := 0
	fmt.Fprintln(stdout, "Captured:")
	for _, r := range results {
		mark := "ok  "
		if r.failed {
			mark = "FAIL"
			failed++
		}
		line := fmt.Sprintf("  %s %-38s", mark, r.step.what)
		if r.status > 0 {
			line += fmt.Sprintf(" HTTP %d", r.status)
		}
		if r.file != "" {
			line += " -> " + r.file
		}
		if r.costUSD > 0 {
			line += " · $" + formatUSD(r.costUSD)
		}
		if r.note != "" {
			line += " (" + r.note + ")"
		}
		fmt.Fprintln(stdout, line)
	}
	fmt.Fprintf(stdout, "Spent $%s of the $%s cap (the vendor's reported cost; the estimate where it reported none).\n", formatUSD(spent), formatUSD(o.maxCostUSD))
	res.CostUSD = round6(spent)
	if c.snapshot != "" {
		res.Snapshot = &c.snapshot
	}
	res.Counts = p6Counts{N: len(steps), Scored: len(steps) - failed, Failed: failed}
	if failed > 0 {
		res.Exit = exitFailed
		res.print(stdout)
		return exitFailed
	}
	res.print(stdout)
	return exitOK
}

func errText(err error) string {
	if err == nil {
		return "no error"
	}
	return err.Error()
}

func endpointNote(host string) string {
	if vendorHost(host) {
		return ""
	}
	return " (not a vendor host: these fixtures are simulated, never captured)"
}

// vendorHosts are contracts/schema/common.v1 VendorHost: only these make a capture or a recording
// real (D203).
var vendorHosts = []string{"openrouter.ai", "api.typesafe.ai", "api.anthropic.com"}

func vendorHost(h string) bool {
	for _, v := range vendorHosts {
		if strings.EqualFold(h, v) {
			return true
		}
	}
	return false
}

// keyLimitLeft reads data.limit_remaining from a key-check body (nil: no limit).
func keyLimitLeft(body []byte) *float64 {
	var k struct {
		Data struct {
			LimitRemaining *float64 `json:"limit_remaining"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &k) != nil {
		return nil
	}
	return k.Data.LimitRemaining
}

func keyUsage(body []byte) (float64, bool) {
	var k struct {
		Data struct {
			Usage *float64 `json:"usage"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &k) != nil || k.Data.Usage == nil {
		return 0, false
	}
	return *k.Data.Usage, true
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// ---------- fixtures ----------

// fixture is one captured exchange, sanitized (the layout of contracts/fixtures/vendor/openrouter).
type fixture struct {
	Source       string            `json:"source"`
	Sanitized    bool              `json:"sanitized"`
	Placeholders []string          `json:"placeholders"`
	Request      fixtureRequest    `json:"request"`
	Status       int               `json:"status"`
	Headers      map[string]string `json:"headers"`
	Body         json.RawMessage   `json:"body"`
}

type fixtureRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// keptRequestHeaders and keptResponseHeaders are the headers a fixture keeps; the ones mapped to
// true are replaced by a placeholder.
var (
	keptRequestHeaders  = map[string]bool{"authorization": true, "content-type": false, "x-title": false, "http-referer": false}
	keptResponseHeaders = map[string]bool{"content-type": false, "access-control-allow-origin": false,
		"access-control-expose-headers": false, "x-provider-name": false, "x-generation-id": true, "request-id": true, "cf-ray": true}
)

// fixtureSource is a fixture's source: captured from a vendor host, or simulated (mock-upstream,
// any other host), which is never labelled captured.
func fixtureSource(host, date string) string {
	if vendorHost(host) {
		return "captured " + date + " via jevcheck --capture"
	}
	return "simulated " + date + " via jevcheck --capture against " + host + " (not a vendor host)"
}

// writeFixture sanitizes x into dir/<stem>.<status>[_variant].json.
func writeFixture(dir string, s captureStep, x *exchange, source, key string) (string, error) {
	var ph []string
	u, err := url.Parse(x.url)
	if err != nil {
		return "", err
	}
	f := fixture{Source: source, Sanitized: true, Status: x.status,
		Request: fixtureRequest{Method: x.method, Path: u.EscapedPath(), Headers: map[string]string{}}, Headers: map[string]string{}}
	for name, values := range x.reqHeader {
		n := strings.ToLower(name)
		masked, kept := keptRequestHeaders[n]
		if !kept || len(values) == 0 {
			continue
		}
		v := values[0]
		if masked {
			v, ph = "Bearer "+MaskedKey, append(ph, "request.headers."+n)
		}
		f.Request.Headers[n] = v
	}
	for name, values := range x.respHeader {
		n := strings.ToLower(name)
		masked, kept := keptResponseHeaders[n]
		if !kept || len(values) == 0 {
			continue
		}
		v := values[0]
		if masked {
			v, ph = placeholderID(v), append(ph, "headers."+n)
		}
		f.Headers[n] = v
	}
	if len(x.reqBody) > 0 {
		b, p, err := sanitizeJSON(x.reqBody, "request.body", false)
		if err != nil {
			return "", fmt.Errorf("request body: %w", err)
		}
		f.Request.Body, ph = b, append(ph, p...)
	}
	keyBody := s.stem == "key"
	b, p, err := sanitizeJSON(x.respBody, "body", keyBody)
	if err != nil {
		// Not JSON (an HTML page, an empty body): kept as a string, elided when long.
		text := string(x.respBody)
		if len(text) > elideAbove {
			text = fmt.Sprintf("<elided: %d chars>", len(text))
		}
		b, _ = json.Marshal(text)
	}
	f.Body, ph = b, append(ph, p...)
	sort.Strings(ph)
	f.Placeholders = append([]string{}, ph...)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // fixtures keep their characters ("<redacted>", not "\u003credacted\u003e")
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return "", err
	}
	out := buf.Bytes()
	if leak := secretIn(out, key); leak != "" {
		return "", fmt.Errorf("refused to write: the sanitized fixture still holds %s", leak)
	}
	name := s.stem + "." + strconv.Itoa(x.status)
	if s.variant != "" {
		name += "_" + s.variant
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// placeholderID keeps an id's prefix ("gen-", "req_") and redacts the rest.
func placeholderID(v string) string {
	if i := strings.IndexAny(v, "-_"); i > 0 && i < 12 {
		return v[:i+1] + "<redacted>"
	}
	return "<redacted>"
}

var (
	skKeyPattern  = regexp.MustCompile(`sk-or-v1-[A-Za-z0-9_-]{8,}|sk-[A-Za-z0-9]{20,}`)
	genIDPattern  = regexp.MustCompile(`gen-[A-Za-z0-9-]{6,}`)
	bearerPattern = regexp.MustCompile(`Bearer\s+[A-Za-z0-9._~+/=-]{12,}`)
)

// secretIn names a secret left in b, or returns "".
func secretIn(b []byte, key string) string {
	s := strings.ReplaceAll(string(b), MaskedKey, "")
	switch {
	case key != "" && strings.Contains(s, key):
		return "the key"
	case skKeyPattern.MatchString(s):
		return "an sk- key"
	case genIDPattern.MatchString(s):
		return "a generation id"
	case bearerPattern.MatchString(s):
		return "a bearer token"
	}
	return ""
}

// sanitizeJSON rewrites a JSON document token by token, keeping its key order and its number
// literals (rounding is part of what a capture records), and replacing what a fixture must not
// hold. keyBody marks the key check's body: its label and amounts are replaced too.
func sanitizeJSON(raw []byte, root string, keyBody bool) (json.RawMessage, []string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out bytes.Buffer
	var ph []string
	if err := sanitizeValue(dec, &out, root, "", keyBody, &ph); err != nil {
		return nil, nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, nil, errors.New("trailing data after the JSON document")
	}
	return out.Bytes(), ph, nil
}

// amountKeys are the key check's amounts (D211: usage and limit amounts become placeholders).
var amountKeys = regexp.MustCompile(`^(usage|limit|limit_remaining|byok_usage)(_[a-z]+)?$`)

func sanitizeValue(dec *json.Decoder, out *bytes.Buffer, path, key string, keyBody bool, ph *[]string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			out.WriteByte('{')
			first := true
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return err
				}
				k, ok := kt.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if !first {
					out.WriteByte(',')
				}
				first = false
				kb, _ := json.Marshal(k)
				out.Write(kb)
				out.WriteByte(':')
				if err := sanitizeValue(dec, out, path+"."+k, k, keyBody, ph); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			out.WriteByte('}')
		case '[':
			out.WriteByte('[')
			for i := 0; dec.More(); i++ {
				if i > 0 {
					out.WriteByte(',')
				}
				if err := sanitizeValue(dec, out, path+"."+strconv.Itoa(i), "", keyBody, ph); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			out.WriteByte(']')
		}
	case string:
		v := t
		switch {
		case keyBody && key == "label":
			if v != "" {
				v = MaskedLabel
				*ph = append(*ph, path)
			}
		case key == "id" || key == "request_id" || key == "generation_id":
			if v != "" {
				v = placeholderID(v)
				*ph = append(*ph, path)
			}
		case len(v) > elideAbove:
			v = fmt.Sprintf("<elided: %d chars>", len(v))
			*ph = append(*ph, path)
		default:
			if r := genIDPattern.ReplaceAllString(skKeyPattern.ReplaceAllString(v, MaskedKey), "gen-<redacted>"); r != v {
				v = r
				*ph = append(*ph, path)
			}
		}
		b, _ := marshalNoEscape(v)
		out.Write(b)
	case json.Number:
		if keyBody && amountKeys.MatchString(key) {
			out.WriteString("0")
			*ph = append(*ph, path)
			return nil
		}
		out.WriteString(t.String())
	case bool:
		out.WriteString(strconv.FormatBool(t))
	case nil:
		out.WriteString("null")
	}
	return nil
}

// marshalNoEscape is json.Marshal without HTML escaping (fixtures keep their characters).
func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// ---------- the P6 result line ----------

// p6Result is the TRUSTAGENT_P6_RESULT line every paid P6 command ends with (P6 plan §0.6).
type p6Result struct {
	Step                 string   `json:"step"`
	RunID                string   `json:"run_id"`
	Exit                 int      `json:"exit"`
	DryRun               bool     `json:"dry_run,omitempty"`
	CostUSD              float64  `json:"cost_usd"`
	CapUSD               float64  `json:"cap_usd"`
	KeyUsageUSD          *float64 `json:"key_usage_usd"`
	KeyLimitRemainingUSD *float64 `json:"key_limit_remaining_usd"`
	Label                string   `json:"label"`
	Snapshot             *string  `json:"snapshot"`
	Hosts                p6Hosts  `json:"hosts"`
	Counts               p6Counts `json:"counts"`
}

type p6Hosts struct {
	Decision string  `json:"decision"`
	Judge    string  `json:"judge"`
	Upstream *string `json:"upstream"`
}

type p6Counts struct {
	N      int `json:"n"`
	Scored int `json:"scored"`
	Failed int `json:"failed"`
}

func (r p6Result) print(w io.Writer) {
	b, _ := json.Marshal(r)
	fmt.Fprintf(w, "TRUSTAGENT_P6_RESULT %s\n", b)
}
