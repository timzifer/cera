package cera

import (
	"image"
	"image/color"

	"github.com/timzifer/stilus"
)

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
// Images arrive as DrawImage with m mapping the image's unit square to
// device space.
//
// Transparency arrives as groups: what is drawn between BeginGroup and
// EndGroup is composited as one, with the group's blend mode, opacity and,
// for a masked group, the soft mask drawn between the BeginMask and
// EndMask just before it. Objects painted with a blend mode or a soft mask
// in the graphics state come as groups of their own. Clips, groups and
// masks nest: a clip pushed inside a group or mask is popped inside it.
// The rectangle r under m bounds what a group or mask covers.
//
// Shadings arrive as FillShading, painting the current clip. A path,
// stroke or text painted with a pattern arrives as the pattern painted
// through a clip of the shape: ClipPath, ClipStroke for strokes, then
// FillShading (a shading pattern) or FillTile (a tiling pattern) and
// PopClip. A tiling pattern of a few large cells arrives as the drawing
// operations of its cells instead.
type Device interface {
	FillPath(p *Path, m Matrix, rule FillRule, paint *Paint)
	StrokePath(p *Path, m Matrix, st *StrokeStyle, paint *Paint)
	ClipPath(p *Path, m Matrix, rule FillRule)
	ClipRect(r Rect, m Matrix)
	// ClipStroke intersects the clip with the area a stroke of p paints.
	ClipStroke(p *Path, m Matrix, st *StrokeStyle)
	PopClip()
	// FillShading paints sh, whose space m maps to device space, over the
	// current clip (and the shading's BBox) with the constant alpha
	// paint.Color.A.
	FillShading(sh *Shading, m Matrix, paint *Paint)
	// FillTile paints t, repeated in both directions, over the current
	// clip, with m mapping the tile's pixel space to device space. A
	// stencil tile (t.Stencil) paints paint through its shape, any other
	// its own colours with the constant alpha paint.Color.A.
	FillTile(t *Tile, m Matrix, paint *Paint)
	// FillGlyphs fills the outlines of run (nonzero) with paint.
	FillGlyphs(run *GlyphRun, paint *Paint)
	// DrawImage paints img, whose unit square m maps to device space. A
	// stencil (img.Stencil) paints paint through its shape; any other
	// image paints its own colours with the constant alpha paint.Color.A.
	DrawImage(img *Image, m Matrix, paint *Paint)
	// BeginGroup starts a transparency group; EndGroup composites it.
	BeginGroup(r Rect, m Matrix, g *Group)
	EndGroup()
	// BeginMask starts drawing a soft mask; EndMask ends it. The mask
	// applies to the group begun next, which has Masked set.
	BeginMask(r Rect, m Matrix, sm *SoftMask)
	EndMask()
}

// BlendMode is a PDF blend mode (PDF 2.0, 11.3.5); PDF's blend modes are
// those of stilus.
type BlendMode = stilus.BlendMode

// Blend modes.
const (
	BlendNormal     = stilus.BlendNormal
	BlendMultiply   = stilus.BlendMultiply
	BlendScreen     = stilus.BlendScreen
	BlendOverlay    = stilus.BlendOverlay
	BlendDarken     = stilus.BlendDarken
	BlendLighten    = stilus.BlendLighten
	BlendColorDodge = stilus.BlendColorDodge
	BlendColorBurn  = stilus.BlendColorBurn
	BlendHardLight  = stilus.BlendHardLight
	BlendSoftLight  = stilus.BlendSoftLight
	BlendDifference = stilus.BlendDifference
	BlendExclusion  = stilus.BlendExclusion
	BlendHue        = stilus.BlendHue
	BlendSaturation = stilus.BlendSaturation
	BlendColor      = stilus.BlendColor
	BlendLuminosity = stilus.BlendLuminosity
)

// Group describes a transparency group (PDF 2.0, 11.4).
type Group struct {
	// Isolated groups start from a transparent backdrop; non-isolated ones
	// see what is below them.
	Isolated bool
	// Knockout groups composite each object against the group's initial
	// backdrop instead of the objects before it.
	Knockout bool
	// Blend composites the group onto its backdrop.
	Blend BlendMode
	// Alpha is the group's constant opacity (255 is opaque).
	Alpha uint8
	// Masked composites the group through the soft mask drawn just before.
	Masked bool

	// alone marks, in a display list, a non-isolated group with blend
	// modes inside that is itself blended: drawn once more on its own
	// first, so that compositing it can remove its backdrop.
	alone bool
}

