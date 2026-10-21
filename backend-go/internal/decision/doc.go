// Package decision is the reference implementation of the TrustAgent decision core (SPEC §2.9):
// canonical JSON (RFC 8785 with the §2.1 additions), token estimates, the question-set compiler,
// answer normalization, calibration apply and decide().
//
// The package is contract-owned. frontend-react/src/lib/decision is a TypeScript port of it, and
// both are held to the same behaviour by the golden vectors in contracts/vectors (SPEC §7.6).
// Go is the reference: cmd/vectorgen generates the expected values of the mechanical vector kinds
// from this package. A change here that alters any output is a contract change and needs a new
// vector (frozen vectors never change).
//
// Everything in the package is pure and deterministic: no clocks, no randomness, no I/O. Floats
// follow SPEC §2.1: probabilities are summed in sorted-key order, thresholds compare with EPS, and
// language round() is never used (see R2).
//
// Scope: canonical JSON, tokens, question hashes, spec parsing, compile, normalize, calibration
// apply and fitting (calibrate*.go: isotonic, Platt, Wilson bounds, the calibration.v1 file
// checks), decide() and the Tier-0 detectors (their patterns are read from the embedded contracts
// rules files), and evaluator validation (lint.go: the evaluator.v1 and evaluator-draft.v1
// schemas, the hard rules and the lints). Evidence extraction lives in internal/evidence.
package decision
