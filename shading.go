package cera

import (
	"image/color"
	"math"
	"slices"
	"sync"

	"github.com/go-pdfkit/reader"
	"github.com/timzifer/stilus"
)

// Shadings (PDF 2.0, 8.7.4.5) are compiled once into what stilus's shaders
// draw, so that a raster worker never evaluates a PDF function: axial and
// radial shadings into a colour ramp whose knots carry the bounds of
// stitching functions (hard stops stay exact), function-based shadings into
// a grid of samples drawn as a texture, and meshes and patches into
// Gouraud-shaded triangles. Colour spaces are converted at compile time.

// Shading is a compiled shading dictionary, as a Device receives it. It is
// immutable and shared by the renders and workers that draw it; devices
// must not modify it.
type Shading struct {
	// Type is the shading type, 1 to 7.
	Type int

	// bbox bounds the shading in its own space, when hasBBox.
	bbox    Rect
	hasBBox bool
	// bg is the premultiplied background, painted where the shading
	// paints nothing when useBg is set (a shading pattern, never sh).
	bg           color.RGBA
	hasBg, useBg bool
	// withBg is the shading with useBg set, for shading patterns; nil
	// without a background.
	withBg *Shading

	// Types 2 and 3: the geometry, and the colours over [t0, t1]
	// normalised to [0, 1], at the parameters knots.
	coords [6]float64
	extend [2]bool
	ramp   stilus.Ramp
	knots  []float32

	// Type 1: the function sampled on a grid of gridW × gridH texture
	// pixels, which gridM maps to shading space.
	grid         *stilus.Texture
	gridW, gridH int
	gridM        Matrix

	// Types 4 to 7: triangles in shading space, coloured by their
	// vertices or by meshRamp; meshBox bounds them.
	tris     []stilus.MeshTriangle
	meshRamp stilus.Ramp
	meshBox  Rect
	meshes   *meshCache
}

// paintRect returns the part of shading space the shading can paint, if
// it is bounded: its BBox, and the grid or the mesh unless the background
// fills the rest.
func (sh *Shading) paintRect() (Rect, bool) {
	r, ok := sh.bbox, sh.hasBBox
	var own Rect
	switch {
	case sh.useBg && sh.hasBg:
		return r, ok
	case sh.Type == 1:
		own = rectUnder(Rect{0, 0, float64(sh.gridW), float64(sh.gridH)}, sh.gridM)
	case sh.Type >= 4:
		own = sh.meshBox
	default:
		return r, ok
	}
	if !ok {
		return own, true
	}
	return Rect{max(r.X0, own.X0), max(r.Y0, own.Y0), min(r.X1, own.X1), min(r.Y1, own.Y1)}, true
}

// rectUnder returns the bounding box of r under m.
func rectUnder(r Rect, m Matrix) Rect {
	out := Rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, c := range [4][2]float64{{r.X0, r.Y0}, {r.X1, r.Y0}, {r.X0, r.Y1}, {r.X1, r.Y1}} {
		x, y := m.Apply(c[0], c[1])
		out = Rect{min(out.X0, x), min(out.Y0, y), max(out.X1, x), max(out.Y1, y)}
	}
	return out
}

// meshCache keeps one MeshShader per device matrix and alpha a mesh is
// drawn with: its triangles are binned once and the shader is then shared
// by every band and worker.
type meshCache struct {
	mu      sync.Mutex
	entries []meshEntry
}

type meshEntry struct {
	m     Matrix
	alpha uint8
	s     *stilus.MeshShader
}

// maxMeshShaders bounds the shaders kept per mesh; beyond, each draw sets
// up its own.
const maxMeshShaders = 8

// shader returns a shader for the mesh under m with alpha, nil if it
// cannot be drawn so.
func (sh *Shading) shader(m Matrix, alpha uint8) *stilus.MeshShader {
	c := sh.meshes
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		if e.m == m && e.alpha == alpha {
			return e.s
		}
	}
	s := &stilus.MeshShader{Alpha: alpha}
	if !s.Set(sh.tris, m, sh.meshRamp) {
		return nil
	}
	if len(c.entries) < maxMeshShaders {
		c.entries = append(c.entries, meshEntry{m: m, alpha: alpha, s: s})
	}
	return s
}

