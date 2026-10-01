package cera

import (
	"encoding/binary"
	"math"
	"sync"
)

// Colour conversion of the CIE-based spaces (PDF 2.0, 8.6.5): CalGray,
// CalRGB, Lab and ICC profiles of the matrix/TRC kind, the "simplified
// ICC" of the spec. Each reaches CIE XYZ relative to the D50 white of the
// ICC connection space (a CalRGB or Lab white point is adapted to it with
// the Bradford transform), and XYZ goes to sRGB. Profiles built from
// lookup tables (A2B tags, every CMYK press profile) are not read: their
// spaces fall back on the device space of as many components, which is
// what PDF says to do when a profile is not supported.

// vec3 and mat3 are the arithmetic of XYZ.
type (
	vec3 [3]float64
	mat3 [9]float64 // row major
)

func (m *mat3) apply(v vec3) vec3 {
	return vec3{
		m[0]*v[0] + m[1]*v[1] + m[2]*v[2],
		m[3]*v[0] + m[4]*v[1] + m[5]*v[2],
		m[6]*v[0] + m[7]*v[1] + m[8]*v[2],
	}
}

func (m *mat3) mul(n *mat3) mat3 {
	var r mat3
	for i := range 3 {
		for j := range 3 {
			for k := range 3 {
				r[3*i+j] += m[3*i+k] * n[3*k+j]
			}
		}
	}
	return r
}

func (m *mat3) inverse() (mat3, bool) {
	a, b, c := m[0], m[1], m[2]
	d, e, f := m[3], m[4], m[5]
	g, h, k := m[6], m[7], m[8]
	det := a*(e*k-f*h) - b*(d*k-f*g) + c*(d*h-e*g)
	if det == 0 || math.IsNaN(det) || math.IsInf(det, 0) {
		return mat3{}, false
	}
	return mat3{
		(e*k - f*h) / det, (c*h - b*k) / det, (b*f - c*e) / det,
		(f*g - d*k) / det, (a*k - c*g) / det, (c*d - a*f) / det,
		(d*h - e*g) / det, (b*g - a*h) / det, (a*e - b*d) / det,
	}, true
}

var (
	whiteD50 = vec3{0.9642, 1, 0.8249}
	// xyzToSRGB maps D50 XYZ to linear sRGB (Bradford-adapted to D65).
	xyzToSRGB = mat3{
		3.1338561, -1.6168667, -0.4906146,
		-0.9787684, 1.9161415, 0.0334540,
		0.0719453, -0.2289914, 1.4052427,
	}
	bradford = mat3{
		0.8951, 0.2664, -0.1614,
		-0.7502, 1.7135, 0.0367,
		0.0389, -0.0685, 1.0296,
	}
	// srgbD50 holds the sRGB primaries in D50 XYZ (columns), as an sRGB
	// ICC profile states them in rXYZ, gXYZ and bXYZ.
	srgbD50 = mat3{
		0.4361, 0.3851, 0.1431,
		0.2225, 0.7169, 0.0606,
		0.0139, 0.0971, 0.7141,
	}
)

// adaptToD50 returns the Bradford transform of XYZ from white point w to
// D50.
func adaptToD50(w vec3) mat3 {
	inv, _ := bradford.inverse()
	s, d := bradford.apply(w), bradford.apply(whiteD50)
	var k mat3
	for i := range 3 {
		if s[i] == 0 {
			return mat3{1, 0, 0, 0, 1, 0, 0, 0, 1}
		}
		k[4*i] = d[i] / s[i]
	}
	r := k.mul(&bradford)
	return inv.mul(&r)
}

// srgbEncode applies the sRGB transfer curve to linear v in [0, 1].
func srgbEncode(v float64) float64 {
	if !(v > 0.0031308) {
		return max(v, 0) * 12.92
	}
	return min(1.055*math.Pow(v, 1/2.4)-0.055, 1)
}

