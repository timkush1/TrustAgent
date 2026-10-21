package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/grpc/enginetest"
)

// reportingEngine is an engine whose health answer reports the runtime configuration it was
// told to report, and whose EngineInfo names its env-managed keys.
type reportingEngine struct {
	mu      sync.Mutex
	version int64
	epoch   string
	eng     *enginetest.Engine
}

func newReportingEngine(t *testing.T, envManaged ...string) *reportingEngine {
	t.Helper()
	r := &reportingEngine{}
	r.eng = enginetest.Start(t, &enginetest.Engine{
		HealthFunc: func(context.Context) (*pb.HealthResponse, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return &pb.HealthResponse{Healthy: true, Version: "2.0.0", RuntimeConfigVersion: r.version, RuntimeConfigEpoch: r.epoch}, nil
		},
		EngineInfoFunc: func(context.Context) (*pb.EngineInfo, error) {
			return &pb.EngineInfo{Version: "2.0.0", EnvManaged: envManaged}, nil
		},
	})
	return r
}

func (r *reportingEngine) report(version int64, epoch string) {
	r.mu.Lock()
	r.version, r.epoch = version, epoch
	r.mu.Unlock()
}

// waitPushes waits until the engine received n pushes and returns them.
func (r *reportingEngine) waitPushes(t *testing.T, n int) []*pb.RuntimeConfig {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if p := r.eng.Pushes(); len(p) >= n {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine received %d pushes, want %d", len(r.eng.Pushes()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestEnginePushAtBoot (D34): the gateway pushes the runtime configuration in effect at boot,
// with the effective price table and no judge (none is bound); /api/status shows the epoch the
// engine applied, and the engine's env-managed keys reach the runtime configuration.
func TestEnginePushAtBoot(t *testing.T) {
	re := newReportingEngine(t, "LLM_PROVIDER", "ANTHROPIC_API_KEY")
	env := newTestEnv(t, envOpts{engine: re.eng})
	snap := env.srv.rt.manager.Current()
	push := re.waitPushes(t, 1)[0]
	if push.GetVersion() != snap.Version || push.GetEpoch() != snap.Epoch || snap.Epoch == "" {
		t.Fatalf("boot push (version %d, epoch %q), want the snapshot's (%d, %q)", push.GetVersion(), push.GetEpoch(), snap.Version, snap.Epoch)
	}
	if push.GetJudge() != nil || len(push.GetCredentials()) != 0 || push.GetDecision() != nil {
		t.Fatalf("boot push carries a judge or credentials without any binding: %v", push)
	}
	if !json.Valid(push.GetPriceTableJson()) || len(push.GetPriceTableJson()) == 0 {
		t.Fatal("the boot push has no price table")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		var st struct {
			Engine struct {
				RuntimeConfigEpoch *string `json:"runtime_config_epoch"`
			} `json:"engine"`
		}
		code, body := env.get("/api/status", testKey)
		if code != http.StatusOK {
			t.Fatalf("/api/status: %d", code)
		}
		if err := json.Unmarshal(body, &st); err != nil {
			t.Fatal(err)
		}
		if st.Engine.RuntimeConfigEpoch != nil && *st.Engine.RuntimeConfigEpoch == snap.Epoch {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("/api/status engine.runtime_config_epoch = %v, want %q", st.Engine.RuntimeConfigEpoch, snap.Epoch)
		}
		time.Sleep(5 * time.Millisecond)
	}

	env.srv.apply.mu.Lock()
	envManaged := env.srv.apply.engineEnv
	env.srv.apply.mu.Unlock()
	if !slices.Equal(envManaged, []string{"ANTHROPIC_API_KEY", "LLM_PROVIDER"}) {
		t.Fatalf("engine env-managed keys handed to the runtime configuration: %v", envManaged)
	}
}

// TestEnginePushFollowsEngineHealth: an engine that reports the configuration in effect gets no
// push; one that restarted (version 0, no epoch) or applied another epoch gets it again.
func TestEnginePushFollowsEngineHealth(t *testing.T) {
	re := newReportingEngine(t)
	env := newTestEnv(t, envOpts{engine: re.eng})
	snap := env.srv.rt.manager.Current()
	re.report(snap.Version, snap.Epoch)
	re.waitPushes(t, 1)
	// The boot health check may have raced the boot push; let both settle.
	time.Sleep(50 * time.Millisecond)
	n := len(re.eng.Pushes())

	ctx := context.Background()
	env.srv.pinger.Check(ctx)
	time.Sleep(50 * time.Millisecond)
	if got := len(re.eng.Pushes()); got != n {
		t.Fatalf("an engine reporting the configuration in effect was pushed again (%d → %d)", n, got)
	}
	re.report(0, "")
	env.srv.pinger.Check(ctx)
	re.waitPushes(t, n+1)
	re.report(snap.Version, "0192f3a0-0000-7000-8000-000000000000")
	env.srv.pinger.Check(ctx)
	pushes := re.waitPushes(t, n+2)
	if last := pushes[len(pushes)-1]; last.GetEpoch() != snap.Epoch {
		t.Fatalf("re-push epoch %q, want the snapshot's %q", last.GetEpoch(), snap.Epoch)
	}
}

// TestEnginePushAtBootWithDatabase (D31, D34): with a settings store, the boot push carries the
// stored epoch and a version of at least 1 (the epoch row's), so an engine restart (version 0)
// is told the configuration in effect.
func TestEnginePushAtBootWithDatabase(t *testing.T) {
	pg := runtimePG(t)
	re := newReportingEngine(t)
	env := newTestEnv(t, envOpts{pg: pg, master: durableMaster(t, 41), engine: re.eng})
	epoch, version, err := pg.RuntimeEpoch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	push := re.waitPushes(t, 1)[0]
	if push.GetVersion() < 1 || push.GetVersion() != version || push.GetEpoch() != epoch {
		t.Fatalf("boot push (version %d, epoch %q), want the store's (%d, %q)", push.GetVersion(), push.GetEpoch(), version, epoch)
	}
	if snap := env.srv.rt.manager.Current(); snap.Version != version || snap.Epoch != epoch {
		t.Errorf("snapshot (%d, %q) is not the store's", snap.Version, snap.Epoch)
	}
}

// TestEnginePushCarriesAPricingOverride (D34, D53): a pricing override saved through
// PATCH /api/settings reaches the engine in the next push, without a restart: the pushed table is
// the cost ledger's effective table, the override listed first and labelled override, so the
// engine prices its judge calls as the gateway prices them.
func TestEnginePushCarriesAPricingOverride(t *testing.T) {
	pg := runtimePG(t)
	re := newReportingEngine(t)
	env := newTestEnv(t, envOpts{pg: pg, master: durableMaster(t, 43), engine: re.eng, adminKey: settingsAdminKey})
	pushedTable := func(p *pb.RuntimeConfig) decision.PriceTable {
		t.Helper()
		var table decision.PriceTable
		if err := json.Unmarshal(p.GetPriceTableJson(), &table); err != nil || len(table.Entries) == 0 {
			t.Fatalf("pushed price table: %v %s", err, p.GetPriceTableJson())
		}
		return table
	}
	boot := re.waitPushes(t, 1)
	if first := pushedTable(boot[len(boot)-1]).Entries[0]; first.Label == decision.PriceLabelOverride {
		t.Fatalf("the boot push already carries an override: %+v", first)
	}

	_, etag := env.settings(settingsAdminKey)
	resp, body := env.patchSettings(etag, `{"pricing_overrides":[{"provider":"anthropic","match":"claude-haiku-4-5*",
		"input_per_mtok":1.1,"output_per_mtok":5.5,"label":"override","as_of":"2026-10-01"}]}`, "")
	env.patched(resp, body)
	version := env.srv.rt.manager.Current().Version

	deadline := time.Now().Add(5 * time.Second)
	var last *pb.RuntimeConfig
	for {
		if p := re.eng.Pushes(); len(p) > 0 && p[len(p)-1].GetVersion() >= version {
			last = p[len(p)-1]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no push of version %d reached the engine", version)
		}
		time.Sleep(5 * time.Millisecond)
	}
	table := pushedTable(last)
	first := table.Entries[0]
	if first.Provider != "anthropic" || first.Match != "claude-haiku-4-5*" || first.InputPerMTok != 1.1 || first.OutputPerMTok != 5.5 ||
		first.Label != decision.PriceLabelOverride {
		t.Errorf("the pushed table's first entry = %+v, want the override", first)
	}
	var vendor bool
	for _, e := range table.Entries[1:] {
		vendor = vendor || (e.Provider == "anthropic" && e.Match == "claude-haiku-4-5*" && e.Label != decision.PriceLabelOverride)
	}
	if !vendor {
		t.Errorf("the contract's own entry is gone from the pushed table: %+v", table.Entries)
	}
}
