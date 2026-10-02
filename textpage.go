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
	// Origin is the pen position of the glyph and Box the box of its em
	// square (ascent to descent, origin to advance), in points of the
	// displayed page: y down, origin at the top-left corner, after
	// /Rotate.
	Origin [2]float64
	Box    Rect
	// Size is the em size in points.
	Size float64
	Font string
	Mode TextMode

	// dir is the unit vector of the baseline and adv the advance along it.
	dir [2]float64
	adv float64
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
			along := dx*prev.dir[0] + dy*prev.dir[1]
			across := -dx*prev.dir[1] + dy*prev.dir[0]
			size := max(prev.Size, c.Size, 1e-9)
			switch {
			case math.Abs(across) > size/2 || along < -size:
				b.WriteByte('\n')
			case along-prev.adv > size/5 && c.Text != " " && prev.Text != " ":
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
}

func (*textDevice) FillPath(*Path, Matrix, FillRule, *Paint)       {}
func (*textDevice) StrokePath(*Path, Matrix, *StrokeStyle, *Paint) {}
func (*textDevice) ClipPath(*Path, Matrix, FillRule)               {}
func (*textDevice) ClipRect(Rect, Matrix)                          {}
func (*textDevice) ClipStroke(*Path, Matrix, *StrokeStyle)         {}
func (*textDevice) FillShading(*Shading, Matrix, *Paint)           {}
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
	for i := range run.Glyphs {
		g := &run.Glyphs[i]
		m := g.M
		c := TextChar{Font: name, Mode: mode}
		c.Text, _ = f.Text(g.Code)
		c.Origin[0], c.Origin[1] = m.Apply(0, 0)
		c.Size = math.Sqrt(math.Abs(m.Det()))
		adv := g.Advance
		x0, y0 := math.Inf(1), math.Inf(1)
		x1, y1 := math.Inf(-1), math.Inf(-1)
		for _, p := range [4][2]float64{{0, desc}, {adv, desc}, {0, asc}, {adv, asc}} {
			x, y := m.Apply(p[0], p[1])
			x0, x1 = min(x0, x), max(x1, x)
			y0, y1 = min(y0, y), max(y1, y)
		}
		c.Box = Rect{x0, y0, x1, y1}
		ux, uy := m.ApplyVec(1, 0)
		if l := math.Hypot(ux, uy); l > 0 {
			c.dir = [2]float64{ux / l, uy / l}
			c.adv = adv * l
		} else {
			c.dir = [2]float64{1, 0}
		}
		d.chars = append(d.chars, c)
	}
}
