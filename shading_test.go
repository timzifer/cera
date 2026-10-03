package cera

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"testing"
	"time"
)

// renderShaded renders content with the given resources on white, failing
// on errors and on unsupported features.
func renderShaded(t *testing.T, content, resources string, objs ...string) (*image.RGBA, Stats) {
	t.Helper()
	img, st, err := renderPage(t, buildPDF([]string{content}, "/Resources << "+resources+" >>", objs...), 0,
		RenderOptions{Background: white, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Unsupported) != 0 || st.Errors != 0 {
		t.Errorf("unsupported %v, errors %d", st.Unsupported, st.Errors)
	}
	return img, st
}

// redToBlue is a type 2 function from red to blue.
const redToBlue = "<< /FunctionType 2 /Domain [0 1] /C0 [1 0 0] /C1 [0 0 1] /N 1 >>"

func TestAxialShading(t *testing.T) {
	// Red at x = 20 to blue at x = 180, not extended.
	sh := "<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [20 0 180 0] /Function " + redToBlue + " >>"
	img, st := renderShaded(t, "/Sh0 sh", "/Shading << /Sh0 100 0 R >>", sh)
	if st.Shadings != 1 {
		t.Errorf("shadings %d", st.Shadings)
	}
	assertNear(t, img, 10, 50, white, 0)
	assertNear(t, img, 190, 50, white, 0)
	assertNear(t, img, 21, 50, rgba(255, 0, 0, 255), 3)
	assertNear(t, img, 99, 50, rgba(128, 0, 127, 255), 3)
	assertNear(t, img, 179, 50, rgba(0, 0, 255, 255), 3)

	// Extended, with a domain, through a clip and a rotated CTM.
	sh = "<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 0 50] /Domain [0.5 1] /Extend [true true] /Function " + redToBlue + " >>"
	img, _ = renderShaded(t, "20 20 40 40 re W n 0 1 -1 0 100 0 cm /Sh0 sh", "/Shading << /Sh0 100 0 R >>", sh)
	assertNear(t, img, 10, 50, white, 0)
	// User x is device y reversed: x = 0 at device x 100, t grows to the
	// left: at device x 25, t = 1.
	assertNear(t, img, 25, 50, rgba(0, 0, 255, 255), 3)
	assertNear(t, img, 59, 50, rgba(24, 0, 231, 255), 2) // v = 40.5: t = 0.81, s = 0.905
}

func TestRadialShading(t *testing.T) {
	// Concentric circles at (100, 50), radius 10 (red) to 40 (blue).
	sh := "<< /ShadingType 3 /ColorSpace /DeviceRGB /Coords [100 50 10 100 50 40] /Function " + redToBlue + " >>"
	img, _ := renderShaded(t, "/Sh0 sh", "/Shading << /Sh0 100 0 R >>", sh)
	assertNear(t, img, 100, 50, white, 0) // inside the first circle
	assertNear(t, img, 195, 50, white, 0) // outside the second
	assertNear(t, img, 125, 49, rgba(128, 0, 127, 255), 8)
	sh = "<< /ShadingType 3 /ColorSpace /DeviceRGB /Coords [100 50 10 100 50 40] /Extend [true true] /Function " + redToBlue + " >>"
	img, _ = renderShaded(t, "/Sh0 sh", "/Shading << /Sh0 100 0 R >>", sh)
	assertNear(t, img, 100, 50, rgba(255, 0, 0, 255), 0)
	assertNear(t, img, 195, 50, rgba(0, 0, 255, 255), 0)
}

func TestFunctionShading(t *testing.T) {
	// Red = x, green = y over [0 1]², scaled to the left half of the page.
	fn := streamObj("/FunctionType 4 /Domain [0 1 0 1] /Range [0 1 0 1 0 1]", []byte("{ 0 }"))
	sh := "<< /ShadingType 1 /ColorSpace /DeviceRGB /Matrix [100 0 0 100 0 0] /Function 101 0 R >>"
	img, _ := renderShaded(t, "/Sh0 sh", "/Shading << /Sh0 100 0 R >>", sh, fn)
	assertNear(t, img, 25, 25, rgba(64, 191, 0, 255), 4)
	assertNear(t, img, 75, 75, rgba(191, 64, 0, 255), 4)
	assertNear(t, img, 150, 50, white, 0) // outside the domain
}

// meshData packs 8-bit flags, 16-bit coordinates over [0 200] × [0 100]
// and 8-bit components.
type meshData struct{ bytes.Buffer }

func (m *meshData) flag(f uint8) { m.WriteByte(f) }

func (m *meshData) pt(x, y float64) {
	var b [4]byte
	binary.BigEndian.PutUint16(b[:], uint16(math.Round(x/200*65535)))
	binary.BigEndian.PutUint16(b[2:], uint16(math.Round(y/100*65535)))
	m.Write(b[:])
}

func (m *meshData) col(c ...uint8) { m.Write(c) }

const meshDict = "/BitsPerCoordinate 16 /BitsPerComponent 8 /BitsPerFlag 8 /Decode [0 200 0 100 0 1 0 1 0 1] /ColorSpace /DeviceRGB"

func TestFreeFormMesh(t *testing.T) {
	// Two triangles sharing an edge: a red-green-blue square.
	var m meshData
	m.flag(0)
	m.pt(20, 10)
	m.col(255, 0, 0)
	m.flag(0)
	m.pt(180, 10)
	m.col(0, 255, 0)
	m.flag(0)
	m.pt(20, 90)
	m.col(0, 0, 255)
	m.flag(1) // with the last two
	m.pt(180, 90)
	m.col(0, 0, 255)
	sh := streamObj("/ShadingType 4 "+meshDict, m.Bytes())
	img, _ := renderShaded(t, "/Sh0 sh", "/Shading << /Sh0 100 0 R >>", sh)
	assertNear(t, img, 10, 50, white, 0)
	assertNear(t, img, 21, 89, rgba(253, 0, 2, 255), 4)
	assertNear(t, img, 178, 89, rgba(2, 252, 0, 255), 4)
	assertNear(t, img, 100, 50, rgba(0, 128, 127, 255), 6) // on the shared edge
	assertNear(t, img, 178, 12, rgba(0, 3, 252, 255), 6)
}

func TestLatticeMeshWithFunction(t *testing.T) {
	// A 2 × 2 lattice carrying the parameter of a red-to-blue function:
	// 0 on the left, 1 on the right.
	var m meshData
	for _, y := range []float64{10, 90} {
		m.pt(20, y)
		m.col(0)
		m.pt(180, y)
		m.col(255)
	}
	sh := streamObj("/ShadingType 5 /VerticesPerRow 2 /BitsPerCoordinate 16 /BitsPerComponent 8 /Decode [0 200 0 100 0 1] /ColorSpace /DeviceRGB /Function "+redToBlue, m.Bytes())
	img, _ := renderShaded(t, "/Sh0 sh", "/Shading << /Sh0 100 0 R >>", sh)
	assertNear(t, img, 21, 50, rgba(255, 0, 0, 255), 3)
	assertNear(t, img, 100, 30, rgba(128, 0, 127, 255), 3)
	assertNear(t, img, 100, 70, rgba(128, 0, 127, 255), 3)
	assertNear(t, img, 190, 50, white, 0)
}

func TestCoonsPatch(t *testing.T) {
	// One patch with straight sides, corners red, green, blue, white,
	// and a tensor patch of the same shape: the centre is their average.
	pts := func(m *meshData) {
		// p00 p01 p02 p03 p13 p23 p33 p32 p31 p30 p20 p10, a square
		// from (20, 10) to (180, 90) with p0j on the left side.
		for _, p := range [][2]float64{
			{20, 10}, {20, 36.67}, {20, 63.33}, {20, 90},
			{73.33, 90}, {126.67, 90}, {180, 90},
			{180, 63.33}, {180, 36.67}, {180, 10},
			{126.67, 10}, {73.33, 10},
		} {
			m.pt(p[0], p[1])
		}
	}
	for _, kind := range []string{"6", "7"} {
		var m meshData
		m.flag(0)
		pts(&m)
		if kind == "7" {
			for _, p := range [][2]float64{{73.33, 36.67}, {73.33, 63.33}, {126.67, 63.33}, {126.67, 36.67}} {
				m.pt(p[0], p[1])
			}
		}
		m.col(255, 0, 0)
		m.col(0, 255, 0)
		m.col(0, 0, 255)
		m.col(255, 255, 255)
		sh := streamObj("/ShadingType "+kind+" "+meshDict, m.Bytes())
		img, _ := renderShaded(t, "/Sh0 sh", "/Shading << /Sh0 100 0 R >>", sh)
		assertNear(t, img, 100, 50, rgba(128, 128, 128, 255), 6)
		// p00 is red at the bottom left, p03 green at the top left.
		assertNear(t, img, 21, 88, rgba(252, 2, 0, 255), 8)
		assertNear(t, img, 21, 11, rgba(2, 252, 0, 255), 8)
		assertNear(t, img, 178, 11, rgba(2, 2, 252, 255), 8)
		assertNear(t, img, 10, 50, white, 0)
	}
}

func TestShadingPattern(t *testing.T) {
	// A rectangle and a stroke filled with an axial pattern whose matrix
	// moves it; the pattern's background is not painted by sh.
	pat := "<< /PatternType 2 /Matrix [1 0 0 1 20 0] /Shading 101 0 R >>"
	sh := "<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 160 0] /Function " + redToBlue + " >>"
	c := "/Pattern cs /P0 scn 20 20 160 60 re f /Pattern CS /P0 SCN 8 w 0 10 m 200 10 l S"
	img, st := renderShaded(t, c, "/Pattern << /P0 100 0 R >>", pat, sh)
	if st.Shadings != 2 {
		t.Errorf("shadings %d", st.Shadings)
	}
	assertNear(t, img, 21, 50, rgba(255, 0, 0, 255), 4)
	assertNear(t, img, 178, 50, rgba(0, 0, 255, 255), 4)
	assertNear(t, img, 100, 15, white, 0)                  // between fill and stroke
	assertNear(t, img, 100, 90, rgba(128, 0, 127, 255), 4) // the stroke
	assertNear(t, img, 100, 96, white, 0)                  // beyond its width

	// Text in a shading pattern, and a pattern with a background and a
	// BBox.
	sh2 := "<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 200 0] /BBox [0 0 100 100] /Background [0 1 0] /Function " + redToBlue + " >>"
	pat2 := "<< /PatternType 2 /Shading 101 0 R >>"
	img, _ = renderShaded(t, "/Pattern cs /P0 scn 0 0 200 100 re f", "/Pattern << /P0 100 0 R >>", pat2, sh2)
	assertNear(t, img, 10, 50, rgba(242, 0, 13, 255), 4)
	assertNear(t, img, 150, 50, white, 0) // outside the BBox
}

func TestShadingTextPattern(t *testing.T) {
	pat := "<< /PatternType 2 /Shading << /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 200 0] /Function " + redToBlue + " >> >>"
	c := "BT /F1 60 Tf /Pattern cs /P0 scn 10 30 Td (II) Tj ET"
	data := buildPDF([]string{c}, "/Resources << /Font << /F1 101 0 R >> /Pattern << /P0 100 0 R >> >>",
		pat, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	img, st, err := renderPage(t, data, 0, RenderOptions{Background: white})
	if err != nil || st.Shadings != 1 || len(st.Unsupported) != 0 {
		t.Fatalf("err %v stats %+v", err, st)
	}
	// Some pixel of the glyphs is reddish, none is black.
	reddish := false
	for y := range 100 {
		for x := range 100 {
			c := img.RGBAAt(x, y)
			reddish = reddish || (c.R > 200 && c.G < 50 && c.B < 80)
			if c.R < 20 && c.G < 20 && c.B < 20 {
				t.Fatalf("black pixel at (%d, %d)", x, y)
			}
		}
	}
	if !reddish {
		t.Error("no glyph painted")
	}
}

func TestShadingsInGroupsAndBands(t *testing.T) {
	// Shadings with alpha, a blend mode and a soft mask, drawn by several
	// workers and as tiles, agree with one pass.
	var m meshData
	m.flag(0)
	m.pt(0, 0)
	m.col(255, 0, 0)
	m.flag(0)
	m.pt(200, 0)
	m.col(0, 255, 0)
	m.flag(0)
	m.pt(100, 100)
	m.col(0, 0, 255)
	objs := []string{
		"<< /ShadingType 3 /ColorSpace /DeviceRGB /Coords [60 50 0 60 50 50] /Function " + redToBlue + " >>",
		streamObj("/ShadingType 4 "+meshDict, m.Bytes()),
		"<< /ca 0.5 /BM /Multiply >>",
	}
	c := "/Sh0 sh q /G0 gs 50 0 0 50 0 0 cm /Sh1 sh Q q 0 0 m 200 100 l 200 0 l W n /Sh1 sh Q"
	data := buildPDF([]string{c}, "/Resources << /Shading << /Sh0 100 0 R /Sh1 101 0 R >> /ExtGState << /G0 102 0 R >> >>", objs...)
	doc, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := doc.Page(0)
	b := p.Bounds(3)
	one := image.NewRGBA(b)
	if err := p.Render(context.Background(), one, RenderOptions{Scale: 3, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	many := image.NewRGBA(b)
	if err := p.Render(context.Background(), many, RenderOptions{Scale: 3, Workers: 4}); err != nil {
		t.Fatal(err)
	}
	tiles := image.NewRGBA(b)
	for y := 0; y < b.Dy(); y += 70 {
		for x := 0; x < b.Dx(); x += 130 {
			tile := tiles.SubImage(image.Rect(x, y, x+130, y+70)).(*image.RGBA)
			if err := p.Render(context.Background(), tile, RenderOptions{Scale: 3, Workers: 1}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := range one.Pix {
		if d := int(one.Pix[i]) - int(many.Pix[i]); d > 1 || d < -1 {
			t.Fatalf("workers differ at byte %d: %d vs %d", i, one.Pix[i], many.Pix[i])
		}
		if d := int(one.Pix[i]) - int(tiles.Pix[i]); d > 1 || d < -1 {
			t.Fatalf("tiles differ at byte %d: %d vs %d", i, one.Pix[i], tiles.Pix[i])
		}
	}
	if raceEnabled {
		return
	}
	allocs := testing.AllocsPerRun(10, func() {
		if err := p.Render(context.Background(), one, RenderOptions{Scale: 3, Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 1 {
		t.Errorf("%v allocations per cached render", allocs)
	}
}

func TestSeparationAndDeviceN(t *testing.T) {
	// A spot colour through a tint transform into CMYK, a DeviceN of two
	// inks into RGB, a separation /None and an Indexed space on a
	// separation.
	spot := "[/Separation /Orange /DeviceCMYK << /FunctionType 2 /Domain [0 1] /C0 [0 0 0 0] /C1 [0 0.5 1 0] /N 1 >>]"
	dn := "[/DeviceN [/A /B] /DeviceRGB 102 0 R]"
	fn := streamObj("/FunctionType 4 /Domain [0 1 0 1] /Range [0 1 0 1 0 1]", []byte("{ 0 }"))
	c := "/CS0 cs 1 scn 0 0 50 100 re f 0.5 scn 50 0 50 100 re f /CS1 cs 1 0.5 scn 100 0 50 100 re f /CS2 cs 1 scn 150 0 25 100 re f /CS3 cs 1 scn 175 0 25 100 re f"
	img, _ := renderShaded(t, c, "/ColorSpace << /CS0 100 0 R /CS1 101 0 R /CS2 [/Separation /None /DeviceGray 103 0 R] /CS3 [/Indexed 100 0 R 1 <00ff>] >>",
		spot, dn, fn, "<< /FunctionType 2 /Domain [0 1] /C0 [1] /C1 [0] >>")
	assertNear(t, img, 25, 50, rgba(255, 128, 0, 255), 1)
	assertNear(t, img, 75, 50, rgba(255, 191, 128, 255), 1)
	assertNear(t, img, 125, 50, rgba(255, 128, 0, 255), 1) // R = 1, G = 0.5, B = 0
	assertNear(t, img, 160, 50, white, 0)
	assertNear(t, img, 185, 50, rgba(255, 128, 0, 255), 1)
}

func TestCIESpaces(t *testing.T) {
	// Lab white and mid grey, CalRGB with the sRGB primaries and gamma 1
	// (so 0.5 is linear mid grey), CalGray with gamma 2.2 (close to the
	// sRGB curve).
	lab := "[/Lab << /WhitePoint [0.9505 1 1.089] >>]"
	cal := "[/CalRGB << /WhitePoint [0.9505 1 1.089] /Matrix [0.4124 0.2126 0.0193 0.3576 0.7152 0.1192 0.1805 0.0722 0.9505] >>]"
	gray := "[/CalGray << /WhitePoint [0.9505 1 1.089] /Gamma 2.2 >>]"
	c := "/L cs 100 0 0 sc 0 0 40 100 re f 53.39 0 0 sc 40 0 40 100 re f /C cs 0.5 0.5 0.5 sc 80 0 40 100 re f 1 0 0 sc 120 0 40 100 re f /G cs 0.5 sc 160 0 40 100 re f"
	img, _ := renderShaded(t, c, "/ColorSpace << /L "+lab+" /C "+cal+" /G "+gray+" >>")
	assertNear(t, img, 20, 50, white, 2)
	assertNear(t, img, 60, 50, rgba(128, 128, 128, 255), 2)
	assertNear(t, img, 100, 50, rgba(188, 188, 188, 255), 2)
	assertNear(t, img, 140, 50, rgba(255, 0, 0, 255), 2)
	assertNear(t, img, 180, 50, rgba(128, 128, 128, 255), 2) // 0.5^2.2 is about sRGB 0.5
}

// iccBytes makes a matrix/TRC RGB profile with the sRGB primaries and the
// given gamma.
func iccBytes(gamma float64) []byte {
	tag := func(sig string, data []byte) [2][]byte { return [2][]byte{[]byte(sig), data} }
	xyz := func(x, y, z float64) []byte {
		b := []byte("XYZ \x00\x00\x00\x00")
		for _, v := range []float64{x, y, z} {
			b = binary.BigEndian.AppendUint32(b, uint32(int32(math.Round(v*65536))))
		}
		return b
	}
	curv := []byte("curv\x00\x00\x00\x00\x00\x00\x00\x01")
	curv = binary.BigEndian.AppendUint16(curv, uint16(math.Round(gamma*256)))
	tags := [][2][]byte{
		tag("rXYZ", xyz(0.4361, 0.2225, 0.0139)), tag("gXYZ", xyz(0.3851, 0.7169, 0.0971)),
		tag("bXYZ", xyz(0.1431, 0.0606, 0.7141)),
		tag("rTRC", curv), tag("gTRC", curv), tag("bTRC", curv),
	}
	head := make([]byte, 128)
	copy(head[12:], "mntr")
	copy(head[16:], "RGB ")
	copy(head[20:], "XYZ ")
	out := binary.BigEndian.AppendUint32(head, uint32(len(tags)))
	off := 132 + 12*len(tags)
	var data []byte
	for _, t := range tags {
		out = append(out, t[0]...)
		out = binary.BigEndian.AppendUint32(out, uint32(off+len(data)))
		out = binary.BigEndian.AppendUint32(out, uint32(len(t[1])))
		data = append(data, t[1]...)
		for len(data)%4 != 0 {
			data = append(data, 0)
		}
	}
	return append(out, data...)
}

func TestICCProfiles(t *testing.T) {
	if p, srgb := iccProfile(iccBytes(2.2), 3); p == nil || !srgb {
		t.Error("a gamma 2.2 profile with sRGB primaries is not taken as sRGB")
	}
	p, srgb := iccProfile(iccBytes(1.0), 3)
	if p == nil || srgb {
		t.Fatal("a linear profile is taken as sRGB")
	}
	if r, g, b := p.rgb([]float64{0.5, 0.5, 0.5}); math.Abs(r-0.735) > 0.01 || math.Abs(g-r) > 0.01 || math.Abs(b-r) > 0.01 {
		t.Errorf("linear mid grey: %v %v %v", r, g, b)
	}
	if r, g, b := p.rgb8(255, 0, 0); r != 255 || g > 2 || b > 2 {
		t.Errorf("red: %d %d %d", r, g, b)
	}
	if p, _ := iccProfile([]byte("short"), 3); p != nil {
		t.Error("a broken profile is read")
	}
	// Drawn: fills and an image in the linear profile.
	icc := streamObj("/N 3", iccBytes(1.0))
	im := streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace [/ICCBased 100 0 R] /BitsPerComponent 8", []byte{128, 128, 128})
	c := "/C cs 0.5 0.5 0.5 sc 0 0 100 100 re f q 100 0 0 100 100 0 cm /Im0 Do Q"
	img, _ := renderShaded(t, c, "/ColorSpace << /C [/ICCBased 100 0 R] >> /XObject << /Im0 101 0 R >>", icc, im)
	assertNear(t, img, 50, 50, rgba(188, 188, 188, 255), 2)
	assertNear(t, img, 150, 50, rgba(188, 188, 188, 255), 2)
}

func TestBrokenShadings(t *testing.T) {
	for _, sh := range []string{
		"<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 1] /Function " + redToBlue + " >>",
		"<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 0 0] /Function " + redToBlue + " >>",
		"<< /ShadingType 9 /ColorSpace /DeviceRGB >>",
		"<< /ShadingType 3 /ColorSpace /Pattern /Coords [0 0 1 0 0 2] /Function " + redToBlue + " >>",
		streamObj("/ShadingType 4 "+meshDict, []byte{0, 1, 2}),
		streamObj("/ShadingType 6 /BitsPerCoordinate 7 /BitsPerComponent 8 /BitsPerFlag 8 /Decode [0 1 0 1 0 1 0 1 0 1] /ColorSpace /DeviceRGB", []byte{0}),
		streamObj("/ShadingType 6 "+meshDict, []byte{1, 2, 3, 4, 5, 6, 7, 8}), // continues a missing patch
	} {
		data := buildPDF([]string{"/Sh0 sh 0 0 1 rg 0 0 10 10 re f"}, "/Resources << /Shading << /Sh0 100 0 R >> >>", sh)
		img, _, err := renderPage(t, data, 0, RenderOptions{Background: white})
		if err != nil {
			t.Errorf("%s: %v", sh, err)
		}
		assertNear(t, img, 5, 95, rgba(0, 0, 255, 255), 0)
	}
}

func FuzzShading(f *testing.F) {
	var m meshData
	m.flag(0)
	m.pt(20, 10)
	m.col(255, 0, 0)
	m.flag(0)
	m.pt(180, 10)
	m.col(0, 255, 0)
	m.flag(0)
	m.pt(20, 90)
	m.col(0, 0, 255)
	m.flag(2)
	m.pt(180, 90)
	m.col(0, 0, 255)
	for _, s := range []string{
		"/Sh0 sh /Sh1 sh /Sh2 sh /Sh3 sh",
		"/Pattern cs /P0 scn 0 0 200 100 re f /Pattern CS /P0 SCN 9 w 0 0 m 200 100 l S",
		"q 0 0 50 50 re W n /G gs /Sh1 sh Q BT /F1 40 Tf /Pattern cs /P0 scn 7 Tr (Hi) Tj ET",
		"q 1e30 0 0 1 0 0 cm /Sh0 sh Q /S cs 0.5 scn 0 0 10 10 re f",
	} {
		f.Add(s, m.Bytes())
	}
	f.Fuzz(func(t *testing.T, c string, mesh []byte) {
		res := "/Shading << /Sh0 101 0 R /Sh1 102 0 R /Sh2 103 0 R /Sh3 105 0 R >> /Pattern << /P0 104 0 R >>" +
			" /ExtGState << /G << /BM /Screen /ca 0.5 >> >> /Font << /F1 100 0 R >>" +
			" /ColorSpace << /S [/Separation /X /DeviceRGB " + redToBlue + "] >>"
		data := buildPDF([]string{c}, "/Resources << "+res+" >>", helvetica,
			streamObj("/ShadingType 4 "+meshDict, mesh),
			streamObj("/ShadingType 6 "+meshDict, mesh),
			streamObj("/ShadingType 5 /VerticesPerRow 2 /BitsPerCoordinate 16 /BitsPerComponent 8 /Decode [0 200 0 100 0 1] /ColorSpace /DeviceRGB /Function "+redToBlue, mesh),
			"<< /PatternType 2 /Shading << /ShadingType 3 /ColorSpace /DeviceRGB /Coords [100 50 0 120 50 60] /Extend [true false] /Function "+redToBlue+" >> >>",
			streamObj("/ShadingType 7 "+meshDict, mesh))
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
			err = p.Render(context.Background(), dst, RenderOptions{Scale: 0.5, Workers: workers, Deadline: time.Now().Add(2 * time.Second)})
			var pe *PanicError
			if errors.As(err, &pe) {
				t.Fatalf("%v\n%s", pe.Value, pe.Stack)
			}
		}
	})
}
