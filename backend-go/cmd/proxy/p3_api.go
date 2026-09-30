package main

import "github.com/gin-gonic/gin"

// mountP3API mounts the P3 REST endpoints (SPEC §2.4 rows 2, 16, 17, 49, 61 and 66): r for
// /health/ready, api for the /api routes, with guards' auth, limits and body cap. None yet.
func (s *server) mountP3API(r *gin.Engine, api *gin.RouterGroup, guards apiGuards) {}
