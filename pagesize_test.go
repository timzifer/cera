package cera

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// sizeFile is a document of three pages: a Letter page with a TrimBox and
// annotations of every kind that says where it is by points, a page that
// is cropped and turned, and a page in units of two points.
func sizeFile() []byte {
	return testpdf.File{Objs: map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R /AcroForm <</Fields [30 0 R] /DA (/Helv 10 Tf 0 g) /DR <</Font <</Helv 10 0 R>>>>>>>>",
		2: "<</Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3>>",
		3: "<</Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /TrimBox [36 36 576 756] /Contents 6 0 R" +
			" /Resources <</Font <</F1 10 0 R>>>> /Annots [20 0 R 21 0 R 22 0 R 23 0 R 30 0 R]>>",
		4: "<</Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /CropBox [56 96 556 696] /Rotate 90 /Contents 7 0 R" +
			" /Resources <</Pattern <</P1 11 0 R>>>>>>",
		5: "<</Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] /UserUnit 2 /Contents 8 0 R>>",
		6: testpdf.Stream("", []byte("1 0 0 rg 100 100 100 100 re f 0 0 1 rg 500 700 100 80 re f BT /F1 24 Tf 100 400 Td (Size) Tj ET")),
		// With a pattern, which lies in the space of the page whatever
		// matrix the content sets.
		7:  testpdf.Stream("", []byte("0 1 0 rg 56 96 100 100 re f 0 0 1 rg 300 300 200 50 re f 0.5 0 0 0.5 0 0 cm /Pattern cs /P1 scn 300 800 400 300 re f")),
		11: testpdf.Stream("/Type /Pattern /PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 40 40] /XStep 40 /YStep 40 /Resources <<>>", []byte("1 0 0 rg 0 0 20 20 re f")),
		8:  testpdf.Stream("", []byte("1 0 1 rg 0 0 150 200 re f 0 g 200 300 50 50 re f")),
		10: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
		20: "<</Type /Annot /Subtype /Link /Rect [100 100 200 200] /Dest [3 0 R /Fit] /Border [0 0 0]>>",
		21: "<</Type /Annot /Subtype /Highlight /F 2 /Rect [100 390 200 430] /QuadPoints [100 430 200 430 100 390 200 390] /C [1 1 0]>>",
		22: "<</Type /Annot /Subtype /Line /F 2 /Rect [95 295 305 355] /L [100 300 300 350]>>",
		23: "<</Type /Annot /Subtype /Ink /F 2 /Rect [10 10 70 80] /InkList [[10 10 20 30 40 50] [60 60 70 80]] /RD [1 2 3 4]>>",
		30: "<</Type /Annot /Subtype /Widget /F 2 /FT /Tx /T (name) /V (v) /Rect [300 100 500 130]>>",
	}, Trailer: "/Root 1 0 R"}.Bytes()
}

// annotNums returns the numbers under key of the annotation of a subtype
// on page i of file, lists of lists flattened.
func annotNums(t *testing.T, file []byte, i int, subtype, key string) []float64 {
	t.Helper()
	d, err := pdf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	page, err := d.Page(i + 1)
	if err != nil {
		t.Fatal(err)
	}
	annots, _ := d.Resolve(page.Get("Annots")).Array()
	var out []float64
	var walk func(o pdf.Object)
	walk = func(o pdf.Object) {
		o = d.Resolve(o)
		if a, ok := o.Array(); ok {
			for _, e := range a {
				walk(e)
			}
		} else if v, ok := o.Float(); ok {
			out = append(out, v)
		}
	}
	for _, a := range annots {
		ad, _ := d.Resolve(a).Dict()
		if s, _ := ad.Get("Subtype").Name(); string(s) == subtype {
			walk(ad.Get(pdf.Name(key)))
			return out
		}
	}
	t.Fatalf("page %d has no %s annotation", i, subtype)
	return nil
}

