package cera

import (
	"image"
	"image/color"
	"math"
	"slices"
	"sync"

	"github.com/timzifer/stilus"
)

// The display list records what the interpreter draws on a page at one
// scale, so a page is parsed once and then drawn any number of times: for
// several tiles or viewports, and by several workers in parallel. Every item
// carries its device-space bounding box, already clipped by the clips around
// it, and a band index lists for each horizontal band of the page the items
// that touch it. Drawing a band touches nothing else.

type dlOp uint8

const (
	dlFill dlOp = iota
	dlStroke
	dlClipPath
	dlClipRect
	dlPopClip
	dlGlyphs
	dlImage
)

// dlItem is one recorded operation.
type dlItem struct {
	op   dlOp
	rule FillRule
	// color is the premultiplied paint of a fill or stroke.
	color color.RGBA
	m     Matrix
	// verbs and points are ranges of the list's path storage; for a stroke
	// style is an index into styles. For glyphs, v0:v1 is a range of glyphs
	// and style an index into fonts.
	v0, v1, p0, p1 int32
	style          int32
	rect           Rect // dlClipRect
	// bbox is the device-space area the item can touch, within the clips
	// around it. A clip and its PopClip share the clip's box, so a band or
	// region that skips one skips the other and everything in between.
	bbox image.Rectangle
}

// dlStyle is a stroke style whose dash pattern is a range of the list's
// dash storage (which may move while recording).
type dlStyle struct {
	st     StrokeStyle // Dash is nil
	d0, d1 int32
}

// displayList is a recorded page. It implements Device.
type displayList struct {
	items  []dlItem
	verbs  []stilus.Verb
	points []stilus.Point
	styles []dlStyle
	dashes []float64
	glyphs []Glyph
	fonts  []*Font
	images []*Image

	// Recording state: the boxes of the open clips, and how many clips
	// are open inside one that is empty (whose content is dropped).
	clips []image.Rectangle
	dead  int

	// bounds is the page in device pixels; bandH the height of a band.
	bounds image.Rectangle
	bandH  int
	// band b lists items bandItems[bandStart[b]:bandStart[b+1]].
	bandStart []int32
	bandItems []int32
	bandFill  []int32

	// What the list was recorded for, and what recording did.
	scale    float64
	stats    Stats
	complete bool
	panic    *PanicError // recovered while recording
	refs     int         // renders using the list; guarded by the page's mutex
}

var lists = sync.Pool{New: func() any { return new(displayList) }}

func getList() *displayList { return lists.Get().(*displayList) }

func putList(l *displayList) {
	l.reset(image.Rectangle{})
	clear(l.stats.Unsupported)
	lists.Put(l)
}

// reset empties the list for recording a page of the given device bounds.
func (l *displayList) reset(bounds image.Rectangle) {
	l.items = l.items[:0]
	l.verbs = l.verbs[:0]
	l.points = l.points[:0]
	l.styles = l.styles[:0]
	l.dashes = l.dashes[:0]
	clear(l.glyphs) // outlines and fonts belong to a document
	l.glyphs = l.glyphs[:0]
	clear(l.fonts)
	l.fonts = l.fonts[:0]
	clear(l.images)
	l.images = l.images[:0]
	l.clips = append(l.clips[:0], bounds)
	l.dead = 0
	l.bounds = bounds
	l.bandStart = l.bandStart[:0]
	l.bandItems = l.bandItems[:0]
	l.complete, l.panic = false, nil
	l.refs = 0
	um := l.stats.Unsupported
	clear(um)
	l.stats = Stats{Unsupported: um}
}

func (l *displayList) clipBox() image.Rectangle { return l.clips[len(l.clips)-1] }

func (l *displayList) addPath(it *dlItem, p *Path) {
	it.v0, it.p0 = int32(len(l.verbs)), int32(len(l.points))
	l.verbs = append(l.verbs, p.Verbs...)
	l.points = append(l.points, p.Points...)
	it.v1, it.p1 = int32(len(l.verbs)), int32(len(l.points))
}

// path returns a view of the path of it; stilus does not modify it.
func (l *displayList) path(it *dlItem) Path {
	return Path{
		Verbs:  l.verbs[it.v0:it.v1:it.v1],
		Points: l.points[it.p0:it.p1:it.p1],
	}
}

