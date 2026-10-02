package main

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/decision"
)

// handlePricing serves GET /api/pricing (SPEC §2.4 endpoint 49): the price table costs are
// computed with when a provider reports none (api.ts PricingTable). Each entry carries its source,
// label and date. The table is contracts/pricing/prices.v1.json, decoded strictly
// (decision.ParsePriceTable): a table that is not valid pricing.v1 is an error rather than a
// partial answer.
func (s *server) handlePricing(c *gin.Context) {
	t, err := decision.ContractsPrices()
	if err != nil {
		s.log.Error("api: the price table does not load", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The price table could not be read; see the proxy log.")
		return
	}
	c.JSON(http.StatusOK, t)
}
