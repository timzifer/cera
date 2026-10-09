package cera

import (
	"context"
	"math"
	"slices"
	"strings"
)

// TextChar is one glyph of the text of a page.
type TextChar struct {
	// Text is what the glyph stands for, usually one character; empty when
	// the font does not say. A glyph standing for several characters, such
	// as a ligature, is split into one TextChar per character. Inside a
	// sequence with /ActualText, the first glyph has that text and the
	// others none.
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

	// group numbers the /ActualText sequence the glyph is in, from 1; 0
	// for none. Its glyphs are selected together.
	group int32
}

// PageText is the text of a page laid out in lines and blocks, in reading
// order. It is not changed after Page.Text returns it and may be used
// from several goroutines.
type PageText struct {
	// Chars are the characters in reading order: the characters of a line
	// follow each other, and the lines of a block.
	Chars []TextChar
	// Lines are the lines of text, in reading order.
	Lines []TextLine
	// Blocks are the blocks of lines, such as paragraphs or table cells,
	// in reading order.
	Blocks []TextBlock
}

// TextLine is a line of text: characters on one baseline, close enough to
// read as one line.
type TextLine struct {
	// Chars[Start:End] of the PageText are the characters of the line.
	Start, End int
	// Quad is the box of the line, from the start of its first character
	// to the end of its last, and from the highest ascent to the lowest
	// descent; Dir is the direction of the text.
	Quad Quad
	Dir  [2]float64
	// Block is the index of the line's block.
	Block int
	// Hyphenated reports that the line ends in a hyphen that breaks a word
	// continued on the next line of the block.
	Hyphenated bool
}

// TextBlock is a block of lines with the same direction, one below the
// other.
type TextBlock struct {
	// Lines[Start:End] of the PageText are the lines of the block.
	Start, End int
	// Bounds is the box around the block.
	Bounds Rect
}

// TextOptions control Page.TextWith.
type TextOptions struct {
	// Layers and Usage select the optional content whose text is
	// extracted, as for RenderOptions.
	Layers *Visibility
	Usage  Usage
	// Annotations selects the annotations whose appearances add text, as
	// for RenderOptions.
	Annotations AnnotMode
	// Form supplies field values, whose generated appearances add text.
	Form *FormState
	// IgnoreStructure orders the text by its layout alone. Otherwise the
	// structure tree of a tagged PDF orders the blocks it covers.
	IgnoreStructure bool
}

// Text extracts the text of the page, including invisible text (such as
// the OCR layer of a scan), and lays it out in lines and blocks.
func (p *Page) Text(ctx context.Context) (*PageText, error) {
	return p.TextWith(ctx, TextOptions{})
}

// TextWith is Text with options. When ctx ends first, the text shown until
// then is laid out and returned with ErrDeadline.
func (p *Page) TextWith(ctx context.Context, opt TextOptions) (*PageText, error) {
	td := &textDevice{}
	err := p.RunWith(ctx, td, RunOptions{Scale: 1, Layers: opt.Layers, Usage: opt.Usage, Annotations: opt.Annotations, Form: opt.Form})
	var ranks map[int]int
	if !opt.IgnoreStructure {
		ranks = p.doc.structRanks(p.index)
	}
	return layoutText(td.finish(), ranks), err
}

// String returns the text: lines are separated by a line break, blocks by
// an empty line, and a space stands where characters on a line are further
// apart than a word space. A character the font gives no text for is
// U+FFFD. White space starting a line is left out, and so are lines
// without text, such as the rest of a word whose /ActualText is on an
// earlier line.
func (t *PageText) String() string {
	if len(t.Lines) == 0 && len(t.Chars) > 0 {
		return layoutText(slices.Clone(t.Chars), nil).String() // made by hand
	}
	var b strings.Builder
	sep := "" // written before the next text
	for _, bl := range t.Blocks {
		if b.Len() > 0 {
			sep = "\n\n"
		}
		for li := bl.Start; li < bl.End; li++ {
			l := &t.Lines[li]
			wrote := false // on this line
			for i := l.Start; i < l.End; i++ {
				c := &t.Chars[i]
				if c.Text == "" && c.group != 0 || !wrote && isSpace(c.Text) {
					continue // replaced by the /ActualText before, or indenting
				}
				wrote = true
				if sep != "" {
					b.WriteString(sep)
					sep = ""
				} else if i > l.Start && t.spaceBefore(i) {
					b.WriteByte(' ')
				}
				if c.Text == "" {
					b.WriteRune('�')
				} else {
					b.WriteString(c.Text)
				}
			}
			if b.Len() > 0 && sep == "" {
				sep = "\n"
			}
		}
	}
	return b.String()
}

