// Package cmyk converts DeviceCMYK to sRGB as a press would print it,
// through a SWOP press profile, as PDFium, MuPDF, Poppler and Ghostscript
// convert through profiles of their own, instead of the naive formula
// R = (1-C)(1-K).
//
// swop.bin holds the colorimetric table (A2B1) of colord's profile of
// CGATS TR 005 (SWOP, coated #5 paper) and its black point, written by
// ./gen. Source of the characterization data: CGATS TR 005, NPES. The
// conversion is what Little CMS does with that profile to sRGB, relative
// colorimetric with black point compensation (transicc -t 1 -b): input
// curves, the table interpolated as Little CMS does, output curves, Lab
// D50, the profile's black point scaled to zero in XYZ, sRGB with its
// primaries adapted to D50.
//
// Parse reads other CMYK profiles (lut8, lut16 or lutAtoB tables) the
// same way, and Load tabulates them for drawing, shared by content: a
// caller's profile for DeviceCMYK, and the profiles of ICCBased spaces.
package cmyk

import (
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"errors"
	"math"
	"runtime"
	"sync"
	"weak"
)

//go:generate go run ./gen -out swop.bin

//go:embed swop.bin
var swop []byte

// Lut is an ICC lut16Type table (mft2): an input curve per input, a grid
// of Grid nodes along each of the In inputs with Out outputs each, and an
// output curve per output, all in [0, 1]. Without curves (the CLUT of a
// lutAtoBType), values pass unchanged.
type Lut struct {
	In, Out, Grid int
	InCurves      [][]float64
	Table         []float64 // first input slowest
	OutCurves     [][]float64
}

// Eval runs v (In values in [0, 1]) through the table into out.
func (t *Lut) Eval(v, out []float64) {
	var x [4]float64
	copy(x[:t.In], v)
	for i, c := range t.InCurves {
		x[i] = curve(c, x[i])
	}
	t.interp(x[:t.In], out)
	for i, c := range t.OutCurves {
		out[i] = curve(c, out[i])
	}
}

// curve evaluates a sampled curve at x in [0, 1].
func curve(c []float64, x float64) float64 {
	if !(x > 0) { // and NaN
		return c[0]
	}
	x = min(x, 1) * float64(len(c)-1)
	i := min(int(x), len(c)-2)
	return c[i] + (x-float64(i))*(c[i+1]-c[i])
}

// interp interpolates the grid at x as Little CMS does: tetrahedrally in
// the cube of the last three inputs, and linearly between two such cubes
// along a fourth.
func (t *Lut) interp(x, out []float64) {
	cube := t.Out * t.Grid * t.Grid * t.Grid
	if t.In == 3 {
		t.tetra(0, x, out)
		return
	}
	i, d := cell(x[0], t.Grid)
	t.tetra(i*cube, x[1:], out)
	if d == 0 {
		return
	}
	var hi [4]float64
	t.tetra((i+1)*cube, x[1:], hi[:t.Out])
	for k := range t.Out {
		out[k] += d * (hi[k] - out[k])
	}
}

// cell returns the node below x in [0, 1] on a grid of n nodes, and the
// fraction of the way to the next.
func cell(x float64, n int) (int, float64) {
	if !(x > 0) {
		return 0, 0
	}
	x = min(x, 1) * float64(n-1)
	i := min(int(x), n-2)
	return i, x - float64(i)
}

// tetra interpolates the cube of the grid at offset base at x (three
// inputs) into out.
func (t *Lut) tetra(base int, x, out []float64) {
	var f [3]float64
	for k := range 3 {
		var i int
		i, f[k] = cell(x[k], t.Grid)
		base += i * t.Out * pow(t.Grid, 2-k)
	}
	s := [3]int{t.Out * t.Grid * t.Grid, t.Out * t.Grid, t.Out}
	// The axes in the order of their fractions, largest first.
	o := [3]int{0, 1, 2}
	if f[o[1]] > f[o[0]] {
		o[0], o[1] = o[1], o[0]
	}
	if f[o[2]] > f[o[1]] {
		o[1], o[2] = o[2], o[1]
		if f[o[1]] > f[o[0]] {
			o[0], o[1] = o[1], o[0]
		}
	}
	c0 := base
	c1 := c0 + s[o[0]]
	c2 := c1 + s[o[1]]
	c3 := c2 + s[o[2]]
	g := t.Table
	for k := range t.Out {
		out[k] = g[c0+k] + f[o[0]]*(g[c1+k]-g[c0+k]) + f[o[1]]*(g[c2+k]-g[c1+k]) + f[o[2]]*(g[c3+k]-g[c2+k])
	}
}

func pow(n, e int) int {
	p := 1
	for range e {
		p *= n
	}
	return p
}

