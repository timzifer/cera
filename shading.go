package cera

import (
	"image/color"
	"math"
	"slices"
	"sync"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/stilus"
)

// Shadings (PDF 2.0, 8.7.4.5) are read once per document into what a
// device draws without evaluating PDF functions: axial and radial
// shadings into a colour ramp over their parameter, function-based
// shadings into a texture sampled over their domain, and the four mesh
// kinds into Gouraud-shaded triangles, coloured at their vertices or, when
// the shading has a function, carrying its parameter into a ramp. Coons
// and tensor-product patches are cut into triangles on a grid finer the
// fewer patches a mesh has.

// Shading is a smooth shading prepared for drawing. It is immutable and
// shared by the pages of a document and the workers drawing them.
type Shading struct {
	// Type is the ShadingType, 1 to 7.
	Type int
	// Coords are those of an axial (x0 y0 x1 y1) or radial
	// (x0 y0 r0 x1 y1 r1) shading.
	Coords [6]float64
	// Extend continues an axial or radial shading beyond its ends.
	Extend [2]bool
	// Ramp is the colour of an axial or radial shading over its
	// parameter, or of a parametric mesh over the parameter of its
	// vertices (premultiplied, opaque).
	Ramp stilus.Ramp
	// Knots, if not empty, are the parameters of the entries of an axial
	// or radial Ramp: the bounds of stitching functions appear twice,
	// with the colours on either side, so that colour breaks stay exact.
	Knots []float32
	// BBox clips the shading in its own space if HasBBox is set.
	BBox    Rect
	HasBBox bool
	// Background is painted where the shading is not defined, when the
	// shading is the paint of a pattern; transparent if there is none.
	Background color.RGBA

	// Texture is the colour of a function-based shading over Domain,
	// which Matrix maps to shading space; its first row is at the
	// bottom of the domain.
	Texture *stilus.Texture
	Domain  Rect
	Matrix  Matrix

	// Mesh is the triangles of a mesh shading in shading space, and
	// Bounds the box of their vertices.
	Mesh   []stilus.MeshTriangle
	Bounds Rect

	// meshes keeps the shaders that draw Mesh, shared with the copy
	// without background.
	meshes *meshCache
}

// shadingEntry is a shading as read, for the sh operator (plain, without
// background) and for patterns (full); feature names why it cannot be
// drawn, or is empty for a broken one.
type shadingEntry struct {
	full, plain *Shading
	feature     string
}

// Bounds of what is read.
const (
	rampSize        = 512
	functionTexSize = 128
)

// shading reads the shading o (a dictionary or, for meshes, a stream),
// once per document when it is named by reference.
func (d *Document) shading(o pdf.Object, res pdf.Dict) *shadingEntry {
	ref, isRef := o.Ref()
	if isRef {
		d.shMu.Lock()
		e := d.shadings[ref]
		d.shMu.Unlock()
		if e != nil {
			return e
		}
	}
	e := d.readShading(d.resolve(o), res)
	if isRef {
		d.shMu.Lock()
		if d.shadings == nil {
			d.shadings = map[pdf.Ref]*shadingEntry{}
		}
		d.shadings[ref] = e
		d.shMu.Unlock()
	}
	return e
}

