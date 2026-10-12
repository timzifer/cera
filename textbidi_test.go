package cera

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// bidiFont is Helvetica whose codes a to k stand for Hebrew letters and
// Arabic presentation forms, as the fonts of right-to-left text do:
//
//	a alef  b bet  c gimel  d shin  e lamed  f vav  g final mem
//	h lam-alef, a ligature (U+FEFB)  i beh, initial form (U+FE91)
//	j alef, final form (U+FE8E)  k lam-khah, initial form (U+FCCB)
//
// Other codes are what WinAnsiEncoding says: Latin letters, digits,
// brackets.
var bidiFont = []string{
	"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding /ToUnicode 101 0 R >>",
	stream("", "/CIDInit /ProcSet findresource begin 12 dict begin begincmap /CMapName /T def /CMapType 2 def\n"+
		"1 begincodespacerange <00> <FF> endcodespacerange\n"+
		"11 beginbfchar\n<61> <05D0>\n<62> <05D1>\n<63> <05D2>\n<64> <05E9>\n<65> <05DC>\n<66> <05D5>\n<67> <05DD>\n"+
		"<68> <FEFB>\n<69> <FE91>\n<6A> <FE8E>\n<6B> <FCCB>\nendbfchar\nendcmap end end"),
}

// bidiPage lays out content on a page of 400 by 400 points with bidiFont
// as F1; catalog is added to the catalogue.
func bidiPage(t *testing.T, content, catalog string) *PageText {
	t.Helper()
	data := buildPDFCatalog(catalog, []string{content}, "/MediaBox [0 0 400 400] /Resources << /Font << /F1 100 0 R >> >>", bidiFont...)
	doc, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	text, err := p.Text(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	checkLayout(t, text)
	return text
}

// TestBidiVisualOrder reads text shown from left to right, as it stands on
// the page, in the order it is read: שלום is shown as its letters from the
// last to the first.
func TestBidiVisualOrder(t *testing.T) {
	text := bidiPage(t, "BT /F1 12 Tf 100 360 Td (gfed) Tj ET", "")
	if got := text.String(); got != "שלום" {
		t.Errorf("text %q, want %q", got, "שלום")
	}
	if len(text.Lines) != 1 || !text.Lines[0].RTL {
		t.Fatalf("lines %+v", text.Lines)
	}
	// In logical order the first character is the rightmost.
	if a, b := text.Chars[0], text.Chars[3]; a.Text != "ש" || b.Text != "ם" || a.Origin[0] <= b.Origin[0] {
		t.Errorf("first %q at %g, last %q at %g", a.Text, a.Origin[0], b.Text, b.Origin[0])
	}
	if ms := text.Find("שלום", SearchOptions{}); len(ms) != 1 {
		t.Errorf("%d matches", len(ms))
	}
	// A line without right-to-left text is as it was.
	latin := bidiPage(t, "BT /F1 12 Tf 100 360 Td (xyz \\(1\\)) Tj ET", "")
	if latin.String() != "xyz (1)" || latin.Lines[0].RTL || latin.Chars[0].rtl {
		t.Errorf("latin %q, line %+v", latin.String(), latin.Lines[0])
	}
}

// TestBidiLogicalOrder reads the same from a writer that shows the letters
// in the order they are read, moving the pen left for each.
func TestBidiLogicalOrder(t *testing.T) {
	text := bidiPage(t, "BT /F1 12 Tf 200 360 Td (d) Tj -6.672 0 Td (e) Tj -3.336 0 Td (f) Tj -6.672 0 Td (g) Tj ET", "")
	if got := text.String(); got != "שלום" {
		t.Errorf("text %q, want %q", got, "שלום")
	}
}

// TestBidiMixed keeps numbers and Latin words inside right-to-left text
// the way they read.
func TestBidiMixed(t *testing.T) {
	// On the page, from the left: xy in brackets, 12, and אבג reversed.
	// Read from the right: אבג, 12, (xy). The brackets read from the
	// right and are drawn mirrored; their glyphs say which they are.
	text := bidiPage(t, "BT /F1 12 Tf 100 360 Td (\\)xy\\( 12 cba) Tj ET", "")
	if got, want := text.String(), "אבג 12 (xy)"; got != want {
		t.Errorf("text %q, want %q", got, want)
	}
	if !text.Lines[0].RTL {
		t.Error("the line does not read from the right")
	}

	// A right-to-left phrase in a line of Latin text: the line reads from
	// the left and the phrase from the right; the brackets around it
	// belong to the line and face as they are shown.
	text = bidiPage(t, "BT /F1 12 Tf 100 360 Td (TOP ROW \\(cba\\) NOW) Tj ET", "")
	if got, want := text.String(), "TOP ROW (אבג) NOW"; got != want {
		t.Errorf("text %q, want %q", got, want)
	}
	if text.Lines[0].RTL {
		t.Error("the line reads from the right")
	}

	// A selection across the directions is one box for each stretch.
	all := text.Quads(text.All())
	if len(all) != 3 {
		t.Fatalf("%d boxes for the line, want 3", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].Bounds().X0 < all[i-1].Bounds().X1-1e-6 {
			t.Errorf("box %d starts at %g, before the end %g of the one before", i, all[i].Bounds().X0, all[i-1].Bounds().X1)
		}
	}
}

// TestBidiPresentationForms gives the letters for the forms the glyphs
// are, and the letters of a ligature their places from the right.
func TestBidiPresentationForms(t *testing.T) {
	// From the left: lam-alef, beh: read "بلا".
	text := bidiPage(t, "BT /F1 12 Tf 100 360 Td (hi) Tj ET", "")
	if got, want := text.String(), "بلا"; got != want {
		t.Errorf("text %q (%U), want %q", got, []rune(got), want)
	}
	if len(text.Chars) != 3 {
		t.Fatalf("%d characters", len(text.Chars))
	}
	beh, lam, alef := text.Chars[0], text.Chars[1], text.Chars[2]
	if !(beh.Origin[0] > lam.Origin[0] && lam.Origin[0] > alef.Origin[0]) {
		t.Errorf("beh at %g, lam at %g, alef at %g: not from the right", beh.Origin[0], lam.Origin[0], alef.Origin[0])
	}
	if lam.Box.X0 < alef.Box.X1-1e-6 || lam.Box.Dx() <= 0 {
		t.Errorf("lam %v and alef %v share the box of the ligature badly", lam.Box, alef.Box)
	}
	// "الخ": alef, then the ligature lam-khah.
	text = bidiPage(t, "BT /F1 12 Tf 100 360 Td (kj) Tj ET", "")
	if got, want := text.String(), "الخ"; got != want {
		t.Errorf("text %q (%U), want %q", got, []rune(got), want)
	}
	if ms := text.Find("الخ", SearchOptions{}); len(ms) != 1 {
		t.Errorf("%d matches", len(ms))
	}
}

// TestBidiPosAt selects against the pointer no more: the start of a line
// that reads from the right is at its right end.
func TestBidiPosAt(t *testing.T) {
	text := bidiPage(t, "BT /F1 12 Tf 100 360 Td (gfed) Tj ET", "")
	l := text.Lines[0]
	b := l.Quad.Bounds()
	y := (b.Y0 + b.Y1) / 2
	if got := text.PosAt(b.X1+20, y); got != 0 {
		t.Errorf("right of the line: position %d, want 0", got)
	}
	if got := text.PosAt(b.X0-20, y); int(got) != len(text.Chars) {
		t.Errorf("left of the line: position %d, want %d", got, len(text.Chars))
	}
	// The second letter: before it in its right half, after it in its left.
	c := text.Chars[1].Box
	if got := text.PosAt(c.X1-0.5, y); got != 1 {
		t.Errorf("right half of the second letter: position %d, want 1", got)
	}
	if got := text.PosAt(c.X0+0.5, y); got != 2 {
		t.Errorf("left half of the second letter: position %d, want 2", got)
	}
	// A drag from the right end to the middle selects the first letters.
	r := text.Select(text.PosAt(b.X1+5, y), text.PosAt(c.X0+0.5, y), SelectChars)
	if got := text.TextOf(r, TextFormat{}); got != "של" {
		t.Errorf("selected %q, want %q", got, "של")
	}
}

// TestBidiColumns reads the columns of a right-to-left page from the
// right, by what its lines are or by what the document says.
func TestBidiColumns(t *testing.T) {
	column := func(x int, lines ...string) string {
		var b strings.Builder
		fmt.Fprintf(&b, "BT /F1 12 Tf 14 TL %d 360 Td ", x)
		for _, l := range lines {
			fmt.Fprintf(&b, "(%s) Tj T* ", l)
		}
		return b.String() + "ET "
	}
	// Left column "גבא גבא", right column "םולש םולש", two lines each.
	text := bidiPage(t, column(40, "cba cba", "cba cba")+column(240, "gfed gfed", "gfed gfed"), "")
	if got, want := text.String(), "שלום שלום\nשלום שלום\n\nאבג אבג\nאבג אבג"; got != want {
		t.Errorf("text %q, want %q", got, want)
	}
	// Latin columns read from the left, unless the document says R2L.
	latin := column(40, "LEFT ONE", "LEFT TWO") + column(240, "RIGHT ONE", "RIGHT TWO")
	if got, want := bidiPage(t, latin, "").String(), "LEFT ONE\nLEFT TWO\n\nRIGHT ONE\nRIGHT TWO"; got != want {
		t.Errorf("text %q, want %q", got, want)
	}
	if got, want := bidiPage(t, latin, "/ViewerPreferences << /Direction /R2L >>").String(), "RIGHT ONE\nRIGHT TWO\n\nLEFT ONE\nLEFT TWO"; got != want {
		t.Errorf("R2L: text %q, want %q", got, want)
	}
}
