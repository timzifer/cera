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
// Filled text arrives as FillGlyphs, so that a device can cache what it
// draws per glyph. Stroked text and text clips (render modes 1, 2 and 4–7)
// arrive as paths, Type 3 glyphs as the drawing operations of their
// content streams. A device that wants the text itself, in every render
// mode including invisible text, implements TextDevice as well.
//
// The set grows with the milestones of the spec: DrawImage (M5),
// BeginGroup/EndGroup and soft masks (M6), FillShading (M7).
type Device interface {
	FillPath(p *Path, m Matrix, rule FillRule, paint *Paint)
	StrokePath(p *Path, m Matrix, st *StrokeStyle, paint *Paint)
	ClipPath(p *Path, m Matrix, rule FillRule)
	ClipRect(r Rect, m Matrix)
	PopClip()
	// FillGlyphs fills the outlines of run (nonzero) with paint.
	FillGlyphs(run *GlyphRun, paint *Paint)
}

// TextDevice is implemented by devices that want to know the text a page
// shows: text extraction, search, hit testing. The interpreter calls
// ShowText for every string shown, whatever its render mode, before it
// draws it.
type TextDevice interface {
	Device
	ShowText(run *GlyphRun, mode TextMode)
}

// TextMode is a text render mode (Tr, PDF 2.0 9.3.6).
type TextMode uint8

// Text render modes.
const (
	TextFill TextMode = iota
	TextStroke
	TextFillStroke
	TextInvisible
	TextFillClip
	TextStrokeClip
	TextFillStrokeClip
	TextClip
)

// Glyph is one glyph of a GlyphRun.
type Glyph struct {
	// Code is the character code shown; run.Font.Text(Code) is its text.
	Code int
	// GID is the glyph index in the font program (the code for Type 3).
	GID int
	// Outline is the glyph in em units (y up, origin at the pen position),
	// nil for an empty or missing glyph and for Type 3 glyphs. It is shared
	// and must not be modified.
	Outline *Path
	// M maps the em square of the glyph to device space: font size,
	// horizontal scaling, rise, text matrix and CTM.
	M Matrix
	// Advance is the pen movement in em, before character and word
	// spacing.
	Advance float64
}

// GlyphRun is the glyphs of one text-showing operator in one font. The
// run and its slice are reused: devices must not keep them.
type GlyphRun struct {
	Font   *Font
	Glyphs []Glyph
}

// RasterDevice draws onto an *image.RGBA through a stilus.Canvas. Glyphs
// small enough are rasterized once per size and subpixel position into a
// cache of coverage masks kept by the device.
type RasterDevice struct {
	C *stilus.Canvas

	glyphs *glyphCache // allocated on first use
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
