package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/credentials"
	"github.com/truthtable/backend-go/internal/jev"
)

// probeRow is one cost_events row of a decision-model probe as the probe cost test reads it.
type probeRow struct {
	kind, provider, status, source string
	usd                            float64
}

// probeRows reads the cost_events rows of component probe, oldest first.
func probeRows(t *testing.T, db *pgxpool.Pool) []probeRow {
	t.Helper()
	rows, err := db.Query(context.Background(), `SELECT kind, COALESCE(provider, ''), COALESCE(status, ''), cost_source,
		COALESCE(cost_usd, 0)::FLOAT8 FROM cost_events WHERE component = $1 ORDER BY id`, jev.ComponentProbe)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []probeRow
	for rows.Next() {
		var r probeRow
		if err := rows.Scan(&r.kind, &r.provider, &r.status, &r.source, &r.usd); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// flushProbeRows flushes the cost ledger until n probe rows are stored (or 10 s pass).
func flushProbeRows(t *testing.T, env *testEnv, db *pgxpool.Pool, n int) []probeRow {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := env.srv.stopCosts(context.Background()); err != nil {
			t.Fatal(err)
		}
		rows := probeRows(t, db)
		if len(rows) >= n || time.Now().After(deadline) {
			return rows
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// statusBudgetsOf is GET /api/status's budgets block.
func statusBudgetsOf(t *testing.T, r *credRig) statusBudgets {
	t.Helper()
	code, body := r.call(http.MethodGet, "/api/status", credAdminKey, nil)
	var st struct {
		Budgets statusBudgets `json:"budgets"`
	}
	if code != http.StatusOK || json.Unmarshal(body, &st) != nil {
		t.Fatalf("status %d: %s", code, body)
	}
	return st.Budgets
}

// TestProbeCostIsBookedOnTheLedger (D40, D82): the probe of every key check (the check of
// PUT /api/credentials/:name, POST /api/credentials/:name/verify) and of the decision-model test
// (POST /api/decision-model/test) is booked on the cost ledger: one cost_events row of component
// probe with the vendor's reported cost, the same amount in the day's decision spend and in
// trustagent_gateway_jev_cost_usd_total{component="probe"}. Probes ask as non-enforcing calls:
// once the day's decision spend reaches the observe line they are held and cost nothing (a key
// check still passes on OpenRouter's free key check, marked probe_skipped; the decision-model test
// reports the budget), while an enforcing gate call still spends the enforcing reserve.
func TestProbeCostIsBookedOnTheLedger(t *testing.T) {
	pg, db := newPGStore(t)
	// #60 allows one test per caller every 10 s: the second test after the observe line comes
	// from a second admin.
	const secondAdmin = "cred-test-admin-key-0002"
	r := newCredRig(t, credRigOpts{pg: pg, mutate: func(c *config.Config) {
		c.Jev.StartupProbe = false // only the probes the test asks for
		c.Auth.AdminKeys = append(c.Auth.AdminKeys, config.APIKey{Label: "admin-2", Key: secondAdmin})
	}})
	ledger := r.env.srv.ledger()
	if ledger == nil {
		t.Fatal("the gateway built no cost ledger")
	}
	metricBefore := jevCostTotal(t, jev.BackendOpenRouterDecisions, jev.ComponentProbe)

	// 1. Three probes, each booked at the cost the vendor reported.
	put := r.put("openrouter", map[string]any{"provider": "openrouter", "secret": credGoodKey, "bind": []string{"decision_model"}})
	if !put.Stored || put.Verification == nil || !put.Verification.OK || put.Verification.CostUSD == nil {
		t.Fatalf("PUT: %s", put.body)
	}
	code, b := r.call(http.MethodPost, "/api/credentials/openrouter/verify", credAdminKey, map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("verify: %d %s", code, b)
	}
	verify := decodeWriteResult(t, b)
	if verify.Verification == nil || !verify.Verification.OK || verify.Verification.CostUSD == nil {
		t.Fatalf("verify: %s", b)
	}
	waitFor(t, "the bound decision model", func() bool { return r.env.srv.gateway.client.Info().Backend == jev.BackendOpenRouterDecisions })
	resp, body := postDecisionModelTest(t, r.env, credAdminKey)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("#60: %d %s", resp.StatusCode, body)
	}
	test := decodeDecisionModelTest(t, body)
	if !test.OK || test.CostUSD == nil || test.CostSource == nil || *test.CostSource != "reported" {
		t.Fatalf("#60: %s", body)
	}
	reported := []float64{*put.Verification.CostUSD, *verify.Verification.CostUSD, *test.CostUSD}

	rows := flushProbeRows(t, r.env, db, 3)
	if len(rows) != 3 {
		t.Fatalf("probe rows = %+v, want 3", rows)
	}
	var sum float64
	for i, row := range rows {
		if row.kind != "decision" || row.provider != jev.BackendOpenRouterDecisions || row.status != jev.AttemptOK || row.source != "reported" {
			t.Errorf("probe row %d = %+v", i, row)
		}
		if math.Abs(row.usd-reported[i]) > 1e-9 || row.usd <= 0 {
			t.Errorf("probe row %d costs %v, the answer reported %v", i, row.usd, reported[i])
		}
		sum += row.usd
	}
	if delta := jevCostTotal(t, jev.BackendOpenRouterDecisions, jev.ComponentProbe) - metricBefore; math.Abs(delta-sum) > 1e-9 {
		t.Errorf("jev_cost_usd_total{component=probe} grew by %v, the rows sum to %v", delta, sum)
	}
	if got := statusBudgetsOf(t, r).DecisionSpentTodayUSD; math.Abs(got-sum) > 1e-9 {
		t.Errorf("decision spend today = %v, want the probes' %v", got, sum)
	}

	// 2. The day's decision spend reaches the observe line; the enforcing reserve is left.
	observe := ledger.Headroom(false)
	hold, err := ledger.Decision().Reserve(observe, true, "")
	if err != nil {
		t.Fatal(err)
	}
	hold.Settle(observe)
	if ledger.Headroom(false) != 0 || ledger.Headroom(true) <= 0 {
		t.Fatalf("headroom: observe %v, enforcing %v", ledger.Headroom(false), ledger.Headroom(true))
	}
	if st := statusBudgetsOf(t, r); !st.DecisionReserveOnly {
		t.Errorf("status budgets = %+v, want reserve only", st)
	}

	// A key check proves the key with the free key check and holds the probe.
	again := r.put("openrouter", map[string]any{"provider": "openrouter", "secret": credOtherGoodKey})
	if v := again.Verification; v == nil || !v.OK || v.Hint == nil || *v.Hint != credentials.HintProbeSkipped || v.CostUSD != nil {
		t.Errorf("a key check at the observe line: %s", again.body)
	}
	// The decision-model test is refused by the budget, without a call.
	resp, body = postDecisionModelTest(t, r.env, secondAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("#60 at the observe line: %d %s", resp.StatusCode, body)
	}
	if held := decodeDecisionModelTest(t, body); held.OK || held.Error == nil || held.Error.Kind != jev.KindBudget || held.CostUSD != nil {
		t.Errorf("#60 at the observe line: %s", body)
	}
	// An enforcing gate call still decides on the reserve.
	if _, err := r.env.srv.gateway.client.Decide(context.Background(), decisionRequest(),
		jev.CallOptions{TraceID: "tr-reserve", Route: "support", Enforcing: true}); err != nil {
		t.Fatalf("an enforcing call at the observe line: %v", err)
	}
	if gate := waitCostRows(t, r.env, db, "tr-reserve", 1); len(gate) != 1 || gate[0].status != jev.AttemptOK || gate[0].usd <= 0 {
		t.Errorf("the enforcing call's rows = %+v", gate)
	}
	if rows := flushProbeRows(t, r.env, db, 0); len(rows) != 3 {
		t.Errorf("held probes booked rows: %+v", rows)
	}
	r.assertNoLeak(credGoodKey)
	r.assertNoLeak(credOtherGoodKey)
}
