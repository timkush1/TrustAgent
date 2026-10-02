package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/costs"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/runtimecfg"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
	"github.com/truthtable/backend-go/internal/upstream"
)

const (
	schemaSettings   = "api.v1.schema.json#/$defs/SettingsView"
	settingsAdminKey = "settings-admin-env-key"
)

// settingsEnv is a test server on a Postgres schema of its own with a durable master key, so the
// settings store and the credential store are ready.
type settingsEnv struct {
	*testEnv
	pg     *store.PostgresStore
	schema *storetest.Schema
	master *authz.MasterKey
}

func newSettingsEnv(t *testing.T, o envOpts) *settingsEnv {
	t.Helper()
	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	o.store = pg
	if o.adminKey == "" {
		o.adminKey = settingsAdminKey
	}
	if o.master == nil {
		o.master = durableMaster(t, 7)
	}
	return &settingsEnv{testEnv: newTestEnv(t, o), pg: pg, schema: schema, master: o.master}
}

// request sends a request with the X-TrustAgent-Key key ("" for none), a JSON body (nil for none)
// and header name/value pairs.
func (e *testEnv) request(method, path, key string, body []byte, headers ...string) (*http.Response, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.http.URL+path, bytes.NewReader(body))
	if key != "" {
		req.Header.Set("X-TrustAgent-Key", key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// settings reads GET /api/settings with key and validates it against the contract.
func (e *testEnv) settings(key string) (settingsView, string) {
	e.t.Helper()
	resp, body := e.request(http.MethodGet, "/api/settings", key, nil)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("GET /api/settings: %d %s", resp.StatusCode, body)
	}
	contractstest.ValidateJSON(e.t, schemaSettings, body)
	var v settingsView
	if err := json.Unmarshal(body, &v); err != nil {
		e.t.Fatal(err)
	}
	return v, resp.Header.Get("ETag")
}

// patchSettings sends PATCH /api/settings as the env admin with If-Match etag ("" for none).
func (e *testEnv) patchSettings(etag, body, query string, headers ...string) (*http.Response, []byte) {
	e.t.Helper()
	return e.patchSettingsAs(settingsAdminKey, etag, body, query, headers...)
}

// patchSettingsAs is patchSettings with the key key.
func (e *testEnv) patchSettingsAs(key, etag, body, query string, headers ...string) (*http.Response, []byte) {
	e.t.Helper()
	if etag != "" {
		headers = append(headers, "If-Match", etag)
	}
	return e.request(http.MethodPatch, "/api/settings"+query, key, []byte(body), headers...)
}

// patched decodes a 200 PATCH answer, validated against the contract.
func (e *testEnv) patched(resp *http.Response, body []byte) settingsView {
	t := e.t
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH: %d %s", resp.StatusCode, body)
	}
	contractstest.ValidateJSON(t, schemaSettings, body)
	var v settingsView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("ETag"); got != v.ETag {
		t.Errorf("ETag header %s, body %s", got, v.ETag)
	}
	return v
}

// apiErr decodes an error answer, validated against the contract.
func apiErr(t *testing.T, body []byte) apiError {
	t.Helper()
	contractstest.ValidateJSON(t, schemaError, body)
	var e apiErrorBody
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatal(err)
	}
	return e.Error
}

// keyView finds a key of the view.
func (v settingsView) key(t *testing.T, name string) settingKeyView {
	t.Helper()
	for _, g := range v.Groups {
		for _, k := range g.Keys {
			if k.Key == name {
				return k
			}
		}
	}
	t.Fatalf("no key %s in the settings view", name)
	return settingKeyView{}
}

// fieldCodes lists path=code of an error's field errors.
func fieldCodes(e apiError) []string {
	var out []string
	for _, f := range e.FieldErrors {
		out = append(out, f.Path+"="+f.Code)
	}
	return out
}

