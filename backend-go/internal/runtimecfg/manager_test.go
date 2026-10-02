package runtimecfg

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/credentials"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/store"
)

// noPatternSecret matches no redaction pattern (P4-PLAN §0 rule 8).
const noPatternSecret = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"

type fakeStore struct {
	mu      sync.Mutex
	rows    *store.SettingsRows
	creds   []store.CredentialRecord
	err     error
	reads   int
	touched map[string]int
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: &store.SettingsRows{Version: 1, Epoch: "0190c0de-0000-7000-8000-000000000001", Rows: map[string]json.RawMessage{}},
		touched: map[string]int{}}
}

func (f *fakeStore) RuntimeState(context.Context) (*store.SettingsRows, []store.CredentialRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.err != nil {
		return nil, nil, f.err
	}
	rows := &store.SettingsRows{Version: f.rows.Version, Epoch: f.rows.Epoch, Rows: map[string]json.RawMessage{}}
	for k, v := range f.rows.Rows {
		rows.Rows[k] = v
	}
	return rows, slices.Clone(f.creds), nil
}

func (f *fakeStore) TouchCredential(_ context.Context, name string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched[name]++
	return nil
}

func (f *fakeStore) set(fn func(f *fakeStore)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeStore) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func master(t *testing.T, b byte) *authz.MasterKey {
	t.Helper()
	m, err := authz.LoadMasterKey(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, authz.MasterKeySize)), "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// sealRow returns a credentials row for secret sealed under mk.
func sealRow(t *testing.T, mk *authz.MasterKey, name, secret string, origins ...string) store.CredentialRecord {
	t.Helper()
	c, err := credentials.New(mk)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := credentials.Lookup(name)
	ct, nonce, keyID, err := c.Seal(name, spec.Provider, origins, secret)
	if err != nil {
		t.Fatal(err)
	}
	return store.CredentialRecord{Name: name, Provider: spec.Provider, Ciphertext: ct, Nonce: nonce, KeyID: keyID,
		Masked: credentials.Masked(secret), Fingerprint: c.Fingerprint(secret), AllowedHosts: origins}
}