func (l *displayList) FillPath(p *Path, m Matrix, rule FillRule, paint *Paint) {
	if l.dead > 0 || flat(p) || paint.Color.A == 0 {
		return
	}
	bb := deviceBox(p, m, 1).Intersect(l.clipBox())
	if bb.Empty() {
		return
	}
	it := dlItem{op: dlFill, rule: rule, color: paint.Color, m: m, bbox: bb}
	l.addPath(&it, p)
	l.items = append(l.items, it)
}

func (l *displayList) StrokePath(p *Path, m Matrix, st *StrokeStyle, paint *Paint) {
	if l.dead > 0 || len(p.Points) == 0 || paint.Color.A == 0 {
		return
	}
	// The widest the outline can reach beyond the path: half the device
	// width (at least one pixel is drawn), times the miter or square-cap
	// extension, plus antialiasing.
	pad := max(st.Width*sigmaMax(m), 1) / 2 * max(st.MiterLimit, 1.5)
	if !(pad < 1<<30) {
		pad = 1 << 30
	}
	bb := deviceBox(p, m, pad+2).Intersect(l.clipBox())
	if bb.Empty() {
		return
	}
	it := dlItem{op: dlStroke, color: paint.Color, m: m, bbox: bb, style: l.style(st)}
	l.addPath(&it, p)
	l.items = append(l.items, it)
}

func (l *displayList) FillGlyphs(run *GlyphRun, paint *Paint) {
	if l.dead > 0 || (paint.Color.A == 0 && paint.Shader == nil) {
		return
	}
	var bb image.Rectangle
	g0 := len(l.glyphs)
	for i := range run.Glyphs {
		g := &run.Glyphs[i]
		if g.Outline == nil {
			continue
		}
		gb := deviceBox(g.Outline, g.M, 1).Intersect(l.clipBox())
		if gb.Empty() {
			continue
		}
		bb = bb.Union(gb)
		l.glyphs = append(l.glyphs, *g)
	}
	if bb.Empty() {
		return
	}
	f := len(l.fonts) - 1
	if f < 0 || l.fonts[f] != run.Font {
		l.fonts = append(l.fonts, run.Font)
		f++
	}
	l.items = append(l.items, dlItem{
		op: dlGlyphs, color: paint.Color, bbox: bb,
		v0: int32(g0), v1: int32(len(l.glyphs)), style: int32(f),
	})
}

func (l *displayList) DrawImage(img *Image, m Matrix, paint *Paint) {
	if l.dead > 0 || paint.Color.A == 0 {
		return
	}
	bb := deviceBoxPoints(unitSquare[:], m, 1).Intersect(l.clipBox())
	if bb.Empty() {
		return
	}
	i := len(l.images) - 1
	if i < 0 || l.images[i] != img {
		l.images = append(l.images, img)
		i++
	}
	l.items = append(l.items, dlItem{op: dlImage, color: paint.Color, m: m, bbox: bb, style: int32(i)})
}

// style returns the index of st in the list, reusing the last one if equal.
func (l *displayList) style(st *StrokeStyle) int32 {
	if n := len(l.styles); n > 0 {
		s := &l.styles[n-1]
		if s.st.Width == st.Width && s.st.Cap == st.Cap && s.st.Join == st.Join &&
			s.st.MiterLimit == st.MiterLimit && s.st.DashPhase == st.DashPhase &&
			equalFloats(l.dashes[s.d0:s.d1], st.Dash) {
			return int32(n - 1)
		}
	}
	s := dlStyle{st: *st, d0: int32(len(l.dashes))}
	s.st.Dash = nil
	l.dashes = append(l.dashes, st.Dash...)
	s.d1 = int32(len(l.dashes))
	l.styles = append(l.styles, s)
	return int32(len(l.styles) - 1)
}

func equalFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (l *displayList) ClipPath(p *Path, m Matrix, rule FillRule) {
	if l.dead > 0 {
		l.dead++
		return
	}
	bb := deviceBox(p, m, 1).Intersect(l.clipBox())
	if flat(p) || bb.Empty() {
		l.dead++
		return
	}
	it := dlItem{op: dlClipPath, rule: rule, m: m, bbox: bb}
	l.addPath(&it, p)
	l.items = append(l.items, it)
	l.clips = append(l.clips, bb)
}