// auditRows lists the admin_audit_log actions with their diffs, oldest first.
func (e *settingsEnv) auditRows(t *testing.T, action string) []map[string]any {
	t.Helper()
	rows, err := e.schema.Pool(t).Query(context.Background(), `SELECT diff FROM admin_audit_log WHERE action = $1 ORDER BY id`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var d map[string]any
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

// TestSettingsETagRoundTrip: GET carries the version as ETag "s<v>"; a PATCH needs If-Match (428),
// refuses a stale one (412), stores only the leaf it changes with the version bump and its audit
// row, and announces settings_updated{version, keys} on the WS stream (D31).
func TestSettingsETagRoundTrip(t *testing.T) {
	env := newSettingsEnv(t, envOpts{mutate: func(c *config.Config) { c.EnvFile = "/app/.env" }})
	v, etag := env.settings(settingsAdminKey)
	if etag != v.ETag || etag != settingsETag(v.Version) {
		t.Fatalf("ETag %s, view %s v%d", etag, v.ETag, v.Version)
	}
	if v.DotenvPath == nil || *v.DotenvPath != "/app/.env" {
		t.Errorf("an admin sees the dotenv path: %v", v.DotenvPath)
	}
	if k := v.key(t, "TRUTHTABLE_SPEND_CAP_LLM_USD_DAY"); !k.Editable || k.Source != "default" {
		t.Errorf("the LLM cap is editable and default: %+v", k)
	}

	resp, body := env.patchSettings("", `{"budgets":{"llm_usd_day":3}}`, "")
	if resp.StatusCode != http.StatusPreconditionRequired || apiErr(t, body).Code != codePreconditionRequired {
		t.Errorf("no If-Match: %d %s", resp.StatusCode, body)
	}
	resp, body = env.patchSettings(settingsETag(v.Version+5), `{"budgets":{"llm_usd_day":3}}`, "")
	if resp.StatusCode != http.StatusPreconditionFailed || apiErr(t, body).Code != codeStaleVersion {
		t.Errorf("stale If-Match: %d %s", resp.StatusCode, body)
	}

	after := env.patched(env.patchSettings(etag, `{"budgets":{"llm_usd_day":3}}`, ""))
	if after.Version != v.Version+1 || after.Runtime.Budgets.LLMUSDDay != 3 {
		t.Fatalf("after the write: v%d %+v", after.Version, after.Runtime.Budgets)
	}
	if k := after.key(t, "TRUTHTABLE_SPEND_CAP_LLM_USD_DAY"); k.Source != "dashboard" || k.Value != "3.00" {
		t.Errorf("the written key: %+v", k)
	}
	if k := after.key(t, "TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY"); k.Source != "default" {
		t.Errorf("a sibling the write did not set stays default: %+v", k)
	}
	if again, _ := env.settings(settingsAdminKey); again.ETag != after.ETag {
		t.Errorf("GET after the write: %s, PATCH answered %s", again.ETag, after.ETag)
	}
	resp, body = env.patchSettings(etag, `{"budgets":{"llm_usd_day":4}}`, "")
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("the old ETag is stale now: %d %s", resp.StatusCode, body)
	}
	// Sending the value in effect changes nothing: no version bump.
	if same := env.patched(env.patchSettings(after.ETag, `{"budgets":{"llm_usd_day":3}}`, "")); same.Version != after.Version {
		t.Errorf("a no-op write bumped the version to %d", same.Version)
	}

	audits := env.auditRows(t, "settings.update")
	if len(audits) != 1 {
		t.Fatalf("settings.update rows: %v", audits)
	}
	fields, _ := audits[0]["fields"].(map[string]any)
	if ch, _ := fields["budgets.llm_usd_day"].(map[string]any); ch == nil || ch["to"] != float64(3) {
		t.Errorf("audit diff %v", audits[0])
	}
	var ev struct {
		Type string `json:"type"`
		Data struct {
			Version int64    `json:"version"`
			Keys    []string `json:"keys"`
		} `json:"data"`
	}
	found := false
	for _, f := range env.wsFrames(t, 200) {
		if json.Unmarshal(f, &ev) == nil && ev.Type == "settings_updated" {
			found = true
			contractstest.ValidateJSON(t, "api.v1.schema.json#/$defs/WsEvent", f)
			if ev.Data.Version != after.Version || !slices.Equal(ev.Data.Keys, []string{"budgets.llm_usd_day"}) {
				t.Errorf("settings_updated %+v", ev.Data)
			}
		}
	}
	if !found {
		t.Error("no settings_updated frame")
	}
}

// TestSettingsMaskingByCaller (D79): a reader sees secrets as "set" or "" (never a mask), no
// dotenv path and nothing editable; an admin sees config.MaskSecret; URLs never show userinfo or
// a query.
func TestSettingsMaskingByCaller(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "sk-test-typesafe-0123456789")
	env := newSettingsEnv(t, envOpts{mutate: func(c *config.Config) {
		c.EnvFile = "/app/.env"
		c.UpstreamURL = "https://user:pw@gw.example.com/v1?api-key=q"
	}})
	reader, _ := env.settings(testKey)
	if reader.DotenvPath != nil {
		t.Error("a reader never sees the dotenv path")
	}
	for _, g := range reader.Groups {
		for _, k := range g.Keys {
			if k.Secret && k.Value != "" && k.Value != "set" {
				t.Errorf("reader sees %s = %q", k.Key, k.Value)
			}
			if k.Editable {
				t.Errorf("reader: %s is editable", k.Key)
			}
		}
	}
	if k := reader.key(t, "TYPESAFE_API_KEY"); k.Value != "set" || k.Source != "env" || k.Env == nil || *k.Env != "TYPESAFE_API_KEY" {
		t.Errorf("reader TYPESAFE_API_KEY %+v", k)
	}
	admin, _ := env.settings(settingsAdminKey)
	if k := admin.key(t, "TYPESAFE_API_KEY"); k.Value != config.MaskSecret("sk-test-typesafe-0123456789") || strings.Contains(k.Value, "0123456") {
		t.Errorf("admin TYPESAFE_API_KEY %+v", k)
	}
	for _, v := range []settingsView{reader, admin} {
		u := *v.Runtime.Upstream.Default.URL
		if strings.Contains(u, "pw") || strings.Contains(u, "api-key") {
			t.Errorf("runtime upstream URL %q", u)
		}
	}
}

