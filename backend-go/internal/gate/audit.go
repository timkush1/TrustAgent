package gate

import (
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/policy"
)

// planAudit applies the SPEC §2.9 caller rules to a decision: the uniform draw wins, then the
// decision's deep_audit class picks the lane and its sample rate. Without an answer there is
// nothing to audit.
func (g *Gate) planAudit(r *policy.Route, core *decision.Core, uniform, hasText bool, scenario string) AuditPlan {
	da := r.DeepAudit
	p := AuditPlan{Verifier: da.Verifier, MockScenario: scenario}
	switch {
	case !da.Enabled:
		p.Status = AuditDisabled
	case !hasText:
		p.Status = AuditNotAssessed
	case uniform:
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

// dailyCounter caps events per UTC day (in memory; the count restarts with the process).
type dailyCounter struct {
	mu    sync.Mutex
	limit int
	day   string
	n     int
}

// take counts one event and reports whether it was under the cap. A cap of zero or less allows
// none.
func (d *dailyCounter) take(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if day := now.UTC().Format(time.DateOnly); day != d.day {
		d.day, d.n = day, 0
	}
	if d.n >= d.limit {
		return false
	}
	d.n++
	return true
}