// numsNear reports whether got is want to a hundredth.
func numsNear(got []float64, want ...float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if math.Abs(got[i]-want[i]) > 0.01 {
			return false
		}
	}
	return true
}

// cm returns the matrix in front of the content of page i of file.
func cm(t *testing.T, file []byte, i int) []float64 {
	t.Helper()
	d, err := pdf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := d.PageContents(i + 1)
	if err != nil {
		t.Fatal(err)
	}
	var m [6]float64
	if n, _ := fmt.Sscanf(string(dec.Data), "q %g %g %g %g %g %g cm", &m[0], &m[1], &m[2], &m[3], &m[4], &m[5]); n != 6 {
		t.Fatalf("the content of page %d starts %q", i, dec.Data[:min(len(dec.Data), 40)])
	}
	if !bytes.HasSuffix(bytes.TrimSpace(dec.Data), []byte("Q")) {
		t.Errorf("the content of page %d ends %q", i, dec.Data[max(len(dec.Data)-20, 0):])
	}
	return m[:]
}

// at returns the colour of page i of doc at a point of the displayed
// page.
func at(t *testing.T, doc *Document, i int, x, y float64) color.RGBA {
	t.Helper()
	img, _ := editRender(t, doc, i)
	return img.RGBAAt(int(2*x), int(2*y))
}

// sameAt checks that page i of a, drawn at one scale, is page j of b
// drawn at another.
func sameAt(t *testing.T, name string, a *Document, i int, sa float64, b *Document, j int, sb float64) {
	t.Helper()
	draw := func(d *Document, i int, s float64) *image.RGBA {
		p, err := d.Page(i)
		if err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(p.Bounds(s))
		if err := p.Render(t.Context(), img, RenderOptions{Scale: s, Background: white, Workers: 1}); err != nil {
			t.Fatal(err)
		}
		return img
	}
	x, y := draw(a, i, sa), draw(b, j, sb)
	if x.Bounds() != y.Bounds() {
		t.Errorf("%s: %v and %v", name, x.Bounds(), y.Bounds())
		return
	}
	// The edges may fall a hair apart: the numbers are written to four
	// places.
	if n, _ := pixDiff(x, y); n > x.Bounds().Dx()+x.Bounds().Dy() {
		t.Errorf("%s: %d pixels differ", name, n)
	}
}

var magenta = color.RGBA{255, 0, 255, 255}

