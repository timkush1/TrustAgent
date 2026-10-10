//go:build !race

package gate_test

// raceEnabled is set under -race, whose instrumentation makes timings meaningless.
const raceEnabled = false
