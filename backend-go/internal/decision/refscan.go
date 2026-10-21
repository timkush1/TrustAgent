package decision

import (
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Reference scans in runs (D167). The number and URL patterns are scanned over every reference
// text. Go's regexp has no start-byte prefilter for a character class, and on texts of a few
// kilobytes it runs the backtracker, which clears a visited set as long as the whole text on every
// call: FindAll over a 2k-token document cost time quadratic in its length, most of the Tier-0
// time on 32k-token evidence.
//
// A match can only hold bytes of the pattern's alphabet (every byte some part of it consumes), so
// every match lies inside one maximal run of alphabet bytes. A pattern with no assertion (^, $,
// \A, \z, \b, \B) that cannot match the empty string finds, in each run alone, exactly the matches
// the whole-text scan finds there: the paths that succeed from a start never read past the run. So
// refScan scans the runs one by one, and only those that hold a byte a match can start with and,
// when the pattern requires a literal (a URL's "://"), that literal. Every scanned input is a short
// run, and a text without the literal is not scanned at all. findAll returns exactly what
// FindAllStringIndex returns (refscan_test.go checks it against the whole-text scan on random
// texts); a pattern the analysis cannot prove safe is scanned whole, as before.

// refScan is a pattern with its run prefilter; runs is false when it is scanned whole. hand is a
// hand-written scanner for a pattern the contract ships (handScanners), used instead of both.
type refScan struct {
	re       *regexp.Regexp
	hand     func(text string, fn func(start, end int))
	runs     bool
	alphabet [256]bool // the bytes a match can hold
	first    [256]bool // the bytes a match can start with
	literal  string    // a substring every match holds; "" when none is known
}

// newRefScan prepares re for reference scans.
func newRefScan(re *regexp.Regexp) *refScan {
	s := &refScan{re: re, hand: handScanners[re.String()]}
	parsed, err := syntax.Parse(re.String(), syntax.Perl)
	if err != nil || hasAssertion(parsed) {
		return s
	}
	parsed = parsed.Simplify()
	first, nullable, ok := firstBytes(parsed)
	if !ok || nullable || !alphabetOf(parsed, &s.alphabet) {
		return s
	}
	s.runs, s.first, s.literal = true, first, requiredLiteral(parsed)
	return s
}

// findAllString is re.FindAllString(text, -1).
func (s *refScan) findAllString(text string) []string {
	var out []string
	s.each(text, func(start, end int) { out = append(out, text[start:end]) })
	return out
}

// findAllIndex is re.FindAllStringIndex(text, -1).
func (s *refScan) findAllIndex(text string) [][]int {
	var out [][]int
	s.each(text, func(start, end int) { out = append(out, []int{start, end}) })
	return out
}

// each calls fn with the start and end of every match re.FindAllStringIndex(text, -1) returns,
// in order.
func (s *refScan) each(text string, fn func(start, end int)) {
	if s.hand != nil {
		s.hand(text, fn)
		return
	}
	if !s.runs {
		for _, m := range s.re.FindAllStringIndex(text, -1) {
			fn(m[0], m[1])
		}
		return
	}
	if s.literal != "" && !strings.Contains(text, s.literal) {
		return
	}
	for i := 0; i < len(text); {
		if !s.alphabet[text[i]] {
			i++
			continue
		}
		// Run boundaries are bytes outside the alphabet. The alphabet holds either every byte from
		// 0x80 up or none, so a boundary never splits a rune.
		j, starts := i, false
		for j < len(text) && s.alphabet[text[j]] {
			starts = starts || s.first[text[j]]
			j++
		}
		if run := text[i:j]; starts && (s.literal == "" || strings.Contains(run, s.literal)) {
			for _, m := range s.re.FindAllStringIndex(run, -1) {
				fn(i+m[0], i+m[1])
			}
		}
		i = j
	}
}

// handScanners are hand-written scanners for patterns the contract ships, keyed by the pattern's
// source: each finds exactly the matches FindAllStringIndex finds (refscan_test.go checks that on
// random texts), without a regexp call per match. A contract edit that changes the source turns
// its scanner off; TestContractPatternsUseTheFastScans then fails, so the change is seen.
var handScanners = map[string]func(text string, fn func(start, end int)){
	// numbers_not_in_evidence: every quantifier is greedy and everything after the first digit is
	// optional, so the leftmost-first match is the longest one: digits and commas, a fraction when a
	// "." is followed by a digit, then a "%".
	`[0-9][0-9,]*(?:\.[0-9]+)?%?`: func(text string, fn func(start, end int)) {
		for i := 0; i < len(text); {
			if !isDigit(text[i]) {
				i++
				continue
			}
			j := i + 1
			for j < len(text) && (isDigit(text[j]) || text[j] == ',') {
				j++
			}
			if j+1 < len(text) && text[j] == '.' && isDigit(text[j+1]) {
				for j += 2; j < len(text) && isDigit(text[j]); j++ {
				}
			}
			if j < len(text) && text[j] == '%' {
				j++
			}
			fn(i, j)
			i = j
		}
	},
}

// addRuneByte marks the first byte of r in set. Any rune outside ASCII, including the replacement
// character an invalid byte decodes to, marks every byte from 0x80 up.
func addRuneByte(set *[256]bool, r rune) {
	if r >= 0 && r < utf8.RuneSelf {
		set[r] = true
		return
	}
	for b := 0x80; b < 0x100; b++ {
		set[b] = true
	}
}

// addClassBytes marks the bytes of a class's ranges (pairs lo, hi) in set.
func addClassBytes(set *[256]bool, ranges []rune) {
	for i := 0; i+1 < len(ranges); i += 2 {
		for r := ranges[i]; r <= ranges[i+1] && r < utf8.RuneSelf; r++ {
			set[r] = true
		}
		if ranges[i+1] >= utf8.RuneSelf {
			addRuneByte(set, utf8.RuneSelf)
		}
	}
}

// addLiteralRune marks r, and under case folding every rune of its fold orbit.
func addLiteralRune(set *[256]bool, r rune, fold bool) {
	addRuneByte(set, r)
	if fold {
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			addRuneByte(set, f)
		}
	}
}

