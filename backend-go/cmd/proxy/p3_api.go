package main

import "github.com/gin-gonic/gin"

// mountP3API mounts the P3 REST endpoints (SPEC §2.4 rows 2, 16, 17, 49, 61 and 66): r for
// /health/ready, api for the /api routes, with guards' auth, limits and body cap.
func (s *server) mountP3API(r *gin.Engine, api *gin.RouterGroup, guards apiGuards) {
	// Public readiness (row 2): component states only, never the status aggregates.
	r.GET("/health/ready", s.handleReady)

	// Stored gate decisions (row 16); they need the database.
	gate := &gateAPI{log: s.log}
	if s.pg != nil {
		gate.store = s.pg
	}
	api.GET("/gate/decisions", guards.read, guards.rateLimit, gate.decisions)
}
