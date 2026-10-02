package gate

import (
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
)

// SeedUniform carries today's uniform-sample count across a restart.
func TestSeedUniformSetsTodaysCount(t *testing.T) {
	cl, err := jev.New(jev.Config{Backend: jev.BackendMock})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	g, err := New(Config{Jev: cl, Evaluators: registry(t), UniformAuditsPerDay: 3, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	g.SeedUniform(2)
	if !g.uniform.take(now) || g.uniform.take(now) {
		t.Error("after a seed of 2 with a cap of 3, exactly one more uniform sample fits today")
	}
	if !g.uniform.take(now.Add(24 * time.Hour)) {
		t.Error("the seeded count belongs to its own day")
	}
	g.SeedUniform(-5)
	if g.uniform.n != 0 {
		t.Errorf("a negative seed counts as zero, got %d", g.uniform.n)
	}
}

// The exported compile helpers are what the gate's methods use (shared with the decision-request
// recompile, D63).
func TestRouteBudgetKeepsBaseAndRouteCaps(t *testing.T) {
	r := &policy.Route{}
	r.Decision.MaxResponseTokens, r.Evidence.MaxTokens = 32, 4000
	base := decision.DefaultBudget()
	b := RouteBudget(base, r, 0)
	if b.CPT != base.CPT || b.MaxResponseTokens != 32 || b.EvidenceMaxTokens != 4000 {
		t.Errorf("RouteBudget without cpt = %+v", b)
	}
	if b := RouteBudget(base, r, 3.1); b.CPT != 3.1 {
		t.Errorf("RouteBudget with cpt = %+v", b)
	}
}
