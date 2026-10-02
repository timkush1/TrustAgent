package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// handlePricing serves GET /api/pricing (SPEC §2.4 endpoint 49, P4-PLAN D53): the effective price
// table costs are computed with when a provider reports none (api.ts PricingTable). The
// dashboard's pricing overrides come first (label override; the first match wins), then the
// TRUTHTABLE_PRICE_TABLE_FILE table when one is set, then the embedded contracts table, each entry
// with its source, label and date. It is the price book's snapshot in force, the same table the
// ledger prices with and the engine is pushed.
func (s *server) handlePricing(c *gin.Context) {
	t := s.priceBook().Effective()
	if len(t.Entries) == 0 {
		s.log.Error("api: the price table is empty; the embedded table did not load")
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The price table could not be read; see the proxy log.")
		return
	}
	c.JSON(http.StatusOK, t)
}
