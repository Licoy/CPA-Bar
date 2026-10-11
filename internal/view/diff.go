package view

import "strings"

// DiffOp says what a diff line is.
type DiffOp int

const (
	DiffEqual DiffOp = iota
	DiffInsert
	DiffDelete
)

// DiffLine is one line of a line diff. Old and New are 1-based line
// numbers in the old and the new text, 0 where the line is not in that
// text (New for a deletion, Old for an insertion).
type DiffLine struct {
	Op   DiffOp
	Text string
	Old  int
	New  int
}

// maxDiffEdits bounds the work of the diff: past this many changed lines
// the middle of the texts is reported as replaced as a whole.
const maxDiffEdits = 1500

// splitLines splits text into lines; a final newline does not start an
// empty line, and the empty text has no lines.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// DiffLines compares two texts line by line (Myers' O(ND) algorithm, after
// trimming the common head and tail).
func DiffLines(oldText, newText string) []DiffLine {
	a, b := splitLines(oldText), splitLines(newText)
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}
	out := make([]DiffLine, 0, len(a)+len(b)-head-tail)
	for i := 0; i < head; i++ {
		out = append(out, DiffLine{Op: DiffEqual, Text: a[i], Old: i + 1, New: i + 1})
	}
	for _, e := range myersDiff(a[head:len(a)-tail], b[head:len(b)-tail]) {
		switch e.op {
		case DiffEqual:
			out = append(out, DiffLine{Op: DiffEqual, Text: e.text, Old: head + e.x + 1, New: head + e.y + 1})
		case DiffDelete:
			out = append(out, DiffLine{Op: DiffDelete, Text: e.text, Old: head + e.x + 1})
		case DiffInsert:
			out = append(out, DiffLine{Op: DiffInsert, Text: e.text, New: head + e.y + 1})
		}
	}
	for i := 0; i < tail; i++ {
		x, y := len(a)-tail+i, len(b)-tail+i
		out = append(out, DiffLine{Op: DiffEqual, Text: a[x], Old: x + 1, New: y + 1})
	}
	return out
}

// DiffStat counts the inserted and the deleted lines.
func DiffStat(lines []DiffLine) (added, removed int) {
	for _, l := range lines {
		switch l.Op {
		case DiffInsert:
			added++
		case DiffDelete:
			removed++
		}
	}
	return added, removed
}

// DiffRow is a row of a collapsed diff: a line, or Skipped unchanged lines.
type DiffRow struct {
	Line    DiffLine
	Skipped int
}

// CollapseDiff keeps the changed lines with context unchanged lines around
// them and folds the rest into skip rows.
func CollapseDiff(lines []DiffLine, context int) []DiffRow {
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if l.Op == DiffEqual {
			continue
		}
		for j := max(0, i-context); j <= min(len(lines)-1, i+context); j++ {
			keep[j] = true
		}
	}
	var rows []DiffRow
	skipped := 0
	for i, l := range lines {
		if keep[i] {
			if skipped > 0 {
				rows = append(rows, DiffRow{Skipped: skipped})
				skipped = 0
			}
			rows = append(rows, DiffRow{Line: l})
			continue
		}
		skipped++
	}
	if skipped > 0 {
		rows = append(rows, DiffRow{Skipped: skipped})
	}
	return rows
}

type diffEdit struct {
	op   DiffOp
	text string
	x, y int // 0-based positions in a and b (x for equal and delete, y for equal and insert)
}

// myersDiff returns the edit script from a to b.
func myersDiff(a, b []string) []diffEdit {
	n, m := len(a), len(b)
	switch {
	case n == 0 && m == 0:
		return nil
	case n == 0:
		return replaceAll(nil, b)
	case m == 0:
		return replaceAll(a, nil)
	}
	limit := min(n+m, maxDiffEdits)
	off := limit + 1
	v := make([]int, 2*limit+3)
	// trace[d] holds v as it was before round d, for k in [-d-1, d+1].
	trace := make([][]int, 0, 16)
	for d := 0; d <= limit; d++ {
		snap := make([]int, 2*d+3)
		copy(snap, v[off-d-1:off+d+2])
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrackMyers(a, b, trace)
			}
		}
	}
	return replaceAll(a, b)
}

func replaceAll(a, b []string) []diffEdit {
	out := make([]diffEdit, 0, len(a)+len(b))
	for i, s := range a {
		out = append(out, diffEdit{op: DiffDelete, text: s, x: i})
	}
	for i, s := range b {
		out = append(out, diffEdit{op: DiffInsert, text: s, y: i})
	}
	return out
}

func backtrackMyers(a, b []string, trace [][]int) []diffEdit {
	x, y := len(a), len(b)
	var rev []diffEdit
	for d := len(trace) - 1; d >= 0; d-- {
		snap := trace[d]
		at := func(k int) int { return snap[k+d+1] }
		k := x - y
		var prevK int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := at(prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			rev = append(rev, diffEdit{op: DiffEqual, text: a[x-1], x: x - 1, y: y - 1})
			x--
			y--
		}
		if d > 0 {
			if x == prevX {
				rev = append(rev, diffEdit{op: DiffInsert, text: b[prevY], y: prevY})
			} else {
				rev = append(rev, diffEdit{op: DiffDelete, text: a[prevX], x: prevX})
			}
		}
		x, y = prevX, prevY
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}
