package cera

import "github.com/timzifer/stilus"

// Geometry types are shared with the rasterizer core, so paths flow from the
// interpreter to stilus without conversion.
type (
	// Path is a sequence of subpaths in user space (float32 coordinates).
	Path = stilus.Path
	// Matrix is an affine transform [a b c d e f]: x' = a·x + c·y + e,
	// y' = b·x + d·y + f.
	Matrix = stilus.Matrix
	// FillRule selects nonzero or even-odd filling.
	FillRule = stilus.FillRule
	// StrokeStyle is a stroke in user space (width, caps, joins, dashes).
	StrokeStyle = stilus.StrokeStyle
	// Paint is a premultiplied solid color or a per-span shader.
	Paint = stilus.Paint
)

// Fill rules.
const (
	NonZero = stilus.NonZero
	EvenOdd = stilus.EvenOdd
)

// Device receives the drawing operations of a page. The interpreter drives
// it; every backend (raster, text extraction, hit testing, GPU) implements it
// without touching the parser or the interpreter.
//
// Paths are in user space and m maps them to device space, so strokes are
// exact under anisotropic transforms. Clips nest: every ClipPath or
// ClipRect is undone by exactly one PopClip.
//
// The set grows with the milestones of the spec: FillGlyphs/ClipGlyphs (M4),
// DrawImage (M5), BeginGroup/EndGroup and soft masks (M6), FillShading (M7).
type Device interface {
	FillPath(p *Path, m Matrix, rule FillRule, paint *Paint)
	StrokePath(p *Path, m Matrix, st *StrokeStyle, paint *Paint)
	ClipPath(p *Path, m Matrix, rule FillRule)
	ClipRect(r Rect, m Matrix)
	PopClip()
}

// RasterDevice draws onto an *image.RGBA through a stilus.Canvas.
type RasterDevice struct {
	C *stilus.Canvas
}

func (d *RasterDevice) FillPath(p *Path, m Matrix, rule FillRule, paint *Paint) {
	d.C.Fill(p, m, rule, paint)
}

func (d *RasterDevice) StrokePath(p *Path, m Matrix, st *StrokeStyle, paint *Paint) {
	d.C.Stroke(p, m, st, paint)
}

func (d *RasterDevice) ClipPath(p *Path, m Matrix, rule FillRule) { d.C.ClipPath(p, m, rule) }

func (d *RasterDevice) ClipRect(r Rect, m Matrix) {
	d.C.ClipRect(stilus.Rect{X0: r.X0, Y0: r.Y0, X1: r.X1, Y1: r.Y1}, m)
}

func (d *RasterDevice) PopClip() { d.C.PopClip() }
