package jev

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/metrics"
	"github.com/truthtable/backend-go/internal/version"
)

// Attribution defaults (TRUTHTABLE_JEV_APP_URL / _APP_TITLE).
const (
	DefaultAppURL   = "https://github.com/timkush1/TrustAgent"
	DefaultAppTitle = "TrustAgent"
)

const (
	// maxResponseBytes bounds a decision response body (answers are small).
	maxResponseBytes = 4 << 20
	// minAttemptWindow is the least time left before the caller's deadline for a retry or a
	// hedge to be worth starting.
	minAttemptWindow = 20 * time.Millisecond
	// keepWarmTimeout bounds one keep-warm request.
	keepWarmTimeout = 5 * time.Second
)

// HTTPConfig configures the HTTP backends (SPEC §2.7, decision model group). Zero durations and
// counts that cannot be zero take their defaults; MaxRetries, MaxRetriesAsync, SyncReserve and
// KeepAlive may be zero.
type HTTPConfig struct {
	// AttemptTimeout bounds one attempt, which runs detached from the caller's context
	// (TRUTHTABLE_JEV_ATTEMPT_TIMEOUT, 3s).
	AttemptTimeout time.Duration
	// MaxRetries and MaxRetriesAsync are the retries of sync and async calls (1 / 2).
	MaxRetries, MaxRetriesAsync int
	// RetryAfterCap is the longest Retry-After honoured (10s).
	RetryAfterCap time.Duration
	// RPM, SyncReserve and MaxInFlight are TRUTHTABLE_GATE_JEV_RPM, _SYNC_RESERVE and
	// _MAX_INFLIGHT (1000 / 0.3 / 64).
	RPM         int
	SyncReserve float64
	MaxInFlight int
	// BreakerFailures, BreakerCooldown and AuthCooldown configure the breaker (5 / 30s / 5m).
	BreakerFailures int
	BreakerCooldown time.Duration
	AuthCooldown    time.Duration
	// PricePerMTok overrides the price table (TRUTHTABLE_JEV_PRICE_PER_MTOK; 0 = table).
	PricePerMTok float64
	// CharsPerToken seeds the chars-per-token EWMA (TRUTHTABLE_JEV_CHARS_PER_TOKEN, 4.0).
	CharsPerToken float64
	// AppURL and AppTitle are the OpenRouter attribution headers; PublicURL
	// (TRUTHTABLE_PUBLIC_URL) is the referer when AppURL is the default and PublicURL is public.
	AppURL, AppTitle, PublicURL string
	// ZDR asks OpenRouter for zero-data-retention endpoints (TRUTHTABLE_JEV_ZDR).
	ZDR bool
	// SendSessionID sends the trace id as OpenRouter session_id (true).
	SendSessionID bool
	// KeepAlive is the keep-warm interval while a sync route exists (60s; 0 disables).
	KeepAlive time.Duration
	// LogPayloads logs request and response bodies at debug level (never keys).
	LogPayloads bool
	// ForwardScenario sends CallOptions.Scenario as X-TrustAgent-Scenario to the backend. Set it
	// only in test mode, against mock-upstream.
	ForwardScenario bool
	// SpendCapUSDDay, EnforcingReserve and PerKeyCapUSDDay build the default MemoryBudget
	// (TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY 2.00, _SPEND_ENFORCING_RESERVE 0.25,
	// TRUTHTABLE_KEY_DECISION_USD_DAY 0) when Config.Budget is nil.
	SpendCapUSDDay, EnforcingReserve, PerKeyCapUSDDay float64
	// Transport overrides the dedicated transport (tests).
	Transport http.RoundTripper
	// Now is the clock of the breaker, limiter and budget (tests); nil uses time.Now.
	Now func() time.Time
}

