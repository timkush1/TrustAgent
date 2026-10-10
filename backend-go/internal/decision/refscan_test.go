package decision

import (
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The contract's number and URL patterns scan in runs, the URL scan needs "://", and the
// whitespace pattern collapses in a byte loop (D167).
func TestContractPatternsUseTheFastScans(t *testing.T) {
	r, err := loadDetectorRules()
	if err != nil {
		t.Fatal(err)
	}
	if !r.numberScan.runs || r.numberScan.alphabet['a'] || !r.numberScan.alphabet['7'] || r.numberScan.first[','] {
		t.Errorf("the number pattern %q does not scan in digit runs", r.number)
	}
	if !r.urlScan.runs || r.urlScan.literal != "://" || r.urlScan.alphabet[' '] || !r.urlScan.first['H'] {
		t.Errorf("the URL pattern %q does not scan in runs needing \"://\": %+v", r.url, r.urlScan.literal)
	}
	if !r.wsRunFast {
		t.Errorf("the whitespace pattern %q has no byte loop", r.whitespace)
	}
}

// newRefScan refuses what a run edge could change or a prefilter cannot bound: an assertion
// anywhere, or a pattern that matches the empty string.
func TestRefScanRefuses(t *testing.T) {
	for _, src := range []string{`\bfoo`, `foo\b`, `^foo`, `(?m)^[0-9]+`, `foo$`, `\Afoo`, `a\Bb`, `x*`, `(?:ab)?`, `a|`, `[0-9]{0,3}`} {
		if s := newRefScan(regexp.MustCompile(src)); s.runs {
			t.Errorf("newRefScan(%q) scans in runs", src)
		}
	}
	for _, c := range []struct{ src, literal string }{
		{`[0-9][0-9,]*(?:\.[0-9]+)?%?`, ""}, {`(?i)https?://[^\s<>"')\]]+`, "://"}, {`(?i)kay`, ""}, {`[a-c]+x?|y`, ""},
		{`x*[0-9]`, ""}, {`é+`, ""}, {`[^"]+`, ""}, {`.+`, ""}, {`(ab)(cd)`, "ab"}, {`k(?i:ay)--x`, "--x"},
	} {
		s := newRefScan(regexp.MustCompile(c.src))
		if !s.runs || s.literal != c.literal {
			t.Errorf("newRefScan(%q): runs %v literal %q, want runs and %q", c.src, s.runs, s.literal, c.literal)
		}
	}
	// (?i)k folds to the Kelvin sign, a rune outside ASCII: every byte from 0x80 up joins the
	// alphabet, so a run never splits a rune.
	if s := newRefScan(regexp.MustCompile(`(?i)k`)); !s.first['k'] || !s.first['K'] || !s.first[0xe2] || !s.alphabet[0x84] {
		t.Error("(?i)k misses a fold of k")
	}
}

// findAllIndex returns exactly what FindAllStringIndex returns, on random texts built from pieces
// that start, end and break matches: digits with separators, URL schemes in either case with their
// long folds, quotation marks, multi-byte runes, continuation and invalid bytes.
func TestRefScanMatchesWholeScan(t *testing.T) {
	r, err := loadDetectorRules()
	if err != nil {
		t.Fatal(err)
	}
	pieces := []string{
		"1", "07", "1,299", "1,299.00", ",", ".", ".5", "30%", "%", "4.50", "2026", " ", "  ", "\t", "\n", "\r\n", "\f",
		"http://", "https://", "HTTPS://", "Http", "hTTp:/", "httpſ://", "://", "\u212a", "k", "K", "help.acme.example",
		"/refunds/12", "?q=1#x", "<", ">", "\"", "'", ")", "]", "(", "“", "”", "‘", "’", "x", "_", "é", "日本", "\xc5", "\xbf",
		"\xff", "\xe2\x80", "kay", "a", "b", "c", "y", "-", "--x",
	}
	patterns := []*regexp.Regexp{r.number, r.url}
	for _, src := range []string{`(?i)kay`, `[a-c]+x?|y`, `x*[0-9]`, `é+`, `[^"]+`, `.+`, `“[^”]+”`, `(?i)ſ?k[0-9]*`, `k(?i:ay)--x`, `[^ ]*://[a-z]+`} {
		patterns = append(patterns, regexp.MustCompile(src))
	}
	scans := make([]*refScan, len(patterns))
	for i, re := range patterns {
		if scans[i] = newRefScan(re); !scans[i].runs {
			t.Fatalf("%q does not scan in runs", re)
		}
	}
	rng := rand.New(rand.NewPCG(7, 167))
	matched := 0
	for n := 0; n < 6000; n++ {
		var b strings.Builder
		for k := rng.IntN(60); k >= 0; k-- {
			b.WriteString(pieces[rng.IntN(len(pieces))])
		}
		text := b.String()
		for i, s := range scans {
			whole := patterns[i].FindAllStringIndex(text, -1)
			got := s.findAllIndex(text)
			if !slices.EqualFunc(whole, got, slices.Equal[[]int]) {
				t.Fatalf("%q on %q:\nwhole %v\nruns  %v", patterns[i], text, whole, got)
			}
			if w, g := patterns[i].FindAllString(text, -1), s.findAllString(text); !slices.Equal(w, g) {
				t.Fatalf("%q strings on %q:\nwhole %q\nruns  %q", patterns[i], text, w, g)
			}
			matched += len(whole)
		}
	}
	if matched < 20000 {
		t.Fatalf("only %d matches: the random texts do not exercise the patterns", matched)
	}
}

// collapseRuns is the whitespace pattern's ReplaceAllString with one space, on random texts.
func TestCollapseRunsMatchesTheRegexp(t *testing.T) {
	r, err := loadDetectorRules()
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{`\s*`, `[ \x{a0}]+`, `[ ]+x`, `(?i)[a-c]+`} {
		set, ok := asciiClassRun(regexp.MustCompile(src))
		if ok && src != `(?i)[a-c]+` {
			t.Errorf("asciiClassRun accepted %q", src)
		}
		if src == `(?i)[a-c]+` && (!ok || !set['A'] || !set['c']) {
			t.Errorf("asciiClassRun(%q) = %v, want the folded class", src, ok)
		}
	}
	pieces := []string{" ", "  ", "\t", "\n", "\r", "\f", "\v", "\u00a0", "a", "B", "é", "\xff", "word", ".", "\u2003"}
	rng := rand.New(rand.NewPCG(3, 167))
	for n := 0; n < 3000; n++ {
		var b strings.Builder
		for k := rng.IntN(40); k >= 0; k-- {
			b.WriteString(pieces[rng.IntN(len(pieces))])
		}
		text := b.String()
		want := r.whitespace.ReplaceAllString(text, " ")
		if got := collapseRuns([]byte(text), &r.wsRun); got != want {
			t.Fatalf("collapseRuns(%q) = %q, want %q", text, got, want)
		}
	}
}

// The response decides whether the reference text is read (D167): a response with no checked
// number, URL or long quote leaves it alone, and the findings are unchanged when it has one.
func TestReferenceReadOnlyForResponseCandidates(t *testing.T) {
	r, err := loadDetectorRules()
	if err != nil {
		t.Fatal(err)
	}
	ev := []EvidenceItem{{ID: "a", Source: "request_system", Text: "Refunds within 30 days at https://help.acme.example/refunds. " +
		"The policy says “refunds always go back to the original card”. A 1,299.00 fee applies."}}
	in := &DetectorInput{Response: "Sure, I can help with that: 2 options.", Evidence: ev, MinInteger: DefaultMinInteger}
	d := newDetection(r, in)
	for name, got := range map[string][]DetectorFinding{"numbers": d.numbers(), "urls": d.urls(), "quotes": d.quotes()} {
		if got == nil || len(got) != 0 {
			t.Errorf("%s() = %#v, want an empty, non-nil list", name, got)
		}
	}
	in.Response = "Refunds within 30 days, or 45 days, for a 1299 fee, at https://HELP.acme.example/refunds/ or " +
		"https://help.acme.example/other. It says \"refunds always go back to the original card\" and \"refunds go to store credit only\"."
	d = newDetection(r, in)
	texts := func(fs []DetectorFinding) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Text)
		}
		return out
	}
	if got := texts(d.numbers()); !slices.Equal(got, []string{"45"}) {
		t.Errorf("numbers() = %q, want [45]", got)
	}
	if got := texts(d.urls()); !slices.Equal(got, []string{"https://help.acme.example/other"}) {
		t.Errorf("urls() = %q, want the URL the evidence lacks", got)
	}
	if got := texts(d.quotes()); !slices.Equal(got, []string{"refunds go to store credit only"}) {
		t.Errorf("quotes() = %q, want the quote the evidence lacks", got)
	}
}