func (d *Document) readShading(o pdf.Object, res pdf.Dict) *shadingEntry {
	e := &shadingEntry{}
	dict, ok := o.Dict()
	s, isStream := o.Stream()
	if isStream {
		dict, ok = s.Dict, true
	}
	if !ok {
		return e
	}
	kind, _ := d.integer(dict.Get("ShadingType"))
	cs, approx := d.colorSpace(dict.Get("ColorSpace"), res, 0)
	if cs == nil || cs.kind == csPattern || kind < 1 || kind > 7 {
		return e
	}
	sh := &Shading{Type: kind, Matrix: identity}
	var fn function
	if f, ok := dict.Lookup("Function"); ok {
		if fn = d.function(f, 0); fn == nil {
			e.feature = "shading-function"
			return e
		}
		if fn.outputs() < cs.n {
			return e
		}
	}
	if bb, ok := d.rect(dict.Get("BBox")); ok {
		sh.BBox, sh.HasBBox = bb, true
	}
	if bg := d.floats(dict.Get("Background")); len(bg) >= cs.n && cs.n > 0 {
		r, g, b := cs.rgb(bg)
		sh.Background = premul(r, g, b, 1)
	}
	switch kind {
	case 1:
		if fn == nil {
			return e
		}
		sh.Domain = Rect{0, 0, 1, 1}
		if dm := d.floats(dict.Get("Domain")); len(dm) == 4 {
			sh.Domain = Rect{dm[0], dm[2], dm[1], dm[3]}
		}
		if m := d.floats(dict.Get("Matrix")); len(m) == 6 {
			sh.Matrix = Matrix(m)
		}
		sh.Texture = functionTexture(cs, fn, sh.Domain)
	case 2, 3:
		c := d.floats(dict.Get("Coords"))
		if fn == nil || len(c) < 2*kind {
			return e
		}
		copy(sh.Coords[:], c)
		t0, t1 := 0.0, 1.0
		if dm := d.floats(dict.Get("Domain")); len(dm) == 2 {
			t0, t1 = dm[0], dm[1]
		}
		if a, ok := d.resolve(dict.Get("Extend")).Array(); ok && len(a) == 2 {
			sh.Extend = [2]bool{d.boolean(a[0]), d.boolean(a[1])}
		}
		sh.Ramp, sh.Knots = knottedRamp(cs, fn, t0, t1)
	default:
		if !isStream {
			return e
		}
		ok, capped := d.readMesh(sh, s, cs, fn)
		if capped {
			e.feature = "mesh-budget"
		}
		if !ok {
			return e
		}
		sh.meshes = new(meshCache)
	}
	if approx != "" && e.feature == "" {
		e.feature = approx
	}
	e.full = sh
	plain := *sh
	plain.Background = color.RGBA{}
	e.plain = &plain
	return e
}

// shadeColor converts the outputs of a shading function (or a colour) in
// cs to an opaque packed colour.
func shadeColor(cs *colorSpace, v []float64) uint32 {
	var c [maxComps]float64
	copy(c[:cs.n], v)
	r, g, b := cs.rgb(c[:cs.n])
	return pack(unit8(r), unit8(g), unit8(b), 255)
}

// ramp tabulates fn from t0 to t1.
func ramp(cs *colorSpace, fn function, t0, t1 float64) stilus.Ramp {
	r := make(stilus.Ramp, rampSize)
	var in [1]float64
	for i := range r {
		in[0] = t0 + (t1-t0)*float64(i)/float64(rampSize-1)
		r[i] = shadeColor(cs, fn.eval(in[:]))
	}
	return r
}

// knottedRamp tabulates fn from t0 to t1 like ramp, with knots at the
// bounds of its stitching functions if it has any: each bound twice, with
// the colours on either side, between evenly spaced samples.
func knottedRamp(cs *colorSpace, fn function, t0, t1 float64) (stilus.Ramp, []float32) {
	var ks []float64
	var breaks []float64
	stitchBounds(fn, &breaks)
	for _, b := range breaks {
		if t1 != t0 {
			if k := (b - t0) / (t1 - t0); k > 0 && k < 1 {
				ks = append(ks, k)
			}
		}
	}
	if len(ks) == 0 {
		return ramp(cs, fn, t0, t1), nil
	}
	slices.Sort(ks)
	ks = slices.Compact(ks)
	var in [1]float64
	at := func(t float64) uint32 {
		in[0] = t
		return shadeColor(cs, fn.eval(in[:]))
	}
	r := make(stilus.Ramp, 0, rampSize+2*len(ks))
	knots := make([]float32, 0, cap(r))
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
			r = append(r, at(left), at(t))
			knots = append(knots, float32(b), float32(b))
		}
		if n := len(knots); n > 0 && float64(knots[n-1]) >= k {
			continue // a break at the sample
		}
		r = append(r, at(t0+k*(t1-t0)))
		knots = append(knots, float32(k))
	}
	knots[len(knots)-1] = 1
	return r, knots
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

// meshShader returns a shader for the mesh of sh under m with alpha, nil
// if it cannot be drawn so.
func (sh *Shading) meshShader(m Matrix, alpha uint8) *stilus.MeshShader {
	c := sh.meshes
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		if e.m == m && e.alpha == alpha {
			return e.s
		}
	}
	s := &stilus.MeshShader{Alpha: alpha}
	if !s.Set(sh.Mesh, m, sh.Ramp) {
		return nil
	}
	if len(c.entries) < maxMeshShaders {
		c.entries = append(c.entries, meshEntry{m: m, alpha: alpha, s: s})
	}
	return s
}

