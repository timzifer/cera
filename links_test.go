package cera

import (
	"context"
	"math"
	"testing"
)

func TestPageLinks(t *testing.T) {
	data := buildPDF([]string{""}, "/Annots [100 0 R 101 0 R 102 0 R 103 0 R 104 0 R 105 0 R]",
		// Two quads inside the rectangle.
		"<< /Subtype /Link /Rect [10 20 50 40] /QuadPoints [10 40 50 40 10 30 50 30 10 30 30 30 10 20 30 20] /A << /S /URI /URI (https://a.example/) >> >>",
		// Quad points outside the rectangle: the rectangle.
		"<< /Subtype /Link /Rect [10 20 50 30] /QuadPoints [0 0 1 0 0 1 1 1] /Dest [10 0 R /Fit] >>",
		"<< /Subtype /Link /Rect [10 20 50 30] /F 2 /Dest [10 0 R /Fit] >>", // hidden
		"<< /Subtype /Text /Rect [10 20 50 30] >>",
		"<< /Subtype /Link /Rect [0 0 5 5] /A << /S /GoToR /F << /UF (other.pdf) >> /D [3 /FitH 700] >> >>",
		"<< /Subtype /Link /Rect [0 0 5 5] /A << /S /Launch /F (run.exe) >> >>",
	)
	doc, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := doc.Page(0)
	links := p.Links()
	if len(links) != 4 {
		t.Fatalf("%d links", len(links))
	}
	// The page is 100 high: user y 40 is displayed y 60.
	if l := links[0]; l.Index != 0 || len(l.Quads) != 2 || l.Quads[0] != (Quad{[4][2]float64{{10, 60}, {50, 60}, {10, 70}, {50, 70}}}) || l.Target.URI != "https://a.example/" {
		t.Errorf("quad points %+v", l)
	}
	if !links[0].Contains(20, 75) || links[0].Contains(40, 75) {
		t.Error("Contains: the second quad is half as wide")
	}
	if l := links[1]; l.Index != 1 || len(l.Quads) != 1 || l.Quads[0] != (Quad{[4][2]float64{{10, 70}, {50, 70}, {10, 80}, {50, 80}}}) || l.Target.Page != 0 {
		t.Errorf("rectangle %+v", l)
	}
	if l := links[2].Target; !l.External || l.File != "other.pdf" || l.Page != 3 || l.Fit != "FitH" || len(l.Params) != 1 || l.Params[0] != 700 {
		t.Errorf("GoToR %+v", l)
	}
	if l := links[3].Target; !l.External || l.File != "run.exe" || l.Page != -1 {
		t.Errorf("Launch %+v", l)
	}
	if _, ok := doc.Destination(&links[2].Target); ok {
		t.Error("followed an external link")
	}
}

func TestURLs(t *testing.T) {
	text := layoutPage(t, `BT /F1 10 Tf 20 360 Td (see https://example.com/a_\(b\)\). or www.cera.dev, mailto:x@y.z. http:// no) Tj ET`, TextOptions{}, "")
	urls := text.URLs()
	want := []string{"https://example.com/a_(b)", "http://www.cera.dev", "mailto:x@y.z"}
	if len(urls) != len(want) {
		t.Fatalf("%d addresses: %+v", len(urls), urls)
	}
	for i, u := range urls {
		if u.Target.URI != want[i] || u.Index != -1 || len(u.Quads) != 1 {
			t.Errorf("address %d: %+v", i, u)
		}
	}
	if r, ok := text.CharAt(urls[1].Quads[0].P[0][0]+1, urls[1].Quads[0].P[0][1]+3); !ok || text.Chars[r].Text != "w" {
		t.Errorf("the quad of www starts at %d", r)
	}
}

