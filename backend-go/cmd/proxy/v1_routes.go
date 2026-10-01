package main

import (
	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/proxy"
)

// gatewayEnforces is whether the /v1 handler enforces block and fallback. The policy refuses those
// modes while it is off; P3 turned it on, so block and fallback routes load and are enforced.
const gatewayEnforces = true

// mountV1 mounts the LLM API (SPEC §2.12): every /v1 response carries the trace id; ingest auth
// reads X-TrustAgent-Key only, so Authorization stays the client's upstream credential (B1).
// proxy.Handler.Mount holds the route table: chat completions, completions and messages gated;
// count_tokens, models and embeddings passed through; the Responses API passed through ungated.
func (s *server) mountV1(r *gin.Engine, v1RateLimit, bodyLimit gin.HandlerFunc) {
	h := s.gateway.handler
	v1 := r.Group("/v1", h.TraceIDs(), s.gateway.auth.Ingest(proxy.Deny), v1RateLimit)
	h.Mount(v1, bodyLimit)
	// The decision of a trace, for clients of observe routes (decisions_longpoll_api.go).
	v1.GET("/trustagent/decisions/:trace_id", s.handleDecisionLongPoll)
}
