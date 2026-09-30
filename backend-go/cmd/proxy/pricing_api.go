package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/contracts"
)

// priceTable is api.ts PricingTable: contracts/pricing/prices.v1.json (schema pricing.v1), the
// table the decision model's cost fallback (internal/jev) and the ROI counterfactuals read.
type priceTable struct {
	Version  string       `json:"version"`
	Currency string       `json:"currency"`
	Entries  []priceEntry `json:"entries"`
}

// priceEntry is api.ts PriceEntry.
type priceEntry struct {
	Match         string  `json:"match"`
	Provider      string  `json:"provider"`
	InputPerMTok  float64 `json:"input_per_mtok"`
	OutputPerMTok float64 `json:"output_per_mtok"`
	Source        string  `json:"source,omitempty"`
	Label         string  `json:"label,omitempty"`
	AsOf          string  `json:"as_of,omitempty"`
	Note          string  `json:"note,omitempty"`
}

// prices reads the embedded price table once; a table that does not decode as a PricingTable
// (contracts-check validates the file) is an error rather than a partial answer.
var prices = sync.OnceValues(func() (priceTable, error) {
	var t priceTable
	b, err := contracts.FS.ReadFile("pricing/prices.v1.json")
	if err != nil {
		return t, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return t, fmt.Errorf("pricing/prices.v1.json: %w", err)
	}
	if t.Version == "" || t.Currency != "USD" || len(t.Entries) == 0 {
		return t, errors.New("pricing/prices.v1.json: version, currency USD and entries are required")
	}
	return t, nil
})

// handlePricing serves GET /api/pricing (SPEC §2.4 endpoint 49): the price table costs are
// computed with when a provider reports none. Each entry carries its source, label and date.
func (s *server) handlePricing(c *gin.Context) {
	t, err := prices()
	if err != nil {
		s.log.Error("api: the price table does not load", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The price table could not be read; see the proxy log.")
		return
	}
	c.JSON(http.StatusOK, t)
}