// TestSettingsEnvManaged (D33): a path a non-empty env var pins answers 409 env_managed with the
// paths and the env vars, unless the write sends the value already in effect.
func TestSettingsEnvManaged(t *testing.T) {
	t.Setenv("TRUTHTABLE_JEV_MODEL", "env-model")
	env := newSettingsEnv(t, envOpts{})
	v, etag := env.settings(settingsAdminKey)
	if !slices.Contains(v.EnvManaged, "decision_model.model") {
		t.Fatalf("env_managed %v", v.EnvManaged)
	}
	if k := v.key(t, "TRUTHTABLE_JEV_MODEL"); k.Editable || k.Source != "env" || k.Value != "env-model" {
		t.Errorf("TRUTHTABLE_JEV_MODEL %+v", k)
	}
	resp, body := env.patchSettings(etag, `{"decision_model":{"model":"other"},"budgets":{"llm_usd_day":2}}`, "")
	e := apiErr(t, body)
	if resp.StatusCode != http.StatusConflict || e.Code != codeEnvManaged {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if f, _ := e.Details["fields"].([]any); len(f) != 1 || f[0] != "decision_model.model" {
		t.Errorf("details.fields %v", e.Details)
	}
	if k, _ := e.Details["keys"].([]any); len(k) != 1 || k[0] != "TRUTHTABLE_JEV_MODEL" {
		t.Errorf("details.keys %v", e.Details)
	}
	after := env.patched(env.patchSettings(etag, `{"decision_model":{"model":"env-model"},"budgets":{"llm_usd_day":2}}`, ""))
	if after.Runtime.Budgets.LLMUSDDay != 2 || *after.Runtime.DecisionModel.Model != "env-model" {
		t.Errorf("the env value in the patch is not a change: %+v", after.Runtime)
	}
	_, rows := env.srv.rt.manager.Stored()
	if strings.Contains(string(rows["decision_model"]), "env-model") {
		t.Errorf("an env value was written into a row: %s", rows["decision_model"])
	}
}

// TestSettingsValidation: unknown paths, wrong types, enums, budgets, URLs (D78), credentials of
// the wrong kind or not saved, D41b disagreement and D37 reenter_secret answer 422 with field
// errors; an http upstream with no key of ours is accepted.
func TestSettingsValidation(t *testing.T) {
	env := newSettingsEnv(t, envOpts{})
	putSealed(t, env.pg, env.master, "compatible", "sk-test-compatible-0123456789",
		map[string]json.RawMessage{"bindings": json.RawMessage(`{"decision_model":"compatible"}`),
			"decision_model": json.RawMessage(`{"backend":"compatible","base_url":"https://gw.example.com/v1"}`)},
		"https://gw.example.com")
	if err := env.srv.rt.manager.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, etag := env.settings(settingsAdminKey)
	cases := []struct {
		name, body string
		want       []string
	}{
		{"unknown", `{"decision_model":{"backnd":"mock"},"colour":1}`, []string{"colour=unknown_setting", "decision_model.backnd=unknown_setting"}},
		{"types", `{"budgets":{"llm_usd_day":"5"},"judge":"x"}`, []string{"budgets.llm_usd_day=invalid_type", "judge=invalid_type"}},
		{"enums", `{"judge":{"provider":"gpt"},"upstream":{"fallback":{"format":"xml"}}}`, []string{"judge.provider=invalid_value", "upstream.fallback.format=invalid_value"}},
		{"budget", `{"budgets":{"decision_usd_day":-1}}`, []string{"budgets.decision_usd_day=invalid_value"}},
		{"userinfo", `{"upstream":{"anthropic":{"url":"https://u:p@anthropic.example.com"}}}`, []string{"upstream.anthropic.url=" + "url_credentials"}},
		{"metadata", `{"upstream":{"default":{"url":"http://169.254.169.254/latest"}}}`, []string{"upstream.default.url=forbidden_target"}},
		{"insecure keyed", `{"decision_model":{"base_url":"http://gw.example.com/v1"}}`, []string{"decision_model.base_url=insecure_url"}},
		{"kind", `{"bindings":{"decision_model":"anthropic"}}`, []string{"bindings.decision_model=credential_kind"}},
		{"unknown name", `{"bindings":{"judge":"nobody"}}`, []string{"bindings.judge=unknown_credential"}},
		{"missing", `{"bindings":{"judge":"openrouter"}}`, []string{"bindings.judge=credential_missing"}},
		{"D41b", `{"bindings":{"upstream_default":"upstream-default"},"upstream":{"default":{"credential":null}}}`,
			[]string{"bindings.upstream_default=binding_mismatch", "upstream.default.credential=binding_mismatch"}},
		{"D37", `{"decision_model":{"base_url":"https://other.example.com/v1"}}`, []string{"decision_model.base_url=reenter_secret"}},
		{"D53", `{"pricing_overrides":[{"match":"jev-*","provider":"openrouter","input_per_mtok":0,"output_per_mtok":0}]}`,
			[]string{"pricing_overrides[0].input_per_mtok=" + costs.CodePriceZeroNotLocal}},
	}
	for _, tc := range cases {
		resp, body := env.patchSettings(etag, tc.body, "")
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", tc.name, resp.StatusCode, body)
			continue
		}
		e := apiErr(t, body)
		got := fieldCodes(e)
		slices.Sort(got)
		want := slices.Clone(tc.want)
		slices.Sort(want)
		if e.Code != codeValidationFailed || !slices.Equal(got, want) {
			t.Errorf("%s: %s %v, want %v", tc.name, e.Code, got, want)
		}
		if tc.name == "D37" {
			if names, _ := e.FieldErrors[0].Details["names"].([]any); len(names) != 1 || names[0] != "compatible" {
				t.Errorf("reenter_secret details %v", e.FieldErrors[0].Details)
			}
		}
	}
	if v, _ := env.settings(settingsAdminKey); v.ETag != etag {
		t.Errorf("a refused write changed the version: %s", v.ETag)
	}
	// Plain http to the client's own upstream, with no key of ours, is the operator's choice.
	v := env.patched(env.patchSettings(etag, `{"upstream":{"anthropic":{"url":"http://llm.internal.example.com:8000"}}}`, ""))
	if got := *v.Runtime.Upstream.Anthropic.URL; got != "http://llm.internal.example.com:8000" {
		t.Errorf("anthropic upstream %q", got)
	}
}

