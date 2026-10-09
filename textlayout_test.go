package cera

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// layoutPage extracts the text of a 400×400 page with font F1 (object 100)
// and checks that its lines and blocks cover it.
func layoutPage(t *testing.T, content string, opt TextOptions, catalog string, objs ...string) *PageText {
	t.Helper()
	data := buildPDFCatalog(catalog, []string{content}, "/MediaBox [0 0 400 400] /Resources << /Font << /F1 100 0 R >> >>", append([]string{helvetica}, objs...)...)
	doc, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	text, err := p.TextWith(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	checkLayout(t, text)
	return text
}

// checkLayout checks that the blocks cover the lines and the lines the
// characters, in order, and that each line's quad holds its characters.
func checkLayout(t *testing.T, text *PageText) {
	t.Helper()
	line := 0
	for bi, b := range text.Blocks {
		if b.Start != line || b.End <= b.Start {
			t.Fatalf("block %d: lines %d:%d after line %d", bi, b.Start, b.End, line)
		}
		line = b.End
		for li := b.Start; li < b.End; li++ {
			if text.Lines[li].Block != bi {
				t.Errorf("line %d: block %d, in block %d", li, text.Lines[li].Block, bi)
			}
		}
	}
	if line != len(text.Lines) {
		t.Fatalf("blocks end at line %d of %d", line, len(text.Lines))
	}
	char := 0
	for li, l := range text.Lines {
		if l.Start != char || l.End <= l.Start {
			t.Fatalf("line %d: chars %d:%d after char %d", li, l.Start, l.End, char)
		}
		char = l.End
		r := l.Quad.Bounds()
		for i := l.Start; i < l.End; i++ {
			o := text.Chars[i].Origin
			if o[0] < r.X0-0.01 || o[0] > r.X1+0.01 || o[1] < r.Y0-0.01 || o[1] > r.Y1+0.01 {
				t.Errorf("line %d: char %d at %v outside %v", li, i, o, r)
			}
		}
	}
	if char != len(text.Chars) {
		t.Fatalf("lines end at char %d of %d", char, len(text.Chars))
	}
}

func TestLayoutColumns(t *testing.T) {
	title := "BT /F1 14 Tf 40 380 Td (Title of the page across both columns) Tj ET\n"
	left := []string{"left one", "left two", "left three"}
	right := []string{"right one", "right two", "right three"}
	line := func(x, y int, s string) string { return fmt.Sprintf("BT /F1 10 Tf %d %d Td (%s) Tj ET\n", x, y, s) }
	want := "Title of the page across both columns\n\nleft one\nleft two\nleft three\n\nright one\nright two\nright three"

	// Column by column, and row by row across the columns.
	var byColumn, byRow strings.Builder
	byColumn.WriteString(title)
	byRow.WriteString(title)
	for i := range left {
		byColumn.WriteString(line(20, 340-14*i, left[i]))
		byRow.WriteString(line(20, 340-14*i, left[i]))
		byRow.WriteString(line(210, 340-14*i, right[i]))
	}
	for i := range right {
		byColumn.WriteString(line(210, 340-14*i, right[i]))
	}
	for name, c := range map[string]string{"by column": byColumn.String(), "by row": byRow.String()} {
		text := layoutPage(t, c, TextOptions{}, "")
		if s := text.String(); s != want {
			t.Errorf("%s: %q", name, s)
		}
		if len(text.Blocks) != 3 || len(text.Lines) != 7 {
			t.Errorf("%s: %d blocks, %d lines", name, len(text.Blocks), len(text.Lines))
		}
	}
}

func TestLayoutLines(t *testing.T) {
	for _, c := range []struct{ name, content, want string }{
		{"out of order", "BT /F1 10 Tf 46 50 Td (world) Tj ET BT /F1 10 Tf 20 50 Td (Hello) Tj ET", "Hello world"},
		{"fake bold", "BT /F1 10 Tf 20 50 Td (Bold) Tj 0.3 0 Td (Bold) Tj ET", "Bold"},
		{"fill then stroke", "BT /F1 10 Tf 20 50 Td (Ink) Tj ET BT 1 Tr /F1 10 Tf 20 50 Td (Ink) Tj ET", "Ink"},
		{"word space", "BT /F1 10 Tf 20 50 Td [(Hello) -300 (World)] TJ ET", "Hello World"},
		{"kerning", "BT /F1 10 Tf 20 50 Td [(Ke) -50 (rn)] TJ ET", "Kern"},
		{"superscript", "BT /F1 10 Tf 20 50 Td (x) Tj 4 Ts 7 Tf (2) Tj 0 Ts 10 Tf ( + y) Tj ET", "x2 + y"},
		{"columns of a table", "BT /F1 10 Tf 20 50 Td (Name) Tj 200 0 Td (Value) Tj ET", "Name\n\nValue"},
		{"blank line", "BT /F1 10 Tf 20 50 Td (a) Tj 0 -12 Td (   ) Tj 0 -12 Td (b) Tj ET", "a\n\nb"},
	} {
		text := layoutPage(t, c.content, TextOptions{}, "")
		if s := text.String(); s != c.want {
			t.Errorf("%s: %q, want %q", c.name, s, c.want)
		}
	}
}

func TestLayoutLigature(t *testing.T) {
	// A glyph for three characters, and one for U+FB01, the fi ligature.
	cmap := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap /CMapName /L def 1 begincodespacerange <00> <FF> endcodespacerange 2 beginbfchar <41> <006600660069> <42> <FB01> endbfchar endcmap CMapName currentdict /CMap defineresource pop end end"
	font := "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding /ToUnicode 102 0 R >>"
	data := buildPDF([]string{"BT /F2 10 Tf 20 50 Td (xAyB) Tj ET"}, "/Resources << /Font << /F2 101 0 R >> >>",
		helvetica, font, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(cmap), cmap))
	text := pageText(t, data, nil)
	checkLayout(t, text)
	if s := text.String(); s != "xffiyfi" || len(text.Chars) != 7 {
		t.Fatalf("%q in %d chars", s, len(text.Chars))
	}
	f1, f2, i := text.Chars[1], text.Chars[2], text.Chars[3]
	if !approx(f1.Advance*3, 0.667*10) || f1.Quad.P[1] != f2.Quad.P[0] || f2.Quad.P[3] != i.Quad.P[2] {
		t.Errorf("parts of the ligature: advance %g, quads %v %v %v", f1.Advance, f1.Quad, f2.Quad, i.Quad)
	}
	if !approx(f2.Origin[0], f1.Origin[0]+f1.Advance) || f1.Code != 'A' || f2.Code != 'A' {
		t.Errorf("origins %v %v, codes %d %d", f1.Origin, f2.Origin, f1.Code, f2.Code)
	}
}