// Compilation limits.
const (
	rampSize      = 256     // samples of an axial or radial ramp
	meshRampSize  = 1024    // entries of a mesh's ramp
	maxMeshTris   = 1 << 16 // triangles per shading
	gridSize      = 64      // samples per side of a type 1 grid
	fineGridSize  = 256     // when neighbouring samples differ visibly
	patchPixels   = 6       // device pixels per patch subdivision step
	maxPatchSteps = 32      // subdivisions per patch side
)

// shadeKey identifies a compiled shading: patches are subdivided for a
// power-of-two range of device scales.
type shadeKey struct {
	ref   reader.Ref
	level int
}

// shading compiles the shading o (a dictionary or stream, or a reference
// to one) to be drawn under m. bad reports a shading that cannot be read;
// budget one that was simplified.
func (d *Document) shading(o reader.Object, res reader.Dict, m Matrix) (sh *Shading, bad, budget bool) {
	ref, isRef := o.(reader.Ref)
	o = d.resolve(o)
	dict, ok := reader.ToDict(o)
	s, isStream := reader.ToStream(o)
	if isStream {
		dict, ok = s.Dict, true
	}
	if !ok {
		return nil, true, false
	}
	typ, _ := d.integer(dict["ShadingType"])
	level := 0
	if typ >= 6 {
		level = int(math.Ceil(math.Log2(max(sigmaMax(m), 1e-3))))
	}
	key := shadeKey{ref, level}
	if isRef {
		d.shMu.Lock()
		sh, ok := d.shades[key]
		d.shMu.Unlock()
		if ok {
			return sh, sh == nil, false
		}
	}
	sh, budget = d.compileShading(typ, dict, s, res, math.Exp2(float64(level)))
	if isRef {
		d.shMu.Lock()
		if d.shades == nil {
			d.shades = map[shadeKey]*Shading{}
		}
		d.shades[key] = sh
		d.shMu.Unlock()
	}
	return sh, sh == nil, budget
}

func (d *Document) compileShading(typ int, dict reader.Dict, s *reader.Stream, res reader.Dict, scale float64) (*Shading, bool) {
	cs, _ := d.colorSpace(dict["ColorSpace"], res, 0)
	if cs == nil || cs.kind == csPattern {
		return nil, false
	}
	sh := &Shading{Type: typ}
	if r, ok := d.rect(dict["BBox"]); ok {
		sh.bbox, sh.hasBBox = r, true
	}
	if bg := d.floats(dict["Background"]); len(bg) >= cs.n {
		r, g, b := cs.rgb(bg)
		sh.bg, sh.hasBg = premul(r, g, b, 1), true
	}
	var fn function
	if f, ok := dict["Function"]; ok {
		if fn = d.function(f, 0); fn == nil || fn.outputs() < cs.n {
			return nil, false
		}
	}
	budget := false
	switch typ {
	case 1:
		if fn == nil || !d.functionGrid(sh, dict, fn, cs) {
			return nil, false
		}
	case 2, 3:
		n := 4
		if typ == 3 {
			n = 6
		}
		c := d.floats(dict["Coords"])
		if fn == nil || len(c) != n {
			return nil, false
		}
		copy(sh.coords[:], c)
		t0, t1 := 0.0, 1.0
		if dm := d.floats(dict["Domain"]); len(dm) == 2 {
			t0, t1 = dm[0], dm[1]
		}
		if e, ok := reader.ToArray(d.resolve(dict["Extend"])); ok && len(e) == 2 {
			sh.extend = [2]bool{d.boolean(e[0]), d.boolean(e[1])}
		}
		sh.ramp, sh.knots = shadingRamp(fn, cs, t0, t1)
		if typ == 3 && (c[2] < 0 || c[5] < 0) {
			return nil, false
		}
	case 4, 5, 6, 7:
		if s == nil {
			return nil, false
		}
		m := &meshReader{doc: d, dict: dict, cs: cs, fn: fn, scale: scale}
		if !m.read(typ, d.r.DecodeStreamRecovering(s).Data) {
			return nil, false
		}
		if len(m.tris) == 0 {
			return nil, false
		}
		sh.tris, budget = m.tris, m.budget
		sh.meshRamp = m.ramp
		sh.meshBox = meshBounds(m.tris)
		sh.meshes = new(meshCache)
	default:
		return nil, false
	}
	if sh.hasBg {
		c := *sh
		c.useBg = true
		sh.withBg = &c
	}
	return sh, budget
}

