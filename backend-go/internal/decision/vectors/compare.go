package vectors

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Compare checks actual against expected with the vector rules (contracts/vectors/README.md):
// objects in expected are subsets at every depth, arrays match in length and order, numbers match
// within tol, and strings, booleans and null match exactly (a null in expected needs a present
// null). It returns one line per mismatch, prefixed with its JSON path.
func Compare(expected, actual any, tol float64) []string {
	var diffs []string
	compare("$", expected, actual, tol, &diffs)
	return diffs
}

func compare(at string, want, got any, tol float64, diffs *[]string) {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			*diffs = append(*diffs, fmt.Sprintf("%s: want an object, got %s", at, show(got)))
			return
		}
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			gv, present := g[k]
			if !present {
				*diffs = append(*diffs, fmt.Sprintf("%s.%s: missing (want %s)", at, k, show(w[k])))
				continue
			}
			compare(at+"."+k, w[k], gv, tol, diffs)
		}
	case []any:
		g, ok := got.([]any)
		if !ok {
			*diffs = append(*diffs, fmt.Sprintf("%s: want an array, got %s", at, show(got)))
			return
		}
		if len(g) != len(w) {
			*diffs = append(*diffs, fmt.Sprintf("%s: want %d elements %s, got %d %s", at, len(w), show(w), len(g), show(g)))
			return
		}
		for i := range w {
			compare(fmt.Sprintf("%s[%d]", at, i), w[i], g[i], tol, diffs)
		}
	case float64:
		g, ok := got.(float64)
		if !ok || math.IsNaN(g) || math.Abs(g-w) > tol {
			*diffs = append(*diffs, fmt.Sprintf("%s: want %s, got %s", at, show(w), show(got)))
		}
	default: // string, bool, nil
		if want != got {
			*diffs = append(*diffs, fmt.Sprintf("%s: want %s, got %s", at, show(want), show(got)))
		}
	}
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}
