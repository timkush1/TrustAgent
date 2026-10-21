package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/calibration"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/metrics"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/websocket"
)

// calibrationReload is how often the decision loops re-read the calibration directory (D14);
// tests shorten it.
var calibrationReload = calibration.ReloadInterval

// initCalibration sets s.calib, the calibration lookup of the gate and the policy readiness
// checks: the files of TRUTHTABLE_CALIBRATION_DIR (SPEC §2.11, D14) and, with a database, the
// calibrations table they are mirrored into, whose active rows then apply (D55). It is read once
// here and re-read by the decision loops (the poll, and the "calibrations:" notifications
// eval_wiring.go subscribes to). The status reporter shows the current snapshot's state from it.
func (s *server) initCalibration() {
	dir, family := calibrationSettings(s.cfg)
	cfg := calibration.Config{
		Dir: dir, FamilyFallback: family, AllowMock: allowMockCalibration(s.cfg),
		Specs: s.evaluatorSpecs, Current: s.currentSnapshot, Logger: s.log,
	}
	if s.pg != nil {
		cfg.DB = s.pg
	}
	st := calibration.New(cfg)
	changes, err := st.Reload()
	switch {
	case err != nil:
		s.log.Warn("calibration: the calibrations cannot be read; every evaluator is uncalibrated until they can", "dir", dir, "err", err)
	case len(changes) == 0:
		s.log.Info("calibration: no calibration files", "dir", dir)
	}
	for _, c := range changes {
		s.log.Info("calibration: loaded", "snapshot", c.Snapshot, "targets", c.Imported, "dir", dir)
	}
	s.calib = st
	if s.status != nil {
		s.status.calib = st
	}
}

// calibrationSettings are TRUTHTABLE_CALIBRATION_DIR and TRUTHTABLE_CALIBRATION_FAMILY_FALLBACK as
// the configuration loaded them. The loader resolves a relative directory that does not exist from
// the working directory against the repository root (a repopath key), so a host run from
// backend-go finds config/calibration. A configuration built without the loader has none.
func calibrationSettings(cfg *config.Config) (dir string, familyFallback bool) {
	return cfg.CalibrationDir, cfg.CalibrationFamilyFallback
}

// allowMockCalibration reports whether mock calibrations load: only where the mock decision model
// runs for the zero-key demo (DEMO_MODE forces it) or a test stack (SPEC §2.11 quality mock).
func allowMockCalibration(cfg *config.Config) bool {
	return cfg.DemoMode || (cfg.TestMode && strings.TrimSpace(cfg.Jev.Backend) == jev.BackendMock)
}

// evaluatorSpecs is the live evaluator registry, nil until the policy services load it.
func (s *server) evaluatorSpecs() policy.Resolver {
	if s.policies == nil || s.policies.Registry == nil {
		return nil
	}
	return s.policies.Registry
}

// currentSnapshot is the snapshot the decision model reports: "" before the gateway is built and,
// for an HTTP backend, before its first answer.
func (s *server) currentSnapshot() string {
	if s.gateway == nil || s.status == nil {
		return ""
	}
	return s.status.jevStatus().Snapshot
}

// calibrationLookup is the lookup policyRuntime hands the policy engine.
func (s *server) calibrationLookup() policy.CalibrationLookup {
	if s.calib == nil {
		return policy.NoCalibration{}
	}
	return s.calib
}

// calibrationState is the calibration state of a decision-model snapshot for the WS
// decision_model_changed event. It runs on a decision-model attempt goroutine, so it never asks
// the client for the current snapshot.
func (s *server) calibrationState(snapshot string) string {
	st, ok := s.calib.(*calibration.Store)
	if !ok || snapshot == "" {
		return policy.CalibrationNone
	}
	state, _ := st.State(snapshot)
	return state
}

// startDecisionLoops starts the decision side's background loops on wg until ctx ends: the
// calibration reload poll, which broadcasts calibration_updated for each snapshot whose
// calibrations changed, and, with a database, the retention job (D17).
func (s *server) startDecisionLoops(ctx context.Context, wg *sync.WaitGroup) {
	if st, ok := s.calib.(*calibration.Store); ok {
		every := calibrationReload
		wg.Add(1)
		go func() {
			defer wg.Done()
			st.Watch(ctx, every, s.calibrationUpdated)
		}()
	}
	if s.pg != nil {
		first, every := retentionFirst, retentionEvery
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.retentionLoop(ctx, first, every)
		}()
	}
}

// The retention job's first run comes shortly after start, then one every hour (SPEC §2.3); tests
// shorten both.
var (
	retentionFirst = time.Minute
	retentionEvery = time.Hour
)

// retentionLoop runs the retention job until ctx ends.
func (s *server) retentionLoop(ctx context.Context, first, every time.Duration) {
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.runRetention(ctx)
		t.Reset(every)
	}
}

// runRetention runs every retention rule once (store.RunRetention) and reports how far the job is
// behind in retention_lag_seconds.
func (s *server) runRetention(ctx context.Context) {
	start := time.Now()
	rep, err := s.pg.RunRetention(ctx, store.RetentionConfig{Days: s.cfg.RetentionDays, CostDays: s.cfg.CostRetentionDays})
	if ctx.Err() != nil {
		return
	}
	metrics.SetRetentionLag(rep.Lag)
	if err != nil {
		s.log.Warn("retention: a rule did not finish; the next run continues", "err", err, "lag", rep.Lag.Round(time.Second))
	}
	var n int64
	for _, rows := range rep.Rows {
		n += rows
	}
	if n > 0 {
		s.log.Info("retention: removed expired data", "rows", rep.Rows, "lag", rep.Lag.Round(time.Second),
			"took_ms", time.Since(start).Milliseconds())
	}
}

// calibrationUpdated broadcasts WS calibration_updated {snapshot, imported} per changed snapshot.
func (s *server) calibrationUpdated(changes []calibration.Change) {
	for _, c := range changes {
		ev, err := websocket.NewEvent("calibration_updated", "", map[string]any{"snapshot": c.Snapshot, "imported": c.Imported}, nil)
		if err != nil {
			s.log.Error("ws: calibration_updated does not marshal", "snapshot", c.Snapshot, "err", err)
			continue
		}
		s.hub.Publish(ev)
	}
}
