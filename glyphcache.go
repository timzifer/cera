package cera

import (
	"image"
	"sync"

	"github.com/timzifer/stilus"
)

// A raster device draws filled glyphs through stilus's glyph cache, which
// keeps the coverage mask of every glyph it has drawn per size and subpixel
// position, so a glyph is rasterized once and then composited: the text of
// a page repeats a few dozen glyphs thousands of times. Composition honours
// the clip stack like every other fill.
//
// Each raster device (one per worker) has its own cache: no locks while
// drawing, and a mask costs about as much to make as one direct fill of
// the glyph, so a worker that meets a glyph twice has already gained.
// Keys hold a font id, not the font, so a pooled device keeps no document
// alive.

// glyphCaches keeps the caches of idle painters, most recently used last,
// so that a serial renderer gets its own back. sync.Pool drops its items at
// every other garbage collection, and a cache is worth more than the
// painter around it.
var glyphCaches struct {
	sync.Mutex
	free []*stilus.GlyphCache
}

const maxIdleGlyphCaches = 64

func getGlyphCache() *stilus.GlyphCache {
	gcs := &glyphCaches
	gcs.Lock()
	defer gcs.Unlock()
	n := len(gcs.free)
	if n == 0 {
		return new(stilus.GlyphCache)
	}
	gc := gcs.free[n-1]
	gcs.free[n-1] = nil
	gcs.free = gcs.free[:n-1]
	return gc
}

func putGlyphCache(gc *stilus.GlyphCache) {
	gcs := &glyphCaches
	gcs.Lock()
	if gc != nil && len(gcs.free) < maxIdleGlyphCaches {
		gcs.free = append(gcs.free, gc)
	}
	gcs.Unlock()
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
	if d.C.Clip().Empty() {
		return
	}
	if d.glyphs == nil {
		d.glyphs = new(stilus.GlyphCache)
	}
	for i := range run.Glyphs {
		g := &run.Glyphs[i]
		switch {
		case g.Outline == nil:
		case run.Font == nil:
			d.C.Fill(g.Outline, g.M, NonZero, paint)
		default:
			d.glyphs.FillGlyph(d.C, run.Font.id, int32(g.GID), g.Outline, g.M, paint)
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
