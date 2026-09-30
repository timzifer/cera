package cera

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"
	"time"
)

// buildPDF writes a minimal PDF with one page per content stream. pageExtra
// is added to every page dictionary; objs are extra objects numbered from
// 100 on ("100 0 R" is the first).
func buildPDF(contents []string, pageExtra string, objs ...string) []byte {
	var b bytes.Buffer
	offsets := map[int]int{}
	obj := func(n int, body string) {
		offsets[n] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	var kids []string
	for i := range contents {
		kids = append(kids, fmt.Sprintf("%d 0 R", 10+2*i))
	}
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d /MediaBox [0 0 200 100] >>", strings.Join(kids, " "), len(contents)))
	for i, c := range contents {
		obj(10+2*i, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /Contents %d 0 R %s >>", 11+2*i, pageExtra))
		obj(11+2*i, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(c), c))
	}
	for i, o := range objs {
		obj(100+i, o)
	}
	last := 0
	for n := range offsets {
		last = max(last, n)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", last+1)
	for n := 1; n <= last; n++ {
		if off, ok := offsets[n]; ok {
			fmt.Fprintf(&b, "%010d 00000 n \n", off)
		} else {
			b.WriteString("0000000000 65535 f \n")
		}
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", last+1, xref)
	return b.Bytes()
}

var white = color.RGBA{255, 255, 255, 255}

func renderPage(t testing.TB, data []byte, i int, opt RenderOptions) (*image.RGBA, Stats, error) {
	t.Helper()
	doc, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	p, err := doc.Page(i)
	if err != nil {
		t.Fatal(err)
	}
	var st Stats
	opt.Stats = &st
	img := image.NewRGBA(p.Bounds(opt.Scale))
	err = p.Render(context.Background(), img, opt)
	return img, st, err
}

func assertPixel(t *testing.T, img *image.RGBA, x, y int, want color.RGBA) {
	t.Helper()
	if got := img.RGBAAt(x, y); got != want {
		t.Errorf("pixel (%d, %d) = %v, want %v", x, y, got, want)
	}
}

func TestFillRect(t *testing.T) {
	// Red square at x 10..30, y 10..30 in PDF space; the page is 100 pt
	// high, so it covers device rows 70..90.
	img, st, err := renderPage(t, buildPDF([]string{"1 0 0 rg 10 10 20 20 re f"}, ""), 0, RenderOptions{Background: white})
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds() != image.Rect(0, 0, 200, 100) {
		t.Fatalf("bounds %v", img.Bounds())
	}
	assertPixel(t, img, 20, 80, color.RGBA{255, 0, 0, 255})
	assertPixel(t, img, 20, 20, white)
	assertPixel(t, img, 5, 80, white)
	if st.Fills != 1 || st.Errors != 0 {
		t.Errorf("stats %+v", st)
	}
}

func TestScaleAndRotate(t *testing.T) {
	data := buildPDF([]string{"0 0 1 rg 0 0 20 20 re f"}, "/Rotate 90")
	img, _, err := renderPage(t, data, 0, RenderOptions{Scale: 2, Background: white})
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds() != image.Rect(0, 0, 200, 400) {
		t.Fatalf("bounds %v", img.Bounds())
	}
	// Rotated 90° clockwise, the bottom-left corner of the page is the
	// top-left corner of the image.
	assertPixel(t, img, 10, 10, color.RGBA{0, 0, 255, 255})
	assertPixel(t, img, 190, 390, white)
}

func TestClipIsRestoredByQ(t *testing.T) {
	c := "q 0 0 50 100 re W n 0 g 0 0 200 100 re f Q 1 0 0 rg 150 0 50 100 re f"
	img, st, err := renderPage(t, buildPDF([]string{c}, ""), 0, RenderOptions{Background: white})
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, img, 25, 50, color.RGBA{0, 0, 0, 255})
	assertPixel(t, img, 100, 50, white)
	assertPixel(t, img, 175, 50, color.RGBA{255, 0, 0, 255})
	if st.Clips != 1 {
		t.Errorf("clips %d", st.Clips)
	}
}

func TestFormXObject(t *testing.T) {
	form := "<< /Type /XObject /Subtype /Form /BBox [0 0 10 10] /Matrix [2 0 0 2 100 0] /Length 22 >>\nstream\n0 1 0 rg 0 0 50 50 re f\nendstream"
	data := buildPDF([]string{"/F1 Do"}, "/Resources << /XObject << /F1 100 0 R >> >>", form)
	img, st, err := renderPage(t, data, 0, RenderOptions{Background: white})
	if err != nil {
		t.Fatal(err)
	}
	// BBox 10×10 scaled by 2 at x=100: device x 100..120, y 80..100.
	assertPixel(t, img, 110, 90, color.RGBA{0, 255, 0, 255})
	assertPixel(t, img, 130, 90, white) // clipped by BBox
	if st.Fills != 1 || st.Errors != 0 {
		t.Errorf("stats %+v", st)
	}
}

func TestStrokeAndAlpha(t *testing.T) {
	c := "/G gs 0 0 1 RG 10 w 0 50 m 200 50 l S"
	data := buildPDF([]string{c}, "/Resources << /ExtGState << /G << /CA 0.5 >> >> >>")
	img, st, err := renderPage(t, data, 0, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := img.RGBAAt(100, 50); got.A < 120 || got.A > 135 || got.B != got.A {
		t.Errorf("half-transparent blue stroke: %v", got)
	}
	if st.Strokes != 1 {
		t.Errorf("strokes %d", st.Strokes)
	}
}

func TestRegionMatchesFullPage(t *testing.T) {
	data := buildPDF([]string{"0.2 0.4 0.6 rg 5 5 m 190 20 l 100 95 l f 0 G 0 w 0 0 m 200 100 l S"}, "")
	full, _, err := renderPage(t, data, 0, RenderOptions{Scale: 1.5, Background: white})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := Open(data)
	p, _ := doc.Page(0)
	tile := image.Rect(100, 40, 228, 104)
	dst := image.NewRGBA(tile)
	if err := p.Render(context.Background(), dst, RenderOptions{Scale: 1.5, Background: white}); err != nil {
		t.Fatal(err)
	}
	for y := tile.Min.Y; y < tile.Max.Y; y++ {
		for x := tile.Min.X; x < tile.Max.X; x++ {
			// The rasterizer's bands start at the region's top row, so
			// coverage may round differently by one level.
			if a, b := full.RGBAAt(x, y), dst.RGBAAt(x, y); diff(a, b) > 2 {
				t.Fatalf("pixel (%d, %d): full %v, tile %v", x, y, a, b)
			}
		}
	}
}

func TestBrokenContentDoesNotStop(t *testing.T) {
	c := "q q Q Q Q 1 0 0 rg xyz 12 re 10 10 foo f ) ] >> 10 10 20 20 re f cm cm"
	img, st, err := renderPage(t, buildPDF([]string{c}, ""), 0, RenderOptions{Background: white})
	if err != nil {
		t.Fatal(err)
	}
	if st.Errors == 0 {
		t.Error("no errors counted")
	}
	assertPixel(t, img, 20, 80, color.RGBA{255, 0, 0, 255})
}

func TestUnsupportedIsCounted(t *testing.T) {
	c := "BT /F1 12 Tf (Hi) Tj ET /Sh sh"
	// Symbol is not embedded, and no stand-in has its glyphs.
	_, st, err := renderPage(t, textPDF(c, "<< /Type /Font /Subtype /Type1 /BaseFont /Symbol >>"), 0, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Unsupported["font-missing"] != 1 || st.Unsupported["shading"] != 1 {
		t.Errorf("unsupported %v", st.Unsupported)
	}
}

func TestDeadline(t *testing.T) {
	c := strings.Repeat("0 0 1 1 re f\n", 2000)
	_, _, err := renderPage(t, buildPDF([]string{c}, ""), 0, RenderOptions{Deadline: time.Now().Add(-time.Second)})
	if !errors.Is(err, ErrDeadline) {
		t.Fatalf("err = %v, want ErrDeadline", err)
	}
}

func diff(a, b color.RGBA) int {
	d := 0
	for _, v := range [][2]uint8{{a.R, b.R}, {a.G, b.G}, {a.B, b.B}, {a.A, b.A}} {
		d = max(d, int(v[0])-int(v[1]), int(v[1])-int(v[0]))
	}
	return d
}

// hatchPage is 2000 hairlines across a 200×100 pt page.
func hatchPage(b *testing.B) *Page {
	var c strings.Builder
	c.WriteString("0 G 0.3 w\n")
	for i := range 2000 {
		fmt.Fprintf(&c, "%d 0 m %d 100 l S\n", i%400-100, i%400)
	}
	doc, err := Open(buildPDF([]string{c.String()}, ""))
	if err != nil {
		b.Fatal(err)
	}
	p, _ := doc.Page(0)
	return p
}

// BenchmarkRenderHatch measures a first render (interpret and draw) and a
// repeated one (draw the cached display list), on one core and on all.
func BenchmarkRenderHatch(b *testing.B) {
	for _, bc := range []struct {
		name    string
		release bool
		workers int
	}{
		{"first/serial", true, 1},
		{"first/parallel", true, 0},
		{"again/serial", false, 1},
		{"again/parallel", false, 0},
	} {
		b.Run(bc.name, func(b *testing.B) {
			p := hatchPage(b)
			dst := image.NewRGBA(p.Bounds(150.0 / 72))
			opt := RenderOptions{Scale: 150.0 / 72, Background: white, Workers: bc.workers}
			b.ReportAllocs()
			for b.Loop() {
				if bc.release {
					p.Release()
				}
				if err := p.Render(context.Background(), dst, opt); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func FuzzContent(f *testing.F) {
	for _, s := range []string{
		"1 0 0 rg 10 10 20 20 re f",
		"q 2 0 0 2 0 0 cm 0 0 m 100 100 l S Q",
		"[3 1] 0 d 5 w 1 J 1 j 0 0 m 50 80 l 90 0 l h S",
		"0 0 100 100 re W* n 1e30 1e30 m -1e30 0 l f",
		"q q q Q Q Q Q W n",
		"BT /F1 12 Tf 10 50 Td (Hello) Tj [(W) -120 (orld)] TJ T* 2 Tc (x) ' 1 2 (y) \" ET",
		"BT 7 Tr /F1 40 Tf 3 Tz 5 Ts 0 1 -1 0 50 0 Tm <0041> Tj ET 0 0 200 100 re f",
		"BT 1 Tr 2 w /F1 1e9 Tf (A) Tj 5 Tr 0 0 0 0 0 0 Tm (B) Tj ET",
		"q 100 0 0 50 0 0 cm BI /W 2 /H 1 /CS /RGB /BPC 8 ID \xff\x00\x00\x00\x00\xff EI Q",
		"q 0 20 -30 0 50 0 cm BI /W 9 /H 3 /IM true /D [1 0] /F /AHx ID 00ff00ff00ff> EI Q",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, c string) {
		doc, err := Open(textPDF(c, helvetica))
		if err != nil {
			return
		}
		p, err := doc.Page(0)
		if err != nil {
			return
		}
		dst := image.NewRGBA(p.Bounds(0.5))
		// Serial, then from the cached display list with bands in parallel.
		for _, workers := range []int{1, 3} {
			err = p.Render(context.Background(), dst, RenderOptions{Scale: 0.5, Workers: workers, Deadline: time.Now().Add(2 * time.Second)})
			var pe *PanicError
			if errors.As(err, &pe) {
				t.Fatalf("%v\n%s", pe.Value, pe.Stack)
			}
		}
	})
}