// TestSettingsBindingsLeaveSimulatedMode (D76, D41b): binding a credential through PATCH turns a
// stored mock backend and mock judge to auto, unless the same patch names them; the default
// upstream's binding is one field with upstream.default.credential and needs an ingest key (D41).
func TestSettingsBindingsLeaveSimulatedMode(t *testing.T) {
	// No env key can ingest: the admin is a dashboard key with the admin scope only.
	env := newSettingsEnv(t, envOpts{mutate: func(c *config.Config) { c.APIKeys, c.Auth.AdminKeys = nil, nil }})
	ctx := context.Background()
	created, err := env.srv.gateway.auth.CreateKey(ctx, "ops", []authz.Scope{authz.ScopeAdmin}, "test")
	if err != nil {
		t.Fatal(err)
	}
	admin := created.Secret
	putSealed(t, env.pg, env.master, "openrouter", "sk-or-v1-e2e-0123456789abcdef", nil)
	putSealed(t, env.pg, env.master, "upstream-default", "sk-test-upstream-0123456789", nil, "http://127.0.0.1:1")
	if err := env.srv.rt.manager.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	_, etag := env.settings(admin)
	v := env.patched(env.patchSettingsAs(admin, etag, `{"decision_model":{"backend":"mock"},"judge":{"provider":"mock"}}`, ""))
	v = env.patched(env.patchSettingsAs(admin, v.ETag, `{"bindings":{"decision_model":"openrouter","judge":"openrouter"}}`, ""))
	if v.Runtime.DecisionModel.Backend != "auto" || v.Runtime.Judge.Provider != "auto" ||
		*v.Runtime.Bindings.DecisionModel != "openrouter" || *v.Runtime.Bindings.Judge != "openrouter" {
		t.Errorf("after binding: %+v %+v %+v", v.Runtime.DecisionModel, v.Runtime.Judge, v.Runtime.Bindings)
	}
	v = env.patched(env.patchSettingsAs(admin, v.ETag, `{"bindings":{"judge":null}}`, ""))
	v = env.patched(env.patchSettingsAs(admin, v.ETag, `{"judge":{"provider":"mock"},"bindings":{"judge":"openrouter"}}`, ""))
	if v.Runtime.Judge.Provider != "mock" {
		t.Errorf("an explicit provider in the same patch wins: %+v", v.Runtime.Judge)
	}

	// The default upstream binding needs a key callers authenticate with (D41).
	resp, body := env.patchSettingsAs(admin, v.ETag, `{"upstream":{"default":{"credential":"upstream-default"}}}`, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("no ingest key: %d %s", resp.StatusCode, body)
	}
	if e := apiErr(t, body); e.Code != codeIngestKeyRequired || e.Details["field"] != "bindings.upstream_default" {
		t.Errorf("no ingest key: %+v", e)
	}
	if _, err := env.srv.gateway.auth.CreateKey(ctx, "app", []authz.Scope{authz.ScopeIngest}, "test"); err != nil {
		t.Fatal(err)
	}
	v = env.patched(env.patchSettingsAs(admin, v.ETag, `{"upstream":{"default":{"credential":"upstream-default"}}}`, ""))
	if b, u := v.Runtime.Bindings.UpstreamDefault, v.Runtime.Upstream.Default.Credential; b == nil || u == nil || *b != "upstream-default" || *u != "upstream-default" {
		t.Errorf("D41b: one field, both set: %v %v", b, u)
	}
}

