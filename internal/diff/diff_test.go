package diff

import (
	"strings"
	"testing"
)

func TestIdentical(t *testing.T) {
	r := Compute("a\nb\n", "a\nb\n", 0)
	if !r.Identical {
		t.Fatal("identical content did not report itself identical")
	}
	if len(r.Hunks) != 0 || r.Added != 0 || r.Removed != 0 {
		t.Fatalf("identical content produced %d hunks, +%d -%d", len(r.Hunks), r.Added, r.Removed)
	}
}

func TestCounts(t *testing.T) {
	cases := []struct {
		name           string
		old, new       string
		added, removed int
	}{
		{"insert", "a\nb\n", "a\nx\nb\n", 1, 0},
		{"delete", "a\nb\nc\n", "a\nc\n", 0, 1},
		{"replace", "a\nb\nc\n", "a\nx\nc\n", 1, 1},
		{"from empty", "", "a\nb\n", 2, 0},
		{"to empty", "a\nb\n", "", 0, 2},
		{"no trailing newline", "a\nb", "a\nc", 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Compute(c.old, c.new, 0)
			if r.Added != c.added || r.Removed != c.removed {
				t.Fatalf("got +%d -%d, want +%d -%d", r.Added, r.Removed, c.added, c.removed)
			}
			if r.Identical {
				t.Fatal("a change reported itself identical")
			}
			if r.Omitted != 0 {
				t.Fatalf("no cap was set but %d lines were omitted", r.Omitted)
			}
		})
	}
}

func TestLinesCarryNoSign(t *testing.T) {
	r := Compute("keep\nold\n", "keep\nnew\n", 0)
	if len(r.Hunks) != 1 {
		t.Fatalf("got %d hunks, want 1", len(r.Hunks))
	}
	want := []Line{
		{Kind: Context, Text: "keep"},
		{Kind: Removed, Text: "old"},
		{Kind: Added, Text: "new"},
	}
	got := r.Hunks[0].Lines
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d is %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A file rewritten whole is the case the cap exists for: the counts have to
// describe the whole change even though the body stops early.
func TestCapKeepsCountsTrue(t *testing.T) {
	var old, new strings.Builder
	for i := 0; i < 200; i++ {
		old.WriteString("old line\n")
		new.WriteString("new line\n")
	}
	r := Compute(old.String(), new.String(), 50)

	if r.Added != 200 || r.Removed != 200 {
		t.Fatalf("got +%d -%d, want +200 -200", r.Added, r.Removed)
	}
	drawn := 0
	for _, h := range r.Hunks {
		drawn += len(h.Lines)
	}
	if drawn != 50 {
		t.Fatalf("the cap was 50 but %d lines were carried", drawn)
	}
	if r.Omitted != 350 {
		t.Fatalf("got %d omitted, want 350", r.Omitted)
	}
}

// Two changes far apart are separate hunks; the positions are what a reader
// uses to find them in the file.
func TestHunkPositions(t *testing.T) {
	old := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n16\n17\n18\n19\n20\n"
	new := "1\n2\nx\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n16\n17\ny\n19\n20\n"
	r := Compute(old, new, 0)
	if len(r.Hunks) != 2 {
		t.Fatalf("got %d hunks, want 2", len(r.Hunks))
	}
	if r.Hunks[0].OldStart >= r.Hunks[1].OldStart {
		t.Fatalf("hunks are out of order: %d then %d", r.Hunks[0].OldStart, r.Hunks[1].OldStart)
	}
	if r.Hunks[0].OldStart < 1 {
		t.Fatalf("the first hunk starts at line %d, which is not a line number", r.Hunks[0].OldStart)
	}
}

func TestHunkSection(t *testing.T) {
	var b strings.Builder
	b.WriteString("package tools\n\nfunc (p *Proc) pump() {\n")
	for i := 0; i < 20; i++ {
		b.WriteString("\tstep()\n")
	}
	b.WriteString("}\n")
	old := b.String()
	changed := strings.Replace(old, "\tstep()\n\tstep()\n}", "\tstep()\n\tdone()\n}", 1)
	r := Compute(old, changed, 0)
	if len(r.Hunks) != 1 {
		t.Fatalf("got %d hunks, want 1", len(r.Hunks))
	}
	if got := r.Hunks[0].Section; got != "func (p *Proc) pump() {" {
		t.Fatalf("section = %q, want the enclosing func line", got)
	}

	// A change at the top of a file has nothing above it to name.
	r = Compute("a\nb\n", "x\nb\n", 0)
	if got := r.Hunks[0].Section; got != "" {
		t.Fatalf("section = %q for a hunk at the first line, want empty", got)
	}
}