// functionTexture samples a function-based shading over its domain.
func functionTexture(cs *colorSpace, fn function, dom Rect) *stilus.Texture {
	n := functionTexSize
	p := stilus.Plane{Kind: stilus.PlaneRGBA, W: n, H: n, Stride: n, Pix32: make([]uint32, n*n)}
	var in [2]float64
	for j := range n {
		in[1] = dom.Y0 + (float64(j)+0.5)/float64(n)*dom.Dy()
		for i := range n {
			in[0] = dom.X0 + (float64(i)+0.5)/float64(n)*dom.Dx()
			p.Pix32[j*n+i] = shadeColor(cs, fn.eval(in[:]))
		}
	}
	return stilus.NewTexture(p)
}

// meshReader reads the packed vertices of a mesh shading.
type meshReader struct {
	data   []byte
	at     int // bit
	bad    bool
	bpc    int // BitsPerCoordinate
	bpcomp int
	bpf    int
	decode []float64
	ncomp  int // components a vertex carries: 1 with a function
	cs     *colorSpace
	param  bool
	t0, t1 float64 // decode range of the parameter
	capped bool    // stopped at a budget with data left
}

func (r *meshReader) bits(n int) uint64 {
	if r.at+n > 8*len(r.data) {
		r.bad = true
		return 0
	}
	var v uint64
	for n > 0 {
		b := r.data[r.at>>3]
		off := r.at & 7
		take := min(8-off, n)
		v = v<<uint(take) | uint64(b>>(8-uint(off)-uint(take))&(1<<uint(take)-1))
		r.at += take
		n -= take
	}
	return v
}

func (r *meshReader) align() { r.at = (r.at + 7) &^ 7 }

func (r *meshReader) more() bool { return !r.bad && r.at < 8*len(r.data) }

func (r *meshReader) value(bits int, lo, hi float64) float64 {
	maxv := math.Ldexp(1, bits) - 1
	return lo + float64(r.bits(bits))*(hi-lo)/maxv
}

// vertex reads a point and its colour.
func (r *meshReader) vertex() stilus.MeshVertex {
	x := r.value(r.bpc, r.decode[0], r.decode[1])
	y := r.value(r.bpc, r.decode[2], r.decode[3])
	v := stilus.MeshVertex{X: float32(x), Y: float32(y)}
	r.color(&v)
	return v
}

// color reads the colour of a vertex.
func (r *meshReader) color(v *stilus.MeshVertex) {
	var c [maxComps]float64
	for i := range r.ncomp {
		c[i] = r.value(r.bpcomp, r.decode[4+2*i], r.decode[5+2*i])
	}
	if r.param {
		t := 0.0
		if r.t1 != r.t0 {
			t = (c[0] - r.t0) / (r.t1 - r.t0)
		}
		v.T = float32(t)
		return
	}
	v.C = shadeColor(r.cs, c[:r.ncomp])
}

// readMesh reads the vertices of a mesh shading (types 4 to 7) into
// triangles.
func (d *Document) readMesh(sh *Shading, s *pdf.Stream, cs *colorSpace, fn function) (ok, capped bool) {
	dict := s.Dict
	r := &meshReader{cs: cs, ncomp: cs.n, param: fn != nil}
	if fn != nil {
		r.ncomp = 1
	}
	r.bpc, _ = d.integer(dict.Get("BitsPerCoordinate"))
	r.bpcomp, _ = d.integer(dict.Get("BitsPerComponent"))
	r.bpf, _ = d.integer(dict.Get("BitsPerFlag"))
	switch r.bpc {
	case 1, 2, 4, 8, 12, 16, 24, 32:
	default:
		return false, false
	}
	switch r.bpcomp {
	case 1, 2, 4, 8, 12, 16:
	default:
		return false, false
	}
	if sh.Type != 5 && r.bpf != 2 && r.bpf != 4 && r.bpf != 8 {
		return false, false
	}
	r.decode = d.floats(dict.Get("Decode"))
	if len(r.decode) < 4+2*r.ncomp {
		return false, false
	}
	if fn != nil {
		r.t0, r.t1 = r.decode[4], r.decode[5]
		sh.Ramp = ramp(cs, fn, r.t0, r.t1)
	}
	r.data = d.r.Decode(s).Data
	switch sh.Type {
	case 4:
		r.freeForm(sh)
	case 5:
		perRow, _ := d.integer(dict.Get("VerticesPerRow"))
		if perRow < 2 {
			return false, false
		}
		r.lattice(sh, perRow)
	default:
		r.patches(sh)
	}
	if len(sh.Mesh) == 0 {
		return false, r.capped
	}
	b := Rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for i := range sh.Mesh {
		for _, v := range sh.Mesh[i] {
			x, y := float64(v.X), float64(v.Y)
			b = Rect{min(b.X0, x), min(b.Y0, y), max(b.X1, x), max(b.Y1, y)}
		}
	}
	sh.Bounds = b
	return true, r.capped
}