func TestLayoutActualText(t *testing.T) {
	// A word hyphenated across two lines, replaced as a whole; an
	// /ActualText inside it is part of it.
	c := `BT /F1 10 Tf 20 60 Td /Span <</ActualText (Drucker)>> BDC (Dru-) Tj
		0 -12 Td /Span <</ActualText (X)>> BDC (ck) Tj EMC (er) Tj EMC ( xx) Tj ET`
	text := layoutPage(t, c, TextOptions{}, "")
	if s := text.String(); s != "Drucker\nxx" {
		t.Errorf("text %q", s)
	}
	if len(text.Chars) != 11 || text.Chars[0].Text != "Drucker" {
		t.Fatalf("%d chars, first %q", len(text.Chars), text.Chars[0].Text)
	}
	for i := 1; i < 8; i++ {
		if c := text.Chars[i]; c.Text != "" || c.group != text.Chars[0].group || c.group == 0 {
			t.Errorf("char %d: %q in group %d", i, c.Text, c.group)
		}
	}
	if text.Chars[8].group != 0 {
		t.Error("the space after the sequence is in its group")
	}
}

func TestLayoutHyphenated(t *testing.T) {
	c := `BT /F1 10 Tf 20 360 Td (Silben-) Tj 0 -12 Td (trennung) Tj ET
		BT /F1 10 Tf 20 200 Td (Haus-) Tj 0 -12 Td (T\374r) Tj ET
		BT /F1 10 Tf 20 100 Td (dash -) Tj 0 -12 Td (next) Tj ET`
	text := layoutPage(t, c, TextOptions{}, "")
	if len(text.Lines) != 6 {
		t.Fatalf("%d lines: %q", len(text.Lines), text.String())
	}
	for i, want := range []bool{true, false, false, false, false, false} {
		if text.Lines[i].Hyphenated != want {
			t.Errorf("line %d hyphenated %v", i, !want)
		}
	}
}

