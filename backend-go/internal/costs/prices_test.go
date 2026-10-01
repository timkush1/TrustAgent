package costs

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
)

func contractsTable(t *testing.T) decision.PriceTable {
	t.Helper()
	ct, err := decision.ContractsPrices()
	if err != nil {
		t.Fatal(err)
	}
	return ct
}

func TestPriceBookContractsOnly(t *testing.T) {
	ct := contractsTable(t)
	b, err := LoadPriceBook("")
	if err != nil {
		t.Fatal(err)
	}
	if b.Version() != ct.Version || len(b.Effective().Entries) != len(ct.Entries) {
		t.Errorf("effective = %+v", b.Effective())
	}
	e, v, ok := b.Lookup("openrouter", "typesafe/jev-1.13")
	if !ok || e.InputPerMTok != 0.042 || v != ct.Version {
		t.Errorf("Lookup = %+v %s %v", e, v, ok)
	}
	if p, v, ok := b.JevPrices(jev.BackendOpenRouterDecisions, "typesafe/jev-1.13-20260917"); !ok || p != 0.042 || v != ct.Version {
		t.Errorf("JevPrices(openrouter) = %v %s %v", p, v, ok)
	}
	if p, _, ok := b.JevPrices(jev.BackendTypeSafe, "jev-1.13"); !ok || p != 0.042 {
		t.Errorf("JevPrices(typesafe) = %v %v", p, ok)
	}
	if _, _, ok := b.JevPrices(jev.BackendMock, "mock/jev"); ok {
		t.Error("the mock backend has no price")
	}
	got := b.PriceCall("openrouter", "anthropic/claude-haiku-4.5", 3525, 120, nil)
	if got.Source != decision.CostComputed || got.USD == nil || *got.USD != 0.004125 {
		t.Errorf("PriceCall = %+v", got)
	}
}

func TestPriceBookOverrides(t *testing.T) {
	b := NewPriceBook(contractsTable(t), nil)
	overrides := []decision.PriceEntry{
		{Match: "typesafe/jev-1.13*", Provider: "openrouter", InputPerMTok: 0.05, Note: "negotiated"},
		{Match: "*", Provider: "openrouter", InputPerMTok: 0, OutputPerMTok: 0},   // refused: $0 for a paid provider
		{Match: "typesafe/jev-1.13*", Provider: "openrouter", InputPerMTok: 0.07}, // refused: duplicate
	}
	pure := b.EffectiveJSON(overrides)
	if !bytes.Equal(b.CurrentJSON(), b.EffectiveJSON(nil)) {
		t.Error("EffectiveJSON changed the book")
	}
	if dropped := b.SetOverrides(overrides); dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
	if !bytes.Equal(pure, b.CurrentJSON()) {
		t.Errorf("EffectiveJSON(overrides) = %s\nCurrentJSON = %s", pure, b.CurrentJSON())
	}
	eff := b.Effective()
	if len(eff.Entries) != len(contractsTable(t).Entries)+1 || eff.Entries[0].Label != decision.PriceLabelOverride || eff.Entries[0].Note != "negotiated" {
		t.Errorf("effective = %+v", eff.Entries)
	}
	if p, v, ok := b.JevPrices(jev.BackendOpenRouterSystemOne, "typesafe/jev-1.13"); !ok || p != 0.05 || v != decision.PriceVersionOverride {
		t.Errorf("the override must price Jev: %v %s %v", p, v, ok)
	}
	// The table served as JSON is a valid pricing.v1 document.
	if _, err := decision.ParsePriceTable(b.CurrentJSON()); err != nil {
		t.Errorf("effective JSON: %v", err)
	}
	// Effective returns a copy.
	eff.Entries[0].InputPerMTok = 99
	if b.Effective().Entries[0].InputPerMTok != 0.05 {
		t.Error("Effective shares its entries")
	}
	b.SetOverrides(nil)
	if p, _, _ := b.JevPrices(jev.BackendOpenRouterDecisions, "typesafe/jev-1.13"); p != 0.042 {
		t.Errorf("clearing the overrides restores the table price: %v", p)
	}
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadPriceBookFile(t *testing.T) {
	path := writeFile(t, `{"version":"acme-2026-10","currency":"USD","entries":[
		{"match":"typesafe/jev-1.13*","provider":"openrouter","input_per_mtok":0.04,"output_per_mtok":0,"note":"contract price"}]}`)
	b, err := LoadPriceBook(path)
	if err != nil {
		t.Fatal(err)
	}
	if b.Version() != "acme-2026-10" {
		t.Errorf("version = %s", b.Version())
	}
	if e, v, _ := b.Lookup("openrouter", "typesafe/jev-1.13"); e.InputPerMTok != 0.04 || v != "acme-2026-10" {
		t.Errorf("the file must win over contracts: %+v %s", e, v)
	}
	// Models the file does not price keep the contracts price.
	if e, _, ok := b.Lookup("openrouter", "anthropic/claude-haiku-4.5"); !ok || e.OutputPerMTok != 5 {
		t.Errorf("contracts under the file: %+v %v", e, ok)
	}
	b.SetOverrides([]decision.PriceEntry{{Match: "typesafe/jev-1.13*", Provider: "openrouter", InputPerMTok: 0.03}})
	if e, v, _ := b.Lookup("openrouter", "typesafe/jev-1.13"); e.InputPerMTok != 0.03 || v != "override" {
		t.Errorf("an override must win over the file: %+v %s", e, v)
	}
	if b.Contracts().Version != contractsTable(t).Version {
		t.Error("Contracts is the embedded table")
	}

	if _, err := LoadPriceBook(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("a missing file is an error")
	}
	bad := writeFile(t, `{"version":"x","currency":"USD","entries":[{"match":"*","provider":"x","input_per_mtok":-1,"output_per_mtok":0}]}`)
	if _, err := LoadPriceBook(bad); !errors.Is(err, decision.ErrPriceTable) {
		t.Errorf("an invalid file: %v", err)
	}
}

// Readers see whole snapshots while the overrides change (run with -race).
func TestPriceBookConcurrent(t *testing.T) {
	b := NewPriceBook(contractsTable(t), nil)
	sets := [][]decision.PriceEntry{
		{{Match: "typesafe/jev-1.13*", Provider: "openrouter", InputPerMTok: 0.05}},
		{{Match: "typesafe/jev-1.13*", Provider: "openrouter", InputPerMTok: 0.06}, {Match: "x*", Provider: "openrouter", InputPerMTok: 1}},
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				var t2 decision.PriceTable
				if err := json.Unmarshal(b.CurrentJSON(), &t2); err != nil {
					t.Error(err)
					return
				}
				n := len(t2.Entries) - len(b.Contracts().Entries)
				if p := t2.Entries[0].InputPerMTok; !(n == 1 && p == 0.05 || n == 2 && p == 0.06 || n == 0) {
					t.Errorf("torn snapshot: %d overrides, first price %v", n, p)
					return
				}
				if p, _, ok := b.JevPrices(jev.BackendOpenRouterDecisions, "typesafe/jev-1.13"); !ok || p != 0.05 && p != 0.06 && p != 0.042 {
					t.Errorf("price %v", p)
					return
				}
			}
		}()
	}
	for i := range 200 {
		b.SetOverrides(sets[i%2])
	}
	close(stop)
	wg.Wait()
}