func loadCfg(env ...string) *config.Config {
	return config.LoadWith(config.LoadOptions{Environ: env})
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestEnvWinsAndIsListed(t *testing.T) {
	cfg := loadCfg("TRUTHTABLE_JEV_MODEL=env-model", "TRUTHTABLE_SPEND_CAP_LLM_USD_DAY=7", "UPSTREAM_LLM_URL=https://llm.example.com",
		"TRUTHTABLE_UPSTREAM_API_KEY=sk-test-upstream-env-0001")
	fs := newFakeStore()
	fs.rows.Rows["decision_model"] = json.RawMessage(`{"model":"row-model","backend":"mock"}`)
	fs.rows.Rows["budgets"] = json.RawMessage(`{"llm_usd_day":1,"decision_usd_day":0.5}`)
	fs.rows.Rows["upstream"] = json.RawMessage(`{"default":{"url":"https://row.example.com","credential":"upstream-default"},"fallback":{"url":"https://fb.example.com"}}`)
	m := New(Options{Config: cfg, Store: fs, Master: master(t, 1), Log: quietLog()})
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := m.Current()
	if *s.Settings.DecisionModel.Model != "env-model" || s.Settings.DecisionModel.Backend != "mock" {
		t.Errorf("decision model %+v: the env model wins, the row backend applies", s.Settings.DecisionModel)
	}
	if s.Settings.Budgets.LLMUSDDay != 7 || s.Settings.Budgets.DecisionUSDDay != 0.5 {
		t.Errorf("budgets %+v", s.Settings.Budgets)
	}
	if *s.Settings.Upstream.Default.URL != "https://llm.example.com" || *s.Settings.Upstream.Fallback.URL != "https://fb.example.com" {
		t.Errorf("upstream %+v", s.Settings.Upstream)
	}
	// Persisted keeps the rows without the env overlay: the base of a write.
	if *s.Persisted.DecisionModel.Model != "row-model" || s.Persisted.Budgets.LLMUSDDay != 1 || *s.Persisted.Upstream.Default.URL != "https://row.example.com" {
		t.Errorf("persisted %+v", s.Persisted)
	}
	want := []string{"decision_model.model", "upstream.default.url", "upstream.default.credential", "bindings.upstream_default", "budgets.llm_usd_day"}
	if !slices.Equal(s.EnvManaged, want) {
		t.Errorf("env managed %v, want %v", s.EnvManaged, want)
	}
	if !slices.Equal(s.EnvSources["upstream.default.url"], []string{"TRUTHTABLE_UPSTREAM_URL"}) ||
		!slices.Equal(s.EnvSources["bindings.upstream_default"], []string{"TRUTHTABLE_UPSTREAM_API_KEY"}) {
		t.Errorf("env sources %v", s.EnvSources)
	}
	if *s.Settings.Bindings.UpstreamDefault != credentials.NameUpstreamDefault || *s.Settings.Upstream.Default.Credential != credentials.NameUpstreamDefault {
		t.Errorf("the env upstream key binds the default upstream: %+v", s.Settings.Bindings)
	}
	c := s.Credentials[credentials.NameUpstreamDefault]
	if c.Source != credentials.SourceEnv || !slices.Equal(c.AllowedOrigins, []string{"https://llm.example.com"}) || c.Fingerprint == "" || c.Masked != "sk-…0001" {
		t.Errorf("env upstream credential %+v", c)
	}
	if s.Version != 1 || s.Epoch != fs.rows.Epoch || s.StoreState != credentials.StateReady {
		t.Errorf("version %d epoch %s state %s", s.Version, s.Epoch, s.StoreState)
	}
	// An empty env var counts as unset.
	if NewStatic(loadCfg("TRUTHTABLE_JEV_MODEL=")).Current().IsEnvManaged("decision_model.model") {
		t.Error("an empty env var is not env-managed")
	}
}

func TestDecisionBindingIsEnvManaged(t *testing.T) {
	for _, tc := range []struct {
		name       string
		env        []string
		binding    string // the env credential bound in effect, "" for none
		managed    bool
		pinnedFor  []string // credentials BackendPinned reports
		unaffected []string // credentials it does not
	}{
		{"auto with an OpenRouter key", []string{"OPENROUTER_API_KEY=sk-or-v1-e2e-0123456789"}, "openrouter", true, nil, []string{"openrouter", "typesafe"}},
		{"auto with a TypeSafe key", []string{"TYPESAFE_API_KEY=sk-test-typesafe-0001"}, "typesafe", true, nil, nil},
		{"auto with the compatible pair", []string{"TRUTHTABLE_JEV_BASE_URL=https://gw.example.com", "TRUTHTABLE_JEV_API_KEY=sk-test-compat-0001"}, "compatible", true, nil, nil},
		{"auto with a JEV key but no base", []string{"TRUTHTABLE_JEV_API_KEY=sk-test-compat-0001"}, "", false, nil, []string{"openrouter"}},
		{"explicit typesafe with the JEV key", []string{"TRUTHTABLE_JEV_BACKEND=typesafe", "TRUTHTABLE_JEV_API_KEY=sk-test-compat-0001"}, "compatible", true, []string{"openrouter", "compatible"}, []string{"typesafe"}},
		{"pinned to mock", []string{"TRUTHTABLE_JEV_BACKEND=mock"}, "", true, []string{"openrouter", "typesafe", "compatible"}, nil},
		{"pinned to off", []string{"TRUTHTABLE_JEV_BACKEND=off"}, "", true, []string{"openrouter"}, nil},
		{"pinned to openrouter without a key", []string{"TRUTHTABLE_JEV_BACKEND=openrouter-decisions"}, "", false, []string{"typesafe", "compatible", "anthropic"}, []string{"openrouter"}},
		{"nothing set", nil, "", false, nil, []string{"openrouter", "typesafe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStatic(loadCfg(tc.env...)).Current()
			if s.IsEnvManaged("bindings.decision_model") != tc.managed {
				t.Errorf("bindings.decision_model env-managed = %v, want %v (%v)", !tc.managed, tc.managed, s.EnvManaged)
			}
			got := ""
			if s.Settings.Bindings.DecisionModel != nil {
				got = *s.Settings.Bindings.DecisionModel
			}
			if got != tc.binding {
				t.Errorf("binding in effect %q, want %q", got, tc.binding)
			}
			for _, n := range tc.pinnedFor {
				if !s.BackendPinned(n) {
					t.Errorf("BackendPinned(%s) = false", n)
				}
			}
			for _, n := range tc.unaffected {
				if s.BackendPinned(n) {
					t.Errorf("BackendPinned(%s) = true", n)
				}
			}
		})
	}
}

func TestJudgeBindingIsEnvManagedFromTheEngine(t *testing.T) {
	m := NewStatic(loadCfg())
	var got []*Snapshot
	m.Subscribe(func(s *Snapshot) { got = append(got, s) })
	if m.Current().IsEnvManaged("bindings.judge") {
		t.Fatal("nothing pins the judge before the engine reports")
	}
	m.SetEngineEnvManaged([]string{"LLM_PROVIDER", "LLM_PROVIDER"})
	s := m.Current()
	if !s.IsEnvManaged("judge.provider") || !s.IsEnvManaged("bindings.judge") || s.IsEnvManaged("judge.model") ||
		!slices.Equal(s.EngineEnv, []string{"LLM_PROVIDER"}) {
		t.Errorf("after LLM_PROVIDER: %v %v", s.EnvManaged, s.EngineEnv)
	}
	if len(got) != 1 {
		t.Fatalf("a change of the engine env publishes once, got %d", len(got))
	}
	m.SetEngineEnvManaged([]string{"LLM_PROVIDER"})
	if len(got) != 1 {
		t.Error("the same names publish nothing")
	}
	// An engine env credential alone pins the judge binding too (the engine resolves from env).
	m.SetEngineEnvManaged([]string{"ANTHROPIC_API_KEY", "LLM_MODEL"})
	s = m.Current()
	if s.IsEnvManaged("judge.provider") || !s.IsEnvManaged("judge.model") || !s.IsEnvManaged("bindings.judge") ||
		!slices.Equal(s.EnvSources["bindings.judge"], []string{"ANTHROPIC_API_KEY"}) {
		t.Errorf("after ANTHROPIC_API_KEY: %v %v", s.EnvManaged, s.EnvSources)
	}
	m.SetEngineEnvManaged(nil)
	if m.Current().IsEnvManaged("bindings.judge") || len(got) != 3 {
		t.Errorf("an engine without env keys frees the judge (%d publications)", len(got))
	}
}

func TestApplyBindingsBackendPinnedComesFirst(t *testing.T) {
	s := NewStatic(loadCfg("TRUTHTABLE_JEV_BACKEND=mock")).Current()
	_, applied, skipped := ApplyBindings(s.Persisted, credentials.NameOpenRouter, []string{TargetDecisionModel, TargetJudge}, s.EnvManaged,
		s.BackendPinned(credentials.NameOpenRouter))
	if !slices.Equal(applied, []string{TargetJudge}) || !slices.Equal(skipped, []BindingSkip{{TargetDecisionModel, SkipBackendPinned}}) {
		t.Errorf("applied %v skipped %v", applied, skipped)
	}
	// A stored mock backend under the env pin is not flipped; a persisted mock judge is (D76).
	p := s.Persisted
	if p.DecisionModel.Backend != "auto" {
		t.Errorf("the write base holds the built-in backend, not the env one: %q", p.DecisionModel.Backend)
	}
	p.DecisionModel.Backend, p.Judge.Provider = "mock", "mock"
	out, _, _ := ApplyBindings(p, credentials.NameOpenRouter, []string{TargetJudge}, s.EnvManaged, false)
	if out.DecisionModel.Backend != "mock" || out.Judge.Provider != "auto" {
		t.Errorf("after the bind %+v %+v", out.DecisionModel, out.Judge)
	}
	rows, err := ChangedRows(p, out)
	if err != nil || len(rows) != 2 || string(rows["bindings"]) != `{"decision_model":null,"judge":"openrouter","upstream_default":null}` ||
		string(rows["judge"]) != `{"provider":"auto","model":null}` {
		t.Errorf("changed rows %s %v", rows, err)
	}
}

// TestChangedFieldsCarriesOnlyTheChangedFields: a credential write's patch holds the fields the
// bind changed and never an env-seeded sibling (TRUTHTABLE_JEV_MODEL here), so the env value is
// not copied into a row and a concurrent write to another field survives the merge.
func TestChangedFieldsCarriesOnlyTheChangedFields(t *testing.T) {
	s := NewStatic(loadCfg("TRUTHTABLE_JEV_MODEL=jev-env-model", "TRUTHTABLE_SPEND_CAP_LLM_USD_DAY=3")).Current()
	p := s.Persisted
	p.DecisionModel.Backend, p.Judge.Provider = "mock", "mock"
	up := "upstream-default"
	p.Bindings.UpstreamDefault = &up
	out, applied, _ := ApplyBindings(p, credentials.NameOpenRouter, []string{TargetDecisionModel, TargetJudge}, s.EnvManaged, false)
	if len(applied) != 2 {
		t.Fatalf("applied %v", applied)
	}
	rows, err := ChangedFields(p, out)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"decision_model": `{"backend":"auto"}`,
		"judge":          `{"provider":"auto"}`,
		"bindings":       `{"decision_model":"openrouter","judge":"openrouter"}`,
	}
	if len(rows) != len(want) {
		t.Errorf("rows %s", rows)
	}
	for k, w := range want {
		if string(rows[k]) != w {
			t.Errorf("%s = %s, want %s", k, rows[k], w)
		}
	}
	// pricing_overrides is an array: a change carries it whole.
	q := out
	q.PricingOverrides = append(q.PricingOverrides, decision.PriceEntry{Match: "m", Provider: "openrouter", InputPerMTok: 1, OutputPerMTok: 2})
	rows, err = ChangedFields(out, q)
	if err != nil || len(rows) != 1 || !strings.HasPrefix(string(rows["pricing_overrides"]), `[{"match":"m"`) {
		t.Errorf("overrides %s %v", rows, err)
	}
	if rows, err := ChangedFields(out, out); err != nil || len(rows) != 0 {
		t.Errorf("no change %s %v", rows, err)
	}
}

