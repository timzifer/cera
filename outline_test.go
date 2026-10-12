package cera

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
)

// outlineFile is a document of three pages with an outline: a chapter
// with two sections and an entry under one of them, entries that lead to
// an explicit and a named destination, through actions, to a structure
// element and nowhere, and a chain that ends in a cycle.
func outlineFile() []byte {
	return testpdf.File{
		Objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R /Outlines 10 0 R /Dests <</second [4 0 R /FitH 700]>>>>",
			2: "<</Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 /MediaBox [0 0 600 800]>>",
			3: "<</Type /Page /Parent 2 0 R>>",
			4: "<</Type /Page /Parent 2 0 R>>",
			5: "<</Type /Page /Parent 2 0 R>>",
			// /Last, /Prev, /Parent and a negative or wrong /Count are not needed.
			10: "<</Type /Outlines /First 11 0 R /Last 99 0 R /Count 42>>",
			11: "<</Title (Chapter) /Parent 10 0 R /Next 12 0 R /First 20 0 R /Count 2 /Dest [3 0 R /XYZ 0 800 null] /F 2 /C [1 0 0]>>",
			12: "<</Title <FEFF00DC0062006500720073006900630068007400200031> /Next 13 0 R /Prev 5 0 R /Dest /second /F 1>>",
			13: "<</Title (Action) /Next 14 0 R /A <</S /GoTo /D [5 0 R /Fit]>>>>",
			14: "<</Title (Web) /Next 15 0 R /A <</S /URI /URI (https://example.com/)>>>>",
			15: "<</Title (Script) /Next 16 0 R /A <</S /JavaScript /JS (app.alert(1))>>>>",
			16: "<</Title (Element) /Next 17 0 R /SE 30 0 R>>",
			17: "<</Title (Closed) /Next 18 0 R /First 22 0 R /Count -1>>",
			18: "<</Title (Last) /Next 12 0 R>>", // back into the chain
			20: "<</Title (Section 1) /Parent 11 0 R /Next 21 0 R /Dest [3 0 R /FitH 400]>>",
			21: "<</Title (Section 2) /Parent 11 0 R /First 23 0 R /Count 1 /Dest [4 0 R /Fit]>>",
			22: "<</Title (Hidden child) /Dest [5 0 R /Fit]>>",
			23: "<</Title (Deep) /Dest [5 0 R /Fit] /First 11 0 R>>", // its child is its ancestor
			30: "<</Type /StructElem /S /P /P 31 0 R>>",
			31: "<</Type /StructElem /S /Sect /Pg 4 0 R>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

// outlineString writes an outline as one entry a line.
func outlineString(items []OutlineItem, depth int) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString(strings.Repeat("  ", depth) + it.Title)
		if it.Open {
			b.WriteString(" [open]")
		}
		switch t := it.Target; {
		case t == nil:
		case t.URI != "":
			b.WriteString(" -> " + t.URI)
		case !t.leads():
			b.WriteString(" (" + t.Action + ")")
		default:
			fmt.Fprintf(&b, " -> page %d %s", t.Page, t.Fit)
		}
		b.WriteString("\n" + outlineString(it.Children, depth+1))
	}
	return b.String()
}

func TestOutline(t *testing.T) {
	doc := editOpen(t, outlineFile())
	out := doc.Outline()
	const want = `Chapter [open] -> page 0 XYZ
  Section 1 -> page 0 FitH
  Section 2 [open] -> page 1 Fit
    Deep -> page 2 Fit
Übersicht 1 -> page 1 FitH
Action -> page 2 Fit
Web -> https://example.com/
Script (JavaScript)
Element -> page 1 XYZ
Closed
  Hidden child -> page 2 Fit
Last
`
	if got := outlineString(out, 0); got != want {
		t.Fatalf("outline:\n%s\nwant:\n%s", got, want)
	}
	chapter, named := out[0], out[1]
	if !chapter.Bold || chapter.Italic || chapter.Color != (color.RGBA{255, 0, 0, 255}) {
		t.Errorf("chapter: bold %v, italic %v, colour %v", chapter.Bold, chapter.Italic, chapter.Color)
	}
	if named.Bold || !named.Italic || named.Color != (color.RGBA{0, 0, 0, 255}) {
		t.Errorf("named: bold %v, italic %v, colour %v", named.Bold, named.Italic, named.Color)
	}
	if named.Target.Named != "second" {
		t.Errorf("named destination %q", named.Target.Named)
	}
	// The targets resolve like those of links.
	dest, ok := doc.Destination(out[0].Children[0].Target)
	if !ok || dest.Page != 0 || dest.Fit != "FitH" || dest.Y != 400 {
		t.Errorf("destination %+v, %v", dest, ok)
	}
	if _, ok := doc.Destination(out[5].Target); !ok {
		t.Error("the page of a structure element does not resolve")
	}
	if &doc.Outline()[0] != &out[0] {
		t.Error("the outline is read twice")
	}
	if got := editOpen(t, fieldsFile(false)).Outline(); got != nil {
		t.Errorf("a document without an outline has %v", got)
	}
}

// TestOutlineBudget reads an outline that is too deep and too long only
// as far as the limits go.
func TestOutlineBudget(t *testing.T) {
	// A chain of entries, each the only child of the one before.
	objs := map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R /Outlines 9 0 R>>",
		2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 600 800]>>",
		3: "<</Type /Page /Parent 2 0 R>>",
		9: "<</Type /Outlines /First 10 0 R>>",
	}
	const levels = maxOutlineDepth + 8
	for i := range levels {
		objs[10+i] = fmt.Sprintf("<</Title (%d) /First %d 0 R>>", i, 11+i)
	}
	objs[10+levels] = "<</Title (leaf)>>"
	depth := 0
	for items := editOpen(t, testpdf.File{Objs: objs, Trailer: "/Root 1 0 R"}.Bytes()).Outline(); len(items) > 0; items = items[0].Children {
		depth++
	}
	if depth != maxOutlineDepth {
		t.Errorf("%d levels read, want %d", depth, maxOutlineDepth)
	}

	// A flat chain longer than the limit.
	const more = 100
	objs = map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R /Outlines 9 0 R>>",
		2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 600 800]>>",
		3: "<</Type /Page /Parent 2 0 R>>",
		9: "<</Type /Outlines /First 10 0 R>>",
	}
	for i := range maxOutlineItems + more {
		objs[10+i] = fmt.Sprintf("<</Title (x) /Next %d 0 R>>", 11+i)
	}
	doc := editOpen(t, testpdf.File{Objs: objs, Trailer: "/Root 1 0 R"}.Bytes())
	if n := len(doc.Outline()); n != maxOutlineItems {
		t.Errorf("%d entries read, want %d", n, maxOutlineItems)
	}
}