func TestLayoutStructure(t *testing.T) {
	// The tree reads the lower paragraph first. A page number is an
	// artifact, and the tree's references to another page and to a form
	// XObject's content are not this page's.
	c := `/P <</MCID 0>> BDC BT /F1 10 Tf 20 300 Td (First) Tj ET EMC
		/P <</MCID 1>> BDC BT /F1 10 Tf 20 100 Td (Second) Tj ET EMC
		/Artifact BMC BT /F1 10 Tf 190 10 Td (7) Tj ET EMC`
	tree := []string{
		"<< /Type /StructTreeRoot /K [102 0 R 103 0 R] >>",
		"<< /S /P /Pg 10 0 R /K [<< /Type /MCR /MCID 0 /Stm 104 0 R >> 1] >>",
		"<< /S /Sect /K [<< /S /P /Pg 105 0 R /K 1 >> << /S /P /K << /Type /MCR /MCID 0 /Pg 10 0 R >> >>] >>",
		"<< /Type /XObject /Subtype /Form /BBox [0 0 1 1] /Length 0 >>\nstream\n\nendstream",
		"<< /Type /Page >>",
	}
	for _, c2 := range []struct {
		name    string
		catalog string
		opt     TextOptions
		want    string
	}{
		{"tagged", "/StructTreeRoot 101 0 R /MarkInfo << /Marked true >>", TextOptions{}, "Second\n\nFirst\n\n7"},
		{"ignored", "/StructTreeRoot 101 0 R", TextOptions{IgnoreStructure: true}, "First\n\nSecond\n\n7"},
		{"suspect", "/StructTreeRoot 101 0 R /MarkInfo << /Marked true /Suspects true >>", TextOptions{}, "First\n\nSecond\n\n7"},
		{"untagged", "", TextOptions{}, "First\n\nSecond\n\n7"},
	} {
		text := layoutPage(t, c, c2.opt, c2.catalog, tree...)
		if s := text.String(); s != c2.want {
			t.Errorf("%s: %q", c2.name, s)
		}
	}
}

func TestLayoutDirections(t *testing.T) {
	// A label up the left margin, drawn first, comes after the text; a
	// page of turned text reads in its own direction.
	c := `BT /F1 10 Tf 0 1 -1 0 15 100 Tm (arXiv label) Tj ET
		BT /F1 10 Tf 40 300 Td (Body one) Tj 0 -12 Td (Body two) Tj ET`
	if s := layoutPage(t, c, TextOptions{}, "").String(); s != "Body one\nBody two\n\narXiv label" {
		t.Errorf("text %q", s)
	}
	// Two columns turned 90° clockwise: lines run down the page, the
	// next line is to their left, the second column below the first.
	turned := `BT /F1 10 Tf 0 -1 1 0 300 380 Tm (one a) Tj 0 -12 Td (one b) Tj ET
		BT /F1 10 Tf 0 -1 1 0 300 150 Tm (two a) Tj 0 -12 Td (two b) Tj ET`
	text := layoutPage(t, turned, TextOptions{}, "")
	if s := text.String(); s != "one a\none b\n\ntwo a\ntwo b" {
		t.Errorf("turned text %q", s)
	}
	if d := text.Lines[0].Dir; !approx(d[0], 0) || !approx(d[1], 1) {
		t.Errorf("turned line runs %v", d)
	}
}

// BenchmarkPageText extracts and lays out a page of two columns of 60
// lines, shown row by row across the columns.
func BenchmarkPageText(b *testing.B) {
	var c strings.Builder
	for i := range 60 {
		fmt.Fprintf(&c, "BT /F1 9 Tf 20 %d Td (Lorem ipsum dolor sit amet, consectetur adipiscing) Tj ET\n", 780-12*i)
		fmt.Fprintf(&c, "BT /F1 9 Tf 320 %d Td (sed do eiusmod tempor incididunt ut labore et dolore) Tj ET\n", 780-12*i)
	}
	data := buildPDF([]string{c.String()}, "/MediaBox [0 0 612 792] /Resources << /Font << /F1 100 0 R >> >>", helvetica)
	doc, err := Open(data)
	if err != nil {
		b.Fatal(err)
	}
	p, _ := doc.Page(0)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := p.Text(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
