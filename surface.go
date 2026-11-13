package cera

import (
	"context"
	"errors"
	"image"
	"image/color"

	"github.com/timzifer/stilus"
)

// A Surface is a destination of Page.RenderSurface other than an image: a
// stilus.Compositor that the raster device's canvas draws onto through
// stilus.Canvas.ResetCompositor, such as a texture on the GPU (the module
// github.com/timzifer/cera/gpu, ADR 0011). Pixels are premultiplied RGBA,
// in device pixels of the page as Render's are.
//
// The page is drawn as Render draws it, with the same rasterizer, clips
// and paints. Transparency groups that are isolated, have no soft mask and
// are not knockout groups are drawn into layers of the surface and
// composited by it; other groups and soft masks are drawn on the CPU,
// into images, and composited onto the surface with stilus.LayerShader
// with images (its Dst nil: the backdrop is the surface), which the surface
// reads by the next Flush. A group drawn on
// the CPU that needs its backdrop reads it with Read; a non-isolated one
// is composited by Write.
//
// A surface is used from one goroutine at a time: RenderSurface draws in
// one pass, without bands.
type Surface interface {
	stilus.Compositor
	// Bounds returns the pixels of the surface.
	Bounds() image.Rectangle
	// Fill sets the pixels of r, which lies within Bounds, to c.
	Fill(r image.Rectangle, c color.RGBA) error
	// Read copies the pixels of dst.Rect, which lies within Bounds, into
	// dst, with everything drawn before.
	Read(dst *image.RGBA) error
	// Write sets the pixels of src.Rect, which lies within Bounds, to
	// those of src, after everything drawn before.
	Write(src *image.RGBA) error
	// NewLayer returns a transparent surface with the same bounds, for a
	// transparency group.
	NewLayer() (Surface, error)
	// Release gives back a surface made by NewLayer.
	Release(layer Surface)
	// Flush draws what was recorded. A paint's images, such as those of
	// a stilus.LayerShader, are read by then at the latest; the device
	// flushes before it draws into them again.
	Flush() error
	// Composite returns the shader that composites c.Layer, a layer of
	// the surface, onto the surface: filled through a canvas drawing onto
	// the surface, it gives what stilus.LayerShader gives for images. The
	// shader is used until the next call.
	Composite(c *Composite) stilus.Shader
}

// Composite is a layer's composition onto its surface (Surface.Composite),
// as stilus.LayerShader has it for images.
type Composite struct {
	Layer Surface
	Alpha uint8
	Blend stilus.BlendMode
	// Subtractive and Black are LayerShader's: layers of inks.
	Subtractive, Black bool
}

// A BandSurface is a Surface that several goroutines draw onto at once,
// one band each, such as a GPU target through recorders (stilus
// gpu.Recorder).
type BandSurface interface {
	Surface
	// Bands returns n compositors drawing onto the surface, one per
	// goroutine. While they are used, nothing else draws onto the
	// surface; what they drew is drawn by the next Flush.
	Bands(n int) ([]stilus.Compositor, error)
}

// RenderSurface draws the page onto dst, as Render draws it into an image
// with the bounds of dst. Region, Background, Deadline, Stats, Layers,
// Annotations, Form, Band and the other options apply as there. The page
// is drawn in one pass, or, if dst is a BandSurface, in bands by up to
// Workers goroutines when it has no transparency groups and soft masks
// and does not blend with the background. Band reports parts as they are
// recorded; the surface draws them at its next Flush.
func (p *Page) RenderSurface(ctx context.Context, dst Surface, opt RenderOptions) error {
	return p.renderTo(ctx, &dest{surf: dst}, opt, projRGB)
}

// surfaceBands returns the compositors of the workers drawing the list
// onto a surface in bands, with how many and whether in one pass, as
// planned; nil for one pass from the calling goroutine. The bands draw
// onto the surface at once, so the list must not need what a recorder
// cannot do: groups and soft masks read and composite layers, and a page
// that blends is drawn into one. The region in is filled first.
func (l *displayList) surfaceBands(dst *dest, in image.Rectangle, bg color.RGBA, fill bool, workers int, whole bool) ([]stilus.Compositor, int, bool) {
	bs, ok := dst.surf.(BandSurface)
	if !ok || whole || workers < 2 || l.blends || len(l.groups) > 0 || len(l.masks) > 0 {
		return nil, 1, true
	}
	comps, err := bs.Bands(workers)
	if err != nil || len(comps) < workers {
		return nil, 1, true
	}
	if fill {
		dst.fill(in, bg)
	}
	return comps, workers, false
}

// bandSurface is a worker's part of a surface drawn in bands: its
// compositor, and none of the surface's operations, which would use it
// while other workers draw onto it.
type bandSurface struct {
	Surface
	c stilus.Compositor
}

var errBands = errors.New("cera: a surface drawn in bands has no layers")

func (s *bandSurface) Blitter(p *stilus.Paint) stilus.Blitter { return s.c.Blitter(p) }
func (s *bandSurface) Fill(image.Rectangle, color.RGBA) error { return errBands }
func (s *bandSurface) Read(*image.RGBA) error                 { return errBands }
func (s *bandSurface) Write(*image.RGBA) error                { return errBands }
func (s *bandSurface) Flush() error                           { return errBands }
func (s *bandSurface) NewLayer() (Surface, error)             { return nil, errBands }

// dest is what a render draws onto: an image or a surface.
type dest struct {
	img  *image.RGBA
	surf Surface
	err  error // the first error of surf
}

func (d *dest) bounds() image.Rectangle {
	if d.surf != nil {
		return d.surf.Bounds()
	}
	return d.img.Bounds()
}

// fill sets the pixels of r to c.
func (d *dest) fill(r image.Rectangle, c color.RGBA) {
	if d.surf == nil {
		fillRegion(d.img, r, c)
		return
	}
	if r = r.Intersect(d.surf.Bounds()); !r.Empty() {
		d.report(d.surf.Fill(r, c))
	}
}

// fillOutside fills the parts of r outside in with c.
func (d *dest) fillOutside(r, in image.Rectangle, c color.RGBA) {
	in = in.Intersect(r)
	if in.Empty() {
		d.fill(r, c)
		return
	}
	d.fill(image.Rect(r.Min.X, r.Min.Y, r.Max.X, in.Min.Y), c)
	d.fill(image.Rect(r.Min.X, in.Max.Y, r.Max.X, r.Max.Y), c)
	d.fill(image.Rect(r.Min.X, in.Min.Y, in.Min.X, in.Max.Y), c)
	d.fill(image.Rect(in.Max.X, in.Min.Y, r.Max.X, in.Max.Y), c)
}

func (d *dest) report(err error) {
	if err != nil && d.err == nil {
		d.err = err
	}
}
