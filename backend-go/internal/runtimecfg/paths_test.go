package runtimecfg

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/decision"
)

// jsonLeaves lists the leaf paths of a JSON object: objects recurse, anything else (a string, a
// number, null, an array) is a leaf.
func jsonLeaves(t *testing.T, prefix string, raw json.RawMessage) []string {
	t.Helper()
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return []string{prefix}
	}
	var out []string
	for k, v := range obj {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		out = append(out, jsonLeaves(t, p, v)...)
	}
	return out
}

// TestLeavesCoverRuntimeSettings: the leaf table is exactly the JSON leaves of RuntimeSettings, so
// a field added to the struct cannot be missed by the settings API.
func TestLeavesCoverRuntimeSettings(t *testing.T) {
	b, err := json.Marshal(seedSettings(nil))
	if err != nil {
		t.Fatal(err)
	}
	got := jsonLeaves(t, "", b)
	var want []string
	for _, l := range Leaves {
		want = append(want, l.Path)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("RuntimeSettings leaves %v\nLeaves table %v", got, want)
	}
	for _, l := range Leaves {
		if !IsBranch(strings.Split(l.Path, ".")[0]) && strings.Contains(l.Path, ".") {
			t.Errorf("%s: its object is not a branch", l.Path)
		}
		if IsBranch(l.Path) {
			t.Errorf("%s is a leaf, not a branch", l.Path)
		}
	}
	if IsBranch("upstream.default.url") || !IsBranch("upstream.default") || IsBranch("nope") {
		t.Error("IsBranch")
	}
}

// TestSettingPathCoversTheSettableKeys: every runtime-settable registry key maps to its path
// (except LLM_FALLBACK, which RuntimeSettings does not carry), and the D33 env paths agree.
func TestSettingPathCoversTheSettableKeys(t *testing.T) {
	reg := config.MustRegistry()
	for _, k := range reg.Keys {
		if k.Runtime != "settable" {
			if SettingPath(k.Name) != "" {
				t.Errorf("%s (runtime %q) has a settings path", k.Name, k.Runtime)
			}
			continue
		}
		p := SettingPath(k.Name)
		if k.Name == "LLM_FALLBACK" {
			if p != "" {
				t.Errorf("LLM_FALLBACK has no RuntimeSettings field, got %q", p)
			}
			continue
		}
		if _, ok := LookupLeaf(p); !ok {
			t.Errorf("settable key %s maps to %q, not a leaf", k.Name, p)
		}
	}
	for _, e := range envPaths {
		if e.key != "" && SettingPath(e.key) != e.path {
			t.Errorf("%s: env path %s, settings path %s", e.key, e.path, SettingPath(e.key))
		}
	}
}

