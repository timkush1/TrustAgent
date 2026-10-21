package studio

import (
	"math"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/decision"
)

func TestDraftPromptChars(t *testing.T) {
	base, err := DraftPromptChars(DraftInput{Description: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if base < 7000 {
		t.Errorf("the prompt is %d characters; the system text alone is over 7,000", base)
	}
	longer, _ := DraftPromptChars(DraftInput{Description: "x" + strings.Repeat("é", 99)})
	if longer-base != 99 {
		t.Errorf("99 more characters of description added %d", longer-base)
	}
	// An empty route takes "(no route selected)"; a named one replaces it.
	named, _ := DraftPromptChars(DraftInput{Description: "x", RouteName: "ab"})
	if base-named != len("(no route selected)")-2 {
		t.Errorf("route name: base %d named %d", base, named)
	}
	ids, _ := DraftPromptChars(DraftInput{Description: "x", ExistingIDs: []string{"groundedness", "refusal"}})
	if ids-base != len("groundedness, refusal")-len("(none)") {
		t.Errorf("existing ids: base %d with ids %d", base, ids)
	}
	// Each variable is cut at its max_chars (2,000 for the description).
	capped, _ := DraftPromptChars(DraftInput{Description: strings.Repeat("a", 5000)})
	if capped-base != MaxDraftDescription-1 {
		t.Errorf("a 5,000-character description counted %d more", capped-base)
	}
}

func TestDraftReserveUSD(t *testing.T) {
	got, err := DraftReserveUSD(4000, 4, decision.PriceEntry{InputPerMTok: 1, OutputPerMTok: 5})
	if err != nil {
		t.Fatal(err)
	}
	// 1,000 input tokens at $1/M plus 1,600 output tokens at $5/M.
	if want := (1000*1.0 + 1600*5.0) / 1e6; math.Abs(got-want) > 1e-15 {
		t.Errorf("reserve = %v, want %v", got, want)
	}
	if free, _ := DraftReserveUSD(4000, 0, decision.PriceEntry{}); free != 0 {
		t.Errorf("a free judge reserves %v", free)
	}
	if d, _ := DraftReserveUSD(4000, 0, decision.PriceEntry{InputPerMTok: 1}); d != 4000/decision.DefaultCPT/1e6 {
		t.Errorf("cpt 0 uses the default: %v", d)
	}
}
