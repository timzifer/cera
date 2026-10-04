package cera

import (
	"context"
	"image"
	"image/color"
	"testing"
)

func TestExtGStateFont(t *testing.T) {
	const res = "/Resources << /Font << /F1 100 0 R >> /ExtGState << /G 101 0 R >> >>"
	viaTf, _, err := renderPage(t, buildPDF([]string{"BT /F1 40 Tf 10 30 Td (HI) Tj ET"}, res, helvetica, "<< >>"), 0,
		RenderOptions{Background: white, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	viaGS, st, err := renderPage(t, buildPDF([]string{"/G gs BT 10 30 Td (HI) Tj ET"}, res, helvetica, "<< /Font [100 0 R 40] >>"), 0,
		RenderOptions{Background: white, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if st.Glyphs != 2 || st.Errors != 0 {
		t.Errorf("stats %+v", st)
	}
	if d := maxDiff(t, viaTf, viaGS, viaTf.Rect); d != 0 {
		t.Errorf("gs /Font differs from Tf by %d", d)
	}
}

// overprintPDF paints a cyan square and, overlapping it, a yellow one in
// the state /O sets, in colour space /CS (100) with yellow given as yellow.
func overprintPDF(ext, cs, yellow string) []byte {
	c := "0 0 100 100 re 1 0 0 0 k f /O gs /CS cs " + yellow + " sc 50 0 100 100 re f"
	return buildPDF([]string{c}, "/Resources << /ExtGState << /O 101 0 R >> /ColorSpace << /CS 100 0 R >> >>",
		cs, "<< "+ext+" >>")
}

func TestOverprint(t *testing.T) {
	cyan, yellow, green := rgba(0, 255, 255, 255), rgba(255, 255, 0, 255), rgba(0, 255, 0, 255)
	sep := "[/Separation /Yellow /DeviceCMYK << /FunctionType 2 /Domain [0 1] /C0 [0 0 0 0] /C1 [0 0 1 0] /N 1 >>]"
	for _, c := range []struct {
		name, ext, cs, yellow string
		overprints            bool // with SimulateOverprint
	}{
		{"cmyk-opm1", "/OP true /OPM 1", "/DeviceCMYK", "0 0 1 0", true},
		{"cmyk-op-only", "/OP false /op true /OPM 1", "/DeviceCMYK", "0 0 1 0", true},
		{"cmyk-fill-off", "/OP true /op false /OPM 1", "/DeviceCMYK", "0 0 1 0", false},
		{"cmyk-opm0", "/OP true", "/DeviceCMYK", "0 0 1 0", false},
		{"separation", "/OP true", sep, "1", true},
		{"rgb", "/OP true /OPM 1", "/DeviceRGB", "1 1 0", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Device values, so that the simulated overprint is plain
			// arithmetic.
			doc, err := OpenWith(overprintPDF(c.ext, c.cs, c.yellow), OpenOptions{NaiveCMYK: true})
			if err != nil {
				t.Fatal(err)
			}
			p, _ := doc.Page(0)
			dst := image.NewRGBA(p.Bounds(1))
			for _, sim := range []bool{false, true, false} {
				var st Stats
				if err := p.Render(context.Background(), dst, RenderOptions{Background: white, Stats: &st, SimulateOverprint: sim}); err != nil {
					t.Fatal(err)
				}
				if st.Reused {
					t.Errorf("simulate %v: list of the other setting reused", sim)
				}
				want := yellow
				if sim && c.overprints {
					want = green
				}
				assertNear(t, dst, 25, 50, cyan, 1)
				assertNear(t, dst, 75, 50, want, 1)
				assertNear(t, dst, 125, 50, yellow, 1)
				if n, counted := st.Unsupported["overprint"], c.overprints && !sim; (n == 1) != counted || n > 1 {
					t.Errorf("simulate %v: overprint counted %d", sim, n)
				}
			}
		})
	}
}

// knockoutText shows two overlapping capital Hs at 50% in render mode mode,
// in the state ext sets, and returns the darkest grey drawn.
func knockoutText(t *testing.T, ext, mode string) (uint8, Stats) {
	t.Helper()
	c := "/G gs BT /F1 40 Tf " + mode + " Tr 2 w 10 30 Td -18 Tc (HH) Tj ET"
	data := buildPDF([]string{c}, "/Resources << /Font << /F1 100 0 R >> /ExtGState << /G 101 0 R >> >>",
		helvetica, "<< /ca 0.5 /CA 0.5 "+ext+" >>")
	img, st, err := renderPage(t, data, 0, RenderOptions{Background: white, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	darkest := uint8(255)
	for y := 0; y < img.Rect.Dy(); y++ {
		for x := 0; x < img.Rect.Dx(); x++ {
			darkest = min(darkest, img.RGBAAt(x, y).R)
		}
	}
	return darkest, st
}

func TestTextKnockout(t *testing.T) {
	// TK true (the default): where the glyphs overlap, the second knocks
	// the first out, so nothing is darker than one glyph at 50%.
	ko, st := knockoutText(t, "", "0")
	if ko < 126 || ko > 129 {
		t.Errorf("darkest with knockout %d, want 127", ko)
	}
	if st.Groups != 1 || st.Glyphs != 2 || len(st.Unsupported) != 0 {
		t.Errorf("stats %+v", st)
	}
	// TK false: the overlap composites twice.
	dark, st := knockoutText(t, "/TK false", "0")
	if dark > 70 {
		t.Errorf("darkest without knockout %d, want 64", dark)
	}
	if st.Groups != 0 {
		t.Errorf("groups %d", st.Groups)
	}
	// Stroked text is not knocked out, but counted.
	if _, st := knockoutText(t, "", "1"); st.Unsupported["text-knockout"] != 1 {
		t.Errorf("stroked: %v", st.Unsupported)
	}
	// Opaque glyphs need no group.
	data := textPDF("BT /F1 40 Tf 10 30 Td -18 Tc (HH) Tj ET", helvetica)
	if _, st, _ := renderPage(t, data, 0, RenderOptions{}); st.Groups != 0 {
		t.Errorf("opaque: groups %d", st.Groups)
	}
}

func TestTransfer(t *testing.T) {
	const invert = "<< /FunctionType 2 /Domain [0 1] /C0 [1] /C1 [0] /N 1 >>"
	red, cyan := rgba(255, 0, 0, 255), rgba(0, 255, 255, 255)
	for _, c := range []struct {
		name, ext string
		want      color.RGBA
		counted   int
	}{
		{"function", "/TR 102 0 R", cyan, 0},
		{"array", "/TR [102 0 R /Identity /Identity 102 0 R]", rgba(0, 0, 0, 255), 0},
		{"tr2-default", "/TR 102 0 R /TR2 /Default", red, 0},
		{"tr2", "/TR /Identity /TR2 102 0 R", cyan, 0},
		{"identity", "/TR /Identity", red, 0},
		{"unreadable", "/TR 5", red, 1},
		{"short-array", "/TR [102 0 R]", red, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			content := "/T gs 1 0 0 rg 0 0 100 100 re f"
			img, st := renderTransparent(t, content, "/ExtGState << /T 101 0 R >>", "<< >>", "<< "+c.ext+" >>", invert)
			assertNear(t, img, 50, 50, c.want, 1)
			assertPixel(t, img, 150, 50, white)
			if st.Unsupported["transfer"] != c.counted {
				t.Errorf("transfer counted %d", st.Unsupported["transfer"])
			}
		})
	}
	// Images keep their colours, and are counted.
	img := streamObj("/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8", []byte{255, 0, 0})
	content := "/T gs q 100 0 0 100 0 0 cm /Im Do Q"
	out, st := renderTransparent(t, content, "/ExtGState << /T 101 0 R >> /XObject << /Im 100 0 R >>", img, "<< /TR 102 0 R >>", invert)
	assertNear(t, out, 50, 50, red, 1)
	if st.Unsupported["transfer"] != 1 {
		t.Errorf("image: transfer counted %d", st.Unsupported["transfer"])
	}
}
