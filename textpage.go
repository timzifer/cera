package cera

import (
	"context"
	"math"
	"strings"
)

// TextChar is one glyph of the text of a page.
type TextChar struct {
	// Text is what the glyph stands for, usually one character; empty when
	// the font does not say.
	Text string
	// Code is the character code shown.
	Code int
	// Origin is the pen position of the glyph, Quad the box of its em
	// square (ascent to descent, origin to advance) and Box the bounds of
	// Quad, in points of the displayed page: y down, origin at the top-left
	// corner, after /Rotate and /UserUnit (see Page.DisplayMatrix).
	Origin [2]float64
	Quad   Quad
	Box    Rect
	// Dir is the unit vector the pen moves along, and Advance how far it
	// moves for this glyph, in points, before character and word spacing.
	Dir     [2]float64
	Advance float64
	// Size is the em size in points.
	Size float64
	Font string
	Mode TextMode
	// MCID is the marked-content identifier of the innermost sequence
	// around the glyph that has one, which ties it to the structure tree
	// of a tagged PDF; -1 when there is none.
	MCID int
	// Artifact reports that the glyph is inside an Artifact sequence:
	// page furniture such as running heads, page numbers or watermarks.
	Artifact bool
}

// PageText is the text of a page in content order.
type PageText struct {
	Chars []TextChar
}

// Text extracts the text of the page, including invisible text (such as
// the OCR layer of a scan).
func (p *Page) Text(ctx context.Context) (*PageText, error) {
	td := &textDevice{}
	err := p.Run(ctx, td, 1, nil)
	return &PageText{Chars: td.chars}, err
}

// String returns the text with line breaks where the baseline jumps and
// spaces where glyphs on a line are further apart than a fifth of the em.
func (t *PageText) String() string {
	var b strings.Builder
	for i := range t.Chars {
		c := &t.Chars[i]
		if i > 0 {
			prev := &t.Chars[i-1]
			dx, dy := c.Origin[0]-prev.Origin[0], c.Origin[1]-prev.Origin[1]
			along := dx*prev.Dir[0] + dy*prev.Dir[1]
			across := -dx*prev.Dir[1] + dy*prev.Dir[0]
			size := max(prev.Size, c.Size, 1e-9)
			switch {
			case math.Abs(across) > size/2 || along < -size:
				b.WriteByte('\n')
			case along-prev.Advance > size/5 && c.Text != " " && prev.Text != " ":
				b.WriteByte(' ')
			}
		}
		if c.Text == "" {
			b.WriteRune('�')
		} else {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// textDevice collects the text a page shows and draws nothing.
type textDevice struct {
	chars []TextChar
	// mc is the open marked content, innermost last.
	mc []textMarked
}

// textMarked is what an open marked-content sequence says about the
// glyphs inside it, with the sequences around it.
type textMarked struct {
	mcid     int
	artifact bool
}

func (d *textDevice) BeginMarkedContent(mc *MarkedContent) {
	m := textMarked{mcid: -1}
	if n := len(d.mc); n > 0 {
		m = d.mc[n-1]
	}
	if mc.MCID >= 0 {
		m.mcid = mc.MCID
	}
	m.artifact = m.artifact || mc.Tag == "Artifact"
	d.mc = append(d.mc, m)
}

func (d *textDevice) EndMarkedContent() {
	if n := len(d.mc); n > 0 {
		d.mc = d.mc[:n-1]
	}
}

func (*textDevice) FillPath(*Path, Matrix, FillRule, *Paint)       {}
func (*textDevice) StrokePath(*Path, Matrix, *StrokeStyle, *Paint) {}
func (*textDevice) ClipPath(*Path, Matrix, FillRule)               {}
func (*textDevice) ClipRect(Rect, Matrix)                          {}
func (*textDevice) ClipStroke(*Path, Matrix, *StrokeStyle)         {}
func (*textDevice) FillShading(*Shading, Matrix, *Paint)           {}
func (*textDevice) FillTile(*Tile, Matrix, *Paint)                 {}
func (*textDevice) PopClip()                                       {}
func (*textDevice) FillGlyphs(*GlyphRun, *Paint)                   {}
func (*textDevice) DrawImage(*Image, Matrix, *Paint)               {}
func (*textDevice) BeginGroup(Rect, Matrix, *Group)                {}
func (*textDevice) EndGroup()                                      {}
func (*textDevice) BeginMask(Rect, Matrix, *SoftMask)              {}
func (*textDevice) EndMask()                                       {}

func (d *textDevice) ShowText(run *GlyphRun, mode TextMode) {
	f := run.Font
	asc, desc := f.Metrics()
	name := f.Name()
	mc := textMarked{mcid: -1}
	if n := len(d.mc); n > 0 {
		mc = d.mc[n-1]
	}
	for i := range run.Glyphs {
		g := &run.Glyphs[i]
		m := g.M
		c := TextChar{Code: g.Code, Font: name, Mode: mode, MCID: mc.mcid, Artifact: mc.artifact}
		c.Text, _ = f.Text(g.Code)
		c.Origin[0], c.Origin[1] = m.Apply(g.Origin[0], g.Origin[1])
		c.Size = math.Sqrt(math.Abs(m.Det()))
		adv := g.Advance
		// The em box in glyph space, ascent side first, and the direction
		// the pen moves.
		q := Quad{[4][2]float64{{0, asc}, {adv, asc}, {0, desc}, {adv, desc}}}
		dx, dy := 1.0, 0.0
		if run.Vertical {
			// An em wide, centred on the pen, from the pen down; the next
			// column is to the left.
			ox, oy := g.Origin[0], g.Origin[1]
			q = Quad{[4][2]float64{{ox + 0.5, oy}, {ox + 0.5, oy - adv}, {ox - 0.5, oy}, {ox - 0.5, oy - adv}}}
			dx, dy = 0, -1
		}
		c.Quad = q.Transform(m)
		c.Box = c.Quad.Bounds()
		ux, uy := m.ApplyVec(dx, dy)
		if l := math.Hypot(ux, uy); l > 0 {
			c.Dir = [2]float64{ux / l, uy / l}
			c.Advance = adv * l
		} else {
			c.Dir = [2]float64{1, 0}
		}
		d.chars = append(d.chars, c)
	}
}
