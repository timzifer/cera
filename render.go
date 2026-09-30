package cera

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"maps"
	"math"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/timzifer/stilus"
)

// RenderOptions control Page.Render.
type RenderOptions struct {
	// Scale is pixels per point; 0 means 1 (72 dpi). 150 dpi is 150/72.
	Scale float64
	// Region restricts drawing to part of the page, in device pixels of the
	// whole page at Scale (a tile or viewport). Empty means dst.Bounds().
	Region image.Rectangle
	// Background fills the region before drawing. The zero value leaves it
	// transparent; use color.RGBA{255, 255, 255, 255} for paper.
	Background color.RGBA
	// Deadline stops interpretation; the image then holds what was drawn
	// and Render returns ErrDeadline. The zero value means no deadline.
	Deadline time.Time
	// Stats, when non-nil, receives what rendering did.
	Stats *Stats
	// Workers is the number of goroutines drawing the page, each taking
	// horizontal bands of it: 0 means GOMAXPROCS, 1 draws on the calling
	// goroutine in one pass. Antialiasing may round differently by one
	// level where bands meet.
	Workers int
}

// Stats describes what rendering a page did.
type Stats struct {
	Ops     int // content-stream operators executed, including forms
	Fills   int
	Strokes int
	Clips   int
	Glyphs  int // glyphs filled, and Type 3 glyphs run
	Images  int // images drawn, inline images and stencil masks included
	Groups  int // transparency groups and soft masks, of forms and objects
	// Unsupported counts features that were skipped or approximated, keyed
	// by feature ("shading", "font-missing", "image-filter", ...).
	Unsupported map[string]int
	// Errors counts recoverable problems: malformed operators, missing
	// resources, content that did not decode.
	Errors int
	// Reused reports that the page was not interpreted again: its display
	// list from an earlier render at the same scale was drawn. The other
	// fields are those of the render that recorded it.
	Reused bool
}

// set copies src into s, reusing s's map.
func (s *Stats) set(src *Stats) {
	um := s.Unsupported
	*s = *src
	clear(um)
	if len(src.Unsupported) > 0 {
		if um == nil {
			um = make(map[string]int, len(src.Unsupported))
		}
		maps.Copy(um, src.Unsupported)
	}
	s.Unsupported = um
}

