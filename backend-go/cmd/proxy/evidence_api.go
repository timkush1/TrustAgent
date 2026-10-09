package main

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
)

// defaultSuggestWindow is the suggestions' window when the request gives none.
const defaultSuggestWindow = "7d"

// GET /api/evidence/suggestions (SPEC §2.4 row 51, §7.1; D64): a free scan of the 500
// most recent answers in the window (on one route, or all) that were checked against no evidence.
// It suggests route evidence.tag_patterns for the document shapes it finds, each with how many of
// those requests would have carried evidence, and the trustagent.context extension. Admin only:
// it reads request bodies.
func (a *studioAPI) suggestions(c *gin.Context) {
	window, ok := policy.ParseWindow(c.DefaultQuery("window", defaultSuggestWindow))
	if !ok {
		badParam(c, "window", "must be one of 15m, 1h, 24h, 7d, 30d")
		return
	}
	route := c.Query("route")
	if a.db == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled, "Suggestions come from stored traffic, and this gateway has no database.")
		return
	}
	now := a.now().UTC()
	rows, err := a.db.NoEvidenceBodies(c.Request.Context(), store.ScanFilter{
		Route: route, Since: now.Add(-window), Limit: evidence.MaxSuggestScan, Now: now,
	})
	if err != nil {
		a.log.Error("evidence suggestions: scan failed", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The recent traffic could not be read.")
		return
	}
	bodies := make([]evidence.ScanBody, 0, len(rows))
	for _, r := range rows {
		var msgs []evidence.ScanMessage
		if err := json.Unmarshal(r.Messages, &msgs); err != nil {
			continue // a body without readable messages has nothing to scan
		}
		bodies = append(bodies, evidence.ScanBody{TraceID: r.TraceID, Route: r.Route, Messages: msgs})
	}
	c.JSON(http.StatusOK, gin.H{"items": evidence.Suggest(bodies, a.evidenceSettings())})
}

// evidenceSettings returns the evidence settings of a route of the active policy; a route that is
// gone takes the defaults route's.
func (a *studioAPI) evidenceSettings() func(route string) evidence.Settings {
	p := a.policy()
	return func(route string) evidence.Settings {
		if p == nil {
			return evidence.DefaultSettings()
		}
		r, ok := p.Route(route)
		if !ok || r == nil {
			r = p.Default
		}
		if r == nil {
			return evidence.DefaultSettings()
		}
		return r.Evidence.Extraction()
	}
}