// DefaultHTTPConfig returns the SPEC §2.7 defaults.
func DefaultHTTPConfig() HTTPConfig {
	return HTTPConfig{
		AttemptTimeout: 3 * time.Second, MaxRetries: 1, MaxRetriesAsync: 2, RetryAfterCap: 10 * time.Second,
		RPM: 1000, SyncReserve: 0.3, MaxInFlight: 64,
		BreakerFailures: 5, BreakerCooldown: 30 * time.Second, AuthCooldown: 5 * time.Minute,
		CharsPerToken: decision.DefaultCPT, AppURL: DefaultAppURL, AppTitle: DefaultAppTitle,
		SendSessionID: true, KeepAlive: 60 * time.Second,
		SpendCapUSDDay: 2.00, EnforcingReserve: 0.25,
	}
}

func (h HTTPConfig) withDefaults() HTTPConfig {
	d := DefaultHTTPConfig()
	if h.AttemptTimeout <= 0 {
		h.AttemptTimeout = d.AttemptTimeout
	}
	h.MaxRetries, h.MaxRetriesAsync = max(h.MaxRetries, 0), max(h.MaxRetriesAsync, 0)
	if h.RetryAfterCap <= 0 {
		h.RetryAfterCap = d.RetryAfterCap
	}
	if h.RPM <= 0 {
		h.RPM = d.RPM
	}
	if h.SyncReserve < 0 || h.SyncReserve > 1 {
		h.SyncReserve = d.SyncReserve
	}
	if h.MaxInFlight <= 0 {
		h.MaxInFlight = d.MaxInFlight
	}
	if h.BreakerFailures <= 0 {
		h.BreakerFailures = d.BreakerFailures
	}
	if h.BreakerCooldown <= 0 {
		h.BreakerCooldown = d.BreakerCooldown
	}
	if h.AuthCooldown <= 0 {
		h.AuthCooldown = d.AuthCooldown
	}
	if h.CharsPerToken <= 0 {
		h.CharsPerToken = d.CharsPerToken
	}
	if h.AppURL == "" {
		h.AppURL = d.AppURL
	}
	if h.AppTitle == "" {
		h.AppTitle = d.AppTitle
	}
	h.KeepAlive = max(h.KeepAlive, 0)
	if h.Now == nil {
		h.Now = time.Now
	}
	return h
}

// referer is the HTTP-Referer attribution: an explicit TRUTHTABLE_JEV_APP_URL, else a public
// TRUTHTABLE_PUBLIC_URL, else the repository URL.
func referer(appURL, publicURL string) string {
	if appURL != "" && appURL != DefaultAppURL {
		return appURL
	}
	if u, err := url.Parse(publicURL); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && !localHost(u.Hostname()) {
		return publicURL
	}
	return DefaultAppURL
}

// httpClient is the HTTP backend shared by openrouter-decisions, openrouter-systemone, typesafe
// and compatible.
type httpClient struct {
	info    Info
	ep      endpoint
	cfg     HTTPConfig
	hc      *http.Client
	keys    *keySource
	breaker *breaker
	limiter *limiter
	budget  Budget
	costs   CostSink
	prices  PriceFunc
	snaps   *snapshotTracker
	cpt     *cptTracker
	status  statusTracker
	log     *slog.Logger
	referer string
	ua      string

	warmMu   sync.Mutex
	warmOn   bool
	warmStop chan struct{}
	warmDone chan struct{}
	closed   bool
}

