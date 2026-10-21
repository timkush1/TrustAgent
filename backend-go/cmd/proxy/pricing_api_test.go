package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/contracts"
)

// TestPricing: GET /api/pricing serves the embedded price table as a PricingTable (read scope).
func TestPricing(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	code, body := env.get("/api/pricing", testKey)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	// pricing.v1 is the PricingTable schema (api.ts PricingTable, PriceEntry).
	contractstest.ValidateJSON(t, "pricing.v1.schema.json", body)
	file, err := contracts.FS.ReadFile("pricing/prices.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	_ = json.Unmarshal(body, &got)
	_ = json.Unmarshal(file, &want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("served table differs from contracts/pricing/prices.v1.json:\n%s", body)
	}
	if code, _ := env.get("/api/pricing", ""); code != http.StatusUnauthorized {
		t.Errorf("without a key: %d", code)
	}
	if code, _ := newTestEnv(t, envOpts{publicRead: true}).get("/api/pricing", ""); code != http.StatusOK {
		t.Errorf("public read: %d", code)
	}
}

// TestPricingServesTheEffectiveTable: GET /api/pricing serves the price book in force (D53): the
// dashboard's overrides first, labelled override, then TRUTHTABLE_PRICE_TABLE_FILE, then the
// contracts table; an override that ValidateOverrides refuses ($0 for a hosted provider) is left
// out. The committed PricingTable fixture is a valid answer.
func TestPricingServesTheEffectiveTable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(file, []byte(`{"version":"2026-10-01","currency":"USD","entries":[
		{"match":"gpt-4o-mini*","provider":"openai","input_per_mtok":0.15,"output_per_mtok":0.6,"source":"https://openai.com/api/pricing","label":"vendor_price","as_of":"2026-10-01"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := newTestEnv(t, envOpts{mutate: func(c *config.Config) { c.PriceTableFile = file }})
	env.srv.ledger().Prices().SetOverrides([]decision.PriceEntry{
		{Match: "gpt-4o-mini*", Provider: "openai", InputPerMTok: 0.12, OutputPerMTok: 0.5, Note: "negotiated rate"},
		{Match: "claude-*", Provider: "anthropic", InputPerMTok: 0, OutputPerMTok: 0}, // refused: $0 for a hosted provider
	})
	code, body := env.get("/api/pricing", testKey)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, "pricing.v1.schema.json", body)
	var got decision.PriceTable
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	contractsTable, err := decision.ContractsPrices()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "2026-10-01" || len(got.Entries) != 2+len(contractsTable.Entries) {
		t.Fatalf("table %s with %d entries", got.Version, len(got.Entries))
	}
	first, second := got.Entries[0], got.Entries[1]
	if first.Label != "override" || first.Match != "gpt-4o-mini*" || first.InputPerMTok != 0.12 || first.Note != "negotiated rate" {
		t.Errorf("first entry %+v", first)
	}
	if second.Label != "vendor_price" || second.InputPerMTok != 0.15 || second.Source == "" {
		t.Errorf("file entry %+v", second)
	}
	if !reflect.DeepEqual(got.Entries[2:], contractsTable.Entries) {
		t.Errorf("the contracts entries do not follow the file's")
	}
	fixture, err := contracts.FS.ReadFile("fixtures/api/PricingTable.default.json")
	if err != nil {
		t.Fatal(err)
	}
	contractstest.ValidateJSON(t, "pricing.v1.schema.json", fixture)
}