// Profile is a CMYK to PCS table with the black point of its profile.
type Profile struct {
	A2B   Transform  // CMYK to the PCS
	PCS   PCS        // how A2B encodes the PCS
	Black [3]float64 // XYZ, D50
}

// The legacy 16-bit Lab encoding of ICC v2 lut16 tables: L* 0..100 over
// 0..0xFF00, a* and b* -128..128 over 0..0xFFFF+1.

// EncodeLab encodes L*, a*, b* for a lut16 table.
func EncodeLab(L, a, b float64) [3]float64 {
	return [3]float64{L / 100 * 65280 / 65535, (a + 128) * 256 / 65535, (b + 128) * 256 / 65535}
}

// DecodeLab decodes what a lut16 table gives.
func DecodeLab(v []float64) (L, a, b float64) {
	return v[0] * 65535 / 65280 * 100, v[1]*65535/256 - 128, v[2]*65535/256 - 128
}

// D50 is the white of the profile connection space.
var D50 = [3]float64{0.9642, 1, 0.8249}

// LabToXYZ converts CIE L*a*b* to XYZ relative to D50.
func LabToXYZ(L, a, b float64) [3]float64 {
	fy := (L + 16) / 116
	finv := func(t float64) float64 {
		if t > 6.0/29 {
			return t * t * t
		}
		return 3 * (6.0 / 29) * (6.0 / 29) * (t - 4.0/29)
	}
	return [3]float64{D50[0] * finv(fy+a/500), finv(fy), D50[2] * finv(fy-b/200)}
}

// xyzToSRGB is sRGB's matrix with its primaries adapted to D50 (Bradford).
var xyzToSRGB = [9]float64{
	3.1338561, -1.6168667, -0.4906146,
	-0.9787684, 1.9161415, 0.0334540,
	0.0719453, -0.2289914, 1.4052427,
}

// RGB converts inks (fractions in [0, 1]) to sRGB components in [0, 1].
func (p *Profile) RGB(c [4]float64) (r, g, b float64) {
	lin := p.linear(c)
	return encode(lin[0]), encode(lin[1]), encode(lin[2])
}

// linear converts inks to linear sRGB components, not clipped.
func (p *Profile) linear(c [4]float64) [3]float64 {
	var pcs [3]float64
	p.A2B.Eval(c[:], pcs[:])
	xyz := p.PCS.XYZOf(pcs[:])
	// Black point compensation: the profile's black to zero, the white
	// kept.
	for i := range xyz {
		xyz[i] = (xyz[i] - p.Black[i]) * D50[i] / (D50[i] - p.Black[i])
	}
	m := &xyzToSRGB
	return [3]float64{
		m[0]*xyz[0] + m[1]*xyz[1] + m[2]*xyz[2],
		m[3]*xyz[0] + m[4]*xyz[1] + m[5]*xyz[2],
		m[6]*xyz[0] + m[7]*xyz[1] + m[8]*xyz[2],
	}
}

// Grid is the number of nodes along each ink of the table that a Table
// interpolates: linear sRGB, unclipped (smooth where a colour leaves
// sRGB), made from the profile on first use.
const Grid = 17

// grid tabulates p at Grid⁴ nodes, C slowest.
func (p *Profile) grid() []float32 {
	t := make([]float32, 0, 3*Grid*Grid*Grid*Grid)
	const s = 1.0 / (Grid - 1)
	for c := range Grid {
		for m := range Grid {
			for y := range Grid {
				for k := range Grid {
					v := p.linear([4]float64{float64(c) * s, float64(m) * s, float64(y) * s, float64(k) * s})
					t = append(t, float32(v[0]), float32(v[1]), float32(v[2]))
				}
			}
		}
	}
	return t
}

// interp interpolates the grid t at v (inks in [0, 1]) over the simplex
// of the cell that holds it: five nodes, from the cell's lowest corner
// one ink at a time in the order of the fractions, largest first.
func interp(t []float32, v [4]float64) (r, g, b float64) {
	const n = Grid - 1
	stride := [4]int{3 * Grid * Grid * Grid, 3 * Grid * Grid, 3 * Grid, 3}
	var f [4]float64
	at := 0
	for i, x := range v {
		x *= n
		if !(x > 0) { // and NaN
			x = 0
		}
		x = min(x, n)
		j := min(int(x), n-1)
		f[i] = x - float64(j)
		at += j * stride[i]
	}
	o := [4]int{0, 1, 2, 3}
	for i := 1; i < 4; i++ {
		for j := i; j > 0 && f[o[j]] > f[o[j-1]]; j-- {
			o[j], o[j-1] = o[j-1], o[j]
		}
	}
	w := 1 - f[o[0]]
	r, g, b = w*float64(t[at]), w*float64(t[at+1]), w*float64(t[at+2])
	for i, k := range o {
		at += stride[k]
		w = f[k]
		if i < 3 {
			w -= f[o[i+1]]
		}
		r += w * float64(t[at])
		g += w * float64(t[at+1])
		b += w * float64(t[at+2])
	}
	return encode(r), encode(g), encode(b)
}