// TestSetPageBox sets the boxes of pages: what is shown changes, the
// content stays where it is.
func TestSetPageBox(t *testing.T) {
	src := sizeFile()
	doc := editOpen(t, src)
	e := doc.Edit()
	// The first page shows the red square and a margin of ten points.
	if err := e.SetPageBox(0, BoxCrop, Rect{210, 90, 90, 210}); err != nil {
		t.Fatal(err)
	}
	if err := e.SetPageBox(0, BoxArt, Rect{100, 100, 200, 200}); err != nil {
		t.Fatal(err)
	}
	// The second page gets a larger medium; its CropBox stays.
	if err := e.SetPageBox(1, BoxMedia, Rect{-10, -10, 700, 800}); err != nil {
		t.Fatal(err)
	}
	out := editSave(t, e)
	od := editOpen(t, out)
	p, _ := od.Page(0)
	if w, h := p.Size(); w != 120 || h != 120 {
		t.Errorf("the cropped page is %g by %g", w, h)
	}
	b := p.Boxes()
	// The TrimBox is the one the page states: a box is clipped to the
	// medium, not to what is shown.
	if b.Crop != (Rect{90, 90, 210, 210}) || b.Media != (Rect{0, 0, 612, 792}) || b.Art != (Rect{100, 100, 200, 200}) ||
		b.Trim != (Rect{36, 36, 576, 756}) || b.Bleed != b.Crop {
		t.Errorf("the boxes are %+v", b)
	}
	if in, out := at(t, od, 0, 60, 60), at(t, od, 0, 5, 5); in != red || out != white {
		t.Errorf("the middle of the cropped page is %v, its corner %v", in, out)
	}
	if got := annotNums(t, out, 0, "Link", "Rect"); !numsNear(got, 100, 100, 200, 200) {
		t.Errorf("the link is at %v", got)
	}
	p, _ = od.Page(1)
	if b := p.Boxes(); b.Media != (Rect{-10, -10, 700, 800}) || b.Crop != (Rect{56, 96, 556, 696}) {
		t.Errorf("the boxes of the second page are %+v", b)
	}
	sameAt(t, "the second page", doc, 1, 1, od, 1, 1)

	// An update does it too, and leaves the file in front as it is.
	var upd bytes.Buffer
	if err := e.Update(&upd); err != nil {
		t.Fatal(err)
	}
	if ud := editOpen(t, upd.Bytes()); !bytes.HasPrefix(upd.Bytes(), src) || ud.r.Repaired() {
		t.Error("the update does not extend the file")
	} else if p, _ := ud.Page(0); p.Boxes().Crop != (Rect{90, 90, 210, 210}) {
		t.Errorf("updated, the page is cropped to %v", p.Boxes().Crop)
	}

	// What is not a box.
	for name, err := range map[string]error{
		"outside the medium": e.SetPageBox(2, BoxCrop, Rect{400, 0, 500, 100}),
		"no area":            e.SetPageBox(2, BoxTrim, Rect{10, 10, 10, 50}),
		"not a number":       e.SetPageBox(2, BoxCrop, Rect{0, 0, math.NaN(), 50}),
		"too large":          e.SetPageBox(2, BoxMedia, Rect{0, 0, 9000, 100}), // in units of two points
		"no such box":        e.SetPageBox(2, PageBox(9), Rect{0, 0, 10, 10}),
		"no such page":       e.SetPageBox(7, BoxCrop, Rect{0, 0, 10, 10}),
	} {
		if err == nil {
			t.Errorf("%s: set", name)
		}
	}
}

