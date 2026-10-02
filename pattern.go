package cera

import (
	"encoding/binary"
	"image"
	"image/color"
	"math"

	"github.com/go-pdfkit/reader"
	"github.com/timzifer/stilus"
)

// Patterns (PDF 2.0, 8.7.3). An object painted with a pattern reaches the
// device as a clip of its shape followed by the pattern over the clip:
// FillShading for a shading pattern, and for a tiling pattern either
// FillTile, one step of the pattern rasterized at device resolution and
// repeated by stilus's wrapping sampler, or, when one cell covers the
// object or a few cells too large for a tile do, the drawing operations
// of each cell (exact vector output).
// The pattern matrix maps pattern space to the default space of the
// content stream whose resources name the pattern.

// Tile is one step of a tiling pattern, XStep × YStep in pattern space,
// rasterized at device resolution into W × H pixels. It is immutable and
// shared by the renders and workers that draw it.
type Tile struct {
	W, H int
	// Stencil reports an uncoloured pattern: the tile is a shape painted
	// in the colour given with the pattern.
	Stencil bool
	tex     *stilus.Texture
}

// pattern is a pattern dictionary, read once per document.
type pattern struct {
	typ    int // 1 tiling, 2 shading
	matrix Matrix

	// Tiling patterns: the cell, its step and its content.
	colored      bool
	bbox         Rect
	xstep, ystep float64
	content      []byte
	res          reader.Dict

	// Shading patterns: the shading, compiled where it is used.
	shading reader.Object
}

// patRef is the pattern of a colour: the pattern, unresolved so that a
// reference serves as a cache key, the default space and the resources of
// the content stream that named it.
type patRef struct {
	o    reader.Object
	base Matrix
	res  reader.Dict
}

// Pattern limits.
const (
	maxPatternDepth = 4        // patterns painted inside pattern cells
	maxReplayCells  = 64       // cells drawn as vector operations
	maxTileSide     = 1024     // pixels of a tile side
	maxTileOffsets  = 64       // copies of a cell drawn into one tile
	maxTileBytes    = 64 << 20 // tiles made for one page
)

// pattern reads the pattern o, nil if it cannot.
func (d *Document) pattern(o reader.Object) *pattern {
	ref, isRef := o.(reader.Ref)
	if isRef {
		d.shMu.Lock()
		p, ok := d.patterns[ref]
		d.shMu.Unlock()
		if ok {
			return p
		}
	}
	p := d.readPattern(d.resolve(o))
	if isRef {
		d.shMu.Lock()
		if d.patterns == nil {
			d.patterns = map[reader.Ref]*pattern{}
		}
		d.patterns[ref] = p
		d.shMu.Unlock()
	}
	return p
}

func (d *Document) readPattern(o reader.Object) *pattern {
	dict, ok := reader.ToDict(o)
	s, isStream := reader.ToStream(o)
	if isStream {
		dict, ok = s.Dict, true
	}
	if !ok {
		return nil
	}
	p := &pattern{matrix: stilus.Identity}
	if m := d.floats(dict["Matrix"]); len(m) == 6 {
		p.matrix = Matrix(m)
	}
	p.typ, _ = d.integer(dict["PatternType"])
	switch p.typ {
	case 1:
		if s == nil {
			return nil
		}
		pt, _ := d.integer(dict["PaintType"])
		p.colored = pt != 2
		var ok1, ok2, ok3 bool
		p.bbox, ok1 = d.rect(dict["BBox"])
		p.xstep, ok2 = d.num(dict["XStep"])
		p.ystep, ok3 = d.num(dict["YStep"])
		if !ok1 || !ok2 || !ok3 || p.xstep == 0 || p.ystep == 0 {
			return nil
		}
		p.res = d.dict(dict["Resources"])
		p.content = d.r.DecodeStreamRecovering(s).Data
	case 2:
		if p.shading = dict["Shading"]; p.shading == nil {
			return nil
		}
	default:
		return nil
	}
	return p
}

// patternPaint returns the pattern, colour space, components and alpha
// of the fill or stroke.
func (in *interp) patternPaint(stroke bool) (*patRef, *colorSpace, []float64, float64) {
	if stroke {
		return &in.gs.strokePat, in.gs.strokeCS, in.gs.stroke[:], in.gs.strokeAlp
	}
	return &in.gs.fillPat, in.gs.fillCS, in.gs.fill[:], in.gs.fillAlpha
}

