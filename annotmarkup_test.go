package cera

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/timzifer/cera/internal/testpdf"
)

// markupFile is a page turned by 90 degrees with markup annotations of
// most kinds: a note with a popup that only the popup links, a reply that
// sets a state on it, a highlight, an ink annotation, a polygon, a line,
// a free text with a callout, a stamp, and a link and a widget, which are
// no markup annotations.
func markupFile() []byte {
	return testpdf.File{
		Objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R>>",
			2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200]>>",
			3: "<</Type /Page /Parent 2 0 R /Rotate 90 /Annots [10 0 R 11 0 R 12 0 R 13 0 R 14 0 R 15 0 R 16 0 R 17 0 R 18 0 R 19 0 R 20 0 R]>>",
			10: "<</Type /Annot /Subtype /Text /Rect [10 150 30 170] /Contents (A note) /T (Ada) /Subj (Remark) /Name /Comment" +
				" /M (D:20260102030405+01'00') /CreationDate (D:20260101000000Z) /C [1 0.5 0] /CA 0.5 /NM (n1)" +
				" /RC (<body><p>A <b>note</b></p></body>)>>",
			11: "<</Type /Annot /Subtype /Popup /Rect [40 100 140 170] /Parent 10 0 R /Open true>>",
			12: "<</Type /Annot /Subtype /Text /Rect [10 150 30 170] /Contents (Accepted) /T (Bob) /IRT 10 0 R /State (Accepted) /StateModel (Review) /F 2>>",
			13: "<</Type /Annot /Subtype /Highlight /Rect [50 20 150 40] /QuadPoints [50 40 150 40 50 20 150 20] /C [1 1 0] /Popup 11 0 R>>",
			14: "<</Type /Annot /Subtype /Ink /Rect [0 0 100 100] /InkList [[10 10 20 30 40 20] [50 50 60 60] []] /BS <</W 3>> /C [0 0 1]>>",
			15: "<</Type /Annot /Subtype /Polygon /Rect [0 0 100 100] /Vertices [10 10 90 10 50 80] /IC [0 1 0] /BS <</W 2 /S /D /D [4 2]>> /IT /PolygonCloud>>",
			16: "<</Type /Annot /Subtype /Line /Rect [0 0 200 100] /L [10 20 190 80] /LE [/OpenArrow /Circle] /Border [0 0 0.5]>>",
			17: "<</Type /Annot /Subtype /FreeText /Rect [100 100 200 150] /Contents (Text) /DA (/Helv 12 Tf 1 0 0 rg) /Q 1" +
				" /CL [50 50 80 120 100 125] /LE /ClosedArrow /IT /FreeTextCallout /IRT 10 0 R /RT /Group>>",
			18: "<</Type /Annot /Subtype /Stamp /Rect [200 100 280 150] /Name /Approved>>",
			19: "<</Type /Annot /Subtype /Link /Rect [0 0 10 10] /C [1 0 0] /A <</S /URI /URI (https://example.com)>>>>",
			20: "<</Type /Annot /Subtype /Widget /FT /Tx /T (field) /Rect [0 0 10 10]>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

func TestMarkup(t *testing.T) {
	doc := editOpen(t, markupFile())
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	annots := p.Annotations()
	if len(annots) != 11 {
		t.Fatalf("%d annotations", len(annots))
	}
	by := map[string]*Markup{}
	for i := range annots {
		a := &annots[i]
		switch a.Subtype {
		case "Link", "Popup", "Widget":
			if a.Markup != nil {
				t.Errorf("%s has markup", a.Subtype)
			}
			continue
		}
		if a.Markup == nil {
			t.Fatalf("%s has no markup", a.Subtype)
		}
		if _, ok := by[a.Subtype]; !ok {
			by[a.Subtype] = a.Markup
		}
	}
	// The page is 200 by 300 points once turned: user (x, y) shows at
	// (y, x).
	turned := func(x, y float64) [2]float64 { return [2]float64{y, x} }

	note := by["Text"]
	want := &Markup{
		Author: "Ada", Subject: "Remark", Icon: "Comment",
		Created:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Modified: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("", 3600)),
		RichText: "<body><p>A <b>note</b></p></body>",
		Color:    []float64{1, 0.5, 0}, Opacity: 0.5, BorderWidth: 1,
		Bounds: Rect{150, 10, 170, 30},
		Popup:  &Popup{Index: 1, Bounds: Rect{100, 40, 170, 140}, Open: true},
	}
	if !note.Created.Equal(want.Created) || !note.Modified.Equal(want.Modified) {
		t.Errorf("created %v, modified %v", note.Created, note.Modified)
	}
	got := *note
	got.Created, got.Modified = want.Created, want.Modified
	if !reflect.DeepEqual(&got, want) {
		t.Errorf("note:\n%+v\npopup %+v\nwant:\n%+v\npopup %+v", got, got.Popup, *want, want.Popup)
	}

	reply := annots[2].Markup
	if reply.Reply == nil || *reply.Reply != (Reply{To: 0}) || reply.State != "Accepted" || reply.StateModel != "Review" || reply.Author != "Bob" {
		t.Errorf("reply %+v", reply)
	}
	if reply.Popup != nil || reply.Opacity != 1 || !reply.Modified.IsZero() {
		t.Errorf("reply: popup %v, opacity %v, modified %v", reply.Popup, reply.Opacity, reply.Modified)
	}

	hl := by["Highlight"]
	if len(hl.Quads) != 1 || hl.Quads[0].P != [4][2]float64{turned(50, 40), turned(150, 40), turned(50, 20), turned(150, 20)} {
		t.Errorf("highlight quads %v", hl.Quads)
	}
	if hl.Popup == nil || hl.Popup.Index != 1 {
		t.Errorf("highlight popup %+v", hl.Popup)
	}

	ink := by["Ink"]
	if !reflect.DeepEqual(ink.Ink, [][][2]float64{
		{turned(10, 10), turned(20, 30), turned(40, 20)}, {turned(50, 50), turned(60, 60)},
	}) || ink.BorderWidth != 3 || !reflect.DeepEqual(ink.Color, []float64{0, 0, 1}) {
		t.Errorf("ink %+v", ink)
	}

	poly := by["Polygon"]
	if !reflect.DeepEqual(poly.Vertices, [][2]float64{turned(10, 10), turned(90, 10), turned(50, 80)}) ||
		!reflect.DeepEqual(poly.Interior, []float64{0, 1, 0}) || poly.Color != nil ||
		poly.BorderStyle != BorderDashed || !reflect.DeepEqual(poly.Dash, []float64{4, 2}) || poly.BorderWidth != 2 || poly.Intent != "PolygonCloud" {
		t.Errorf("polygon %+v", poly)
	}

	line := by["Line"]
	if !reflect.DeepEqual(line.Line, [][2]float64{turned(10, 20), turned(190, 80)}) ||
		line.LineEndings != [2]string{"OpenArrow", "Circle"} || line.BorderWidth != 0.5 {
		t.Errorf("line %+v", line)
	}

	ft := by["FreeText"]
	if ft.DefaultAppearance != "/Helv 12 Tf 1 0 0 rg" || ft.Align != AlignCenter || ft.Intent != "FreeTextCallout" ||
		!reflect.DeepEqual(ft.Callout, [][2]float64{turned(50, 50), turned(80, 120), turned(100, 125)}) ||
		ft.LineEndings != [2]string{"ClosedArrow", ""} || ft.Reply == nil || *ft.Reply != (Reply{To: 0, Group: true}) {
		t.Errorf("free text %+v", ft)
	}
	if by["Stamp"].Icon != "Approved" || by["Stamp"].Bounds != (Rect{100, 200, 150, 280}) {
		t.Errorf("stamp %+v", by["Stamp"])
	}
}

