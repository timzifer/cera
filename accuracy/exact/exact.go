// Package exact renders the synthetic drawings of the corpus as ground
// truth: an independent interpreter for the operators they use (paths,
// strokes with every cap and join, fills with both rules, clips, grey and
// RGB colours) that computes what each pixel should show.
//
// Every paint is reduced, per sample row, to the exact x-intervals it
// covers: strokes as the union of their segment quads, caps and joins
// (circles analytically), fills by their winding rule, clipped by the
// intersection of the clip paths. A later paint replaces what it covers.
// Each pixel is the colour integrated exactly along x and sampled at Rows
// rows per pixel along y (the error is below 1/(2·Rows) of a pixel's area
// at horizontal edges, none elsewhere but where paths turn). Colours are
// averaged in their sRGB-encoded values, as renderers composite.
//
// Hairlines (line width 0) are one device pixel wide, the thinnest line
// the PDF specification asks for; renderers differ there by design.
package exact

import (
	"cmp"
	"container/heap"
	"fmt"
	"image"
	"math"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Rows is the number of sample rows per pixel.
const Rows = 64

// flatness is the largest distance, in device pixels, between a Bézier
// curve and its polyline.
const flatness = 0.002

type pt struct{ x, y float64 }

// matrix is a PDF matrix [a b c d e f].
type matrix [6]float64

var identity = matrix{1, 0, 0, 1, 0, 0}

func (m matrix) apply(p pt) pt {
	return pt{m[0]*p.x + m[2]*p.y + m[4], m[1]*p.x + m[3]*p.y + m[5]}
}

// mul is m then n.
func (m matrix) mul(n matrix) matrix {
	return matrix{
		m[0]*n[0] + m[1]*n[2], m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2], m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4], m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

// similarity reports whether m keeps circles circles, and its scale.
func (m matrix) similarity() (float64, bool) {
	a, b, c, d := m[0], m[1], m[2], m[3]
	s1, s2 := math.Hypot(a, b), math.Hypot(c, d)
	return s1, math.Abs(s1-s2) < 1e-9*s1 && math.Abs(a*c+b*d) < 1e-9*s1*s1
}

// piece is a region of a paint: a polygon (filled by the paint's rule) or
// a circle.
type piece struct {
	poly   []pt
	circle bool
	c      pt
	r      float64
	y0, y1 float64
}

func polyPiece(p []pt) piece {
	y0, y1 := math.Inf(1), math.Inf(-1)
	for _, q := range p {
		if !math.IsNaN(q.y) { // subpath separator
			y0, y1 = min(y0, q.y), max(y1, q.y)
		}
	}
	return piece{poly: p, y0: y0, y1: y1}
}

func circlePiece(c pt, r float64) piece {
	return piece{circle: true, c: c, r: r, y0: c.y - r, y1: c.y + r}
}

// region is a set of pieces: the union of convex pieces (a stroke), or one
// polygon set under a winding rule (a fill or clip).
type region struct {
	pieces  []piece
	union   bool // pieces are unioned; else pieces[0] holds all subpaths
	evenOdd bool
	y0, y1  float64
}

func (r *region) add(p piece) {
	if len(r.pieces) == 0 {
		r.y0, r.y1 = p.y0, p.y1
	}
	r.y0, r.y1 = min(r.y0, p.y0), max(r.y1, p.y1)
	r.pieces = append(r.pieces, p)
}

type span struct{ x0, x1 float64 }

// spans appends the intervals of r on the line y, sorted and disjoint.
func (r *region) spans(y float64, out []span, scratch *[]crossing) []span {
	if y < r.y0 || y >= r.y1 {
		return out
	}
	if !r.union {
		return polySpans(r.pieces[0].poly, y, r.evenOdd, out, scratch)
	}
	start := len(out)
	for i := range r.pieces {
		p := &r.pieces[i]
		if y < p.y0 || y >= p.y1 {
			continue
		}
		if p.circle {
			dy := y - p.c.y
			if h := p.r*p.r - dy*dy; h > 0 {
				w := math.Sqrt(h)
				out = append(out, span{p.c.x - w, p.c.x + w})
			}
			continue
		}
		out = polySpans(p.poly, y, false, out, scratch)
	}
	return mergeSpans(out, start)
}

// mergeSpans sorts and merges out[start:].
func mergeSpans(out []span, start int) []span {
	s := out[start:]
	if len(s) < 2 {
		return out
	}
	slices.SortFunc(s, func(a, b span) int { return cmp.Compare(a.x0, b.x0) })
	n := 0
	for _, v := range s[1:] {
		if v.x0 <= s[n].x1 {
			s[n].x1 = max(s[n].x1, v.x1)
			continue
		}
		n++
		s[n] = v
	}
	return out[:start+n+1]
}

type crossing struct {
	x   float64
	dir int
}

// polySpans appends the intervals of the closed polygons in poly (subpaths
// separated by NaN points) on the line y under the winding rule.
func polySpans(poly []pt, y float64, evenOdd bool, out []span, scratch *[]crossing) []span {
	xs := (*scratch)[:0]
	first := 0
	for i := range poly {
		last := i+1 == len(poly) || math.IsNaN(poly[i+1].x)
		if math.IsNaN(poly[i].x) {
			first = i + 1
			continue
		}
		a := poly[i]
		b := poly[first]
		if !last {
			b = poly[i+1]
		}
		if a.y == b.y {
			continue
		}
		dir := 1
		if a.y > b.y {
			a, b, dir = b, a, -1
		}
		if y < a.y || y >= b.y {
			continue
		}
		xs = append(xs, crossing{a.x + (y-a.y)*(b.x-a.x)/(b.y-a.y), dir})
	}
	*scratch = xs
	slices.SortFunc(xs, func(a, b crossing) int { return cmp.Compare(a.x, b.x) })
	w := 0
	for i, c := range xs {
		was := w
		w += c.dir
		in := w != 0
		if evenOdd {
			in = (i+1)%2 == 1
		}
		wasIn := was != 0
		if evenOdd {
			wasIn = i%2 == 1
		}
		switch {
		case in && !wasIn:
			out = append(out, span{x0: c.x})
		case !in && wasIn:
			out[len(out)-1].x1 = c.x
		}
	}
	return out
}

// clip is a clip path and the clip it is intersected with.
type clip struct {
	parent *clip
	r      region
}

// paint is one painting operation.
type paint struct {
	r     region
	color [3]uint8
	clip  *clip
}

// Page is an interpreted page, ready to render.
type Page struct {
	W, H   int // pixels
	paints []paint
}

type gstate struct {
	ctm        matrix
	width      float64
	cap, join  int
	miterLimit float64
	stroke     [3]uint8
	fill       [3]uint8
	clip       *clip
}

// subpath is a polyline in user space.
type subpath struct {
	pts    []pt
	closed bool
}

// Interpret reads content for a page of w×h points with the origin at the
// lower left, drawn at scale pixels per point.
func Interpret(content string, w, h, scale float64) (*Page, error) {
	pg := &Page{W: max(1, int(math.Ceil(w*scale))), H: max(1, int(math.Ceil(h*scale)))}
	device := matrix{scale, 0, 0, -scale, 0, h * scale}
	gs := gstate{ctm: identity, width: 1, miterLimit: 10}
	var stack []gstate
	var path []subpath
	var cur pt
	var pendingClip int // 0, 1 nonzero, 2 even-odd
	var nums []float64
	toDevice := func() matrix { return gs.ctm.mul(device) }

	moveTo := func(p pt) {
		path = append(path, subpath{pts: []pt{p}})
		cur = p
	}
	lineTo := func(p pt) {
		if len(path) == 0 {
			moveTo(p)
			return
		}
		sp := &path[len(path)-1]
		sp.pts = append(sp.pts, p)
		cur = p
	}
	curveTo := func(p1, p2, p3 pt) {
		if len(path) == 0 {
			moveTo(cur)
		}
		m := toDevice()
		sp := &path[len(path)-1]
		flatten(cur, p1, p2, p3, m, &sp.pts)
		cur = p3
	}
	finish := func(op string) error {
		m := toDevice()
		fillPaint := func(evenOdd bool) {
			if r, ok := fillRegion(path, m, evenOdd); ok {
				pg.paints = append(pg.paints, paint{r: r, color: gs.fill, clip: gs.clip})
			}
		}
		strokePaint := func() {
			if r, ok := strokeRegion(path, m, gs); ok {
				pg.paints = append(pg.paints, paint{r: r, color: gs.stroke, clip: gs.clip})
			}
		}
		switch op {
		case "S":
			strokePaint()
		case "s":
			closeLast(path)
			strokePaint()
		case "f", "F":
			fillPaint(false)
		case "f*":
			fillPaint(true)
		case "B", "B*":
			fillPaint(op == "B*")
			strokePaint()
		case "b", "b*":
			closeLast(path)
			fillPaint(op == "b*")
			strokePaint()
		case "n":
		}
		if pendingClip != 0 {
			if r, ok := fillRegion(path, m, pendingClip == 2); ok {
				gs.clip = &clip{parent: gs.clip, r: r}
			} else {
				gs.clip = &clip{parent: gs.clip} // empty: clips everything
			}
			pendingClip = 0
		}
		path = path[:0]
		return nil
	}

	for tok := range strings.FieldsSeq(content) {
		if v, err := strconv.ParseFloat(tok, 64); err == nil {
			nums = append(nums, v)
			continue
		}
		need := map[string]int{
			"q": 0, "Q": 0, "cm": 6, "w": 1, "J": 1, "j": 1, "M": 1, "G": 1, "g": 1, "RG": 3, "rg": 3,
			"m": 2, "l": 2, "c": 6, "v": 4, "y": 4, "h": 0, "re": 4,
			"S": 0, "s": 0, "f": 0, "F": 0, "f*": 0, "B": 0, "B*": 0, "b": 0, "b*": 0, "n": 0, "W": 0, "W*": 0,
		}
		n, ok := need[tok]
		if !ok {
			return nil, fmt.Errorf("exact: operator %q is not supported", tok)
		}
		if len(nums) != n {
			return nil, fmt.Errorf("exact: %s takes %d operands, got %d", tok, n, len(nums))
		}
		a := nums
		switch tok {
		case "q":
			stack = append(stack, gs)
		case "Q":
			if len(stack) == 0 {
				return nil, fmt.Errorf("exact: Q without q")
			}
			gs, stack = stack[len(stack)-1], stack[:len(stack)-1]
		case "cm":
			gs.ctm = matrix(a).mul(gs.ctm)
		case "w":
			gs.width = math.Abs(a[0])
		case "J":
			gs.cap = int(a[0])
		case "j":
			gs.join = int(a[0])
		case "M":
			gs.miterLimit = max(1, a[0])
		case "G":
			gs.stroke = grey(a[0])
		case "g":
			gs.fill = grey(a[0])
		case "RG":
			gs.stroke = rgb(a)
		case "rg":
			gs.fill = rgb(a)
		case "m":
			moveTo(pt{a[0], a[1]})
		case "l":
			lineTo(pt{a[0], a[1]})
		case "c":
			curveTo(pt{a[0], a[1]}, pt{a[2], a[3]}, pt{a[4], a[5]})
		case "v":
			curveTo(cur, pt{a[0], a[1]}, pt{a[2], a[3]})
		case "y":
			curveTo(pt{a[0], a[1]}, pt{a[2], a[3]}, pt{a[2], a[3]})
		case "h":
			closeLast(path)
			if len(path) > 0 {
				cur = path[len(path)-1].pts[0]
			}
		case "re":
			x, y, w, h := a[0], a[1], a[2], a[3]
			moveTo(pt{x, y})
			lineTo(pt{x + w, y})
			lineTo(pt{x + w, y + h})
			lineTo(pt{x, y + h})
			closeLast(path)
			cur = pt{x, y}
		case "W":
			pendingClip = 1
		case "W*":
			pendingClip = 2
		default:
			if err := finish(tok); err != nil {
				return nil, err
			}
		}
		nums = nums[:0]
	}
	if len(nums) > 0 {
		return nil, fmt.Errorf("exact: %d operands without operator", len(nums))
	}
	return pg, nil
}

func grey(v float64) [3]uint8 {
	g := level(v)
	return [3]uint8{g, g, g}
}

func rgb(a []float64) [3]uint8 { return [3]uint8{level(a[0]), level(a[1]), level(a[2])} }

func level(v float64) uint8 { return uint8(math.Round(255 * min(1, max(0, v)))) }

func closeLast(path []subpath) {
	if len(path) > 0 {
		path[len(path)-1].closed = true
	}
}

// flatten appends the Bézier curve p0…p3 (user space) as a polyline whose
// distance from the curve under m is below flatness pixels.
func flatten(p0, p1, p2, p3 pt, m matrix, out *[]pt) {
	d0, d1, d2, d3 := m.apply(p0), m.apply(p1), m.apply(p2), m.apply(p3)
	// The control polygon bounds the curve; its second differences bound
	// the deviation of a uniform subdivision (Wang's formula).
	dd := max(math.Hypot(d0.x-2*d1.x+d2.x, d0.y-2*d1.y+d2.y), math.Hypot(d1.x-2*d2.x+d3.x, d1.y-2*d2.y+d3.y))
	n := max(1, int(math.Ceil(math.Sqrt(3*dd/(4*flatness)))))
	n = min(n, 1<<14)
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		u := 1 - t
		a, b, c, d := u*u*u, 3*u*u*t, 3*u*t*t, t*t*t
		*out = append(*out, pt{a*p0.x + b*p1.x + c*p2.x + d*p3.x, a*p0.y + b*p1.y + c*p2.y + d*p3.y})
	}
}

