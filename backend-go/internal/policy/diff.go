package policy

import (
	"fmt"
	"strings"
)

// diffContext is the number of unchanged lines around a change in a hunk.
const diffContext = 3

// maxDiffCells bounds the LCS table (lines(a) x lines(b)); larger inputs are shown as a whole
// replacement. Policies are a few hundred lines, far below it.
const maxDiffCells = 4_000_000

// UnifiedDiff returns a unified diff (---/+++ headers, @@ hunks, 3 lines of context) from a to
// b, or "" when they are equal.
func UnifiedDiff(fromName, toName, a, b string) string {
	if a == b {
		return ""
	}
	x, y := splitLines(a), splitLines(b)
	ops := diffOps(x, y)
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", fromName, toName)
	// Group the operations into hunks separated by more than 2*context unchanged lines.
	i := 0
	for i < len(ops) {
		for i < len(ops) && ops[i].kind == ' ' {
			i++
		}
		if i == len(ops) {
			break
		}
		start := max(i-diffContext, 0)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*diffContext {
				end = min(end+diffContext, len(ops))
				break
			}
			end = run
		}
		writeHunk(&out, ops[start:end])
		i = end
	}
	return out.String()
}

type diffOp struct {
	kind       byte // ' ', '-', '+'
	line       string
	aLine, bLn int // 1-based line numbers in a and b (0 when absent)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffOps computes an edit script with a longest-common-subsequence table.
func diffOps(x, y []string) []diffOp {
	n, m := len(x), len(y)
	var ops []diffOp
	if n*m > maxDiffCells {
		for i, l := range x {
			ops = append(ops, diffOp{kind: '-', line: l, aLine: i + 1})
		}
		for j, l := range y {
			ops = append(ops, diffOp{kind: '+', line: l, bLn: j + 1})
		}
		return ops
	}
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && x[i] == y[j]:
			ops = append(ops, diffOp{kind: ' ', line: x[i], aLine: i + 1, bLn: j + 1})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, diffOp{kind: '-', line: x[i], aLine: i + 1})
			i++
		default:
			ops = append(ops, diffOp{kind: '+', line: y[j], bLn: j + 1})
			j++
		}
	}
	return ops
}

func writeHunk(out *strings.Builder, ops []diffOp) {
	aStart, bStart, aLen, bLen := 0, 0, 0, 0
	for _, op := range ops {
		if op.aLine > 0 {
			if aStart == 0 {
				aStart = op.aLine
			}
			aLen++
		}
		if op.bLn > 0 {
			if bStart == 0 {
				bStart = op.bLn
			}
			bLen++
		}
	}
	// With context lines, a side is empty only when that whole text is empty: it starts at 0.
	fmt.Fprintf(out, "@@ -%s +%s @@\n", hunkRange(aStart, aLen), hunkRange(bStart, bLen))
	for _, op := range ops {
		out.WriteByte(op.kind)
		out.WriteString(op.line)
		if !strings.HasSuffix(op.line, "\n") {
			out.WriteString("\n\\ No newline at end of file\n")
		}
	}
}

func hunkRange(start, length int) string {
	if length == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, length)
}
