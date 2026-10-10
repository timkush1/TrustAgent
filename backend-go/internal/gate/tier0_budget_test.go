package gate_test

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
)

// Tier-0 budget (D120): the detectors' p50 on 8k-token evidence and a 1k-token answer stays under
// 4 ms here and 12 ms on a shared CI runner (CI=true): headroom over the published statement,
// "Tier-0 p50 < 2 ms for ≤ 32k-token evidence and ≤ 1k-token answers, on the stated machine".
const (
	tier0BudgetLocal = 4 * time.Millisecond
	tier0BudgetCI    = 12 * time.Millisecond
	tier0Runs        = 41
)

// tier0P50 times decision.Detect runs times (after two warm-up runs) and returns the median.
func tier0P50(t *testing.T, in *decision.DetectorInput, runs int) time.Duration {
	t.Helper()
	for range 2 {
		if _, err := decision.Detect(in); err != nil {
			t.Fatal(err)
		}
	}
	took := make([]time.Duration, runs)
	for i := range took {
		start := time.Now()
		if _, err := decision.Detect(in); err != nil {
			t.Fatal(err)
		}
		took[i] = time.Since(start)
	}
	slices.Sort(took)
	return took[runs/2]
}

// TestTier0Budget guards the Tier-0 budget. Timing under -race or -short means nothing, so it is
// skipped there (go test -run TestTier0Budget without either flag runs it). The budget case is a
// plain 1k-token answer; the dense answer (a date, a URL, a quotation and a citation in every
// sentence) and the 1 MiB document are reported, never asserted.
func TestTier0Budget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timings are meaningless under -race and skipped under -short")
	}
	budget := tier0BudgetLocal
	if os.Getenv("CI") == "true" {
		budget = tier0BudgetCI
	}
	for _, c := range []struct {
		name   string
		in     *decision.DetectorInput
		assert bool
	}{
		{"evidence=8k/answer=1k", detectorInput(answerText(1000, false), evidenceText(8000)), true},
		{"evidence=8k/answer=1k dense (report only)", detectorInput(answerText(1000, true), evidenceText(8000)), false},
		{"evidence=1MiB/answer=1k (report only)", detectorInput(answerText(1000, false), evidenceText(int(float64(1<<20)/charsPerToken))), false},
	} {
		runs := tier0Runs
		if !c.assert {
			runs = 7
		}
		p50 := tier0P50(t, c.in, runs)
		t.Logf("Tier 0 %s: p50 %s (budget %s)", c.name, p50, budget)
		if c.assert && p50 >= budget {
			t.Errorf("Tier 0 %s: p50 %s, over the %s budget", c.name, p50, budget)
		}
	}
}
