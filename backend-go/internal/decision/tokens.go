package decision

import (
	"errors"
	"fmt"
	"math"
	"unicode/utf8"
)

// Token estimate constants (SPEC §2.9 Tokens, §2.7 TRUTHTABLE_JEV_CHARS_PER_TOKEN).
const (
	// Overhead is the fixed per-request token overhead added to every estimate (OVERHEAD).
	Overhead = 275
	// DefaultCPT is the default characters-per-token ratio.
	DefaultCPT = 4.0
	// MinCPT and MaxCPT bound the runtime cpt estimate.
	MinCPT = 2.5
	MaxCPT = 6.0
	// wideFrom is the first code point counted as a whole token (CJK and above).
	wideFrom = 0x3000
)

// errBadCPT is returned for a characters-per-token ratio that is not a positive finite number.
var errBadCPT = errors.New("decision: cpt must be a positive finite number")

// TokenCount is the breakdown of an estimate: the code points of the canonical JSON, split into
// narrow (below U+3000) and wide ones, and the resulting estimate.
type TokenCount struct {
	Narrow int `json:"narrow"`
	Wide   int `json:"wide"`
	Est    int `json:"est"`
}

// Est returns est(v) = ceil(n_narrow/cpt + n_wide) over the code points of the canonical JSON of v
// (SPEC §2.9). A string is estimated with its quotes, as its canonical form carries them.
func Est(v any, cpt float64) (int, error) {
	c, err := CountTokens(v, cpt)
	return c.Est, err
}

// CountTokens is Est with the narrow and wide code point counts.
func CountTokens(v any, cpt float64) (TokenCount, error) {
	if !(cpt > 0) || math.IsInf(cpt, 0) {
		return TokenCount{}, errBadCPT
	}
	b, err := Canonicalize(v)
	if err != nil {
		return TokenCount{}, err
	}
	return countCanonical(b, cpt), nil
}

// estString is est() of a string value; it cannot fail for a valid cpt.
func estString(s string, cpt float64) int {
	b, _ := Canonicalize(s)
	return countCanonical(b, cpt).Est
}

func countCanonical(b []byte, cpt float64) TokenCount {
	var c TokenCount
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		i += size
		if r >= wideFrom {
			c.Wide++
		} else {
			c.Narrow++
		}
	}
	c.Est = int(math.Ceil(float64(c.Narrow)/cpt + float64(c.Wide)))
	return c
}

// UpdateCPT is the runtime characters-per-token estimate (SPEC §2.9, Go only):
// clamp(0.8*cpt + 0.2*(charsSent/(inputTokens-Overhead)), MinCPT, MaxCPT). charsSent is the number
// of code points sent and inputTokens the vendor-reported input tokens. An observation with
// inputTokens <= Overhead carries no information and leaves cpt unchanged.
func UpdateCPT(cpt float64, charsSent, inputTokens int) float64 {
	if inputTokens <= Overhead || charsSent <= 0 {
		return cpt
	}
	next := 0.8*cpt + 0.2*(float64(charsSent)/float64(inputTokens-Overhead))
	return math.Min(MaxCPT, math.Max(MinCPT, next))
}

// validCPT reports an error unless cpt is usable by Est.
func validCPT(cpt float64) error {
	if !(cpt > 0) || math.IsInf(cpt, 0) {
		return fmt.Errorf("%w: %v", errBadCPT, cpt)
	}
	return nil
}
