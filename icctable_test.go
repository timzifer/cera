package cera

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"testing"
)

// tableProfile makes an ICC profile built from a lookup table, as a
// scanner has one: of n device components (1 or 3), with a table of the
// type given ("mft2", a lut16Type, or "mAB ", a lutAtoBType) of two nodes
// along each. The table is the linear map that iccBytes(1) says with
// curves and a matrix — the sRGB primaries, or for a grey their sum — to a
// connection space in XYZ, or with lab a grey axis in Lab.
func tableProfile(typ string, n int, lab bool) []byte {
	u16 := func(v float64) []byte {
		return binary.BigEndian.AppendUint16(nil, uint16(math.Round(min(max(v, 0), 65535))))
	}
	prim := [3][3]float64{{0.4361, 0.2225, 0.0139}, {0.3851, 0.7169, 0.0971}, {0.1431, 0.0606, 0.7141}}
	// The nodes: the first component slowest.
	var clut []byte
	for i := range 1 << n {
		var xyz [3]float64
		for k := range n {
			if i>>(n-1-k)&1 == 0 {
				continue
			}
			for c := range 3 {
				if n == 1 {
					xyz[c] = prim[0][c] + prim[1][c] + prim[2][c]
				} else {
					xyz[c] += prim[k][c]
				}
			}
		}
		for c := range 3 {
			if lab {
				// The Lab of lut16: L* over 0 to 0xFF00, a* and b* 0 at 0x8000.
				clut = append(clut, u16([3]float64{float64(i) * 0xFF00, 0x8000, 0x8000}[c])...)
			} else {
				clut = append(clut, u16(xyz[c]*32768)...)
			}
		}
	}
	var tag []byte
	switch typ {
	case "mft2":
		tag = append([]byte("mft2\x00\x00\x00\x00"), byte(n), 3, 2, 0)
		for i := range 9 { // the matrix, which a device does not go through
			tag = binary.BigEndian.AppendUint32(tag, uint32(65536*b2i(i%4 == 0)))
		}
		tag = append(tag, 0, 2, 0, 2) // two entries in each curve
		for range n {
			tag = append(tag, 0, 0, 0xff, 0xff)
		}
		tag = append(tag, clut...)
		for range 3 {
			tag = append(tag, 0, 0, 0xff, 0xff)
		}
	case "mAB ":
		tag = append([]byte("mAB \x00\x00\x00\x00"), byte(n), 3, 0, 0)
		for _, off := range []uint32{32, 0, 0, 68, 0} { // B curves, matrix, M curves, CLUT, A curves
			tag = binary.BigEndian.AppendUint32(tag, off)
		}
		for range 3 {
			tag = append(tag, "curv\x00\x00\x00\x00\x00\x00\x00\x00"...)
		}
		grid := make([]byte, 16)
		for k := range n {
			grid[k] = 2
		}
		tag = append(append(tag, grid...), 2, 0, 0, 0)
		tag = append(tag, clut...)
	}
	head := make([]byte, 128)
	head[8] = 2
	if typ == "mAB " {
		head[8] = 4
	}
	copy(head[12:], "scnr")
	copy(head[16:], map[int]string{1: "GRAY", 3: "RGB "}[n])
	copy(head[20:], "XYZ ")
	if lab {
		copy(head[20:], "Lab ")
	}
	copy(head[36:], "acsp")
	out := binary.BigEndian.AppendUint32(head, 1)
	out = append(out, "A2B0"...)
	out = binary.BigEndian.AppendUint32(out, 144)
	out = binary.BigEndian.AppendUint32(out, uint32(len(tag)))
	out = append(out, tag...)
	binary.BigEndian.PutUint32(out, uint32(len(out)))
	return out
}