// TestPersistedNeverHoldsEnvValues: the write base (Snapshot.Persisted) is the built-in defaults
// and the stored rows; an env value shows only in Settings, so a whole-row write to a sibling
// field (ChangedRows) never copies it into a row, where it would outlive the env var (D33).
func TestPersistedNeverHoldsEnvValues(t *testing.T) {
	cfg := loadCfg("TRUTHTABLE_JEV_MODEL=env-model", "TRUTHTABLE_JEV_BACKEND=typesafe", "TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY=9",
		"TRUTHTABLE_UPSTREAM_AUTH_HEADER=api-key")
	builtin := NewStatic(loadCfg()).Current().Settings
	fs := newFakeStore()
	fs.rows.Rows["budgets"] = json.RawMessage(`{"llm_usd_day":4}`)
	m := New(Options{Config: cfg, Store: fs, Master: master(t, 9), Log: quietLog()})
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := m.Current()
	if *s.Settings.DecisionModel.Model != "env-model" || s.Settings.DecisionModel.Backend != "typesafe" ||
		s.Settings.Budgets.DecisionUSDDay != 9 || s.Settings.Upstream.Default.AuthHeader != "api-key" {
		t.Errorf("settings in effect %+v %+v %+v", s.Settings.DecisionModel, s.Settings.Budgets, s.Settings.Upstream.Default)
	}
	p := s.Persisted
	if p.DecisionModel.Model != nil || p.DecisionModel.Backend != builtin.DecisionModel.Backend ||
		p.Budgets.DecisionUSDDay != builtin.Budgets.DecisionUSDDay || p.Budgets.LLMUSDDay != 4 ||
		p.Upstream.Default.AuthHeader != builtin.Upstream.Default.AuthHeader {
		t.Errorf("persisted %+v %+v %+v: built-in defaults and the stored row only", p.DecisionModel, p.Budgets, p.Upstream.Default)
	}
	// A write to sibling fields starts from Persisted: the env values never reach the rows.
	after := p
	path := "/v2/decide"
	after.DecisionModel.Path = &path
	after.Budgets.LLMUSDDay = 5
	after.Upstream.Default.URL = &path
	rows, err := ChangedRows(p, after)
	if err != nil {
		t.Fatal(err)
	}
	for k, raw := range rows {
		for _, envValue := range []string{"env-model", "typesafe", `"decision_usd_day":9`, `"auth_header":"api-key"`} {
			if strings.Contains(string(raw), envValue) {
				t.Errorf("row %s = %s copies the env value %q", k, raw, envValue)
			}
		}
	}
	if len(rows) != 3 {
		t.Errorf("rows %s", rows)
	}
}