// rgbOf converts a colour value of cs to a premultiplied opaque pixel.
func rgbOf(cs *colorSpace, v []float64) uint32 {
	r, g, b := cs.rgb(v)
	return stilus.PackRGBA(premul(r, g, b, 1))
}

// shadingRamp samples f over [t0, t1] into a ramp with knots: evenly
// spaced samples, and each bound of a stitching function twice, with the
// colours on either side, so that a colour break stays where it is.
func shadingRamp(f function, cs *colorSpace, t0, t1 float64) (stilus.Ramp, []float32) {
	var breaks []float64
	stitchBounds(f, &breaks)
	at := func(t float64) uint32 { return rgbOf(cs, f.eval([]float64{t})) }
	ramp := make(stilus.Ramp, 0, rampSize+2*len(breaks))
	knots := make([]float32, 0, cap(ramp))
	var ks []float64
	for _, b := range breaks {
		if t1 != t0 {
			if k := (b - t0) / (t1 - t0); k > 0 && k < 1 {
				ks = append(ks, k)
			}
		}
	}
	slices.Sort(ks)
	ks = slices.Compact(ks)
	eps := 1e-9 * max(math.Abs(t1-t0), 1)
	for i := range rampSize {
		k := float64(i) / (rampSize - 1)
		for len(ks) > 0 && ks[0] <= k {
			b := ks[0]
			ks = ks[1:]
			t := t0 + b*(t1-t0)
			left := t - eps
			if t1 < t0 {
				left = t + eps
			}
			ramp = append(ramp, at(left), at(t))
			knots = append(knots, float32(b), float32(b))
		}
		if n := len(knots); n > 0 && float64(knots[n-1]) >= k {
			continue // a break at the sample
		}
		ramp = append(ramp, at(t0+k*(t1-t0)))
		knots = append(knots, float32(k))
	}
	knots[len(knots)-1] = 1
	return ramp, knots
}

// stitchBounds appends the bounds of the stitching functions in f, in the
// input space of f.
func stitchBounds(f function, dst *[]float64) {
	switch f := f.(type) {
	case *fnArray:
		for _, p := range f.parts {
			stitchBounds(p, dst)
		}
	case *stitchingFn:
		*dst = append(*dst, f.bounds...)
	}
}

// functionGrid samples a type 1 shading's function on a grid.
func (d *Document) functionGrid(sh *Shading, dict reader.Dict, fn function, cs *colorSpace) bool {
	dom := [4]float64{0, 1, 0, 1}
	if v := d.floats(dict["Domain"]); len(v) == 4 {
		copy(dom[:], v)
	}
	if !(dom[1] != dom[0] && dom[3] != dom[2]) {
		return false
	}
	fm := stilus.Identity
	if v := d.floats(dict["Matrix"]); len(v) == 6 {
		fm = Matrix(v)
	}
	sample := func(n int) []uint32 {
		pix := make([]uint32, n*n)
		in := make([]float64, 2)
		for j := range n {
			in[1] = dom[2] + (float64(j)+0.5)*(dom[3]-dom[2])/float64(n)
			for i := range n {
				in[0] = dom[0] + (float64(i)+0.5)*(dom[1]-dom[0])/float64(n)
				pix[j*n+i] = rgbOf(cs, fn.eval(in))
			}
		}
		return pix
	}
	n := gridSize
	pix := sample(n)
	if maxStep(pix, n) > 4 {
		n = fineGridSize
		pix = sample(n)
	}
	sh.grid = stilus.NewTexture(stilus.Plane{Kind: stilus.PlaneRGBA, W: n, H: n, Stride: n, Pix32: pix})
	sh.gridW, sh.gridH = n, n
	sh.gridM = stilus.Scale((dom[1]-dom[0])/float64(n), (dom[3]-dom[2])/float64(n)).
		Mul(stilus.Translate(dom[0], dom[2])).Mul(fm)
	return true
}

// maxStep returns the largest difference of a channel between
// neighbouring pixels of an n × n grid.
func maxStep(pix []uint32, n int) int {
	diff := func(a, b uint32) int {
		d := 0
		for s := 0; s < 32; s += 8 {
			x, y := int(a>>s&0xff), int(b>>s&0xff)
			d = max(d, x-y, y-x)
		}
		return d
	}
	best := 0
	for j := range n {
		for i := range n {
			c := pix[j*n+i]
			if i+1 < n {
				best = max(best, diff(c, pix[j*n+i+1]))
			}
			if j+1 < n {
				best = max(best, diff(c, pix[(j+1)*n+i]))
			}
		}
	}
	return best
}

