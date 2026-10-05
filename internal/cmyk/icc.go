package cmyk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Transform maps the inputs of an ICC table to its outputs, each in
// [0, 1].
type Transform interface {
	Eval(in, out []float64)
}

// PCS is how a table encodes the profile connection space in [0, 1].
type PCS uint8

const (
	// LabV2 is the legacy 16-bit Lab of lut16 tables: L* 0..100 over
	// 0..0xFF00, a* and b* -128..128 over 0..0xFFFF+1.
	LabV2 PCS = iota
	// LabV4 is the Lab of ICC v4 and of lut8 tables: L* 0..100, a* and b*
	// -128..127, each over [0, 1].
	LabV4
	// XYZ is XYZ (D50) as u1Fixed15: 0..1+32767/32768 over 0..0xFFFF.
	XYZ
)

// XYZOf converts what a table gives in encoding e to XYZ (D50).
func (e PCS) XYZOf(v []float64) [3]float64 {
	switch e {
	case LabV4:
		return LabToXYZ(v[0]*100, v[1]*255-128, v[2]*255-128)
	case XYZ:
		const s = 65535.0 / 32768
		return [3]float64{v[0] * s, v[1] * s, v[2] * s}
	}
	return LabToXYZ(DecodeLab(v))
}

// encodeLab encodes L*, a*, b* as a table of encoding e takes them.
func (e PCS) encodeLab(L, a, b float64) [3]float64 {
	switch e {
	case LabV4:
		return [3]float64{L / 100, (a + 128) / 255, (b + 128) / 255}
	case XYZ:
		xyz := LabToXYZ(L, a, b)
		const s = 32768.0 / 65535
		return [3]float64{xyz[0] * s, xyz[1] * s, xyz[2] * s}
	}
	return EncodeLab(L, a, b)
}

// Parse reads a CMYK ICC profile (v2 or v4) whose device to PCS tables
// are lut8Type, lut16Type or lutAtoBType, with a Lab or XYZ connection
// space, for the relative colorimetric intent: its A2B1 table (A2B0 if it
// has none) and the black point Little CMS finds for that intent, to
// convert with black point compensation as transicc -t 1 -b does.
func Parse(b []byte) (*Profile, error) {
	if len(b) < 132 {
		return nil, errors.New("cmyk: not an ICC profile")
	}
	if string(b[36:40]) != "acsp" {
		return nil, errors.New("cmyk: not an ICC profile")
	}
	if string(b[16:20]) != "CMYK" {
		return nil, fmt.Errorf("cmyk: a profile of %q, not CMYK", b[16:20])
	}
	var pcs PCS
	switch string(b[20:24]) {
	case "Lab ":
	case "XYZ ":
		pcs = XYZ
	default:
		return nil, fmt.Errorf("cmyk: connection space %q", b[20:24])
	}
	tags := map[string][]byte{}
	n := int(binary.BigEndian.Uint32(b[128:]))
	if n > (len(b)-132)/12 {
		return nil, errors.New("cmyk: truncated tag table")
	}
	for i := range n {
		e := b[132+12*i:]
		off, size := int(binary.BigEndian.Uint32(e[4:])), int(binary.BigEndian.Uint32(e[8:]))
		if off < 0 || size < 0 || off > len(b) || size > len(b)-off {
			return nil, fmt.Errorf("cmyk: tag %q out of range", e[:4])
		}
		tags[string(e[:4])] = b[off : off+size]
	}
	tag := "A2B1"
	if _, ok := tags[tag]; !ok {
		tag = "A2B0"
	}
	a2b, enc, err := readTable(tags[tag], pcs, false)
	if err != nil {
		return nil, fmt.Errorf("cmyk: %s: %w", tag, err)
	}
	p := &Profile{A2B: a2b, PCS: enc}
	// Little CMS: the perceptual black of an output profile, sent through
	// B2A0 and back colorimetrically; of any other, the darkest colorant
	// (all inks full). Either at L* 50 at most, made neutral.
	var lab [3]float64
	b2a, benc, err := readTable(tags["B2A0"], pcs, true)
	if string(b[12:16]) == "prtr" && err == nil {
		black := benc.encodeLab(0, 0, 0)
		if b[8] >= 4 {
			// Little CMS compensates the black point on the way into a
			// v4 perceptual table, which takes black to the perceptual
			// black of ICC v4.
			black = benc.encodeLab(XYZToLab(perceptualBlack))
		}
		var ink [4]float64
		b2a.Eval(black[:], ink[:])
		a2b.Eval(ink[:], lab[:])
	} else {
		a2b.Eval([]float64{1, 1, 1, 1}, lab[:])
	}
	L, _, _ := XYZToLab(enc.XYZOf(lab[:]))
	p.Black = LabToXYZ(min(max(L, 0), 50), 0, 0)
	return p, nil
}