// TestEnvCompatibleKeyFollowsTheBackendInEffect: TRUTHTABLE_JEV_API_KEY serves an explicit backend
// (SPEC §7.5), whether the env or a settings row chose it, and goes only to that backend's vendor
// origin or to the env base URL; a base URL from a settings row never widens its origins.
func TestEnvCompatibleKeyFollowsTheBackendInEffect(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		env   []string
		row   string
		allow []string
		deny  []string
	}{
		{"row backend openrouter", nil, `{"backend":"openrouter-decisions"}`, []string{"https://openrouter.ai"}, []string{"https://api.typesafe.ai"}},
		{"row backend typesafe", nil, `{"backend":"typesafe"}`, []string{"https://api.typesafe.ai"}, []string{"https://openrouter.ai"}},
		{"row base URL", nil, `{"backend":"compatible","base_url":"https://evil.example.com","model":"m"}`, nil,
			[]string{"https://evil.example.com", "https://openrouter.ai"}},
		{"env base URL wins over a row backend", []string{"TRUTHTABLE_JEV_BASE_URL=https://gw.example.com"}, `{"backend":"typesafe"}`,
			[]string{"https://gw.example.com"}, []string{"https://api.typesafe.ai"}},
		{"auto", nil, `{}`, nil, []string{"https://openrouter.ai", "https://api.typesafe.ai"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeStore()
			fs.rows.Rows["decision_model"] = json.RawMessage(tc.row)
			env := append([]string{"TRUTHTABLE_JEV_API_KEY=" + noPatternSecret}, tc.env...)
			m := New(Options{Config: loadCfg(env...), Store: fs, Master: master(t, 10), Log: quietLog()})
			if err := m.Reload(ctx); err != nil {
				t.Fatal(err)
			}
			if c := m.Current().Credentials[credentials.NameCompatible]; c.Source != credentials.SourceEnv || !slices.Equal(c.AllowedOrigins, nonNil(tc.allow)) {
				t.Errorf("origins %v, want %v", c.AllowedOrigins, tc.allow)
			}
			key := m.KeyFunc(credentials.NameCompatible)
			for _, o := range tc.allow {
				if got, err := key(ctx, o); err != nil || got != noPatternSecret {
					t.Errorf("%s refused: %v", o, err)
				}
			}
			for _, o := range tc.deny {
				if _, err := key(ctx, o); !errors.Is(err, ErrHostNotAllowed) {
					t.Errorf("%s: %v, want ErrHostNotAllowed", o, err)
				}
			}
		})
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestStoredCredentialsOpenAndUndecryptableAreListed(t *testing.T) {
	mk := master(t, 2)
	fs := newFakeStore()
	fs.creds = []store.CredentialRecord{
		sealRow(t, mk, credentials.NameOpenRouter, "sk-or-v1-e2e-stored-0001"),
		sealRow(t, mk, credentials.NameCompatible, noPatternSecret, "https://gw.example.com"),
		sealRow(t, master(t, 3), credentials.NameTypeSafe, "sk-test-other-master-01"), // another master key
	}
	tampered := sealRow(t, mk, credentials.NameUpstreamDefault, "sk-test-upstream-0001", "https://llm.example.com")
	tampered.AllowedHosts = []string{"https://evil.example.com"} // widening the origins breaks the AAD
	fs.creds = append(fs.creds, tampered)
	red := config.NewRedactor()
	var logs bytes.Buffer
	m := New(Options{Config: loadCfg("TRUTHTABLE_OPENROUTER_BASE_URL=http://mock-upstream:8090"), Store: fs, Master: mk, Redactor: red,
		Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{ReplaceAttr: red.ReplaceAttr}))})
	if red.Redact(noPatternSecret) != noPatternSecret {
		t.Fatal("the secret is unknown before it is loaded")
	}
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := m.Current()
	if !slices.Equal(s.Undecryptable, []string{credentials.NameTypeSafe, credentials.NameUpstreamDefault}) {
		t.Errorf("undecryptable %v", s.Undecryptable)
	}
	if _, ok := s.Credentials[credentials.NameTypeSafe]; ok {
		t.Error("an undecryptable row is never used")
	}
	or := s.Credentials[credentials.NameOpenRouter]
	if or.Secret != "sk-or-v1-e2e-stored-0001" || or.Source != credentials.SourceDashboard ||
		!slices.Equal(or.AllowedOrigins, []string{"http://mock-upstream:8090", "https://openrouter.ai"}) {
		t.Errorf("openrouter %+v: vendor origins are recomputed at load with the env base", or.AllowedOrigins)
	}
	if c := s.Credentials[credentials.NameCompatible]; c.Secret != noPatternSecret || !slices.Equal(c.AllowedOrigins, []string{"https://gw.example.com"}) {
		t.Errorf("compatible origins %v", c.AllowedOrigins)
	}
	if len(s.Warnings) != 1 || s.Warnings[0].Code != WarnMasterKeyChanged ||
		!slices.Equal(s.Warnings[0].Details["names"].([]string), []string{"typesafe", "upstream-default"}) {
		t.Errorf("warnings %+v", s.Warnings)
	}
	// The redactor masks a loaded secret (D42), even one that matches no pattern.
	if got := red.Redact("key " + noPatternSecret); strings.Contains(got, noPatternSecret) {
		t.Errorf("the decrypted secret is not redacted: %q", got)
	}
	slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{ReplaceAttr: red.ReplaceAttr})).Info("echo", "v", noPatternSecret)
	if strings.Contains(logs.String(), noPatternSecret) || !strings.Contains(logs.String(), "typesafe,upstream-default") {
		t.Errorf("logs: %s", logs.String())
	}
}

