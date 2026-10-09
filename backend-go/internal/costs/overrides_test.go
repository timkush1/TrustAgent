package costs

import (
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/decision"
)

func TestPricingOverridesClean(t *testing.T) {
	ok := []decision.PriceEntry{
		{Match: "typesafe/jev-1.13*", Provider: "openrouter", InputPerMTok: 0.05, Label: "override", AsOf: "2026-10-01", Note: "negotiated"},
		{Match: "*", Provider: "ollama"}, // $0 is fine locally
		{Match: "anthropic/claude-haiku-4.5", Provider: "openrouter", InputPerMTok: 100, OutputPerMTok: 500}, // exactly 100×
		{Match: "my-model", Provider: "compatible", InputPerMTok: 0, OutputPerMTok: 2},                       // output-only billing
	}
	if got := ValidateOverrides(ok); len(got) != 0 {
		t.Errorf("findings on valid overrides: %+v", got)
	}
	if got := ValidateOverrides(nil); got != nil {
		t.Errorf("nil overrides: %+v", got)
	}
}

func TestPricingOverridesFindings(t *testing.T) {
	overrides := []decision.PriceEntry{
		{Match: "", Provider: "openrouter", InputPerMTok: 1},                                          // 0: schema
		{Match: "gpt-*", Provider: "OpenAI", InputPerMTok: 1},                                         // 1: schema (provider)
		{Match: "gpt-*", Provider: "openai", InputPerMTok: -1},                                        // 2: schema (price)
		{Match: "gpt-*", Provider: "openai", InputPerMTok: 1, Label: "vendor_price"},                  // 3: not an override label
		{Match: "*", Provider: "openrouter"},                                                          // 4: $0 for a paid provider
		{Match: "typesafe/jev-1.13*", Provider: "openrouter", InputPerMTok: 5},                        // 5: 119× the contracts price (warning)
		{Match: "typesafe/jev-1.13*", Provider: "openrouter", InputPerMTok: 0.05},                     // 6: duplicate of 5
		{Match: "claude-haiku-4-5-2025*", Provider: "anthropic", InputPerMTok: 1, OutputPerMTok: 501}, // 7: output > 100× via the glob (warning)
		{Match: "gpt-*", Provider: "openai", InputPerMTok: 1e9},                                       // 8: no contracts price to compare with
	}
	got := ValidateOverrides(overrides)
	want := []struct {
		path, code string
		warning    bool
	}{
		{"pricing_overrides[0].match", CodeInvalidPriceEntry, false},
		{"pricing_overrides[1].provider", CodeInvalidPriceEntry, false},
		{"pricing_overrides[2].input_per_mtok", CodeInvalidPriceEntry, false},
		{"pricing_overrides[3].label", CodeInvalidPriceEntry, false},
		{"pricing_overrides[4].input_per_mtok", CodePriceZeroNotLocal, false},
		{"pricing_overrides[5].input_per_mtok", CodePriceFarAboveContracts, true},
		{"pricing_overrides[6].match", CodeDuplicateOverride, false},
		{"pricing_overrides[7].output_per_mtok", CodePriceFarAboveContracts, true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d findings, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Path != w.path || g.Code != w.code || g.Warning != w.warning || g.Message == "" {
			t.Errorf("finding %d = %+v, want %s %s warning=%v", i, g, w.path, w.code, w.warning)
		}
	}
	if d := got[5].Details; d["contracts_per_mtok"] != 0.042 || d["ratio"] != 119.0 {
		t.Errorf("far-above details = %v", d)
	}
	if !strings.Contains(got[5].Message, "119×") || !strings.Contains(got[5].Message, "$0.042") {
		t.Errorf("far-above message = %q", got[5].Message)
	}
	if got[4].Details["provider"] != "openrouter" {
		t.Errorf("zero-price details = %v", got[4].Details)
	}
	if got[6].Details["index"] != 5 {
		t.Errorf("duplicate details = %v", got[6].Details)
	}
	// Refused overrides are never priced; the warned one is.
	usable := usableOverrides(overrides)
	if len(usable) != 3 || usable[0].InputPerMTok != 5 || usable[1].OutputPerMTok != 501 || usable[2].InputPerMTok != 1e9 {
		t.Errorf("usable = %+v", usable)
	}
}
