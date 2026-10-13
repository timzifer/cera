package cera

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
)

// structFile is a tagged document of two pages: a heading and a paragraph
// with a link on the first, the paragraph's end, a figure, a table and a
// list on the second.
func structFile(catalog string, more map[int]string) []byte {
	objs := map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R /Lang (de-DE) " + catalog + ">>",
		2: "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 300 200] /Resources <</Font <</F1 6 0 R>>>>>>",
		3: "<</Type /Page /Parent 2 0 R /Contents 5 0 R /Annots [9 0 R 10 0 R]>>",
		4: "<</Type /Page /Parent 2 0 R /Contents 7 0 R>>",
		5: testpdf.Stream("", []byte("BT /F1 12 Tf 20 170 Td /H1 <</MCID 0>> BDC (Title) Tj EMC 0 -20 Td /P <</MCID 1>> BDC (One ) Tj EMC"+
			" /Artifact BMC (7) Tj EMC /P <</MCID 2>> BDC (two ) Tj EMC /Link <</MCID 3>> BDC (there) Tj EMC ET")),
		6:  "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
		7:  testpdf.Stream("", []byte("BT /F1 12 Tf 20 170 Td /P <</MCID 0>> BDC (three.) Tj EMC 0 -40 Td /TH <</MCID 2>> BDC (Sum) Tj EMC ET")),
		9:  "<</Type /Annot /Subtype /Text /Rect [0 0 10 10]>>",
		10: "<</Type /Annot /Subtype /Link /Rect [60 145 90 160] /Dest [4 0 R /Fit]>>",

		20: "<</Type /StructTreeRoot /K 21 0 R /RoleMap <</Heading1 /H1 /Para /P /Round /Robin /Robin /Round>>" +
			" /ClassMap <</wide <</O /Table /ColSpan 3 /RowSpan 5>> /both [<</O /Table /Scope /Both>> 0 <</O /List /ListNumbering /Disc>>]>>>>",
		21: "<</Type /StructElem /S /Document /K [22 0 R 23 0 R 24 0 R 27 0 R 31 0 R 32 0 R 21 0 R 22 0 R (no element) 33 0 R]>>",
		22: "<</Type /StructElem /S /Heading1 /T (The title) /Pg 3 0 R /K 0 /Lang (en-US) /ID (h1)>>",
		23: "<</Type /StructElem /S /Para /Pg 3 0 R /K [1 <</Type /MCR /MCID 2>> 25 0 R <</Type /MCR /Pg 4 0 R /MCID 0>> 1.5 -1]>>",
		25: "<</S /Link /K [<</Type /MCR /MCID 3>> <</Type /OBJR /Obj 10 0 R>> <</Type /OBJR /Obj 6 0 R>> <</Type /MCR /MCID 4 /Stm 5 0 R>>]>>",
		24: "<</S /Figure /Alt <FEFF00420069006C0064> /ActualText (x) /E (short for) /Pg 4 0 R /K 1 /A <</O /Layout /BBox [10 20 110 120]>> /ID (h1)>>",
		27: "<</S /Table /Pg 4 0 R /A [<</O /Table /Summary (Sums)>> 0] /K 28 0 R>>",
		28: "<</S /TR /K [29 0 R 30 0 R]>>",
		29: "<</S /TH /ID (th) /A <</O /Table /Scope /Column /RowSpan 2>> /C /wide /K 2>>",
		30: "<</S /TD /A <</O /Table /Headers [(th) 7 (h1)] /ColSpan 0>> /C [/both 0 /none]>>",
		31: "<</S /L /A <</O /List /ListNumbering /Decimal>> /C /both /K <</S /LI /Lang ()>>>>",
		32: "<</S /chapter /NS 40 0 R /K [<</S /deep /NS 40 0 R>> <</S /other /NS 40 0 R>> <</S /Round>> <</S /Para /NS 41 0 R>>]>>",
		33: "<</K 34 0 R>>",
		34: "<</S /Span /Pg 99 0 R /K 5>>",
		40: "<</Type /Namespace /NS (urn:example) /RoleMapNS <</chapter /Sect /deep [/para 41 0 R]>>>>",
		41: "<</Type /Namespace /NS (urn:deeper) /RoleMapNS <</para /P>>>>",
	}
	for n, o := range more {
		objs[n] = o
	}
	return testpdf.File{Objs: objs, Trailer: "/Root 1 0 R"}.Bytes()
}

// structString writes the tree below e, an element a line.
func structString(b *strings.Builder, e *StructElem, depth int) {
	fmt.Fprintf(b, "%s%s", strings.Repeat("  ", depth), e.Type)
	if e.Role != e.Type {
		fmt.Fprintf(b, "=%s", e.Role)
	}
	fmt.Fprintf(b, " p%d %s", e.Page, e.Lang)
	for _, k := range e.Kids {
		switch {
		case k.Elem != nil:
			if k.Page != k.Elem.Page || k.MCID != -1 || k.Annot != -1 {
				fmt.Fprintf(b, " BAD%+v", k)
			}
		case k.MCID >= 0 && k.Annot == -1:
			fmt.Fprintf(b, " %d:%d", k.Page, k.MCID)
		case k.Annot >= 0 && k.MCID == -1:
			fmt.Fprintf(b, " %d:a%d", k.Page, k.Annot)
		default:
			fmt.Fprintf(b, " BAD%+v", k)
		}
	}
	b.WriteByte('\n')
	for c := range e.Children() {
		structString(b, c, depth+1)
	}
}