func TestEnvCredentialShadowsTheRow(t *testing.T) {
	mk := master(t, 4)
	fs := newFakeStore()
	fs.creds = []store.CredentialRecord{sealRow(t, mk, credentials.NameOpenRouter, "sk-or-v1-e2e-dashboard-01")}
	m := New(Options{Config: loadCfg("OPENROUTER_API_KEY=sk-or-v1-e2e-env-key-0001"), Store: fs, Master: mk, Log: quietLog()})
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := m.Current().Credentials[credentials.NameOpenRouter]
	if c.Source != credentials.SourceEnv || c.Secret != "sk-or-v1-e2e-env-key-0001" || c.Fingerprint != credentials.NewFingerprinter(mk).Of(c.Secret) {
		t.Errorf("env wins: %+v", c.Source)
	}
}

func TestStoreUnavailableWithAnEphemeralMaster(t *testing.T) {
	fs := newFakeStore()
	fs.creds = []store.CredentialRecord{sealRow(t, master(t, 5), credentials.NameOpenRouter, "sk-or-v1-e2e-stored-0002")}
	m := New(Options{Config: loadCfg(), Store: fs, Master: authz.EphemeralMasterKey(), Log: quietLog()})
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := m.Current()
	if s.StoreState != credentials.StateUnavailable || len(s.Credentials) != 0 || !slices.Equal(s.Undecryptable, []string{"openrouter"}) {
		t.Errorf("state %s creds %d undecryptable %v", s.StoreState, len(s.Credentials), s.Undecryptable)
	}
	if len(s.Warnings) != 1 || s.Warnings[0].Code != WarnStoreUnavailable {
		t.Errorf("warnings %+v", s.Warnings)
	}
	if _, err := m.Cipher(); !errors.Is(err, credentials.ErrUnavailable) {
		t.Errorf("an ephemeral master never seals: %v", err)
	}
	if NewStatic(loadCfg()).Current().StoreState != credentials.StateNoDatabase {
		t.Error("no database")
	}
	if c, err := New(Options{Store: fs, Master: master(t, 5)}).Cipher(); err != nil || c.KeyID() == "" {
		t.Errorf("a ready store seals: %v", err)
	}
}

