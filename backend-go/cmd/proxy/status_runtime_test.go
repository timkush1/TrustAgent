package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/credentials"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/runtimecfg"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

// statusOf reads GET /api/status with key, validated against the contract.
func (e *testEnv) statusOf(key string) systemStatus {
	e.t.Helper()
	resp, body := e.request(http.MethodGet, "/api/status", key, nil)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("GET /api/status: %d %s", resp.StatusCode, body)
	}
	contractstest.ValidateJSON(e.t, schemaStatus, body)
	var st systemStatus
	if err := json.Unmarshal(body, &st); err != nil {
		e.t.Fatal(err)
	}
	return st
}

// TestStatusDecisionCredential (SEC-6, D79): the decision model's credential carries its name, the
// env var it comes from, for every caller, and its mask for an admin only.
func TestStatusDecisionCredential(t *testing.T) {
	vendor, _ := echoVendor(t)
	t.Setenv("TRUTHTABLE_JEV_API_KEY", noPatternSecret)
	env := newTestEnv(t, envOpts{adminKey: leakAdminKey, mutate: func(cfg *config.Config) {
		cfg.Jev.Backend, cfg.Jev.BaseURL, cfg.Jev.Model, cfg.Jev.StartupProbe = jev.BackendCompatible, vendor.URL, "jev-status-test", false
	}})
	admin := env.statusOf(leakAdminKey).DecisionModel.Credential
	if admin.Source != "env" || admin.Name == nil || *admin.Name != "TRUTHTABLE_JEV_API_KEY" ||
		admin.Masked == nil || *admin.Masked != config.MaskSecret(noPatternSecret) {
		t.Errorf("admin sees %+v", admin)
	}
	reader := env.statusOf(testKey).DecisionModel.Credential
	if reader.Source != "env" || reader.Name == nil || *reader.Name != "TRUTHTABLE_JEV_API_KEY" || reader.Masked != nil {
		t.Errorf("a reader sees the name, never the mask: %+v", reader)
	}
}

// TestDecisionCredentialFollowsTheClient: a dashboard credential shows by its name; while the
// decision client has not caught up with the configuration (its source differs), no name shows.
func TestDecisionCredentialFollowsTheClient(t *testing.T) {
	name := credentials.NameOpenRouter
	snap := &runtimecfg.Snapshot{
		Settings: runtimecfg.RuntimeSettings{Bindings: runtimecfg.Bindings{DecisionModel: &name}},
		Credentials: runtimecfg.CredentialSet{name: {Name: name, Provider: credentials.ProviderOpenRouter,
			Source: credentials.SourceDashboard, Masked: "sk-…cdef"}},
	}
	s := &statusReporter{runtime: func() *runtimecfg.Snapshot { return snap }}
	if c := s.decisionCredential("dashboard", true); c.Name == nil || *c.Name != "openrouter" || c.Masked == nil || *c.Masked != "sk-…cdef" {
		t.Errorf("dashboard, admin: %+v", c)
	}
	if c := s.decisionCredential("dashboard", false); c.Name == nil || c.Masked != nil {
		t.Errorf("dashboard, reader: %+v", c)
	}
	if c := s.decisionCredential("env", true); c.Name != nil || c.Masked != nil || c.Source != "env" {
		t.Errorf("the client uses another credential: %+v", c)
	}
	if c := s.decisionCredential("", true); c.Source != "none" || c.Name != nil {
		t.Errorf("no credential: %+v", c)
	}
	if c := (&statusReporter{}).decisionCredential("dashboard", true); c.Name != nil {
		t.Errorf("without a runtime configuration: %+v", c)
	}
}

