package grpc

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	pb "github.com/truthtable/backend-go/api/audit/v1"
)

// Engine component states (ComponentStateName).
const (
	StateUnknown  = "unknown"
	StateUp       = "up"
	StateDegraded = "degraded"
	StateDown     = "down"
)

// HealthChecker is the engine RPC the pinger calls.
type HealthChecker interface {
	Health(ctx context.Context) (*pb.HealthResponse, error)
}

// EngineHealth is the cached result of the latest engine health check.
type EngineHealth struct {
	// State is unknown before the first check, up when the engine reports healthy, degraded when
	// it answers but reports a failing dependency (such as its judge), and down when it does not
	// answer.
	State     string
	Detail    string
	CheckedAt time.Time
	LatencyMs int64
	// Response is the last health answer; nil when the engine did not answer.
	Response *pb.HealthResponse
}

// Reachable reports whether the engine answered the last check (GET /health audit_engine).
func (h EngineHealth) Reachable() bool { return h.State == StateUp || h.State == StateDegraded }

// Pinger checks the engine's health on an interval and caches the result (M4), so /health,
// /api/status and the WS hello report the real engine state without an RPC per request.
type Pinger struct {
	client   HealthChecker
	interval time.Duration
	onChange func(EngineHealth)
	now      func() time.Time

	mu    sync.RWMutex
	state EngineHealth
}

// NewPinger builds a pinger. onChange, when set, is called after every state transition (the WS
// status event).
func NewPinger(client HealthChecker, interval time.Duration, onChange func(EngineHealth)) *Pinger {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	return &Pinger{client: client, interval: interval, onChange: onChange, now: time.Now,
		state: EngineHealth{State: StateUnknown, Detail: "not checked yet"}}
}

// Run checks at once and then every interval until ctx ends.
func (p *Pinger) Run(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		p.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Check runs one health check and records the result.
func (p *Pinger) Check(ctx context.Context) EngineHealth {
	start := p.now()
	resp, err := p.client.Health(ctx)
	h := EngineHealth{CheckedAt: p.now().UTC(), LatencyMs: p.now().Sub(start).Milliseconds(), Response: resp}
	switch {
	case err != nil:
		h.State, h.Detail, h.Response = StateDown, "the audit engine did not answer its health check", nil
	case resp.GetHealthy():
		h.State = StateUp
	case judgeOnlyMissing(resp):
		// Started without a judge (judge_provider "none"): the engine serves every RPC and answers
		// audits NOT_ASSESSED(no_judge). That is a configuration state, not an outage; the judge's
		// own status says "disabled".
		h.State, h.Detail = StateUp, "no deep-audit judge is configured: audits come back not assessed"
	default:
		h.State, h.Detail = StateDegraded, "the audit engine reports an unhealthy dependency"
		deps := resp.GetDependencies()
		for _, name := range slices.Sorted(maps.Keys(deps)) {
			if !deps[name] {
				h.Detail = "the audit engine reports " + name + " unhealthy"
				break
			}
		}
	}
	p.mu.Lock()
	changed := p.state.State != h.State
	p.state = h
	p.mu.Unlock()
	if changed && p.onChange != nil {
		p.onChange(h)
	}
	return h
}

// judgeOnlyMissing reports an unhealthy answer whose only failing dependency is a judge the engine
// was started without.
func judgeOnlyMissing(resp *pb.HealthResponse) bool {
	if resp.GetJudgeProvider() != "none" {
		return false
	}
	for name, ok := range resp.GetDependencies() {
		if !ok && name != "judge" {
			return false
		}
	}
	return true
}

// Health returns the cached state.
func (p *Pinger) Health() EngineHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}