func meshBounds(tris []stilus.MeshTriangle) Rect {
	r := Rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for i := range tris {
		for _, v := range tris[i] {
			x, y := float64(v.X), float64(v.Y)
			r = Rect{min(r.X0, x), min(r.Y0, y), max(r.X1, x), max(r.Y1, y)}
		}
	}
	return r
}

// meshReader decodes the vertex data of shading types 4 to 7.
type meshReader struct {
	doc   *Document
	dict  reader.Dict
	cs    *colorSpace
	fn    function
	scale float64 // device pixels per unit of shading space, a bound

	bc, bp, bf int       // bits per coordinate, component, flag
	decode     []float64 // xmin xmax ymin ymax, then a pair per component
	ncomp      int       // components per vertex: 1 with a function
	br         bitReader

	ramp   stilus.Ramp
	t0, t1 float64 // the range of t, with a function
	tris   []stilus.MeshTriangle
	budget bool
}

// meshVertex is a decoded vertex: a point and its colour components (or
// t).
type meshVertex struct {
	x, y float64
	c    [maxComps]float64
}

func (m *meshReader) read(typ int, data []byte) bool {
	d := m.doc
	m.bc, _ = d.integer(m.dict["BitsPerCoordinate"])
	m.bp, _ = d.integer(m.dict["BitsPerComponent"])
	m.bf, _ = d.integer(m.dict["BitsPerFlag"])
	switch m.bc {
	case 1, 2, 4, 8, 12, 16, 24, 32:
	default:
		return false
	}
	switch m.bp {
	case 1, 2, 4, 8, 12, 16:
	default:
		return false
	}
	if typ != 5 {
		switch m.bf {
		case 2, 4, 8:
		default:
			return false
		}
	}
	m.ncomp = m.cs.n
	if m.fn != nil {
		m.ncomp = 1
	}
	m.decode = d.floats(m.dict["Decode"])
	if len(m.decode) < 4+2*m.ncomp {
		return false
	}
	if m.fn != nil {
		m.t0, m.t1 = m.decode[4], m.decode[5]
		m.ramp = make(stilus.Ramp, meshRampSize)
		for i := range m.ramp {
			t := m.t0 + float64(i)/(meshRampSize-1)*(m.t1-m.t0)
			m.ramp[i] = rgbOf(m.cs, m.fn.eval([]float64{t}))
		}
	}
	m.br = bitReader{data: data}
	switch typ {
	case 4:
		m.freeForm()
	case 5:
		perRow, _ := d.integer(m.dict["VerticesPerRow"])
		if perRow < 2 {
			return false
		}
		m.lattice(perRow)
	default:
		m.patches(typ == 7)
	}
	return true
}

// vertex reads one vertex, without its flag.
func (m *meshReader) vertex(v *meshVertex) bool {
	x, ok1 := m.br.read(m.bc)
	y, ok2 := m.br.read(m.bc)
	if !ok1 || !ok2 {
		return false
	}
	v.x = decodeValue(x, m.bc, m.decode[0], m.decode[1])
	v.y = decodeValue(y, m.bc, m.decode[2], m.decode[3])
	return m.colour(v.c[:m.ncomp])
}

// colour reads the components of one colour.
func (m *meshReader) colour(c []float64) bool {
	for i := range c {
		v, ok := m.br.read(m.bp)
		if !ok {
			return false
		}
		c[i] = decodeValue(v, m.bp, m.decode[4+2*i], m.decode[5+2*i])
	}
	return true
}

func decodeValue(v uint64, bits int, lo, hi float64) float64 {
	return lo + float64(v)*(hi-lo)/float64(uint64(1)<<bits-1)
}

// meshVert converts a decoded vertex.
func (m *meshReader) meshVert(v *meshVertex) stilus.MeshVertex {
	out := stilus.MeshVertex{X: float32(v.x), Y: float32(v.y)}
	if m.fn != nil {
		t := 0.0
		if m.t1 != m.t0 {
			t = (v.c[0] - m.t0) / (m.t1 - m.t0)
		}
		out.T = float32(clamp01(t))
	} else {
		out.C = rgbOf(m.cs, v.c[:m.ncomp])
	}
	return out
}

