package cera

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/stilus"
)

// formObj returns a form XObject with a 200×100 BBox, the given extra
// dictionary entries and content.
func formObj(extra, content string) string {
	return fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 200 100] %s /Length %d >>\nstream\n%s\nendstream",
		extra, len(content), content)
}

func newCanvas() *stilus.Canvas { return stilus.NewCanvas(noImage) }

func refTo(n int) pdf.Ref { return pdf.Ref{Num: int32(n)} }

func renderTransparent(t *testing.T, content, resources string, objs ...string) (*image.RGBA, Stats) {
	t.Helper()
	img, st, err := renderPage(t, buildPDF([]string{content}, "/Resources << "+resources+" >>", objs...), 0,
		RenderOptions{Background: white, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"blend-mode", "soft-mask", "transparency-group", "non-isolated-blend", "smask-transfer"} {
		if st.Unsupported[k] != 0 {
			t.Errorf("unsupported %s: %d", k, st.Unsupported[k])
		}
	}
	return img, st
}

func TestBlendModes(t *testing.T) {
	red, blue := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}
	for _, c := range []struct {
		bm      string
		overlap color.RGBA
	}{
		{"/Normal", blue},
		{"/Multiply", color.RGBA{0, 0, 0, 255}},
		{"/Screen", color.RGBA{255, 0, 255, 255}},
		{"/Lighten", color.RGBA{255, 0, 255, 255}},
		{"/Darken", color.RGBA{0, 0, 0, 255}},
		{"/Difference", color.RGBA{255, 0, 255, 255}},
		{"[/Bogus /Exclusion]", color.RGBA{255, 0, 255, 255}},
		{"/Luminosity", color.RGBA{94, 0, 0, 255}}, // red at the luminosity of blue
	} {
		t.Run(c.bm, func(t *testing.T) {
			content := "1 0 0 rg 0 0 100 100 re f /B gs 0 0 1 rg 50 0 100 100 re f"
			img, st := renderTransparent(t, content, "/ExtGState << /B 100 0 R >>", "<< /BM "+c.bm+" >>")
			assertPixel(t, img, 25, 50, red)
			assertNear(t, img, 75, 50, c.overlap, 2)
			// Over white, every mode above but Darken, Multiply and
			// Luminosity gives the source.
			switch c.bm {
			case "/Normal", "/Multiply", "/Darken":
				assertPixel(t, img, 125, 50, blue)
			}
			if c.bm != "/Normal" && st.Groups != 1 {
				t.Errorf("groups %d", st.Groups)
			}
		})
	}
}

func TestGroupOpacity(t *testing.T) {
	// Two overlapping opaque rectangles in a group at 50%: the overlap is
	// as light as the rest. Without the group, each is drawn at 50%.
	content := "0 0 1 rg 0 0 100 100 re f 50 0 100 100 re f"
	half := color.RGBA{127, 127, 255, 255}
	for _, c := range []struct {
		group   string
		overlap color.RGBA
	}{
		{"/Group << /S /Transparency >>", half},
		{"/Group << /S /Transparency /I true >>", half},
		{"", color.RGBA{64, 64, 255, 255}},
	} {
		img, _ := renderTransparent(t, "/A gs /F Do", "/ExtGState << /A << /ca 0.5 >> >> /XObject << /F 100 0 R >>",
			formObj(c.group, content))
		assertNear(t, img, 25, 50, half, 1)
		assertNear(t, img, 75, 50, c.overlap, 1)
		assertPixel(t, img, 175, 50, white)
	}
}