func newHTTPClient(cfg Config, r resolution) (*httpClient, error) {
	h := DefaultHTTPConfig()
	if cfg.HTTP != nil {
		h = *cfg.HTTP
	}
	h = h.withDefaults()
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	tr := h.Transport
	if tr == nil {
		tr = NewTransport()
	}
	c := &httpClient{
		info: Info{Backend: r.backend, Reason: r.reason, Model: r.model, Endpoint: redactURL(r.ep.url), CredentialSource: "env", Warnings: r.warnings},
		ep:   r.ep, cfg: h, log: log.With("component", "jev", "backend", r.backend),
		hc: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // never follow a redirect with the key
		}},
		limiter: newLimiter(h.RPM, h.SyncReserve, h.MaxInFlight, h.Now),
		budget:  cfg.Budget, costs: cfg.Costs, prices: cfg.Prices, cpt: newCPTTracker(h.CharsPerToken),
		referer: referer(h.AppURL, h.PublicURL), ua: "TrustAgent/" + version.Version + " (+" + DefaultAppURL + ")",
	}
	backend := r.backend
	c.breaker = newBreaker(h.BreakerFailures, h.BreakerCooldown, h.AuthCooldown, h.Now, func(state string) {
		metrics.JevBreakerState.WithLabelValues(backend).Set(breakerGauge(state))
	})
	metrics.JevBreakerState.WithLabelValues(backend).Set(0)
	c.keys = &keySource{static: r.key, origin: r.ep.origin, onChange: c.breaker.reset}
	if r.binding != nil {
		c.keys.static, c.keys.fn = "", r.binding.Key
		c.info.CredentialSource = "dashboard"
	}
	if c.budget == nil {
		if h.SpendCapUSDDay > 0 || h.PerKeyCapUSDDay > 0 {
			c.budget = NewMemoryBudget(BudgetConfig{CapUSD: h.SpendCapUSDDay, EnforcingReserve: h.EnforcingReserve, PerKeyCapUSD: h.PerKeyCapUSDDay, Now: h.Now})
		} else {
			c.budget = unlimitedBudget{}
		}
	}
	if c.costs == nil {
		c.costs = discardCosts{}
	}
	c.snaps = newSnapshotTracker(backend, r.model, cfg.KnownSnapshot, cfg.OnSnapshotChange)
	return c, nil
}

func breakerGauge(state string) float64 {
	switch state {
	case BreakerHalfOpen:
		return 1
	case BreakerOpen:
		return 2
	}
	return 0
}

func (c *httpClient) Info() Info { return c.info }

// CPT is the runtime chars-per-token estimate.
func (c *httpClient) CPT() float64 { return c.cpt.get() }

// ResetBreaker closes the breaker (a credential change made outside KeyFunc).
func (c *httpClient) ResetBreaker() { c.breaker.reset() }

// Status reports the live backend state.
func (c *httpClient) Status() Status {
	st := Status{Info: c.info, Snapshot: c.snaps.get(c.info.Model), RPMLimit: c.cfg.RPM, CPT: c.cpt.get()}
	b := c.breaker.view()
	st.Breaker = b.state
	if !b.until.IsZero() {
		u := b.until
		st.BreakerUntil = &u
	}
	st.InFlight, st.RPMUsed = c.limiter.load()
	if sr, ok := c.budget.(SpendReporter); ok {
		s := sr.Spend()
		st.Spend = &s
	}
	c.status.fill(&st, b, st.Spend)
	c.warmMu.Lock()
	st.KeepWarm = c.warmOn
	c.warmMu.Unlock()
	return st
}

// keySource yields the credential of each attempt: the environment key, or the binding's KeyFunc
// (hot reload). A changed key resets the breaker.
type keySource struct {
	static string
	fn     KeyFunc
	// origin is the endpoint's canonical origin, which the KeyFunc asserts (D78).
	origin   string
	onChange func()

	mu   sync.Mutex
	last [32]byte
	seen bool
}

func (k *keySource) get(ctx context.Context) (string, *Error) {
	key := k.static
	if k.fn != nil {
		var err error
		if key, err = k.fn(ctx, k.origin); err != nil {
			return "", &Error{Kind: KindCredentialsUnavailable, Message: scrub(err.Error()), Hint: Hint(KindCredentialsUnavailable)}
		}
	}
	if key == "" {
		return "", &Error{Kind: KindCredentialsUnavailable, Message: "the decision-model credential is empty", Hint: Hint(KindCredentialsUnavailable)}
	}
	sum := sha256.Sum256([]byte(key))
	k.mu.Lock()
	changed := k.seen && sum != k.last
	k.last, k.seen = sum, true
	k.mu.Unlock()
	if changed && k.onChange != nil {
		k.onChange()
	}
	return key, nil
}