// perceptualBlack is the black of the perceptual intent of ICC v4 (XYZ,
// D50).
var perceptualBlack = [3]float64{0.00336, 0.0034731, 0.00287}

// XYZToLab converts XYZ relative to D50 to CIE L*a*b*.
func XYZToLab(xyz [3]float64) (L, a, b float64) {
	fx, fy, fz := cbrtLab(xyz[0]/D50[0]), cbrtLab(xyz[1]), cbrtLab(xyz[2]/D50[2])
	return 116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)
}

// cbrtLab is CIE's f(t) of L* = 116 f(Y) - 16.
func cbrtLab(t float64) float64 {
	if t > 216.0/24389 {
		return math.Cbrt(t)
	}
	return t*24389/27/116 + 16.0/116
}

// readTable reads an A2B (4 inputs, 3 outputs) or, with b2a, a B2A table
// (3 inputs, 4 outputs), and the encoding of its PCS side.
func readTable(b []byte, pcs PCS, b2a bool) (Transform, PCS, error) {
	if len(b) < 12 {
		return nil, 0, errors.New("missing")
	}
	in, out := 4, 3
	if b2a {
		in, out = 3, 4
	}
	var t Transform
	var err error
	enc := pcs
	switch string(b[:4]) {
	case "mft2":
		t, err = readLut(b, 2, in, out)
	case "mft1":
		if pcs == XYZ {
			return nil, 0, errors.New("lut8Type with XYZ")
		}
		enc = LabV4
		t, err = readLut(b, 1, in, out)
	case "mAB ", "mBA ":
		if (string(b[:4]) == "mBA ") != b2a {
			return nil, 0, fmt.Errorf("%q in the wrong direction", b[:4])
		}
		if pcs != XYZ {
			enc = LabV4
		}
		t, err = readLutAB(b, in, out, b2a)
	default:
		return nil, 0, fmt.Errorf("unsupported table type %q", b[:4])
	}
	return t, enc, err
}

// readLut reads a lut8Type (w = 1) or lut16Type (w = 2) of in inputs and
// out outputs.
func readLut(b []byte, w, in, out int) (Transform, error) {
	if len(b) < 48 || int(b[8]) != in || int(b[9]) != out {
		return nil, errors.New("table of the wrong shape")
	}
	t := &Lut{In: in, Out: out, Grid: int(b[10])}
	nIn, nOut, p := 256, 256, 48
	if w == 2 {
		if len(b) < 52 {
			return nil, errors.New("truncated table")
		}
		nIn, nOut, p = int(binary.BigEndian.Uint16(b[48:])), int(binary.BigEndian.Uint16(b[50:])), 52
	}
	if t.Grid < 2 || nIn < 2 || nOut < 2 || nIn > 4096 || nOut > 4096 {
		return nil, errors.New("unsupported table")
	}
	size := out * pow(t.Grid, in)
	if len(b) < p+w*((in*nIn)+size+out*nOut) {
		return nil, errors.New("truncated table")
	}
	get := func(k int) []float64 {
		v := make([]float64, k)
		for i := range v {
			if w == 2 {
				v[i] = float64(binary.BigEndian.Uint16(b[p+2*i:])) / 65535
			} else {
				v[i] = float64(b[p+i]) / 255
			}
		}
		p += w * k
		return v
	}
	for range in {
		t.InCurves = append(t.InCurves, get(nIn))
	}
	t.Table = get(size)
	for range out {
		t.OutCurves = append(t.OutCurves, get(nOut))
	}
	// The matrix applies to XYZ input only, which a CMYK table does not
	// take, and to the three inputs of a B2A table (Little CMS: where it is
	// not the identity).
	m := matrixOf(b[12:], false)
	if in == 3 && !m.identity() {
		return pipeline{m, t}, nil
	}
	return t, nil
}

