package main

import (
	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/proxy"
)

// gatewayEnforces is whether the /v1 handler enforces block and fallback: the policy refuses
// those modes until it does (the P3 turn-on sets it).
const gatewayEnforces = false

// mountV1 mounts the LLM API (SPEC §2.12): every /v1 response carries the trace id; ingest auth
// reads X-TrustAgent-Key only, so Authorization stays the client's upstream credential (B1).
func (s *server) mountV1(r *gin.Engine, v1RateLimit, bodyLimit gin.HandlerFunc) {
	h := s.gateway.handler
	v1 := r.Group("/v1", h.TraceIDs(), s.gateway.auth.Ingest(proxy.Deny), v1RateLimit)
	v1.POST("/chat/completions", bodyLimit, h.ChatCompletions)
	// Proxied but not gated yet: completions are gated in P3; /v1/responses stays ungated.
	v1.POST("/completions", bodyLimit, h.Passthrough(proxy.StatusSkippedUnsupportedAPI))
	v1.Any("/responses", bodyLimit, h.Passthrough(proxy.StatusSkippedUnsupportedAPI))
	v1.Any("/responses/*rest", bodyLimit, h.Passthrough(proxy.StatusSkippedUnsupportedAPI))
	v1.Any("/models", h.Passthrough(""))
	v1.Any("/models/*model", h.Passthrough(""))
	v1.Any("/embeddings", bodyLimit, h.Passthrough(""))
	// The decision of a trace, for clients of observe routes (decisions_longpoll_api.go).
	v1.GET("/trustagent/decisions/:trace_id", s.handleDecisionLongPoll)
}
