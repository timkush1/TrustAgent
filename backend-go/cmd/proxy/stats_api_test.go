package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/store"
)

// outcomes_series keeps would_flag and would_block apart, so the Overview chart can draw both.
func TestOutcomeSeriesKeepsWouldBlockApart(t *testing.T) {
	at := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	got := outcomeSeries([]store.OutcomePoint{{T: at, Counts: map[string]int64{"passed": 5, "would_flag": 2, "would_block": 3, "blocked": 1}}})
	if len(got) != 1 || got[0].WouldFlag != 2 || got[0].WouldBlock != 3 || got[0].Passed != 5 || got[0].Blocked != 1 {
		t.Fatalf("series %+v", got)
	}
	b, _ := json.Marshal(got[0])
	if !strings.Contains(string(b), `"would_block":3`) || !strings.Contains(string(b), `"would_flag":2`) {
		t.Errorf("JSON %s", b)
	}
}