// fillRegion is the path under m as one polygon set.
func fillRegion(path []subpath, m matrix, evenOdd bool) (region, bool) {
	var poly []pt
	for _, sp := range path {
		if len(sp.pts) < 3 {
			continue
		}
		if len(poly) > 0 {
			poly = append(poly, pt{math.NaN(), math.NaN()})
		}
		for _, p := range sp.pts {
			poly = append(poly, m.apply(p))
		}
	}
	if len(poly) == 0 {
		return region{}, false
	}
	var r region
	r.add(polyPiece(poly))
	r.evenOdd = evenOdd
	return r, true
}

// strokeRegion is the stroke of path: the union of a quad per segment, the
// caps of open subpaths and the joins between segments. A hairline is
// stroked in device space, one pixel wide.
func strokeRegion(path []subpath, m matrix, gs gstate) (region, bool) {
	w := gs.width
	toDev := m
	hairline := w == 0
	if hairline {
		w, toDev = 1, identity
	}
	scale, circles := toDev.similarity()
	hw := w / 2
	r := region{union: true}
	disk := func(c pt) {
		if circles {
			r.add(circlePiece(toDev.apply(c), hw*scale))
			return
		}
		n := 256
		poly := make([]pt, n)
		for i := range poly {
			a := 2 * math.Pi * float64(i) / float64(n)
			poly[i] = toDev.apply(pt{c.x + hw*math.Cos(a), c.y + hw*math.Sin(a)})
		}
		r.add(polyPiece(poly))
	}
	quad := func(ps ...pt) {
		poly := make([]pt, len(ps))
		for i, p := range ps {
			poly[i] = toDev.apply(p)
		}
		r.add(polyPiece(poly))
	}
	for _, sp := range path {
		pts := sp.pts
		if hairline {
			// Hairline: the polyline in device space.
			pts = make([]pt, len(sp.pts))
			for i, p := range sp.pts {
				pts[i] = m.apply(p)
			}
		}
		pts = dedup(pts)
		if sp.closed && len(pts) > 1 && pts[0] == pts[len(pts)-1] {
			pts = pts[:len(pts)-1]
		}
		if len(pts) == 1 {
			if gs.cap == 1 {
				disk(pts[0])
			}
			continue
		}
		n := len(pts)
		segs := n - 1
		if sp.closed {
			segs = n
		}
		for i := range segs {
			a, b := pts[i], pts[(i+1)%n]
			d := unit(b.x-a.x, b.y-a.y)
			if gs.cap == 2 && !sp.closed {
				if i == 0 {
					a = pt{a.x - d.x*hw, a.y - d.y*hw}
				}
				if i == segs-1 {
					b = pt{b.x + d.x*hw, b.y + d.y*hw}
				}
			}
			nx, ny := -d.y*hw, d.x*hw
			quad(pt{a.x + nx, a.y + ny}, pt{b.x + nx, b.y + ny}, pt{b.x - nx, b.y - ny}, pt{a.x - nx, a.y - ny})
		}
		if !sp.closed && gs.cap == 1 {
			disk(pts[0])
			disk(pts[n-1])
		}
		// Joins at interior vertices, and at every vertex of a closed path.
		for i := range n {
			if !sp.closed && (i == 0 || i == n-1) {
				continue
			}
			prev, v, next := pts[(i+n-1)%n], pts[i], pts[(i+1)%n]
			if gs.join == 1 {
				disk(v)
				continue
			}
			d1, d2 := unit(v.x-prev.x, v.y-prev.y), unit(next.x-v.x, next.y-v.y)
			cross := d1.x*d2.y - d1.y*d2.x
			if math.Abs(cross) < 1e-12 && d1.x*d2.x+d1.y*d2.y > 0 {
				continue // straight on
			}
			// The outer side is to the right of a left turn.
			s := 1.0
			if cross > 0 {
				s = -1
			}
			o1 := pt{v.x - d1.y*hw*s, v.y + d1.x*hw*s}
			o2 := pt{v.x - d2.y*hw*s, v.y + d2.x*hw*s}
			cosT := -(d1.x*d2.x + d1.y*d2.y) // cosine of the angle between the segments
			theta := math.Acos(min(1, max(-1, cosT)))
			if gs.join == 0 && theta > 1e-9 && 1/math.Sin(theta/2) <= gs.miterLimit {
				// The miter tip, along the bisector of the outer offsets.
				bx, by := (o1.x+o2.x)/2-v.x, (o1.y+o2.y)/2-v.y
				l := math.Hypot(bx, by)
				tip := hw / math.Sin(theta/2)
				quad(v, o1, pt{v.x + bx/l*tip, v.y + by/l*tip}, o2)
				continue
			}
			quad(v, o1, o2)
		}
	}
	return r, len(r.pieces) > 0
}