// firstBytes returns the bytes a match of re can start with, whether re can match the empty
// string, and ok=false for an operator the analysis does not know (an assertion: newRefScan
// refuses those first).
func firstBytes(re *syntax.Regexp) (set [256]bool, nullable, ok bool) {
	union := func(sub *syntax.Regexp) (bool, bool) {
		s, n, k := firstBytes(sub)
		for b, in := range s {
			if in {
				set[b] = true
			}
		}
		return n, k
	}
	switch re.Op {
	case syntax.OpNoMatch:
		return set, false, true
	case syntax.OpEmptyMatch:
		return set, true, true
	case syntax.OpLiteral:
		if len(re.Rune) == 0 {
			return set, true, true
		}
		addLiteralRune(&set, re.Rune[0], re.Flags&syntax.FoldCase != 0)
		return set, false, true
	case syntax.OpCharClass:
		addClassBytes(&set, re.Rune)
		return set, false, true
	case syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		addClassBytes(&set, []rune{0, unicode.MaxRune})
		if re.Op == syntax.OpAnyCharNotNL {
			set['\n'] = false
		}
		return set, false, true
	case syntax.OpCapture, syntax.OpPlus:
		n, k := union(re.Sub[0])
		return set, n, k
	case syntax.OpStar, syntax.OpQuest:
		_, k := union(re.Sub[0])
		return set, true, k
	case syntax.OpRepeat:
		n, k := union(re.Sub[0])
		return set, n || re.Min == 0, k
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			n, k := union(sub)
			if !k {
				return set, false, false
			}
			if !n {
				return set, false, true
			}
		}
		return set, true, true
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			n, k := union(sub)
			if !k {
				return set, false, false
			}
			nullable = nullable || n
		}
		return set, nullable, true
	}
	return set, false, false
}