func TestOriginMismatchIsRefusedForEveryKind(t *testing.T) {
	mk := master(t, 6)
	fs := newFakeStore()
	fs.creds = []store.CredentialRecord{
		sealRow(t, mk, credentials.NameOpenRouter, "sk-or-v1-e2e-0000000001"),
		sealRow(t, mk, credentials.NameTypeSafe, "sk-test-typesafe-000001"),
		sealRow(t, mk, credentials.NameAnthropic, "sk-ant-test-0000000001"),
		sealRow(t, mk, credentials.NameOpenAI, "sk-test-openai-00000001"),
		sealRow(t, mk, credentials.NameCompatible, "sk-test-compat-00000001", "https://gw.example.com"),
		sealRow(t, mk, credentials.NameUpstreamDefault, "sk-test-upstream-000001", "https://llm.example.com"),
		sealRow(t, mk, credentials.NameUpstreamAnthropic, "sk-test-upstream-000002", "https://anthropic-proxy.example.com"),
		sealRow(t, mk, credentials.NameUpstreamFallback, "sk-test-upstream-000003", "http://localhost:11434"),
	}
	m := New(Options{Config: loadCfg(), Store: fs, Master: mk, Log: quietLog()})
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]string{
		"openrouter": "https://openrouter.ai", "typesafe": "https://api.typesafe.ai", "anthropic": "https://api.anthropic.com",
		"openai": "https://api.openai.com", "compatible": "https://gw.example.com", "upstream-default": "https://llm.example.com",
		"upstream-anthropic": "https://anthropic-proxy.example.com", "upstream-fallback": "http://localhost:11434",
	}
	ctx := context.Background()
	for _, name := range credentials.Names() {
		key := m.KeyFunc(name)
		if got, err := key(ctx, allowed[name]); err != nil || got == "" {
			t.Errorf("%s at its own origin: %v", name, err)
		}
		for _, other := range []string{"https://evil.example.com", "http://openrouter.ai", "https://openrouter.ai:8443", "https://api.openai.com.evil.example"} {
			if other == allowed[name] {
				continue
			}
			_, err := key(ctx, other)
			if !errors.Is(err, ErrHostNotAllowed) {
				t.Errorf("%s at %s: %v", name, other, err)
			}
			if err != nil && strings.Contains(err.Error(), "sk-") {
				t.Errorf("%s: the error carries the secret", name)
			}
		}
	}
	m.Wait()
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.touched["openrouter"] != 1 || fs.touched["upstream-fallback"] != 1 {
		t.Errorf("each dashboard credential is touched once per minute: %v", fs.touched)
	}
}

