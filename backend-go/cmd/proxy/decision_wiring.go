package main

import (
	"context"
	"sync"

	"github.com/truthtable/backend-go/internal/policy"
)

// initCalibration sets s.calib, the calibration lookup the gate and the policy readiness checks
// read. For now no calibration is loaded: every evaluator is uncalibrated (the P3 file lookup
// reads TRUTHTABLE_CALIBRATION_DIR, GD-4).
func (s *server) initCalibration() {
	s.calib = policy.NoCalibration{}
}

// calibrationLookup is the lookup policyRuntime hands the policy engine.
func (s *server) calibrationLookup() policy.CalibrationLookup {
	if s.calib == nil {
		return policy.NoCalibration{}
	}
	return s.calib
}

// calibrationState is the calibration state of a decision-model snapshot for the WS
// decision_model_changed event: none until calibrations load.
func (s *server) calibrationState(snapshot string) string {
	return "none"
}

// startDecisionLoops starts the decision side's background loops on wg until ctx ends (the
// calibration reload poll and retention, GD-4 and GD-10). There are none yet.
func (s *server) startDecisionLoops(ctx context.Context, wg *sync.WaitGroup) {}
