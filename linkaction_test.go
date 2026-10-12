package cera

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
)

// actionsFile is a document of three pages whose first page has a link
// for each kind of action, in this order: Named actions, a JavaScript
// action chained to a GoTo, a relative and an absolute URI, GoToE, an
// action cera only reports, a chain that leads nowhere, and a link with
// nothing.
func actionsFile() []byte {
	link := func(rest string) string {
		return "<</Type /Annot /Subtype /Link /Rect [0 0 10 10] " + rest + ">>"
	}
	return testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R /URI <</Base (https://example.org/docs/)>>>>",
			2:  "<</Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 /MediaBox [0 0 200 100]>>",
			3:  "<</Type /Page /Parent 2 0 R /Annots [10 0 R 11 0 R 12 0 R 13 0 R 14 0 R 15 0 R 16 0 R 17 0 R 18 0 R 19 0 R]>>",
			4:  "<</Type /Page /Parent 2 0 R>>",
			5:  "<</Type /Page /Parent 2 0 R>>",
			10: link("/A <</S /Named /N /NextPage>>"),
			11: link("/A <</S /Named /N /LastPage>>"),
			12: link("/A <</S /Named /N /Print>>"),
			13: link("/A <</S /JavaScript /JS (app.beep()) /Next <</S /GoTo /D [5 0 R /Fit]>>>>"),
			14: link("/A <</S /URI /URI (page.html#top) /IsMap true>>"),
			15: link("/A <</S /URI /URI (mailto:a@example.com)>>"),
			16: link("/A <</S /GoToE /F (outer.pdf) /D (chapter) /T <</R /C /N (inner.pdf) /T <</R /P>>>>>>"),
			17: link("/A <</S /SubmitForm /F (https://example.com/submit)>>"),
			18: link("/A <</S /JavaScript /JS (1) /Next [<</S /Hide>> 30 0 R]>>"),
			19: link(""),
			// A chain that comes back to itself.
			30: "<</S /ResetForm /Next 30 0 R>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

// TestLinkActions reads the actions of links beyond URI and GoTo: every
// link with an action is a link, and says what it does.
func TestLinkActions(t *testing.T) {
	doc := editOpen(t, actionsFile())
	p, _ := doc.Page(0)
	links := p.Links()
	if len(links) != 9 {
		t.Fatalf("%d links, want 9: all but the one without an action", len(links))
	}
	target := func(i int) *LinkTarget { return &links[i].Target }

	next, last, print := target(0), target(1), target(2)
	if next.Action != "Named" || next.NamedAction != "NextPage" || !next.leads() {
		t.Errorf("NextPage: %+v", next)
	}
	for _, tt := range []struct {
		t          *LinkTarget
		current, n int
		want       int
		ok         bool
	}{
		{next, 0, 3, 1, true}, {next, 2, 3, 3, false}, {last, 0, 3, 2, true}, {print, 0, 3, -1, false},
		{&LinkTarget{NamedAction: "PrevPage"}, 0, 3, -1, false}, {&LinkTarget{NamedAction: "FirstPage"}, 2, 3, 0, true},
	} {
		if got, ok := tt.t.NamedPage(tt.current, tt.n); got != tt.want || ok != tt.ok {
			t.Errorf("%s from page %d of %d: %d, %v", tt.t.NamedAction, tt.current, tt.n, got, ok)
		}
	}
	if print.NamedAction != "Print" || print.leads() {
		t.Errorf("Print: %+v", print)
	}
	if _, ok := doc.Destination(next); ok {
		t.Error("a Named action resolved without a current page")
	}

	// The script is not run; the action chained to it leads somewhere.
	if c := target(3); c.Action != "GoTo" || c.Page != 2 || c.Fit != "Fit" {
		t.Errorf("JavaScript, then GoTo: %+v", c)
	}
	if d, ok := doc.Destination(target(3)); !ok || d.Page != 2 {
		t.Errorf("destination %+v, %v", d, ok)
	}
	if u := target(4); u.URI != "https://example.org/docs/page.html#top" || !u.IsMap || u.Action != "URI" {
		t.Errorf("relative URI: %+v", u)
	}
	if u := target(5); u.URI != "mailto:a@example.com" || u.IsMap {
		t.Errorf("absolute URI: %+v", u)
	}
	if e := target(6); e.Action != "GoToE" || !e.External || e.File != "outer.pdf" || e.Named != "chapter" ||
		!reflect.DeepEqual(e.Embedded, []string{"inner.pdf", ".."}) {
		t.Errorf("GoToE: %+v", e)
	}
	if s := target(7); s.Action != "SubmitForm" || s.leads() || s.Page != -1 || s.URI != "" {
		t.Errorf("SubmitForm: %+v", s)
	}
	// Nothing in the chain leads anywhere: the link is what its first
	// action is.
	if c := target(8); c.Action != "JavaScript" || c.leads() {
		t.Errorf("a chain that leads nowhere: %+v", c)
	}
	annots := p.Annotations()
	if annots[9].Link != nil {
		t.Errorf("a link with nothing has the target %+v", annots[9].Link)
	}
}

// TestNameTreeBytes finds a named destination in a name tree whose keys
// are ordered as bytes: as text, é (0xE9 in PDFDocEncoding) sorts before
// the bullet (0x80), and the first kid would be searched for it in vain.
func TestNameTreeBytes(t *testing.T) {
	src := testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R /Names <</Dests 20 0 R>>>>",
			2:  "<</Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 /MediaBox [0 0 200 100]>>",
			3:  "<</Type /Page /Parent 2 0 R /Annots [10 0 R 11 0 R 12 0 R]>>",
			4:  "<</Type /Page /Parent 2 0 R>>",
			5:  "<</Type /Page /Parent 2 0 R>>",
			10: "<</Type /Annot /Subtype /Link /Rect [0 0 10 10] /Dest <E941>>>",
			11: "<</Type /Annot /Subtype /Link /Rect [0 0 10 10] /A <</S /GoTo /D <8041>>>>>",
			12: "<</Type /Annot /Subtype /Link /Rect [0 0 10 10] /Dest (plain)>>",
			20: "<</Kids [21 0 R 22 0 R]>>",
			21: "<</Limits [(A) <8041>] /Names [(A) [3 0 R /Fit] (plain) <</D [4 0 R /Fit]>> <8041> [4 0 R /FitH 50]]>>",
			22: "<</Limits [<9041> <E941>] /Names [<9041> [3 0 R /Fit] <E941> [5 0 R /Fit]]>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
	doc := editOpen(t, src)
	p, _ := doc.Page(0)
	links := p.Links()
	if len(links) != 3 {
		t.Fatalf("%d links", len(links))
	}
	for i, want := range []struct {
		named string
		page  int
	}{{"éA", 2}, {"•A", 1}, {"plain", 1}} {
		got := links[i].Target
		if got.Named != want.named || got.Page != want.page {
			t.Errorf("link %d: destination %q on page %d, want %q on page %d", i, got.Named, got.Page, want.named, want.page)
		}
	}
}

// TestLinkActionWritten writes a Named action, and keeps the action of a
// link that cera cannot write when the link changes.
func TestLinkActionWritten(t *testing.T) {
	src := actionsFile()
	doc := editOpen(t, src)
	p, _ := doc.Page(0)
	e := doc.Edit()
	box := Rect{20, 20, 60, 40}
	if _, err := e.AddAnnotation(0, NewAnnotation{Subtype: "Link", Link: &LinkTarget{NamedAction: "PrevPage"}, Markup: Markup{Bounds: box}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddAnnotation(0, NewAnnotation{Subtype: "Link", Link: &LinkTarget{URI: "https://example.com/map", IsMap: true}, Markup: Markup{Bounds: box}}); err != nil {
		t.Fatal(err)
	}
	for _, link := range []*LinkTarget{{Page: -1}, {Page: -1, Action: "JavaScript"}, {Action: "GoToE", External: true, File: "x.pdf"}} {
		if _, err := e.AddAnnotation(0, NewAnnotation{Subtype: "Link", Link: link, Markup: Markup{Bounds: box}}); err == nil {
			t.Errorf("a link with the target %+v was added", link)
		}
	}
	// Move the links with a script and with an embedded target.
	for _, index := range []int{3, 6, 7} {
		a, ok := p.EditAnnotation(index)
		if !ok {
			t.Fatalf("link %d cannot be edited", index)
		}
		a.Bounds = Rect{100, 20, 140, 40}
		if err := e.SetAnnotation(0, index, a); err != nil {
			t.Fatalf("link %d: %v", index, err)
		}
	}
	out := editOpen(t, editSave(t, e))
	op, _ := out.Page(0)
	links := op.Links()
	if len(links) != 11 {
		t.Fatalf("%d links", len(links))
	}
	if l := links[9].Target; l.Action != "Named" || l.NamedAction != "PrevPage" {
		t.Errorf("written Named action: %+v", l)
	}
	if l := links[10].Target; l.URI != "https://example.com/map" || !l.IsMap {
		t.Errorf("written URI: %+v", l)
	}
	// Moved, and what they do is what it was: the script chained to a
	// GoTo is still there.
	if s, _ := out.name(out.dict(op.Annotations()[3].dict.Get("A")).Get("S")); s != "JavaScript" {
		t.Errorf("the action of the chained link is now %q", s)
	}
	for _, index := range []int{3, 6, 7} {
		got := links[index]
		if b := got.Quads[0].Bounds(); !sameRect(b, Rect{100, 20, 140, 40}) {
			t.Errorf("link %d is at %v", index, b)
		}
	}
	if l := links[3].Target; l.Page != 2 || l.Fit != "Fit" {
		t.Errorf("the chained link: %+v", l)
	}
	if l := links[6].Target; l.Action != "GoToE" || l.File != "outer.pdf" || !reflect.DeepEqual(l.Embedded, []string{"inner.pdf", ".."}) {
		t.Errorf("the embedded target: %+v", l)
	}
	if l := links[7].Target; l.Action != "SubmitForm" {
		t.Errorf("the form action: %+v", l)
	}
	if s := fmt.Sprint(len(op.Annotations())); s != "12" {
		t.Errorf("%s annotations", s)
	}
}
