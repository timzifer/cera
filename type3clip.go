package cera

import (
	"image"

	"github.com/go-pdfkit/reader"
)

// Type 3 glyphs shown in a clipping render mode (4–7) add their shapes to
// the text clip (ADR 0009): the glyph procedure runs once more against
// t3Clip, which collects what it paints into the text object's clip path
// in device space instead of drawing it. Fills and glyphs are exact;
// strokes, images, and shadings or tiles painted outside a clip of the
// glyph are approximated by their device boxes and counted as
// type3-clip-approx, as are even-odd fills, which join the nonzero clip.
// Clips inside the glyph only bound what shadings and tiles paint (the
// innermost one), and soft masks contribute nothing.

// t3Clip is the device a Type 3 glyph procedure runs against to clip.
type t3Clip struct {
	in    *interp
	dst   *Path  // the text object's clip
	clips []Path // device-space clips of the glyph, innermost last
	n     int    // clips in use
	masks int    // soft masks being drawn
}

func (c *t3Clip) approx() { c.in.st.unsupported("type3-clip-approx") }

// box appends the device pixels r as a rectangle to d.
func box(d *Path, r image.Rectangle) {
	if r.Empty() {
		return
	}
	x0, y0, x1, y1 := float32(r.Min.X), float32(r.Min.Y), float32(r.Max.X), float32(r.Max.Y)
	d.MoveTo(x0, y0)
	d.LineTo(x1, y0)
	d.LineTo(x1, y1)
	d.LineTo(x0, y1)
	d.Close()
}

func (c *t3Clip) FillPath(p *Path, m Matrix, rule FillRule, _ *Paint) {
	if c.masks > 0 {
		return
	}
	if rule == EvenOdd {
		c.approx()
	}
	appendTransformed(c.dst, p, m)
}

func (c *t3Clip) StrokePath(p *Path, m Matrix, st *StrokeStyle, _ *Paint) {
	if c.masks > 0 {
		return
	}
	c.approx()
	box(c.dst, strokeBox(p, m, st))
}

func (c *t3Clip) push() *Path {
	if c.n == len(c.clips) {
		c.clips = append(c.clips, Path{})
	}
	p := &c.clips[c.n]
	p.Reset()
	c.n++
	return p
}

func (c *t3Clip) ClipPath(p *Path, m Matrix, _ FillRule) { appendTransformed(c.push(), p, m) }

func (c *t3Clip) ClipRect(r Rect, m Matrix) {
	q := c.push()
	pt := func(x, y float64) (float32, float32) {
		x, y = m.Apply(x, y)
		return float32(x), float32(y)
	}
	q.MoveTo(pt(r.X0, r.Y0))
	q.LineTo(pt(r.X1, r.Y0))
	q.LineTo(pt(r.X1, r.Y1))
	q.LineTo(pt(r.X0, r.Y1))
	q.Close()
}

func (c *t3Clip) ClipStroke(p *Path, m Matrix, st *StrokeStyle) {
	c.approx()
	box(c.push(), strokeBox(p, m, st))
}

func (c *t3Clip) PopClip() {
	if c.n > 0 {
		c.n--
	}
}

// area adds what paints the current clip: the innermost clip of the
// glyph, approximated when there are more.
func (c *t3Clip) area() {
	if c.masks > 0 {
		return
	}
	if c.n == 0 {
		c.approx() // unbounded within the glyph: nothing to add
		return
	}
	if c.n > 1 {
		c.approx()
	}
	q := &c.clips[c.n-1]
	appendTransformed(c.dst, q, identity)
}

func (c *t3Clip) FillShading(*Shading, Matrix, *Paint) { c.area() }
func (c *t3Clip) FillTile(*Tile, Matrix, *Paint)       { c.area() }

func (c *t3Clip) FillGlyphs(run *GlyphRun, _ *Paint) {
	if c.masks > 0 {
		return
	}
	for i := range run.Glyphs {
		if o := run.Glyphs[i].Outline; o != nil {
			appendTransformed(c.dst, o, run.Glyphs[i].M)
		}
	}
}

func (c *t3Clip) DrawImage(_ *Image, m Matrix, _ *Paint) {
	if c.masks > 0 {
		return
	}
	// The image's parallelogram, whatever its mask shows.
	c.approx()
	pt := func(x, y float64) (float32, float32) {
		x, y = m.Apply(x, y)
		return float32(x), float32(y)
	}
	d := c.dst
	d.MoveTo(pt(0, 0))
	d.LineTo(pt(1, 0))
	d.LineTo(pt(1, 1))
	d.LineTo(pt(0, 1))
	d.Close()
}

func (c *t3Clip) BeginGroup(Rect, Matrix, *Group)   {}
func (c *t3Clip) EndGroup()                         {}
func (c *t3Clip) BeginMask(Rect, Matrix, *SoftMask) { c.masks++ }
func (c *t3Clip) EndMask()                          { c.masks-- }

// type3Clip adds the glyph procedure of code, with glyph space mapped to
// user space by mu, to the text clip. The procedure paints into t3Clip
// only: the text device, optional content and the display list's tags are
// left out of it (a hidden glyph clips like a visible one), and what it
// paints is not counted in the stats.
func (in *interp) type3Clip(f *Font, code int, mu Matrix, res reader.Dict, depth int) {
	c := &in.t3c
	c.in, c.dst, c.n, c.masks = in, &in.text.clip, 0, 0
	dev, out, mute, td, rec, oc := in.dev, in.out, in.mute.d, in.td, in.rec, in.ocCur
	in.dev, in.out, in.mute.d, in.td, in.rec, in.ocCur = c, c, c, nil, nil, 0
	st := *in.st
	in.type3Glyph(f, code, mu, res, depth, true)
	in.st.Fills, in.st.Strokes, in.st.Clips, in.st.Glyphs = st.Fills, st.Strokes, st.Clips, st.Glyphs
	in.st.Images, in.st.Groups, in.st.Shadings = st.Images, st.Groups, st.Shadings
	in.dev, in.out, in.mute.d, in.td, in.rec, in.ocCur = dev, out, mute, td, rec, oc
	c.in, c.dst = nil, nil
}