func (l *displayList) ClipRect(r Rect, m Matrix) {
	if l.dead > 0 {
		l.dead++
		return
	}
	corners := [2]stilus.Point{{X: float32(r.X0), Y: float32(r.Y0)}, {X: float32(r.X1), Y: float32(r.Y1)}}
	bb := deviceBoxPoints(corners[:], m, 1).Intersect(l.clipBox())
	if !(r.X0 < r.X1 && r.Y0 < r.Y1) || bb.Empty() {
		l.dead++
		return
	}
	l.items = append(l.items, dlItem{op: dlClipRect, rect: r, m: m, bbox: bb})
	l.clips = append(l.clips, bb)
}

func (l *displayList) PopClip() {
	if l.dead > 0 {
		l.dead--
		return
	}
	if len(l.clips) <= 1 {
		return // unbalanced; the interpreter does not do this
	}
	bb := l.clipBox()
	l.clips = l.clips[:len(l.clips)-1]
	l.items = append(l.items, dlItem{op: dlPopClip, bbox: bb})
}

// finish closes the clips left open and builds the band index.
func (l *displayList) finish() {
	for len(l.clips) > 1 || l.dead > 0 {
		l.PopClip()
	}
	h := l.bounds.Dy()
	l.bandH = bandHeight(h)
	nb := (h + l.bandH - 1) / l.bandH
	l.bandStart = slices.Grow(l.bandStart[:0], nb+1)[:nb+1]
	clear(l.bandStart)
	// Count, then fill (CSR layout).
	for i := range l.items {
		b0, b1 := l.bandRange(l.items[i].bbox)
		for b := b0; b < b1; b++ {
			l.bandStart[b+1]++
		}
	}
	for b := range nb {
		l.bandStart[b+1] += l.bandStart[b]
	}
	total := int(l.bandStart[nb])
	l.bandItems = slices.Grow(l.bandItems[:0], total)[:total]
	l.bandFill = append(l.bandFill[:0], l.bandStart[:nb]...)
	for i := range l.items {
		b0, b1 := l.bandRange(l.items[i].bbox)
		for b := b0; b < b1; b++ {
			l.bandItems[l.bandFill[b]] = int32(i)
			l.bandFill[b]++
		}
	}
	l.clips = l.clips[:0]
}

// bandHeight splits a page into about 16 bands of a multiple of 32 rows,
// at least 64: enough to share the work between cores, few enough that a
// path crossing the page is not set up too often (each band sets up every
// path that touches it again).
func bandHeight(h int) int {
	b := (h/16 + 31) &^ 31
	return max(b, 64)
}

// bandRange returns the bands [b0, b1) that r touches.
func (l *displayList) bandRange(r image.Rectangle) (b0, b1 int) {
	r = r.Intersect(l.bounds)
	if r.Empty() {
		return 0, 0
	}
	b0 = (r.Min.Y - l.bounds.Min.Y) / l.bandH
	b1 = (r.Max.Y - l.bounds.Min.Y + l.bandH - 1) / l.bandH
	return b0, b1
}

// band returns the device rows of band b.
func (l *displayList) band(b int) image.Rectangle {
	y0 := l.bounds.Min.Y + b*l.bandH
	return image.Rect(l.bounds.Min.X, y0, l.bounds.Max.X, min(y0+l.bandH, l.bounds.Max.Y))
}

func (l *displayList) numBands() int { return len(l.bandStart) - 1 }

// drawState holds what draw passes to the device by pointer; it lives in
// the painter so that calls through the Device interface do not allocate.
type drawState struct {
	path  Path
	style StrokeStyle
	paint Paint
	run   GlyphRun
}

// drawBand replays the items of band b that touch r onto dev.
func (l *displayList) drawBand(dev Device, ds *drawState, b int, r image.Rectangle, lim *limit) bool {
	defer func() { *ds = drawState{} }() // keep no references to the list
	for k, i := range l.bandItems[l.bandStart[b]:l.bandStart[b+1]] {
		if k%checkEvery == checkEvery-1 && lim.expired() {
			return false
		}
		if it := &l.items[i]; it.bbox.Overlaps(r) {
			l.drawItem(dev, ds, it)
		}
	}
	return true
}

