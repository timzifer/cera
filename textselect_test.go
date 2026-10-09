package cera

import (
	"testing"
)

// selectPage is a block of two lines with a word broken across them and
// a block further down; Helvetica 10 from x 20.
const selectPage = `BT /F1 10 Tf 20 360 Td (Hello, wonder-) Tj 0 -12 Td (ful world.) Tj ET
	BT /F1 10 Tf 20 200 Td (Other block) Tj ET`

func TestTextOf(t *testing.T) {
	text := layoutPage(t, selectPage, TextOptions{}, "")
	if s := text.TextOf(text.All(), TextFormat{}); s != "Hello, wonder-\nful world.\n\nOther block" || s != text.String() {
		t.Errorf("all %q, String %q", s, text.String())
	}
	if s := text.TextOf(text.All(), TextFormat{JoinHyphenated: true}); s != "Hello, wonderful world.\n\nOther block" {
		t.Errorf("joined %q", s)
	}
	// The hyphen stays when the range ends on its line.
	if s := text.TextOf(TextRange{7, 14}, TextFormat{JoinHyphenated: true}); s != "wonder-" {
		t.Errorf("end of the line %q", s)
	}
	if s := text.TextOf(TextRange{12, 29}, TextFormat{}); s != "r-\nful world.\n\nOther" {
		t.Errorf("across blocks %q", s)
	}
	if s := text.TextOf(TextRange{5, 5}, TextFormat{}); s != "" {
		t.Errorf("empty %q", s)
	}
}

func TestSelectWords(t *testing.T) {
	text := layoutPage(t, selectPage, TextOptions{}, "")
	word := func(r TextRange) string { return text.TextOf(r, TextFormat{}) }
	for p, want := range map[TextPos]string{0: "Hello", 2: "Hello", 5: ",", 7: "wonder", 12: "wonder", 13: "-", 14: "ful", 23: ".", 24: "Other", 35: "block"} {
		if got := word(text.WordAt(p)); got != want {
			t.Errorf("WordAt(%d) = %q, want %q", p, got, want)
		}
	}
	if r := text.WordAt(6); r != (TextRange{6, 7}) {
		t.Errorf("WordAt on a space %v", r)
	}
	if r := text.LineAt(9); r != (TextRange{0, 14}) {
		t.Errorf("LineAt %v", r)
	}
	for _, c := range []struct {
		a, b TextPos
		g    Granularity
		want string
	}{
		{2, 9, SelectChars, "llo, wo"},
		{9, 2, SelectChars, "llo, wo"},
		{2, 9, SelectWords, "Hello, wonder"},
		{9, 2, SelectWords, "Hello, wonder"},
		{3, 3, SelectWords, "Hello"},
		{3, 16, SelectLines, "Hello, wonder-\nful world."},
		{0, 0, SelectBlocks, "Hello, wonder-\nful world."},
		{-5, 100, SelectChars, "Hello, wonder-\nful world.\n\nOther block"},
	} {
		if got := word(text.Select(c.a, c.b, c.g)); got != c.want {
			t.Errorf("Select(%d, %d, %d) = %q, want %q", c.a, c.b, c.g, got, c.want)
		}
	}
}

func TestPosAt(t *testing.T) {
	text := layoutPage(t, selectPage, TextOptions{}, "")
	// The baseline of the first line is at y 40; H is 7.22 wide.
	for _, c := range []struct {
		x, y float64
		want TextPos
	}{
		{21, 38, 0},   // first half of H
		{25, 38, 1},   // second half
		{5, 38, 0},    // left of the line
		{300, 38, 14}, // far right of it: its end, not the lower block
		{25, 51, 15},  // the second line, a little above it
		{25, 190, 25}, // the lower block
		{25, 399, 25}, // below everything: the nearest line
	} {
		if got := text.PosAt(c.x, c.y); got != c.want {
			t.Errorf("PosAt(%g, %g) = %d, want %d", c.x, c.y, got, c.want)
		}
	}
	if i, ok := text.CharAt(21, 38); !ok || i != 0 {
		t.Errorf("CharAt on H: %d %v", i, ok)
	}
	if _, ok := text.CharAt(5, 5); ok {
		t.Error("CharAt off the text")
	}
	if got := text.PosAt(1, 1); (&PageText{}).PosAt(1, 1) != 0 || got != 0 {
		t.Errorf("PosAt above the text %d", got)
	}
}

func TestPosAtTurned(t *testing.T) {
	// Up the page: A from y 80 to 73.33, B above it.
	text := layoutPage(t, "BT /F1 10 Tf 0 1 -1 0 100 320 Tm (AB) Tj ET", TextOptions{}, "")
	for _, c := range []struct {
		x, y float64
		want TextPos
	}{{95, 79, 0}, {95, 72, 1}, {95, 60, 2}, {95, 90, 0}} {
		if got := text.PosAt(c.x, c.y); got != c.want {
			t.Errorf("PosAt(%g, %g) = %d, want %d", c.x, c.y, got, c.want)
		}
	}
}