// encode is sRGB's transfer function, clipping to [0, 1]; between the
// samples of a table of it, linear.
func encode(v float64) float64 {
	if !(v > 0) {
		return 0
	}
	if v >= 1 {
		return 1
	}
	x := v * encodeSteps
	i := int(x)
	return encodeTab[i] + (x-float64(i))*(encodeTab[i+1]-encodeTab[i])
}

const encodeSteps = 1 << 14

var encodeTab = func() *[encodeSteps + 1]float64 {
	var t [encodeSteps + 1]float64
	for i := range t {
		v := float64(i) / encodeSteps
		if v <= 0.0031308 {
			t[i] = 12.92 * v
		} else {
			t[i] = 1.055*math.Pow(v, 1/2.4) - 0.055
		}
	}
	return &t
}()

// MarshalBinary writes p as swop.bin holds it: little-endian In, Out,
// Grid and the number of curve entries (uint16 each), the input curves,
// the grid and the output curves (uint16 over [0, 1]), the black point
// (float64 each). Only a lut16 table with Lab can be written.
func (p *Profile) MarshalBinary() ([]byte, error) {
	t, ok := p.A2B.(*Lut)
	if !ok || p.PCS != LabV2 || len(t.InCurves) != t.In || len(t.OutCurves) != t.Out {
		return nil, errors.New("cmyk: not a lut16 table with Lab")
	}
	n := len(t.InCurves[0])
	b := binary.LittleEndian.AppendUint16(nil, uint16(t.In))
	b = binary.LittleEndian.AppendUint16(b, uint16(t.Out))
	b = binary.LittleEndian.AppendUint16(b, uint16(t.Grid))
	b = binary.LittleEndian.AppendUint16(b, uint16(n))
	put := func(vs []float64) {
		for _, v := range vs {
			b = binary.LittleEndian.AppendUint16(b, uint16(math.Round(min(max(v, 0), 1)*65535)))
		}
	}
	for _, c := range t.InCurves {
		if len(c) != n {
			return nil, errors.New("cmyk: curves of different lengths")
		}
		put(c)
	}
	put(t.Table)
	for _, c := range t.OutCurves {
		if len(c) != n {
			return nil, errors.New("cmyk: curves of different lengths")
		}
		put(c)
	}
	for _, v := range p.Black {
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(v))
	}
	return b, nil
}

// UnmarshalBinary reads what MarshalBinary writes.
func (p *Profile) UnmarshalBinary(b []byte) error {
	if len(b) < 8 {
		return errors.New("cmyk: short table")
	}
	u := func(i int) int { return int(binary.LittleEndian.Uint16(b[2*i:])) }
	t := &Lut{In: u(0), Out: u(1), Grid: u(2)}
	n := u(3)
	if t.In < 3 || t.In > 4 || t.Out != 3 || t.Grid < 2 || n < 2 {
		return errors.New("cmyk: unsupported table")
	}
	words := (t.In+t.Out)*n + t.Out*pow(t.Grid, t.In)
	if len(b) != 8+2*words+24 {
		return errors.New("cmyk: table of the wrong size")
	}
	at := 4
	get := func(k int) []float64 {
		v := make([]float64, k)
		for i := range v {
			v[i] = float64(u(at+i)) / 65535
		}
		at += k
		return v
	}
	for range t.In {
		t.InCurves = append(t.InCurves, get(n))
	}
	t.Table = get(t.Out * pow(t.Grid, t.In))
	for range t.Out {
		t.OutCurves = append(t.OutCurves, get(n))
	}
	for i := range p.Black {
		p.Black[i] = math.Float64frombits(binary.LittleEndian.Uint64(b[2*at+8*i:]))
	}
	p.A2B = t
	return nil
}

// Table is a profile tabulated for drawing: linear sRGB at Grid⁴ nodes,
// made on first use, interpolated over the simplex of a cell. It is safe
// for concurrent use.
type Table struct {
	once sync.Once
	p    *Profile
	bin  []byte // what p is read from, for the bundled table
	grid []float32
}

// NewTable tabulates p.
func NewTable(p *Profile) *Table { return &Table{p: p} }

func (t *Table) init() {
	t.once.Do(func() {
		if t.p == nil {
			t.p = new(Profile)
			if err := t.p.UnmarshalBinary(t.bin); err != nil {
				panic(err) // a broken build
			}
		}
		t.grid = t.p.grid()
	})
}

// Profile returns the profile t tabulates.
func (t *Table) Profile() *Profile {
	t.init()
	return t.p
}

var swopTable = &Table{bin: swop}

// Default returns the table of the bundled profile (SWOP).
func Default() *Table { return swopTable }