// drawAll replays the items that touch r onto dev in one pass.
func (l *displayList) drawAll(dev Device, ds *drawState, r image.Rectangle, lim *limit) bool {
	defer func() { *ds = drawState{} }()
	for k := range l.items {
		if k%checkEvery == checkEvery-1 && lim.expired() {
			return false
		}
		if it := &l.items[k]; it.bbox.Overlaps(r) {
			l.drawItem(dev, ds, it)
		}
	}
	return true
}

func (l *displayList) drawItem(dev Device, ds *drawState, it *dlItem) {
	switch it.op {
	case dlFill:
		ds.path = l.path(it)
		ds.paint.Color = it.color
		dev.FillPath(&ds.path, it.m, it.rule, &ds.paint)
	case dlStroke:
		ds.path = l.path(it)
		s := &l.styles[it.style]
		ds.style = s.st
		ds.style.Dash = l.dashes[s.d0:s.d1:s.d1]
		ds.paint.Color = it.color
		dev.StrokePath(&ds.path, it.m, &ds.style, &ds.paint)
	case dlClipPath:
		ds.path = l.path(it)
		dev.ClipPath(&ds.path, it.m, it.rule)
	case dlClipRect:
		dev.ClipRect(it.rect, it.m)
	case dlPopClip:
		dev.PopClip()
	case dlGlyphs:
		ds.run = GlyphRun{Font: l.fonts[it.style], Glyphs: l.glyphs[it.v0:it.v1:it.v1]}
		ds.paint.Color = it.color
		dev.FillGlyphs(&ds.run, &ds.paint)
	case dlImage:
		ds.paint.Color = it.color
		dev.DrawImage(l.images[it.style], it.m, &ds.paint)
	}
}

// flat reports whether p encloses no area because all its points lie on a
// horizontal or vertical line in user space (or it has none): filling it
// draws nothing, clipping to it clips everything away.
func flat(p *Path) bool {
	if len(p.Points) == 0 {
		return true
	}
	q := p.Points[0]
	sameX, sameY := true, true
	for _, r := range p.Points[1:] {
		sameX = sameX && r.X == q.X
		sameY = sameY && r.Y == q.Y
		if !sameX && !sameY {
			return false
		}
	}
	return true
}

// deviceBox returns the device pixels the points of p can touch under m,
// grown by pad pixels. A non-finite result covers everything.
func deviceBox(p *Path, m Matrix, pad float64) image.Rectangle {
	return deviceBoxPoints(p.Points, m, pad)
}

func deviceBoxPoints(pts []stilus.Point, m Matrix, pad float64) image.Rectangle {
	if len(pts) == 0 {
		return image.Rectangle{}
	}
	x0, y0 := math.Inf(1), math.Inf(1)
	x1, y1 := math.Inf(-1), math.Inf(-1)
	for _, q := range pts {
		x, y := float64(q.X), float64(q.Y)
		x0, x1 = min(x0, x), max(x1, x)
		y0, y1 = min(y0, y), max(y1, y)
	}
	// The box of the transformed rectangle is the box of the transformed
	// path.
	var bx0, by0 = math.Inf(1), math.Inf(1)
	var bx1, by1 = math.Inf(-1), math.Inf(-1)
	for _, c := range [4][2]float64{{x0, y0}, {x1, y0}, {x0, y1}, {x1, y1}} {
		x, y := m.Apply(c[0], c[1])
		bx0, bx1 = min(bx0, x), max(bx1, x)
		by0, by1 = min(by0, y), max(by1, y)
	}
	const lim = 1 << 30
	if !(bx0 >= -lim && by0 >= -lim && bx1 <= lim && by1 <= lim) {
		// NaN or huge: let the rasterizer decide.
		return image.Rect(-lim, -lim, lim, lim)
	}
	return image.Rect(
		int(math.Floor(bx0-pad)), int(math.Floor(by0-pad)),
		int(math.Ceil(bx1+pad)), int(math.Ceil(by1+pad)),
	)
}

// sigmaMax returns the largest singular value of the linear part of m: the
// most a unit length in user space can grow in device space.
func sigmaMax(m Matrix) float64 {
	a, b, c, d := m[0], m[1], m[2], m[3]
	s := a*a + b*b + c*c + d*d
	det := a*d - b*c
	q := s*s/4 - det*det
	return math.Sqrt(s/2 + math.Sqrt(max(q, 0)))
}