func TestQuads(t *testing.T) {
	text := layoutPage(t, selectPage, TextOptions{}, "")
	qs := text.Quads(TextRange{2, 18})
	if len(qs) != 2 {
		t.Fatalf("%d quads", len(qs))
	}
	l0, l1 := text.Lines[0].Quad, text.Lines[1].Quad
	// From the left edge of the first l to the end of the line, as high
	// as the line; then from the start of the next line to the r of
	// "ful wor".
	if q := qs[0].P; !approx(q[0][0], text.Chars[2].Quad.P[0][0]) || !approx(q[1][0], l0.P[1][0]) || q[0][1] != l0.P[0][1] || q[2][1] != l0.P[2][1] {
		t.Errorf("first quad %v, line %v", q, l0)
	}
	if q := qs[1].P; !approx(q[0][0], l1.P[0][0]) || !approx(q[1][0], text.Chars[17].Quad.P[1][0]) || q[2][1] != l1.P[2][1] {
		t.Errorf("second quad %v, line %v", q, l1)
	}
	if text.Quads(TextRange{3, 3}) != nil {
		t.Error("quads of an empty range")
	}
}

func TestInRect(t *testing.T) {
	text := layoutPage(t, selectPage, TextOptions{}, "")
	// Around "Other", and the start of both lines of the first block:
	// the characters whose centres are inside.
	rs := text.InRect(Rect{18, 190, 46, 205})
	if len(rs) != 1 || text.TextOf(rs[0], TextFormat{}) != "Other" {
		t.Errorf("ranges %v", rs)
	}
	rs = text.InRect(Rect{18, 30, 36, 60})
	if len(rs) != 2 || text.TextOf(rs[0], TextFormat{}) != "Hel" || text.TextOf(rs[1], TextFormat{}) != "ful " {
		t.Errorf("column: %v %q", rs, []string{text.TextOf(rs[0], TextFormat{})})
	}
}

func TestSelectActualText(t *testing.T) {
	text := layoutPage(t, "BT /F1 10 Tf 20 360 Td (x) Tj /Span <</ActualText (fi)>> BDC (AB) Tj EMC (y) Tj ET", TextOptions{}, "")
	if s := text.String(); s != "xfiy" {
		t.Fatalf("text %q", s)
	}
	// Half the sequence is all of it.
	if r := text.Select(2, 3, SelectChars); r != (TextRange{1, 3}) || text.TextOf(r, TextFormat{}) != "fi" {
		t.Errorf("select in the sequence %v", r)
	}
	if r := text.WordAt(2); r != (TextRange{0, 4}) {
		t.Errorf("word %v", r)
	}
}

func TestOffsets(t *testing.T) {
	text := layoutPage(t, selectPage, TextOptions{}, "")
	s := text.String() // "Hello, wonder-\nful world.\n\nOther block"
	for p, want := range map[TextPos]int{0: 0, 7: 7, 13: 13, 14: 15, 24: 27, 35: 38, 99: 38, -1: 0} {
		if got := text.Offset(p); got != want {
			t.Errorf("Offset(%d) = %d, want %d", p, got, want)
		}
	}
	for n, want := range map[int]TextPos{0: 0, 7: 7, 14: 14, 15: 14, 25: 24, 26: 24, 27: 24, 38: 35, 100: 35} {
		if got := text.PosAtOffset(n); got != want {
			t.Errorf("PosAtOffset(%d) = %d, want %d", n, got, want)
		}
	}
	// From a range to bytes of the text and back.
	for _, r := range []TextRange{{7, 13}, {2, 18}, {0, 35}, {24, 29}, {14, 14}} {
		lo, hi := text.Offset(r.Start), text.Offset(r.End)
		if got := text.RangeOfOffsets(lo, hi); got != r {
			t.Errorf("%v → %q → %v", r, s[lo:hi], got)
		}
		if r.Empty() {
			continue
		}
		if got := text.TextOf(r, TextFormat{}); got != s[lo:hi] {
			t.Errorf("%v: TextOf %q, bytes %q", r, got, s[lo:hi])
		}
	}
	// Bytes inside a character take it whole.
	if r := text.RangeOfOffsets(8, 10); r != (TextRange{8, 10}) {
		t.Errorf("RangeOfOffsets(8, 10) = %v", r)
	}

	at := layoutPage(t, "BT /F1 10 Tf 20 360 Td (x) Tj /Span <</ActualText (fi)>> BDC (AB) Tj EMC (y) Tj ET", TextOptions{}, "")
	if r := at.RangeOfOffsets(2, 3); r != (TextRange{1, 3}) {
		t.Errorf("in the /ActualText: %v", r)
	}
	if at.Offset(2) != 3 || at.Offset(3) != 3 {
		t.Errorf("offsets in the sequence: %d %d", at.Offset(2), at.Offset(3))
	}
}

func TestPageTextConcurrent(t *testing.T) {
	text := layoutPage(t, selectPage, TextOptions{}, "")
	done := make(chan string)
	for range 8 {
		go func() {
			_ = text.PosAtOffset(20)
			done <- text.String()
		}()
	}
	for range 8 {
		if s := <-done; s != "Hello, wonder-\nful world.\n\nOther block" {
			t.Errorf("text %q", s)
		}
	}
	if r := (&PageText{Chars: text.Chars}).WordAt(3); r != (TextRange{}) {
		t.Errorf("WordAt without lines %v", r)
	}
}