// readLutAB reads a lutAtoBType (A curves, CLUT, M curves, matrix, B
// curves) or, with b2a, a lutBtoAType (the same in reverse).
func readLutAB(b []byte, in, out int, b2a bool) (Transform, error) {
	if len(b) < 32 || int(b[8]) != in || int(b[9]) != out {
		return nil, errors.New("table of the wrong shape")
	}
	off := func(i int) int { return int(binary.BigEndian.Uint32(b[12+4*i:])) }
	offB, offM, offC, offA := off(0), off(2), off(3), off(4)
	offMat := off(1)
	// The side of the CLUT on the device, and on the PCS.
	dev, pcs := in, out
	if b2a {
		dev, pcs = out, in
	}
	if offC == 0 {
		return nil, errors.New("no CLUT")
	}
	if offB == 0 {
		return nil, errors.New("no B curves")
	}
	B, err := readCurves(b, offB, pcs)
	if err != nil {
		return nil, err
	}
	var A, M [][]float64
	if offA != 0 {
		if A, err = readCurves(b, offA, dev); err != nil {
			return nil, err
		}
	}
	if offM != 0 {
		if M, err = readCurves(b, offM, pcs); err != nil {
			return nil, err
		}
	}
	var mat *matrix
	if offMat != 0 {
		if offMat < 0 || offMat+48 > len(b) {
			return nil, errors.New("truncated matrix")
		}
		m := matrixOf(b[offMat:], true)
		mat = &m
	}
	clut, err := readCLUT(b, offC, in, out)
	if err != nil {
		return nil, err
	}
	clut.InCurves = A
	if b2a {
		clut.InCurves, clut.OutCurves = nil, A
	}
	var p pipeline
	add := func(s Transform) { p = append(p, s) }
	if !b2a {
		add(clut)
		if M != nil {
			add(curves(M))
		}
		if mat != nil {
			add(*mat)
		}
		add(curves(B))
	} else {
		add(curves(B))
		if mat != nil {
			add(*mat)
		}
		if M != nil {
			add(curves(M))
		}
		add(clut)
	}
	return p, nil
}

// readCLUT reads the CLUT of a lutAtoBType or lutBtoAType at off, of the
// same number of nodes along each input.
func readCLUT(b []byte, off, in, out int) (*Lut, error) {
	if off < 0 || off+20 > len(b) {
		return nil, errors.New("truncated CLUT")
	}
	g := int(b[off])
	for i := range in {
		if int(b[off+i]) != g {
			return nil, errors.New("CLUT of different grids per input")
		}
	}
	w := int(b[off+16])
	if g < 2 || (w != 1 && w != 2) {
		return nil, errors.New("unsupported CLUT")
	}
	size := out * pow(g, in)
	p := off + 20
	if p+w*size > len(b) {
		return nil, errors.New("truncated CLUT")
	}
	t := &Lut{In: in, Out: out, Grid: g, Table: make([]float64, size)}
	for i := range t.Table {
		if w == 2 {
			t.Table[i] = float64(binary.BigEndian.Uint16(b[p+2*i:])) / 65535
		} else {
			t.Table[i] = float64(b[p+i]) / 255
		}
	}
	return t, nil
}

// readCurves reads n curves (curveType or parametricCurveType, each
// padded to four bytes) at off, sampled.
func readCurves(b []byte, off, n int) ([][]float64, error) {
	var cs [][]float64
	for range n {
		if off < 0 || off+12 > len(b) {
			return nil, errors.New("truncated curve")
		}
		c, size, err := readCurve(b[off:])
		if err != nil {
			return nil, err
		}
		cs = append(cs, c)
		off += (size + 3) &^ 3
	}
	return cs, nil
}

