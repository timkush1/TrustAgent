package vectors

// lintKind is how the Go reference runs the lint vectors. It is a P4 Step-0 stub that keeps the
// kind skipped; GEVAL replaces it with a runner over internal/decision/lint.go, which ports
// frontend-react/src/lib/decision/lint.ts 1:1 (P4-PLAN D54), and the 19 lint vectors freeze once
// Go passes them.
var lintKind = Kind{Skip: "evaluator validation and lints land in the P2 decision package"}