func dedup(p []pt) []pt {
	out := p[:0:0]
	for i, q := range p {
		if i == 0 || q != out[len(out)-1] {
			out = append(out, q)
		}
	}
	return out
}

func unit(x, y float64) pt {
	l := math.Hypot(x, y)
	return pt{x / l, y / l}
}

// Render renders pg on white.
func (pg *Page) Render() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, pg.W, pg.H))
	// Paints binned by pixel row.
	rows := make([][]int32, pg.H)
	for i := range pg.paints {
		r := &pg.paints[i].r
		y0, y1 := max(0, int(math.Floor(r.y0))), min(pg.H-1, int(math.Floor(r.y1)))
		for y := y0; y <= y1; y++ {
			rows[y] = append(rows[y], int32(i))
		}
	}
	var wg sync.WaitGroup
	next := make(chan int, pg.H)
	for y := range pg.H {
		next <- y
	}
	close(next)
	for range runtime.NumCPU() {
		wg.Go(func() {
			rr := newRowRenderer(pg.W)
			for y := range next {
				rr.row(pg, y, rows[y], img.Pix[y*img.Stride:])
			}
		})
	}
	wg.Wait()
	return img
}

type rowRenderer struct {
	acc     []float64 // per pixel: covered length × colour, 3 channels, and covered length
	spans   []span
	clipBuf []span
	tmp     []span
	events  []event
	cross   []crossing
	active  activeHeap
	clipped map[*clip][]span
}

