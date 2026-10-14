package cera

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/timzifer/cera/internal/cmyk"
)

// renderCMYK renders page 0 of data at scale 1 in CMYK.
func renderCMYK(t *testing.T, data []byte) *image.CMYK {
	t.Helper()
	return renderCMYKDoc(t, editOpen(t, data), RenderOptions{Scale: 1})
}

// renderCMYKDoc renders page 0 of doc in CMYK.
func renderCMYKDoc(t *testing.T, doc *Document, opt RenderOptions) *image.CMYK {
	t.Helper()
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	dst := image.NewCMYK(p.Bounds(opt.Scale))
	if err := p.RenderCMYK(context.Background(), dst, opt); err != nil {
		t.Fatal(err)
	}
	return dst
}

func inkAt(img *image.CMYK, x, y int) [4]uint8 {
	c := img.CMYKAt(x, y)
	return [4]uint8{c.C, c.M, c.Y, c.K}
}

// TestRenderCMYKColours keeps DeviceCMYK values, puts grey on black,
// separations of process colorants on their inks, and converts RGB as PDF
// converts DeviceRGB.
func TestRenderCMYKColours(t *testing.T) {
	content := "0.2 0.4 0.6 0.8 k 0 0 20 20 re f " + // DeviceCMYK
		"0.25 g 20 0 20 20 re f " + // grey: 75 % black
		"1 0 0 rg 40 0 20 20 re f " + // red: magenta and yellow
		"/Cy cs 0.5 scn 60 0 20 20 re f " + // Separation Cyan
		"/MB cs 0.3 0.7 scn 80 0 20 20 re f " + // DeviceN Magenta Black
		"/Sp cs 1 scn 100 0 20 20 re f " + // a spot colour through its alternate
		"0 g BT /F1 1 Tf ET"
	data := buildPDF([]string{content}, "/Resources <</ColorSpace <<"+
		"/Cy [/Separation /Cyan /DeviceCMYK <</FunctionType 2 /Domain [0 1] /C0 [0 0 0 0] /C1 [1 0 0 0] /N 1>>]"+
		" /MB [/DeviceN [/Magenta /Black] /DeviceCMYK <</FunctionType 4 /Domain [0 1 0 1] /Range [0 1 0 1 0 1 0 1] /Length 20>>]"+
		" /Sp [/Separation /Gold /DeviceCMYK <</FunctionType 2 /Domain [0 1] /C0 [0 0 0 0] /C1 [0 0.3 0.9 0.1] /N 1>>]"+
		">>>>")
	img := renderCMYK(t, data)
	h := img.Rect.Dy()
	for _, c := range []struct {
		name string
		x    int
		want [4]uint8
	}{
		{"DeviceCMYK", 10, [4]uint8{51, 102, 153, 204}},
		{"grey", 30, [4]uint8{0, 0, 0, 191}},
		{"red", 50, [4]uint8{0, 255, 255, 0}},
		{"Separation Cyan", 70, [4]uint8{128, 0, 0, 0}},
		{"DeviceN Magenta Black", 90, [4]uint8{0, 77, 0, 179}},
		{"spot colour", 110, [4]uint8{0, 77, 230, 26}},
		{"paper", 150, [4]uint8{}},
	} {
		if got := inkAt(img, c.x, h-10); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// assertInks checks the inks at (x, y) to within tol.
func assertInks(t *testing.T, img *image.CMYK, name string, x, y int, want [4]uint8, tol int) {
	t.Helper()
	got := inkAt(img, x, y)
	for i := range got {
		if d := int(got[i]) - int(want[i]); d < -tol || d > tol {
			t.Errorf("%s at (%d, %d): %v, want %v", name, x, y, got, want)
			return
		}
	}
}

// TestRenderCMYKTransparency composites inks: opacity mixes them, blend
// modes blend their complements, and black takes the backdrop's or the
// source's in the non-separable modes.
func TestRenderCMYKTransparency(t *testing.T) {
	content := "1 0 0 0 k 0 0 20 20 re f /H gs 0 1 0 0 k 0 0 20 20 re f /N gs " +
		"0.5 0 0 0 k 20 0 20 20 re f /M gs 0.4 0 0 0 k 20 0 20 20 re f /N gs " +
		"0 0 0 0.6 k 40 0 20 20 re f /U gs 1 0 0 0 k 40 0 20 20 re f /N gs " +
		"0 0 0 0.6 k 60 0 20 20 re f /L gs 0 0 0 0.2 k 60 0 20 20 re f /N gs " +
		"/Q gs 0 0 0 1 k 80 0 20 20 re f"
	data := buildPDF([]string{content}, "/Resources <</ExtGState <<"+
		"/H <</ca 0.5>> /N <</ca 1 /BM /Normal>> /M <</BM /Multiply>> /U <</BM /Hue>> /L <</BM /Luminosity>>"+
		" /Q <</ca 0.25>> >>>>")
	img := renderCMYK(t, data)
	h := img.Rect.Dy()
	assertInks(t, img, "opacity", 10, h-10, [4]uint8{128, 128, 0, 0}, 1)
	assertInks(t, img, "Multiply", 30, h-10, [4]uint8{179, 0, 0, 0}, 1)
	assertInks(t, img, "Hue keeps black", 50, h-10, [4]uint8{0, 0, 0, 153}, 1)
	assertInks(t, img, "Luminosity takes black", 70, h-10, [4]uint8{0, 0, 0, 51}, 1)
	assertInks(t, img, "opacity over paper", 90, h-10, [4]uint8{0, 0, 0, 64}, 1)
}

// TestRenderCMYKSoftMask draws a luminosity soft mask in the grey of its
// CMYK colours.
func TestRenderCMYKSoftMask(t *testing.T) {
	// White on the left, half black in the middle, black on the right.
	mask := formObj("/Group << /S /Transparency /CS /DeviceCMYK >>",
		"0 0 0 0 k 0 0 60 100 re f 0 0 0 0.5 k 60 0 60 100 re f 0 0 0 1 k 120 0 80 100 re f")
	data := buildPDF([]string{"/S gs 1 0 0 0 k 0 0 200 100 re f"},
		"/Resources <</ExtGState <</S <</SMask 100 0 R>> >> >>",
		"<< /S /Luminosity /G 101 0 R >>", mask)
	img := renderCMYK(t, data)
	assertInks(t, img, "white", 30, 50, [4]uint8{255, 0, 0, 0}, 1)
	assertInks(t, img, "half black", 90, 50, [4]uint8{128, 0, 0, 0}, 1)
	assertInks(t, img, "black", 160, 50, [4]uint8{}, 1)
}

// TestRenderCMYKShadingImage keeps the inks of a DeviceCMYK shading and
// the samples of a DeviceCMYK image.
func TestRenderCMYKShadingImage(t *testing.T) {
	sh := "<< /ShadingType 2 /ColorSpace /DeviceCMYK /Coords [0 0 100 0] " +
		"/Function << /FunctionType 2 /Domain [0 1] /C0 [0 0.2 0 0] /C1 [1 0.2 0 0.4] /N 1 >> >>"
	im := streamObj("/Subtype /Image /Width 2 /Height 1 /ColorSpace /DeviceCMYK /BitsPerComponent 8",
		[]byte{10, 20, 30, 40, 200, 150, 100, 50})
	data := buildPDF([]string{"q 0 0 100 50 re W n /Sh sh Q q 100 0 0 50 100 50 cm /Im Do Q"},
		"/Resources <</Shading <</Sh 100 0 R>> /XObject <</Im 101 0 R>> >>", sh, im)
	img := renderCMYK(t, data)
	assertInks(t, img, "shading start", 0, 75, [4]uint8{1, 51, 0, 1}, 2)
	assertInks(t, img, "shading middle", 50, 75, [4]uint8{128, 51, 0, 51}, 2)
	assertInks(t, img, "shading end", 99, 75, [4]uint8{254, 51, 0, 101}, 2)
	assertInks(t, img, "image left", 125, 25, [4]uint8{10, 20, 30, 40}, 0)
	assertInks(t, img, "image right", 175, 25, [4]uint8{200, 150, 100, 50}, 0)
}

// TestRenderCMYKProfile converts RGB through the colour-to-ink table of a
// profile that has one.
func TestRenderCMYKProfile(t *testing.T) {
	icc, err := os.ReadFile(filepath.Join("internal", "cmyk", "testdata", "swop-lut16.icc"))
	if err != nil {
		t.Fatal(err)
	}
	prof, err := cmyk.Parse(icc)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := OpenWith(buildPDF([]string{"1 0 0 rg 0 0 200 100 re f"}, ""), OpenOptions{CMYKProfile: icc})
	if err != nil {
		t.Fatal(err)
	}
	ink, ok := prof.Ink(srgbD50.apply(vec3{1, 0, 0}))
	if !ok {
		t.Fatal("no B2A")
	}
	var want [4]uint8
	for i, v := range ink {
		want[i] = uint8(v*255 + 0.5)
	}
	img := renderCMYKDoc(t, doc, RenderOptions{Scale: 1})
	assertInks(t, img, "red", 100, 50, want, 1)
}

// TestRenderCMYKRegion draws a region and bands as the whole page has it.
func TestRenderCMYKRegion(t *testing.T) {
	data := buildPDF([]string{"0.1 0.2 0.3 0.4 k 0 0 100 100 re f 1 0 0 rg 100 0 100 100 re f"}, "")
	doc := editOpen(t, data)
	whole := renderCMYKDoc(t, doc, RenderOptions{Scale: 2})
	region := image.Rect(150, 30, 290, 170)
	var bands []image.Rectangle
	part := renderCMYKDoc(t, doc, RenderOptions{Scale: 2, Region: region, Band: func(r image.Rectangle) { bands = append(bands, r) }})
	for y := 0; y < whole.Rect.Dy(); y++ {
		for x := 0; x < whole.Rect.Dx(); x++ {
			want := inkAt(whole, x, y)
			if !image.Pt(x, y).In(region) {
				want = [4]uint8{}
			}
			if got := inkAt(part, x, y); got != want {
				t.Fatalf("(%d, %d): %v, want %v", x, y, got, want)
			}
		}
	}
	if len(bands) == 0 || bands[0].Min != region.Min || bands[len(bands)-1].Max != region.Max {
		t.Errorf("bands %v of region %v", bands, region)
	}
}