// Decide sends one decision request: admission (breaker, limiter, budget), then attempts with
// retries and an optional hedge until one answers, a non-retryable error arrives, or ctx ends.
func (c *httpClient) Decide(ctx context.Context, req *Request, opt CallOptions) (*Response, error) {
	c.status.call()
	if req == nil {
		return nil, &Error{Kind: KindBadRequest, Message: "no request", Hint: Hint(KindBadRequest)}
	}
	if err := ctx.Err(); err != nil {
		return nil, &Error{Kind: KindTimeout, Message: "the deadline passed before the call", Err: err, Hint: Hint(KindTimeout)}
	}
	r := *req
	if r.Model == "" {
		r.Model = c.info.Model
	}
	body, err := encodeRequest(c.ep, &r, &c.cfg)
	if err != nil {
		return nil, &Error{Kind: KindBadRequest, Message: "the request cannot be encoded as JSON", Err: err, Hint: Hint(KindBadRequest)}
	}
	count, err := decision.CountTokens(map[string]any{"model": r.Model, "state": r.State, "questions": r.Questions}, c.cpt.get())
	if err != nil {
		return nil, &Error{Kind: KindBadRequest, Message: "the request is not canonicalizable", Err: err, Hint: Hint(KindBadRequest)}
	}
	if c.cfg.LogPayloads {
		c.log.Debug("jev request", "trace_id", opt.TraceID, "url", c.info.Endpoint, "body", string(body))
	}
	retries := c.cfg.MaxRetries
	if opt.Class == ClassAsync {
		retries = c.cfg.MaxRetriesAsync
	}
	cl := &call{
		c: c, req: &r, opt: opt, body: body, count: count, estTokens: decision.Overhead + count.Est,
		price: c.priceOf(r.Model), start: time.Now(),
		results: make(chan attemptResult, retries+2), retries: retries,
	}
	return cl.wait(ctx)
}

// call is one Decide: its attempts and their results. The waiter goroutine owns it until it
// returns; after that, only the late-decision goroutine (if any) reads it.
type call struct {
	c         *httpClient
	req       *Request
	opt       CallOptions
	body      []byte
	count     decision.TokenCount
	estTokens int
	price     price
	start     time.Time
	retries   int

	results  chan attemptResult
	attempts []*attempt
	running  int
	hedged   bool
	// doneCost is the cost of attempts that failed before the winner.
	doneCost float64
}

// attempt is one vendor request.
type attempt struct {
	n           int
	start       time.Time
	hedge       bool
	trial       bool
	release     func()
	reservation Reservation
	cancel      context.CancelFunc
	// ours is set, with reason, before we cancel the attempt (hedge loser, caller gone), so the
	// cancellation is neutral for the breaker; late is set when the caller stopped waiting.
	ours   atomic.Bool
	reason atomic.Value // string
	late   atomic.Bool
}

// attemptResult is what an attempt reports back.
type attemptResult struct {
	n      int
	wire   *wireResponse
	err    *Error
	effect breakerEffect
	// neutral: cancelled by us; network: no HTTP response (connection-level failure); wrote:
	// the request was sent, so the vendor may bill it.
	neutral, network, wrote bool
	// answers are the answers to asked questions.
	answers     map[string]*decision.RawAnswer
	inputTokens *int
	costUSD     float64
	costSource  string
	connectMs   *int64
}