// triangle adds a triangle unless the budget is spent.
func (m *meshReader) triangle(a, b, c *meshVertex) {
	if len(m.tris) >= maxMeshTris {
		m.budget = true
		return
	}
	m.tris = append(m.tris, stilus.MeshTriangle{m.meshVert(a), m.meshVert(b), m.meshVert(c)})
}

// freeForm reads a free-form triangle mesh (type 4).
func (m *meshReader) freeForm() {
	var va, vb, vc meshVertex
	have := 0
	for {
		f, ok := m.br.read(m.bf)
		var v meshVertex
		if !ok || !m.vertex(&v) {
			return
		}
		m.br.align()
		switch {
		case f == 0 || have < 3:
			// A new triangle: this vertex and the next two.
			va = v
			for _, p := range []*meshVertex{&vb, &vc} {
				if _, ok := m.br.read(m.bf); !ok || !m.vertex(p) {
					return
				}
				m.br.align()
			}
			have = 3
		case f == 1:
			va, vb, vc = vb, vc, v
		default:
			vb, vc = vc, v
		}
		m.triangle(&va, &vb, &vc)
	}
}

// lattice reads a lattice-form mesh (type 5).
func (m *meshReader) lattice(perRow int) {
	if perRow > 1<<16 {
		return
	}
	prev := make([]meshVertex, perRow)
	cur := make([]meshVertex, perRow)
	for row := 0; ; row++ {
		for i := range cur {
			if !m.vertex(&cur[i]) {
				return
			}
		}
		m.br.align()
		if row > 0 {
			for i := range perRow - 1 {
				m.triangle(&prev[i], &prev[i+1], &cur[i])
				m.triangle(&prev[i+1], &cur[i+1], &cur[i])
			}
		}
		prev, cur = cur, prev
	}
}

// patch is a tensor-product patch: control points p[i][j], i along u and
// j along v, and the colours of the corners 00, 03, 33 and 30.
type patch struct {
	p [4][4][2]float64
	c [4][maxComps]float64
}

// Positions of the stored control points of a patch, in stream order.
var patchOrder = [16][2]int{
	{0, 0}, {0, 1}, {0, 2}, {0, 3}, {1, 3}, {2, 3}, {3, 3}, {3, 2},
	{3, 1}, {3, 0}, {2, 0}, {1, 0}, {1, 1}, {1, 2}, {2, 2}, {2, 1},
}

// patches reads Coons (type 6) or tensor-product (type 7) patches.
func (m *meshReader) patches(tensor bool) {
	npts := 12
	if tensor {
		npts = 16
	}
	var prev, cur patch
	first := true
	for {
		f, ok := m.br.read(m.bf)
		if !ok {
			return
		}
		k, kc := 0, 0 // stored points and colours taken from prev
		if f != 0 {
			if first {
				return
			}
			// The edge shared with the previous patch becomes p00..p03.
			var edge [4][2]int
			var c0, c1 int
			switch f {
			case 1:
				edge, c0, c1 = [4][2]int{{0, 3}, {1, 3}, {2, 3}, {3, 3}}, 1, 2
			case 2:
				edge, c0, c1 = [4][2]int{{3, 3}, {3, 2}, {3, 1}, {3, 0}}, 2, 3
			default:
				edge, c0, c1 = [4][2]int{{3, 0}, {2, 0}, {1, 0}, {0, 0}}, 3, 0
			}
			for i, e := range edge {
				cur.p[0][i] = prev.p[e[0]][e[1]]
			}
			cur.c[0], cur.c[1] = prev.c[c0], prev.c[c1]
			k, kc = 4, 2
		}
		for ; k < npts; k++ {
			x, ok1 := m.br.read(m.bc)
			y, ok2 := m.br.read(m.bc)
			if !ok1 || !ok2 {
				return
			}
			o := patchOrder[k]
			cur.p[o[0]][o[1]] = [2]float64{
				decodeValue(x, m.bc, m.decode[0], m.decode[1]),
				decodeValue(y, m.bc, m.decode[2], m.decode[3]),
			}
		}
		for ; kc < 4; kc++ {
			if !m.colour(cur.c[kc][:m.ncomp]) {
				return
			}
		}
		m.br.align()
		if !tensor {
			coonsInterior(&cur)
		}
		m.subdivide(&cur)
		prev, first = cur, false
		if m.budget {
			return
		}
	}
}