func TestKnockoutGroup(t *testing.T) {
	// Half-transparent red, then blue over it: in a knockout group blue
	// replaces red where they overlap.
	content := "/A gs 1 0 0 rg 0 0 100 100 re f 0 0 1 rg 50 0 100 100 re f"
	for _, k := range []bool{false, true} {
		group := fmt.Sprintf("/Group << /S /Transparency /K %v >> /Resources << /ExtGState << /A << /ca 0.5 >> >> >>", k)
		img, _ := renderTransparent(t, "/F Do", "/XObject << /F 100 0 R >>", formObj(group, content))
		assertNear(t, img, 25, 50, color.RGBA{255, 127, 127, 255}, 1)
		assertNear(t, img, 125, 50, color.RGBA{127, 127, 255, 255}, 1)
		want := color.RGBA{127, 64, 191, 255} // blue over red over white
		if k {
			want = color.RGBA{127, 127, 255, 255}
		}
		assertNear(t, img, 75, 50, want, 1)
	}
}

func TestKnockoutNestedAndBlend(t *testing.T) {
	// A group in a knockout group is one object: its shape (not its
	// opacity) knocks out what is below (pdf.js knockout_nested).
	inner := formObj("/Group << /S /Transparency /I true /K true >> /Resources << /ExtGState << /H << /ca 0.5 >> >> >>",
		"/H gs 0 0 1 rg 70 30 100 40 re f")
	outer := formObj("/Group << /S /Transparency /I true /K true >> /Resources << /XObject << /C 101 0 R >> >>",
		"1 0 0 rg 20 30 100 40 re f /C Do")
	img, _ := renderTransparent(t, "/O Do", "/XObject << /O 100 0 R >>", outer, inner)
	assertPixel(t, img, 40, 50, color.RGBA{255, 0, 0, 255})
	assertNear(t, img, 100, 50, color.RGBA{127, 127, 255, 255}, 1)
	// A blend mode in a non-isolated knockout group sees the backdrop
	// (pdf.js knockout_blend_multiply).
	ko := formObj("/Group << /S /Transparency /K true >> /Resources << /ExtGState << /M << /BM /Multiply >> >> >>",
		"/M gs 0 1 1 rg 40 30 120 40 re f")
	img, _ = renderTransparent(t, "1 1 0 rg 0 0 200 100 re f /G Do", "/XObject << /G 100 0 R >>", ko)
	assertPixel(t, img, 100, 50, color.RGBA{0, 255, 0, 255})
	assertPixel(t, img, 20, 50, color.RGBA{255, 255, 0, 255})
}

// A group drawn at opacity 0 draws nothing, also as an object of a
// knockout group: it knocks out nothing below it, as Acrobat, PDFium and
// Ghostscript draw it (pdf.js issue18032, where InDesign puts such a
// group over a gradient).
func TestKnockoutOpacityZero(t *testing.T) {
	zero := formObj("/Group << /S /Transparency >>", "0 0 1 rg 50 30 100 40 re f")
	ko := formObj("/Group << /S /Transparency /K true >> /Resources << /ExtGState << /Z << /ca 0 /CA 0 >> >> /XObject << /Z 101 0 R >> >>",
		"1 0 0 rg 0 0 100 100 re f /Z gs /Z Do")
	img, _ := renderTransparent(t, "/K Do", "/XObject << /K 100 0 R >>", ko, zero)
	assertPixel(t, img, 75, 50, color.RGBA{255, 0, 0, 255})
	assertPixel(t, img, 125, 50, white)
}

// AIS true is counted only where cera draws shape as opacity and the two
// differ: inside a knockout group. Elsewhere shape and opacity multiply
// (PDF 2.0 11.3.7), and the page is drawn as the spec says.
func TestAlphaIsShapeCounted(t *testing.T) {
	gs := "/ExtGState << /S << /AIS true /ca 0.5 >> >>"
	_, st := renderTransparent(t, "/S gs 1 0 0 rg 0 0 100 100 re f", gs)
	if n := st.Unsupported["alpha-is-shape"]; n != 0 {
		t.Errorf("AIS outside a knockout group counted %d times", n)
	}
	ko := formObj("/Group << /S /Transparency /K true >> /Resources << "+gs+" >>", "/S gs 1 0 0 rg 0 0 100 100 re f")
	_, st = renderTransparent(t, "/K Do", "/XObject << /K 100 0 R >>", ko)
	if st.Unsupported["alpha-is-shape"] == 0 {
		t.Error("AIS inside a knockout group not counted")
	}
}

