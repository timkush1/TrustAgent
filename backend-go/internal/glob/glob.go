// Package glob matches names against the shell-style patterns the policy uses for request paths,
// model names and untrusted tool names (SPEC §2.7, §2.10).
//
// A pattern is literal text with two wildcards: '*' matches any run of characters (including '/'
// and the empty run) and '?' matches exactly one character. There are no character classes and no
// escapes, so a pattern means the same thing in Go and in the TypeScript port.
package glob

import "unicode/utf8"

// Match reports whether name matches pattern as a whole. Matching works on code points.
func Match(pattern, name string) bool {
	// Iterative wildcard matching with single-star backtracking: when a later literal fails, the
	// most recent '*' absorbs one more code point of name and matching resumes after it.
	p, n := 0, 0
	starP, starN := -1, 0
	for n < len(name) {
		if p < len(pattern) {
			pc, pw := utf8.DecodeRuneInString(pattern[p:])
			nc, nw := utf8.DecodeRuneInString(name[n:])
			switch {
			case pc == '*':
				starP, starN = p, n
				p += pw
				continue
			case pc == '?' || pc == nc:
				p += pw
				n += nw
				continue
			}
		}
		if starP < 0 {
			return false
		}
		_, nw := utf8.DecodeRuneInString(name[starN:])
		starN += nw
		p, n = starP+1, starN
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// Any reports whether name matches at least one of patterns.
func Any(patterns []string, name string) bool {
	for _, p := range patterns {
		if Match(p, name) {
			return true
		}
	}
	return false
}
