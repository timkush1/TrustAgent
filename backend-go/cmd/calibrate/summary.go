package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
)

// summary is the plain-language report of a calibration, with the honesty text of SPEC §7.3:
// what the quality means for blocking, that the metrics are agreement with the labels measured
// in-sample, and that activation changes no live decision until a route adopts the calibration.
func (res *outcome) summary() string {
	e := res.entry
	m := res.file.DecisionModel
	var b strings.Builder
	fmt.Fprintf(&b, "Calibration for %s on %s (backend %s)\n", res.ref, m.Snapshot, m.Backend)

	fmt.Fprintf(&b, "  Labels: %d used (%d should fire, %d should not); sources: %s", e.N, e.NPos, e.NNeg, sourceList(e.LabelSource))
	if skipped := res.skippedList(); skipped != "" {
		fmt.Fprintf(&b, "; not used: %s", skipped)
	}
	b.WriteString("\n")
	if len(e.Datasets) > 0 {
		fmt.Fprintf(&b, "  Datasets: %s\n", strings.Join(e.Datasets, ", "))
	}
	fmt.Fprintf(&b, "  Quality: %s\n", qualityText(e))

	method := e.Method
	if res.fit != nil && res.fit.CVBrier != nil {
		method += fmt.Sprintf(" (5-fold cross-validated Brier: isotonic %s, Platt %s)", f3(res.fit.CVBrier.Isotonic), f3(res.fit.CVBrier.Platt))
	}
	if e.Platt != nil {
		method += fmt.Sprintf(": calibrated(x) = 1/(1 + exp(-(%s·x %s)))", strconv.FormatFloat(e.Platt.A, 'f', -1, 64), signed(e.Platt.B))
	}
	fmt.Fprintf(&b, "  Method: %s\n", method)
	fmt.Fprintf(&b, "  Agreement with the labels, in-sample (not ground truth): ECE %s → %s, Brier %s → %s, AUROC %s\n",
		opt(e.Metrics.ECEBefore), opt(e.Metrics.ECEAfter), opt(e.Metrics.BrierBefore), opt(e.Metrics.BrierAfter), opt(e.Metrics.AUROC))

	b.WriteString("  Recommended thresholds (calibrated space):\n")
	r := e.Recommended
	if w := r.WarnAt; w != nil {
		fmt.Fprintf(&b, "    warn_at %s: precision %s, recall %s%s\n", f3(w.Value), pct(w.Precision), pct(w.Recall), res.recallRange(w.Value))
	} else {
		b.WriteString("    warn_at: none; no threshold reaches precision 80%\n")
	}
	if c := r.CriticalAt; c != nil {
		fmt.Fprintf(&b, "    critical_at %s: precision at least %s with 95%% confidence over %d predicted positives\n",
			f3(c.Value), pct(c.PrecisionLB95), c.PredictedPos)
	} else {
		reason := decision.CriticalNullReason
		if r.CriticalNullReason != nil {
			reason = *r.CriticalNullReason
		}
		fmt.Fprintf(&b, "    critical_at: none (%s): blocking needs %d predicted positives whose precision is at least 95%% at the 95%% lower bound\n",
			reason, decision.CriticalMinPredictedPos)
	}
	if w := r.WarnAt; w != nil {
		fmt.Fprintf(&b, "  With %d labels, recall at warn_at is %s%s. Keep it watching and label more traffic.\n", e.N, pct(w.Recall), res.recallRange(w.Value))
	}
	for _, w := range res.warnings {
		fmt.Fprintf(&b, "  Warning: %s\n", w)
	}
	b.WriteString("  Activating this calibration does not change live decisions: a route uses it only after \"Adopt calibration\" moves its thresholds to the calibrated space.\n")
	return b.String()
}

// qualityText explains the derived quality in terms of what it unlocks.
func qualityText(e *decision.CalibrationFileEntry) string {
	switch e.Quality {
	case decision.QualityMeasured:
		return "measured: human and public-dataset labels only, with at least 200 labels and 30 of each class, so it can satisfy require_calibration."
	case decision.QualityProxy:
		n := e.LabelSource.UniformAudit + e.LabelSource.Judge
		return fmt.Sprintf("proxy: %d labels came from uniform-sample audits or a judge, which never count as measured. Blocking stays capped at Flag.", n)
	case decision.QualityMock:
		return "mock: a simulated snapshot, honoured only with the mock backend in demo mode. These numbers are simulated, not measured."
	}
	return fmt.Sprintf("provisional: measured needs at least %d human or public-dataset labels with %d of each class (have %d: %d should fire, %d should not). Blocking stays capped at Flag.",
		decision.MeasuredMinLabels, decision.MeasuredMinPerClass, e.N, e.NPos, e.NNeg)
}

// recallRange is the Wilson 95% range of recall at a calibrated threshold, when the labels are at
// hand (not for --source api).
func (res *outcome) recallRange(t float64) string {
	if res.samples == nil {
		return ""
	}
	cal := make([]decision.LabeledScore, len(res.samples))
	for i, s := range res.samples {
		cal[i] = decision.LabeledScore{Risk: decision.ApplyCalibration(res.entry.Table, s.Risk), Fire: s.Fire}
	}
	c := decision.ConfusionAt(cal, t)
	lo, hi := decision.WilsonInterval(c.TP, c.TP+c.FN, decision.WilsonZ95)
	return fmt.Sprintf(" (95%% range %s–%s)", pct(lo), pct(hi))
}

func (res *outcome) skippedList() string {
	var parts []string
	for _, k := range sortedKeys(res.skipped) {
		parts = append(parts, fmt.Sprintf("%d %s", res.skipped[k], k))
	}
	return strings.Join(parts, ", ")
}

func sourceList(s decision.LabelSourceCounts) string {
	var parts []string
	for _, p := range []struct {
		name string
		n    int
	}{{"human", s.Human}, {"public_dataset", s.PublicDataset}, {"uniform_audit", s.UniformAudit}, {"judge", s.Judge}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", p.name, p.n))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// f3 formats a value with three decimals, rounding half up.
func f3(x float64) string {
	return strconv.FormatFloat(math.Floor(x*1000+0.5)/1000, 'f', 3, 64)
}

func opt(x *float64) string {
	if x == nil {
		return "n/a"
	}
	return f3(*x)
}

// pct formats a proportion as a percentage with one decimal, rounding half up.
func pct(p float64) string {
	return strconv.FormatFloat(math.Floor(p*1000+0.5)/10, 'f', 1, 64) + "%"
}

func signed(x float64) string {
	if x < 0 {
		return "- " + strconv.FormatFloat(-x, 'f', -1, 64)
	}
	return "+ " + strconv.FormatFloat(x, 'f', -1, 64)
}