// TestSettingsConfirmation (D77): turning the decision model off while a fail-closed enforcing
// route exists answers 409 confirmation_required with the routes, until ?confirm=true.
func TestSettingsConfirmation(t *testing.T) {
	env := newSettingsEnv(t, envOpts{})
	routes := env.srv.enforcingRoutes()
	if !slices.Contains(routes, "payments-faq") {
		t.Fatalf("the demo policy has the fail-closed route payments-faq: %v", routes)
	}
	_, etag := env.settings(settingsAdminKey)
	resp, body := env.patchSettings(etag, `{"decision_model":{"backend":"off"}}`, "")
	e := apiErr(t, body)
	if resp.StatusCode != http.StatusConflict || e.Code != codeConfirmationRequired {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if got, _ := e.Details["routes"].([]any); len(got) != len(routes) || got[0] != routes[0] {
		t.Errorf("details.routes %v, want %v", e.Details["routes"], routes)
	}
	v := env.patched(env.patchSettings(etag, `{"decision_model":{"backend":"off"}}`, "?confirm=true"))
	if v.Runtime.DecisionModel.Backend != "off" {
		t.Errorf("confirmed: %+v", v.Runtime.DecisionModel)
	}
}

// TestSettingsConfirmationRules: which changes need a confirmation (D77), with the spend today
// from the ledger's hook.
func TestSettingsConfirmationRules(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	s := &server{policies: env.srv.policies, status: &statusReporter{cfg: env.srv.cfg,
		spend: func() *spendView { return &spendView{DecisionSpentTodayUSD: 1.5} }}}
	name := "openrouter"
	base := runtimecfg.RuntimeSettings{DecisionModel: runtimecfg.DecisionModelSettings{Backend: "auto"},
		Bindings: runtimecfg.Bindings{DecisionModel: &name}, Budgets: runtimecfg.Budgets{DecisionUSDDay: 2}}
	for _, tc := range []struct {
		name string
		edit func(*runtimecfg.RuntimeSettings)
		want bool
	}{
		{"backend off", func(r *runtimecfg.RuntimeSettings) { r.DecisionModel.Backend = "off" }, true},
		{"unbind", func(r *runtimecfg.RuntimeSettings) { r.Bindings.DecisionModel = nil }, true},
		{"cap below spend", func(r *runtimecfg.RuntimeSettings) { r.Budgets.DecisionUSDDay = 1 }, true},
		{"cap above spend", func(r *runtimecfg.RuntimeSettings) { r.Budgets.DecisionUSDDay = 1.75 }, false},
		{"no cap", func(r *runtimecfg.RuntimeSettings) { r.Budgets.DecisionUSDDay = 0 }, false},
		{"backend mock", func(r *runtimecfg.RuntimeSettings) { r.DecisionModel.Backend = "mock" }, false},
	} {
		after := base
		tc.edit(&after)
		msg, routes := s.settingsConfirmation(base, after)
		if (msg != "") != tc.want || (tc.want && !slices.Contains(routes, "payments-faq")) {
			t.Errorf("%s: %q %v", tc.name, msg, routes)
		}
	}
}

// TestSettingsWithoutDatabase (D31): version 0, nothing editable, and writes answer 503
// persistence_disabled.
func TestSettingsWithoutDatabase(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: settingsAdminKey})
	v, etag := env.settings(settingsAdminKey)
	if v.Version != 0 || etag != `"s0"` {
		t.Errorf("v%d %s", v.Version, etag)
	}
	for _, g := range v.Groups {
		for _, k := range g.Keys {
			if k.Editable {
				t.Errorf("%s is editable without a database", k.Key)
			}
		}
	}
	resp, body := env.patchSettings(etag, `{"budgets":{"llm_usd_day":1}}`, "")
	if resp.StatusCode != http.StatusServiceUnavailable || apiErr(t, body).Code != codePersistenceDisabled {
		t.Errorf("%d %s", resp.StatusCode, body)
	}
}

