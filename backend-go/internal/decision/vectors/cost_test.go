package vectors

import (
	"encoding/json"
	"strings"
	"testing"
)

// The cost runner refuses inputs the calculator would misread, so a malformed vector fails
// instead of pinning a wrong expected value.
func TestRunCostRefusesBadInput(t *testing.T) {
	const table = `{"version":"v1","currency":"USD","entries":[{"match":"*","provider":"ollama","input_per_mtok":0,"output_per_mtok":0}]}`
	for name, c := range map[string]struct{ input, wantErr string }{
		"unknown fn":        {`{"fn":"savings"}`, `fn "savings"`},
		"missing fn":        {`{"provider":"x"}`, `fn ""`},
		"unknown field":     {`{"fn":"call_cost","provider":"x","model":"m","input_tokens":1,"output_tokens":0,"price":1,"table":` + table + `}`, "unknown field"},
		"call table":        {`{"fn":"call_cost","provider":"x","model":"m","input_tokens":1,"output_tokens":0,"table":{"version":"v1","currency":"EUR","entries":[]}}`, "table:"},
		"lookup contracts":  {`{"fn":"price_lookup","provider":"x","model":"m","overrides":[],"contracts":{"version":"","currency":"USD","entries":[]}}`, "contracts:"},
		"lookup file":       {`{"fn":"price_lookup","provider":"x","model":"m","overrides":[],"file":{"version":"f","currency":"USD","entries":[]},"contracts":` + table + `}`, "file:"},
		"lookup override":   {`{"fn":"price_lookup","provider":"x","model":"m","overrides":[{"match":"","provider":"x","input_per_mtok":1,"output_per_mtok":0}],"contracts":` + table + `}`, "overrides[0].match"},
		"per_1k bad rate":   {`{"fn":"per_1k","decision_model":{"provider":"ollama","model":"m"},"judge":{"provider":"ollama","model":"m"},"settings":{"pass_sample_rate":2},"table":` + table + `}`, "pass_sample_rate"},
		"per_1k bad table":  {`{"fn":"per_1k","decision_model":{"provider":"ollama","model":"m"},"judge":{"provider":"ollama","model":"m"},"settings":{},"table":{"version":"v","currency":"USD","entries":[]}}`, "table:"},
		"per_1k bad member": {`{"fn":"per_1k","decision_model":{"provider":"ollama","model":"m","price":1},"judge":{"provider":"ollama","model":"m"},"settings":{},"table":` + table + `}`, "unknown field"},
	} {
		_, err := Run("cost", json.RawMessage(c.input), nil)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want one mentioning %q", name, err, c.wantErr)
		}
	}
}

// Run marshals the calculator's output, so the JSON tags are what the vectors pin: an unknown
// cost is a present null, and an entry keeps only the members it has.
func TestRunCostOutputShape(t *testing.T) {
	out, err := Run("cost", json.RawMessage(`{"fn":"call_cost","provider":"openai","model":"gpt-5","input_tokens":10,"output_tokens":0,
		"table":{"version":"v1","currency":"USD","entries":[{"match":"*","provider":"ollama","input_per_mtok":0,"output_per_mtok":0}]}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := out.(map[string]any)
	if v, present := m["cost_usd"]; !present || v != nil || m["cost_source"] != "unknown" || m["price_table_version"] != "v1" {
		t.Errorf("call_cost output = %v", out)
	}
	if !Kinds["cost"].Mechanical || Kinds["cost"].Skip != "" {
		t.Error("cost is a mechanical kind with a Go runner")
	}
}