// UnsupportedKeys returns the keys of s.Unsupported in sorted order.
func (s *Stats) UnsupportedKeys() []string {
	keys := make([]string, 0, len(s.Unsupported))
	for k := range s.Unsupported {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (s *Stats) unsupported(feature string) {
	if s.Unsupported == nil {
		s.Unsupported = map[string]int{}
	}
	s.Unsupported[feature]++
}

// ErrDeadline is returned when rendering stops at RenderOptions.Deadline or
// because the context was cancelled.
var ErrDeadline = errors.New("cera: render deadline exceeded")

// PanicError reports a panic recovered while opening or rendering. It always
// indicates a bug in cera or one of its dependencies.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("cera: panic: %v", e.Value) }

// recoverPanic turns a panic into a *PanicError in *err. Use it deferred.
func recoverPanic(err *error) {
	if v := recover(); v != nil {
		*err = &PanicError{Value: v, Stack: debug.Stack()}
	}
}

// Bounds returns the device-pixel size of the page at scale (pixels per
// point), rounded up.
func (p *Page) Bounds(scale float64) image.Rectangle {
	scale = normScale(scale)
	w, h := p.Size()
	return image.Rect(0, 0, max(1, int(math.Ceil(w*scale))), max(1, int(math.Ceil(h*scale))))
}

func normScale(s float64) float64 {
	if !(s > 0) || math.IsInf(s, 0) {
		return 1
	}
	return s
}

// limit is the deadline of one render, shared by its goroutines.
type limit struct {
	ctx      context.Context
	deadline time.Time
	hit      atomic.Bool
}

func (l *limit) expired() bool {
	if l.hit.Load() {
		return true
	}
	if l.ctx.Err() != nil || (!l.deadline.IsZero() && time.Now().After(l.deadline)) {
		l.hit.Store(true)
		return true
	}
	return false
}

// painter draws display-list bands; painters are pooled, so steady-state
// rendering reuses their canvases.
type painter struct {
	canvas *stilus.Canvas
	dev    RasterDevice
	ds     drawState
}

// noImage is what idle painters point at.
var noImage = image.NewRGBA(image.Rectangle{})

var painters = sync.Pool{New: func() any { return new(painter) }}

// recorders are pooled interpreters.
var recorders = sync.Pool{New: func() any { return new(interp) }}

// Render draws the page into dst, whose pixel coordinates are device pixels
// of the whole page at opt.Scale (see Bounds). dst belongs to the caller and
// can be reused across calls, so rendering does not allocate a page image.
//
// The page is interpreted into a display list once per scale; later renders
// at the same scale (other tiles, a scrolled viewport) only draw. Release
// frees the list. Bands of the page are drawn by opt.Workers goroutines.
//
// Broken content does not stop rendering. A non-nil error means the page
// was drawn partially: ErrDeadline, a rasterizer budget, or a *PanicError.
func (p *Page) Render(ctx context.Context, dst *image.RGBA, opt RenderOptions) (err error) {
	defer recoverPanic(&err)
	region := dst.Bounds()
	if !opt.Region.Empty() {
		region = region.Intersect(opt.Region)
	}
	fillRegion(dst, region, opt.Background)
	st := opt.Stats
	if st == nil {
		st = new(Stats)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lim := &limit{ctx: ctx, deadline: opt.Deadline}
	scale := normScale(opt.Scale)

	dl, reused := p.list(scale, lim)
	defer p.done(dl)
	st.set(&dl.stats)
	st.Reused = reused
	switch {
	case dl.panic != nil:
		err = dl.panic
	case !dl.complete:
		err = ErrDeadline
	}
	if region.Empty() {
		return err
	}
	if derr := dl.render(dst, region, opt.Workers, lim); derr != nil && err == nil {
		err = derr
	}
	return err
}

// list returns the display list of the page at scale, recording it unless
// the cached one fits. The caller must pass it to done.
func (p *Page) list(scale float64, lim *limit) (dl *displayList, reused bool) {
	p.mu.Lock()
	if dl := p.dl; dl != nil && dl.scale == scale {
		dl.refs++
		p.mu.Unlock()
		return dl, true
	}
	p.mu.Unlock()

	dl = getList()
	dl.reset(p.Bounds(scale))
	dl.scale = scale
	in := recorders.Get().(*interp)
	func() {
		defer func() {
			if v := recover(); v != nil {
				// An interpreter that panicked may hold inconsistent state;
				// its list is incomplete and not cached.
				in = nil
				dl.complete = false
				dl.panic = &PanicError{Value: v, Stack: debug.Stack()}
			}
		}()
		dl.complete = p.record(in, dl, &dl.stats, scale, lim)
	}()
	if in != nil {
		in.release()
		recorders.Put(in)
	}
	dl.finish()

	p.mu.Lock()
	dl.refs++
	if dl.complete {
		if old := p.dl; old != nil && old.refs == 0 {
			putList(old)
		}
		p.dl = dl
	}
	p.mu.Unlock()
	return dl, false
}

// record interprets the page into dev at scale and reports whether it got
// to the end.
func (p *Page) record(in *interp, dev Device, st *Stats, scale float64, lim *limit) bool {
	base := p.deviceMatrix(scale)
	dev.ClipRect(p.Box, base)
	in.reset(p.doc, dev, st, lim)
	res := p.doc.dict(p.dict["Resources"])
	dec, derr := p.doc.r.PageContentDecoded(p.index + 1)
	switch {
	case derr != nil:
		st.Errors++
	default:
		if dec.Recovered {
			st.Errors++
		}
		in.run(dec.Data, res, base, 0)
	}
	return in.err == nil
}

// Run interprets the page into dev, without a display list: device space
// is the page at scale pixels per point, as for Render. It is how devices
// other than the raster one (text extraction, hit testing) see a page. The
// page's box is the outermost clip. st, when non-nil, receives the
// statistics. The error is ErrDeadline when ctx ends first, or a
// *PanicError.
func (p *Page) Run(ctx context.Context, dev Device, scale float64, st *Stats) (err error) {
	defer recoverPanic(&err)
	if ctx == nil {
		ctx = context.Background()
	}
	if st == nil {
		st = new(Stats)
	} else {
		st.set(&Stats{})
	}
	lim := &limit{ctx: ctx}
	in := recorders.Get().(*interp)
	ok := p.record(in, dev, st, normScale(scale), lim)
	dev.PopClip()
	in.release()
	recorders.Put(in)
	if !ok {
		return ErrDeadline
	}
	return nil
}

// done ends a render's use of dl.
func (p *Page) done(dl *displayList) {
	p.mu.Lock()
	dl.refs--
	if dl.refs == 0 && p.dl != dl {
		putList(dl)
	}
	p.mu.Unlock()
}

// Release frees the display list cached by Render. The page stays usable.
func (p *Page) Release() {
	p.mu.Lock()
	if dl := p.dl; dl != nil {
		p.dl = nil
		if dl.refs == 0 {
			putList(dl)
		}
	}
	p.mu.Unlock()
}

// render draws the part of the list inside region into dst. One worker
// draws the region in one pass; several share the bands that touch it.
func (l *displayList) render(dst *image.RGBA, region image.Rectangle, workers int, lim *limit) error {
	b0, b1 := l.bandRange(region)
	if b0 >= b1 {
		return nil
	}
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	j := jobs.Get().(*job)
	*j = job{l: l, dst: dst, region: region, lim: lim, b1: b1}
	if workers = min(workers, b1-b0); workers == 1 {
		j.work(true)
	} else {
		j.next.Store(int32(b0))
		j.wg.Add(workers)
		for range workers {
			go j.work(false)
		}
		j.wg.Wait()
	}
	err := j.err
	if err == nil && lim.hit.Load() {
		err = ErrDeadline
	}
	*j = job{}
	jobs.Put(j)
	return err
}

// job is one render of a display list, shared by its workers.
type job struct {
	l      *displayList
	dst    *image.RGBA
	region image.Rectangle
	lim    *limit
	b1     int
	next   atomic.Int32 // next band to draw
	wg     sync.WaitGroup
	mu     sync.Mutex
	err    error
}

var jobs = sync.Pool{New: func() any { return new(job) }}

func (j *job) report(err error) {
	j.mu.Lock()
	if j.err == nil {
		j.err = err
	}
	j.mu.Unlock()
}

// work draws the whole region in one pass, or bands until none are left.
func (j *job) work(whole bool) {
	pt := painters.Get().(*painter)
	defer func() {
		if v := recover(); v != nil {
			// A painter that panicked may hold inconsistent state.
			j.report(&PanicError{Value: v, Stack: debug.Stack()})
			j.lim.hit.Store(true) // stop the other workers
		} else {
			pt.dev.Reset(noImage, noImage.Rect) // do not keep dst alive
			putGlyphCache(pt.dev.glyphs)
			pt.dev.glyphs = nil
			painters.Put(pt)
		}
		if !whole {
			j.wg.Done()
		}
	}()
	if pt.canvas == nil {
		pt.canvas = stilus.NewCanvas(noImage)
		pt.dev.C = pt.canvas
	}
	pt.dev.glyphs = getGlyphCache()
	if whole {
		j.paint(pt, -1, j.region)
		return
	}
	for {
		b := int(j.next.Add(1)) - 1
		if b >= j.b1 || j.lim.hit.Load() {
			return
		}
		j.paint(pt, b, j.l.band(b).Intersect(j.region))
	}
}

// paint draws r, band b of the list or the whole region if b < 0.
func (j *job) paint(pt *painter, b int, r image.Rectangle) {
	pt.dev.Reset(j.dst, r)
	var ok bool
	if b < 0 {
		ok = j.l.drawAll(&pt.dev, &pt.ds, r, j.lim)
	} else {
		ok = j.l.drawBand(&pt.dev, &pt.ds, b, r, j.lim)
	}
	if err := pt.dev.Err(); err != nil {
		j.report(err)
	} else if !ok {
		j.report(ErrDeadline)
	}
}

// deviceMatrix maps default user space to device pixels: y down, origin at
// the top-left corner of the visible box, rotated clockwise by /Rotate.
func (p *Page) deviceMatrix(scale float64) Matrix {
	s := scale * p.unit
	b := p.Box
	switch p.Rotate {
	case 90:
		return Matrix{0, s, s, 0, -b.Y0 * s, -b.X0 * s}
	case 180:
		return Matrix{-s, 0, 0, s, b.X1 * s, -b.Y0 * s}
	case 270:
		return Matrix{0, -s, -s, 0, b.Y1 * s, b.X1 * s}
	}
	return Matrix{s, 0, 0, -s, -b.X0 * s, b.Y1 * s}
}

// fillRegion sets every pixel of r in dst to c by doubling copies.
func fillRegion(dst *image.RGBA, r image.Rectangle, c color.RGBA) {
	if r.Empty() {
		return
	}
	row := dst.Pix[dst.PixOffset(r.Min.X, r.Min.Y):][:4*r.Dx()]
	row[0], row[1], row[2], row[3] = c.R, c.G, c.B, c.A
	for n := 4; n < len(row); n *= 2 {
		copy(row[n:], row[:n])
	}
	for y := r.Min.Y + 1; y < r.Max.Y; y++ {
		copy(dst.Pix[dst.PixOffset(r.Min.X, y):][:len(row)], row)
	}
}

// premul converts straight-alpha components in [0, 1] to premultiplied
// RGBA8.
func premul(r, g, b, a float64) color.RGBA {
	a = clamp01(a)
	return color.RGBA{
		R: uint8(clamp01(r)*a*255 + 0.5),
		G: uint8(clamp01(g)*a*255 + 0.5),
		B: uint8(clamp01(b)*a*255 + 0.5),
		A: uint8(a*255 + 0.5),
	}
}

func clamp01(v float64) float64 {
	if !(v > 0) { // also NaN
		return 0
	}
	return min(v, 1)
}