// alphabetOf marks in set every byte a match of re can hold; false for an operator it does not
// know.
func alphabetOf(re *syntax.Regexp, set *[256]bool) bool {
	switch re.Op {
	case syntax.OpNoMatch, syntax.OpEmptyMatch:
		return true
	case syntax.OpLiteral:
		for _, r := range re.Rune {
			addLiteralRune(set, r, re.Flags&syntax.FoldCase != 0)
		}
		return true
	case syntax.OpCharClass:
		addClassBytes(set, re.Rune)
		return true
	case syntax.OpAnyCharNotNL:
		addClassBytes(set, []rune{0, '\n' - 1, '\n' + 1, unicode.MaxRune})
		return true
	case syntax.OpAnyChar:
		addClassBytes(set, []rune{0, unicode.MaxRune})
		return true
	case syntax.OpCapture, syntax.OpPlus, syntax.OpStar, syntax.OpQuest, syntax.OpRepeat, syntax.OpConcat, syntax.OpAlternate:
		for _, sub := range re.Sub {
			if !alphabetOf(sub, set) {
				return false
			}
		}
		return true
	}
	return false
}

// requiredLiteral is the longest literal every match of re holds: a part of re's top-level
// concatenation that is a literal with no case folding that changes it ("://" in a case-insensitive
// URL pattern). It returns "" when there is none.
func requiredLiteral(re *syntax.Regexp) string {
	for re.Op == syntax.OpCapture {
		re = re.Sub[0]
	}
	parts := []*syntax.Regexp{re}
	if re.Op == syntax.OpConcat {
		parts = re.Sub
	}
	best := ""
	for _, p := range parts {
		for p.Op == syntax.OpCapture {
			p = p.Sub[0]
		}
		if p.Op != syntax.OpLiteral || len(p.Rune) <= len([]rune(best)) {
			continue
		}
		if p.Flags&syntax.FoldCase != 0 && !foldFree(p.Rune) {
			continue
		}
		best = string(p.Rune)
	}
	return best
}

// foldFree reports runes that case folding leaves alone (punctuation, digits).
func foldFree(rs []rune) bool {
	for _, r := range rs {
		if unicode.SimpleFold(r) != r {
			return false
		}
	}
	return true
}

// hasAssertion reports an assertion anywhere in re: a run edge could change what it decides.
func hasAssertion(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText,
		syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return true
	}
	for _, sub := range re.Sub {
		if hasAssertion(sub) {
			return true
		}
	}
	return false
}

// asciiClassRun returns the byte set of a pattern that is one character class of ASCII bytes
// repeated one or more times, such as the rules' whitespace [\t\n\f\r ]+, and ok=false for any
// other pattern.
func asciiClassRun(re *regexp.Regexp) (set [256]bool, ok bool) {
	parsed, err := syntax.Parse(re.String(), syntax.Perl)
	if err != nil {
		return set, false
	}
	parsed = parsed.Simplify()
	if parsed.Op != syntax.OpPlus || parsed.Sub[0].Op != syntax.OpCharClass || len(parsed.Sub[0].Rune) == 0 {
		return set, false
	}
	class := parsed.Sub[0].Rune
	for i := 0; i+1 < len(class); i += 2 {
		if class[i+1] >= utf8.RuneSelf {
			return set, false
		}
		for r := class[i]; r <= class[i+1]; r++ {
			set[r] = true
		}
	}
	return set, true
}

// collapseRuns is regexp `[set]+` ReplaceAllString(string(b), " ") for an ASCII byte set: every
// maximal run of bytes in set becomes one space. It reuses b.
func collapseRuns(b []byte, set *[256]bool) string {
	out := b[:0]
	inRun := false
	for _, c := range b {
		if set[c] {
			if !inRun {
				out = append(out, ' ')
				inRun = true
			}
			continue
		}
		out = append(out, c)
		inRun = false
	}
	return string(out)
}
