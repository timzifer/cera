package cera

import (
	"image"
	"math"
	"sync"

	"github.com/timzifer/stilus"
)

// The glyph cache keeps the coverage mask of every glyph a raster device
// has drawn, per size and subpixel position, so a glyph is rasterized once
// and then composited: the text of a page repeats a few dozen glyphs
// thousands of times. Masks are keyed by font, glyph, the linear part of the
// glyph's device matrix (to 1/64 pixel per em) and the position of its
// origin to a quarter pixel in x and y. Composition goes through the
// canvas as a pixel-aligned rectangle with a mask shader, so it honours the
// clip stack like every other fill.
//
// Each raster device (one per worker) has its own cache: no locks while
// drawing, and a mask costs about as much to make as one direct fill of
// the glyph, so a worker that meets a glyph twice has already gained.
// Keys hold a font id, not the font, so a pooled device keeps no document
// alive.

const (
	// maxCachedEm is the em size in device pixels above which glyphs are
	// filled as paths: large glyphs are few, and their masks big.
	maxCachedEm = 160
	// maxMaskArea bounds one mask (glyphs can reach far out of their em).
	maxMaskArea = 400 * 400
	// glyphCacheBytes bounds the masks of one cache; a full cache is
	// emptied.
	glyphCacheBytes = 4 << 20
	subpixel        = 4
)

// glyphCaches keeps the caches of idle painters, most recently used last,
// so that a serial renderer gets its own back. sync.Pool drops its items at
// every other garbage collection, and a cache is worth more than the
// painter around it.
var glyphCaches struct {
	sync.Mutex
	free []*glyphCache
}

const maxIdleGlyphCaches = 64

func getGlyphCache() *glyphCache {
	gcs := &glyphCaches
	gcs.Lock()
	defer gcs.Unlock()
	n := len(gcs.free)
	if n == 0 {
		return new(glyphCache)
	}
	gc := gcs.free[n-1]
	gcs.free[n-1] = nil
	gcs.free = gcs.free[:n-1]
	return gc
}

func putGlyphCache(gc *glyphCache) {
	gcs := &glyphCaches
	gcs.Lock()
	if gc != nil && len(gcs.free) < maxIdleGlyphCaches {
		gcs.free = append(gcs.free, gc)
	}
	gcs.Unlock()
}

type glyphKey struct {
	font       uint64
	gid        int32
	a, b, c, d int32
	fx, fy     uint8
}

type glyphCache struct {
	masks map[glyphKey]*image.Alpha // nil: nothing to draw at this size
	bytes int

	r       *stilus.Rasterizer
	mb      stilus.MaskBlitter
	tmp     image.Alpha // rasterized, before trim
	scratch []uint8
	shader  maskShader
	rect    Path
	paint   Paint
}

// FillGlyphs draws the glyphs of run through the glyph cache, or as paths
// when they are large or the paint is not a solid colour.
func (d *RasterDevice) FillGlyphs(run *GlyphRun, paint *Paint) {
	d.t.inked(paint.Color.A)
	if !d.t.knockout {
		d.fillGlyphs(run, paint)
		return
	}
	var bb image.Rectangle
	for i := range run.Glyphs {
		if g := &run.Glyphs[i]; g.Outline != nil {
			bb = bb.Union(deviceBox(g.Outline, g.M, 1))
		}
	}
	box := d.koBegin(bb)
	d.fillGlyphs(run, paint)
	d.koShape()
	d.fillGlyphs(run, &opaque)
	d.koEnd(box)
}

func (d *RasterDevice) fillGlyphs(run *GlyphRun, paint *Paint) {
	clip := d.C.Clip()
	if clip.Empty() {
		return
	}
	if d.glyphs == nil {
		d.glyphs = new(glyphCache)
	}
	gc := d.glyphs
	for i := range run.Glyphs {
		g := &run.Glyphs[i]
		if g.Outline == nil {
			continue
		}
		m := g.M
		ox, oy := math.Floor(m[4]), math.Floor(m[5])
		if paint.Shader != nil || run.Font == nil || !(sigmaMax(m) <= maxCachedEm) ||
			!(math.Abs(ox) < 1<<24 && math.Abs(oy) < 1<<24) {
			d.C.Fill(g.Outline, m, NonZero, paint)
			continue
		}
		fx := int(math.Round((m[4] - ox) * subpixel))
		fy := int(math.Round((m[5] - oy) * subpixel))
		if fx == subpixel {
			ox, fx = ox+1, 0
		}
		if fy == subpixel {
			oy, fy = oy+1, 0
		}
		origin := image.Pt(int(ox), int(oy))
		key := glyphKey{
			font: run.Font.id, gid: int32(g.GID),
			a: q64(m[0]), b: q64(m[1]), c: q64(m[2]), d: q64(m[3]),
			fx: uint8(fx), fy: uint8(fy),
		}
		mask, ok := gc.masks[key]
		if !ok {
			// The matrix the mask is made with: the quantized linear part
			// and the subpixel phase of the origin.
			mm := Matrix{
				float64(key.a) / 64, float64(key.b) / 64, float64(key.c) / 64, float64(key.d) / 64,
				float64(fx) / subpixel, float64(fy) / subpixel,
			}
			bb := deviceBox(g.Outline, mm, 1)
			if !bb.Add(origin).Overlaps(clip) {
				continue // not cached: this worker may never need it
			}
			if bb.Dx()*bb.Dy() > maxMaskArea {
				d.C.Fill(g.Outline, m, NonZero, paint)
				continue
			}
			mask = gc.rasterize(g.Outline, mm, bb)
			gc.store(key, mask)
		}
		if mask == nil {
			continue
		}
		r := mask.Rect.Add(origin)
		if !r.Overlaps(clip) {
			continue
		}
		gc.shader = maskShader{mask: mask, ox: origin.X, oy: origin.Y, color: stilus.PackRGBA(paint.Color)}
		gc.paint.Shader = &gc.shader
		gc.rect.Reset()
		x0, y0, x1, y1 := float32(r.Min.X), float32(r.Min.Y), float32(r.Max.X), float32(r.Max.Y)
		gc.rect.MoveTo(x0, y0)
		gc.rect.LineTo(x1, y0)
		gc.rect.LineTo(x1, y1)
		gc.rect.LineTo(x0, y1)
		gc.rect.Close()
		d.C.Fill(&gc.rect, stilus.Identity, NonZero, &gc.paint)
	}
	gc.shader.mask = nil
}