func (cl *call) wait(ctx context.Context) (*Response, error) {
	if e := cl.launch(ctx, false); e != nil {
		return nil, e
	}
	hedge := newStoppedTimer()
	defer hedge.Stop()
	retry := newStoppedTimer()
	defer retry.Stop()
	cl.armHedge(ctx, hedge)
	retryNo := 0
	var lastErr *Error
	for {
		select {
		case r := <-cl.results:
			cl.running--
			if r.err == nil {
				cl.cancelRunning(r.n, AttemptHedgeLost)
				return cl.response(r, false), nil
			}
			cl.doneCost += r.costUSD
			lastErr = r.err
			if !r.err.Retryable {
				cl.cancelRunning(-1, AttemptCancelled)
				return nil, cl.fail(r.err)
			}
			if cl.running > 0 {
				continue // the hedge (or the primary) may still answer
			}
			if cl.retries <= 0 {
				return nil, cl.fail(lastErr)
			}
			delay := backoff(r.err, retryNo)
			if !fits(ctx, delay) {
				return nil, cl.fail(lastErr)
			}
			cl.retries--
			retryNo++
			hedge.Stop()
			retry.Reset(delay)
		case <-retry.C:
			if e := cl.launch(ctx, false); e != nil {
				// Admission refused the retry (breaker now open, limiter, budget): the vendor
				// error is the useful one.
				return nil, cl.fail(lastErr)
			}
			cl.armHedge(ctx, hedge)
		case <-hedge.C:
			if cl.running == 1 && !cl.hedged && cl.launch(ctx, true) == nil {
				cl.hedged = true
			}
		case <-ctx.Done():
			return cl.leave(ctx)
		}
	}
}

// armHedge starts the hedge timer when hedging is on, not yet used, and fits the deadline.
func (cl *call) armHedge(ctx context.Context, t *time.Timer) {
	if h := cl.opt.HedgeAfter; h > 0 && !cl.hedged && fits(ctx, h) {
		t.Reset(h)
	}
}

func newStoppedTimer() *time.Timer {
	t := time.NewTimer(time.Hour)
	t.Stop()
	return t
}

// fits reports whether waiting d still leaves an attempt window before ctx's deadline.
func fits(ctx context.Context, d time.Duration) bool {
	deadline, ok := ctx.Deadline()
	return !ok || time.Until(deadline)-d > minAttemptWindow
}

// backoff is the delay before retry n: the vendor's Retry-After when it sent one, else 50 ms
// doubling per retry (capped at 1 s) with jitter.
func backoff(e *Error, n int) time.Duration {
	if e.RetryAfter > 0 {
		return e.RetryAfter
	}
	d := min(50*time.Millisecond<<min(n, 5), time.Second)
	return d/2 + rand.N(d/2+1) // #nosec G404 -- retry jitter spreads load; it guards nothing
}

// launch admits and starts one attempt. Admission checks the credential, the breaker, the
// budget and the limiter, in that order, and fails without a vendor request. The budget goes
// before the limiter so that calls refused for spend take no rate token: past the soft cap,
// observe and flag traffic would otherwise drain the bucket that enforcing routes still need.
func (cl *call) launch(ctx context.Context, hedge bool) *Error {
	c := cl.c
	key, e := c.keys.get(ctx)
	if e != nil {
		return e
	}
	ok, trial := c.breaker.allow(hedge)
	if !ok {
		v := c.breaker.view()
		msg := "the breaker is open after repeated vendor faults"
		if v.cause != "" {
			msg += " (last: " + v.cause + ")"
		}
		return &Error{Kind: KindBreakerOpen, Message: msg, Hint: Hint(KindBreakerOpen)}
	}
	res, err := c.budget.Reserve(SpendRequest{USD: cl.price.usd(cl.estTokens), Enforcing: cl.opt.Enforcing, KeyLabel: cl.opt.KeyLabel})
	if err != nil {
		c.breaker.record(effectNeutral, trial, "")
		return &Error{Kind: KindBudget, Message: err.Error(), Err: err, Hint: Hint(KindBudget)}
	}
	release, kind := c.limiter.acquire(cl.opt.Class)
	if kind != "" {
		res.Settle(0)
		c.breaker.record(effectNeutral, trial, "")
		metrics.JevRateLimited.WithLabelValues(cl.opt.Class.String()).Inc()
		return &Error{Kind: kind, Message: "refused by the gateway limiter", Hint: Hint(kind)}
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.AttemptTimeout)
	a := &attempt{n: len(cl.attempts), hedge: hedge, trial: trial, release: release, reservation: res, cancel: cancel, start: time.Now()}
	cl.attempts = append(cl.attempts, a)
	cl.running++
	go func() {
		defer cancel()
		r := c.do(actx, cl, a, key)
		c.finish(cl, a, &r)
		cl.results <- r
	}()
	return nil
}

