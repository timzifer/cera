package cera

import (
	"bytes"
	"image/color"
	"strings"
	"testing"
)

// outlineOf saves e and returns the outline of the result as
// outlineString writes it, with the document.
func outlineOf(t *testing.T, e *Editor) (string, *Document) {
	t.Helper()
	od := editOpen(t, editSave(t, e))
	return outlineString(od.Outline(), 0), od
}

// TestSetOutline sets the outline of a result: the document's as it is,
// changed, new, and none.
func TestSetOutline(t *testing.T) {
	src := outlineFile()
	doc := editOpen(t, src)
	const asRead = `Chapter [open] -> page 0 XYZ
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
	// As it reads, it is written as it was: also the action cera does
	// not write and the entry that names a structure element.
	e := doc.Edit()
	if got := outlineString(e.Outline(), 0); got != asRead {
		t.Fatalf("the editor has the outline\n%s", got)
	}
	if err := e.SetOutline(e.Outline()); err != nil {
		t.Fatal(err)
	}
	got, od := outlineOf(t, e)
	if got != asRead {
		t.Errorf("set as it was, the outline is\n%s", got)
	}
	o := od.Outline()
	if !o[0].Bold || o[0].Color != (color.RGBA{255, 0, 0, 255}) || !o[1].Italic || o[1].Target.Named != "second" {
		t.Errorf("the entries are %+v and %+v", o[0], o[1])
	}
	if v, ok := od.Destination(o[0].Children[0].Target); !ok || v.Y != 400 {
		t.Errorf("the first section leads to %+v", v)
	}
	// An update writes it too.
	var upd bytes.Buffer
	if err := e.Update(&upd); err != nil {
		t.Fatal(err)
	}
	if ud := editOpen(t, upd.Bytes()); !bytes.HasPrefix(upd.Bytes(), src) || ud.r.Repaired() || outlineString(ud.Outline(), 0) != asRead {
		t.Errorf("updated, the outline is\n%s", outlineString(ud.Outline(), 0))
	}

	// Renamed, an entry taken out, one led elsewhere, reordered, new ones.
	items := e.Outline()
	items[0].Title = "First chapter"
	items[0].Children = items[0].Children[1:]
	items[2].Target = &LinkTarget{Page: 0, Fit: "FitH", Params: []float64{500}}
	items[3], items[4] = items[4], items[3]
	items = append(items[:6:6], OutlineItem{
		Title: "Neu: Übersicht", Bold: true, Open: true, Color: color.RGBA{0, 0, 255, 255},
		Target: &LinkTarget{Page: 2},
		Children: []OutlineItem{
			{Title: "Site", Target: &LinkTarget{URI: "https://example.org/", Page: -1}},
			{Title: "Named", Italic: true, Target: &LinkTarget{Named: "second", Page: -1}},
			{Title: "Heading"},
		},
	})
	if err := e.SetOutline(items); err != nil {
		t.Fatal(err)
	}
	const changed = `First chapter [open] -> page 0 XYZ
  Section 2 [open] -> page 1 Fit
    Deep -> page 2 Fit
Übersicht 1 -> page 1 FitH
Action -> page 0 FitH
Script (JavaScript)
Web -> https://example.com/
Element -> page 1 XYZ
Neu: Übersicht [open] -> page 2 Fit
  Site -> https://example.org/
  Named -> page 1 FitH
  Heading
`
	got, od = outlineOf(t, e)
	if got != changed {
		t.Errorf("changed, the outline is\n%s", got)
	}
	o = od.Outline()
	if n := o[len(o)-1]; !n.Bold || n.Italic || n.Color != (color.RGBA{0, 0, 255, 255}) || !n.Children[1].Italic {
		t.Errorf("the new entry is %+v", n)
	}
	// 500 points up a page of 800: 300 down from its top.
	if v, ok := od.Destination(o[2].Target); !ok || v.Y != 300 {
		t.Errorf("the entry led elsewhere leads to %+v", v)
	}
	// The editor has it as it is set: a named destination is looked up
	// by who reads the result.
	if got := outlineString(e.Outline(), 0); got != strings.Replace(changed, "Named -> page 1 FitH", "Named -> page -1 ", 1) {
		t.Errorf("the editor has the outline\n%s", got)
	}

	// The targets stay with their pages when pages move afterwards.
	if err := e.MovePage(2, 0); err != nil {
		t.Fatal(err)
	}
	const moved = `First chapter [open] -> page 1 XYZ
  Section 2 [open] -> page 2 Fit
    Deep -> page 0 Fit
Übersicht 1 -> page 2 FitH
Action -> page 1 FitH
Script (JavaScript)
Web -> https://example.com/
Element -> page 2 XYZ
Neu: Übersicht [open] -> page 0 Fit
  Site -> https://example.org/
  Named -> page 2 FitH
  Heading
`
	if got, _ = outlineOf(t, e); got != moved {
		t.Errorf("with a page moved, the outline is\n%s", got)
	}
	if got := outlineString(e.Outline(), 0); got != strings.Replace(moved, "Named -> page 2 FitH", "Named -> page -1 ", 1) {
		t.Errorf("with a page moved, the editor has the outline\n%s", got)
	}

	// A page deleted: the entries that led to it lead nowhere, as they
	// do without an outline set; pruned, they are gone, and a heading
	// that is left with nothing.
	e = doc.Edit()
	if err := e.DeletePages(2); err != nil {
		t.Fatal(err)
	}
	plain, _ := outlineOf(t, e)
	if err := e.SetOutline(e.Outline()); err != nil {
		t.Fatal(err)
	}
	if got, _ = outlineOf(t, e); got != plain {
		t.Errorf("with a page deleted, the outline set is\n%s, as it is\n%s", got, plain)
	}
	const pruned = `Chapter [open] -> page 0 XYZ
  Section 1 -> page 0 FitH
  Section 2 -> page 1 Fit
Übersicht 1 -> page 1 FitH
Web -> https://example.com/
Script (JavaScript)
Element -> page 1 XYZ
Last
`
	e.PruneOutline()
	if got, _ = outlineOf(t, e); got != pruned {
		t.Errorf("pruned, the outline set is\n%s", got)
	}
	// The document's own outline is pruned alike, in a new file and in
	// an update.
	e = doc.Edit()
	e.PruneOutline()
	if err := e.DeletePages(2); err != nil {
		t.Fatal(err)
	}
	if got, _ = outlineOf(t, e); got != pruned {
		t.Errorf("pruned, the outline is\n%s", got)
	}
	upd.Reset()
	if err := e.Update(&upd); err != nil {
		t.Fatal(err)
	}
	if got := outlineString(editOpen(t, upd.Bytes()).Outline(), 0); got != pruned {
		t.Errorf("updated and pruned, the outline is\n%s", got)
	}
	// A new entry to a page that is deleted then goes too.
	e = doc.Edit()
	if err := e.SetOutline([]OutlineItem{{Title: "Kept", Target: &LinkTarget{Page: 0}}, {Title: "Lost", Target: &LinkTarget{Page: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := e.DeletePages(1); err != nil {
		t.Fatal(err)
	}
	if got, _ = outlineOf(t, e); got != "Kept -> page 0 Fit\nLost\n" {
		t.Errorf("with the page of a new entry deleted, the outline is\n%s", got)
	}
	e.PruneOutline()
	if got, _ = outlineOf(t, e); got != "Kept -> page 0 Fit\n" {
		t.Errorf("pruned, it is\n%s", got)
	}

	// None.
	e = doc.Edit()
	if err := e.SetOutline(nil); err != nil {
		t.Fatal(err)
	}
	out := editSave(t, e)
	if o := editOpen(t, out).Outline(); o != nil || bytes.Contains(out, []byte("/Outlines")) || bytes.Contains(out, []byte("Chapter")) {
		t.Errorf("without an outline the result has %v", o)
	}
	upd.Reset()
	if err := e.Update(&upd); err != nil {
		t.Fatal(err)
	}
	if o := editOpen(t, upd.Bytes()).Outline(); o != nil {
		t.Errorf("updated to have no outline, the result has %v", o)
	}

	// A new document, of pages of two: an outline for the merged file.
	other := editOpen(t, labelsFile(3, "<</Nums [0 <</S /D>>]>>", nil))
	ne := NewEditor()
	if err := ne.ImportPages(0, doc, EditPage{Index: 0}, EditPage{Index: 1}); err != nil {
		t.Fatal(err)
	}
	if err := ne.ImportPages(2, other, EditPage{Index: 0}, EditPage{Index: 2}); err != nil {
		t.Fatal(err)
	}
	if err := ne.SetOutline([]OutlineItem{
		{Title: "The first", Open: true, Target: &LinkTarget{Page: 0}, Children: []OutlineItem{{Title: "Its second page", Target: &LinkTarget{Page: 1, Fit: "XYZ", Params: []float64{0, 400, 0}}}}},
		{Title: "The other", Target: &LinkTarget{Page: 2}},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ = outlineOf(t, ne); got != "The first [open] -> page 0 Fit\n  Its second page -> page 1 XYZ\nThe other -> page 2 Fit\n" {
		t.Errorf("the merged file has the outline\n%s", got)
	}
	if o := ne.Outline(); len(o) != 2 || o[1].Target.Page != 2 {
		t.Errorf("the new editor has the outline %+v", o)
	}
	// Entries of another document are new ones: their pages are those
	// of the result.
	ne2 := other.Edit()
	if err := ne2.SetOutline(doc.Outline()[:1]); err != nil {
		t.Fatal(err)
	}
	if got, _ = outlineOf(t, ne2); !strings.HasPrefix(got, "Chapter [open] -> page 0 XYZ\n  Section 1 -> page 0 FitH\n  Section 2 [open] -> page 1 Fit\n") {
		t.Errorf("with the entries of another document, the outline is\n%s", got)
	}

	// What is no outline: nothing is set.
	e = doc.Edit()
	deep := []OutlineItem{{Title: "x"}}
	for range maxOutlineDepth + 1 {
		deep = []OutlineItem{{Title: "x", Children: deep}}
	}
	for name, items := range map[string][]OutlineItem{
		"no such page":  {{Title: "x", Target: &LinkTarget{Page: 3}}},
		"an action":     {{Title: "x", Target: &LinkTarget{Page: -1, Action: "JavaScript"}}},
		"nowhere":       {{Title: "x", Target: &LinkTarget{Page: -1}}},
		"too deep":      deep,
		"another's own": editOpen(t, outlineFile()).Outline()[4:5], // its script
	} {
		if err := e.SetOutline(items); err == nil {
			t.Errorf("%s: set", name)
		}
	}
	if e.outlineSet {
		t.Error("an outline is set after all that failed")
	}
}

// TestSetPageLabels sets the labels of the pages of a result.
func TestSetPageLabels(t *testing.T) {
	src := labelsFile(6, "<</Nums [0 <</S /r>> 2 <</S /D /P (A-) /St 5>>]>>", nil)
	doc := editOpen(t, src)
	if r := doc.PageLabels(); len(r) != 2 || r[0] != (LabelRange{Start: 0, Style: LabelRomanLower, First: 1}) ||
		r[1] != (LabelRange{Start: 2, Style: LabelDecimal, Prefix: "A-", First: 5}) {
		t.Errorf("the document has the labels %+v", r)
	}
	if editOpen(t, outlineFile()).PageLabels() != nil {
		t.Error("a document without labels has some")
	}
	e := doc.Edit()
	ranges := []LabelRange{
		{Start: 1, Style: LabelLetterUpper},
		{Start: 3, Style: LabelRomanUpper, Prefix: "Anhang ", First: 4},
		{Start: 5, Prefix: "Ende"},
	}
	if err := e.SetPageLabels(ranges); err != nil {
		t.Fatal(err)
	}
	const want = "1 A B Anhang IV Anhang V Ende"
	od := editOpen(t, editSave(t, e))
	if got := pageLabels(od); got != want {
		t.Errorf("the pages are labelled %q, want %q", got, want)
	}
	if i, ok := od.PageByLabel("Anhang V"); !ok || i != 4 {
		t.Errorf("the page labelled Anhang V is %d", i)
	}
	var upd bytes.Buffer
	if err := e.Update(&upd); err != nil {
		t.Fatal(err)
	}
	if ud := editOpen(t, upd.Bytes()); !bytes.HasPrefix(upd.Bytes(), src) || pageLabels(ud) != want {
		t.Errorf("updated, the pages are labelled %q", pageLabels(ud))
	}
	// They are of the pages by their place: a page moved has the label
	// of where it is.
	if err := e.MovePage(5, 0); err != nil {
		t.Fatal(err)
	}
	if got := pageLabels(editOpen(t, editSave(t, e))); got != want {
		t.Errorf("with a page moved, the pages are labelled %q", got)
	}
	// Fewer pages than ranges.
	if err := e.DeletePages(3, 4, 5); err != nil {
		t.Fatal(err)
	}
	if got := pageLabels(editOpen(t, editSave(t, e))); got != "1 A B" {
		t.Errorf("with pages deleted, the pages are labelled %q", got)
	}

	// None: the result has no labels, though the document has.
	e = doc.Edit()
	if err := e.SetPageLabels(nil); err != nil {
		t.Fatal(err)
	}
	out := editSave(t, e)
	if od := editOpen(t, out); od.PageLabels() != nil || bytes.Contains(out, []byte("/PageLabels")) {
		t.Errorf("without labels the result has %+v", od.PageLabels())
	}
	upd.Reset()
	if err := e.Update(&upd); err != nil {
		t.Fatal(err)
	}
	if ud := editOpen(t, upd.Bytes()); ud.PageLabels() != nil {
		t.Errorf("updated to have no labels, the result has %+v", ud.PageLabels())
	}

	// A new document.
	ne := NewEditor()
	if err := ne.ImportPages(0, doc, EditPage{Index: 0}, EditPage{Index: 1}, EditPage{Index: 2}); err != nil {
		t.Fatal(err)
	}
	if err := ne.SetPageLabels([]LabelRange{{Style: LabelLetterLower, First: 26}}); err != nil {
		t.Fatal(err)
	}
	if got := pageLabels(editOpen(t, editSave(t, ne))); got != "z aa bb" {
		t.Errorf("the new document is labelled %q", got)
	}

	// What are no labels: nothing is set.
	e = doc.Edit()
	for name, ranges := range map[string][]LabelRange{
		"no such page":  {{Start: 6}},
		"before none":   {{Start: -1}},
		"not in order":  {{Start: 2}, {Start: 1}},
		"twice":         {{Start: 2}, {Start: 2}},
		"no such style": {{Style: 'x'}},
		"no number":     {{Style: LabelDecimal, First: -3}},
	} {
		if err := e.SetPageLabels(ranges); err == nil {
			t.Errorf("%s: set", name)
		}
	}
	if e.labelsSet {
		t.Error("labels are set after all that failed")
	}
}
