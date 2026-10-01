package vectors

// costKind is how the Go reference runs the cost vectors. It is a P4 Step-0 stub that keeps the
// kind skipped; GCOST replaces it with a runner over the pure calculator in
// internal/decision/cost.go for every input fn (call_cost, per_1k, price_lookup; P4-PLAN D85).
// cmd/vectorgen fills in expected, so the kind is mechanical.
var costKind = Kind{Mechanical: true, Skip: "the cost calculator lands with internal/costs in P4"}