func TestNonIsolatedGroup(t *testing.T) {
	// Multiply inside a non-isolated group at 50%: the group's result is
	// the red page multiplied by blue (black), half way over the red.
	content := "/M gs 0 0 1 rg 50 0 100 100 re f 0 1 0 rg 160 0 30 100 re f"
	res := "/Resources << /ExtGState << /M << /BM /Multiply >> >> >>"
	for _, clip := range []string{"", "0 0 m 200 0 l 200 100 l 0 100 l h W n "} {
		img, _ := renderTransparent(t, "1 0 0 rg 0 0 100 100 re f /A gs "+clip+"/F Do",
			"/ExtGState << /A << /ca 0.5 >> >> /XObject << /F 100 0 R >>",
			formObj("/Group << /S /Transparency >> "+res, content))
		assertNear(t, img, 75, 50, color.RGBA{128, 0, 0, 255}, 1)
		assertNear(t, img, 125, 50, color.RGBA{127, 127, 255, 255}, 1)
		assertNear(t, img, 175, 50, color.RGBA{127, 255, 127, 255}, 1)
		assertPixel(t, img, 25, 50, color.RGBA{255, 0, 0, 255})
	}
}

// A non-isolated group with Multiply inside, composited with Screen:
// blue multiplied onto yellow is black, but the group's backdrop is
// removed before Screen blends it (PDF 2.0, 11.4.8), so Screen sees
// black over yellow on the left; on the right the page's backdrop is
// transparent (the paper comes after it), so the blue stays.
func TestNonIsolatedGroupBlended(t *testing.T) {
	content := "/M gs 0 0 1 rg 50 0 100 100 re f"
	res := "/Resources << /ExtGState << /M << /BM /Multiply >> >> >>"
	for _, workers := range []int{1, 3} {
		img, st, err := renderPage(t, buildPDF([]string{"1 1 0 rg 0 0 100 100 re f /S gs /F Do"},
			"/Resources << /ExtGState << /S << /BM /Screen >> >> /XObject << /F 100 0 R >> >>",
			formObj("/Group << /S /Transparency >> "+res, content)), 0,
			RenderOptions{Background: white, Workers: workers, Scale: 2})
		if err != nil || len(st.Unsupported) != 0 {
			t.Fatal(err, st.Unsupported)
		}
		assertNear(t, img, 150, 100, color.RGBA{255, 255, 0, 255}, 1)
		assertNear(t, img, 250, 100, color.RGBA{0, 0, 255, 255}, 1)
		assertNear(t, img, 50, 100, color.RGBA{255, 255, 0, 255}, 1)
	}
}

func TestIsolatedGroupBlends(t *testing.T) {
	// Multiply inside a group: an isolated group blends with its own
	// transparent backdrop (so the blue shows unchanged), a non-isolated
	// one with the red page below.
	content := "/M gs 0 0 1 rg 50 0 100 100 re f"
	res := "/Resources << /ExtGState << /M << /BM /Multiply >> >> >>"
	for _, iso := range []bool{false, true} {
		img, _ := renderTransparent(t, "1 0 0 rg 0 0 100 100 re f /F Do", "/XObject << /F 100 0 R >>",
			formObj(fmt.Sprintf("/Group << /S /Transparency /I %v >> %s", iso, res), content))
		want := color.RGBA{0, 0, 0, 255}
		if iso {
			want = color.RGBA{0, 0, 255, 255}
		}
		assertPixel(t, img, 75, 50, want)
		assertPixel(t, img, 125, 50, color.RGBA{0, 0, 255, 255})
	}
}

