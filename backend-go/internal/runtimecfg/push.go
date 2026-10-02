package runtimecfg

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/contracts"
)

// Push timing (P4-PLAN CN-3): full-jitter backoff from pushMinBackoff to pushMaxBackoff between
// failed pushes, each bounded by pushTimeout.
const (
	pushMinBackoff = 250 * time.Millisecond
	pushMaxBackoff = 30 * time.Second
	pushTimeout    = 10 * time.Second
)

// pushJudgeProviders are the judge providers whose credential the engine can use (D34): mock, none
// and ollama carry none.
var pushJudgeProviders = []string{"openrouter", "anthropic", "openai"}

// EngineApplier is the engine RPC the pusher calls (grpc.AuditClient.ApplyRuntimeConfig).
type EngineApplier interface {
	ApplyRuntimeConfig(ctx context.Context, cfg *pb.RuntimeConfig) (*pb.ApplyRuntimeConfigResponse, error)
}

// BuildRuntimeConfig is the engine push of snap (P4-PLAN D34): its version and epoch, the judge
// and only the judge's bound credential, and priceTable (the effective price table, D53; nil
// sends the contracts table). decision is never set.
//
// Judge resolution: judge.provider unless it is auto; otherwise the provider of the credential
// bound to the judge; otherwise no judge, which reverts the engine to its own env judge. The
// model is judge.model or "" (the engine's default); base_url is never set. A credential goes
// along only when the judge's provider can use it (openrouter, anthropic, openai) and the bound
// credential is of that provider, with its allowed origins, which the engine intersects with its
// own vendor table.
func BuildRuntimeConfig(snap *Snapshot, priceTable []byte) *pb.RuntimeConfig {
	if priceTable == nil {
		priceTable = pushContractsPriceTable()
	}
	rc := &pb.RuntimeConfig{Version: snap.Version, Epoch: snap.Epoch, PriceTableJson: priceTable, Credentials: map[string]*pb.Credential{}}
	j := snap.Settings.Judge
	name := ""
	if b := snap.Settings.Bindings.Judge; b != nil {
		name = *b
	}
	cred, bound := snap.Credentials[name]
	bound = bound && name != "" && cred.Secret != ""
	provider := j.Provider
	if provider == "" || provider == "auto" {
		if !bound || !slices.Contains(pushJudgeProviders, cred.Provider) {
			return rc
		}
		provider = cred.Provider
	}
	rc.Judge = &pb.JudgeConfig{Provider: provider}
	if j.Model != nil {
		rc.Judge.Model = *j.Model
	}
	if bound && cred.Provider == provider && slices.Contains(pushJudgeProviders, provider) {
		rc.Judge.Credential = name
		rc.Credentials[name] = &pb.Credential{
			Provider: cred.Provider, Secret: cred.Secret, Fingerprint: cred.Fingerprint,
			AllowedHosts: slices.Clone(cred.AllowedOrigins),
		}
	}
	return rc
}

// pushContractsPriceTable is contracts/pricing/prices.v1.json, the engine's table when the gateway
// has no effective table of its own.
var pushContractsPriceTable = sync.OnceValue(func() []byte {
	b, err := contracts.FS.ReadFile("pricing/prices.v1.json")
	if err != nil {
		return nil
	}
	return b
})

// PushState is the engine push as the gateway last saw it.
type PushState struct {
	// Version and Epoch are what the engine last acknowledged (Epoch "" before the first push).
	Version int64
	Epoch   string
	// Pending is set while the engine has not acknowledged the snapshot in effect.
	Pending bool
	// JudgeStatus is the engine's judge_status of the last push (configured, unchanged,
	// env_managed, no_judge or invalid).
	JudgeStatus string
	// Warning is a plain-language status warning, "" when none.
	Warning string
}

