package config

// Levenshtein returns the edit distance (insertions, deletions, substitutions) between a and b.
func Levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// Closest returns the candidate nearest to name within maxDistance edits, or "". Ties go to the
// alphabetically first candidate, as in the engine's loader.
func Closest(name string, candidates []string, maxDistance int) string {
	best, bestDist := "", maxDistance+1
	for _, c := range candidates {
		d := Levenshtein(name, c)
		if d < bestDist || (d == bestDist && best != "" && c < best) {
			best, bestDist = c, d
		}
	}
	if bestDist > maxDistance {
		return ""
	}
	return best
}
