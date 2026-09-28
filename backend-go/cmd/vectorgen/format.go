package main

import (
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
)

const (
	// lineWidth is the width a line may reach before a value is broken over several lines.
	lineWidth = 100
	// indentUnit is one indentation level of a vector file.
	indentUnit = "  "
)

// format renders a generic JSON value the way the hand-written vector files are laid out: a value
// that fits on the rest of its line stays on one line (`{"a": 1, "b": [2, 3]}`); otherwise objects
// put one member per line and arrays of scalars fill lines up to lineWidth. Object keys are in
// UTF-16 order; strings and numbers are written as canonical JSON writes them. level is the
// indentation level of the line the value starts on.
func format(v any, level int) string {
	var b strings.Builder
	writeValue(&b, v, level, len(indentUnit)*level+len(`"expected": `))
	return b.String()
}

// writeValue writes v starting at column col of a line indented at level.
func writeValue(b *strings.Builder, v any, level, col int) {
	if one := compact(v); col+len(one) <= lineWidth {
		b.WriteString(one)
		return
	}
	inner := strings.Repeat(indentUnit, level+1)
	switch x := v.(type) {
	case map[string]any:
		keys := sortedKeys(x)
		b.WriteString("{\n")
		for i, k := range keys {
			b.WriteString(inner)
			key := quote(k) + ": "
			b.WriteString(key)
			writeValue(b, x[k], level+1, len(inner)+len(key))
			if i < len(keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat(indentUnit, level) + "}")
	case []any:
		b.WriteString("[\n")
		if allScalars(x) {
			line := inner
			for i, e := range x {
				item := compact(e)
				if i < len(x)-1 {
					item += ","
				}
				if line != inner && len(line)+1+len(item) > lineWidth {
					b.WriteString(line + "\n")
					line = inner
				}
				if line != inner {
					line += " "
				}
				line += item
			}
			b.WriteString(line + "\n")
		} else {
			for i, e := range x {
				b.WriteString(inner)
				writeValue(b, e, level+1, len(inner))
				if i < len(x)-1 {
					b.WriteByte(',')
				}
				b.WriteByte('\n')
			}
		}
		b.WriteString(strings.Repeat(indentUnit, level) + "]")
	default:
		b.WriteString(compact(v))
	}
}

// compact renders v on one line with ", " and ": " separators.
func compact(v any) string {
	switch x := v.(type) {
	case map[string]any:
		parts := make([]string, 0, len(x))
		for _, k := range sortedKeys(x) {
			parts = append(parts, quote(k)+": "+compact(x[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, compact(e))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	b, err := decision.Canonicalize(v)
	if err != nil {
		panic(err) // generic JSON values from encoding/json always canonicalize
	}
	return string(b)
}

func quote(s string) string {
	b, _ := decision.Canonicalize(s)
	return string(b)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	decision.SortUTF16(keys)
	return keys
}

func allScalars(xs []any) bool {
	for _, x := range xs {
		switch x.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}
