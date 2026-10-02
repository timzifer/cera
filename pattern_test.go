package cera

import (
	"context"
	"fmt"
	"image"
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

func TestBrokenPatterns(t *testing.T) {
	for _, res := range []string{
		"/Pattern << /P << /PatternType 7 >> >>",
		"/Pattern << /P 100 0 R >>", // a tiling pattern without a stream
	} {
		_, st, err := renderPage(t, buildPDF([]string{"/Pattern cs /P scn 0 0 10 10 re f"}, "/Resources << "+res+" >>",
			"<< /PatternType 1 /PaintType 1 /BBox [0 0 1 1] /XStep 1 /YStep 1 >>"), 0, RenderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if st.Errors != 1 {
			t.Errorf("%s: %d errors, unsupported %v", res, st.Errors, st.Unsupported)
		}
	}
}
