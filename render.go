package cera

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"runtime/debug"
	"sort"
	"sync"
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
}

// Stats describes what rendering a page did.
type Stats struct {
	Ops     int // content-stream operators executed, including forms
	Fills   int
	Strokes int
	Clips   int
	// Unsupported counts features that were skipped or approximated, keyed
	// by feature ("text", "image", "shading", ...).
	Unsupported map[string]int
	// Errors counts recoverable problems: malformed operators, missing
	// resources, content that did not decode.
	Errors int
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

// worker is the per-goroutine state of a render: canvas, interpreter and
// their buffers. Workers are pooled, so steady-state rendering reuses them.
type worker struct {
	canvas *stilus.Canvas
	dev    RasterDevice
	in     interp
}

var workers = sync.Pool{New: func() any { return new(worker) }}

// Render draws the page into dst, whose pixel coordinates are device pixels
// of the whole page at opt.Scale (see Bounds). dst belongs to the caller and
// can be reused across calls, so rendering does not allocate a page image.
//
// Broken content does not stop rendering. A non-nil error means the page
// was drawn partially: ErrDeadline, a rasterizer budget, or a *PanicError.
func (p *Page) Render(ctx context.Context, dst *image.RGBA, opt RenderOptions) (err error) {
	region := dst.Bounds()
	if !opt.Region.Empty() {
		region = region.Intersect(opt.Region)
	}
	fillRegion(dst, region, opt.Background)
	if region.Empty() {
		return nil
	}

	w := workers.Get().(*worker)
	if w.canvas == nil {
		w.canvas = stilus.NewCanvas(dst)
		w.dev.C = w.canvas
	}
	w.canvas.Reset(dst, region)
	st := opt.Stats
	if st == nil {
		st = new(Stats)
	}
	*st = Stats{}
	defer func() {
		if v := recover(); v != nil {
			err = &PanicError{Value: v, Stack: debug.Stack()}
			// A worker that panicked may hold inconsistent state.
			return
		}
		w.in.release()
		workers.Put(w)
	}()

	base := p.deviceMatrix(normScale(opt.Scale))
	w.dev.ClipRect(p.Box, base)

	in := &w.in
	in.reset(p.doc, &w.dev, st, ctx, opt.Deadline)
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
	if in.err != nil {
		return in.err
	}
	return w.canvas.Err()
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
