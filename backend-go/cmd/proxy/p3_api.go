package main

import (
	"time"

	"github.com/gin-gonic/gin"
)

// mountP3API mounts the P3 REST endpoints (SPEC §2.4 rows 2, 16, 17, 49 and 66; the Playground,
// row 61, follows once the gateway's enforcement lands): r for /health/ready, api for the /api
// routes, with guards' auth, limits and body cap. It also wires the direct-audit judge check (row
// 15).
func (s *server) mountP3API(r *gin.Engine, api *gin.RouterGroup, guards apiGuards) {
	// Public readiness (row 2): component states only, never the status aggregates.
	r.GET("/health/ready", s.handleReady)

	// POST /api/audit (row 15, mounted with the audit reads) refuses audits the engine cannot
	// judge: 503 no_judge.
	s.pool.SetJudgeCheck(s.judgeCheck)

	// Stored gate decisions and reliability (rows 16 and 17); they need the database.
	gate := &gateAPI{
		snapshot: func() string { return s.status.jevStatus().Snapshot }, includeMock: s.cfg.DemoMode,
		now: time.Now, log: s.log,
	}
	if s.pg != nil {
		gate.store = s.pg
	}
	api.GET("/gate/decisions", guards.read, guards.rateLimit, gate.decisions)
	api.GET("/gate/reliability", guards.read, guards.rateLimit, gate.reliability)

	// The price table (row 49).
	api.GET("/pricing", guards.read, guards.rateLimit, s.handlePricing)

	// The recording manifest of stored traffic (row 66, admin).
	api.GET("/export/replay", guards.admin, guards.rateLimit, s.handleExportReplay)
}