func TestStructure(t *testing.T) {
	doc := editOpen(t, structFile("/StructTreeRoot 20 0 R /MarkInfo <</Marked true>>", nil))
	tree := doc.Structure()
	if tree == nil || len(tree.Elems) != 1 || tree.Suspects || tree.Truncated {
		t.Fatalf("tree %+v", tree)
	}
	if doc.Structure() != tree {
		t.Error("the tree is read again")
	}
	var b strings.Builder
	structString(&b, tree.Elems[0], 0)
	// The element named twice is there once, the tree does not go round,
	// and what is no element, no content of a page or content of a form is
	// left out.
	want := `Document p-1 de-DE
  Heading1=H1 p0 en-US 0:0
  Para=P p-1 de-DE 0:1 0:2 1:0
    Link p0 de-DE 0:3 0:a1
  Figure p1 de-DE 1:1
  Table p1 de-DE
    TR p1 de-DE
      TH p1 de-DE 1:2
      TD p1 de-DE
  L p-1 de-DE
    LI p-1 de-DE
  chapter=Sect p-1 de-DE
    deep=P p-1 de-DE
    other p-1 de-DE
    Round p-1 de-DE
    Para p-1 de-DE
   p-1 de-DE
    Span p-1 de-DE
`
	if got := b.String(); got != want {
		t.Errorf("tree:\n%s\nwant:\n%s", got, want)
	}

	kids := tree.Elems[0].Kids
	h1, fig, table, list := kids[0].Elem, kids[2].Elem, kids[3].Elem, kids[4].Elem
	if h1.Title != "The title" || h1.ID != "h1" || tree.ByID("h1") != h1 || tree.ByID("nothing") != nil {
		t.Errorf("heading %+v", h1)
	}
	if fig.Alt != "Bild" || fig.ActualText != "x" || fig.Expansion != "short for" {
		t.Errorf("figure %+v", fig)
	}
	plain := StructAttr{RowSpan: 1, ColSpan: 1, ListNumbering: "None"}
	with := func(f func(*StructAttr)) StructAttr {
		a := plain
		f(&a)
		return a
	}
	th, td := table.Kids[0].Elem.Kids[0].Elem, table.Kids[0].Elem.Kids[1].Elem
	for _, c := range []struct {
		e    *StructElem
		want StructAttr
	}{
		{h1, plain},
		{fig, with(func(a *StructAttr) { a.BBox = Rect{10, 20, 110, 120} })},
		{table, with(func(a *StructAttr) { a.Summary = "Sums" })},
		// Its own attributes before those of its class.
		{th, with(func(a *StructAttr) { a.RowSpan, a.ColSpan, a.Scope = 2, 3, "Column" })},
		{td, with(func(a *StructAttr) { a.Headers, a.Scope, a.ListNumbering = []string{"th", "h1"}, "Both", "Disc" })},
		{list, with(func(a *StructAttr) { a.ListNumbering, a.Scope = "Decimal", "Both" })},
	} {
		if got := c.e.Attr(); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("attributes of %s: %+v, want %+v", c.e.Type, got, c.want)
		}
	}
	if tree.ByID("th") != th {
		t.Error("the header cell is not found by its identifier")
	}

	// The text of an element is that of its marked content.
	p0, _ := doc.Page(0)
	text, err := p0.Text(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	marked := map[int]string{}
	for _, c := range text.Chars {
		marked[c.MCID] += c.Text
	}
	if marked[h1.Kids[0].MCID] != "Title" || marked[3] != "there" {
		t.Errorf("marked text %v", marked)
	}
	// The annotation of the link.
	link := kids[1].Elem.Kids[2].Elem.Kids[1]
	if a := p0.Annotations(); link.Annot != 1 || a[link.Annot].Index != 1 || a[link.Annot].Subtype != "Link" {
		t.Errorf("the link names annotation %d", link.Annot)
	}
}

func TestStructureOdd(t *testing.T) {
	if tree := editOpen(t, structFile("", nil)).Structure(); tree != nil {
		t.Errorf("an untagged document has the tree %+v", tree)
	}
	if tree := editOpen(t, structFile("/StructTreeRoot 20 0 R /MarkInfo <</Suspects true>>", nil)).Structure(); tree == nil || !tree.Suspects || len(tree.Elems) != 1 {
		t.Errorf("a suspect tree: %+v", tree)
	}
	// A root without elements, with content of its own, and none at all.
	for _, root := range []string{"<</Type /StructTreeRoot>>", "<</Type /StructTreeRoot /K [3 (x) <</Type /MCR /MCID 1 /Pg 3 0 R>>]>>", "[1 2]"} {
		tree := editOpen(t, structFile("/StructTreeRoot 20 0 R", map[int]string{20: root})).Structure()
		if (tree == nil) != (root == "[1 2]") || tree != nil && (len(tree.Elems) != 0 || tree.Truncated) {
			t.Errorf("root %s: %+v", root, tree)
		}
	}

	// Deeper than is read.
	deep := map[int]string{20: "<</Type /StructTreeRoot /K 100 0 R>>"}
	for i := range maxStructDepth + 20 {
		deep[100+i] = fmt.Sprintf("<</S /Div /K [%d 0 R <</S /Span>>]>>", 101+i)
	}
	tree := editOpen(t, structFile("/StructTreeRoot 20 0 R", deep)).Structure()
	if tree == nil || !tree.Truncated || len(tree.Elems) != 1 {
		t.Fatalf("a deep tree: %+v", tree)
	}
	levels, spans := 0, 0
	for e := tree.Elems[0]; e != nil; levels++ {
		var next *StructElem
		for c := range e.Children() {
			if c.Type == "Span" {
				spans++
			} else {
				next = c
			}
		}
		e = next
	}
	// The elements beside those too deep are read.
	if levels != maxStructDepth || spans != maxStructDepth-1 {
		t.Errorf("%d levels and %d spans of a deep tree, want %d", levels, spans, maxStructDepth)
	}
}