// TestSettingsCredentialStoreUnavailable (D32): with an ephemeral master key, binding a stored
// credential answers 503 credentials_unavailable.
func TestSettingsCredentialStoreUnavailable(t *testing.T) {
	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	env := newTestEnv(t, envOpts{store: pg, adminKey: settingsAdminKey}) // no master: ephemeral
	v, etag := env.settings(settingsAdminKey)
	if !slices.ContainsFunc(v.Warnings, func(w settingsWarning) bool { return w.Code == runtimecfg.WarnStoreUnavailable }) {
		t.Errorf("warnings %+v", v.Warnings)
	}
	resp, body := env.patchSettings(etag, `{"bindings":{"judge":"openrouter"}}`, "")
	if resp.StatusCode != http.StatusServiceUnavailable || apiErr(t, body).Code != codeCredentialsUnavailable {
		t.Errorf("%d %s", resp.StatusCode, body)
	}
	// Settings that need no stored credential still save.
	env.patched(env.patchSettings(etag, `{"budgets":{"llm_usd_day":1}}`, ""))
}

// TestSettingsReadonlyAndCSRF: read-only mode refuses the write (403 readonly); a cookie session
// needs the CSRF header and an allowed Origin.
func TestSettingsReadonlyAndCSRF(t *testing.T) {
	ro := newSettingsEnv(t, envOpts{readonly: true})
	_, etag := ro.settings(settingsAdminKey)
	resp, body := ro.patchSettings(etag, `{"budgets":{"llm_usd_day":1}}`, "")
	if resp.StatusCode != http.StatusForbidden || apiErr(t, body).Code != "readonly" {
		t.Errorf("readonly: %d %s", resp.StatusCode, body)
	}

	env := newSettingsEnv(t, envOpts{mutate: func(c *config.Config) { c.PublicURL = dashOrigin }})
	resp, body = env.request(http.MethodPost, "/api/session", "", []byte(`{"key":"`+settingsAdminKey+`"}`), csrf...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sign in: %d %s", resp.StatusCode, body)
	}
	cookie := sessionCookie(t, resp).String()
	_, etag = env.settings(settingsAdminKey)
	resp, body = env.request(http.MethodPatch, "/api/settings", "", []byte(`{"budgets":{"llm_usd_day":1}}`), "Cookie", cookie, "If-Match", etag)
	if resp.StatusCode != http.StatusForbidden || apiErr(t, body).Code != "csrf_failed" {
		t.Errorf("cookie without CSRF: %d %s", resp.StatusCode, body)
	}
	resp, body = env.request(http.MethodPatch, "/api/settings", "", []byte(`{"budgets":{"llm_usd_day":1}}`),
		append([]string{"Cookie", cookie, "If-Match", etag}, csrf...)...)
	env.patched(resp, body)
	if resp, _ := env.request(http.MethodPatch, "/api/settings", testKey, []byte(`{}`), "If-Match", etag); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a reader may not write: %d", resp.StatusCode)
	}
}