func TestSetLeavesAndFlatten(t *testing.T) {
	base := seedSettings(nil)
	before := Flatten(base)
	if _, err := SetLeaves(base, map[string]json.RawMessage{
		"decision_model.model":      json.RawMessage(`"jev-1"`),
		"upstream.fallback.url":     json.RawMessage(`"https://fallback.example.com/v1"`),
		"upstream.fallback.format":  json.RawMessage(`"anthropic"`),
		"budgets.llm_usd_day":       json.RawMessage(`7.5`),
		"bindings.judge":            json.RawMessage(`"openrouter"`),
		"pricing_overrides":         json.RawMessage(`[{"match":"jev-*","provider":"openrouter","input_per_mtok":1,"output_per_mtok":2}]`),
		"upstream.default.url":      json.RawMessage(`null`),
		"upstream.anthropic.header": json.RawMessage(`"x"`),
	}); err == nil || !strings.Contains(err.Error(), "upstream.anthropic.header") {
		t.Fatalf("an unknown path is refused: %v", err)
	}
	out, err := SetLeaves(base, map[string]json.RawMessage{
		"decision_model.model":     json.RawMessage(`"jev-1"`),
		"upstream.fallback.url":    json.RawMessage(`"https://fallback.example.com/v1"`),
		"upstream.fallback.format": json.RawMessage(`"anthropic"`),
		"budgets.llm_usd_day":      json.RawMessage(`7.5`),
		"bindings.judge":           json.RawMessage(`"openrouter"`),
		"pricing_overrides":        json.RawMessage(`[{"match":"jev-*","provider":"openrouter","input_per_mtok":1,"output_per_mtok":2}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if *out.DecisionModel.Model != "jev-1" || *out.Upstream.Fallback.URL != "https://fallback.example.com/v1" ||
		out.Upstream.Fallback.Format != "anthropic" || out.Upstream.Fallback.AuthHeader != "authorization" ||
		out.Budgets.LLMUSDDay != 7.5 || *out.Bindings.Judge != "openrouter" ||
		!slices.Equal(out.PricingOverrides, []decision.PriceEntry{{Match: "jev-*", Provider: "openrouter", InputPerMTok: 1, OutputPerMTok: 2}}) {
		t.Errorf("set leaves: %+v", out)
	}
	if got := Flatten(base); !mapsEqual(got, before) {
		t.Error("SetLeaves wrote through to its input")
	}
	changed := ChangedLeaves(base, out)
	want := []string{"decision_model.model", "upstream.fallback.url", "upstream.fallback.format", "bindings.judge",
		"budgets.llm_usd_day", "pricing_overrides"}
	if !slices.Equal(changed, want) {
		t.Errorf("changed %v, want %v (Leaves order)", changed, want)
	}
	for path, raw := range map[string]string{
		"decision_model.backend": `null`, "budgets.llm_usd_day": `"5"`, "judge.model": `5`,
		"pricing_overrides": `[{"model":"x"}]`,
	} {
		if _, err := SetLeaves(base, map[string]json.RawMessage{path: json.RawMessage(raw)}); err == nil || !strings.Contains(err.Error(), path+" must be") {
			t.Errorf("%s = %s: %v", path, raw, err)
		}
	}
}

func mapsEqual(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if string(b[k]) != string(v) {
			return false
		}
	}
	return true
}

// TestRowsWithLeavesKeepsTheStoredFields: a write stores only the leaves it changes, merged into
// the rows as stored, and reading those rows back over the built-in settings gives the settings
// the write produced.
func TestRowsWithLeavesKeepsTheStoredFields(t *testing.T) {
	stored := map[string]json.RawMessage{
		"upstream": json.RawMessage(`{"default":{"url":"https://gw.example.com/v1"}}`),
		"budgets":  json.RawMessage(`{"llm_usd_day":4}`),
	}
	builtin := seedSettings(nil)
	before, bad := overlayRows(builtin, stored)
	if len(bad) > 0 {
		t.Fatal(bad)
	}
	after, err := SetLeaves(before, map[string]json.RawMessage{
		"upstream.fallback.url":    json.RawMessage(`"https://fb.example.com"`),
		"budgets.decision_usd_day": json.RawMessage(`3`),
		"pricing_overrides":        json.RawMessage(`[]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	changed := ChangedLeaves(before, after)
	rows, err := RowsWithLeaves(stored, after, changed)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(rows["upstream"]); got != `{"default":{"url":"https://gw.example.com/v1"},"fallback":{"url":"https://fb.example.com"}}` {
		t.Errorf("upstream row %s", got)
	}
	if got := string(rows["budgets"]); got != `{"decision_usd_day":3,"llm_usd_day":4}` {
		t.Errorf("budgets row %s", got)
	}
	if _, ok := rows["pricing_overrides"]; ok {
		t.Error("an unchanged leaf is not written")
	}
	merged := map[string]json.RawMessage{}
	for k, v := range stored {
		merged[k] = v
	}
	for k, v := range rows {
		merged[k] = v
	}
	reread, _ := overlayRows(builtin, merged)
	if !mapsEqual(Flatten(reread), Flatten(after)) {
		t.Errorf("rows read back %+v, want %+v", reread, after)
	}
	paths := StoredPaths(merged)
	want := []string{"upstream.default.url", "upstream.fallback.url", "budgets.decision_usd_day", "budgets.llm_usd_day"}
	if !slices.Equal(paths, want) {
		t.Errorf("stored paths %v, want %v", paths, want)
	}
	if _, err := RowsWithLeaves(stored, after, []string{"upstream.default"}); err == nil {
		t.Error("a branch is not a leaf")
	}
}

// TestStoredAndEffective: Stored pairs the snapshot with the rows it was built from; Effective
// lays the env values over persisted settings exactly as the snapshot does.
func TestStoredAndEffective(t *testing.T) {
	cfg := loadCfg("TRUTHTABLE_JEV_BACKEND=typesafe", "TRUTHTABLE_SPEND_CAP_LLM_USD_DAY=9")
	fs := newFakeStore()
	fs.rows.Version = 4
	fs.rows.Rows["budgets"] = json.RawMessage(`{"decision_usd_day":1.5}`)
	m := New(Options{Config: cfg, Store: fs, Master: master(t, 5), Log: quietLog()})
	if snap, rows := m.Stored(); snap.Version != 0 || len(rows) != 0 {
		t.Errorf("before the first read: v%d %v", snap.Version, rows)
	}
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap, rows := m.Stored()
	if snap.Version != 4 || string(rows["budgets"]) != `{"decision_usd_day":1.5}` {
		t.Errorf("stored v%d %s", snap.Version, rows)
	}
	rows["budgets"][0] = 'X' // a copy: the Manager's rows stay intact
	if _, again := m.Stored(); string(again["budgets"]) != `{"decision_usd_day":1.5}` {
		t.Error("Stored returned the Manager's own rows")
	}
	eff := m.Effective(snap.Persisted)
	if !mapsEqual(Flatten(eff), Flatten(snap.Settings)) {
		t.Errorf("Effective(Persisted) %+v, snapshot %+v", eff, snap.Settings)
	}
	p := snap.Persisted
	p.Budgets.LLMUSDDay, p.DecisionModel.Backend = 2, "mock"
	eff = m.Effective(p)
	if eff.Budgets.LLMUSDDay != 9 || eff.DecisionModel.Backend != "typesafe" || eff.Budgets.DecisionUSDDay != 1.5 {
		t.Errorf("env wins over persisted: %+v %+v", eff.Budgets, eff.DecisionModel)
	}
}