func TestSoftMask(t *testing.T) {
	// The mask form is white on the left half.
	maskForm := formObj("/Group << /S /Transparency /CS /DeviceGray >>", "1 g 0 0 100 100 re f")
	red := color.RGBA{255, 0, 0, 255}
	for _, c := range []struct {
		name        string
		smask       string
		left, right color.RGBA
	}{
		{"luminosity", "/S /Luminosity /G 101 0 R", red, white},
		{"backdrop", "/S /Luminosity /G 101 0 R /BC [1]", red, red},
		{"alpha", "/S /Alpha /G 101 0 R", red, white},
		{"transfer", "/S /Alpha /G 101 0 R /TR << /FunctionType 2 /Domain [0 1] /C0 [1] /C1 [0] /N 1 >>", white, red},
		{"calculator", "/S /Alpha /G 101 0 R /TR 102 0 R", white, red},
		{"half", "/S /Alpha /G 101 0 R /TR << /FunctionType 2 /Domain [0 1] /C0 [0] /C1 [0.5] >>",
			color.RGBA{255, 127, 127, 255}, white},
	} {
		t.Run(c.name, func(t *testing.T) {
			calc := "<< /FunctionType 4 /Domain [0 1] /Range [0 1] /Length 12 >>\nstream\n{ 1 exch sub }\nendstream"
			for _, content := range []string{
				"/S gs 1 0 0 rg 0 0 200 100 re f", // a fill
				"/S gs /F Do",                     // a group
				// Text, stroked wide enough to cover the page.
				"/S gs 1 0 0 rg 1 0 0 RG 100 w BT 2 Tr /F1 1 Tf 400 0 0 200 -10 0 Tm (M) Tj ET",
			} {
				res := "/ExtGState << /S << /SMask 100 0 R >> >> /XObject << /F 103 0 R >> /Font << /F1 104 0 R >>"
				img, st := renderTransparent(t, content, res, "<< "+c.smask+" >>", maskForm, calc,
					formObj("/Group << /S /Transparency >>", "1 0 0 rg 0 0 200 100 re f"), helvetica)
				assertNear(t, img, 50, 50, c.left, 1)
				assertNear(t, img, 150, 50, c.right, 1)
				if st.Groups != 2 {
					t.Errorf("%s: groups %d", content, st.Groups)
				}
			}
		})
	}
}

func TestSoftMaskNone(t *testing.T) {
	c := "/S gs /N gs 1 0 0 rg 0 0 200 100 re f"
	img, st := renderTransparent(t, c, "/ExtGState << /S << /SMask 100 0 R >> /N << /SMask /None >> >>",
		"<< /S /Luminosity /G 101 0 R >>", formObj("", "1 g 0 0 100 100 re f"))
	assertPixel(t, img, 150, 50, color.RGBA{255, 0, 0, 255})
	if st.Groups != 0 {
		t.Errorf("groups %d", st.Groups)
	}
}

func TestImageSoftMaskOverridesState(t *testing.T) {
	// An image's /SMask replaces the soft mask of the graphics state, which
	// here hides everything; the fill alpha still applies. Illustrator's
	// drop shadows are drawn so (borb 0279.pdf).
	res := "/ExtGState << /S << /SMask << /S /Luminosity /G 100 0 R >> >> /H << /ca 0.5 >> >>" +
		" /XObject << /M 101 0 R /P 103 0 R >>"
	objs := []string{
		formObj("", "0 g 0 0 200 100 re f"),
		streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8 /SMask 102 0 R", []byte{255, 0, 0}),
		streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8", []byte{255}),
		streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8", []byte{255, 0, 0}),
	}
	img, _ := renderTransparent(t, "/S gs q 100 0 0 100 0 0 cm /M Do Q q 100 0 0 100 100 0 cm /P Do Q", res, objs...)
	assertNear(t, img, 50, 50, color.RGBA{255, 0, 0, 255}, 1)
	assertNear(t, img, 150, 50, white, 1) // no /SMask: the state's mask applies
	img, _ = renderTransparent(t, "/S gs /H gs 200 0 0 100 0 0 cm /M Do", res, objs...)
	assertNear(t, img, 50, 50, color.RGBA{255, 127, 127, 255}, 2)
}

