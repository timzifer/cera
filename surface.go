package cera

import (
	"context"
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

// RenderSurface draws the page onto dst, as Render draws it into an image
// with the bounds of dst. Region, Background, Deadline, Stats, Layers,
// Annotations, Form, Band and the other options apply as there; Workers
// is ignored: the page is drawn in one pass.
func (p *Page) RenderSurface(ctx context.Context, dst Surface, opt RenderOptions) error {
	opt.Workers = 1
	return p.renderTo(ctx, &dest{surf: dst}, opt, projRGB)
}

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
