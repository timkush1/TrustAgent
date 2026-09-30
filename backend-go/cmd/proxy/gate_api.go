package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/store"
)

// gateReader is the part of the store the gate-decision reads use (store.PostgresStore).
type gateReader interface {
	ListDecisions(ctx context.Context, f store.DecisionFilter) ([]store.DecisionRow, string, error)
}

// gateAPI serves GET /api/gate/decisions (SPEC §2.4 endpoint 16): the stored gate decisions, one
// row per trace. It reads the database directly, so without one it answers 503.
type gateAPI struct {
	store gateReader // nil without a database
	log   *slog.Logger
}

// Enum values of the /api/gate filters (api.ts GateStatus, EvaluatorLevel).
var (
	decisionStatuses = []string{"ok", "partial", "not_assessed", "skipped", "timeout", "error"}
	evaluatorLevels  = []string{"ok", "warn", "critical"}
)

// evaluatorParam is an evaluator ref ("<id>@<version>") or a bare evaluator id.
var evaluatorParam = regexp.MustCompile(`^[a-z][a-z0-9_]{1,47}(@[1-9][0-9]{0,8})?$`)

// available answers 503 persistence_disabled when there is no database.
func (g *gateAPI) available(c *gin.Context) bool {
	if g.store == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled,
			"Gate decisions are read from stored traces, and this proxy has no database (TRUTHTABLE_DATABASE_URL).")
		return false
	}
	return true
}

// decisions serves GET /api/gate/decisions.
func (g *gateAPI) decisions(c *gin.Context) {
	if !g.available(c) {
		return
	}
	f, ok := decisionFilter(c)
	if !ok {
		return
	}
	rows, next, err := g.store.ListDecisions(c.Request.Context(), f)
	if errors.Is(err, store.ErrBadCursor) {
		badParam(c, "cursor", "is not a cursor this server issued")
		return
	}
	if err != nil {
		g.internal(c, "list gate decisions", err)
		return
	}
	c.JSON(http.StatusOK, newPage(rows, next))
}

// decisionFilter parses the query of GET /api/gate/decisions.
func decisionFilter(c *gin.Context) (store.DecisionFilter, bool) {
	var f store.DecisionFilter
	var ok bool
	if f.Limit, ok = queryLimit(c); !ok {
		return f, false
	}
	f.Cursor = c.Query("cursor")
	if f.Since, ok = queryTime(c, "since"); !ok {
		return f, false
	}
	if f.UpdatedSince, ok = queryTime(c, "updated_since"); !ok {
		return f, false
	}
	enums := []struct {
		name    string
		dst     *string
		allowed []string
	}{
		{"action", &f.Action, gateActions},
		{"status", &f.Status, decisionStatuses},
		{"level", &f.Level, evaluatorLevels},
	}
	for _, e := range enums {
		if *e.dst, ok = queryEnum(c, e.name, e.allowed...); !ok {
			return f, false
		}
	}
	f.Route = c.Query("route")
	if f.Evaluator = c.Query("evaluator"); f.Evaluator != "" && !evaluatorParam.MatchString(f.Evaluator) {
		badParam(c, "evaluator", "must be an evaluator id or ref, such as groundedness or groundedness@1")
		return f, false
	}
	if f.Disagreement, ok = queryBool(c, "disagreement"); !ok {
		return f, false
	}
	if f.Late, ok = queryBool(c, "late"); !ok {
		return f, false
	}
	return f, true
}

// queryBool reads a true/false parameter; nil when absent.
func queryBool(c *gin.Context, name string) (*bool, bool) {
	raw := c.Query(name)
	if raw == "" {
		return nil, true
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		badParam(c, name, "must be true or false")
		return nil, false
	}
	return &b, true
}

func (g *gateAPI) internal(c *gin.Context, what string, err error) {
	g.log.Error("api: "+what+" failed", "path", c.FullPath(), "err", err)
	abortAPI(c, http.StatusInternalServerError, codeInternal, "The request could not be completed; see the proxy log.")
}