func TestEnvCredentialOriginsAndKeyFunc(t *testing.T) {
	m := NewStatic(loadCfg("TRUTHTABLE_UPSTREAM_API_KEY=sk-test-upstream-env-01", "TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY=sk-test-fallback-env-01",
		"TRUTHTABLE_FALLBACK_UPSTREAM_URL=http://mock-upstream:8090/v1", "TRUTHTABLE_JEV_BACKEND=typesafe", "TRUTHTABLE_JEV_API_KEY=sk-test-compat-env-001"))
	s := m.Current()
	for name, want := range map[string][]string{
		credentials.NameUpstreamDefault:  {"https://api.openai.com"}, // the default upstream
		credentials.NameUpstreamFallback: {"http://mock-upstream:8090"},
		credentials.NameCompatible:       {"https://api.typesafe.ai"},
	} {
		if c := s.Credentials[name]; !slices.Equal(c.AllowedOrigins, want) {
			t.Errorf("%s origins %v, want %v", name, c.AllowedOrigins, want)
		}
	}
	if _, err := m.KeyFunc(credentials.NameUpstreamDefault)(context.Background(), "https://api.openai.com"); err != nil {
		t.Errorf("env upstream key: %v", err)
	}
}

func TestReloadOnKickAndPoll(t *testing.T) {
	fs := newFakeStore()
	m := New(Options{Config: loadCfg(), Store: fs, Master: master(t, 7), Log: quietLog()})
	got := make(chan *Snapshot, 8)
	m.Subscribe(func(s *Snapshot) { got <- s })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx, 0) }()
	defer func() { cancel(); <-done }()

	// A hub dispatch (settings:… or credentials:…) kicks a reload.
	fs.set(func(f *fakeStore) {
		f.rows.Version = 2
		f.rows.Rows["judge"] = json.RawMessage(`{"provider":"mock"}`)
	})
	m.Kick()
	s := waitSnapshot(t, got)
	if s.Version != 2 || s.Settings.Judge.Provider != "mock" {
		t.Errorf("after the kick: v%d %+v", s.Version, s.Settings.Judge)
	}
	// A reload that reads the same state publishes nothing.
	reads := fs.readCount()
	m.Kick()
	waitReads(t, fs, reads+1)
	select {
	case s := <-got:
		t.Errorf("an unchanged reload published v%d", s.Version)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	<-done

	// The poll reloads on its own.
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { defer close(done2); m.Run(ctx2, 20*time.Millisecond) }()
	defer func() { cancel2(); <-done2 }()
	fs.set(func(f *fakeStore) {
		f.rows.Version = 3
		f.rows.Rows["budgets"] = json.RawMessage(`{"decision_usd_day":4}`)
	})
	s = waitSnapshot(t, got)
	if s.Version != 3 || s.Settings.Budgets.DecisionUSDDay != 4 {
		t.Errorf("after the poll: v%d %+v", s.Version, s.Settings.Budgets)
	}
}