// curveSamples is the number of samples a curve given as a function is
// tabulated at.
const curveSamples = 4096

// readCurve reads a curveType or parametricCurveType as samples over
// [0, 1], and its size in bytes.
func readCurve(b []byte) ([]float64, int, error) {
	switch string(b[:4]) {
	case "curv":
		n := int(binary.BigEndian.Uint32(b[8:]))
		switch {
		case n == 0:
			return []float64{0, 1}, 12, nil
		case n == 1 && len(b) >= 14:
			g := float64(binary.BigEndian.Uint16(b[12:])) / 256
			return sample(func(x float64) float64 { return math.Pow(x, g) }), 14, nil
		case n >= 2 && n <= 1<<16 && len(b) >= 12+2*n:
			t := make([]float64, n)
			for i := range t {
				t[i] = float64(binary.BigEndian.Uint16(b[12+2*i:])) / 65535
			}
			return t, 12 + 2*n, nil
		}
	case "para":
		fn := int(binary.BigEndian.Uint16(b[8:]))
		np := [...]int{1, 3, 4, 5, 7}
		if fn > 4 || len(b) < 12+4*np[fn] {
			break
		}
		var p [7]float64
		for i := range np[fn] {
			p[i] = float64(int32(binary.BigEndian.Uint32(b[12+4*i:]))) / 65536
		}
		g, a, bb, c, d, e, f := p[0], p[1], p[2], p[3], p[4], p[5], p[6]
		pw := func(x float64) float64 {
			if x <= 0 {
				return 0
			}
			return math.Pow(x, g)
		}
		var fun func(float64) float64
		switch fn {
		case 0:
			fun = pw
		case 1:
			if a == 0 {
				break
			}
			fun = func(x float64) float64 {
				if x >= -bb/a {
					return pw(a*x + bb)
				}
				return 0
			}
		case 2:
			if a == 0 {
				break
			}
			fun = func(x float64) float64 {
				if x >= -bb/a {
					return pw(a*x+bb) + c
				}
				return c
			}
		case 3:
			fun = func(x float64) float64 {
				if x >= d {
					return pw(a*x + bb)
				}
				return c * x
			}
		case 4:
			fun = func(x float64) float64 {
				if x >= d {
					return pw(a*x+bb) + e
				}
				return c*x + f
			}
		}
		if fun != nil {
			return sample(fun), 12 + 4*np[fn], nil
		}
	}
	return nil, 0, fmt.Errorf("unsupported curve %q", b[:4])
}

// sample tabulates f over [0, 1], clipped to [0, 1].
func sample(f func(float64) float64) []float64 {
	t := make([]float64, curveSamples)
	for i := range t {
		v := f(float64(i) / (curveSamples - 1))
		if !(v > 0) {
			v = 0
		}
		t[i] = min(v, 1)
	}
	return t
}

// curves applies a sampled curve to each value.
type curves [][]float64

func (cs curves) Eval(in, out []float64) {
	for i, c := range cs {
		out[i] = curve(c, in[i])
	}
}

// matrix is a 3×3 matrix and an offset.
type matrix [12]float64

// matrixOf reads nine s15Fixed16 numbers, and with offset three more.
func matrixOf(b []byte, offset bool) matrix {
	var m matrix
	n := 9
	if offset {
		n = 12
	}
	for i := range n {
		m[i] = float64(int32(binary.BigEndian.Uint32(b[4*i:]))) / 65536
	}
	return m
}

func (m matrix) identity() bool {
	return m == matrix{1, 0, 0, 0, 1, 0, 0, 0, 1}
}

func (m matrix) Eval(in, out []float64) {
	x, y, z := in[0], in[1], in[2]
	for i := range 3 {
		out[i] = min(max(m[3*i]*x+m[3*i+1]*y+m[3*i+2]*z+m[9+i], 0), 1)
	}
}

// pipeline runs its stages one after another.
type pipeline []Transform

func (p pipeline) Eval(in, out []float64) {
	var a, b [4]float64
	copy(a[:], in)
	for _, s := range p {
		b = [4]float64{}
		s.Eval(a[:], b[:])
		a = b
	}
	copy(out, a[:len(out)])
}