// SoftMask describes a soft mask (PDF 2.0, 11.6.5.2). Its values come from
// the group drawn between BeginMask and EndMask.
type SoftMask struct {
	// Luminosity takes the mask from the luminosity of the group drawn on
	// Backdrop; otherwise it is the group's alpha.
	Luminosity bool
	// Backdrop is the opaque colour a luminosity mask group is drawn on,
	// and the mask's value outside the group.
	Backdrop color.RGBA
	// Transfer maps mask values; nil is the identity.
	Transfer *[256]uint8
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
	// spacing: to the right, or down for vertical text.
	Advance float64
	// Origin is the pen position in the glyph's em square: (0, 0) for
	// horizontal text; for vertical text the position vector, about half
	// the width right of and 0.88 em above the glyph's own origin.
	Origin [2]float64
}

// GlyphRun is the glyphs of one text-showing operator in one font. The
// run and its slice are reused: devices must not keep them.
type GlyphRun struct {
	Font   *Font
	Glyphs []Glyph
	// Vertical is text written top to bottom (a font with WMode 1).
	Vertical bool
}

// RasterDevice draws onto an *image.RGBA through a stilus.Canvas. Glyphs
// small enough are rasterized once per size and subpixel position into a
// cache of coverage masks kept by the device. Images are sampled from the
// mip level that fits the device resolution. Groups and soft masks are
// drawn into layers of their own (see Reset).
type RasterDevice struct {
	C *stilus.Canvas

	glyphs *glyphCache // allocated on first use
	img    imageDraw
	shade  shadeDraw
	t      layers
}

// Reset targets the device, and its canvas, at region of dst. Groups need
// it: a device whose canvas was targeted directly draws their content
// without compositing it and does not draw soft masks.
func (d *RasterDevice) Reset(dst *image.RGBA, region image.Rectangle) {
	d.t.reset(dst, region)
	d.C.Reset(dst, region)
}

// Err returns the first error of the canvas since Reset.
func (d *RasterDevice) Err() error {
	if d.t.err != nil {
		return d.t.err
	}
	return d.C.Err()
}

func (d *RasterDevice) FillPath(p *Path, m Matrix, rule FillRule, paint *Paint) {
	d.t.inked(paint.Color.A)
	if !d.t.knockout {
		d.C.Fill(p, m, rule, paint)
		return
	}
	box := d.koBegin(deviceBox(p, m, 1))
	d.C.Fill(p, m, rule, paint)
	d.koShape()
	d.C.Fill(p, m, rule, &opaque)
	d.koEnd(box)
}

func (d *RasterDevice) StrokePath(p *Path, m Matrix, st *StrokeStyle, paint *Paint) {
	d.t.inked(paint.Color.A)
	if !d.t.knockout {
		d.C.Stroke(p, m, st, paint)
		return
	}
	box := d.koBegin(strokeBox(p, m, st))
	d.C.Stroke(p, m, st, paint)
	d.koShape()
	d.C.Stroke(p, m, st, &opaque)
	d.koEnd(box)
}

// fillUnion draws the union of the elements of u (a run of the display
// list, never in a knockout group) with paint.
func (d *RasterDevice) fillUnion(u *stilus.Union, paint *Paint) {
	d.t.inked(paint.Color.A)
	d.C.FillUnion(u, paint)
}

func (d *RasterDevice) ClipPath(p *Path, m Matrix, rule FillRule) {
	d.t.pushClip(p, Rect{}, m, rule, false, nil)
	d.C.ClipPath(p, m, rule)
}

func (d *RasterDevice) ClipRect(r Rect, m Matrix) {
	d.t.pushClip(nil, r, m, 0, true, nil)
	d.C.ClipRect(stilus.Rect{X0: r.X0, Y0: r.Y0, X1: r.X1, Y1: r.Y1}, m)
}

func (d *RasterDevice) ClipStroke(p *Path, m Matrix, st *StrokeStyle) {
	d.t.pushClip(p, Rect{}, m, 0, false, st)
	d.C.ClipStroke(p, m, st)
}

func (d *RasterDevice) PopClip() {
	d.t.popClip()
	d.C.PopClip()
}