// TestSettingsSurfaceOtherChecks: the upstream risks (D41/D23, GCONN) refuse as field errors and
// warn as warnings; price overrides (D53, GCOST) likewise.
func TestSettingsSurfaceOtherChecks(t *testing.T) {
	prevRisks, prevOverrides := upstreamRisks, validateOverrides
	t.Cleanup(func() { upstreamRisks, validateOverrides = prevRisks, prevOverrides })
	upstreamRisks = func(s runtimecfg.RuntimeSettings, _ runtimecfg.CredentialSet) ([]upstream.FieldError, []upstream.Warning) {
		w := []upstream.Warning{{Code: "fallback_same_host", Message: "the fallback is the default upstream"}}
		if u := s.Upstream.Fallback.URL; u != nil && strings.Contains(*u, "other-host") {
			return []upstream.FieldError{{Path: "upstream.fallback.url", Code: "fallback_key_required", Message: "needs its own key",
				Details: map[string]any{"host": "other-host.example.com"}}}, w
		}
		return nil, w
	}
	validateOverrides = func(o []decision.PriceEntry) []costs.FieldError {
		var out []costs.FieldError
		for i, p := range o {
			if p.InputPerMTok == 0 {
				out = append(out, costs.FieldError{Path: "pricing_overrides[" + string(rune('0'+i)) + "].input_per_mtok", Code: "zero_price", Message: "not $0"})
			} else if p.InputPerMTok > 1000 {
				out = append(out, costs.FieldError{Path: "pricing_overrides", Code: "price_far_above", Message: "far above", Warning: true})
			}
		}
		return out
	}
	env := newSettingsEnv(t, envOpts{})
	v, etag := env.settings(settingsAdminKey)
	if !slices.ContainsFunc(v.Warnings, func(w settingsWarning) bool { return w.Code == "fallback_same_host" }) {
		t.Errorf("an upstream warning shows on GET: %+v", v.Warnings)
	}
	resp, body := env.patchSettings(etag, `{"upstream":{"fallback":{"url":"https://other-host.example.com/v1"}}}`, "")
	e := apiErr(t, body)
	if resp.StatusCode != http.StatusUnprocessableEntity || !slices.Equal(fieldCodes(e), []string{"upstream.fallback.url=fallback_key_required"}) ||
		e.FieldErrors[0].Details["host"] != "other-host.example.com" {
		t.Errorf("D23: %d %s", resp.StatusCode, body)
	}
	resp, body = env.patchSettings(etag, `{"pricing_overrides":[{"match":"x","provider":"openrouter","input_per_mtok":0,"output_per_mtok":1}]}`, "")
	if e := apiErr(t, body); resp.StatusCode != http.StatusUnprocessableEntity || !slices.Equal(fieldCodes(e), []string{"pricing_overrides[0].input_per_mtok=zero_price"}) {
		t.Errorf("zero price: %d %s", resp.StatusCode, body)
	}
	v = env.patched(env.patchSettings(etag, `{"pricing_overrides":[{"match":"x","provider":"openrouter","input_per_mtok":5000,"output_per_mtok":1}]}`, ""))
	if !slices.ContainsFunc(v.Warnings, func(w settingsWarning) bool { return w.Code == "price_far_above" }) || len(v.Runtime.PricingOverrides) != 1 {
		t.Errorf("a price warning saves and warns: %+v", v.Warnings)
	}
}