// TestMarkupOfHighlight reads back what AddHighlight wrote: a mark made
// with cera can be shown as a mark again.
func TestMarkupOfHighlight(t *testing.T) {
	src := fieldsFile(false)
	doc := editOpen(t, src)
	when := time.Date(2026, 10, 12, 9, 30, 0, 0, time.FixedZone("", 2*3600))
	h := Highlight{
		Quads: []Quad{QuadOf(Rect{20, 30, 120, 44}), QuadOf(Rect{20, 46, 80, 60})},
		Color: []float64{0, 1, 1}, Opacity: 0.4, Note: "check this", Author: "Ada", Name: "mark-1", Date: when,
	}
	e := doc.Edit()
	if err := e.AddHighlight(0, h); err != nil {
		t.Fatal(err)
	}
	out := editOpen(t, editSave(t, e))
	p, _ := out.Page(0)
	var a *Annotation
	annots := p.Annotations()
	for i := range annots {
		if annots[i].Subtype == "Highlight" {
			a = &annots[i]
		}
	}
	if a == nil {
		t.Fatal("no highlight")
	}
	m := a.Markup
	if a.Contents != h.Note || a.Name != h.Name || m.Author != h.Author || m.Opacity != h.Opacity ||
		!reflect.DeepEqual(m.Color, h.Color) || !m.Modified.Equal(when) || !m.Created.Equal(when) {
		t.Errorf("highlight %+v, markup %+v", a, m)
	}
	if len(m.Quads) != len(h.Quads) {
		t.Fatalf("%d quads, want %d", len(m.Quads), len(h.Quads))
	}
	for i, q := range m.Quads {
		for k, pt := range q.P {
			if math.Abs(pt[0]-h.Quads[i].P[k][0]) > 1e-6 || math.Abs(pt[1]-h.Quads[i].P[k][1]) > 1e-6 {
				t.Errorf("quad %d: %v, want %v", i, q, h.Quads[i])
			}
		}
	}
	if m.Popup == nil || m.Popup.Open || annots[len(annots)-1].Index != m.Popup.Index {
		t.Errorf("popup %+v", m.Popup)
	}
}