// TestResizePage puts pages on pages of another size: the content by a
// matrix in front of it, the boxes and the annotations with it.
func TestResizePage(t *testing.T) {
	src := sizeFile()
	doc := editOpen(t, src)

	// Letter to A4: scaled to the width, in the middle of the height.
	const s, ty = 595.28 / 612, (841.89 - 792*595.28/612) / 2
	e := doc.Edit()
	if err := e.ResizePage(0, 595.28, 841.89, FitInside); err != nil {
		t.Fatal(err)
	}
	out := editSave(t, e)
	od := editOpen(t, out)
	p, _ := od.Page(0)
	if w, h := p.Size(); math.Abs(w-595.28) > 0.01 || math.Abs(h-841.89) > 0.01 {
		t.Errorf("the page is %g by %g", w, h)
	}
	if m := cm(t, out, 0); !numsNear(m, s, 0, 0, s, 0, ty) {
		t.Errorf("the matrix is %v, want %g and %g up", m, s, ty)
	}
	b := p.Boxes()
	if !numsNear([]float64{b.Trim.X0, b.Trim.Y0, b.Trim.X1, b.Trim.Y1}, 36*s, 36*s+ty, 576*s, 756*s+ty) || b.Crop != b.Media || b.Art != b.Crop {
		t.Errorf("the boxes are %+v", b)
	}
	// The red square, 100 to 200 both ways, and what is beside it.
	if in, beside := at(t, od, 0, 150*s, 841.89-(150*s+ty)), at(t, od, 0, 198, 841.89-(150*s+ty)); in != red || beside != white {
		t.Errorf("the square is %v, beside it %v", in, beside)
	}
	for _, c := range []struct {
		subtype, key string
		want         []float64
	}{
		{"Link", "Rect", []float64{100 * s, 100*s + ty, 200 * s, 200*s + ty}},
		{"Highlight", "QuadPoints", []float64{100 * s, 430*s + ty, 200 * s, 430*s + ty, 100 * s, 390*s + ty, 200 * s, 390*s + ty}},
		{"Highlight", "Rect", []float64{100 * s, 390*s + ty, 200 * s, 430*s + ty}},
		{"Line", "L", []float64{100 * s, 300*s + ty, 300 * s, 350*s + ty}},
		{"Ink", "InkList", []float64{10 * s, 10*s + ty, 20 * s, 30*s + ty, 40 * s, 50*s + ty, 60 * s, 60*s + ty, 70 * s, 80*s + ty}},
		{"Ink", "RD", []float64{1 * s, 2 * s, 3 * s, 4 * s}},
		{"Widget", "Rect", []float64{300 * s, 100*s + ty, 500 * s, 130*s + ty}},
	} {
		if got := annotNums(t, out, 0, c.subtype, c.key); !numsNear(got, c.want...) {
			t.Errorf("%s %s is %v, want %v", c.subtype, c.key, got, c.want)
		}
	}
	if links := p.Links(); len(links) != 1 || od.Form().Field("name") == nil {
		t.Errorf("the page has %d links", len(links))
	}
	// The other pages are as they were.
	sameAt(t, "the second page", doc, 1, 1, od, 1, 1)
	sameAt(t, "the third page", doc, 2, 1, od, 2, 1)

	// The other ways to fit, on a square of half the width.
	for fit, want := range map[Fit][]float64{
		FitInside:  {306. / 792, 0, 0, 306. / 792, (306 - 612*306./792) / 2, 0},
		FitFill:    {0.5, 0, 0, 0.5, 0, (306 - 396) / 2.},
		FitStretch: {0.5, 0, 0, 306. / 792, 0, 0},
		FitNone:    {1, 0, 0, 1, (306 - 612) / 2., (306 - 792) / 2.},
	} {
		e := doc.Edit()
		if err := e.ResizePage(0, 306, 306, fit); err != nil {
			t.Fatal(err)
		}
		out := editSave(t, e)
		if m := cm(t, out, 0); !numsNear(m, want...) {
			t.Errorf("fit %d: the matrix is %v, want %v", fit, m, want)
		}
		p, _ := editOpen(t, out).Page(0)
		if w, h := p.Size(); w != 306 || h != 306 {
			t.Errorf("fit %d: the page is %g by %g", fit, w, h)
		}
	}

	// A page that is cropped and turned, and one in other units, at half
	// their size: drawn twice as large they are what they were. With a
	// drawing, which speaks of the page as it was, and moves with it.
	e = doc.Edit()
	draw := func(e *Editor) {
		if err := e.Draw(2, true, func(c *Canvas) { c.FillRect(Rect{20, 20, 120, 220}, blue) }); err != nil {
			t.Fatal(err)
		}
	}
	draw(e)
	if err := e.ResizePage(1, 300, 250, FitInside); err != nil {
		t.Fatal(err)
	}
	if err := e.ResizePage(2, 300, 400, FitStretch); err != nil {
		t.Fatal(err)
	}
	out = editSave(t, e)
	od = editOpen(t, out)
	plain := doc.Edit()
	draw(plain)
	pd := editOpen(t, editSave(t, plain))
	for i, want := range [][2]float64{1: {300, 250}, 2: {300, 400}} {
		if i == 0 {
			continue
		}
		p, _ := od.Page(i)
		if w, h := p.Size(); w != want[0] || h != want[1] {
			t.Errorf("page %d is %g by %g", i, w, h)
		}
		sameAt(t, fmt.Sprintf("page %d at half its size", i), pd, i, 1, od, i, 2)
	}
	if c := at(t, od, 2, 35, 60); c != blue {
		t.Errorf("the drawing on the resized page is %v", c)
	}
	p, _ = od.Page(1)
	if b := p.Boxes(); b.Media != (Rect{0, 0, 250, 300}) || p.Rotate != 90 {
		t.Errorf("the turned page has the medium %v and is turned by %d", b.Media, p.Rotate)
	}
	p, _ = od.Page(2)
	if b := p.Boxes(); b.Media != (Rect{0, 0, 150, 200}) {
		t.Errorf("the page in units of two points has the medium %v", b.Media)
	}

	// Twice: fitted twice. And turned afterwards, the new page turns.
	e = doc.Edit()
	for range 2 {
		if err := e.ResizePage(2, 300, 400, FitInside); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.RotatePage(2, 90); err != nil {
		t.Fatal(err)
	}
	out = editSave(t, e)
	if m := cm(t, out, 2); !numsNear(m, 0.5, 0, 0, 0.5, 0, 0) {
		t.Errorf("resized twice, the matrix is %v", m)
	}
	if p, _ := editOpen(t, out).Page(2); p.Rotate != 90 {
		t.Errorf("turned afterwards, the page is turned by %d", p.Rotate)
	} else if w, h := p.Size(); w != 400 || h != 300 {
		t.Errorf("turned afterwards, the page is %g by %g", w, h)
	}

	// After a crop, what is fitted is what the page then shows.
	e = doc.Edit()
	if err := e.SetPageBox(0, BoxCrop, Rect{100, 100, 200, 200}); err != nil {
		t.Fatal(err)
	}
	if err := e.ResizePage(0, 50, 50, FitInside); err != nil {
		t.Fatal(err)
	}
	od = editOpen(t, editSave(t, e))
	if a, b := at(t, od, 0, 2, 2), at(t, od, 0, 48, 48); a != red || b != red {
		t.Errorf("cropped to the square and resized, the corners are %v and %v", a, b)
	}
	// And nothing else: the square below what is shown does not come
	// into the margin of a taller page.
	e = doc.Edit()
	if err := e.SetPageBox(0, BoxCrop, Rect{100, 300, 500, 700}); err != nil {
		t.Fatal(err)
	}
	if err := e.ResizePage(0, 400, 800, FitInside); err != nil {
		t.Fatal(err)
	}
	od = editOpen(t, editSave(t, e))
	if below, text := at(t, od, 0, 50, 750), textOf(t, od, 0).String(); below != white || !strings.Contains(text, "Size") {
		t.Errorf("below what the page showed is %v; it reads %q", below, text)
	}

	// A tagged page keeps its content, so that the structure tree finds
	// the marked content it names; the others draw theirs as a form.
	tagged := editOpen(t, redactFile(""))
	e = tagged.Edit()
	for i := range 2 {
		if err := e.ResizePage(i, 150, 100, FitInside); err != nil {
			t.Fatal(err)
		}
	}
	out = editSave(t, e)
	td, err := pdf.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false} {
		dec, _ := td.PageContents(i + 1)
		page, _ := td.Page(i + 1)
		res, _ := td.Resolve(page.Get("Resources")).Dict()
		xo, _ := td.Resolve(res.Get("XObject")).Dict()
		if kept := bytes.Contains(dec.Data, []byte("/MCID 0")) && bytes.Contains(dec.Data, []byte(" re W n")); kept != want || xo.Has("CeraPage") == want {
			t.Errorf("page %d: its content is the page's: %v, want %v; the content starts %q", i, kept, want, dec.Data[:min(len(dec.Data), 60)])
		}
		if m := cm(t, out, i); !numsNear(m, 0.5, 0, 0, 0.5, 0, 0) {
			t.Errorf("page %d: the matrix is %v", i, m)
		}
	}
	od = editOpen(t, out)
	// Drawn twice as large the page is what it was, but for the text of
	// its two fields, which keeps its size in rectangles half as large:
	// they are 60 to 75 points up the page.
	was, _ := editRender(t, tagged, 0)
	p, _ = od.Page(0)
	is := image.NewRGBA(p.Bounds(4))
	if err := p.Render(t.Context(), is, RenderOptions{Scale: 4, Background: white, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	differ := 0
	for y := range was.Rect.Dy() {
		for x := range was.Rect.Dx() {
			if (y < 248 || y >= 282) && was.RGBAAt(x, y) != is.RGBAAt(x, y) {
				differ++
			}
		}
	}
	if is.Rect != was.Rect || differ > was.Rect.Dx() {
		t.Errorf("the tagged page: %d pixels differ outside its fields", differ)
	}
	sameAt(t, "the page after it", tagged, 1, 1, od, 1, 2)
	if a, b := textOf(t, tagged, 0).String(), textOf(t, od, 0).String(); a != b {
		t.Errorf("the tagged page reads %q, was %q", b, a)
	}
	if tree := od.Structure(); tree == nil || len(tree.Elems) == 0 {
		t.Error("the structure tree is gone")
	}

	// An imported page: its link moves as it is copied.
	ne := NewEditor()
	if err := ne.ImportPages(0, doc, EditPage{Index: 0}); err != nil {
		t.Fatal(err)
	}
	if err := ne.ResizePage(0, 306, 396, FitInside); err != nil {
		t.Fatal(err)
	}
	out = editSave(t, ne)
	if got := annotNums(t, out, 0, "Link", "Rect"); !numsNear(got, 50, 50, 100, 100) {
		t.Errorf("imported and resized, the link is at %v", got)
	}
	sameAt(t, "imported", doc, 0, 1, editOpen(t, out), 0, 2)

	// A page made here.
	ne = NewEditor()
	if err := ne.AddPage(0, 200, 100); err != nil {
		t.Fatal(err)
	}
	if err := ne.Draw(0, true, func(c *Canvas) { c.FillRect(Rect{0, 0, 100, 50}, red) }); err != nil {
		t.Fatal(err)
	}
	if err := ne.ResizePage(0, 400, 200, FitStretch); err != nil {
		t.Fatal(err)
	}
	od = editOpen(t, editSave(t, ne))
	p, _ = od.Page(0)
	if w, h := p.Size(); w != 400 || h != 200 {
		t.Errorf("the new page is %g by %g", w, h)
	}
	if in, out := at(t, od, 0, 150, 75), at(t, od, 0, 250, 150); in != red || out != white {
		t.Errorf("on the new page the drawing is %v, beside it %v", in, out)
	}

	// An update, which leaves the file in front as it is.
	e = doc.Edit()
	if err := e.ResizePage(0, 306, 396, FitInside); err != nil {
		t.Fatal(err)
	}
	var upd bytes.Buffer
	if err := e.Update(&upd); err != nil {
		t.Fatal(err)
	}
	ud := editOpen(t, upd.Bytes())
	if !bytes.HasPrefix(upd.Bytes(), src) || ud.r.Repaired() {
		t.Error("the update does not extend the file")
	}
	if got := annotNums(t, upd.Bytes(), 0, "Widget", "Rect"); !numsNear(got, 150, 50, 250, 65) {
		t.Errorf("updated, the field is at %v", got)
	}
	if ud.Form().Field("name").Saved.Text() != "v" {
		t.Error("updated, the field lost its value")
	}

	// What is not a size.
	for name, err := range map[string]error{
		"no width":     e.ResizePage(0, 0, 100, FitInside),
		"too large":    e.ResizePage(0, 20000, 100, FitInside),
		"no such fit":  e.ResizePage(0, 100, 100, Fit(9)),
		"no such page": e.ResizePage(9, 100, 100, FitInside),
	} {
		if err == nil {
			t.Errorf("%s: resized", name)
		}
	}
}

// TestAddSheets puts several pages on a sheet.
func TestAddSheets(t *testing.T) {
	objs := map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R>>",
		2: "<</Type /Pages /Kids [3 0 R 4 0 R 5 0 R 6 0 R] /Count 4 /MediaBox [0 0 200 100]>>",
	}
	for i, c := range []string{"1 0 0", "0 1 0", "0 0 1", "0 0 0"} {
		objs[3+i] = fmt.Sprintf("<</Type /Page /Parent 2 0 R /Contents %d 0 R>>", 10+i)
		objs[10+i] = testpdf.Stream("", []byte(c+" rg 0 0 200 100 re f"))
	}
	src := editOpen(t, testpdf.File{Objs: objs, Trailer: "/Root 1 0 R"}.Bytes())
	// Places of 190 by 90 points: a page is 180 by 90 in the middle.
	sheet := Sheet{Width: 420, Height: 220, Columns: 2, Rows: 2, Margin: 10, Gap: 20}
	places := [4][2]float64{{105, 55}, {315, 55}, {105, 165}, {315, 165}}
	look := func(d *Document, i int) string {
		var out []string
		for _, p := range places {
			switch at(t, d, i, p[0], p[1]) {
			case red:
				out = append(out, "red")
			case green:
				out = append(out, "green")
			case blue:
				out = append(out, "blue")
			case black:
				out = append(out, "black")
			case white:
				out = append(out, "-")
			default:
				out = append(out, "?")
			}
		}
		return strings.Join(out, " ")
	}

	e := NewEditor()
	if err := e.AddSheets(0, src, sheet); err != nil {
		t.Fatal(err)
	}
	od := editOpen(t, editSave(t, e))
	p, _ := od.Page(0)
	if w, h := p.Size(); od.NumPages() != 1 || w != 420 || h != 220 {
		t.Fatalf("%d sheets of %g by %g", od.NumPages(), w, h)
	}
	if got := look(od, 0); got != "red green blue black" {
		t.Errorf("the sheet has %s", got)
	}
	// The margin, the gap, and the strip a page leaves in its place.
	for _, pt := range [][2]float64{{5, 5}, {210, 55}, {105, 110}, {12, 55}, {198, 55}} {
		if c := at(t, od, 0, pt[0], pt[1]); c != white {
			t.Errorf("at %v the sheet is %v", pt, c)
		}
	}
	if c := at(t, od, 0, 17, 12); c != red {
		t.Errorf("the corner of the first page is %v", c)
	}

	// An order of one's own, with a place left empty, on two sheets,
	// after the pages of a document; and something drawn on a sheet.
	e = src.Edit()
	if err := e.AddSheets(4, src, sheet, 3, -1, 0, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := e.Draw(5, true, func(c *Canvas) { c.FillRect(Rect{300, 150, 330, 180}, magenta) }); err != nil {
		t.Fatal(err)
	}
	od = editOpen(t, editSave(t, e))
	if od.NumPages() != 6 {
		t.Fatalf("%d pages", od.NumPages())
	}
	if a, b := look(od, 4), look(od, 5); a != "black - red green" || b != "blue - - ?" {
		t.Errorf("the sheets have %s and %s", a, b)
	}

	// What is not a sheet: nothing is added.
	for name, err := range map[string]error{
		"no columns":   e.AddSheets(0, src, Sheet{Width: 400, Height: 200, Rows: 1}),
		"no room":      e.AddSheets(0, src, Sheet{Width: 400, Height: 200, Columns: 2, Rows: 1, Margin: 200}),
		"no sheet":     e.AddSheets(0, src, Sheet{Columns: 1, Rows: 1}),
		"no such page": e.AddSheets(0, src, sheet, 0, 4),
		"no place":     e.AddSheets(9, src, sheet),
		"no document":  e.AddSheets(0, nil, sheet),
	} {
		if err == nil {
			t.Errorf("%s: added", name)
		}
	}
	if e.NumPages() != 6 {
		t.Errorf("after what failed the editor has %d pages", e.NumPages())
	}
}
