package tools

import (
	"fmt"
	"strings"
)

// ADR-0040 D2 — edits report evidence, not a claim.
//
// "edited path (1 replacement(s))" tells the model nothing it can verify, so it
// re-reads the file. A unified diff answers the question directly and costs a few
// hundred bytes. The diff is capped; past the cap the edit still stands and the
// note says so.

const (
	diffContext   = 3
	diffMaxBytes  = 4096
	diffCellLimit = 2_000_000 // LCS budget; past it we emit a replace-whole-region diff
)

type diffLine struct {
	kind byte // ' ' | '-' | '+'
	text string
	oldN int
	newN int
}

// unifiedDiff renders a 3-line-context unified diff, capped at diffMaxBytes.
func unifiedDiff(path, before, after string) string {
	if before == after {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s (%d bytes)\n+++ %s (%d bytes)\n", path, len(before), path, len(after))

	ops := diffOps(splitDiffLines(before), splitDiffLines(after))
	if len(ops) == 0 {
		return b.String()
	}
	numberOps(ops)
	b.WriteString(renderHunks(ops, diffContext))

	out := b.String()
	if len(out) > diffMaxBytes {
		cut := diffMaxBytes
		for cut > 0 && !isRuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + "\n…[diff truncated at 4 KiB; the edit was applied in full]\n"
	}
	return out
}

func splitDiffLines(s string) []string { return strings.Split(s, "\n") }

// diffOps trims the common head and tail (where nearly all of a file's lines are)
// and runs LCS only on what is left.
func diffOps(a, b []string) []diffLine {
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}
	mid := lcsOps(a[head:len(a)-tail], b[head:len(b)-tail])

	ops := make([]diffLine, 0, len(a)+len(b))
	for _, line := range a[:head] {
		ops = append(ops, diffLine{kind: ' ', text: line})
	}
	ops = append(ops, mid...)
	for _, line := range a[len(a)-tail:] {
		ops = append(ops, diffLine{kind: ' ', text: line})
	}
	return ops
}

func lcsOps(a, b []string) []diffLine {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		out := make([]diffLine, 0, n+m)
		for _, l := range a {
			out = append(out, diffLine{kind: '-', text: l})
		}
		for _, l := range b {
			out = append(out, diffLine{kind: '+', text: l})
		}
		return out
	}
	if n*m > diffCellLimit {
		// Too big to diff line-wise within budget: show it as one replacement.
		out := make([]diffLine, 0, n+m)
		for _, l := range a {
			out = append(out, diffLine{kind: '-', text: l})
		}
		for _, l := range b {
			out = append(out, diffLine{kind: '+', text: l})
		}
		return out
	}

	dp := make([][]int32, n+1)
	for i := range dp {
		dp[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				dp[i][j] = dp[i+1][j+1] + 1
			case dp[i+1][j] >= dp[i][j+1]:
				dp[i][j] = dp[i+1][j]
			default:
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	out := make([]diffLine, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, diffLine{kind: ' ', text: a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			out = append(out, diffLine{kind: '-', text: a[i]})
			i++
		default:
			out = append(out, diffLine{kind: '+', text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffLine{kind: '-', text: a[i]})
	}
	for ; j < m; j++ {
		out = append(out, diffLine{kind: '+', text: b[j]})
	}
	return out
}

// numberOps records the old/new line number each op refers to.
func numberOps(ops []diffLine) {
	oldN, newN := 1, 1
	for i := range ops {
		switch ops[i].kind {
		case ' ':
			ops[i].oldN, ops[i].newN = oldN, newN
			oldN++
			newN++
		case '-':
			ops[i].oldN = oldN
			oldN++
		case '+':
			ops[i].newN = newN
			newN++
		}
	}
}

func renderHunks(ops []diffLine, ctx int) string {
	var changes []int
	for i := range ops {
		if ops[i].kind != ' ' {
			changes = append(changes, i)
		}
	}
	if len(changes) == 0 {
		return ""
	}
	var b strings.Builder
	for g := 0; g < len(changes); {
		first, last := changes[g], changes[g]
		for g+1 < len(changes) && changes[g+1]-last-1 <= 2*ctx {
			g++
			last = changes[g]
		}
		g++

		from := first - ctx
		if from < 0 {
			from = 0
		}
		to := last + ctx
		if to > len(ops)-1 {
			to = len(ops) - 1
		}

		var oldBefore, newBefore, aCount, bCount int
		for i := 0; i < from; i++ {
			switch ops[i].kind {
			case ' ':
				oldBefore++
				newBefore++
			case '-':
				oldBefore++
			case '+':
				newBefore++
			}
		}
		for i := from; i <= to; i++ {
			switch ops[i].kind {
			case ' ', '-':
				aCount++
			}
			switch ops[i].kind {
			case ' ', '+':
				bCount++
			}
		}
		aStart, bStart := oldBefore+1, newBefore+1
		if aCount == 0 {
			aStart = oldBefore
		}
		if bCount == 0 {
			bStart = newBefore
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", aStart, aCount, bStart, bCount)
		for i := from; i <= to; i++ {
			b.WriteByte(ops[i].kind)
			b.WriteString(ops[i].text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
