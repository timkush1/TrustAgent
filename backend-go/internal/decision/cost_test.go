package decision

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/truthtable/contracts"
)

func TestGlobMatch(t *testing.T) {
	for _, c := range []struct {
		p, s string
		ok   bool
	}{
		{"typesafe/jev-1.13*", "typesafe/jev-1.13-20260917", true},
		{"typesafe/jev-1.13*", "typesafe/jev-1.12", false},
		{"jev-*", "jev-1.13.0", true},
		{"*", "anything/at-all", true},
		{"*", "", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
		{"ab*bc", "abc", false}, // the prefix and the suffix may not overlap
		{"a*a", "aa", true},
		{"*.5", "claude-haiku-4.5", true},
		{"claude.haiku", "claudeXhaiku", false}, // only * is special
		{"exact", "exact", true},
		{"exact", "exact2", false},
	} {
		if GlobMatch(c.p, c.s) != c.ok {
			t.Errorf("%q ~ %q: want %v", c.p, c.s, c.ok)
		}
	}
}

func testEntries() []PriceEntry {
	return []PriceEntry{
		{Match: "anthropic/claude-haiku-4.5", Provider: "openrouter", InputPerMTok: 1, OutputPerMTok: 5},
		{Match: "*", Provider: "ollama"},
		{Match: "claude-haiku-4-5*", Provider: "anthropic", InputPerMTok: 1, OutputPerMTok: 5},
		{Match: "anthropic/claude-haiku-4.5", Provider: "anthropic", InputPerMTok: 2, OutputPerMTok: 10},
		{Match: "*", Provider: "mock", InputPerMTok: 9, OutputPerMTok: 9},
	}
}

func TestLookupPrice(t *testing.T) {
	entries := testEntries()
	for _, c := range []struct {
		provider, model string
		want            int // index into entries, -1 for no match
	}{
		{"anthropic", "anthropic/claude-haiku-4.5", 3},  // the provider's own entry beats an earlier one
		{"anthropic", "claude-haiku-4-5-20251001", 2},   // glob
		{"compatible", "anthropic/claude-haiku-4.5", 0}, // no entry of its own: the first other match
		{"", "anthropic/claude-haiku-4.5", 0},           // no pricing provider: any provider
		{"ollama", "llama3.1:8b", 1},                    // a local provider's own (free) entry
		{"ollama", "anthropic/claude-haiku-4.5", 1},
		{"openai", "gpt-4o-mini", -1}, // the free ollama and mock catch-alls never price others
		{"", "gpt-4o-mini", -1},
	} {
		got, ok := LookupPrice(entries, c.provider, c.model)
		switch {
		case c.want < 0 && ok:
			t.Errorf("%s %s: matched %+v, want none", c.provider, c.model, got)
		case c.want >= 0 && (!ok || got != entries[c.want]):
			t.Errorf("%s %s: %+v %v, want entry %d", c.provider, c.model, got, ok, c.want)
		}
	}
	// A local or mock provider never borrows another provider's paid price.
	paid := []PriceEntry{{Match: "*", Provider: "openrouter", InputPerMTok: 1}}
	for _, p := range []string{"ollama", "mock"} {
		if e, ok := LookupPrice(paid, p, "llama3.1:8b"); ok {
			t.Errorf("%s borrowed %+v", p, e)
		}
	}
}

func TestTokenCostUSD(t *testing.T) {
	for _, c := range []struct {
		in, out   int64
		pin, pout float64
		want      string // the JSON of the result: exact decimals, no float noise
	}{
		{566, 0, 0.042, 0, "0.000023772"},
		{1, 0, 0.042, 0, "4.2e-8"},
		{24275, 0, 0.042, 0, "0.00101955"},
		{3525, 120, 1, 5, "0.004125"},
		{0, 0, 1, 5, "0"},
		{-10, -3, 1, 5, "0"},
		{1000, 0, 0.0421234567, 0, "0.0000421234567"},                // more than six decimals: the exact path
		{1_000_000_000_000, 7, 3, 15, "3000000.000105"},              // beyond 2^53 pico-dollars
		{3, 0, 0.1, 0, "3e-7"},                                       // 3 × 0.1 is not 0.30000000000000004 here
		{123456789, 987654321, 0.000001, 0.000003, "0.003086419752"}, // a micro-dollar price
	} {
		got := TokenCostUSD(c.in, c.out, c.pin, c.pout)
		if b, _ := json.Marshal(got); string(b) != c.want {
			t.Errorf("TokenCostUSD(%d, %d, %v, %v) = %s, want %s", c.in, c.out, c.pin, c.pout, b, c.want)
		}
	}
}

func TestPriceCall(t *testing.T) {
	table := PriceTable{Version: "2026-09-27", Currency: "USD", Entries: append([]PriceEntry{
		{Match: "anthropic/claude-sonnet-*", Provider: "openrouter", InputPerMTok: 3, OutputPerMTok: 15, Label: PriceLabelOverride},
	}, testEntries()...)}
	reported := 0.0031
	for _, c := range []struct {
		name    string
		in      CallCostInput
		usd     *float64
		source  string
		version string
	}{
		{"computed", CallCostInput{Provider: "openrouter", Model: "anthropic/claude-haiku-4.5", InputTokens: 3525, OutputTokens: 120}, ptr(0.004125), CostComputed, "2026-09-27"},
		{"reported wins", CallCostInput{Provider: "openrouter", Model: "anthropic/claude-haiku-4.5", InputTokens: 3525, OutputTokens: 120, ReportedUSD: &reported}, ptr(0.0031), CostReported, "2026-09-27"},
		{"override", CallCostInput{Provider: "openrouter", Model: "anthropic/claude-sonnet-4.5", InputTokens: 1000, OutputTokens: 100}, ptr(0.0045), CostComputed, "override"},
		{"local", CallCostInput{Provider: "ollama", Model: "llama3.1:8b", InputTokens: 5000, OutputTokens: 500}, ptr(0.0), CostFreeLocal, "2026-09-27"},
		{"mock beats reported", CallCostInput{Provider: "mock", Model: "mock-judge", InputTokens: 5000, ReportedUSD: &reported}, ptr(0.0), CostSimulated, "2026-09-27"},
		{"unknown", CallCostInput{Provider: "openai", Model: "gpt-4o-mini", InputTokens: 5000}, nil, CostUnknown, "2026-09-27"},
	} {
		c.in.Table = table
		got := PriceCall(c.in)
		if got.Source != c.source || got.PriceTableVersion != c.version || (got.USD == nil) != (c.usd == nil) ||
			got.USD != nil && *got.USD != *c.usd {
			t.Errorf("%s: %+v (usd %v), want %v %s %s", c.name, got, usdOf(got.USD), usdOf(c.usd), c.source, c.version)
		}
	}
	// A local provider whose own entry has a price is computed like any other.
	paidLocal := PriceTable{Version: "v", Currency: "USD", Entries: []PriceEntry{{Match: "*", Provider: "ollama", InputPerMTok: 0.5}}}
	if got := PriceCall(CallCostInput{Provider: "ollama", Model: "m", InputTokens: 2_000_000, Table: paidLocal}); got.Source != CostComputed || *got.USD != 1 {
		t.Errorf("priced local = %+v", got)
	}
	// An unknown cost is null in JSON, so it is never summed as $0.
	b, _ := json.Marshal(PriceCall(CallCostInput{Provider: "openai", Model: "x", Table: table}))
	if !strings.Contains(string(b), `"cost_usd":null`) {
		t.Errorf("unknown cost JSON = %s", b)
	}
}

func usdOf(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestEffectivePrices(t *testing.T) {
	base := PriceTable{Version: "2026-09-27", Currency: "USD", Entries: []PriceEntry{{Match: "jev-*", Provider: "typesafe", InputPerMTok: 0.042, Label: PriceLabelVendor}}}
	file := PriceTable{Version: "acme-1", Currency: "USD", Entries: []PriceEntry{{Match: "jev-*", Provider: "typesafe", InputPerMTok: 0.04}}}
	overrides := []PriceEntry{{Match: "jev-1.13*", Provider: "typesafe", InputPerMTok: 0.03, Note: "negotiated"}}

	got := EffectivePrices(base, &file, overrides)
	if got.Version != "acme-1" || got.Currency != "USD" || len(got.Entries) != 3 {
		t.Fatalf("effective = %+v", got)
	}
	if got.Entries[0].Label != PriceLabelOverride || got.Entries[0].Note != "negotiated" || got.Entries[1] != file.Entries[0] || got.Entries[2] != base.Entries[0] {
		t.Errorf("order or labels: %+v", got.Entries)
	}
	if overrides[0].Label != "" {
		t.Error("EffectivePrices modified the overrides")
	}
	if e, _ := LookupPrice(got.Entries, "typesafe", "jev-1.13.0"); e.InputPerMTok != 0.03 {
		t.Errorf("the override must win: %+v", e)
	}
	if e, _ := LookupPrice(got.Entries, "typesafe", "jev-2.0"); e.InputPerMTok != 0.04 {
		t.Errorf("the file must win over contracts: %+v", e)
	}
	if got := EffectivePrices(base, nil, nil); got.Version != "2026-09-27" || len(got.Entries) != 1 {
		t.Errorf("contracts only = %+v", got)
	}
}

func TestParsePriceTable(t *testing.T) {
	good := `{"version":"v1","currency":"USD","entries":[{"match":"*","provider":"ollama","input_per_mtok":0,"output_per_mtok":0}]}`
	if _, err := ParsePriceTable([]byte(good)); err != nil {
		t.Fatalf("valid table: %v", err)
	}
	for name, doc := range map[string]string{
		"unknown field":    `{"version":"v1","currency":"USD","extra":1,"entries":[{"match":"*","provider":"ollama","input_per_mtok":0,"output_per_mtok":0}]}`,
		"no version":       `{"version":"","currency":"USD","entries":[{"match":"*","provider":"ollama","input_per_mtok":0,"output_per_mtok":0}]}`,
		"currency":         `{"version":"v1","currency":"EUR","entries":[{"match":"*","provider":"ollama","input_per_mtok":0,"output_per_mtok":0}]}`,
		"no entries":       `{"version":"v1","currency":"USD","entries":[]}`,
		"negative price":   `{"version":"v1","currency":"USD","entries":[{"match":"*","provider":"x","input_per_mtok":-1,"output_per_mtok":0}]}`,
		"provider pattern": `{"version":"v1","currency":"USD","entries":[{"match":"*","provider":"Open Router","input_per_mtok":1,"output_per_mtok":0}]}`,
		"label":            `{"version":"v1","currency":"USD","entries":[{"match":"*","provider":"x","input_per_mtok":1,"output_per_mtok":0,"label":"guess"}]}`,
		"as_of":            `{"version":"v1","currency":"USD","entries":[{"match":"*","provider":"x","input_per_mtok":1,"output_per_mtok":0,"as_of":"27/09/2026"}]}`,
		"trailing data":    good + `{}`,
		"not json":         `{`,
	} {
		if _, err := ParsePriceTable([]byte(doc)); !errors.Is(err, ErrPriceTable) {
			t.Errorf("%s: err = %v, want ErrPriceTable", name, err)
		}
	}
	// The embedded contracts table parses, and every entry the jev and judge paths need is there.
	ct, err := ContractsPrices()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []ModelRef{{"openrouter", "typesafe/jev-1.13"}, {"typesafe", "jev-1.13"}, {"anthropic", "claude-haiku-4-5"}, {"openrouter", "anthropic/claude-haiku-4.5"}, {"ollama", "llama3.1"}} {
		if _, ok := LookupPrice(ct.Entries, m.Provider, m.Model); !ok {
			t.Errorf("contracts table has no price for %+v", m)
		}
	}
}

// assumptionsDoc is the part of contracts/pricing/assumptions.v1.json the calculator reproduces.
type assumptionsDoc struct {
	// Prices name table entries by provider and glob.
	Prices map[string]struct {
		Provider string `json:"provider"`
		Match    string `json:"match"`
	} `json:"prices"`
	Inputs map[string]struct {
		Value float64 `json:"value"`
	} `json:"inputs"`
	Figures []struct {
		ID        string   `json:"id"`
		Per1kUSD  *float64 `json:"per_1k_usd"`
		Tolerance *float64 `json:"tolerance"`
	} `json:"figures"`
}

func loadAssumptions(t *testing.T) (assumptionsDoc, Per1kSettings) {
	t.Helper()
	b, err := contracts.FS.ReadFile("pricing/assumptions.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var a assumptionsDoc
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	inputs := map[string]float64{}
	for k, v := range a.Inputs {
		inputs[k] = v.Value
	}
	// The settings are named as the assumption inputs, so a JSON round trip maps them.
	raw, _ := json.Marshal(inputs)
	var s Per1kSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return a, s
}

// The calculator reproduces every cost figure of the published assumptions, so the README and
// landing copy cannot drift from the code (SPEC §1 cost story).
func TestCostPer1kMatchesAssumptions(t *testing.T) {
	a, s := loadAssumptions(t)
	ct, err := ContractsPrices()
	if err != nil {
		t.Fatal(err)
	}
	model := func(alias string) ModelRef { // a model the aliased entry's glob matches
		p := a.Prices[alias]
		return ModelRef{Provider: p.Provider, Model: strings.ReplaceAll(p.Match, "*", "")}
	}
	got, err := CostPer1k(Per1kInput{Table: ct, DecisionModel: model("decision_model"), Judge: model("judge"), Settings: s})
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalPer1k == nil || got.FastCheckPer1k == nil || got.DeepAuditUSD == nil {
		t.Fatalf("per 1k = %+v", got)
	}
	figures := map[string]float64{
		"fast_check_per_1k":     *got.FastCheckPer1k,
		"llm_full_audit_per_1k": *got.DeepAuditUSD * 1000,
		"default_total_per_1k":  *got.TotalPer1k,
	}
	for _, f := range a.Figures {
		v, ok := figures[f.ID]
		if !ok || f.Per1kUSD == nil {
			continue
		}
		tol := 0.005
		if f.Tolerance != nil {
			tol = *f.Tolerance
		}
		if math.Abs(v-*f.Per1kUSD) > tol {
			t.Errorf("%s: calculator %v, assumptions state %v (tolerance %v)", f.ID, v, *f.Per1kUSD, tol)
		}
		delete(figures, f.ID)
	}
	if len(figures) != 0 {
		t.Errorf("figures missing from assumptions.v1.json: %v", figures)
	}
	// Exact decimals: $0.21 + 0.079 × $35.5 = $3.0145.
	if *got.TotalPer1k != 3.0145 || *got.FastCheckPer1k != 0.21 || *got.DeepAuditPer1k != 2.8045 || got.AuditRate != 0.079 || *got.DeepAuditUSD != 0.0355 {
		t.Errorf("default policy = %+v", got)
	}
	if got.DecisionCostSource != CostComputed || got.JudgeCostSource != CostComputed {
		t.Errorf("sources = %s, %s", got.DecisionCostSource, got.JudgeCostSource)
	}
}

func TestCostPer1kSources(t *testing.T) {
	_, s := loadAssumptions(t)
	ct, _ := ContractsPrices()
	in := Per1kInput{Table: ct, DecisionModel: ModelRef{"openrouter", "typesafe/jev-1.13"}, Settings: s}

	in.Judge = ModelRef{"ollama", "llama3.1:8b"}
	got, err := CostPer1k(in)
	if err != nil || got.JudgeCostSource != CostFreeLocal || *got.DeepAuditPer1k != 0 || *got.TotalPer1k != 0.21 {
		t.Errorf("local judge = %+v, %v", got, err)
	}
	in.Judge = ModelRef{"openai", "gpt-4o-mini"}
	got, _ = CostPer1k(in)
	if got.JudgeCostSource != CostUnknown || got.DeepAuditUSD != nil || got.DeepAuditPer1k != nil || got.TotalPer1k != nil || *got.FastCheckPer1k != 0.21 {
		t.Errorf("unknown judge = %+v", got)
	}
	in.DecisionModel = ModelRef{"mock", "mock/jev"}
	got, _ = CostPer1k(in)
	if got.DecisionCostSource != CostSimulated || *got.FastCheckUSD != 0 {
		t.Errorf("mock decision model = %+v", got)
	}
	// Rates that add up past every answer audit every answer once.
	in.Settings.GroundingFlagRate, in.Settings.PassSampleRate, in.Settings.UniformSampleRate = 0.9, 0.9, 0.9
	if got, _ = CostPer1k(in); got.AuditRate != 1 {
		t.Errorf("audit rate = %v", got.AuditRate)
	}
	for _, bad := range []func(*Per1kSettings){
		func(s *Per1kSettings) { s.PassSampleRate = 1.5 },
		func(s *Per1kSettings) { s.ClaimsPerAnswer = -1 },
		func(s *Per1kSettings) { s.FastCheckInputTokens = math.Inf(1) },
	} {
		in.Settings = s
		bad(&in.Settings)
		if _, err := CostPer1k(in); !errors.Is(err, ErrPer1kSettings) {
			t.Errorf("bad settings %+v: err = %v", in.Settings, err)
		}
	}
}

func TestLookupEffective(t *testing.T) {
	ct, _ := ContractsPrices()
	file := &PriceTable{Version: "acme-2026-10", Currency: "USD", Entries: []PriceEntry{{Match: "typesafe/jev-1.13*", Provider: "openrouter", InputPerMTok: 0.04}}}
	in := PriceLookupInput{Contracts: ct, File: file, Overrides: []PriceEntry{{Match: "*jev*", Provider: "openrouter", InputPerMTok: 0.05}}, Provider: "typesafe", Model: "jev-1.13"}
	got := LookupEffective(in)
	if got.Entry == nil || got.Entry.Provider != "typesafe" || got.PriceTableVersion != "acme-2026-10" {
		t.Errorf("the provider's own contracts entry must beat another provider's override: %+v", got)
	}
	in.Provider, in.Model = "openrouter", "typesafe/jev-1.13"
	if got = LookupEffective(in); got.Entry == nil || got.Entry.InputPerMTok != 0.05 || got.PriceTableVersion != "override" {
		t.Errorf("override = %+v", got)
	}
	in.Provider, in.Model = "openai", "gpt-5"
	if got = LookupEffective(in); got.Entry != nil || got.PriceTableVersion != "acme-2026-10" {
		t.Errorf("no match = %+v", got)
	}
}
