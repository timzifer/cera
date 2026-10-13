package cera

import (
	"context"
	"image/color"
	"math"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
)

// boxFile is a page of 300 by 200 points whose CropBox leaves out 20
// points at the sides and 10 above and below, with the boxes of a page for
// print inside: a red mark at the lower left corner of the medium, a blue
// square at that of the trimmed page, a line of text, a link over it and a
// square annotation.
func boxFile(boxes string) []byte {
	return testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R>>",
			2:  "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 300 200] /ArtBox [90 90 100 100]>>",
			3:  "<</Type /Page /Parent 2 0 R /Contents 5 0 R /Resources <</Font <</F1 6 0 R>>>> /Annots [10 0 R 11 0 R] " + boxes + ">>",
			4:  "<</Type /Page /Parent 2 0 R>>",
			5:  testpdf.Stream("", []byte("1 0 0 rg 0 0 10 10 re f 0 0 1 rg 40 30 20 20 re f 0 g BT /F1 12 Tf 100 150 Td (Hello) Tj ET")),
			6:  "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
			10: "<</Type /Annot /Subtype /Link /Rect [100 145 130 165] /Dest [4 0 R /FitH 180]>>",
			11: "<</Type /Annot /Subtype /Square /Rect [200 40 240 80] /C [0 0 1] /IC [0 1 0]>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

const printBoxes = "/CropBox [20 10 280 190] /BleedBox [30 20 270 180] /TrimBox [40 30 260 170]"

func TestPageBoxes(t *testing.T) {
	media, crop := Rect{0, 0, 300, 200}, Rect{20, 10, 280, 190}
	for _, c := range []struct {
		boxes string
		want  PageBoxes
	}{
		// The ArtBox of the page tree is not the page's: only the MediaBox
		// and the CropBox are inherited.
		{"", PageBoxes{media, media, media, media, media}},
		{"/CropBox [20 10 280 190]", PageBoxes{media, crop, crop, crop, crop}},
		{printBoxes + " /ArtBox [50 40 250 160]", PageBoxes{media, crop, Rect{30, 20, 270, 180}, Rect{40, 30, 260, 170}, Rect{50, 40, 250, 160}}},
		// Corners in any order, and no further than the medium.
		{"/TrimBox [320 170 40 -30]", PageBoxes{media, media, media, Rect{40, 0, 300, 170}, media}},
		// A box may be larger than the CropBox.
		{"/CropBox [20 10 280 190] /BleedBox [10 5 290 195]", PageBoxes{media, crop, Rect{10, 5, 290, 195}, crop, crop}},
		// Nothing of it on the medium, no area, not a rectangle.
		{"/CropBox [20 10 280 190] /BleedBox [400 400 500 500] /TrimBox [40 30 40 170] /ArtBox (all)", PageBoxes{media, crop, crop, crop, crop}},
	} {
		doc := editOpen(t, boxFile(c.boxes))
		p, _ := doc.Page(0)
		if got := p.Boxes(); got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.boxes, got, c.want)
		}
		if p.Box != c.want.Crop {
			t.Errorf("%s: Box %v", c.boxes, p.Box)
		}
		for b, want := range []Rect{c.want.Crop, c.want.Media, c.want.Bleed, c.want.Trim, c.want.Art} {
			if got := p.Boxes().Box(PageBox(b)); got != want {
				t.Errorf("%s: box %d is %v, want %v", c.boxes, b, got, want)
			}
			if v := p.InBox(PageBox(b)); v.Box != want || v.Boxes() != c.want {
				t.Errorf("%s: the view of box %d shows %v, want %v", c.boxes, b, v.Box, want)
			}
		}
	}
}