// PusherConfig configures a Pusher.
type PusherConfig struct {
	Engine EngineApplier
	// Prices is the effective price table of the snapshot's overrides (costHooks.priceJSON); nil
	// or a nil table sends the contracts table.
	Prices func(overrides []decision.PriceEntry) []byte
	// Redact masks secrets in engine error text (D42); nil is the identity.
	Redact func(string) string
	// Poll is the retry interval of an engine that predates ApplyRuntimeConfig
	// (TRUTHTABLE_POLICY_RELOAD_INTERVAL; 30 s when not positive).
	Poll   time.Duration
	Logger *slog.Logger
}

// Pusher pushes the runtime configuration to the audit engine (P4-PLAN CN-3, D34): on every new
// snapshot (Want), and again whenever the engine reports another version or epoch than the one
// in effect (EngineReported: a restarted engine reports version 0). A failed push is retried with
// full-jitter backoff from 250 ms to 30 s; an engine without the RPC (UNIMPLEMENTED: it predates
// P4) is warned about once and retried at the poll interval. The secret in a push never reaches
// a log.
type Pusher struct {
	engine EngineApplier
	prices func([]decision.PriceEntry) []byte
	redact func(string) string
	poll   time.Duration
	log    *slog.Logger
	kick   chan struct{}
	// after and jitter are the timer and the random source (tests replace them).
	after  func(time.Duration) <-chan time.Time
	jitter func() float64

	mu                  sync.Mutex
	want                *Snapshot
	state               PushState
	warnedUnimplemented bool
}

// NewPusher builds a pusher; Run pushes.
func NewPusher(c PusherConfig) *Pusher {
	p := &Pusher{
		engine: c.Engine, prices: c.Prices, redact: c.Redact, poll: c.Poll, log: c.Logger,
		kick: make(chan struct{}, 1), after: time.After, jitter: rand.Float64,
	}
	if p.poll <= 0 {
		p.poll = 30 * time.Second
	}
	if p.redact == nil {
		p.redact = func(s string) string { return s }
	}
	if p.log == nil {
		p.log = slog.New(slog.DiscardHandler)
	}
	return p
}

// Want records snap as the configuration to push and wakes Run (Manager.Subscribe). It never
// blocks.
func (p *Pusher) Want(snap *Snapshot) {
	if snap == nil {
		return
	}
	p.mu.Lock()
	p.want = snap
	p.state.Pending = !p.appliedLocked(snap)
	p.mu.Unlock()
	p.wake()
}

// EngineReported compares the runtime configuration the engine reports (its health answer) with
// the one in effect and pushes again when they differ: an engine restart (version 0) or a push
// from another epoch.
func (p *Pusher) EngineReported(version int64, epoch string) {
	p.mu.Lock()
	want := p.want
	differs := want != nil && (version != want.Version || epoch != want.Epoch)
	if differs {
		p.state.Pending = true
	}
	p.mu.Unlock()
	if differs {
		p.wake()
	}
}

// State is the push state for /api/status.
func (p *Pusher) State() PushState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

func (p *Pusher) wake() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// appliedLocked reports whether the engine acknowledged snap. Caller holds mu.
func (p *Pusher) appliedLocked(snap *Snapshot) bool {
	return p.state.Epoch != "" && p.state.Epoch == snap.Epoch && p.state.Version == snap.Version
}

// Run pushes until ctx ends.
func (p *Pusher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.kick:
		}
		p.pushUntilAcknowledged(ctx)
	}
}

// pushUntilAcknowledged pushes the wanted snapshot until the engine acknowledges it (or a newer
// one), backing off between failures.
func (p *Pusher) pushUntilAcknowledged(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		p.mu.Lock()
		snap := p.want
		p.mu.Unlock()
		if snap == nil {
			return
		}
		err := p.push(ctx, snap)
		if err == nil {
			p.mu.Lock()
			done := p.want == snap
			p.mu.Unlock()
			if done {
				return
			}
			failures = 0 // a newer snapshot arrived meanwhile: push it at once
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if status.Code(err) == codes.Unimplemented {
			p.unimplemented()
			// Kicks (every engine health answer differs from the wanted version) do not hurry
			// an engine that cannot apply anything: wait out the poll interval.
			select {
			case <-ctx.Done():
				return
			case <-p.after(p.poll):
			}
			continue
		}
		p.failed(snap, err, failures)
		delay := p.backoff(failures)
		failures++
		select {
		case <-ctx.Done():
			return
		case <-p.after(delay):
		case <-p.kick: // the engine came back or a new snapshot arrived: try now
		}
	}
}