// TestICCTables reads grey and RGB profiles built from lookup tables: they
// convert as the profile of curves and a matrix that says the same.
func TestICCTables(t *testing.T) {
	linear, _ := iccProfile(iccBytes(1), 3)
	for _, typ := range []string{"mft2", "mAB "} {
		p, srgb := iccProfile(tableProfile(typ, 3, false), 3)
		if p == nil || p.table == nil || srgb {
			t.Fatalf("%s: the profile reads as %v, sRGB %v", typ, p, srgb)
		}
		for _, v := range [][]float64{{0, 0, 0}, {1, 1, 1}, {0.5, 0.5, 0.5}, {1, 0, 0}, {0.2, 0.7, 0.4}, {0.03, 0.98, 0.5}, {2, -1, math.NaN()}} {
			r, g, b := p.rgb(v)
			wr, wg, wb := linear.rgb([]float64{min(max(v[0], 0), 1), min(max(v[1], 0), 1), max(min(v[2], 1), 0)})
			if math.Abs(r-wr) > 0.004 || math.Abs(g-wg) > 0.004 || math.Abs(b-wb) > 0.004 {
				t.Errorf("%s: %v converts to %.3f %.3f %.3f, want %.3f %.3f %.3f", typ, v, r, g, b, wr, wg, wb)
			}
		}
		if r, g, b := p.rgb8(128, 128, 128); r < 186 || r > 190 || g != r || b != r {
			t.Errorf("%s: mid grey is %d %d %d", typ, r, g, b)
		}
		// A grey: its white is the white, half of it is half the light.
		g, _ := iccProfile(tableProfile(typ, 1, false), 1)
		if g == nil || g.table == nil {
			t.Fatalf("%s: the grey profile is not read", typ)
		}
		if r, gg, b := g.rgb([]float64{0.5}); math.Abs(r-0.735) > 0.01 || math.Abs(gg-r) > 0.01 || math.Abs(b-r) > 0.01 {
			t.Errorf("%s: half a grey is %.3f %.3f %.3f", typ, r, gg, b)
		}
		if r, _, _ := g.rgb([]float64{1}); math.Abs(r-1) > 0.005 {
			t.Errorf("%s: the white of a grey is %.3f", typ, r)
		}
	}
	// A connection space in Lab: L* 50 is the grey 119.
	lab, _ := iccProfile(tableProfile("mft2", 1, true), 1)
	if lab == nil {
		t.Fatal("the grey profile in Lab is not read")
	}
	if r, g, b := lab.rgb([]float64{0.5}); math.Abs(r-119./255) > 0.01 || math.Abs(g-r) > 0.005 || math.Abs(b-r) > 0.005 {
		t.Errorf("L* 50 is %.3f %.3f %.3f", r, g, b)
	}

	// Not read: a table cut short, the wrong number of components, a
	// profile of another device.
	whole := tableProfile("mft2", 3, false)
	for name, c := range map[string]struct {
		data []byte
		n    int
	}{
		"cut short":    {whole[:len(whole)-20], 3},
		"three as one": {whole, 1},
		"no table":     {bytes.Replace(whole, []byte("A2B0"), []byte("XXXX"), 1), 3},
		"no signature": {bytes.Replace(whole, []byte("acsp"), []byte("xxxx"), 1), 3},
		"another type": {bytes.Replace(whole, []byte("mft2"), []byte("mpet"), 1), 3},
	} {
		if p, _ := iccProfile(c.data, c.n); p != nil {
			t.Errorf("%s: read", name)
		}
	}

	// Drawn: a fill and an image, as with the profile of curves.
	icc := streamObj("/N 3", tableProfile("mft2", 3, false))
	im := streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace [/ICCBased 100 0 R] /BitsPerComponent 8", []byte{128, 128, 128})
	c := "/C cs 0.5 0.5 0.5 sc 0 0 100 100 re f q 100 0 0 100 100 0 cm /Im0 Do Q"
	res := "/ColorSpace << /C [/ICCBased 100 0 R] >> /XObject << /Im0 101 0 R >>"
	img, st := renderShaded(t, c, res, icc, im)
	assertNear(t, img, 50, 50, rgba(188, 188, 188, 255), 2)
	assertNear(t, img, 150, 50, rgba(188, 188, 188, 255), 2)
	if len(st.Unsupported) != 0 {
		t.Errorf("unsupported: %v", st.Unsupported)
	}
	// A profile that does not read: the device space, and counted.
	img, st, err := renderPage(t, buildPDF([]string{c}, "/Resources << "+res+" >>", streamObj("/N 3", whole[:len(whole)-20]), im), 0,
		RenderOptions{Background: white, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, img, 50, 50, rgba(128, 128, 128, 255), 1)
	assertNear(t, img, 150, 50, rgba(128, 128, 128, 255), 1)
	if st.Unsupported["icc-profile"] == 0 {
		t.Errorf("unsupported: %v, want icc-profile", st.Unsupported)
	}
}

// TestICCTableBudget converts the spaces of more profiles built from
// tables than maxICCTables: the further ones as the device space, counted.
func TestICCTableBudget(t *testing.T) {
	var cs, content strings.Builder
	var objs []string
	for i := range maxICCTables + 2 {
		// Profiles that differ only in their header's creator field.
		p := tableProfile("mft2", 3, false)
		binary.BigEndian.PutUint32(p[80:], uint32(i))
		objs = append(objs, streamObj("/N 3", p))
		fmt.Fprintf(&cs, "/C%d [/ICCBased %d 0 R] ", i, 100+i)
		// Twice each: the second use finds the table made for the first.
		fmt.Fprintf(&content, "/C%d cs 0.5 0.5 0.5 scn %d 0 5 100 re f /C%d cs 0.5 0.5 0.5 scn %d 0 5 100 re f\n", i, 10*i, i, 10*i+5)
	}
	data := buildPDF([]string{content.String()}, "/Resources << /ColorSpace << "+cs.String()+">> >>", objs...)
	img, st, err := renderPage(t, data, 0, RenderOptions{Background: white})
	if err != nil {
		t.Fatal(err)
	}
	if st.Unsupported["icc-budget"] == 0 || st.Unsupported["icc-profile"] != 0 {
		t.Errorf("unsupported %v, want icc-budget", st.Unsupported)
	}
	assertNear(t, img, 2, 50, rgba(188, 188, 188, 255), 2)                     // through its table
	assertNear(t, img, 7, 50, rgba(188, 188, 188, 255), 2)                     // and again
	assertNear(t, img, 10*(maxICCTables+1)+2, 50, rgba(128, 128, 128, 255), 1) // as the device space
}
