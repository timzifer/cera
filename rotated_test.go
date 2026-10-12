package cera

import (
	"context"
	"image"
	"math"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
)

// viewFile is a page of 300 by 200 points with a red box at its lower
// left, a line of text, a link over the text and a square annotation, and
// a second page that a link leads to.
func viewFile() []byte {
	return testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R>>",
			2:  "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 300 200]>>",
			3:  "<</Type /Page /Parent 2 0 R /Contents 5 0 R /Resources <</Font <</F1 6 0 R>>>> /Annots [10 0 R 11 0 R]>>",
			4:  "<</Type /Page /Parent 2 0 R>>",
			5:  testpdf.Stream("", []byte("1 0 0 rg 20 10 60 30 re f BT /F1 12 Tf 100 150 Td (Hello) Tj ET")),
			6:  "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
			10: "<</Type /Annot /Subtype /Link /Rect [100 145 130 165] /Dest [4 0 R /FitH 180]>>",
			11: "<</Type /Annot /Subtype /Square /Rect [200 20 260 60] /C [0 0 1] /IC [0 1 0]>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

func renderView(t *testing.T, p *Page) *image.RGBA {
	t.Helper()
	dst := image.NewRGBA(p.Bounds(1))
	if err := p.Render(context.Background(), dst, RenderOptions{Scale: 1, Background: white, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	return dst
}

// TestRotated views a page turned: its size, what is drawn, the text and
// the annotations are those of the turned page.
func TestRotated(t *testing.T) {
	doc := editOpen(t, viewFile())
	p, _ := doc.Page(0)
	v := p.Rotated(90)
	if v == p || v.Rotate != 90 || p.Rotate != 0 {
		t.Fatalf("rotations %d and %d", p.Rotate, v.Rotate)
	}
	// The same view each time, and turns add up.
	if p.Rotated(90) != v || p.Rotated(0) != p || p.Rotated(360) != p || v.Rotated(-90) != p || v.Rotated(270) != p ||
		v.Rotated(90) != p.Rotated(180) || p.Rotated(100) != v || p.Rotated(-270) != v {
		t.Error("Rotated does not return the views it made")
	}
	if b := v.Bounds(1); b.Dx() != 200 || b.Dy() != 300 {
		t.Errorf("bounds of the turned page %v", b)
	}
	if w, h := v.Size(); w != 200 || h != 300 {
		t.Errorf("size of the turned page %g by %g", w, h)
	}

	// Turned clockwise, the pixel at (x, y) is at (height-1-y, x).
	a, b := renderView(t, p), renderView(t, v)
	h := a.Bounds().Dy()
	diff := 0
	for y := range h {
		for x := range a.Bounds().Dx() {
			if a.RGBAAt(x, y) != b.RGBAAt(h-1-y, x) {
				diff++
			}
		}
	}
	// Text is drawn with other pixels when it is turned; boxes are not.
	if diff > 400 {
		t.Errorf("%d pixels of the turned page are not those of the page", diff)
	}
	// The box at the lower left of the page is at the upper left of the
	// page turned clockwise.
	if c := b.RGBAAt(25, 40); c != red {
		t.Errorf("the red box is not at the upper left of the turned page: %v", c)
	}
	// Both lists are kept: the page renders as before after its view did.
	if n, _ := pixDiff(a, renderView(t, p)); n != 0 {
		t.Error("the page renders differently after its view was rendered")
	}
	p.Release()
	if n, _ := pixDiff(b, renderView(t, v)); n != 0 {
		t.Error("the view renders differently after Release")
	}

	// The text reads the same, at turned places.
	pt, err := p.Text(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	vt, err := v.Text(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if vt.String() != "Hello" || vt.String() != pt.String() {
		t.Errorf("text %q", vt.String())
	}
	po, vo := pt.Chars[0].Origin, vt.Chars[0].Origin
	if math.Abs(vo[0]-(200-po[1])) > 1e-6 || math.Abs(vo[1]-po[0]) > 1e-6 {
		t.Errorf("H is at %v on the page and at %v on its view", po, vo)
	}
	if d := vt.Lines[0].Dir; math.Abs(d[0]) > 1e-9 || d[1] < 0.99 {
		t.Errorf("the line of the view runs along %v, want down", d)
	}

	// Links and annotations are where the view shows them.
	pl, vl := p.Links(), v.Links()
	if len(vl) != 1 {
		t.Fatalf("%d links", len(vl))
	}
	pb, vb := pl[0].Quads[0].Bounds(), vl[0].Quads[0].Bounds()
	if !sameRect(vb, Rect{200 - pb.Y1, pb.X0, 200 - pb.Y0, pb.X1}) {
		t.Errorf("link at %v on the page and at %v on its view", pb, vb)
	}
	pm, vm := p.Annotations()[1].Markup.Bounds, v.Annotations()[1].Markup.Bounds
	if !sameRect(vm, Rect{200 - pm.Y1, pm.X0, 200 - pm.Y0, pm.X1}) {
		t.Errorf("square at %v on the page and at %v on its view", pm, vm)
	}

	// A destination on the target as it is viewed: the top edge of a page
	// turned by 90 degrees is its left edge.
	next, _ := doc.Page(1)
	d0, ok := doc.Destination(&pl[0].Target)
	if !ok || d0.Fit != "FitH" || d0.Y != 20 {
		t.Errorf("destination %+v, %v", d0, ok)
	}
	d1, ok := next.Rotated(90).Destination(&pl[0].Target)
	if !ok || d1.Page != 1 || d1.Fit != "FitV" || d1.X != 180 {
		t.Errorf("destination on the turned page %+v, %v", d1, ok)
	}
	if _, ok := p.Destination(&pl[0].Target); ok {
		t.Error("a destination on a page it does not lead to")
	}
}

// TestRotatedIndex hits what a turned view shows.
func TestRotatedIndex(t *testing.T) {
	doc := editOpen(t, viewFile())
	x := NewTextIndex(doc, TextOptions{}, 0)
	ctx := context.Background()
	// The link is at x 100 to 130, y 35 to 55 of the page as displayed.
	h, err := x.Hit(ctx, 0, 110, 45)
	if err != nil || h.Kind != HitLink {
		t.Fatalf("hit %+v, %v", h, err)
	}
	x.SetRotation(90)
	if h, _ := x.Hit(ctx, 0, 110, 45); h.Kind == HitLink {
		t.Error("the link is still where it was")
	}
	// Turned: x = 200 - y, y = x.
	h, err = x.Hit(ctx, 0, 200-45, 110)
	if err != nil || h.Kind != HitLink || h.Link.Target.Page != 1 {
		t.Errorf("hit on the turned page %+v, %v", h, err)
	}
	text, err := x.Page(ctx, 0)
	if err != nil || text.Lines[0].Dir[1] < 0.99 {
		t.Errorf("text of the turned page: %v, %v", text, err)
	}
}

// TestRotatedEdit writes annotations given in the coordinates of a turned
// view, and reads them back there.
func TestRotatedEdit(t *testing.T) {
	src := viewFile()
	doc := editOpen(t, src)
	p, _ := doc.Page(0)
	v := p.Rotated(90)

	e := doc.Edit()
	// Changed through the view, and changed in nothing: it stays.
	a, ok := v.EditAnnotation(1)
	if !ok || a.View != 90 {
		t.Fatalf("EditAnnotation on the view: %+v, %v", a, ok)
	}
	if err := e.SetAnnotation(0, 1, a); err != nil {
		t.Fatal(err)
	}
	// Added where the view shows the top left corner.
	box := Rect{10, 20, 60, 50}
	note := NewAnnotation{Subtype: "FreeText", Contents: "Up", View: 90, Markup: Markup{Bounds: box, BorderWidth: 1}}
	if _, err := e.AddAnnotation(0, note); err != nil {
		t.Fatal(err)
	}
	quad := QuadOf(Rect{100, 100, 150, 112})
	if err := e.AddHighlight(0, Highlight{Quads: []Quad{quad}, View: 90}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddAnnotation(0, NewAnnotation{Subtype: "Caret", View: 45, Markup: Markup{Bounds: box}}); err == nil {
		t.Error("an annotation in a view turned by 45 degrees was added")
	}

	out := editOpen(t, editSave(t, e))
	op, _ := out.Page(0)
	ov := op.Rotated(90)
	if got, want := op.Annotations()[1].Rect, p.Annotations()[1].Rect; !sameRect(got, want) {
		t.Errorf("the square moved from %v to %v", want, got)
	}
	free, ok := ov.EditAnnotation(2)
	if !ok || !sameRect(free.Bounds, box) || free.View != 90 {
		t.Errorf("the free text is at %v in the view, want %v", free.Bounds, box)
	}
	// On the page as the file turns it: x = y of the view, y = 200 - x.
	if got, _ := op.EditAnnotation(2); !sameRect(got.Bounds, Rect{20, 200 - 60, 50, 200 - 10}) {
		t.Errorf("the free text is at %v on the page", got.Bounds)
	}
	// Its text stands upright in the view.
	if r, _ := out.integer(op.Annotations()[2].dict.Get("Rotate")); r != 90 {
		t.Errorf("/Rotate %d of the free text, want 90", r)
	}
	hl, ok := ov.EditAnnotation(3)
	if !ok || len(hl.Quads) != 1 || !samePoints(hl.Quads[0].P[:], quad.P[:]) {
		t.Errorf("the highlight has the quads %v in the view, want %v", hl.Quads, quad)
	}
}
