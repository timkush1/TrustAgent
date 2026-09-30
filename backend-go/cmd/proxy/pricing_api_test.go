package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/truthtable/backend-go/internal/contractstest"
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
