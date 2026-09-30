package proxy

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/upstream"
)

// flow is one gated /v1 exchange. Fields are append-only.
type flow struct {
	c      *gin.Context
	format string              // llmapi format id
	req    *llmapi.ChatRequest // nil when unparseable
	raw    []byte              // client body
	x      *gate.Exchange
	route  *policy.Route
	up     *upstream.Client
	start  time.Time
	record context.Context // context.WithoutCancel(request ctx)
}
