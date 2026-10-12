package cera

import (
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// mergeBase is the document pages are merged into: two pages labelled i
// and ii, an outline of two entries, and the named destinations "intro"
// and "shared". Without outline it has no /Outlines.
func mergeBase(outline bool) []byte {
	cat := "<</Type /Catalog /Pages 2 0 R /Names <</Dests 20 0 R /JavaScript 25 0 R>> /PageLabels <</Nums [0 <</S /r>>]>>"
	if outline {
		cat += " /Outlines 10 0 R"
	}
	return testpdf.File{
		Objs: map[int]string{
			1:  cat + ">>",
			2:  "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 200 100]>>",
			3:  "<</Type /Page /Parent 2 0 R>>",
			4:  "<</Type /Page /Parent 2 0 R>>",
			10: "<</Type /Outlines /First 11 0 R /Last 12 0 R /Count 2>>",
			11: "<</Title (A1) /Parent 10 0 R /Next 12 0 R /Dest [3 0 R /Fit]>>",
			12: "<</Title (A2) /Parent 10 0 R /Prev 11 0 R /Dest /shared>>",
			20: "<</Names [(intro) [3 0 R /Fit] (shared) [4 0 R /FitH 50]]>>",
			25: "<</Names []>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

// mergeOther is a document to import from: three pages labelled A-1 to
// A-3, an outline — a chapter with two sections, the first through a named
// destination, a web address, and an entry on the third page —, named
// destinations in a tree and in the old dictionary of the catalogue, and
// on its first page a link by name to its second.
func mergeOther() []byte {
	return testpdf.File{
		Objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R /Outlines 10 0 R /Names <</Dests 20 0 R>> /Dests <</legacy [3 0 R /Fit]>>" +
				" /PageLabels <</Nums [0 <</S /D /P (A-)>>]>>>>",
			2:  "<</Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 /MediaBox [0 0 200 100]>>",
			3:  "<</Type /Page /Parent 2 0 R /Annots [30 0 R]>>",
			4:  "<</Type /Page /Parent 2 0 R>>",
			5:  "<</Type /Page /Parent 2 0 R>>",
			10: "<</Type /Outlines /First 11 0 R /Last 16 0 R /Count 5>>",
			11: "<</Title (Chapter) /Parent 10 0 R /Next 14 0 R /First 12 0 R /Last 13 0 R /Count 2 /Dest [3 0 R /XYZ 0 100 null] /F 2 /C [1 0 0]>>",
			12: "<</Title (Section 1) /Parent 11 0 R /Next 13 0 R /A <</S /GoTo /D (sec1)>>>>",
			13: "<</Title (Section 2) /Parent 11 0 R /Prev 12 0 R /Dest [5 0 R /Fit]>>",
			14: "<</Title (Web) /Parent 10 0 R /Prev 11 0 R /Next 15 0 R /A <</S /URI /URI (https://example.com)>>>>",
			15: "<</Title (Heading) /Parent 10 0 R /Prev 14 0 R /Next 16 0 R /First 17 0 R /Last 17 0 R /Count 1>>",
			16: "<</Title (Lost) /Parent 10 0 R /Prev 15 0 R /Dest [5 0 R /Fit]>>",
			17: "<</Title <FEFF00DC006200650072> /Parent 15 0 R /Dest [4 0 R /Fit]>>",
			20: "<</Kids [21 0 R]>>",
			21: "<</Limits [(gone) (shared)] /Names [(gone) [5 0 R /Fit] (sec1) <</D [4 0 R /FitH 80]>> (shared) [3 0 R /Fit]]>>",
			30: "<</Type /Annot /Subtype /Link /Rect [0 0 10 10] /Dest (sec1)>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

// destPage returns the page a named destination of d leads to, -1 for
// none.
func destPage(d *Document, name string) int {
	t := d.destination(pdf.String([]byte(name)))
	if t == nil {
		return -1
	}
	return t.Page
}

// TestImportOptions merges a document into another with its outline, its
// named destinations and its page labels.
func TestImportOptions(t *testing.T) {
	other := editOpen(t, mergeOther())
	all := ImportOptions{Outline: true, OutlineTitle: "Appendix Ü", Destinations: true, Labels: true}
	for _, tt := range []struct {
		name    string
		base    []byte // nil: a new document
		opt     ImportOptions
		outline string
		dests   map[string]int
		labels  string
	}{
		{"everything", mergeBase(true), all,
			"A1 -> page 0 Fit\nA2 -> page 1 FitH\nAppendix Ü\n  Chapter [open] -> page 2 XYZ\n    Section 1 -> page 3 FitH\n  Heading [open]\n    Über -> page 3 Fit\n",
			map[string]int{"intro": 0, "shared": 1, "shared_2": 2, "sec1": 3, "legacy": 2, "gone": -1}, "i ii A-1 A-2"},
		{"nothing", mergeBase(true), ImportOptions{},
			"A1 -> page 0 Fit\nA2 -> page 1 FitH\n",
			map[string]int{"intro": 0, "shared": 1, "sec1": -1, "legacy": -1}, "i ii - -"},
		{"at the top level", mergeBase(true), ImportOptions{Outline: true},
			"A1 -> page 0 Fit\nA2 -> page 1 FitH\nChapter [open] -> page 2 XYZ\n  Section 1 -> page 3 FitH\nHeading [open]\n  Über -> page 3 Fit\n",
			map[string]int{"shared": 1, "sec1": -1}, "i ii - -"},
		{"into a document without an outline", mergeBase(false), all,
			"Appendix Ü\n  Chapter [open] -> page 2 XYZ\n    Section 1 -> page 3 FitH\n  Heading [open]\n    Über -> page 3 Fit\n",
			map[string]int{"intro": 0, "shared": 1, "shared_2": 2, "sec1": 3}, "i ii A-1 A-2"},
		{"into a new document", nil, ImportOptions{Outline: true, Destinations: true, Labels: true},
			"Chapter [open] -> page 0 XYZ\n  Section 1 -> page 1 FitH\nHeading [open]\n  Über -> page 1 Fit\n",
			map[string]int{"shared": 0, "sec1": 1, "legacy": 0, "gone": -1}, "A-1 A-2"},
	} {
		for _, mode := range []string{"save", "update"} {
			if tt.base == nil && mode == "update" {
				continue
			}
			t.Run(tt.name+"/"+mode, func(t *testing.T) {
				e := NewEditor()
				if tt.base != nil {
					e = editOpen(t, tt.base).Edit()
				}
				// The first two pages of three.
				if err := e.ImportPagesWith(e.NumPages(), other, tt.opt, EditPage{Index: 0}, EditPage{Index: 1}); err != nil {
					t.Fatal(err)
				}
				var out []byte
				if mode == "save" {
					out = editSave(t, e)
				} else {
					out = editUpdate(t, e, tt.base)
				}
				d := editOpen(t, out)
				if got := outlineString(d.Outline(), 0); got != tt.outline {
					t.Errorf("outline:\n%s\nwant:\n%s", got, tt.outline)
				}
				for name, want := range tt.dests {
					if got := destPage(d, name); got != want {
						t.Errorf("destination %q leads to page %d, want %d", name, got, want)
					}
				}
				if got := pageLabels(d); got != tt.labels {
					t.Errorf("labels %q, want %q", got, tt.labels)
				}
				// The link by name on the imported page still arrives.
				first := d.NumPages() - 2
				p, _ := d.Page(first)
				if links := p.Links(); len(links) != 1 || links[0].Target.Page != first+1 {
					t.Errorf("the link of the imported page: %+v", links)
				}
				if tt.base != nil {
					// What else the catalogue names stays.
					cat, _ := d.r.Catalog()
					if d.dict(d.dict(cat.Get("Names")).Get("JavaScript")).IsZero() {
						t.Error("/Names lost its other trees")
					}
				}
			})
		}
	}
}

// TestImportOptionsStyle keeps the style of entries, and writes an outline
// other readers can walk: every entry linked to its neighbours and its
// parent.
func TestImportOptionsStyle(t *testing.T) {
	other := editOpen(t, mergeOther())
	e := editOpen(t, mergeBase(true)).Edit()
	if err := e.ImportPagesWith(2, other, ImportOptions{Outline: true, OutlineTitle: "More"}, EditPage{Index: 0}, EditPage{Index: 1}, EditPage{Index: 2}); err != nil {
		t.Fatal(err)
	}
	// Twice from one document: the options add up, the entries come once.
	if err := e.ImportPagesWith(0, other, ImportOptions{Labels: true}, EditPage{Index: 2}); err != nil {
		t.Fatal(err)
	}
	d := editOpen(t, editSave(t, e))
	items := d.Outline()
	if len(items) != 3 || items[2].Title != "More" || len(items[2].Children) != 3 {
		t.Fatalf("outline:\n%s", outlineString(items, 0))
	}
	ch := items[2].Children[0]
	if !ch.Bold || ch.Color.R != 255 || len(ch.Children) != 2 || !ch.Open || items[2].Open {
		t.Errorf("chapter %+v", ch)
	}
	if got := pageLabels(d); got != "A-3 i ii - - -" {
		t.Errorf("labels %q", got)
	}

	// Walk it by /Next and back by /Prev, checking /Parent, /First, /Last
	// and /Count: the entries shown below, negative for a closed entry.
	cat, _ := d.r.Catalog()
	var walk func(parent pdf.Object) int
	walk = func(parent pdf.Object) int {
		pd := d.dict(parent)
		n, last := 0, pdf.Null
		var prev pdf.Object
		for o := pd.Get("First"); !o.IsNull(); {
			it := d.dict(o)
			if it.Get("Parent").String() != parent.String() {
				t.Errorf("%q: /Parent %v, want %v", textString(d.resolve(it.Get("Title"))), it.Get("Parent"), parent)
			}
			if it.Get("Prev").String() != prev.String() {
				t.Errorf("%q: /Prev %v, want %v", textString(d.resolve(it.Get("Title"))), it.Get("Prev"), prev)
			}
			n++
			if !it.Get("First").IsNull() {
				kids := walk(o)
				c, _ := d.integer(it.Get("Count"))
				if c != -kids && c != kids {
					t.Errorf("%q: /Count %d with %d entries", textString(d.resolve(it.Get("Title"))), c, kids)
				}
				if c > 0 {
					n += kids
				}
			}
			prev, last = o, o
			o = it.Get("Next")
		}
		if n > 0 && pd.Get("Last").String() != last.String() {
			t.Errorf("/Last %v, want %v", pd.Get("Last"), last)
		}
		return n
	}
	if n := walk(cat.Get("Outlines")); n != 3 {
		t.Errorf("%d entries at the top", n)
	}
	if c, _ := d.integer(d.dict(cat.Get("Outlines")).Get("Count")); c != 3 {
		t.Errorf("/Count %d of the outline", c)
	}
}
