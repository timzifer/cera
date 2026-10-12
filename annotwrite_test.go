package cera

import (
	"bytes"
	"context"
	"errors"
	"image"
	"testing"
	"time"
)

// highlightDoc is two pages with text, the first turned by rotate.
func highlightDoc(rotate string) []byte {
	return buildPDF([]string{"BT /F1 10 Tf 10 50 Td (alpha beta gamma) Tj ET", "BT /F1 10 Tf 10 50 Td (delta) Tj ET"},
		"/Rotate "+rotate+" /Resources << /Font << /F1 100 0 R >> >>", helvetica)
}

// yellowIn counts the yellow pixels of img in r, scaled by 2.
func yellowIn(img *image.RGBA, r Rect) int {
	n := 0
	for y := int(r.Y0 * 2); y < int(r.Y1*2); y++ {
		for x := int(r.X0 * 2); x < int(r.X1*2); x++ {
			c := img.RGBAAt(x, y)
			if c.R > 200 && c.G > 200 && c.B < 80 {
				n++
			}
		}
	}
	return n
}

func TestAddHighlight(t *testing.T) {
	for _, rot := range []string{"0", "90"} {
		src := highlightDoc(rot)
		doc := editOpen(t, src)
		p, _ := doc.Page(0)
		text, err := p.Text(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ms := text.Find("beta", SearchOptions{})
		if len(ms) != 1 {
			t.Fatalf("rotate %s: %d matches", rot, len(ms))
		}
		quads := text.Quads(ms[0].Range)
		e := doc.Edit()
		date := time.Date(2026, 10, 12, 15, 4, 5, 0, time.FixedZone("", 2*3600))
		if err := e.AddHighlight(0, Highlight{Quads: quads, Note: "Größe", Author: "me", Name: "h1", Date: date}); err != nil {
			t.Fatal(err)
		}
		if err := e.AddHighlight(0, Highlight{Kind: "Underline", Quads: quads, Color: []float64{1, 0, 0}, Opacity: 0.5}); err != nil {
			t.Fatal(err)
		}

		for name, out := range map[string][]byte{"save": editSave(t, e), "update": editUpdate(t, e, src)} {
			d := editOpen(t, out)
			sp, _ := d.Page(0)
			as := sp.Annotations()
			if len(as) != 3 || as[0].Subtype != "Highlight" || as[1].Subtype != "Popup" || as[2].Subtype != "Underline" {
				t.Fatalf("rotate %s, %s: annotations %+v", rot, name, as)
			}
			h := &as[0]
			if h.Contents != "Größe" || h.Name != "h1" || h.Flags != AnnotPrint || textString(d.resolve(h.dict.Get("T"))) != "me" ||
				textString(d.resolve(h.dict.Get("M"))) != "D:20261012150405+02'00'" {
				t.Errorf("rotate %s, %s: highlight %+v", rot, name, h)
			}
			if ap := d.appearance(h, ""); ap == nil || d.dict(d.dict(d.dict(ap.Dict.Get("Resources")).Get("ExtGState")).Get("GS0")).Get("BM").String() != "/Multiply" {
				t.Errorf("rotate %s, %s: appearance %v", rot, name, ap)
			}
			// The quad points read back as the quads, in displayed points.
			m := sp.DisplayMatrix()
			got := d.quadPoints(h)
			if len(got) != len(quads) {
				t.Fatalf("rotate %s, %s: %d quads", rot, name, len(got))
			}
			for k, q := range got {
				q = q.Transform(m)
				for c := range 4 {
					if !approx(q.P[c][0], quads[k].P[c][0]) || !approx(q.P[c][1], quads[k].P[c][1]) {
						t.Errorf("rotate %s, %s: quad %v, want %v", rot, name, q, quads[k])
					}
				}
			}
			img, _ := editRender(t, d, 0)
			if n := yellowIn(img, quads[0].Bounds()); n < 20 {
				t.Errorf("rotate %s, %s: %d yellow pixels", rot, name, n)
			}
			if n := yellowIn(img, Rect{0, 0, 8, 8}); n != 0 {
				t.Errorf("rotate %s, %s: yellow off the highlight", rot, name)
			}
			if len(d.Edit().pages) != 2 {
				t.Error("pages lost")
			}
			// The second page has none.
			if p1, _ := d.Page(1); len(p1.Annotations()) != 0 {
				t.Errorf("rotate %s, %s: annotations on page 1", rot, name)
			}
		}
	}
}

func TestAddHighlightMoved(t *testing.T) {
	src := highlightDoc("0")
	doc := editOpen(t, src)
	e := doc.Edit()
	q := Quad{[4][2]float64{{10, 40}, {40, 40}, {10, 52}, {40, 52}}}
	if err := e.AddHighlight(0, Highlight{Quads: []Quad{q}}); err != nil {
		t.Fatal(err)
	}
	if err := e.MovePage(0, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.ImportPages(0, doc, EditPage{Index: 0}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddHighlight(0, Highlight{Quads: []Quad{q}}); err == nil {
		t.Error("highlight on an imported page")
	}
	d := editOpen(t, editSave(t, e))
	for i, want := range []int{0, 0, 1} { // the copy of the first page, the second, the first
		p, _ := d.Page(i)
		if n := len(p.Annotations()); n != want {
			t.Errorf("page %d: %d annotations, want %d", i, n, want)
		}
	}
	if err := e.DeletePages(2); err != nil {
		t.Fatal(err)
	}
	if e.annotated() {
		t.Error("the highlight stayed with a deleted page")
	}
}

func TestAddHighlightFlattened(t *testing.T) {
	src := buildPDFCatalog("/AcroForm << /Fields [102 0 R] >>", []string{"BT /F1 10 Tf 10 50 Td (Hello) Tj ET"},
		"/Resources << /Font << /F1 100 0 R >> >> /Annots [101 0 R 102 0 R]",
		helvetica,
		"<< /Subtype /Text /Rect [150 10 170 30] >>",
		"<< /Type /Annot /Subtype /Widget /FT /Tx /T (f) /V (x) /Rect [150 60 190 90] >>",
	)
	e := editOpen(t, src).Edit()
	if err := e.Flatten(); err != nil {
		t.Fatal(err)
	}
	if err := e.AddHighlight(0, Highlight{Quads: []Quad{{[4][2]float64{{10, 40}, {40, 40}, {10, 52}, {40, 52}}}}}); err != nil {
		t.Fatal(err)
	}
	d := editOpen(t, editSave(t, e))
	p, _ := d.Page(0)
	as := p.Annotations()
	if len(as) != 2 || as[0].Subtype != "Text" || as[1].Subtype != "Highlight" {
		t.Errorf("annotations %+v", as)
	}
}

func TestAddHighlightErrors(t *testing.T) {
	e := editOpen(t, highlightDoc("0")).Edit()
	q := []Quad{{[4][2]float64{{10, 40}, {40, 40}, {10, 52}, {40, 52}}}}
	for _, c := range []struct {
		page int
		h    Highlight
	}{
		{2, Highlight{Quads: q}},
		{0, Highlight{}},
		{0, Highlight{Quads: []Quad{{[4][2]float64{{10, 40}, {10, 40}, {10, 52}, {10, 52}}}}}}, // no area
		{0, Highlight{Quads: q, Kind: "Circle"}},
		{0, Highlight{Quads: q, Color: []float64{1, 0}}},
		{0, Highlight{Quads: q, Opacity: 2}},
	} {
		if err := e.AddHighlight(c.page, c.h); err == nil {
			t.Errorf("AddHighlight(%d, %+v) took it", c.page, c.h)
		}
	}
	if e.annotated() {
		t.Error("a failed AddHighlight changed the editor")
	}
	if err := NewEditor().AddHighlight(0, Highlight{Quads: q}); err == nil {
		t.Error("highlight in a new document")
	}

	// A certification that allows forms only refuses annotations; one that
	// allows annotations takes them.
	for mdp, want := range map[int]error{2: ErrCertified, 3: nil} {
		e := editOpen(t, signedFile(false, mdp, false)).Edit()
		if err := e.AddHighlight(0, Highlight{Quads: q}); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := e.Update(&out); !errors.Is(err, want) && err != want {
			t.Errorf("DocMDP %d: %v, want %v", mdp, err, want)
		}
	}
}

func TestPDFDate(t *testing.T) {
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "D:20260102030405Z"},
		{time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("", -(5*3600+30*60))), "D:20260102030405-05'30'"},
	} {
		if got := pdfDate(c.t); got != c.want {
			t.Errorf("pdfDate(%v) = %s, want %s", c.t, got, c.want)
		}
		if got := parseDate(c.want); !got.Equal(c.t) {
			t.Errorf("%s reads as %v", c.want, got)
		}
	}
}
