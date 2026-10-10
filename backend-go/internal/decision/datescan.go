package decision

import (
	"regexp"
	"regexp/syntax"
	"unicode"
	"unicode/utf8"
)

// Date scanning in windows (P5 integration, D161). A date pattern's regexp has no literal prefix
// (a case-insensitive month alternation), so scanning a whole reference text with it costs about
// 100 ns a byte per pattern: tens of milliseconds over 32k-token evidence. Every match of a date
// pattern contains its year, four ASCII digits, and is at most a fixed number of bytes long. So
// the scan only needs the text near runs of four or more digits. dateWindow derives both facts
// from the pattern itself; a pattern for which it cannot prove them is scanned whole, as before.
// findAll returns exactly the matches a whole-text scan returns (datescan_test.go checks this
// against the whole-text scan on random texts), so the vectors and the TypeScript port are
// unaffected.

// dateWindow returns the most bytes a match of the pattern src can span when every match must
// contain the capture group year as exactly four ASCII digits, and the pattern has no assertion
// other than \b and \B. It returns 0 when either fact cannot be shown (the pattern is then scanned
// whole).
func dateWindow(src string, year int) int {
	re, err := syntax.Parse(src, syntax.Perl)
	if err != nil || year <= 0 || !requiresYear(re, year) {
		return 0
	}
	n := maxMatchBytes(re)
	if n <= 0 {
		return 0
	}
	return n
}

// requiresYear reports a capture group year that every match goes through (it hangs off the root
// through concatenations and captures only) and that matches exactly four ASCII digits.
func requiresYear(re *syntax.Regexp, year int) bool {
	switch re.Op {
	case syntax.OpCapture:
		if re.Cap == year {
			return fourDigits(re.Sub[0])
		}
		return requiresYear(re.Sub[0], year)
	case syntax.OpConcat:
		for _, s := range re.Sub {
			if requiresYear(s, year) {
				return true
			}
		}
	}
	return false
}

// fourDigits reports [0-9]{4} (or four [0-9] in a row), with no case folding that adds runes.
func fourDigits(re *syntax.Regexp) bool {
	digit := func(c *syntax.Regexp) bool {
		return c.Op == syntax.OpCharClass && len(c.Rune) == 2 && c.Rune[0] == '0' && c.Rune[1] == '9'
	}
	switch re.Op {
	case syntax.OpRepeat:
		return re.Min == 4 && re.Max == 4 && digit(re.Sub[0])
	case syntax.OpConcat:
		if len(re.Sub) != 4 {
			return false
		}
		for _, s := range re.Sub {
			if !digit(s) {
				return false
			}
		}
		return true
	}
	return false
}

// maxMatchBytes is the most bytes a match of re can span, or -1 when it is unbounded or uses an
// assertion that a window edge could change (^, $, \A, \z). A case-folded literal counts the
// longest rune of each fold orbit, since a fold can be longer than the rune written.
func maxMatchBytes(re *syntax.Regexp) int {
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpNoMatch, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return 0
	case syntax.OpLiteral:
		n := 0
		for _, r := range re.Rune {
			n += runeBytes(r, re.Flags&syntax.FoldCase != 0)
		}
		return n
	case syntax.OpCharClass:
		if len(re.Rune) == 0 {
			return 0
		}
		if l := utf8.RuneLen(re.Rune[len(re.Rune)-1]); l > 0 {
			return l
		}
		return utf8.UTFMax
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return utf8.UTFMax
	case syntax.OpCapture, syntax.OpQuest:
		return maxMatchBytes(re.Sub[0])
	case syntax.OpRepeat:
		sub := maxMatchBytes(re.Sub[0])
		if re.Max < 0 || sub < 0 {
			return -1
		}
		return re.Max * sub
	case syntax.OpConcat:
		n := 0
		for _, s := range re.Sub {
			m := maxMatchBytes(s)
			if m < 0 {
				return -1
			}
			n += m
		}
		return n
	case syntax.OpAlternate:
		n := 0
		for _, s := range re.Sub {
			m := maxMatchBytes(s)
			if m < 0 {
				return -1
			}
			n = max(n, m)
		}
		return n
	}
	return -1 // OpStar, OpPlus, OpBeginLine, OpEndLine, OpBeginText, OpEndText
}

// runeBytes is the most bytes r takes in UTF-8, or, case-folded, the most any rune of its
// simple-fold orbit takes (ſ for s is two bytes, the Kelvin sign for k three).
func runeBytes(r rune, fold bool) int {
	n := utf8.RuneLen(r)
	if n < 0 {
		return utf8.UTFMax
	}
	if fold {
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			n = max(n, utf8.RuneLen(f))
		}
	}
	return n
}

// findAll is p.re.FindAllStringSubmatchIndex(text, -1), scanning only the windows around runs of
// four or more digits when p.window says that is enough.
func (p datePattern) findAll(text string) [][]int {
	if p.window <= 0 {
		return p.re.FindAllStringSubmatchIndex(text, -1)
	}
	var out [][]int
	for _, seg := range dateSegments(text, p.window) {
		for _, m := range p.re.FindAllStringSubmatchIndex(text[seg[0]:seg[1]], -1) {
			for i := range m {
				if m[i] >= 0 {
					m[i] += seg[0]
				}
			}
			out = append(out, m)
		}
	}
	return out
}

// dateSegments returns the disjoint, ordered byte ranges of text a match spanning at most window
// bytes and holding four ASCII digits can lie in: window bytes either side of every run of four or
// more digits, merged. Each edge is moved outward onto a rune start next to a byte that is not an
// ASCII word character, so \b and \B decide the same inside a segment as in the whole text, and
// decoding inside the segment stays aligned with decoding the whole text.
func dateSegments(text string, window int) [][2]int {
	var segs [][2]int
	for i := 0; i < len(text); {
		if !isDigit(text[i]) {
			i++
			continue
		}
		j := i
		for j < len(text) && isDigit(text[j]) {
			j++
		}
		if j-i >= 4 {
			start, end := max(0, i+4-window), min(len(text), j-4+window)
			for start > 0 && (isWordByte(text[start-1]) || !utf8.RuneStart(text[start])) {
				start--
			}
			for end < len(text) && (isWordByte(text[end]) || !utf8.RuneStart(text[end])) {
				end++
			}
			if n := len(segs); n > 0 && start <= segs[n-1][1] {
				segs[n-1][1] = max(segs[n-1][1], end)
			} else {
				segs = append(segs, [2]int{start, end})
			}
		}
		i = j
	}
	return segs
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// isWordByte is an ASCII word character, the only kind regexp's \b knows.
func isWordByte(c byte) bool {
	return c < utf8.RuneSelf && syntax.IsWordChar(rune(c))
}

// compileDatePattern compiles a date pattern and works out its scan window.
func compileDatePattern(p rulePattern, year int) (*regexp.Regexp, int, error) {
	re, err := p.compile()
	if err != nil {
		return nil, 0, err
	}
	src := p.Pattern
	if p.Flags != "" {
		src = "(?" + p.Flags + ")" + src
	}
	return re, dateWindow(src, year), nil
}