// paintPattern paints the pattern of the fill or stroke over the clip the
// caller pushed: the shape of an object within the device pixels box.
func (in *interp) paintPattern(stroke bool, box image.Rectangle) {
	pr, cs, v, alpha := in.patternPaint(stroke)
	var comps [maxComps]float64 // drawing the cells changes the state
	copy(comps[:], v)
	a := unit8(alpha)
	if pr.o == nil || a == 0 {
		return
	}
	p := in.doc.pattern(pr.o)
	if p == nil {
		in.st.unsupported("pattern-bad")
		return
	}
	pm := p.matrix.Mul(pr.base)
	switch p.typ {
	case 2:
		sh := in.compileShading(p.shading, pr.res, pm)
		if sh == nil {
			return
		}
		if sh.withBg != nil {
			sh = sh.withBg
		}
		in.dev.FillShading(sh, pm, a)
	case 1:
		var col color.RGBA
		if !p.colored {
			if cs.base == nil {
				in.st.unsupported("pattern-bad")
				return
			}
			r, g, b := cs.base.rgb(comps[:])
			col = premul(r, g, b, alpha)
		}
		in.tiling(p, pr.o, pm, cs.base, comps[:], col, a, box)
	}
}

// compileShading compiles a shading to be drawn under m, counting what
// it cannot read; nil means nothing is drawn.
func (in *interp) compileShading(o reader.Object, res reader.Dict, m Matrix) *Shading {
	sh, bad, budget := in.doc.shading(o, res, m)
	if budget {
		in.st.unsupported("shading-mesh-budget")
	}
	if bad {
		in.st.unsupported("shading-bad")
	}
	return sh
}

// tiling paints tiling pattern p (o) under pm: a replay of its cells if
// few cover box, else its tile.
func (in *interp) tiling(p *pattern, o reader.Object, pm Matrix, base *colorSpace, comps []float64, col color.RGBA, a uint8, box image.Rectangle) {
	if in.patDepth >= maxPatternDepth {
		in.st.unsupported("pattern-budget")
		return
	}
	if !in.devBox.Empty() {
		box = box.Intersect(in.devBox)
	}
	inv, ok := pm.Invert()
	if box.Empty() || !ok || !finite(inv) {
		return
	}
	area := rectUnder(Rect{float64(box.Min.X), float64(box.Min.Y), float64(box.Max.X), float64(box.Max.Y)}, inv)
	i0, i1 := cellRange(area.X0, area.X1, p.bbox.X0, p.bbox.X1, p.xstep)
	j0, j1 := cellRange(area.Y0, area.Y1, p.bbox.Y0, p.bbox.Y1, p.ystep)
	if i0 > i1 || j0 > j1 {
		return
	}
	// The tile repeats without seams, where cells drawn side by side
	// would leave antialiased edges between them; cells too large for a
	// tile are drawn as they are, if there are few.
	cells := (i1 - i0 + 1) * (j1 - j0 + 1)
	large := math.Hypot(pm[0], pm[1])*math.Abs(p.xstep) > maxTileSide ||
		math.Hypot(pm[2], pm[3])*math.Abs(p.ystep) > maxTileSide
	if cells == 1 || large && cells <= maxReplayCells {
		in.replayCells(p, pm, base, comps, a, box, i0, i1, j0, j1)
		return
	}
	t := in.tile(p, o, pm)
	if t == nil {
		return
	}
	paint := Paint{Color: color.RGBA{A: a}}
	if t.Stencil {
		paint.Color = col
	}
	toDevice := stilus.Scale(p.xstep/float64(t.W), p.ystep/float64(t.H)).Mul(pm)
	in.dev.FillTile(t, toDevice, &paint)
}

// cellRange returns the cells k whose box [b0, b1] moved by k·step meets
// [a0, a1].
func cellRange(a0, a1, b0, b1, step float64) (k0, k1 int) {
	lo, hi := (a0-b1)/step, (a1-b0)/step
	if lo > hi {
		lo, hi = hi, lo
	}
	const lim = 1 << 30
	if !(lo > -lim && hi < lim) {
		return 1, 0 // far too many: callers treat as empty or tile
	}
	return int(math.Ceil(lo)), int(math.Floor(hi))
}

// replayCells draws cells [i0, i1] × [j0, j1] of p as drawing operations,
// in a group of opacity a.
func (in *interp) replayCells(p *pattern, pm Matrix, base *colorSpace, comps []float64, a uint8, box image.Rectangle, i0, i1, j0, j1 int) {
	depth := in.depth + 1
	if depth >= maxFormDepth || len(in.stack)+1 >= maxStateDepth {
		in.st.Errors++
		return
	}
	grouped := a != 255
	if grouped {
		g := Group{Isolated: true, Blend: BlendNormal, Alpha: a}
		in.st.Groups++
		in.dev.BeginGroup(Rect{float64(box.Min.X), float64(box.Min.Y), float64(box.Max.X), float64(box.Max.Y)}, stilus.Identity, &g)
	}
	in.pushObject()
	td := in.td
	in.td = nil // cells are not text of the page
	in.patDepth++
	for j := j0; j <= j1 && in.err == nil; j++ {
		for i := i0; i <= i1 && in.err == nil; i++ {
			m := stilus.Translate(float64(i)*p.xstep, float64(j)*p.ystep).Mul(pm)
			in.stack = append(in.stack, in.gs)
			n := len(in.stack)
			in.initState(m)
			in.cellState(p, base, comps)
			in.dev.ClipRect(p.bbox, m)
			in.gs.clips++
			in.content(p.content, p.res, depth)
			in.unwind(n)
			in.restore()
		}
	}
	in.patDepth--
	in.td = td
	in.popObject()
	if grouped {
		in.dev.EndGroup()
	}
}

