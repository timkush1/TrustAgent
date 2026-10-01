package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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

// handleFileUpload is POST /api/kb/documents over the server's engine (SPEC §2.4 row 65). At P4
// it also books the ingest cost and refuses uploads past the LLM cap (D48, GCOST).
func (s *server) handleFileUpload(maxUploadBytes int64) gin.HandlerFunc {
	return fileUpload(s.engine, maxUploadBytes)
}

// fileUpload returns a handler that accepts JSON document uploads for RAG ingestion.
func fileUpload(auditClient *grpc.AuditClient, maxUploadBytes int64) gin.HandlerFunc {
	type uploadDocument struct {
		Content  string            `json:"content"`
		Metadata map[string]string `json:"metadata"`
	}
	const maxDocuments = 1000
	const maxDocumentChars = 50000

	return func(c *gin.Context) {
		if auditClient == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Audit engine not connected"})
			return
		}

		file, header, err := c.Request.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "No file provided. Use multipart form field 'file'"})
			return
		}
		defer file.Close()

		if header.Size > maxUploadBytes {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": fmt.Sprintf("File too large (max %d bytes)", maxUploadBytes),
			})
			return
		}

		data, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read file"})
			return
		}
		if int64(len(data)) > maxUploadBytes {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": fmt.Sprintf("File too large (max %d bytes)", maxUploadBytes),
			})
			return
		}

		var docs []uploadDocument
		if err := json.Unmarshal(data, &docs); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON. Expected array of {\"content\": \"...\", \"metadata\": {...}}"})
			return
		}

		if len(docs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "No documents in file"})
			return
		}
		if len(docs) > maxDocuments {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": fmt.Sprintf("Too many documents (max %d per upload)", maxDocuments),
			})
			return
		}
		for i, doc := range docs {
			if len(doc.Content) == 0 {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("Document %d has empty 'content'", i),
				})
				return
			}
			if len(doc.Content) > maxDocumentChars {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{
					"error": fmt.Sprintf("Document %d exceeds %d characters", i, maxDocumentChars),
				})
				return
			}
		}

		// Convert to gRPC IngestDocument format
		ingestDocs := make([]grpc.IngestDocument, len(docs))
		for i, doc := range docs {
			ingestDocs[i] = grpc.IngestDocument{
				Content:  doc.Content,
				Metadata: doc.Metadata,
			}
		}

		result, err := auditClient.IngestDocuments(c.Request.Context(), ingestDocs)
		if err != nil {
			log.Printf("Document ingestion failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to ingest documents"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"documents_ingested": result.DocumentsIngested,
			"claims_accepted":    result.ClaimsAccepted,
			"claims_quarantined": result.ClaimsQuarantined,
			"conflicts_detected": result.ConflictsDetected,
			"claim_results":      result.ClaimResults,
			"status":             "success",
		})
	}
}
