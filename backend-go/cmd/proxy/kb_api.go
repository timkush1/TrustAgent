package main

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/truthtable/backend-go/internal/grpc"
)

// parseKBListParams extracts ?limit=, ?offset=, ?status= for /api/kb/claims.
func parseKBListParams(c *gin.Context) (limit, offset int, status string, err error) {
	limit = 50
	if raw := c.Query("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			return 0, 0, "", errors.New("'limit' must be an integer between 1 and 200")
		}
	}
	if raw := c.Query("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return 0, 0, "", errors.New("'offset' must be a non-negative integer")
		}
	}
	status = c.Query("status")
	switch status {
	case "", "accepted", "quarantined":
	default:
		return 0, 0, "", errors.New("'status' must be 'accepted' or 'quarantined'")
	}
	return limit, offset, status, nil
}

func requireEngine(c *gin.Context, auditClient *grpc.AuditClient) bool {
	if auditClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Audit engine not connected"})
		return false
	}
	return true
}

// handleListKBClaims returns paginated knowledge-base claims.
func handleListKBClaims(auditClient *grpc.AuditClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEngine(c, auditClient) {
			return
		}

		limit, offset, status, err := parseKBListParams(c)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		claims, total, err := auditClient.ListKBClaims(c.Request.Context(), limit, offset, status)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to list knowledge-base claims"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"claims": claims,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		})
	}
}

// handleListConflicts returns contradiction pairs detected at ingest time.
func handleListConflicts(auditClient *grpc.AuditClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEngine(c, auditClient) {
			return
		}

		limit := 50
		if raw := c.Query("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 200 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "'limit' must be an integer between 1 and 200"})
				return
			}
			limit = parsed
		}

		conflicts, total, err := auditClient.ListConflicts(c.Request.Context(), limit)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to list conflicts"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"conflicts": conflicts, "total": total})
	}
}

// handleKBStats returns knowledge-base counters.
func handleKBStats(auditClient *grpc.AuditClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEngine(c, auditClient) {
			return
		}

		stats, err := auditClient.GetKBStats(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to load knowledge-base stats"})
			return
		}

		c.JSON(http.StatusOK, stats)
	}
}