// cellState prepares the state a cell starts in: an uncoloured cell
// paints in the pattern's colour (or white for a stencil tile, base nil).
func (in *interp) cellState(p *pattern, base *colorSpace, comps []float64) {
	if p.colored {
		return
	}
	cs, v := spaceGray, []float64{1}
	if base != nil {
		cs, v = base, comps
	}
	in.gs.fillCS, in.gs.strokeCS = cs, cs
	copy(in.gs.fill[:], v[:cs.n])
	copy(in.gs.stroke[:], v[:cs.n])
	in.gs.uncolored = true
}

// tileKey identifies a tile: a pattern drawn under the linear part of a
// matrix (the translation only moves the tile).
type tileKey struct {
	o          reader.Object
	a, b, c, d float64
}

// tile returns the tile of p under pm, rasterizing it on first use; nil if
// it cannot be made.
func (in *interp) tile(p *pattern, o reader.Object, pm Matrix) *Tile {
	_, isRef := o.(reader.Ref)
	key := tileKey{o, pm[0], pm[1], pm[2], pm[3]}
	if isRef {
		if t, ok := in.tiles[key]; ok {
			return t
		}
	}
	t := in.makeTile(p, pm)
	if isRef {
		if in.tiles == nil {
			in.tiles = map[tileKey]*Tile{}
		}
		in.tiles[key] = t
	}
	return t
}

// tileSide returns the pixels of a tile side: one step of length step
// along a pattern axis whose unit vector m maps to (x, y) device pixels.
func tileSide(x, y, step float64) int {
	n := math.Ceil(math.Hypot(x, y)*math.Abs(step) - 1e-6)
	if !(n >= 1) {
		return 1
	}
	return int(min(n, maxTileSide))
}

func (in *interp) makeTile(p *pattern, pm Matrix) *Tile {
	w, h := tileSide(pm[0], pm[1], p.xstep), tileSide(pm[2], pm[3], p.ystep)
	if in.tileBytes += 4 * w * h; in.tileBytes > maxTileBytes {
		in.st.unsupported("pattern-budget")
		return nil
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	dev := &RasterDevice{C: stilus.NewCanvas(img)}
	dev.Reset(img, img.Rect)

	sub := recorders.Get().(*interp)
	sub.reset(in.doc, dev, in.st, in.lim)
	sub.ocVis, sub.ocZoom = in.ocVis, in.ocZoom
	sub.devBox, sub.patDepth, sub.tileBytes = img.Rect, in.patDepth+1, in.tileBytes
	toTile := stilus.Scale(float64(w)/p.xstep, float64(h)/p.ystep)
	// The cell is drawn at every offset by whole steps at which its box
	// reaches into the tile, so that what crosses an edge of the step
	// wraps around.
	bx := rectUnder(p.bbox, toTile)
	k0, k1 := int(math.Floor(-bx.X1/float64(w))), int(math.Ceil((float64(w)-bx.X0)/float64(w)))
	l0, l1 := int(math.Floor(-bx.Y1/float64(h))), int(math.Ceil((float64(h)-bx.Y0)/float64(h)))
	if (k1-k0+1)*(l1-l0+1) > maxTileOffsets {
		in.st.unsupported("pattern-budget")
		k1, l1 = min(k1, k0+7), min(l1, l0+7)
	}
	for l := l0; l <= l1 && sub.err == nil; l++ {
		for k := k0; k <= k1 && sub.err == nil; k++ {
			m := stilus.Translate(float64(k)*p.xstep, float64(l)*p.ystep).Mul(toTile)
			sub.initState(m)
			if !p.colored {
				sub.cellState(p, nil, nil)
			}
			dev.ClipRect(p.bbox, m)
			sub.gs.clips++
			sub.content(p.content, p.res, in.depth+1)
			sub.unwind(0)
			sub.popClips(sub.gs.clips)
		}
	}
	err := sub.err
	in.tileBytes = sub.tileBytes
	sub.release()
	recorders.Put(sub)
	if err != nil {
		in.err = err
		return nil
	}
	if dev.Err() != nil {
		in.st.Errors++
	}
	t := &Tile{W: w, H: h, Stencil: !p.colored}
	pl := stilus.Plane{W: w, H: h, Stride: w}
	if t.Stencil {
		pl.Kind, pl.Pal, pl.Pix8 = stilus.PlaneIndex, stilus.AlphaPalette, make([]uint8, w*h)
		for i := range pl.Pix8 {
			pl.Pix8[i] = img.Pix[4*i+3]
		}
	} else {
		pl.Kind, pl.Pix32 = stilus.PlaneRGBA, make([]uint32, w*h)
		for i := range pl.Pix32 {
			pl.Pix32[i] = binary.NativeEndian.Uint32(img.Pix[4*i:])
		}
	}
	t.tex = stilus.NewTexture(pl)
	return t
}