// q64 quantizes a matrix entry to 1/64.
func q64(v float64) int32 {
	return int32(math.Round(max(min(v, 1<<24), -1<<24) * 64))
}

// rasterize returns the coverage of outline under m over bb, or nil if it
// covers nothing.
func (gc *glyphCache) rasterize(outline *Path, m Matrix, bb image.Rectangle) *image.Alpha {
	if bb.Empty() {
		return nil
	}
	if gc.r == nil {
		gc.r = stilus.NewRasterizer(bb)
	}
	n := bb.Dx() * bb.Dy()
	if cap(gc.scratch) < n {
		gc.scratch = make([]uint8, n)
	}
	gc.scratch = gc.scratch[:n]
	clear(gc.scratch)
	mask := &gc.tmp
	*mask = image.Alpha{Pix: gc.scratch, Stride: bb.Dx(), Rect: bb}
	gc.r.SetClip(bb)
	gc.mb.Mask = mask
	gc.r.Fill(outline, m, NonZero, &gc.mb)
	gc.mb.Mask = nil
	out := trim(mask)
	mask.Pix = nil
	return out
}

// trim returns a copy of the part of mask that has coverage (every pixel of
// a cached mask is composited), or nil if none has.
func trim(mask *image.Alpha) *image.Alpha {
	r := mask.Rect
	x0, y0, x1, y1 := r.Max.X, r.Max.Y, r.Min.X, r.Min.Y
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := mask.Pix[(y-r.Min.Y)*mask.Stride:][:r.Dx()]
		for x, a := range row {
			if a != 0 {
				x0, x1 = min(x0, r.Min.X+x), max(x1, r.Min.X+x+1)
				y0, y1 = min(y0, y), max(y1, y+1)
			}
		}
	}
	t := image.Rect(x0, y0, x1, y1)
	if t.Empty() {
		return nil
	}
	out := &image.Alpha{Pix: make([]uint8, t.Dx()*t.Dy()), Stride: t.Dx(), Rect: t}
	for y := t.Min.Y; y < t.Max.Y; y++ {
		copy(out.Pix[(y-t.Min.Y)*out.Stride:][:t.Dx()], mask.Pix[mask.PixOffset(t.Min.X, y):])
	}
	return out
}

func (gc *glyphCache) store(key glyphKey, mask *image.Alpha) {
	n := 64 // key and map overhead
	if mask != nil {
		n += len(mask.Pix)
	}
	if gc.masks == nil || gc.bytes+n > glyphCacheBytes {
		if gc.masks == nil {
			gc.masks = make(map[glyphKey]*image.Alpha, 256)
		}
		clear(gc.masks)
		gc.bytes = 0
	}
	gc.masks[key] = mask
	gc.bytes += n
}

// maskShader paints a solid premultiplied colour through a coverage mask
// whose origin is at (ox, oy) in device space.
type maskShader struct {
	mask   *image.Alpha
	ox, oy int
	color  uint32
}

func (s *maskShader) ShadeSpan(y, x int, dst []uint32) {
	o := s.mask.PixOffset(x-s.ox, y-s.oy)
	row := s.mask.Pix[o : o+len(dst)]
	c := s.color
	for i, a := range row {
		switch a {
		case 0:
			dst[i] = 0
		case 255:
			dst[i] = c
		default:
			dst[i] = scale255(c, uint32(a))
		}
	}
}

// scale255 multiplies the four bytes of c by a/255, rounded.
func scale255(c, a uint32) uint32 {
	rb := (c&0x00ff00ff)*a + 0x00800080
	rb = (rb + (rb>>8)&0x00ff00ff) >> 8 & 0x00ff00ff
	ag := (c>>8&0x00ff00ff)*a + 0x00800080
	ag = (ag + (ag>>8)&0x00ff00ff) & 0xff00ff00
	return rb | ag
}
