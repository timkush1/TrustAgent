package gate

import (
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/metrics"
	"github.com/truthtable/backend-go/internal/policy"
)

// auditDraw is what planAudit needs besides the route and the decision.
type auditDraw struct {
	// uniform is the pre-gate uniform draw; hasText says there is an answer to audit.
	uniform, hasText bool
	// forced is Exchange.AuditSelection; scenario the trace's scenario id (test mode).
	forced, scenario string
}

// planAudit applies the SPEC §2.9 caller rules to a decision: a selection the caller forces (a
// Playground run) wins, then the uniform draw, then the decision's deep_audit class picks the lane
// and its sample rate. Without an answer there is nothing to audit.
func (g *Gate) planAudit(r *policy.Route, core *decision.Core, d auditDraw) AuditPlan {
	da := r.DeepAudit
	p := AuditPlan{Verifier: da.Verifier, MockScenario: d.scenario}
	switch {
	case d.forced != "" && d.hasText:
		p.Selection, p.Lane, p.InclusionProb = d.forced, LaneHigh, 1
	case d.forced != "":
		p.Status = AuditNotAssessed
	case !da.Enabled:
		p.Status = AuditDisabled
	case !d.hasText:
		p.Status = AuditNotAssessed
	case d.uniform:
		p.Selection, p.Lane, p.InclusionProb = SelectionUniform, LaneLow, da.UniformSampleRate
		p.UniformSample, p.Verifier = true, "llm"
	case core.DeepAudit == decision.DeepAuditHigh:
		p.Selection, p.Lane, p.InclusionProb = SelectionHigh, LaneHigh, 1
	case core.DeepAudit == decision.DeepAuditSample:
		g.sample(&p, SelectionSample, da.PassSampleRate)
	case core.DeepAudit == decision.DeepAuditSampleError:
		g.sample(&p, SelectionErrorSample, da.ErrorSampleRate)
	default: // none: no evidence at all, so the audit would be NOT_ASSESSED with no LLM call
		p.Status = AuditNotAssessed
	}
	return p
}

func (g *Gate) sample(p *AuditPlan, selection string, rate float64) {
	if rate > 0 && (rate >= 1 || g.cfg.Rand() < rate) {
		p.Selection, p.Lane, p.InclusionProb = selection, LaneLow, rate
		return
	}
	p.Status = AuditSampledOut
}

// planBody applies the route store_bodies setting: all keeps every body, none keeps none, and
// sampled keeps every non-pass body and a body_sample_rate_pass share of passes.
func (g *Gate) planBody(r *policy.Route, core *decision.Core) BodyPlan {
	switch r.StoreBodies {
	case policy.StoreBodiesAll:
		return BodyPlan{Store: true, InclusionProb: 1}
	case policy.StoreBodiesNone:
		return BodyPlan{}
	}
	if core.WouldAction != decision.ActionPass {
		return BodyPlan{Store: true, InclusionProb: 1}
	}
	rate := r.BodySampleRatePass
	if rate > 0 && (rate >= 1 || g.cfg.Rand() < rate) {
		return BodyPlan{Store: true, InclusionProb: rate}
	}
	return BodyPlan{InclusionProb: rate}
}

// dailyCounter caps events per UTC day (in memory; the count restarts with the process). The
// uniform sample counter reports its count as uniform_samples_today.
type dailyCounter struct {
	mu    sync.Mutex
	limit int
	day   string
	n     int
}

// SeedUniform sets today's uniform-sample count to n, e.g. from the persisted count after a
// restart (the cost ledger seeds it), so the daily cap survives restarts. n below zero counts
// as zero.
func (g *Gate) SeedUniform(n int) {
	g.uniform.seed(g.cfg.Now(), max(n, 0))
}

// seed sets the count of the UTC day of now to n.
func (d *dailyCounter) seed(now time.Time, n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.day, d.n = now.UTC().Format(time.DateOnly), n
	metrics.SetUniformSamplesToday(n)
}

// take counts one event and reports whether it was under the cap. A cap of zero or less allows
// none.
func (d *dailyCounter) take(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if day := now.UTC().Format(time.DateOnly); day != d.day {
		d.day, d.n = day, 0
		metrics.SetUniformSamplesToday(0)
	}
	if d.n >= d.limit {
		return false
	}
	d.n++
	metrics.SetUniformSamplesToday(d.n)
	return true
}
