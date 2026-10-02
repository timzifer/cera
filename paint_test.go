package cera

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"
)

// renderPaint renders a 200 × 100 page and fails on errors and on the
// unsupported keys of shadings and patterns.
func renderPaint(t *testing.T, content, resources string, objs ...string) *image.RGBA {
	t.Helper()
	img, st, err := renderPage(t, buildPDF([]string{content}, "/Resources << "+resources+" >>", objs...), 0,
		RenderOptions{Background: white, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	for k := range st.Unsupported {
		if strings.HasPrefix(k, "shading") || strings.HasPrefix(k, "pattern") {
			t.Errorf("unsupported %s: %d", k, st.Unsupported[k])
		}
	}
	if st.Errors != 0 {
		t.Errorf("%d errors", st.Errors)
	}
	return img
}

const redToBlue = "<< /FunctionType 2 /Domain [0 1] /C0 [1 0 0] /C1 [0 0 1] /N 1 >>"

func TestAxialShading(t *testing.T) {
	sh := "<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 200 0] /Function 101 0 R >>"
	img := renderPaint(t, "/S sh", "/Shading << /S 100 0 R >>", sh, redToBlue)
	assertNear(t, img, 0, 50, red, 2)
	assertNear(t, img, 199, 50, blue, 2)
	assertNear(t, img, 100, 50, color.RGBA{127, 0, 127, 255}, 2)
}

func TestShadingHardStop(t *testing.T) {
	// A stitching function: red up to 0.3, then blue.
	stitch := "<< /FunctionType 3 /Domain [0 1] /Bounds [0.3] /Encode [0 1 0 1] /Functions [102 0 R 103 0 R] >>"
	sh := "<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 200 0] /Function 101 0 R >>"
	img := renderPaint(t, "/S sh", "/Shading << /S 100 0 R >>", sh, stitch,
		"<< /FunctionType 2 /Domain [0 1] /C0 [1 0 0] /C1 [1 0 0] /N 1 >>",
		"<< /FunctionType 2 /Domain [0 1] /C0 [0 0 1] /C1 [0 0 1] /N 1 >>")
	// The break is at x = 60 exactly: pixel 59 is red, pixel 60 blue.
	assertPixel(t, img, 59, 50, red)
	assertPixel(t, img, 60, 50, blue)
}

func TestRadialShadingExtend(t *testing.T) {
	sh := "<< /ShadingType 3 /ColorSpace /DeviceRGB /Coords [100 50 0 100 50 40] /Function 101 0 R /Extend [false true] >>"
	img := renderPaint(t, "/S sh", "/Shading << /S 100 0 R >>", sh, redToBlue)
	assertNear(t, img, 100, 50, red, 8)
	assertNear(t, img, 100+20, 50, color.RGBA{127, 0, 127, 255}, 8)
	assertNear(t, img, 190, 50, blue, 2) // extended
}

func TestShadingBBoxAndAlpha(t *testing.T) {
	sh := "<< /ShadingType 2 /ColorSpace /DeviceGray /Coords [0 0 200 0] /BBox [50 0 150 100]" +
		" /Function << /FunctionType 2 /Domain [0 1] /C0 [0] /C1 [0] /N 1 >> >>"
	img := renderPaint(t, "/A gs /S sh", "/Shading << /S 100 0 R >> /ExtGState << /A << /ca 0.5 >> >>", sh)
	assertPixel(t, img, 25, 50, white)
	assertNear(t, img, 100, 50, color.RGBA{128, 128, 128, 255}, 1)
}

func TestFunctionShading(t *testing.T) {
	// Red grows with x, blue with y, over the page.
	fn := "<< /FunctionType 4 /Domain [0 1 0 1] /Range [0 1 0 1 0 1] /Length 20 >>\nstream\n{ 0 exch }\nendstream"
	sh := "<< /ShadingType 1 /ColorSpace /DeviceRGB /Matrix [200 0 0 100 0 0] /Function 101 0 R >>"
	img := renderPaint(t, "/S sh", "/Shading << /S 100 0 R >>", sh, fn)
	assertNear(t, img, 2, 97, color.RGBA{0, 0, 0, 255}, 8)
	assertNear(t, img, 197, 97, color.RGBA{255, 0, 0, 255}, 8)
	assertNear(t, img, 2, 2, color.RGBA{0, 0, 255, 255}, 8)
}

func TestFreeFormMesh(t *testing.T) {
	// A red triangle over the lower left half of the page, and one that
	// shares its diagonal and has a blue corner at the upper right; 8 bits
	// per value: flag, x, y, r, g, b.
	data := []byte{
		0, 0, 0, 255, 0, 0,
		0, 255, 0, 255, 0, 0,
		0, 0, 255, 255, 0, 0,
		1, 255, 255, 0, 0, 255,
	}
	sh := streamObj("/ShadingType 4 /ColorSpace /DeviceRGB /BitsPerCoordinate 8 /BitsPerComponent 8"+
		" /BitsPerFlag 8 /Decode [0 200 0 100 0 1 0 1 0 1]", data)
	img := renderPaint(t, "/S sh", "/Shading << /S 100 0 R >>", sh)
	assertPixel(t, img, 10, 80, red)
	// Blue in proportion to x/200 + y/100 − 1 at the pixel centre.
	assertNear(t, img, 190, 10, color.RGBA{39, 0, 216, 255}, 2)
	assertNear(t, img, 120, 20, color.RGBA{154, 0, 101, 255}, 2)
}

func TestLatticeMesh(t *testing.T) {
	// Two rows of two vertices over the left half: red at the bottom,
	// blue at the top; 8 bits per value: x, y, r, g, b.
	data := []byte{
		0, 0, 255, 0, 0, 128, 0, 255, 0, 0,
		0, 255, 0, 0, 255, 128, 255, 0, 0, 255,
	}
	sh := streamObj("/ShadingType 5 /ColorSpace /DeviceRGB /BitsPerCoordinate 8 /BitsPerComponent 8"+
		" /VerticesPerRow 2 /Decode [0 200 0 100 0 1 0 1 0 1]", data)
	img := renderPaint(t, "/S sh", "/Shading << /S 100 0 R >>", sh)
	assertNear(t, img, 50, 99, red, 4)
	assertNear(t, img, 50, 0, blue, 4)
	assertPixel(t, img, 150, 50, white) // outside the mesh
}

func TestCoonsPatch(t *testing.T) {
	// A flat patch over the page: corners red (0, 0), green (0, 1), blue
	// (1, 1), black (1, 0); 8 bits per coordinate and component.
	pts := [][2]byte{{0, 0}, {0, 85}, {0, 170}, {0, 255}, {85, 255}, {170, 255}, {255, 255}, {255, 170},
		{255, 85}, {255, 0}, {170, 0}, {85, 0}}
	b := []byte{0}
	for _, p := range pts {
		b = append(b, p[0], p[1])
	}
	b = append(b, 255, 0, 0, 0, 255, 0, 0, 0, 255, 0, 0, 0)
	sh := streamObj("/ShadingType 6 /ColorSpace /DeviceRGB /BitsPerCoordinate 8 /BitsPerComponent 8"+
		" /BitsPerFlag 8 /Decode [0 200 0 100 0 1 0 1 0 1]", b)
	img := renderPaint(t, "/S sh", "/Shading << /S 100 0 R >>", sh)
	assertNear(t, img, 1, 98, red, 12)
	assertNear(t, img, 1, 1, green, 12)
	assertNear(t, img, 198, 1, blue, 12)
	assertNear(t, img, 198, 98, black, 12)
}

func TestShadingPattern(t *testing.T) {
	// An axial shading from x = 50 to 150 without extension, on a cyan
	// background, filling a rectangle and stroking a line.
	pat := "<< /PatternType 2 /Shading << /ShadingType 2 /ColorSpace /DeviceRGB /Coords [50 0 150 0]" +
		" /Function 101 0 R /Background [0 1 1] >> >>"
	img := renderPaint(t, "/Pattern cs /P scn 0 0 200 50 re f /Pattern CS /P SCN 20 w 0 80 m 200 80 l S",
		"/Pattern << /P 100 0 R >>", pat, redToBlue)
	assertPixel(t, img, 20, 75, color.RGBA{0, 255, 255, 255}) // background
	assertNear(t, img, 51, 75, red, 4)
	assertNear(t, img, 148, 75, blue, 4)
	assertNear(t, img, 100, 20, color.RGBA{127, 0, 127, 255}, 4) // the stroke
	assertPixel(t, img, 100, 40, white)
}

// hatchPattern is a tiling pattern of a 2 × 2 red square in a 10 × 10 cell
// at the origin.
func hatchPattern(paintType int, extra, cell string) string {
	return streamObj(fmt.Sprintf("/PatternType 1 /PaintType %d /TilingType 1 /BBox [0 0 10 10] /XStep 10 /YStep 10"+
		" /Resources << >> %s", paintType, extra), []byte(cell))
}

func TestTilingPatternTile(t *testing.T) {
	// 200 cells: drawn from a tile.
	img := renderPaint(t, "/Pattern cs /P scn 0 0 200 100 re f", "/Pattern << /P 100 0 R >>",
		hatchPattern(1, "", "1 0 0 rg 0 0 2 2 re f"))
	for _, c := range [][2]int{{0, 99}, {1, 98}, {10, 99}, {50, 59}, {190, 9}} {
		assertNear(t, img, c[0], c[1], red, 8)
	}
	for _, c := range [][2]int{{5, 95}, {3, 99}, {15, 90}, {108, 45}} {
		assertNear(t, img, c[0], c[1], white, 8)
	}
}

func TestTilingPatternReplay(t *testing.T) {
	// Cells of 1200 pixels, too large for a tile: drawn as vector
	// operations, through the object's clip.
	img := renderPaint(t, "/Pattern cs /P scn 0 0 150 100 re f", "/Pattern << /P 100 0 R >>",
		hatchPattern(1, "/Matrix [120 0 0 120 0 0]", "1 0 0 rg 0 0 0.5 0.5 re f"))
	assertPixel(t, img, 30, 70, red)
	assertPixel(t, img, 59, 99, red)
	assertPixel(t, img, 60, 70, white)
	assertPixel(t, img, 30, 39, white)
}

func TestTilingPatternNoSeams(t *testing.T) {
	// Stripes that run across the cells' edges at a scale where cells are
	// not whole pixels: every pixel of a stripe is fully red.
	pat := streamObj("/PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 30 30] /XStep 30 /YStep 30"+
		" /Resources << >>", []byte("1 0 0 rg 0 0 10 30 re f 20 0 10 30 re f"))
	img, _, err := renderPage(t, buildPDF([]string{"/Pattern cs /P scn 0 0 200 100 re f"},
		"/Resources << /Pattern << /P 100 0 R >> >>", pat), 0, RenderOptions{Background: white, Scale: 150.0 / 72, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	s := 150.0 / 72
	for y := 2; y < img.Bounds().Dy()-2; y++ {
		for _, ux := range []float64{25, 35, 55, 65} { // inside stripes that cross x = 30 and 60
			x := int(ux * s)
			if c := img.RGBAAt(x, y); c.G > 2 {
				t.Fatalf("pixel (%d, %d) = %v", x, y, c)
			}
		}
	}
}

func TestUncolouredPattern(t *testing.T) {
	// Drawn from a tile, and with cells too large for one, replayed.
	for _, c := range []struct{ m, cell string }{{"", "0 0 2 2"}, {"/Matrix [120 0 0 120 0 0]", "0 0 0.1 0.1"}} {
		t.Run(c.m, func(t *testing.T) {
			img := renderPaint(t, "/CS0 cs 0 0 1 /P scn 0 0 200 100 re f",
				"/Pattern << /P 100 0 R >> /ColorSpace << /CS0 [/Pattern /DeviceRGB] >>",
				hatchPattern(2, c.m, "1 0 0 rg "+c.cell+" re f"))
			assertNear(t, img, 0, 99, blue, 8)
			assertNear(t, img, 20, 95, white, 8)
		})
	}
}

func TestRotatedPattern(t *testing.T) {
	// A hatch of vertical lines 1 unit wide every 4 units, rotated by
	// 45°: half the cells' area... about a quarter of the page is red.
	pat := streamObj("/PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 4 4] /XStep 4 /YStep 4"+
		" /Matrix [0.7071 0.7071 -0.7071 0.7071 0 0] /Resources << >>", []byte("1 0 0 rg 0 0 1 4 re f"))
	img := renderPaint(t, "/Pattern cs /P scn 0 0 200 100 re f", "/Pattern << /P 100 0 R >>", pat)
	var sum int
	for y := range 100 {
		for x := range 200 {
			sum += 255 - int(img.RGBAAt(x, y).G)
		}
	}
	if share := float64(sum) / 255 / 20000; share < 0.22 || share > 0.28 {
		t.Errorf("red share %.3f, want 0.25", share)
	}
}

func TestPatternWorkersAndTilesAgree(t *testing.T) {
	pat := hatchPattern(1, "/Matrix [1.3 0.4 -0.4 1.3 3 7]", "1 0 0 rg 0 0 3 3 re f 0 0 1 RG 8 8 m 12 12 l S")
	sh := "<< /ShadingType 3 /ColorSpace /DeviceRGB /Coords [60 50 5 120 40 60] /Function 102 0 R /Extend [true true] >>"
	content := "/Pattern cs /P scn 0 0 200 100 re f q 40 10 120 80 re W n /S sh Q"
	doc, _ := Open(buildPDF([]string{content}, "/Resources << /Pattern << /P 100 0 R >> /Shading << /S 101 0 R >> >>",
		pat, sh, redToBlue))
	p, _ := doc.Page(0)
	const scale = 3
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
	tile := image.Rect(170, 90, 500, 260)
	if d := maxDiff(t, one, render(3, tile), tile); d > 2 {
		t.Errorf("tile differs by %d", d)
	}
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

func TestBrokenShadingsAndPatterns(t *testing.T) {
	for _, c := range []struct{ content, res, key string }{
		{"/S sh", "/Shading << /S << /ShadingType 9 >> >>", "shading-bad"},
		{"/S sh", "/Shading << /S << /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 1] >> >>", "shading-bad"},
		{"/Pattern cs /P scn 0 0 10 10 re f", "/Pattern << /P << /PatternType 7 >> >>", "pattern-bad"},
	} {
		_, st, err := renderPage(t, buildPDF([]string{c.content}, "/Resources << "+c.res+" >>"), 0, RenderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if st.Unsupported[c.key] != 1 {
			t.Errorf("%s: unsupported %v", c.res, st.Unsupported)
		}
	}
}

func TestMeshWithFunction(t *testing.T) {
	// A tensor-product patch (type 7) over the page whose corners carry t:
	// 0 at the left, 1 at the right, through a red-to-blue function.
	b := []byte{0}
	for _, k := range patchOrder {
		b = append(b, byte(k[0]*85), byte(k[1]*85))
	}
	b = append(b, 0, 0, 255, 255) // t at corners 00, 03, 33, 30
	sh := streamObj("/ShadingType 7 /ColorSpace /DeviceRGB /BitsPerCoordinate 8 /BitsPerComponent 8"+
		" /BitsPerFlag 8 /Decode [0 200 0 100 0 1] /Function 101 0 R", b)
	img := renderPaint(t, "/S sh", "/Shading << /S 100 0 R >>", sh, redToBlue)
	assertNear(t, img, 0, 50, red, 4)
	assertNear(t, img, 199, 50, blue, 4)
	assertNear(t, img, 100, 20, color.RGBA{127, 0, 127, 255}, 4)
}

func TestPaintSteadyStateAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("sync.Pool drops items under the race detector")
	}
	pat := hatchPattern(1, "/Matrix [1.3 0.4 -0.4 1.3 3 7]", "1 0 0 rg 0 0 3 3 re f")
	sh := "<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 200 0] /Function 102 0 R >>"
	mesh := streamObj("/ShadingType 4 /ColorSpace /DeviceRGB /BitsPerCoordinate 8 /BitsPerComponent 8"+
		" /BitsPerFlag 8 /Decode [0 200 0 100 0 1 0 1 0 1]",
		[]byte{0, 0, 0, 255, 0, 0, 0, 255, 0, 0, 255, 0, 0, 0, 255, 0, 0, 255})
	content := "/Pattern cs /P scn 0 0 200 100 re f q 0 0 100 50 re W n /S sh Q /M sh"
	doc, _ := Open(buildPDF([]string{content},
		"/Resources << /Pattern << /P 100 0 R >> /Shading << /S 101 0 R /M 103 0 R >> >>", pat, sh, redToBlue, mesh))
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