// TestStatusFailClosedUnprotected (D77): with the decision model off and a fail-closed enforcing
// route in the policy, /api/status carries fail_closed_unprotected with the routes, in warnings
// and warning_details.
func TestStatusFailClosedUnprotected(t *testing.T) {
	env := newTestEnv(t, envOpts{mutate: func(cfg *config.Config) { cfg.Jev.Backend = jev.BackendOff }})
	st := env.statusOf(testKey)
	if st.DecisionModel.State != "off" {
		t.Fatalf("decision model %s", st.DecisionModel.State)
	}
	i := slices.IndexFunc(st.WarningDetails, func(w statusWarning) bool { return w.Code == runtimecfg.WarnFailClosedUnprotected })
	if i < 0 {
		t.Fatalf("warning_details %+v", st.WarningDetails)
	}
	w := st.WarningDetails[i]
	if w.Details == nil || !slices.Contains(w.Details.Routes, "payments-faq") || !slices.Contains(st.Warnings, w.Message) {
		t.Errorf("warning %+v; warnings %v", w, st.Warnings)
	}
	// The mock decision model answers: no warning.
	if st := newTestEnv(t, envOpts{}).statusOf(testKey); len(st.WarningDetails) != 0 {
		t.Errorf("mock: %+v", st.WarningDetails)
	}
}

// TestStatusCredentialStore (D32): the store's state and the rows the master key cannot open come
// from the runtime configuration, with the master_key_changed warning naming them.
func TestStatusCredentialStore(t *testing.T) {
	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	putSealed(t, pg, durableMaster(t, 1), "typesafe", "sk-test-typesafe-0123456789", nil)
	env := newTestEnv(t, envOpts{store: pg, master: durableMaster(t, 2)})
	st := env.statusOf(testKey)
	if st.Credentials.Store != credentials.StateReady || !slices.Equal(st.Credentials.Undecryptable, []string{"typesafe"}) {
		t.Errorf("credentials %+v", st.Credentials)
	}
	i := slices.IndexFunc(st.WarningDetails, func(w statusWarning) bool { return w.Code == runtimecfg.WarnMasterKeyChanged })
	if i < 0 || st.WarningDetails[i].Details == nil || !slices.Equal(st.WarningDetails[i].Details.Names, []string{"typesafe"}) {
		t.Errorf("warning_details %+v", st.WarningDetails)
	}

	if st := newTestEnv(t, envOpts{noStore: true}).statusOf(testKey); st.Credentials.Store != credentials.StateNoDatabase {
		t.Errorf("without a database: %+v", st.Credentials)
	}
	ephemeral := newTestEnv(t, envOpts{store: pg})
	if st := ephemeral.statusOf(testKey); st.Credentials.Store != credentials.StateUnavailable ||
		!slices.ContainsFunc(st.WarningDetails, func(w statusWarning) bool { return w.Code == runtimecfg.WarnStoreUnavailable }) {
		t.Errorf("ephemeral master: %+v %+v", st.Credentials, st.WarningDetails)
	}
}

// TestJudgeCredentialSourceFromTheEngine (SEC-6): the judge's credential source is what the engine
// reports, a dashboard key included; a value outside the contract keeps the fallback.
func TestJudgeCredentialSourceFromTheEngine(t *testing.T) {
	health := func(src string) grpc.EngineHealth {
		return grpc.EngineHealth{State: grpc.StateUp,
			Response: &pb.HealthResponse{Healthy: true, JudgeProvider: "openrouter", Dependencies: map[string]bool{"judge": true}},
			Info:     &pb.EngineInfo{Judge: &pb.JudgeInfo{EffectiveProvider: "openrouter", CredentialSource: src}}}
	}
	for src, want := range map[string]string{"dashboard": "dashboard", "env": "env", "none": "none", "": "env", "keyring": "env"} {
		if got := judgeStatus(health(src)).CredentialSource; got != want {
			t.Errorf("engine says %q: %q, want %q", src, got, want)
		}
	}
}

// TestStatusPushHook: the engine push as last applied shows its epoch and its warning.
func TestStatusPushHook(t *testing.T) {
	s := &statusReporter{cfg: &config.Config{}, jev: jev.Info{Backend: jev.BackendMock}, now: time.Now,
		auth: &apiAuth{keys: authz.New(nil, "keys")},
		push: func() pushState {
			return pushState{Version: 4, Epoch: "0190c0de-0000-7000-8000-000000000001", Warning: "The audit engine predates P4."}
		}}
	st := s.system(context.Background())
	if st.Engine.RuntimeConfigEpoch == nil || *st.Engine.RuntimeConfigEpoch != "0190c0de-0000-7000-8000-000000000001" ||
		!slices.ContainsFunc(st.Warnings, func(w string) bool { return strings.Contains(w, "predates P4") }) {
		t.Errorf("engine %+v warnings %v", st.Engine, st.Warnings)
	}
}