// cancelRunning cancels every attempt but winner as ours, with reason for the cost ledger.
func (cl *call) cancelRunning(winner int, reason string) {
	for _, a := range cl.attempts {
		if a.n == winner {
			continue
		}
		if a.reason.CompareAndSwap(nil, reason) {
			a.ours.Store(true)
		}
		a.cancel()
	}
}

// leave handles the caller's deadline: a result that landed at the deadline still counts;
// otherwise running attempts continue as late decisions when OnLate is set and fewer than half
// of MaxInFlight calls are in flight, and are cancelled otherwise.
func (cl *call) leave(ctx context.Context) (*Response, error) {
	for drained := false; !drained; {
		select {
		case r := <-cl.results:
			cl.running--
			if r.err == nil {
				cl.cancelRunning(r.n, AttemptHedgeLost)
				return cl.response(r, false), nil
			}
			cl.doneCost += r.costUSD
		default:
			drained = true
		}
	}
	for _, a := range cl.attempts {
		a.late.Store(true)
	}
	continued := false
	if cl.running > 0 {
		inFlight, _ := cl.c.limiter.load()
		if cl.opt.OnLate != nil && 2*inFlight < cl.c.cfg.MaxInFlight {
			continued = true
			go cl.drainLate(cl.running)
		} else {
			cl.cancelRunning(-1, AttemptCancelled)
		}
	}
	return nil, &Error{Kind: KindTimeout, Message: "no decision before the deadline", Err: ctx.Err(), Hint: Hint(KindTimeout),
		Attempts: len(cl.attempts), Continued: continued}
}

// drainLate waits for the n running attempts after the caller left and reports the first
// success (cancelling the rest), or the last error, to OnLate.
func (cl *call) drainLate(n int) {
	var lastErr *Error
	for range n {
		r := <-cl.results
		if r.err == nil {
			cl.cancelRunning(r.n, AttemptHedgeLost)
			cl.opt.OnLate(cl.response(r, true), nil)
			return
		}
		cl.doneCost += r.costUSD
		lastErr = r.err
	}
	cl.opt.OnLate(nil, cl.fail(lastErr))
}

// fail returns a copy of e with the call's attempt count.
func (cl *call) fail(e *Error) *Error {
	out := *e
	out.Attempts = len(cl.attempts)
	return &out
}

func (cl *call) response(r attemptResult, late bool) *Response {
	w := r.wire
	cost := cl.doneCost + r.costUSD
	return &Response{
		ID: w.ID, Snapshot: w.Model, Answers: r.answers, InputTokens: r.inputTokens,
		CostUSD: &cost, CostSource: r.costSource, Backend: cl.c.info.Backend,
		LatencyMs: time.Since(cl.start).Milliseconds(), ConnectMs: r.connectMs,
		Attempts: len(cl.attempts), Hedged: cl.hedged, Late: late,
	}
}

// connTrace measures connection acquisition (connect_ms: 0 for a reused connection) and whether
// the request was written.
type connTrace struct {
	mu        sync.Mutex
	getConn   time.Time
	connectMs *int64
	wrote     bool
}

func withClientTrace(ctx context.Context, t *connTrace) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn: func(string) {
			t.mu.Lock()
			t.getConn = time.Now()
			t.mu.Unlock()
		},
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			var ms int64
			if !info.Reused && !t.getConn.IsZero() {
				ms = time.Since(t.getConn).Milliseconds()
			}
			t.connectMs = &ms
			t.mu.Unlock()
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				t.mu.Lock()
				t.wrote = true
				t.mu.Unlock()
			}
		},
	})
}

func (t *connTrace) result() (connectMs *int64, wrote bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connectMs, t.wrote
}