func TestReloadFailureKeepsTheLastSettings(t *testing.T) {
	mk := master(t, 8)
	fs := newFakeStore()
	fs.creds = []store.CredentialRecord{sealRow(t, mk, credentials.NameOpenRouter, "sk-or-v1-e2e-kept-000001")}
	fs.rows.Rows["bindings"] = json.RawMessage(`{"judge":"openrouter"}`)
	m := New(Options{Config: loadCfg(), Store: fs, Master: mk, Log: quietLog()})
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	fs.set(func(f *fakeStore) { f.err = errors.New("connection refused") })
	if err := m.Reload(context.Background()); err == nil {
		t.Fatal("the failed read is returned")
	}
	s := m.Current()
	if *s.Settings.Bindings.Judge != "openrouter" || s.Credentials["openrouter"].Secret == "" || s.Version != 1 {
		t.Errorf("the last good state stays: %+v", s)
	}
	if len(s.Warnings) != 1 || s.Warnings[0].Code != WarnSettingsUnreadable || strings.Contains(s.Warnings[0].Message, "refused") {
		t.Errorf("warnings %+v", s.Warnings)
	}
	fs.set(func(f *fakeStore) { f.err = nil })
	if err := m.Reload(context.Background()); err != nil || len(m.Current().Warnings) != 0 {
		t.Errorf("recovered: %v %+v", err, m.Current().Warnings)
	}
	// An invalid row is ignored rather than breaking the snapshot.
	fs.set(func(f *fakeStore) { f.rows.Rows["budgets"] = json.RawMessage(`{"decision_usd_day":"lots"}`) })
	if err := m.Reload(context.Background()); err != nil || m.Current().Settings.Budgets.DecisionUSDDay != 2 {
		t.Errorf("invalid row: %v %+v", err, m.Current().Settings.Budgets)
	}
}

func TestSubscribersSeeSnapshotsInOrder(t *testing.T) {
	fs := newFakeStore()
	m := New(Options{Config: loadCfg(), Store: fs, Log: quietLog()})
	var mu sync.Mutex
	var a, b []int64
	m.Subscribe(func(s *Snapshot) { mu.Lock(); a = append(a, s.Version); mu.Unlock() })
	cancelB := m.Subscribe(func(s *Snapshot) { mu.Lock(); b = append(b, s.Version); mu.Unlock() })
	for v := int64(2); v <= 4; v++ {
		fs.set(func(f *fakeStore) { f.rows.Version = v })
		if err := m.Reload(context.Background()); err != nil {
			t.Fatal(err)
		}
		if v == 3 {
			cancelB()
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(a, []int64{2, 3, 4}) || !slices.Equal(b, []int64{2, 3}) {
		t.Errorf("a %v b %v", a, b)
	}
}

func waitSnapshot(t *testing.T, ch <-chan *Snapshot) *Snapshot {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no snapshot published")
	}
	return nil
}

func waitReads(t *testing.T, fs *fakeStore, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for fs.readCount() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d reads, want %d", fs.readCount(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
