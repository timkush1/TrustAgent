package decision

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The contract's four date patterns all scan in windows: each needs its year and is bounded.
func TestDatePatternsScanInWindows(t *testing.T) {
	r, err := loadDetectorRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.dates) == 0 {
		t.Fatal("no date patterns")
	}
	for i, p := range r.dates {
		if p.window <= 0 || p.window > 128 {
			t.Errorf("date pattern %d: window %d, want a bounded window", i, p.window)
		}
	}
}

// dateWindow refuses what a window could change: an unbounded part, a text or line anchor, an
// optional or alternative year, or a year that is not exactly four digits.
func TestDateWindowRefuses(t *testing.T) {
	for _, src := range []string{
		`\b([A-Z]+) ([0-9]{4})\b`,          // unbounded month
		`^(May) ([0-9]{4})\b`,              // anchor
		`(May) ([0-9]{4})$`,                // anchor
		`(May)(?: ([0-9]{4}))?\b`,          // optional year
		`(May) (?:([0-9]{4})|now)\b`,       // alternative year
		`(May) ([0-9]{2,4})\b`,             // not exactly four digits
		`(May) ([0-9a]{4})\b`,              // not only digits
		`(May) .{0,3}([0-9]{4})`,           // bounded, kept: '.' counts four bytes
		`(?i)\b(May|June)\.? ([0-9]{4})\b`, // kept
	} {
		got := dateWindow(src, 2)
		kept := strings.HasPrefix(src, `(May) .`) || strings.HasPrefix(src, `(?i)`)
		if kept != (got > 0) {
			t.Errorf("dateWindow(%q) = %d, kept %v", src, got, kept)
		}
	}
	if got := dateWindow(`\b(May|June)\.? ([0-9]{4})\b`, 2); got != len("June. 2026") {
		t.Errorf("window = %d, want %d", got, len("June. 2026"))
	}
	// Folded, "June" can be no longer than written, but "Kay" (May's neighbour with a K) and "Sun"
	// can: the Kelvin sign is three bytes and ſ two.
	if got := dateWindow(`(?i)\b(May|June)\.? ([0-9]{4})\b`, 2); got != len("June. 2026") {
		t.Errorf("folded window = %d, want %d", got, len("June. 2026"))
	}
	if got := dateWindow(`(?i)\b(Kay|Sun)\.? ([0-9]{4})\b`, 2); got != len("\u212aay. 2026") {
		t.Errorf("folded window = %d, want %d (the Kelvin sign)", got, len("\u212aay. 2026"))
	}
}

// findAll in windows returns exactly what the whole-text scan returns, on random texts built from
// pieces that sit near the edges: month names in either case and their long folds (ſ, K), digit
// runs of every length, word characters next to the window edges, multi-byte runes and invalid
// UTF-8.
func TestDateWindowScanMatchesWholeScan(t *testing.T) {
	r, err := loadDetectorRules()
	if err != nil {
		t.Fatal(err)
	}
	pieces := []string{
		"March", "march", "MAR", "Sept", "ſept", "ſeptember", "Dec", "December", "Auguſt", "June", "May",
		" ", " ", ", ", ". ", ".", ",", "-", "_", "x", "é", "日本", "\xc5", "\xbf", "\xff", "\n",
		"1", "5", "12", "31", "2026", "1999", "20261", "0000", "123456789", "2026-03-04", "2026-13-40",
		"on ", " of ", "a2026", "2026a", "_2026", "K",
	}
	// The contract's patterns, plus unfolded ones whose windows are exact, so window edges land
	// right next to month names and digits.
	patterns := slices.Clone(r.dates)
	for _, x := range []struct {
		src  string
		year int
	}{{`\b(May|June)\.? ([0-9]{1,2}),? ([0-9]{4})\b`, 3}, {`(May) ([0-9]{4})\B`, 2}, {`\B([0-9]{4})-([0-9]{2})`, 1}} {
		re, window, err := compileDatePattern(rulePattern{Pattern: x.src}, x.year)
		if err != nil || window <= 0 {
			t.Fatalf("%s: window %d, %v", x.src, window, err)
		}
		patterns = append(patterns, datePattern{re: re, window: window})
	}
	// Texts whose window edges fall exactly on a month name or a digit run.
	edges := []string{"xJune. 12, 2026", "1June. 12, 2026", "_June. 12, 2026", "éJune. 12, 2026", "June. 12, 2026x",
		"May 20261", "xMay 2026 2026-03", "a2026-03", "2026-03a", "\xbfJune. 12, 2026"}
	rng := rand.New(rand.NewPCG(5, 161))
	matched := 0
	for n := 0; n < 6000+len(edges); n++ {
		var b strings.Builder
		if n < len(edges) {
			b.WriteString(edges[n])
		} else {
			for k := rng.IntN(60); k >= 0; k-- {
				b.WriteString(pieces[rng.IntN(len(pieces))])
			}
		}
		text := b.String()
		for i, p := range patterns {
			whole := p.re.FindAllStringSubmatchIndex(text, -1)
			got := p.findAll(text)
			if !slices.EqualFunc(whole, got, slices.Equal[[]int]) {
				t.Fatalf("pattern %d on %q:\nwhole  %v\nwindow %v", i, text, whole, got)
			}
			matched += len(whole)
		}
	}
	if matched < 1500 {
		t.Fatalf("only %d matches: the random texts do not exercise the patterns", matched)
	}
}

// The response without a date never reads the reference text, and the findings are unchanged.
func TestDatesWithoutResponseDate(t *testing.T) {
	r, err := loadDetectorRules()
	if err != nil {
		t.Fatal(err)
	}
	in := &DetectorInput{Response: "Refunds take 30 days.", Evidence: []EvidenceItem{{ID: "a", Source: "request_system", Text: "Updated March 3, 2026."}}}
	if got := newDetection(r, in).dates(); got == nil || len(got) != 0 {
		t.Fatalf("dates() = %#v, want an empty, non-nil list", got)
	}
	in.Response = "Updated March 3, 2026 and again on 4 March 2026."
	got := newDetection(r, in).dates()
	if len(got) != 1 || got[0].Text != "4 March 2026" {
		t.Fatalf("dates() = %#v, want only the date the evidence lacks", got)
	}
}