// do runs one HTTP attempt and classifies its outcome.
func (c *httpClient) do(ctx context.Context, cl *call, a *attempt, key string) attemptResult {
	res := attemptResult{n: a.n}
	ct := &connTrace{}
	hreq, err := http.NewRequestWithContext(withClientTrace(ctx, ct), http.MethodPost, c.ep.url, bytes.NewReader(cl.body))
	if err != nil {
		res.err, res.effect = &Error{Kind: KindBadRequest, Message: "cannot build the request", Err: err, Hint: Hint(KindBadRequest)}, effectNeutral
		return res
	}
	c.setHeaders(hreq.Header, key)
	forwardTestHeaders(hreq.Header, hreq.URL, cl.opt, c.cfg.ForwardScenario)
	resp, err := c.hc.Do(hreq)
	res.connectMs, res.wrote = ct.result()
	if err != nil {
		c.transportError(ctx, a, err, &res)
		return res
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		res.wrote = true
		c.transportError(ctx, a, err, &res)
		return res
	}
	if c.cfg.LogPayloads {
		c.log.Debug("jev response", "trace_id", cl.opt.TraceID, "status", resp.StatusCode, "body", string(body))
	}
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode <= 399:
		res.err = &Error{Kind: KindNotFound, Status: resp.StatusCode,
			Message: "the endpoint redirects to " + redactURL(resp.Header.Get("Location")) + "; TrustAgent never follows redirects with the key",
			Hint:    Hint(KindNotFound)}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		res.err, res.effect = classifyStatus(resp.StatusCode, resp.Header, redactBody(body, key), c.cfg.RetryAfterCap)
		res.err.Message = redactSecret(res.err.Message, key)
	case len(body) > maxResponseBytes:
		res.err, res.effect = &Error{Kind: KindBadResponse, Status: resp.StatusCode, Message: "the response is larger than 4 MiB", Hint: Hint(KindBadResponse)}, effectFault
	default:
		if e := checkContentType(resp.Header, body); e != nil {
			e.Status = resp.StatusCode
			res.err, res.effect = e, effectTrip
			return res
		}
		w, e := decodeResponse(body)
		if e != nil {
			e.Message = redactSecret(e.Message, key)
			res.err, res.effect = e, effectFault
			return res
		}
		res.wire, res.effect = w, effectSuccess
	}
	return res
}