type event struct {
	x     float64
	order int32
	start bool
}

func newRowRenderer(w int) *rowRenderer {
	return &rowRenderer{acc: make([]float64, 4*w), clipped: map[*clip][]span{}}
}

// row renders pixel row y into pix.
func (rr *rowRenderer) row(pg *Page, y int, paints []int32, pix []byte) {
	clear(rr.acc)
	for k := range Rows {
		sy := float64(y) + (float64(k)+0.5)/Rows
		clear(rr.clipped)
		rr.events = rr.events[:0]
		for _, i := range paints {
			p := &pg.paints[i]
			rr.spans = p.r.spans(sy, rr.spans[:0], &rr.cross)
			if len(rr.spans) == 0 {
				continue
			}
			if p.clip != nil {
				cs := rr.clipSpans(p.clip, sy)
				rr.tmp = intersect(rr.spans, cs, rr.tmp[:0])
				rr.spans, rr.tmp = rr.tmp, rr.spans
			}
			for _, s := range rr.spans {
				rr.events = append(rr.events, event{s.x0, i, true}, event{s.x1, i, false})
			}
		}
		rr.sweep(pg, float64(pg.W))
	}
	for x := range pg.W {
		a := rr.acc[4*x : 4*x+4]
		cov := min(1, a[3]/Rows)
		for c := range 3 {
			// Uncovered area is white paper.
			v := a[c]/Rows + 255*(1-cov)
			pix[4*x+c] = uint8(math.Round(min(255, max(0, v))))
		}
		pix[4*x+3] = 255
	}
}

