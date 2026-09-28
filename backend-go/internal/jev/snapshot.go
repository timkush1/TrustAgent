package jev

import (
	"math"
	"sync"
	"sync/atomic"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/metrics"
)

// SnapshotChange reports that a model is answered by a different snapshot than before, e.g.
// typesafe/jev-1.13-20260917 → typesafe/jev-1.13-20261015 (SPEC §2.11 snapshot change).
type SnapshotChange struct {
	Backend string
	// Model is the requested model; Previous is "" when no snapshot was known for it.
	Model, Previous, Snapshot string
}

// snapshotTracker remembers the snapshot answering each requested model, so a route that pins
// another model does not look like a snapshot change of the default one.
type snapshotTracker struct {
	backend  string
	onChange func(SnapshotChange)

	mu      sync.Mutex
	current map[string]string
}

func newSnapshotTracker(backend, defaultModel, known string, onChange func(SnapshotChange)) *snapshotTracker {
	t := &snapshotTracker{backend: backend, onChange: onChange, current: map[string]string{}}
	if known != "" && defaultModel != "" {
		t.current[defaultModel] = known
		metrics.JevSnapshotInfo.WithLabelValues(known).Set(1)
	}
	return t
}

// observe records the snapshot that answered model and fires the change hook when it differs.
func (t *snapshotTracker) observe(model, snapshot string) {
	if snapshot == "" {
		return
	}
	t.mu.Lock()
	prev, seen := t.current[model]
	if seen && prev == snapshot {
		t.mu.Unlock()
		return
	}
	t.current[model] = snapshot
	stillUsed := false
	for _, s := range t.current {
		stillUsed = stillUsed || s == prev
	}
	t.mu.Unlock()

	metrics.JevSnapshotInfo.WithLabelValues(snapshot).Set(1)
	if prev != "" {
		metrics.JevAnomalies.WithLabelValues(AnomalySnapshotChanged).Inc()
		if !stillUsed {
			metrics.JevSnapshotInfo.DeleteLabelValues(prev)
		}
	}
	if t.onChange != nil {
		t.onChange(SnapshotChange{Backend: t.backend, Model: model, Previous: prev, Snapshot: snapshot})
	}
}

func (t *snapshotTracker) get(model string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current[model]
}

// cptTracker is the runtime chars-per-token estimate (SPEC §2.9, Go only): an EWMA corrected
// from each response's usage.input_tokens.
type cptTracker struct{ bits atomic.Uint64 }

func newCPTTracker(initial float64) *cptTracker {
	if !(initial > 0) || math.IsInf(initial, 0) {
		initial = decision.DefaultCPT
	}
	t := &cptTracker{}
	t.bits.Store(math.Float64bits(math.Min(decision.MaxCPT, math.Max(decision.MinCPT, initial))))
	return t
}

func (t *cptTracker) get() float64 { return math.Float64frombits(t.bits.Load()) }

// observe folds one measurement into the estimate. est() counts wide code points as one token
// each and narrow ones at cpt per token, so the narrow characters are matched against the tokens
// left after the wide ones: cpt ← clamp(0.8·cpt + 0.2·narrow/(input_tokens − wide − OVERHEAD)).
func (t *cptTracker) observe(count decision.TokenCount, inputTokens int) {
	for {
		old := t.bits.Load()
		next := decision.UpdateCPT(math.Float64frombits(old), count.Narrow, inputTokens-count.Wide)
		if t.bits.CompareAndSwap(old, math.Float64bits(next)) {
			return
		}
	}
}
