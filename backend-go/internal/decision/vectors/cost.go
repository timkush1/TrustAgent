package vectors

import (
	"encoding/json"
	"fmt"
	"io/fs"

	"github.com/truthtable/backend-go/internal/decision"
)

// costKind runs the cost vectors over the pure calculator in internal/decision/cost.go
// (D52, D85). The input's fn names the function; the other members are its arguments, named as the
// JSON tags of its input type, and every price table is inline in the pricing.v1 shape:
//
//	call_cost:    {fn, provider, model, input_tokens, output_tokens, reported_cost_usd?, table}
//	              -> decision.CallCost {cost_usd, cost_source, price_table_version}
//	price_lookup: {fn, contracts, file?, overrides, provider, model}
//	              -> decision.PriceLookup {entry, price_table_version}
//	per_1k:       {fn, table, decision_model, judge, settings}
//	              -> decision.Per1k
//
// cmd/vectorgen fills in expected, so the kind is mechanical. Python runs call_cost only (it is
// pushed the already-effective table); TypeScript (frontend-react/src/lib/decision/vectors/cost.ts)
// runs every fn too.
var costKind = Kind{Mechanical: true, run: runCost}

// Cost vector functions.
const (
	costFnCallCost    = "call_cost"
	costFnPriceLookup = "price_lookup"
	costFnPer1k       = "per_1k"
)

func runCost(in json.RawMessage, _ fs.FS) (any, error) {
	var head struct {
		Fn string `json:"fn"`
	}
	if err := json.Unmarshal(in, &head); err != nil {
		return nil, err
	}
	switch head.Fn {
	case costFnCallCost:
		var x struct {
			Fn string `json:"fn"`
			decision.CallCostInput
		}
		if err := strictUnmarshal(in, &x); err != nil {
			return nil, err
		}
		if err := x.Table.Validate(); err != nil {
			return nil, fmt.Errorf("table: %w", err)
		}
		return decision.PriceCall(x.CallCostInput), nil
	case costFnPriceLookup:
		var x struct {
			Fn string `json:"fn"`
			decision.PriceLookupInput
		}
		if err := strictUnmarshal(in, &x); err != nil {
			return nil, err
		}
		if err := checkLookupInput(x.PriceLookupInput); err != nil {
			return nil, err
		}
		return decision.LookupEffective(x.PriceLookupInput), nil
	case costFnPer1k:
		var x struct {
			Fn string `json:"fn"`
			decision.Per1kInput
		}
		if err := strictUnmarshal(in, &x); err != nil {
			return nil, err
		}
		if err := x.Table.Validate(); err != nil {
			return nil, fmt.Errorf("table: %w", err)
		}
		return decision.CostPer1k(x.Per1kInput)
	}
	return nil, fmt.Errorf("cost vector fn %q is not one of %s, %s, %s", head.Fn, costFnCallCost, costFnPriceLookup, costFnPer1k)
}

// checkLookupInput holds the price_lookup layers to pricing.v1.
func checkLookupInput(x decision.PriceLookupInput) error {
	if err := x.Contracts.Validate(); err != nil {
		return fmt.Errorf("contracts: %w", err)
	}
	if x.File != nil {
		if err := x.File.Validate(); err != nil {
			return fmt.Errorf("file: %w", err)
		}
	}
	for i, e := range x.Overrides {
		if field, problem := decision.CheckPriceEntry(e); field != "" {
			return fmt.Errorf("overrides[%d].%s %s", i, field, problem)
		}
	}
	return nil
}