// push sends snap once and records the engine's answer.
func (p *Pusher) push(ctx context.Context, snap *Snapshot) error {
	var table []byte
	if p.prices != nil {
		table = p.prices(snap.Settings.PricingOverrides)
	}
	rc := BuildRuntimeConfig(snap, table)
	cctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	resp, err := p.engine.ApplyRuntimeConfig(cctx, rc)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state.Version, p.state.Epoch, p.state.JudgeStatus = resp.GetAppliedVersion(), resp.GetAppliedEpoch(), resp.GetJudgeStatus()
	if p.state.Epoch == "" {
		// An engine that echoes no epoch applied the one it was sent.
		p.state.Epoch = snap.Epoch
	}
	p.state.Pending = p.want != nil && !p.appliedLocked(p.want)
	p.state.Warning = ""
	if resp.GetJudgeStatus() == "invalid" {
		p.state.Warning = "The audit engine could not use the judge settings from the dashboard, so deep audits keep the engine's own judge."
		if w := resp.GetWarnings(); len(w) > 0 {
			p.state.Warning += " " + pushTruncate(p.redact(w[0]), 300)
		}
	}
	p.log.Info("runtime configuration pushed to the audit engine", "version", p.state.Version, "epoch", p.state.Epoch,
		"judge_status", resp.GetJudgeStatus(), "judge", rc.GetJudge().GetProvider(), "price_table_bytes", len(rc.GetPriceTableJson()))
	return nil
}

// unimplemented records an engine that predates ApplyRuntimeConfig: one log warning, and a status
// warning while it lasts.
func (p *Pusher) unimplemented() {
	p.mu.Lock()
	first := !p.warnedUnimplemented
	p.warnedUnimplemented = true
	p.state.Warning = "The audit engine is older than this gateway, so it cannot apply settings from the dashboard (the judge key, prices). Update and restart the engine."
	p.mu.Unlock()
	if first {
		p.log.Warn("the audit engine does not implement ApplyRuntimeConfig (it predates P4): dashboard judge settings and prices do not reach it; retrying at the poll interval",
			"poll", p.poll)
	}
}

// failed records a failed push. An unreachable engine already shows as down in the status, so
// only an engine that answers and refuses gets a status warning.
func (p *Pusher) failed(snap *Snapshot, err error, failures int) {
	msg := pushTruncate(p.redact(status.Convert(err).Message()), 300)
	code := status.Code(err)
	p.mu.Lock()
	switch code {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled:
	default:
		p.state.Warning = "The audit engine has not applied the settings from the dashboard yet (it answered " + code.String() + "): " + msg
	}
	p.mu.Unlock()
	if failures == 0 {
		p.log.Warn("runtime configuration push failed; retrying with backoff", "version", snap.Version, "code", code.String(), "err", msg)
	} else {
		p.log.Debug("runtime configuration push failed again", "version", snap.Version, "code", code.String(), "attempt", failures+1)
	}
}

// backoff is the full-jitter delay after n earlier failures: uniform in [0, min(max, min·2^n)].
func (p *Pusher) backoff(n int) time.Duration {
	ceiling := pushMaxBackoff
	if n < 16 {
		ceiling = min(pushMaxBackoff, pushMinBackoff<<n)
	}
	return time.Duration(p.jitter() * float64(ceiling))
}

// pushTruncate cuts s to at most n bytes on a rune boundary. Callers redact first: a secret cut
// in half before masking would no longer match, and its prefix would stay behind.
func pushTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !pushRuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func pushRuneStart(b byte) bool { return b&0xC0 != 0x80 }