// freeForm reads a type 4 mesh: each vertex starts a triangle (flag 0,
// with the next two) or makes one with two vertices of the last (1: the
// last two, 2: the first and the last).
func (r *meshReader) freeForm(sh *Shading) {
	var a, b, c stilus.MeshVertex
	have := 0
	for r.more() && len(sh.Mesh) < maxMeshTris {
		flag := r.bits(r.bpf)
		v := r.vertex()
		r.align() // every vertex starts on a byte boundary
		if r.bad {
			return
		}
		switch {
		case have < 3 || flag == 0:
			if flag == 0 && have == 3 {
				have = 0
			}
			switch have {
			case 0:
				a = v
			case 1:
				b = v
			default:
				c = v
			}
			if have++; have == 3 {
				sh.Mesh = append(sh.Mesh, stilus.MeshTriangle{a, b, c})
			}
		case flag == 1:
			a, b, c = b, c, v
			sh.Mesh = append(sh.Mesh, stilus.MeshTriangle{a, b, c})
		default:
			b, c = c, v
			sh.Mesh = append(sh.Mesh, stilus.MeshTriangle{a, b, c})
		}
	}
	r.capped = len(sh.Mesh) >= maxMeshTris && r.more()
}

// lattice reads a type 5 mesh: rows of perRow vertices, two triangles
// between neighbours of consecutive rows.
func (r *meshReader) lattice(sh *Shading, perRow int) {
	if perRow > maxMeshRow {
		r.capped = true
		return
	}
	prev := make([]stilus.MeshVertex, 0, perRow)
	row := make([]stilus.MeshVertex, 0, perRow)
	for r.more() && len(sh.Mesh) < maxMeshTris {
		row = row[:0]
		for range perRow {
			row = append(row, r.vertex())
			r.align()
		}
		if r.bad {
			return
		}
		if len(prev) == perRow {
			for i := 0; i+1 < perRow; i++ {
				sh.Mesh = append(sh.Mesh,
					stilus.MeshTriangle{prev[i], prev[i+1], row[i]},
					stilus.MeshTriangle{prev[i+1], row[i+1], row[i]})
			}
		}
		prev, row = row, prev
	}
	r.capped = len(sh.Mesh) >= maxMeshTris && r.more()
}

// patch is a Coons or tensor-product patch: its 4 × 4 control net p[i][j]
// and the colours of its corners p00, p03, p33, p30.
type patch struct {
	p [4][4][2]float64
	c [4]stilus.MeshVertex
}

// The order of the boundary points in the stream, the four inner points of
// a tensor patch, and the points and colours a flag takes from the patch
// before (PDF 2.0, 8.7.4.5.7).
var (
	boundary = [12][2]int{{0, 0}, {0, 1}, {0, 2}, {0, 3}, {1, 3}, {2, 3}, {3, 3}, {3, 2}, {3, 1}, {3, 0}, {2, 0}, {1, 0}}
	inner    = [4][2]int{{1, 1}, {1, 2}, {2, 2}, {2, 1}}
	shared   = [4][4][2]int{1: {{0, 3}, {1, 3}, {2, 3}, {3, 3}}, 2: {{3, 3}, {3, 2}, {3, 1}, {3, 0}}, 3: {{3, 0}, {2, 0}, {1, 0}, {0, 0}}}
	sharedC  = [4][2]int{1: {1, 2}, 2: {2, 3}, 3: {3, 0}}
)