// spaceBefore reports whether a word space stands between Chars[i-1] and
// Chars[i] of one line: a gap of more than 0.15 em without a space glyph.
func (t *PageText) spaceBefore(i int) bool {
	prev, c := &t.Chars[i-1], &t.Chars[i]
	if isSpace(prev.Text) || isSpace(c.Text) {
		return false
	}
	along := (c.Origin[0]-prev.Origin[0])*prev.Dir[0] + (c.Origin[1]-prev.Origin[1])*prev.Dir[1]
	return along-prev.Advance > 0.15*max(prev.Size, c.Size)
}

// isSpace reports whether s is white space and nothing else.
func isSpace(s string) bool {
	return s != "" && strings.TrimSpace(s) == ""
}

// textDevice collects the text a page shows and draws nothing.
type textDevice struct {
	chars []TextChar
	// mc is the open marked content, innermost last.
	mc []textMarked
	// spans are the /ActualText sequences, numbered from 1 by group.
	spans []actualSpan
}

// textMarked is what an open marked-content sequence says about the
// glyphs inside it, with the sequences around it.
type textMarked struct {
	mcid     int
	artifact bool
	group    int32 // the /ActualText around, 0 for none
	opens    bool  // this sequence has the /ActualText
}

// actualSpan is the /ActualText of the glyphs chars[start:end].
type actualSpan struct {
	text       string
	start, end int
}

func (d *textDevice) BeginMarkedContent(mc *MarkedContent) {
	m := textMarked{mcid: -1}
	if n := len(d.mc); n > 0 {
		m = d.mc[n-1]
		m.opens = false
	}
	if mc.MCID >= 0 {
		m.mcid = mc.MCID
	}
	m.artifact = m.artifact || mc.Tag == "Artifact"
	if mc.HasActualText && m.group == 0 {
		// An /ActualText inside another is replaced with it.
		d.spans = append(d.spans, actualSpan{text: mc.ActualText, start: len(d.chars)})
		m.group, m.opens = int32(len(d.spans)), true
	}
	d.mc = append(d.mc, m)
}

func (d *textDevice) EndMarkedContent() {
	if n := len(d.mc); n > 0 {
		if m := d.mc[n-1]; m.opens {
			d.spans[m.group-1].end = len(d.chars)
		}
		d.mc = d.mc[:n-1]
	}
}

// finish puts the /ActualText on the glyphs it replaces and returns them.
func (d *textDevice) finish() []TextChar {
	for _, m := range d.mc {
		if m.opens {
			d.spans[m.group-1].end = len(d.chars)
		}
	}
	for _, s := range d.spans {
		if s.end > s.start {
			d.chars[s.start].Text = s.text
			for i := s.start + 1; i < s.end; i++ {
				d.chars[i].Text = ""
			}
		}
	}
	return d.chars
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
	t3 := f.Type3()
	if need := len(d.chars) + len(run.Glyphs); need > cap(d.chars) {
		// Doubled: append grows large slices by a quarter, copying a page
		// of characters many times.
		d.chars = slices.Grow(d.chars, max(need, 2*cap(d.chars))-len(d.chars))
	}
	mc := textMarked{mcid: -1}
	if n := len(d.mc); n > 0 {
		mc = d.mc[n-1]
	}
	for i := range run.Glyphs {
		g := &run.Glyphs[i]
		m := g.M
		c := TextChar{Code: g.Code, Font: name, Mode: mode, MCID: mc.mcid, Artifact: mc.artifact, group: mc.group}
		c.Text, _ = f.Text(g.Code)
		if c.Text == "" && g.Outline == nil && g.GID != 0 && !t3 {
			c.Text = " " // a blank glyph the font gives no text for, not a missing one
		}
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
