package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/costs"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/store"
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

// componentKBIngest is the component a knowledge ingest's LLM spend is booked under.
const componentKBIngest = "kb_ingest"

// kbIngester ingests uploaded documents into the knowledge base (grpc.AuditClient).
type kbIngester interface {
	IngestDocuments(ctx context.Context, documents []grpc.IngestDocument) (*grpc.IngestResult, error)
}

// ingestCostReporter is an ingest result that carries the engine's LLM cost records
// (IngestResponse.costs).
type ingestCostReporter interface {
	CostRecords() []*pb.CostRecord
}

// ingestCosts are the LLM cost records of an ingest: none when the result does not report them.
func ingestCosts(res *grpc.IngestResult) []*pb.CostRecord {
	if r, ok := any(res).(ingestCostReporter); ok {
		return r.CostRecords()
	}
	return nil
}

// kbUploadHooks connect the upload to the LLM cap and the admin audit log; nil fields do nothing.
type kbUploadHooks struct {
	// reserve asks whether the day's LLM cap is spent (a hold of $0: an ingest's cost is not
	// known before it runs, D48) and returns the settle function of the booking.
	reserve func(ctx context.Context) (settle func(actualUSD float64), err error)
	// record writes the ingest's cost rows, linked to the uploader's key label.
	record func(ctx context.Context, records []*pb.CostRecord, keyLabel string)
	// audit appends the admin_audit_log row of the upload.
	audit func(ctx context.Context, e store.AdminAuditEntry) error
	log   *slog.Logger
}

// handleFileUpload is POST /api/kb/documents over the server's engine (SPEC §2.4 row 65). It is
// refused with 503 budget_exhausted while the day's LLM cap is spent, books the ingest's LLM cost
// afterwards (it may pass the cap by one ingest, D48) and writes the admin audit row kb.upload.
func (s *server) handleFileUpload(maxUploadBytes int64) gin.HandlerFunc {
	var engine kbIngester
	if s.engine != nil {
		engine = s.engine
	}
	hooks := kbUploadHooks{
		reserve: func(ctx context.Context) (func(float64), error) { return s.costs.reserve(ctx, 0, componentKBIngest) },
		record: func(ctx context.Context, records []*pb.CostRecord, keyLabel string) {
			s.costs.record(ctx, records, CostLink{KeyLabel: keyLabel})
		},
		log: s.log,
	}
	if s.pg != nil {
		hooks.audit = s.pg.AppendAdminAudit
	}
	return fileUpload(engine, maxUploadBytes, hooks)
}

// fileUpload returns a handler that accepts JSON document uploads for knowledge ingestion.
func fileUpload(engine kbIngester, maxUploadBytes int64, hooks kbUploadHooks) gin.HandlerFunc {
	type uploadDocument struct {
		Content  string            `json:"content"`
		Metadata map[string]string `json:"metadata"`
	}
	const maxDocuments = 1000
	const maxDocumentChars = 50000
	logger := hooks.log
	if logger == nil {
		logger = slog.Default()
	}

	return func(c *gin.Context) {
		if engine == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Audit engine not connected"})
			return
		}
		settle := func(float64) {}
		if hooks.reserve != nil {
			s, err := hooks.reserve(c.Request.Context())
			if err != nil {
				abortAPIDetails(c, http.StatusServiceUnavailable, codeBudgetExhausted,
					"Today's deep-audit budget is used up, and adding knowledge needs the judge: uploads start again at midnight UTC, or as soon as the budget is raised.",
					map[string]any{"cap": "llm"})
				return
			}
			settle = s
		}
		booked := false
		defer func() {
			if !booked {
				settle(0)
			}
		}()

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

		result, err := engine.IngestDocuments(c.Request.Context(), ingestDocs)
		if err != nil {
			logger.Error("knowledge: document ingestion failed", "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to ingest documents"})
			return
		}

		// Book the ingest's LLM cost now that it is known (simulated and unpriced calls count $0).
		records := ingestCosts(result)
		booked = true
		settle(costs.RealCostUSD(records))
		actor := ""
		if id := callerIdentity(c); id != nil {
			actor = id.Label
		}
		ctx := context.WithoutCancel(c.Request.Context())
		if hooks.record != nil && len(records) > 0 {
			hooks.record(ctx, records, actor)
		}
		if hooks.audit != nil {
			if err := hooks.audit(ctx, store.AdminAuditEntry{Actor: actor, Action: "kb.upload", Target: header.Filename,
				Diff: map[string]any{
					"documents_ingested": result.DocumentsIngested, "claims_accepted": result.ClaimsAccepted,
					"claims_quarantined": result.ClaimsQuarantined, "conflicts_detected": result.ConflictsDetected,
				}}); err != nil {
				logger.Error("knowledge: the admin audit row of an upload was not written", "err", err)
			}
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