// patches reads a type 6 or 7 mesh and cuts its patches into triangles.
func (r *meshReader) patches(sh *Shading) {
	tensor := sh.Type == 7
	var list []patch
	for r.more() && len(list) < maxPatches {
		flag := int(r.bits(r.bpf)) & 3
		if r.bad || (flag != 0 && len(list) == 0) {
			break
		}
		var p patch
		first := 0
		if flag != 0 {
			q := &list[len(list)-1]
			for i, at := range shared[flag] {
				b := boundary[i]
				p.p[b[0]][b[1]] = q.p[at[0]][at[1]]
			}
			p.c[0], p.c[1] = q.c[sharedC[flag][0]], q.c[sharedC[flag][1]]
			first = 4
		}
		for _, b := range boundary[first:] {
			p.p[b[0]][b[1]] = [2]float64{r.value(r.bpc, r.decode[0], r.decode[1]), r.value(r.bpc, r.decode[2], r.decode[3])}
		}
		if tensor {
			for _, b := range inner {
				p.p[b[0]][b[1]] = [2]float64{r.value(r.bpc, r.decode[0], r.decode[1]), r.value(r.bpc, r.decode[2], r.decode[3])}
			}
		} else {
			p.coons()
		}
		for i := first / 2; i < 4; i++ {
			r.color(&p.c[i])
		}
		r.align()
		if r.bad {
			break
		}
		list = append(list, p)
	}
	r.capped = len(list) >= maxPatches && r.more()
	steps := 16
	switch n := len(list); {
	case n > 4096:
		steps = 2
	case n > 1024:
		steps = 4
	case n > 64:
		steps = 8
	}
	steps = max(1, min(steps, int(math.Sqrt(float64(maxMeshTris/(2*max(len(list), 1)))))))
	grid := make([]stilus.MeshVertex, (steps+1)*(steps+1))
	for i := range list {
		list[i].cut(sh, steps, grid, r.param)
	}
}

// coons sets the inner points of a Coons patch from its boundary.
func (p *patch) coons() {
	at := func(i, j int) [2]float64 { return p.p[i][j] }
	in := func(c, a, b, l1, l2, f1, f2, o [2]float64) [2]float64 {
		var r [2]float64
		for k := range 2 {
			r[k] = (-4*c[k] + 6*(a[k]+b[k]) - 2*(l1[k]+l2[k]) + 3*(f1[k]+f2[k]) - o[k]) / 9
		}
		return r
	}
	p.p[1][1] = in(at(0, 0), at(0, 1), at(1, 0), at(0, 3), at(3, 0), at(3, 1), at(1, 3), at(3, 3))
	p.p[1][2] = in(at(0, 3), at(0, 2), at(1, 3), at(0, 0), at(3, 3), at(3, 2), at(1, 0), at(3, 0))
	p.p[2][1] = in(at(3, 0), at(3, 1), at(2, 0), at(3, 3), at(0, 0), at(0, 1), at(2, 3), at(0, 3))
	p.p[2][2] = in(at(3, 3), at(3, 2), at(2, 3), at(3, 0), at(0, 3), at(0, 2), at(2, 0), at(0, 0))
}

func bernstein(t float64) [4]float64 {
	s := 1 - t
	return [4]float64{s * s * s, 3 * s * s * t, 3 * s * t * t, t * t * t}
}

// cut appends the patch as a grid of steps × steps cells of two triangles.
func (p *patch) cut(sh *Shading, steps int, grid []stilus.MeshVertex, param bool) {
	n := steps + 1
	for i := range n {
		u := float64(i) / float64(steps)
		bu := bernstein(u)
		for j := range n {
			v := float64(j) / float64(steps)
			bv := bernstein(v)
			var x, y float64
			for a := range 4 {
				for b := range 4 {
					w := bu[a] * bv[b]
					x += w * p.p[a][b][0]
					y += w * p.p[a][b][1]
				}
			}
			g := &grid[i*n+j]
			g.X, g.Y = float32(x), float32(y)
			// Corners c0 = p00, c1 = p03, c2 = p33, c3 = p30.
			w := [4]float64{(1 - u) * (1 - v), (1 - u) * v, u * v, u * (1 - v)}
			if param {
				var t float64
				for k := range 4 {
					t += w[k] * float64(p.c[k].T)
				}
				g.T = float32(t)
				continue
			}
			var ch [4]float64
			for k := range 4 {
				r, gg, b, a := unpack(p.c[k].C)
				ch[0] += w[k] * float64(r)
				ch[1] += w[k] * float64(gg)
				ch[2] += w[k] * float64(b)
				ch[3] += w[k] * float64(a)
			}
			g.C = pack(uint8(ch[0]+0.5), uint8(ch[1]+0.5), uint8(ch[2]+0.5), uint8(ch[3]+0.5))
		}
	}
	for i := range steps {
		for j := range steps {
			a, b, c, e := grid[i*n+j], grid[(i+1)*n+j], grid[i*n+j+1], grid[(i+1)*n+j+1]
			sh.Mesh = append(sh.Mesh, stilus.MeshTriangle{a, b, c}, stilus.MeshTriangle{b, e, c})
		}
	}
}