func TestSoftMaskKeepsPathAndText(t *testing.T) {
	// The mask is drawn when the object is painted, between the path (or
	// text) and its painting, and its own path and text must not leak.
	mask := formObj("", "0 0 10 10 re BT /F1 12 Tf (x) Tj ET 1 g 0 0 200 100 re f")
	c := "0 0 1 rg 20 20 m 180 20 l 180 80 l 20 80 l h /S gs f"
	res := "/ExtGState << /S << /SMask << /S /Luminosity /G 100 0 R >> >> >> /Font << /F1 101 0 R >>"
	img, _ := renderTransparent(t, c, res, mask, helvetica)
	assertPixel(t, img, 100, 50, color.RGBA{0, 0, 255, 255})
	assertPixel(t, img, 10, 50, white)
	doc, _ := Open(buildPDF([]string{c + " BT /F1 12 Tf 10 10 Td (ab) Tj ET"}, "/Resources << "+res+" >>", mask, helvetica))
	p, _ := doc.Page(0)
	text, err := p.Text(context.Background())
	if err != nil || text.String() != "ab" {
		t.Errorf("text %q, %v", text.String(), err)
	}
}

func TestTrivialGroupsAreFlattened(t *testing.T) {
	// Forms with a group that composites like its content need no layer.
	var c strings.Builder
	for range 50 {
		c.WriteString("/F Do ")
	}
	data := buildPDF([]string{c.String()}, "/Resources << /XObject << /F 100 0 R >> >>",
		formObj("/Group << /S /Transparency /I true >>", "0 0 1 rg 10 10 20 20 re f"))
	doc, _ := Open(data)
	p, _ := doc.Page(0)
	if err := p.Render(context.Background(), image.NewRGBA(p.Bounds(1)), RenderOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, it := range p.dl.items {
		if it.op == dlBeginGroup || it.op == dlEndGroup {
			t.Fatalf("group kept: %+v", it)
		}
	}
}

func TestSingleObjectGroupsAreFolded(t *testing.T) {
	// A group of one object at 50% draws the object at 50%.
	data := buildPDF([]string{"/A gs /F Do"}, "/Resources << /ExtGState << /A << /ca 0.5 >> >> /XObject << /F 100 0 R >> >>",
		formObj("/Group << /S /Transparency >>", "q 0 0 50 50 re W n 0 0 1 rg 10 10 80 80 re f Q"))
	doc, _ := Open(data)
	p, _ := doc.Page(0)
	img := image.NewRGBA(p.Bounds(1))
	if err := p.Render(context.Background(), img, RenderOptions{Background: white}); err != nil {
		t.Fatal(err)
	}
	for _, it := range p.dl.items {
		if it.op == dlBeginGroup || it.op == dlEndGroup {
			t.Fatalf("group kept: %+v", it)
		}
	}
	assertNear(t, img, 30, 70, color.RGBA{127, 127, 255, 255}, 1)
	assertPixel(t, img, 70, 70, white)
}

// transparencyPDF draws a page with every kind of group and mask.
func transparencyPDF() []byte {
	var c strings.Builder
	c.WriteString("0.9 0.6 0.1 rg 0 0 200 100 re f\n")
	c.WriteString("q /M gs 0.2 0.3 0.9 rg 20 10 m 180 30 l 100 95 l f Q\n")
	c.WriteString("q 30 0 m 170 0 l 100 100 l h W n /A gs /K Do Q\n")
	c.WriteString("q /S gs 0 0.8 0.2 rg 10 w 0 50 m 200 60 l S Q\n")
	c.WriteString("q /D gs 0 1 1 rg 70 20 60 60 re f Q\n")
	res := "/ExtGState << /M << /BM /Multiply >> /A << /ca 0.6 >> /D << /BM /Difference >>" +
		" /S << /SMask << /S /Luminosity /G 101 0 R >> >> >> /XObject << /K 100 0 R >>"
	knockout := formObj("/Group << /S /Transparency /K true /I true >> /Resources << /ExtGState << /H << /ca 0.5 >> >> >>",
		"/H gs 1 0 0 rg 10 10 120 60 re f 0 0 1 rg 60 30 120 60 re f")
	mask := formObj("/Group << /S /Transparency >>", "1 g 0 0 m 200 0 l 200 100 l h f")
	return buildPDF([]string{c.String()}, "/Resources << "+res+" >>", knockout, mask)
}

func TestTransparencyWorkersAndTilesAgree(t *testing.T) {
	doc, _ := Open(transparencyPDF())
	p, _ := doc.Page(0)
	const scale = 5
	render := func(workers int, r image.Rectangle) *image.RGBA {
		dst := image.NewRGBA(r)
		if err := p.Render(context.Background(), dst, RenderOptions{Scale: scale, Background: white, Workers: workers}); err != nil {
			t.Fatal(err)
		}
		return dst
	}
	full := p.Bounds(scale)
	one, four := render(1, full), render(4, full)
	if d := maxDiff(t, one, four, full); d > 2 {
		t.Errorf("1 and 4 workers differ by %d", d)
	}
	tile := image.Rect(170, 90, 700, 400)
	if d := maxDiff(t, one, render(3, tile), tile); d > 2 {
		t.Errorf("tile differs by %d", d)
	}
	// And Page.Run on a raster device, without a display list.
	direct := image.NewRGBA(full)
	fillRegion(direct, full, white)
	dev := &RasterDevice{C: newCanvas()}
	dev.Reset(direct, full)
	if err := p.Run(context.Background(), dev, scale, nil); err != nil || dev.Err() != nil {
		t.Fatal(err, dev.Err())
	}
	if d := maxDiff(t, one, direct, full); d > 2 {
		t.Errorf("Page.Run differs by %d", d)
	}
}

func TestTransparencySteadyStateAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("sync.Pool drops items under the race detector")
	}
	doc, _ := Open(transparencyPDF())
	p, _ := doc.Page(0)
	dst := image.NewRGBA(p.Bounds(2))
	opt := RenderOptions{Scale: 2, Workers: 1}
	if err := p.Render(context.Background(), dst, opt); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(20, func() {
		if err := p.Render(context.Background(), dst, opt); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 1 {
		t.Errorf("%v allocations per cached render", allocs)
	}
}

func TestFunctions(t *testing.T) {
	doc, _ := Open(buildPDF([]string{""}, "",
		"<< /FunctionType 0 /Domain [0 1] /Range [0 1] /Size [2] /BitsPerSample 8 /Length 2 >>\nstream\n\x00\xff\nendstream",
		"<< /FunctionType 3 /Domain [0 1] /Functions [<< /FunctionType 2 /Domain [0 1] /C0 [0] /C1 [1] >> << /FunctionType 2 /Domain [0 1] /C0 [1] /C1 [0] >>] /Bounds [0.5] /Encode [0 1 0 1] >>",
		"<< /FunctionType 4 /Domain [0 1] /Range [0 1] /Length 40 >>\nstream\n{ dup 0.5 gt { pop 1 } { 2 mul } ifelse }\nendstream",
	))
	for _, c := range []struct {
		obj     int
		in, out float64
	}{
		{100, 0.25, 0.25}, {100, 1, 1},
		{101, 0.25, 0.5}, {101, 0.75, 0.5},
		{102, 0.25, 0.5}, {102, 0.75, 1},
	} {
		f := doc.function(refTo(c.obj).Object(), 0)
		if f == nil {
			t.Fatalf("function %d not read", c.obj)
		}
		if got := f.eval([]float64{c.in})[0]; math.Abs(got-c.out) > 1e-9 {
			t.Errorf("function %d(%v) = %v, want %v", c.obj, c.in, got, c.out)
		}
	}
}

func FuzzTransparency(f *testing.F) {
	for _, s := range []string{
		"/M gs 1 0 0 rg 0 0 100 100 re f /K Do",
		"q /S gs 0 0 1 RG 5 w 0 0 m 200 100 l S /I Do Q",
		"/A gs /L gs BT /F1 30 Tf 2 Tr (Hi) Tj ET /K Do",
		"0 0 50 50 re W n /S gs /M gs /K Do /I Do BI /W 1 /H 1 /CS /G /BPC 8 ID \x80 EI",
		"q 1 0 0 1 1e30 0 cm /S gs /K Do Q /L gs 0 0 0 0 re f",
		"/O gs 0 1 0 0 k 0 0 100 100 re f /F gs BT -20 Tc (HHH) Tj ET /K Do",
		"/F gs /O gs /M gs BT 0 Tr -30 Tc (HH) ' ET /I Do",
	} {
		f.Add(s)
	}
	res := "/ExtGState << /M << /BM /Multiply >> /A << /ca 0.5 /BM [/Hue] >>" +
		" /S << /SMask << /S /Luminosity /G 101 0 R /BC [0.5] /TR 103 0 R >> >>" +
		" /L << /SMask << /S /Alpha /G 102 0 R >> /BM /SoftLight >>" +
		" /O << /OP true /OPM 1 /TR 103 0 R /ca 0.5 >> /F << /Font [100 0 R 20] /TK true /TR2 [103 0 R /Identity 103 0 R 103 0 R] >> >>" +
		" /XObject << /K 101 0 R /I 102 0 R >> /Font << /F1 100 0 R >>"
	calc := "<< /FunctionType 4 /Domain [0 1] /Range [0 1] /Length 12 >>\nstream\n{ 1 exch sub }\nendstream"
	f.Fuzz(func(t *testing.T, c string) {
		data := buildPDF([]string{c}, "/Resources << "+res+" >>", helvetica,
			formObj("/Group << /S /Transparency /K true /CS /DeviceGray >> /Resources << "+res+" >>",
				"/A gs 0.5 g 0 0 150 80 re f /L gs 0 0 1 rg 20 20 40 40 re f"),
			formObj("/Group << /S /Transparency /I true >> /Resources << "+res+" >>", "/M gs 1 0 0 rg 30 30 60 40 re f"),
			calc)
		doc, err := Open(data)
		if err != nil {
			return
		}
		p, err := doc.Page(0)
		if err != nil {
			return
		}
		dst := image.NewRGBA(p.Bounds(0.5))
		for _, workers := range []int{1, 3} {
			err = p.Render(context.Background(), dst, RenderOptions{Scale: 0.5, Workers: workers, Deadline: time.Now().Add(2 * time.Second),
				SimulateOverprint: workers > 1})
			var pe *PanicError
			if errors.As(err, &pe) {
				t.Fatalf("%v\n%s", pe.Value, pe.Stack)
			}
		}
	})
}

func TestPageBackdropIsTransparent(t *testing.T) {
	// Blended onto the page, a colour over nothing stays itself (the
	// paper comes after the page group); over red it blends.
	c := "1 0 0 rg 0 0 100 100 re f /G0 gs 0 0 1 rg 50 0 100 100 re f"
	img, _ := renderTransparent(t, c, "/ExtGState << /G0 100 0 R >>", "<< /BM /Difference >>")
	assertPixel(t, img, 25, 50, rgba(255, 0, 0, 255))
	assertPixel(t, img, 75, 50, rgba(255, 0, 255, 255))
	assertPixel(t, img, 125, 50, rgba(0, 0, 255, 255))
	assertPixel(t, img, 175, 50, white)
}