// TestSettingsEngineEnvManaged (D33, D80): the keys the engine reports set in its env
// (EngineInfo.env_managed) show with source env, a secret as "set" (the proxy never sees the
// engine's values), and the judge paths they pin are read-only.
func TestSettingsEngineEnvManaged(t *testing.T) {
	env := newSettingsEnv(t, envOpts{})
	env.srv.rt.manager.SetEngineEnvManaged([]string{"LLM_PROVIDER", "ANTHROPIC_API_KEY"})
	v, etag := env.settings(settingsAdminKey)
	if !slices.Contains(v.EnvManaged, "judge.provider") || !slices.Contains(v.EnvManaged, "bindings.judge") {
		t.Errorf("env_managed %v", v.EnvManaged)
	}
	if k := v.key(t, "LLM_PROVIDER"); k.Source != "env" || k.Editable || k.Env == nil || *k.Env != "LLM_PROVIDER" {
		t.Errorf("LLM_PROVIDER %+v", k)
	}
	if k := v.key(t, "ANTHROPIC_API_KEY"); k.Source != "env" || k.Value != "set" {
		t.Errorf("ANTHROPIC_API_KEY %+v", k)
	}
	if k := v.key(t, "LLM_MODEL"); !k.Editable || k.Source != "default" {
		t.Errorf("LLM_MODEL stays editable: %+v", k)
	}
	resp, body := env.patchSettings(etag, `{"judge":{"provider":"openrouter"}}`, "")
	e := apiErr(t, body)
	if resp.StatusCode != http.StatusConflict || e.Code != codeEnvManaged {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if keys, _ := e.Details["keys"].([]any); len(keys) != 1 || keys[0] != "LLM_PROVIDER" {
		t.Errorf("details.keys %v", e.Details)
	}
	if v := env.patched(env.patchSettings(etag, `{"judge":{"model":"anthropic/claude-haiku-4.5"}}`, "")); *v.Runtime.Judge.Model != "anthropic/claude-haiku-4.5" {
		t.Errorf("judge.model %v", v.Runtime.Judge.Model)
	}
}