// TestInBox views another box of a page: its size, what is drawn, the text
// and the annotations are those of the box.
func TestInBox(t *testing.T) {
	doc := editOpen(t, boxFile(printBoxes))
	p, _ := doc.Page(0)
	m, tr := p.InBox(BoxMedia), p.InBox(BoxTrim)
	// The same view each time; a box and a rotation in either order.
	if p.InBox(BoxMedia) != m || m == p || tr == m || p.InBox(BoxCrop) != p || m.InBox(BoxCrop) != p || m.InBox(BoxTrim) != tr ||
		p.InBox(-1) != p || p.InBox(numBoxes) != p {
		t.Error("InBox does not return the views it made")
	}
	mr := m.Rotated(90)
	if mr != p.Rotated(90).InBox(BoxMedia) || mr.Rotated(-90) != m || mr.InBox(BoxCrop) != p.Rotated(90) || mr.Rotate != 90 {
		t.Error("a box and a rotation do not combine")
	}
	for _, c := range []struct {
		v    *Page
		w, h int
	}{{p, 260, 180}, {m, 300, 200}, {tr, 220, 140}, {p.InBox(BoxBleed), 240, 160}, {p.InBox(BoxArt), 260, 180}, {mr, 200, 300}, {tr.Rotated(270), 140, 220}} {
		if b := c.v.Bounds(1); b.Dx() != c.w || b.Dy() != c.h {
			t.Errorf("bounds %v, want %d by %d", b, c.w, c.h)
		}
		if w, h := c.v.Size(); w != float64(c.w) || h != float64(c.h) {
			t.Errorf("size %g by %g, want %d by %d", w, h, c.w, c.h)
		}
	}

	// The page is the part of its medium inside the CropBox, and the
	// trimmed page the part of that inside the TrimBox.
	blue := color.RGBA{0, 0, 255, 255}
	pi, mi, ti := renderView(t, p), renderView(t, m), renderView(t, tr)
	for _, c := range []struct {
		name   string
		got    color.RGBA
		wanted color.RGBA
	}{
		{"the mark on the medium", mi.RGBAAt(5, 195), red},
		{"the corner of the page", pi.RGBAAt(5, 175), white},
		{"the square on the medium", mi.RGBAAt(50, 160), blue},
		{"the square on the page", pi.RGBAAt(30, 150), blue},
		{"the square on the trimmed page", ti.RGBAAt(10, 130), blue},
	} {
		if c.got != c.wanted {
			t.Errorf("%s is %v, want %v", c.name, c.got, c.wanted)
		}
	}
	for y := range 180 {
		for x := range 260 {
			if pi.RGBAAt(x, y) != mi.RGBAAt(x+20, y+10) {
				t.Fatalf("the pixel at %d, %d of the page is not the one of its medium", x, y)
			}
		}
	}
	for y := range 140 {
		for x := range 220 {
			if ti.RGBAAt(x, y) != pi.RGBAAt(x+20, y+20) {
				t.Fatalf("the pixel at %d, %d of the trimmed page is not the one of the page", x, y)
			}
		}
	}
	// The mark turns with its medium: from the lower left to the upper left.
	if c := renderView(t, mr).RGBAAt(5, 5); c != red {
		t.Errorf("the mark on the turned medium is %v", c)
	}
	// The lists are kept, and Release frees those of the views.
	if n, _ := pixDiff(pi, renderView(t, p)); n != 0 {
		t.Error("the page renders differently after its views were rendered")
	}
	p.Release()
	if m.dl != nil || tr.dl != nil || mr.dl != nil {
		t.Error("Release left the display lists of the views")
	}
	if n, _ := pixDiff(mi, renderView(t, m)); n != 0 {
		t.Error("the view renders differently after Release")
	}

	// The text, the links and the annotations are 20 points further right
	// and 10 further down on the medium.
	pt, err := p.Text(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mt, err := m.Text(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mt.String() != "Hello" || pt.String() != "Hello" {
		t.Errorf("text %q on the medium, %q on the page", mt.String(), pt.String())
	}
	po, mo := pt.Chars[0].Origin, mt.Chars[0].Origin
	if math.Abs(mo[0]-po[0]-20) > 1e-6 || math.Abs(mo[1]-po[1]-10) > 1e-6 || math.Abs(mo[0]-100) > 1e-6 || math.Abs(mo[1]-50) > 1e-6 {
		t.Errorf("H is at %v on the page and at %v on its medium", po, mo)
	}
	moved := func(r Rect) Rect { return Rect{r.X0 + 20, r.Y0 + 10, r.X1 + 20, r.Y1 + 10} }
	pl, ml := p.Links(), m.Links()
	if len(pl) != 1 || len(ml) != 1 || !sameRect(ml[0].Quads[0].Bounds(), moved(pl[0].Quads[0].Bounds())) {
		t.Errorf("links %+v on the page, %+v on its medium", pl, ml)
	}
	pa, ma := p.Annotations()[1].Markup.Bounds, m.Annotations()[1].Markup.Bounds
	if !sameRect(ma, moved(pa)) || !sameRect(ma, Rect{200, 120, 240, 160}) {
		t.Errorf("square at %v on the page and at %v on its medium", pa, ma)
	}
	// A destination on the medium of its page: 180 from the bottom of 200.
	next, _ := doc.Page(1)
	if d, ok := next.InBox(BoxMedia).Destination(&pl[0].Target); !ok || d.Fit != "FitH" || d.Y != 20 {
		t.Errorf("destination %+v, %v", d, ok)
	}
}

// TestInBoxIndex hits what the view of a box shows.
func TestInBoxIndex(t *testing.T) {
	doc := editOpen(t, boxFile(printBoxes))
	x := NewTextIndex(doc, TextOptions{}, 0)
	ctx := context.Background()
	// The link is at x 100 to 130, y 35 to 55 of the medium, and 20 and 10
	// less of the page.
	if h, err := x.Hit(ctx, 0, 85, 30); err != nil || h.Kind != HitLink {
		t.Fatalf("hit %+v, %v", h, err)
	}
	x.SetBox(BoxMedia)
	if h, _ := x.Hit(ctx, 0, 85, 30); h.Kind == HitLink {
		t.Error("the link is still where it was")
	}
	if h, err := x.Hit(ctx, 0, 125, 50); err != nil || h.Kind != HitLink || h.Link.Target.Page != 1 {
		t.Errorf("hit on the medium %+v, %v", h, err)
	}
	x.SetRotation(90)
	// Turned: x = 200 - y, y = x.
	if h, err := x.Hit(ctx, 0, 200-50, 125); err != nil || h.Kind != HitLink {
		t.Errorf("hit on the turned medium %+v, %v", h, err)
	}
}

// TestInBoxEdit writes annotations given in the coordinates of the view of
// a box, and reads them back there.
func TestInBoxEdit(t *testing.T) {
	doc := editOpen(t, boxFile(printBoxes))
	p, _ := doc.Page(0)
	m := p.InBox(BoxMedia)

	e := doc.Edit()
	// Changed through the view, and changed in nothing: it stays.
	a, ok := m.EditAnnotation(1)
	if !ok || a.Box != BoxMedia || a.View != 0 {
		t.Fatalf("EditAnnotation on the view: %+v, %v", a, ok)
	}
	if err := e.SetAnnotation(0, 1, a); err != nil {
		t.Fatal(err)
	}
	// Added at the top left corner of the medium, and of the trimmed page
	// turned.
	box := Rect{0, 0, 30, 20}
	if _, err := e.AddAnnotation(0, NewAnnotation{Subtype: "Square", Box: BoxMedia, Markup: Markup{Bounds: box, BorderWidth: 1}}); err != nil {
		t.Fatal(err)
	}
	quad := QuadOf(Rect{10, 20, 60, 32})
	if err := e.AddHighlight(0, Highlight{Quads: []Quad{quad}, Box: BoxTrim, View: 90}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddAnnotation(0, NewAnnotation{Subtype: "Caret", Box: numBoxes, Markup: Markup{Bounds: box}}); err == nil {
		t.Error("an annotation in the view of no box was added")
	}

	out := editOpen(t, editSave(t, e))
	op, _ := out.Page(0)
	if got, want := op.Annotations()[1].Rect, p.Annotations()[1].Rect; !sameRect(got, want) {
		t.Errorf("the square moved from %v to %v", want, got)
	}
	if got, ok := op.InBox(BoxMedia).EditAnnotation(2); !ok || !sameRect(got.Bounds, box) || got.Box != BoxMedia {
		t.Errorf("the new square is at %v on the medium, want %v", got.Bounds, box)
	}
	// In user space: the upper edge of the medium is at y 200.
	if got := op.Annotations()[2].Rect; !sameRect(got, Rect{0, 180, 30, 200}) {
		t.Errorf("the new square has the rectangle %v", got)
	}
	// On the page it is outside, 20 to the left and 10 above.
	if got, _ := op.EditAnnotation(2); !sameRect(got.Bounds, Rect{-20, -10, 10, 10}) {
		t.Errorf("the new square is at %v on the page", got.Bounds)
	}
	hl, ok := op.InBox(BoxTrim).Rotated(90).EditAnnotation(3)
	if !ok || len(hl.Quads) != 1 || !samePoints(hl.Quads[0].P[:], quad.P[:]) || hl.Box != BoxTrim || hl.View != 90 {
		t.Errorf("the highlight has the quads %v in the view, want %v", hl.Quads, quad)
	}
}