// clipSpans is the intersection of the clip chain c on the line y.
func (rr *rowRenderer) clipSpans(c *clip, y float64) []span {
	if s, ok := rr.clipped[c]; ok {
		return s
	}
	var s []span
	if len(c.r.pieces) > 0 {
		s = c.r.spans(y, nil, &rr.cross)
	}
	if c.parent != nil {
		s = intersect(s, rr.clipSpans(c.parent, y), nil)
	}
	rr.clipped[c] = s
	return s
}

// intersect appends the intersection of the sorted disjoint a and b.
func intersect(a, b []span, out []span) []span {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		x0, x1 := max(a[i].x0, b[j].x0), min(a[i].x1, b[j].x1)
		if x0 < x1 {
			out = append(out, span{x0, x1})
		}
		if a[i].x1 < b[j].x1 {
			i++
		} else {
			j++
		}
	}
	return out
}

// sweep resolves the spans of one sample row, the latest paint on top, and
// adds each covered length and colour to its pixels.
func (rr *rowRenderer) sweep(pg *Page, w float64) {
	ev := rr.events
	slices.SortFunc(ev, func(a, b event) int { return cmp.Compare(a.x, b.x) })
	h := &rr.active
	*h = (*h)[:0]
	ended := map[int32]int{}
	x := 0.0
	for _, e := range ev {
		// Drop paints that ended.
		for h.Len() > 0 && ended[(*h)[0]] > 0 {
			ended[(*h)[0]]--
			heap.Pop(h)
		}
		if h.Len() > 0 && e.x > x {
			rr.cover(x, e.x, pg.paints[(*h)[0]].color, w)
		}
		x = e.x
		if e.start {
			heap.Push(h, e.order)
		} else {
			ended[e.order]++
		}
	}
}

// cover adds colour col over [x0, x1) of the current sample row.
func (rr *rowRenderer) cover(x0, x1 float64, col [3]uint8, w float64) {
	x0, x1 = max(0, x0), min(w, x1)
	for x0 < x1 {
		px := int(x0)
		end := min(x1, float64(px+1))
		l := end - x0
		a := rr.acc[4*px : 4*px+4]
		a[0] += l * float64(col[0])
		a[1] += l * float64(col[1])
		a[2] += l * float64(col[2])
		a[3] += l
		x0 = end
	}
}

// activeHeap keeps the latest active paint on top.
type activeHeap []int32

func (h activeHeap) Len() int           { return len(h) }
func (h activeHeap) Less(i, j int) bool { return h[i] > h[j] }
func (h activeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *activeHeap) Push(x any)        { *h = append(*h, x.(int32)) }
func (h *activeHeap) Pop() any {
	old := *h
	v := old[len(old)-1]
	*h = old[:len(old)-1]
	return v
}
