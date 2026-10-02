package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/store"
)

// adminAuditLister reads the admin audit log (store.PostgresStore).
type adminAuditLister interface {
	ListAdminAudit(ctx context.Context, limit int, cursor string) ([]store.AdminAuditRecord, string, error)
}

// adminAuditPage is Page<AdminAuditEntry>.
type adminAuditPage struct {
	Items      []store.AdminAuditRecord `json:"items"`
	NextCursor *string                  `json:"next_cursor"`
}

// handleAdminAuditLog serves GET /api/admin/audit-log (SPEC §2.4 row 70, admin): who changed
// which setting, key or credential, newest first, keyset-paged by limit and cursor. Rows never
// hold secrets (D44). Without a database there is no log: 503 persistence_disabled.
func (s *server) handleAdminAuditLog(c *gin.Context) {
	var lister adminAuditLister
	if s.pg != nil {
		lister = s.pg
	}
	if lister == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled,
			"The admin audit log needs a database (TRUTHTABLE_DATABASE_URL).")
		return
	}
	limit, ok := queryLimit(c)
	if !ok {
		return
	}
	items, next, err := lister.ListAdminAudit(c.Request.Context(), limit, c.Query("cursor"))
	if errors.Is(err, store.ErrBadCursor) {
		badParam(c, "cursor", "is not a cursor this server issued")
		return
	}
	if err != nil {
		s.log.Error("list the admin audit log failed", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The request failed; see the proxy log.")
		return
	}
	p := adminAuditPage{Items: items}
	if next != "" {
		p.NextCursor = &next
	}
	c.JSON(http.StatusOK, p)
}
