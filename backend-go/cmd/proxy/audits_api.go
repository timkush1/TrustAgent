package main

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/truthtable/backend-go/internal/store"
)

// parseListFilter extracts pagination and filter params for GET /api/audits.
// Supported: ?limit= (1-200, default 50), ?offset=, ?grade=A..F, ?flagged=true|false
func parseListFilter(c *gin.Context) (store.ListFilter, error) {
	filter := store.ListFilter{Limit: 50}

	if raw := c.Query("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			return filter, errors.New("'limit' must be an integer between 1 and 200")
		}
		filter.Limit = limit
	}
	if raw := c.Query("offset"); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return filter, errors.New("'offset' must be a non-negative integer")
		}
		filter.Offset = offset
	}
	if raw := c.Query("grade"); raw != "" {
		switch raw {
		case "A", "B", "C", "D", "F":
			filter.Grade = raw
		default:
			return filter, errors.New("'grade' must be one of A, B, C, D, F")
		}
	}
	if raw := c.Query("flagged"); raw != "" {
		flagged, err := strconv.ParseBool(raw)
		if err != nil {
			return filter, errors.New("'flagged' must be true or false")
		}
		filter.Flagged = &flagged
	}
	return filter, nil
}

// handleListAudits returns paginated audit history.
func handleListAudits(auditStore store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		if auditStore == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "Audit persistence is not configured (set TRUTHTABLE_DATABASE_URL)",
			})
			return
		}

		filter, err := parseListFilter(c)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		audits, total, err := auditStore.ListAudits(c.Request.Context(), filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list audits"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"audits": audits,
			"total":  total,
			"limit":  filter.Limit,
			"offset": filter.Offset,
		})
	}
}

// handleGetAudit returns one audit with its claim verifications.
func handleGetAudit(auditStore store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		if auditStore == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "Audit persistence is not configured (set TRUTHTABLE_DATABASE_URL)",
			})
			return
		}

		record, err := auditStore.GetAudit(c.Request.Context(), c.Param("id"))
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Audit not found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load audit"})
			return
		}

		c.JSON(http.StatusOK, record)
	}
}