// SWOP returns the bundled profile.
func SWOP() *Profile { return swopTable.Profile() }

var cache struct {
	sync.Mutex
	m map[[32]byte]weak.Pointer[Table]
}

// Load parses an ICC profile (see Parse) and tabulates it. Tables are
// shared by content while anyone holds them.
func Load(icc []byte) (*Table, error) {
	key := sha256.Sum256(icc)
	cache.Lock()
	t := cache.m[key].Value()
	cache.Unlock()
	if t != nil {
		return t, nil
	}
	p, err := Parse(icc)
	if err != nil {
		return nil, err
	}
	t = NewTable(p)
	cache.Lock()
	defer cache.Unlock()
	if old := cache.m[key].Value(); old != nil {
		return old, nil
	}
	if cache.m == nil {
		cache.m = map[[32]byte]weak.Pointer[Table]{}
	}
	cache.m[key] = weak.Make(t)
	runtime.AddCleanup(t, func(key [32]byte) {
		cache.Lock()
		if cache.m[key].Value() == nil {
			delete(cache.m, key)
		}
		cache.Unlock()
	}, key)
	return t, nil
}

// RGB converts inks in [0, 1] to sRGB components in [0, 1] through the
// bundled profile.
func RGB(c, m, y, k float64) (r, g, b float64) { return swopTable.RGB(c, m, y, k) }

// RGB8 converts ink bytes to sRGB bytes through the bundled profile.
func RGB8(c, m, y, k uint8) (r, g, b uint8) { return swopTable.RGB8(c, m, y, k) }

// RGB converts inks in [0, 1] to sRGB components in [0, 1].
func (t *Table) RGB(c, m, y, k float64) (r, g, b float64) {
	t.init()
	return interp(t.grid, [4]float64{c, m, y, k})
}

// RGB8 converts ink bytes (255 is full ink) to sRGB bytes. It is RGB for
// bytes, made fast for images: the cell and fraction of each byte, and
// sRGB bytes of linear values, come from tables.
func (t *Table) RGB8(c, m, y, k uint8) (r, g, b uint8) {
	t.init()
	g8 := t.grid
	cs, ms, ys, ks := cells[c], cells[m], cells[y], cells[k]
	at := int(cs.at)*stride0 + int(ms.at)*stride1 + int(ys.at)*stride2 + int(ks.at)*3
	// The inks in the order of their fractions, largest first (a sorting
	// network on fraction<<2 | ink).
	a0, a1, a2, a3 := uint32(cs.f)<<2, uint32(ms.f)<<2|1, uint32(ys.f)<<2|2, uint32(ks.f)<<2|3
	if a0 < a1 {
		a0, a1 = a1, a0
	}
	if a2 < a3 {
		a2, a3 = a3, a2
	}
	if a0 < a2 {
		a0, a2 = a2, a0
	}
	if a1 < a3 {
		a1, a3 = a3, a1
	}
	if a1 < a2 {
		a1, a2 = a2, a1
	}
	const unit = 1.0 / 256
	w := float32(256-int(a0>>2)) * unit
	_ = g8[at+2]
	fr, fg, fb := w*g8[at], w*g8[at+1], w*g8[at+2]
	strides := [4]int{stride0, stride1, stride2, 3}
	for i, a := range [4]uint32{a0, a1, a2, a3} {
		at += strides[a&3]
		next := uint32(0)
		switch i {
		case 0:
			next = a1
		case 1:
			next = a2
		case 2:
			next = a3
		}
		w = float32(int(a>>2)-int(next>>2)) * unit
		_ = g8[at+2]
		fr += w * g8[at]
		fg += w * g8[at+1]
		fb += w * g8[at+2]
	}
	return encode8(fr), encode8(fg), encode8(fb)
}

const (
	stride0 = 3 * Grid * Grid * Grid
	stride1 = 3 * Grid * Grid
	stride2 = 3 * Grid
)

// cell8 is the node below a byte (the last but one at most) and the
// fraction, of 256, of the way to the next.
type cell8 struct {
	at uint8
	f  uint16
}

var cells = func() (t [256]cell8) {
	for i := range t {
		x := float64(i) * (Grid - 1) / 255
		j := min(int(x), Grid-2)
		t[i] = cell8{uint8(j), uint16(math.Round((x - float64(j)) * 256))}
	}
	return t
}()

// encode8 is encode to a byte, from a table over linear [0, 1].
func encode8(v float32) uint8 {
	if !(v > 0) {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return encode8Tab[int(v*encode8Steps+0.5)]
}

const encode8Steps = 1 << 14

var encode8Tab = func() *[encode8Steps + 1]uint8 {
	var t [encode8Steps + 1]uint8
	for i := range t {
		t[i] = uint8(encode(float64(i)/encode8Steps)*255 + 0.5)
	}
	return &t
}()
