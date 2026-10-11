package view

import (
	"fmt"
	"strings"
	"testing"
)

func renderDiff(ls []DiffLine) string {
	var sb strings.Builder
	for _, l := range ls {
		sb.WriteString(map[DiffOp]string{DiffEqual: " ", DiffInsert: "+", DiffDelete: "-"}[l.Op] + l.Text + "|")
	}
	return sb.String()
}

func TestDiffLines(t *testing.T) {
	for _, tc := range []struct {
		name, a, b, want string
		add, del         int
	}{
		{"identical", "a\nb\n", "a\nb\n", " a| b|", 0, 0},
		{"both empty", "", "", "", 0, 0},
		{"insert middle", "a\nc\n", "a\nb\nc\n", " a|+b| c|", 1, 0},
		{"delete middle", "a\nb\nc\n", "a\nc\n", " a|-b| c|", 0, 1},
		{"change", "a\nb\nc\n", "a\nB\nc\n", " a|-b|+B| c|", 1, 1},
		{"old empty", "", "x\ny\n", "+x|+y|", 2, 0},
		{"new empty", "x\ny\n", "", "-x|-y|", 0, 2},
		{"trailing newline ignored", "a\nb", "a\nb\n", " a| b|", 0, 0},
		{"append", "a\n", "a\nb\n", " a|+b|", 1, 0},
		{"prepend", "b\n", "a\nb\n", "+a| b|", 1, 0},
		{"interleaved", "1\n2\n3\n4\n5\n", "1\nx\n3\ny\n5\n", " 1|-2|+x| 3|-4|+y| 5|", 2, 2},
		{"repeated lines", "a\na\na\n", "a\na\n", " a| a|-a|", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ls := DiffLines(tc.a, tc.b)
			got := renderDiff(ls)
			add, del := DiffStat(ls)
			if add != tc.add || del != tc.del {
				t.Errorf("stat = +%d -%d, want +%d -%d (%s)", add, del, tc.add, tc.del, got)
			}
			if tc.name == "repeated lines" {
				// Which of the equal lines is reported deleted is not
				// specified; the counts are.
				return
			}
			if got != tc.want {
				t.Errorf("diff = %q, want %q", got, tc.want)
			}
		})
	}
}

// Applying a diff to the old text yields the new text, and the line numbers
// point at the right lines.
func TestDiffLinesRoundTrip(t *testing.T) {
	mk := func(seed, n int) string {
		var sb strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&sb, "line %d\n", (i*seed)%7)
		}
		return sb.String()
	}
	for _, tc := range [][2]string{{mk(3, 40), mk(5, 45)}, {mk(2, 10), mk(4, 0)}, {mk(1, 60), mk(6, 60)}} {
		a, b := splitLines(tc[0]), splitLines(tc[1])
		var rebuilt []string
		for _, l := range DiffLines(tc[0], tc[1]) {
			switch l.Op {
			case DiffEqual:
				if a[l.Old-1] != l.Text || b[l.New-1] != l.Text {
					t.Fatalf("bad line numbers in %+v", l)
				}
				rebuilt = append(rebuilt, l.Text)
			case DiffInsert:
				if b[l.New-1] != l.Text || l.Old != 0 {
					t.Fatalf("bad insert %+v", l)
				}
				rebuilt = append(rebuilt, l.Text)
			case DiffDelete:
				if a[l.Old-1] != l.Text || l.New != 0 {
					t.Fatalf("bad delete %+v", l)
				}
			}
		}
		if strings.Join(rebuilt, "\n") != strings.Join(b, "\n") {
			t.Errorf("rebuilt text differs")
		}
	}
}

func TestDiffLinesTooDifferent(t *testing.T) {
	var a, b []string
	for i := 0; i < 2000; i++ {
		a = append(a, fmt.Sprintf("a%d", i))
		b = append(b, fmt.Sprintf("b%d", i))
	}
	ls := DiffLines(strings.Join(a, "\n"), strings.Join(b, "\n"))
	if add, del := DiffStat(ls); add != 2000 || del != 2000 {
		t.Errorf("stat = +%d -%d", add, del)
	}
}

func TestCollapseDiff(t *testing.T) {
	var a, b []string
	for i := 0; i < 30; i++ {
		a = append(a, fmt.Sprintf("l%d", i))
		b = append(b, fmt.Sprintf("l%d", i))
	}
	b[15] = "changed"
	rows := CollapseDiff(DiffLines(strings.Join(a, "\n"), strings.Join(b, "\n")), 2)
	// 12 skipped, 2 context, -1, +1, 2 context, 12 skipped.
	if len(rows) != 8 || rows[0].Skipped != 13 || rows[len(rows)-1].Skipped != 12 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows := CollapseDiff(DiffLines("a\nb\n", "a\nb\n"), 3); len(rows) != 1 || rows[0].Skipped != 2 {
		t.Errorf("identical collapses to %+v", rows)
	}
	if rows := CollapseDiff(nil, 3); len(rows) != 0 {
		t.Errorf("empty diff has rows: %+v", rows)
	}
}