// srgbDecode is the inverse of srgbEncode.
func srgbDecode(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// encodeLUT holds srgbEncode at 4096 steps of linear light.
var encodeLUT = sync.OnceValue(func() *[4097]float64 {
	var t [4097]float64
	for i := range t {
		t[i] = srgbEncode(float64(i) / 4096)
	}
	return &t
})

func encode(v float64) float64 {
	if !(v > 0) {
		return 0
	}
	if v >= 1 {
		return 1
	}
	return encodeLUT()[int(v*4096+0.5)]
}

// curve is a tone reproduction curve: a gamma, a table or a parametric
// curve of ICC type 'para'.
type curve struct {
	kind  uint8 // 0 gamma, 1 table, 2 parametric
	gamma float64
	table []float64 // normalized
	par   [7]float64
	fn    int
}

func (c *curve) eval(x float64) float64 {
	x = clamp01(x)
	switch c.kind {
	case 1:
		n := len(c.table) - 1
		f := x * float64(n)
		i := min(int(f), n-1)
		return c.table[i] + (f-float64(i))*(c.table[i+1]-c.table[i])
	case 2:
		g, a, b, cc, d, e, f := c.par[0], c.par[1], c.par[2], c.par[3], c.par[4], c.par[5], c.par[6]
		pow := func(v float64) float64 {
			if v <= 0 {
				return 0
			}
			return math.Pow(v, g)
		}
		switch c.fn {
		case 0:
			return pow(x)
		case 1:
			if x >= -b/a {
				return pow(a*x + b)
			}
			return 0
		case 2:
			if x >= -b/a {
				return pow(a*x+b) + cc
			}
			return cc
		case 3:
			if x >= d {
				return pow(a*x + b)
			}
			return cc * x
		default:
			if x >= d {
				return pow(a*x+b) + e
			}
			return cc*x + f
		}
	}
	if c.gamma == 1 {
		return x
	}
	return math.Pow(x, c.gamma)
}

// srgbLike reports whether c is close to the sRGB curve.
func (c *curve) srgbLike() bool {
	for _, x := range [...]float64{0.02, 0.1, 0.3, 0.5, 0.7, 0.9} {
		if math.Abs(c.eval(x)-srgbDecode(x)) > 0.01 {
			return false
		}
	}
	return true
}

// lut returns the curve at 256 steps.
func (c *curve) lut() *[256]float64 {
	var t [256]float64
	for i := range t {
		t[i] = c.eval(float64(i) / 255)
	}
	return &t
}

// cieSpace converts a CIE-based colour to sRGB: n components through
// curves (or, for Lab, the Lab formula) to linear values, a matrix to
// D50 XYZ and on to linear sRGB (toRGB is the product).
type cieSpace struct {
	n     int
	lab   bool
	white vec3 // Lab
	curve [3]curve
	toRGB mat3 // linear values (or Lab's XYZ) to linear sRGB
	// rng is the Range of Lab's a* and b*.
	rng [4]float64

	once sync.Once
	luts [3]*[256]float64
}

func (s *cieSpace) rgb(v []float64) (r, g, b float64) {
	if s.lab {
		return s.labRGB(v)
	}
	if s.n == 1 {
		y := encode(s.curve[0].eval(v[0]))
		return y, y, y
	}
	l := vec3{s.curve[0].eval(v[0]), s.curve[1].eval(v[1]), s.curve[2].eval(v[2])}
	o := s.toRGB.apply(l)
	return encode(o[0]), encode(o[1]), encode(o[2])
}

// rgb8 converts three 8-bit components through lookup tables.
func (s *cieSpace) rgb8(c0, c1, c2 uint8) (r, g, b uint8) {
	s.once.Do(func() {
		for i := range 3 {
			s.luts[i] = s.curve[i].lut()
		}
	})
	l := vec3{s.luts[0][c0], s.luts[1][c1], s.luts[2][c2]}
	o := s.toRGB.apply(l)
	return unit8(encode(o[0])), unit8(encode(o[1])), unit8(encode(o[2]))
}

func (s *cieSpace) labRGB(v []float64) (r, g, b float64) {
	l := min(max(v[0], 0), 100)
	a := clampTo(v[1], s.rng[0], s.rng[1])
	bb := clampTo(v[2], s.rng[2], s.rng[3])
	fy := (l + 16) / 116
	fx, fz := fy+a/500, fy-bb/200
	inv := func(t float64) float64 {
		if t >= 6.0/29 {
			return t * t * t
		}
		return 108.0 / 841 * (t - 4.0/29)
	}
	xyz := vec3{s.white[0] * inv(fx), s.white[1] * inv(fy), s.white[2] * inv(fz)}
	o := s.toRGB.apply(xyz)
	return encode(o[0]), encode(o[1]), encode(o[2])
}

// whitePoint reads a white point; one with Y ≤ 0 is not.
func whitePoint(v []float64) (vec3, bool) {
	if len(v) != 3 || !(v[1] > 0) {
		return vec3{}, false
	}
	return vec3{v[0], v[1], v[2]}, true
}

// calSpace makes a CalGray (n = 1) or CalRGB space; nil means the white
// point is missing and the device space should be used.
func calSpace(n int, white, gamma, matrix []float64) *cieSpace {
	w, ok := whitePoint(white)
	if !ok {
		return nil
	}
	s := &cieSpace{n: n}
	for i := range n {
		s.curve[i].gamma = 1
		if i < len(gamma) && gamma[i] > 0 {
			s.curve[i].gamma = gamma[i]
		}
	}
	if n == 1 {
		return s
	}
	m := mat3{1, 0, 0, 0, 1, 0, 0, 0, 1}
	if len(matrix) == 9 {
		// PDF writes the matrix by columns: X = XA·A + XB·B + XC·C, …
		m = mat3{matrix[0], matrix[3], matrix[6], matrix[1], matrix[4], matrix[7], matrix[2], matrix[5], matrix[8]}
	}
	adapt := adaptToD50(w)
	t := adapt.mul(&m)
	s.toRGB = xyzToSRGB.mul(&t)
	return s
}

// labSpace makes a Lab space.
func labSpace(white, rng []float64) *cieSpace {
	w, ok := whitePoint(white)
	if !ok {
		w = whiteD50
	}
	s := &cieSpace{n: 3, lab: true, white: w, rng: [4]float64{-100, 100, -100, 100}}
	if len(rng) == 4 && rng[0] <= rng[1] && rng[2] <= rng[3] {
		copy(s.rng[:], rng)
	}
	adapt := adaptToD50(w)
	s.toRGB = xyzToSRGB.mul(&adapt)
	return s
}

// iccProfile reads a grey or RGB profile of the matrix/TRC kind. It
// returns nil for the profiles it does not read, and srgb true for a
// profile close enough to sRGB (or a grey of the sRGB curve) to be drawn
// as the device space.
func iccProfile(data []byte, n int) (s *cieSpace, srgb bool) {
	if len(data) < 132 {
		return nil, false
	}
	space := string(data[16:20])
	tags := map[string][]byte{}
	count := int(binary.BigEndian.Uint32(data[128:]))
	for i := 0; i < count && 132+12*i+12 <= len(data) && i < 256; i++ {
		e := data[132+12*i:]
		off, size := int(binary.BigEndian.Uint32(e[4:])), int(binary.BigEndian.Uint32(e[8:]))
		if off >= 0 && size >= 0 && off <= len(data) && size <= len(data)-off {
			tags[string(e[:4])] = data[off : off+size]
		}
	}
	switch {
	case space == "GRAY" && (n == 0 || n == 1):
		c, ok := readCurve(tags["kTRC"])
		if !ok {
			return nil, false
		}
		s = &cieSpace{n: 1}
		s.curve[0] = c
		return s, c.srgbLike()
	case space == "RGB " && (n == 0 || n == 3):
		s = &cieSpace{n: 3}
		var m mat3
		srgb = true
		for i, ch := range [3]string{"r", "g", "b"} {
			c, ok := readCurve(tags[ch+"TRC"])
			xyz, ok2 := readXYZ(tags[ch+"XYZ"])
			if !ok || !ok2 {
				return nil, false
			}
			s.curve[i] = c
			m[i], m[3+i], m[6+i] = xyz[0], xyz[1], xyz[2]
			srgb = srgb && c.srgbLike()
		}
		for i := range m {
			srgb = srgb && math.Abs(m[i]-srgbD50[i]) < 0.01
		}
		s.toRGB = xyzToSRGB.mul(&m)
		return s, srgb
	}
	return nil, false
}

func s15(b []byte) float64 { return float64(int32(binary.BigEndian.Uint32(b))) / 65536 }

func readXYZ(b []byte) (vec3, bool) {
	if len(b) < 20 || string(b[:4]) != "XYZ " {
		return vec3{}, false
	}
	return vec3{s15(b[8:]), s15(b[12:]), s15(b[16:])}, true
}

func readCurve(b []byte) (curve, bool) {
	if len(b) < 12 {
		return curve{}, false
	}
	switch string(b[:4]) {
	case "curv":
		n := int(binary.BigEndian.Uint32(b[8:]))
		switch {
		case n == 0:
			return curve{gamma: 1}, true
		case n == 1 && len(b) >= 14:
			return curve{gamma: float64(binary.BigEndian.Uint16(b[12:])) / 256}, true
		case n >= 2 && n <= 1<<16 && len(b) >= 12+2*n:
			t := make([]float64, n)
			for i := range t {
				t[i] = float64(binary.BigEndian.Uint16(b[12+2*i:])) / 65535
			}
			return curve{kind: 1, table: t}, true
		}
	case "para":
		fn := int(binary.BigEndian.Uint16(b[8:]))
		np := [...]int{1, 3, 4, 5, 7}
		if fn > 4 || len(b) < 12+4*np[fn] {
			return curve{}, false
		}
		c := curve{kind: 2, fn: fn}
		for i := range np[fn] {
			c.par[i] = s15(b[12+4*i:])
		}
		if fn > 0 && c.par[1] == 0 {
			return curve{}, false
		}
		return c, true
	}
	return curve{}, false
}
