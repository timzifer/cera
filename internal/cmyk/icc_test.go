package cmyk

import (
	"bytes"
	"encoding/binary"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the profiles in testdata")

// The profiles in testdata are made by the builders below. swop-lut16.icc
// and swop-mab.icc carry the table of swop.bin (CGATS TR 005, see gen),
// one as an ICC v2 lut16Type, one as an ICC v4 lutAtoBType through every
// stage of it; their B2A0 is a small table that sends Lab black to
// 80/80/80/100 ink. toy-lut16.icc is a grid of two nodes, unlike any
// press.
var testProfiles = map[string]func() []byte{
	"swop-lut16.icc": swopLut16,
	"swop-mab.icc":   swopMAB,
	"toy-lut16.icc":  toyLut16,
}

func TestTestdataProfiles(t *testing.T) {
	for name, build := range testProfiles {
		path := filepath.Join("testdata", name)
		b := build()
		if *update {
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		have, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(have, b) {
			t.Errorf("%s is not what its builder makes; run go test -update", name)
		}
	}
}

// TestProfilePatches converts the flat inks of #20 through the testdata
// profiles; want is Little CMS 2.18 with the same file to sRGB, relative
// colorimetric with black point compensation (Pillow's ImageCms, which is
// transicc -t 1 -b).
func TestProfilePatches(t *testing.T) {
	inks := [][4]uint8{
		{255, 0, 0, 0}, {0, 255, 0, 0}, {0, 0, 255, 0}, {0, 0, 0, 255},
		{128, 0, 0, 0}, {0, 128, 128, 0}, {255, 255, 0, 0}, {0, 0, 0, 128},
		{0, 0, 0, 0}, {255, 255, 255, 255}, {204, 141, 0, 0}, {59, 5, 0, 196},
	}
	want := map[string][][3]uint8{
		"swop-lut16.icc": {
			{0, 174, 240}, {236, 15, 141}, {255, 242, 0}, {44, 41, 42},
			{118, 208, 246}, {245, 152, 125}, {56, 54, 148}, {150, 152, 155},
			{255, 255, 255}, {0, 0, 4}, {72, 114, 184}, {75, 88, 97},
		},
		"swop-mab.icc": {
			{0, 174, 240}, {236, 9, 141}, {255, 242, 0}, {42, 39, 40},
			{117, 208, 246}, {245, 151, 125}, {54, 52, 147}, {150, 152, 155},
			{255, 255, 255}, {0, 0, 0}, {71, 113, 184}, {74, 87, 97},
		},
		"toy-lut16.icc": {
			{0, 192, 252}, {234, 89, 155}, {233, 213, 19}, {79, 79, 79},
			{135, 217, 247}, {238, 156, 108}, {67, 70, 162}, {156, 156, 156},
			{240, 240, 240}, {12, 0, 0}, {96, 133, 200}, {87, 100, 106},
		},
	}
	for name, rgbs := range want {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		tab, err := Load(b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for i, c := range inks {
			r, g, bl := tab.RGB8(c[0], c[1], c[2], c[3])
			got := [3]uint8{r, g, bl}
			for k := range got {
				if math.Abs(float64(got[k])-float64(rgbs[i][k])) > 2 {
					t.Errorf("%s %v: %v, want %v", name, c, got, rgbs[i])
					break
				}
			}
		}
	}
}

func TestLoadShares(t *testing.T) {
	b := swopLut16()
	t1, err := Load(b)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := Load(bytes.Clone(b))
	if err != nil {
		t.Fatal(err)
	}
	if t1 != t2 {
		t.Error("the same profile loaded twice is tabulated twice")
	}
}

func TestParseRejects(t *testing.T) {
	good := swopLut16()
	rgb := bytes.Clone(good)
	copy(rgb[16:], "RGB ")
	noA2B := bytes.Clone(good)
	for i := range 4 {
		copy(noA2B[132+12*i:], "zzz"+string(rune('0'+i)))
	}
	for name, b := range map[string][]byte{
		"empty":     nil,
		"short":     good[:131],
		"rgb":       rgb,
		"no tables": noA2B,
		"truncated": good[:len(good)/2],
		"garbage":   bytes.Repeat([]byte("acsp"), 100),
	} {
		if _, err := Parse(b); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	// Every truncation of a good profile fails cleanly, without a panic.
	for n := range len(good) {
		Parse(good[:n])
	}
}

// The stages of swop-mab.icc must give the colours the lut16 table gives
// (their black points differ: Little CMS finds a v4 profile's through
// the perceptual black of ICC v4).
func TestLutAtoBMatchesLut16(t *testing.T) {
	a, err := Parse(swopLut16())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(swopMAB())
	if err != nil {
		t.Fatal(err)
	}
	if a.PCS != LabV2 || b.PCS != LabV4 {
		t.Fatalf("encodings %v and %v", a.PCS, b.PCS)
	}
	for i := range 200 {
		c := []float64{float64(i%7) / 6, float64(i%5) / 4, float64(i%11) / 10, float64(i%3) / 2}
		var va, vb [3]float64
		a.A2B.Eval(c, va[:])
		b.A2B.Eval(c, vb[:])
		xa, xb := a.PCS.XYZOf(va[:]), b.PCS.XYZOf(vb[:])
		for k := range xa {
			if math.Abs(xa[k]-xb[k]) > 1e-4 {
				t.Errorf("%v: XYZ %v and %v", c, xa, xb)
				break
			}
		}
	}
	if !(b.Black[1] > a.Black[1]) {
		t.Errorf("black points %v (v2) and %v (v4)", a.Black, b.Black)
	}
}

// iccFile writes a CMYK printer profile of the given version and tags.
func iccFile(version uint32, tags [][2]any) []byte {
	var body bytes.Buffer
	type entry struct {
		sig       string
		off, size int
	}
	var table []entry
	at := 128 + 4 + 12*len(tags)
	for i, tg := range tags {
		data := tg[1].([]byte)
		if i > 0 && bytes.Equal(data, tags[i-1][1].([]byte)) {
			// The same data, shared, as ICC allows.
			table = append(table, entry{tg[0].(string), table[i-1].off, len(data)})
			continue
		}
		for body.Len()%4 != 0 {
			body.WriteByte(0)
		}
		table = append(table, entry{tg[0].(string), at + body.Len(), len(data)})
		body.Write(data)
	}
	h := make([]byte, 128)
	binary.BigEndian.PutUint32(h[8:], version)
	copy(h[12:], "prtr")
	copy(h[16:], "CMYK")
	copy(h[20:], "Lab ")
	binary.BigEndian.PutUint16(h[24:], 2026)
	binary.BigEndian.PutUint16(h[26:], 10)
	binary.BigEndian.PutUint16(h[28:], 5)
	copy(h[36:], "acsp")
	binary.BigEndian.PutUint32(h[64:], 1) // relative colorimetric
	putXYZ(h[68:], D50)
	out := bytes.NewBuffer(h)
	binary.Write(out, binary.BigEndian, uint32(len(table)))
	for _, e := range table {
		out.WriteString(e.sig)
		binary.Write(out, binary.BigEndian, uint32(e.off))
		binary.Write(out, binary.BigEndian, uint32(e.size))
	}
	out.Write(body.Bytes())
	b := out.Bytes()
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	return b
}

func s15f16(v float64) uint32 { return uint32(int32(math.Round(v * 65536))) }

func putXYZ(b []byte, v [3]float64) {
	for i := range 3 {
		binary.BigEndian.PutUint32(b[4*i:], s15f16(v[i]))
	}
}

func xyzTag(v [3]float64) []byte {
	b := make([]byte, 20)
	copy(b, "XYZ ")
	putXYZ(b[8:], v)
	return b
}

func u16(v float64) uint16 { return uint16(math.Round(min(max(v, 0), 1) * 65535)) }

// mft2 writes a lut16Type with an identity matrix.
func mft2(t *Lut) []byte {
	b := make([]byte, 52)
	copy(b, "mft2")
	b[8], b[9], b[10] = byte(t.In), byte(t.Out), byte(t.Grid)
	for i := range 3 {
		binary.BigEndian.PutUint32(b[12+16*i:], s15f16(1))
	}
	binary.BigEndian.PutUint16(b[48:], uint16(len(t.InCurves[0])))
	binary.BigEndian.PutUint16(b[50:], uint16(len(t.OutCurves[0])))
	put := func(vs []float64) {
		for _, v := range vs {
			b = binary.BigEndian.AppendUint16(b, u16(v))
		}
	}
	for _, c := range t.InCurves {
		put(c)
	}
	put(t.Table)
	for _, c := range t.OutCurves {
		put(c)
	}
	return b
}

// curvTag writes a curveType of samples, or of n = 0 (identity) for nil.
func curvTag(c []float64) []byte {
	b := make([]byte, 12)
	copy(b, "curv")
	binary.BigEndian.PutUint32(b[8:], uint32(len(c)))
	for _, v := range c {
		b = binary.BigEndian.AppendUint16(b, u16(v))
	}
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// lutAB writes a lutAtoBType (or lutBtoAType with b2a) of the elements
// given: curves (nil for none, a nil curve for identity), a matrix (nil
// for none) and a CLUT with 16-bit entries.
func lutAB(b2a bool, in, out int, B, M, A [][]float64, mat []float64, clut *Lut) []byte {
	b := make([]byte, 32)
	copy(b, "mAB ")
	if b2a {
		copy(b, "mBA ")
	}
	b[8], b[9] = byte(in), byte(out)
	setOff := func(i int) { binary.BigEndian.PutUint32(b[12+4*i:], uint32(len(b))) }
	curves := func(i int, cs [][]float64) {
		if cs == nil {
			return
		}
		setOff(i)
		for _, c := range cs {
			b = append(b, curvTag(c)...)
		}
	}
	curves(0, B)
	if mat != nil {
		setOff(1)
		for _, v := range mat {
			b = binary.BigEndian.AppendUint32(b, s15f16(v))
		}
	}
	curves(2, M)
	setOff(3)
	g := make([]byte, 20)
	for i := range clut.In {
		g[i] = byte(clut.Grid)
	}
	g[16] = 2
	b = append(b, g...)
	for _, v := range clut.Table {
		b = binary.BigEndian.AppendUint16(b, u16(v))
	}
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	curves(4, A)
	return b
}

// b2aGrid is the CLUT of the B2A0 tables of the test profiles: 80/80/80/100
// ink at L* = 0, falling to none at L* = 100, whatever a* and b*.
func b2aGrid() *Lut {
	t := &Lut{In: 3, Out: 4, Grid: 2}
	for L := range 2 {
		for range 4 { // a*, b* corners
			ink := [4]float64{0.8, 0.8, 0.8, 1}
			if L == 1 {
				ink = [4]float64{}
			}
			t.Table = append(t.Table, ink[:]...)
		}
	}
	return t
}

func identityCurves(n int) [][]float64 {
	cs := make([][]float64, n)
	for i := range cs {
		cs[i] = []float64{0, 1}
	}
	return cs
}

func swopLut16() []byte {
	a2b := SWOP().A2B.(*Lut)
	b2a := b2aGrid()
	b2a.InCurves, b2a.OutCurves = identityCurves(3), identityCurves(4)
	return iccFile(0x02100000, [][2]any{
		{"wtpt", xyzTag(D50)},
		{"A2B0", mft2(a2b)},
		{"A2B1", mft2(a2b)},
		{"B2A0", mft2(b2a)},
	})
}

func swopMAB() []byte {
	a2b := SWOP().A2B.(*Lut)
	clut := &Lut{In: 4, Out: 3, Grid: a2b.Grid, Table: a2b.Table}
	// The lut16 output curves work on the legacy encoding; the matrix
	// takes it to the v4 one, a scale of 65535/65280 for L*, a* and b*.
	s := 65535.0 / 65280
	mat := []float64{s, 0, 0, 0, s, 0, 0, 0, s, 0, 0, 0}
	nilCurves := [][]float64{nil, nil, nil}
	a2bTag := lutAB(false, 4, 3, nilCurves, a2b.OutCurves, a2b.InCurves, mat, clut)
	b2aTag := lutAB(true, 3, 4, nilCurves, nil, [][]float64{nil, nil, nil, nil}, nil, b2aGrid())
	return iccFile(0x04300000, [][2]any{
		{"wtpt", xyzTag(D50)},
		{"A2B0", a2bTag},
		{"A2B1", a2bTag},
		{"B2A0", b2aTag},
	})
}

// toyLut16 is a grid of two nodes from Lab of each corner: cyan, magenta
// and yellow take away from L* and push a* and b*, black darkens.
func toyLut16() []byte {
	t := &Lut{In: 4, Out: 3, Grid: 2, InCurves: identityCurves(4), OutCurves: identityCurves(3)}
	for c := range 2 {
		for m := range 2 {
			for y := range 2 {
				for k := range 2 {
					L := 95 - 25*float64(c) - 35*float64(m) - 10*float64(y) - 60*float64(k)
					a := -40*float64(c) + 60*float64(m) - 5*float64(y)
					bb := -45*float64(c) - 5*float64(m) + 80*float64(y)
					e := EncodeLab(max(L, 5), a*(1-0.7*float64(k)), bb*(1-0.7*float64(k)))
					t.Table = append(t.Table, e[:]...)
				}
			}
		}
	}
	b2a := b2aGrid()
	b2a.InCurves, b2a.OutCurves = identityCurves(3), identityCurves(4)
	return iccFile(0x02100000, [][2]any{
		{"wtpt", xyzTag(D50)},
		{"A2B0", mft2(t)},
		{"A2B1", mft2(t)},
		{"B2A0", mft2(b2a)},
	})
}

func FuzzParse(f *testing.F) {
	for name := range testProfiles {
		if b, err := os.ReadFile(filepath.Join("testdata", name)); err == nil {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := Parse(b)
		if err != nil {
			return
		}
		for _, c := range [][4]float64{{0, 0, 0, 0}, {1, 1, 1, 1}, {0.3, 0.6, 0.1, 0.9}, {math.NaN(), -1, 2, 0.5}} {
			r, g, bl := p.RGB(c)
			for _, v := range [3]float64{r, g, bl} {
				if !(v >= 0 && v <= 1) {
					t.Fatalf("%v: %v %v %v", c, r, g, bl)
				}
			}
		}
	})
}