// transportError classifies an attempt that got no complete response: our cancellation
// (neutral), the attempt timeout (retryable, counted) or a network error (retryable, counted).
func (c *httpClient) transportError(ctx context.Context, a *attempt, err error, res *attemptResult) {
	switch {
	case a.ours.Load():
		res.neutral, res.effect = true, effectNeutral
		res.err = &Error{Kind: KindTimeout, Message: "cancelled by the gateway", Err: err, Hint: Hint(KindTimeout)}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.effect = effectFault
		res.err = &Error{Kind: KindTimeout, Retryable: true, Message: fmt.Sprintf("no answer within the %s attempt timeout", c.cfg.AttemptTimeout), Err: errAttemptTimeout, Hint: Hint(KindTimeout)}
	default:
		res.effect, res.network = effectFault, true
		res.err = &Error{Kind: KindRetryable, Retryable: true, Message: "network error: " + scrub(unwrapURLError(err).Error()), Err: err, Hint: Hint(KindRetryable)}
	}
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// setHeaders sets the auth, content and attribution headers of a vendor request.
func (c *httpClient) setHeaders(h http.Header, key string) {
	h.Set("Authorization", "Bearer "+key)
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("User-Agent", c.ua)
	if c.ep.openRouter {
		h.Set("HTTP-Referer", c.referer)
		h.Set("X-OpenRouter-Title", c.cfg.AppTitle)
		h.Set("X-Title", c.cfg.AppTitle) // the older name of the same attribution header
	}
}

// finish does the bookkeeping of a finished attempt: limiter slot, breaker, spend, cost event,
// metrics, status, snapshot and cpt.
func (c *httpClient) finish(cl *call, a *attempt, r *attemptResult) {
	a.release()
	now := c.cfg.Now()
	cause := ""
	if r.err != nil {
		cause = r.err.Kind
	}
	c.breaker.record(r.effect, a.trial, cause)

	ev := CostEvent{
		OccurredAt: now, TraceID: cl.opt.TraceID, Route: cl.opt.Route, KeyLabel: cl.opt.KeyLabel,
		Component: cl.opt.component(), Kind: CostKindDecision, Provider: c.info.Backend, Model: cl.req.Model,
		Attempt: a.n, Hedge: a.hedge, CostSource: CostComputed, PriceTableVersion: cl.price.version,
		Questions: len(cl.req.Questions), Late: a.late.Load(),
	}
	latency := time.Since(a.start)
	switch {
	case r.err == nil:
		w := r.wire
		ev.Status = AttemptOK
		if reason, _ := a.reason.Load().(string); reason != "" {
			ev.Status = reason // a loser that answered before its cancellation landed
		}
		if w.Model != "" {
			ev.Model = w.Model
		}
		r.inputTokens = w.Usage.inputTokens()
		if w.Usage != nil {
			ev.OutputTokens = w.Usage.OutputTokens
		}
		switch {
		case w.Usage != nil && w.Usage.Cost != nil:
			ev.CostUSD, ev.CostSource = *w.Usage.Cost, CostReported
		case r.inputTokens != nil:
			ev.CostUSD = cl.price.usd(*r.inputTokens)
		default:
			ev.CostUSD, ev.Estimated = cl.price.usd(cl.estTokens), true
		}
		ev.InputTokens = r.inputTokens
		if ev.Estimated {
			est := cl.estTokens
			ev.InputTokens = &est
		}
		var anomalies []string
		r.answers, anomalies = checkAnswers(cl.req.Questions, w.Answers)
		for _, k := range anomalies {
			metrics.JevAnomalies.WithLabelValues(k).Inc()
		}
		c.snaps.observe(cl.req.Model, w.Model)
		if r.inputTokens != nil {
			c.cpt.observe(cl.count, *r.inputTokens)
		}
	case r.neutral:
		ev.Status, _ = a.reason.Load().(string)
		if ev.Status == "" {
			ev.Status = AttemptCancelled
		}
		fallthrough
	case r.err.Kind == KindTimeout:
		// No response, but the request went out: it may be billed, so book the estimate.
		if ev.Status == "" {
			ev.Status = "error:" + r.err.Kind
		}
		if r.wrote {
			est := cl.estTokens
			ev.InputTokens, ev.CostUSD, ev.Estimated = &est, cl.price.usd(est), true
		}
	default:
		ev.Status = "error:" + r.err.Kind
	}
	a.reservation.Settle(ev.CostUSD)
	r.costUSD, r.costSource = ev.CostUSD, ev.CostSource
	ev.LatencyMs = latency.Milliseconds()
	c.costs.RecordCost(ev)

	outcome := ev.Status
	if r.err != nil && !r.neutral {
		outcome = r.err.Kind
	}
	backend := c.info.Backend
	metrics.JevRequests.WithLabelValues(backend, outcome).Inc()
	if r.wire != nil || r.err != nil && r.err.Status != 0 {
		metrics.JevSeconds.WithLabelValues(backend).Observe(latency.Seconds())
	}
	if r.connectMs != nil {
		metrics.JevConnectSeconds.Observe(float64(*r.connectMs) / 1000)
	}
	if ev.CostUSD > 0 {
		metrics.JevCostUSD.WithLabelValues(backend, ev.Component).Add(ev.CostUSD)
	}
	if ev.InputTokens != nil {
		metrics.JevInputTokens.WithLabelValues(backend).Add(float64(*ev.InputTokens))
	}
	c.status.attempt(now, ev.CostUSD, r.err, r.neutral, r.network)
	if r.err != nil && !r.neutral {
		c.log.Warn("jev attempt failed", "trace_id", cl.opt.TraceID, "attempt", a.n, "hedge", a.hedge, "kind", r.err.Kind, "status", r.err.Status, "message", r.err.Message)
	}
}