func TestDestination(t *testing.T) {
	for _, rot := range []int{0, 90} {
		data := buildPDF([]string{"", ""}, "/Rotate "+map[int]string{0: "0", 90: "90"}[rot])
		doc, err := Open(data)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := doc.Page(1)
		m := p.DisplayMatrix()
		x, y := m.Apply(10, 90)

		d, ok := doc.Destination(&LinkTarget{Page: 1, Fit: "XYZ", Params: []float64{10, 90, 2}})
		if !ok || d.Page != 1 || d.Fit != "XYZ" || !approx(d.X, x) || !approx(d.Y, y) || d.Zoom != 2 {
			t.Errorf("rotate %d: XYZ %+v, want %g %g", rot, d, x, y)
		}
		d, _ = doc.Destination(&LinkTarget{Page: 1, Fit: "XYZ", Params: []float64{math.NaN(), 90, 0}})
		if rot == 0 && (!math.IsNaN(d.X) || !approx(d.Y, 10) || d.Zoom != 0) || rot == 90 && (!approx(d.X, x) || !math.IsNaN(d.Y)) {
			t.Errorf("rotate %d: XYZ with null left %+v", rot, d)
		}
		d, _ = doc.Destination(&LinkTarget{Page: 1, Fit: "FitH", Params: []float64{90}})
		if rot == 0 && (d.Fit != "FitH" || !approx(d.Y, 10) || !math.IsNaN(d.X)) || rot == 90 && (d.Fit != "FitV" || !approx(d.X, x) || !math.IsNaN(d.Y)) {
			t.Errorf("rotate %d: FitH %+v", rot, d)
		}
		d, _ = doc.Destination(&LinkTarget{Page: 1, Fit: "FitR", Params: []float64{10, 20, 50, 90}})
		if want := (Quad{[4][2]float64{{10, 20}, {50, 20}, {10, 90}, {50, 90}}}).Transform(m).Bounds(); d.Fit != "FitR" || d.Rect != want {
			t.Errorf("rotate %d: FitR %+v, want %v", rot, d, want)
		}
		if d, ok := doc.Destination(&LinkTarget{Page: 0}); !ok || d.Fit != "Fit" {
			t.Errorf("no view %+v", d)
		}
		for _, lt := range []*LinkTarget{nil, {URI: "https://x/", Page: -1}, {Page: 2}, {Named: "nowhere", Page: -1}} {
			if _, ok := doc.Destination(lt); ok {
				t.Errorf("resolved %+v", lt)
			}
		}
	}
}

func TestHit(t *testing.T) {
	data := buildPDFCatalog("/AcroForm << /Fields [102 0 R] >>", []string{"BT /F1 10 Tf 10 50 Td (Hello www.x.org) Tj ET"},
		"/Resources << /Font << /F1 100 0 R >> >> /Annots [101 0 R 102 0 R]",
		helvetica,
		"<< /Subtype /Link /Rect [150 10 190 30] /A << /S /URI /URI (https://b.example/) >> >>",
		"<< /Type /Annot /Subtype /Widget /FT /Tx /T (f) /Rect [150 60 190 90] >>",
	)
	doc, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	x := NewTextIndex(doc, TextOptions{}, 0)
	ctx := context.Background()
	for _, c := range []struct {
		x, y float64
		kind HitKind
	}{
		{170, 80, HitLink},
		{170, 25, HitWidget},
		{12, 47, HitText},
		{55, 47, HitLink}, // the address in the text
		{5, 5, HitNone},
	} {
		h, err := x.Hit(ctx, 0, c.x, c.y)
		if err != nil || h.Kind != c.kind {
			t.Errorf("Hit(%g, %g) = %+v %v, want kind %d", c.x, c.y, h, err, c.kind)
			continue
		}
		switch h.Kind {
		case HitLink:
			if h.Link == nil || h.Link.Target.URI == "" {
				t.Errorf("link %+v", h.Link)
			}
		case HitWidget:
			if h.Widget == nil || h.Widget.Field.Name != "f" {
				t.Errorf("widget %+v", h.Widget)
			}
		case HitText:
			if h.Char != 0 || h.Pos != 0 {
				t.Errorf("text %+v", h)
			}
		}
	}
	if h, _ := x.Hit(ctx, 0, 55, 47); h.Link.Target.URI != "http://www.x.org" || h.Link.Index != -1 {
		t.Errorf("address %+v", h.Link)
	}
	if _, err := x.Hit(ctx, 3, 0, 0); err == nil {
		t.Error("page out of range")
	}
}