// coonsInterior sets the inner control points of a Coons patch, given as
// its boundary, so that it is a tensor-product patch (PDF 2.0, 8.7.4.5.8).
func coonsInterior(pt *patch) {
	p := &pt.p
	for c := range 2 {
		at := func(i, j int) float64 { return p[i][j][c] }
		p[1][1][c] = (-4*at(0, 0) + 6*(at(0, 1)+at(1, 0)) - 2*(at(0, 3)+at(3, 0)) + 3*(at(3, 1)+at(1, 3)) - at(3, 3)) / 9
		p[1][2][c] = (-4*at(0, 3) + 6*(at(0, 2)+at(1, 3)) - 2*(at(0, 0)+at(3, 3)) + 3*(at(3, 2)+at(1, 0)) - at(3, 0)) / 9
		p[2][1][c] = (-4*at(3, 0) + 6*(at(3, 1)+at(2, 0)) - 2*(at(3, 3)+at(0, 0)) + 3*(at(0, 1)+at(2, 3)) - at(0, 3)) / 9
		p[2][2][c] = (-4*at(3, 3) + 6*(at(3, 2)+at(2, 3)) - 2*(at(3, 0)+at(0, 3)) + 3*(at(0, 2)+at(2, 0)) - at(0, 0)) / 9
	}
}

// subdivide adds the triangles of a patch, evaluated on a grid fine
// enough for the device scale.
func (m *meshReader) subdivide(pt *patch) {
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i := range 4 {
		for j := range 4 {
			q := pt.p[i][j]
			if math.IsNaN(q[0]) || math.IsNaN(q[1]) || math.IsInf(q[0], 0) || math.IsInf(q[1], 0) {
				return
			}
			x0, x1 = min(x0, q[0]), max(x1, q[0])
			y0, y1 = min(y0, q[1]), max(y1, q[1])
		}
	}
	size := math.Hypot(x1-x0, y1-y0) * m.scale
	n := int(math.Ceil(size / patchPixels))
	n = min(max(n, 1), maxPatchSteps)
	if left := (maxMeshTris - len(m.tris)) / 2; n*n > left {
		n = max(int(math.Sqrt(float64(left))), 1)
		m.budget = true
	}
	var row0, row1 [maxPatchSteps + 1]meshVertex
	for b := 0; b <= n; b++ {
		v := float64(b) / float64(n)
		for a := 0; a <= n; a++ {
			m.patchPoint(pt, float64(a)/float64(n), v, &row1[a])
		}
		if b > 0 {
			for a := range n {
				m.triangle(&row0[a], &row0[a+1], &row1[a+1])
				m.triangle(&row0[a], &row1[a+1], &row1[a])
			}
		}
		row0, row1 = row1, row0
	}
}

// patchPoint evaluates the patch and its colour at (u, v).
func (m *meshReader) patchPoint(pt *patch, u, v float64, out *meshVertex) {
	bu, bv := bernstein(u), bernstein(v)
	var x, y float64
	for i := range 4 {
		for j := range 4 {
			w := bu[i] * bv[j]
			x += pt.p[i][j][0] * w
			y += pt.p[i][j][1] * w
		}
	}
	out.x, out.y = x, y
	for k := range m.ncomp {
		c00, c03, c33, c30 := pt.c[0][k], pt.c[1][k], pt.c[2][k], pt.c[3][k]
		out.c[k] = (1-u)*(1-v)*c00 + (1-u)*v*c03 + u*v*c33 + u*(1-v)*c30
	}
}

// bernstein returns the cubic Bernstein polynomials at t.
func bernstein(t float64) [4]float64 {
	s := 1 - t
	return [4]float64{s * s * s, 3 * t * s * s, 3 * t * t * s, t * t * t}
}

// bitReader reads big-endian bit fields.
type bitReader struct {
	data []byte
	pos  int // in bits
}

// read returns the next n bits (n ≤ 32), false at the end of the data.
func (b *bitReader) read(n int) (uint64, bool) {
	if b.pos+n > 8*len(b.data) {
		return 0, false
	}
	var v uint64
	for n > 0 {
		byt := b.data[b.pos>>3]
		off := b.pos & 7
		take := min(8-off, n)
		v = v<<take | uint64(byt>>(8-off-take))&(1<<take-1)
		b.pos += take
		n -= take
	}
	return v, true
}

// align skips to the next byte boundary.
func (b *bitReader) align() { b.pos = (b.pos + 7) &^ 7 }
