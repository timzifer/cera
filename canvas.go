package cera

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"unicode/utf16"

	"github.com/go-opentype/opentype"
	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
	"github.com/timzifer/cera/internal/pdfwrite"
	"github.com/timzifer/stilus"
)

// Content an editor makes (ADR 0013): blank pages, and drawings on pages —
// a watermark or a stamp, page numbers, a header, a cover sheet, pages of
// scanned images, sheets of several pages.
//
// A drawing is written as a form XObject with resources of its own, painted
// before or after the content of its page, which goes between q and Q: the
// page's content is neither parsed nor rewritten, and a page drawn on gets
// one name more in its resources. A Canvas writes the content stream of
// the form, as the generators of appearances do theirs.

// maxPageSide is the longest side of a page in points: what a viewer
// without /UserUnit shows (PDF 2.0, annex C).
const maxPageSide = 14400

// AddPage inserts a blank page of width by height points before page at
// (NumPages to append). [Editor.Draw] draws on it.
func (e *Editor) AddPage(at int, width, height float64) error {
	if at < 0 || at > len(e.pages) {
		return fmt.Errorf("cera: insert position %d is out of range [0, %d]", at, len(e.pages))
	}
	if !(width >= 1 && height >= 1) || width > maxPageSide || height > maxPageSide {
		return fmt.Errorf("cera: a page of %g by %g points: sides are from 1 to %d", width, height, maxPageSide)
	}
	p := editPage{blank: true, w: width, h: height}
	e.pages = append(e.pages[:at], append([]editPage{p}, e.pages[at:]...)...)
	return nil
}

// Draw draws on page i of the result: draw is called once, with a canvas
// in points of the displayed page — the origin at its top left corner, y
// down, as everywhere in the editor. What it draws lies over the content of
// the page, or under it; drawings of several calls lie in their order. A
// drawing belongs to its page: it moves with it, turns with it when the
// page is rotated afterwards, and goes when the page is deleted.
// Annotations stay above everything drawn.
//
// The content of the page is kept as it is. Content that leaves the
// graphics state unbalanced — more q than Q — moves what is drawn over it.
func (e *Editor) Draw(i int, over bool, draw func(c *Canvas)) error {
	if err := e.check(i); err != nil {
		return err
	}
	ep := &e.pages[i]
	w, h, m, err := ep.view()
	if err != nil {
		return err
	}
	c := &Canvas{w: w, h: h, cur: canvasState{opacity: 1, set: canvasGS{alpha: 1}}, glyphs: map[*fallbackFont]map[opentype.GlyphIndex]rune{}}
	draw(c)
	for len(c.stack) > 0 {
		c.Restore()
	}
	if len(c.cw) > 0 {
		ep.draws = append(ep.draws, &drawing{c: c, under: !over, m: m})
	}
	return nil
}

// drawsOn reports whether the editor draws on a page.
func (e *Editor) drawsOn() bool {
	return slices.ContainsFunc(e.pages, func(p editPage) bool { return len(p.draws) > 0 })
}

// view returns the size of the page as it is displayed, in points, and the
// matrix from these points to its default user space.
func (ep *editPage) view() (w, h float64, m Matrix, err error) {
	var v *Page
	if ep.blank {
		v = &Page{Box: Rect{0, 0, ep.w, ep.h}, unit: 1, Rotate: (ep.rotate%360 + 360) % 360}
	} else {
		pg, err := ep.src.Page(ep.index)
		if err != nil {
			return 0, 0, Matrix{}, err
		}
		v = pg.Rotated(ep.rotate)
	}
	w, h = v.Size()
	m, ok := v.DisplayMatrix().Invert()
	if !ok {
		return 0, 0, Matrix{}, errors.New("cera: the page has no area")
	}
	return w, h, m, nil
}

// drawing is what one call of Draw drew on a page.
type drawing struct {
	c     *Canvas
	under bool
	m     Matrix // from the canvas to the default user space of the page
}

// Canvas draws on a page, in points of the displayed page: the origin at
// the top left corner, x to the right and y down. It is valid during the
// call of [Editor.Draw] it was made for.
type Canvas struct {
	w, h  float64
	cw    csw
	cur   canvasState
	stack []canvasState

	// The resources of what was drawn: graphics states GS0 and on, the
	// standard fonts, the embedded fonts with the glyphs shown, images
	// Im0 and on, pages P0 and on.
	gs      []canvasGS
	fonts   []*CanvasFont
	fbs     []*fallbackFont
	glyphs  map[*fallbackFont]map[opentype.GlyphIndex]rune
	images  []*CanvasImage
	pages   []canvasPage
	lacking bool
}

// canvasGS is a graphics state parameter dictionary: the opacity of what
// is painted and how it blends.
type canvasGS struct {
	alpha float64
	blend BlendMode
}

// canvasState is what Save keeps: the opacity and blend mode asked for,
// and the graphics state set in the content stream.
type canvasState struct {
	opacity float64
	blend   BlendMode
	set     canvasGS
}

// canvasPage is a page drawn on a canvas.
type canvasPage struct {
	src   *Document
	index int
}

// Size returns the size of the displayed page in points.
func (c *Canvas) Size() (w, h float64) { return c.w, c.h }

// Save keeps the state of the canvas — the transform, the clip, the
// opacity and the blend mode — until the Restore that belongs to it. What
// is still saved when the drawing ends is restored.
func (c *Canvas) Save() {
	c.cw.op("q")
	c.stack = append(c.stack, c.cur)
}

// Restore brings back the state kept by the last Save.
func (c *Canvas) Restore() {
	if n := len(c.stack); n > 0 {
		c.cw.op("Q")
		c.cur, c.stack = c.stack[n-1], c.stack[:n-1]
	}
}

// Transform applies m to what is drawn from here on: its coordinates are
// taken through m to those of the canvas as it was.
func (c *Canvas) Transform(m Matrix) {
	c.cw.matrix(m)
	c.cw.op("cm")
}

// SetOpacity sets the opacity of what is drawn from here on, from 0 to 1.
// The alpha of a colour is multiplied with it.
func (c *Canvas) SetOpacity(alpha float64) {
	if !(alpha >= 0) {
		alpha = 0
	}
	c.cur.opacity = min(alpha, 1)
}

// SetBlendMode sets how what is drawn from here on blends with what lies
// under it.
func (c *Canvas) SetBlendMode(b BlendMode) { c.cur.blend = b }

// blendNames are the names of the blend modes in a file.
var blendNames = func() map[BlendMode]pdf.Name {
	m := map[BlendMode]pdf.Name{}
	for name, b := range blendModes {
		if name != "Compatible" {
			m[b] = name
		}
	}
	return m
}()

// state sets the graphics state for painting with the given alpha.
func (c *Canvas) state(alpha float64) {
	need := canvasGS{alpha: math.Round(c.cur.opacity*alpha*1000) / 1000, blend: c.cur.blend}
	if need == c.cur.set {
		return
	}
	i := slices.Index(c.gs, need)
	if i < 0 {
		i = len(c.gs)
		c.gs = append(c.gs, need)
	}
	c.cw.op(fmt.Sprintf("/GS%d gs", i))
	c.cur.set = need
}

// paint sets the colour for filling or stroking, and its opacity. A
// [color.Gray] and a [color.CMYK] are written as they are, any other
// colour as RGB; nil is black.
func (c *Canvas) paint(col color.Color, fill bool) {
	alpha := 1.0
	var comps []float64
	switch v := col.(type) {
	case nil:
		comps = []float64{0}
	case color.Gray:
		comps = []float64{float64(v.Y) / 255}
	case color.CMYK:
		comps = []float64{float64(v.C) / 255, float64(v.M) / 255, float64(v.Y) / 255, float64(v.K) / 255}
	default:
		n := color.NRGBAModel.Convert(col).(color.NRGBA)
		comps = []float64{float64(n.R) / 255, float64(n.G) / 255, float64(n.B) / 255}
		alpha = float64(n.A) / 255
	}
	c.state(alpha)
	c.cw.color(comps, fill)
}

// path writes the construction of p, and reports whether there is any.
func (c *Canvas) path(p *Path) bool {
	if p == nil || p.Empty() {
		return false
	}
	pts := p.Points
	var x, y float64 // the current point
	at := func(q stilus.Point) (float64, float64) { return float64(q.X), float64(q.Y) }
	for _, v := range p.Verbs {
		switch {
		case v == stilus.MoveTo && len(pts) >= 1:
			x, y = at(pts[0])
			c.cw.moveTo(x, y)
			pts = pts[1:]
		case v == stilus.LineTo && len(pts) >= 1:
			x, y = at(pts[0])
			c.cw.lineTo(x, y)
			pts = pts[1:]
		case v == stilus.QuadTo && len(pts) >= 2:
			// As the cubic curve it is.
			qx, qy := at(pts[0])
			ex, ey := at(pts[1])
			c.cw.pt(x+2*(qx-x)/3, y+2*(qy-y)/3)
			c.cw.pt(ex+2*(qx-ex)/3, ey+2*(qy-ey)/3)
			c.cw.pt(ex, ey)
			c.cw.op("c")
			x, y = ex, ey
			pts = pts[2:]
		case v == stilus.CubicTo && len(pts) >= 3:
			for _, q := range pts[:3] {
				c.cw.pt(at(q))
			}
			c.cw.op("c")
			x, y = at(pts[2])
			pts = pts[3:]
		case v == stilus.Close:
			c.cw.op("h")
		}
	}
	return true
}

// Fill fills p with col.
func (c *Canvas) Fill(p *Path, rule FillRule, col color.Color) {
	if p == nil || p.Empty() {
		return
	}
	c.paint(col, true)
	c.path(p)
	if rule == EvenOdd {
		c.cw.op("f*")
	} else {
		c.cw.op("f")
	}
}

// FillRect fills r with col.
func (c *Canvas) FillRect(r Rect, col color.Color) {
	c.paint(col, true)
	c.cw.pt(r.X0, r.Y0)
	c.cw.pt(r.Dx(), r.Dy())
	c.cw.op("re f")
}

// Stroke strokes p with col. A width of 0 is the thinnest line a device
// draws; a miter limit of 0 stands for 10.
func (c *Canvas) Stroke(p *Path, st StrokeStyle, col color.Color) {
	if p == nil || p.Empty() {
		return
	}
	c.paint(col, false)
	c.cw.num(max(st.Width, 0))
	c.cw.op("w")
	miter := st.MiterLimit
	switch {
	case miter == 0:
		miter = 10
	case !(miter >= 1):
		miter = 1
	}
	c.cw.op(fmt.Sprintf("%d J %d j", min(st.Cap, stilus.SquareCap), min(st.Join, stilus.BevelJoin)))
	c.cw.num(miter)
	c.cw.op("M")
	// A dash pattern with a negative length, or of nothing but gaps, is
	// none.
	sum, bad := 0.0, false
	for _, d := range st.Dash {
		bad = bad || !(d >= 0) || math.IsInf(d, 0)
		sum += d
	}
	c.cw = append(c.cw, '[')
	if !bad && sum > 0 {
		for _, d := range st.Dash {
			c.cw.num(d)
		}
	}
	c.cw = append(c.cw, "] "...)
	c.cw.num(st.DashPhase)
	c.cw.op("d")
	c.path(p)
	c.cw.op("S")
}

// Clip keeps what is drawn from here on inside p, until the state saved
// before is restored.
func (c *Canvas) Clip(p *Path, rule FillRule) {
	if !c.path(p) {
		return
	}
	if rule == EvenOdd {
		c.cw.op("W* n")
	} else {
		c.cw.op("W n")
	}
}

// DrawImage draws img into r.
func (c *Canvas) DrawImage(img *CanvasImage, r Rect) {
	if img == nil {
		return
	}
	k := slices.Index(c.images, img)
	if k < 0 {
		k = len(c.images)
		c.images = append(c.images, img)
	}
	c.state(1)
	c.cw.op("q")
	// The image fills the unit square, its top at 1.
	c.cw.matrix(Matrix{r.Dx(), 0, 0, -r.Dy(), r.X0, r.Y1})
	c.cw.op(fmt.Sprintf("cm /Im%d Do Q", k))
}

// DrawPage draws page index of src into r, as it is displayed: its content
// without its annotations. The page is stretched to r; [Page.Size] gives
// its proportions. It is how several pages are put on a sheet, and how a
// page of a file becomes a stamp or a letterhead.
func (c *Canvas) DrawPage(src *Document, index int, r Rect) error {
	if src == nil || index < 0 || index >= src.NumPages() {
		return fmt.Errorf("cera: page index %d is out of range", index)
	}
	if !pdfedit.MayAssemble(src.r) {
		return ErrNoAssembly
	}
	pg, err := src.Page(index)
	if err != nil {
		return err
	}
	w, h := pg.Size()
	if !(w > 0 && h > 0) {
		return errors.New("cera: the page has no area")
	}
	cp := canvasPage{src, index}
	k := slices.Index(c.pages, cp)
	if k < 0 {
		k = len(c.pages)
		c.pages = append(c.pages, cp)
	}
	c.state(1)
	// A page starts from the initial graphics state, not from the colours
	// and the line the canvas drew with last.
	c.cw.op("q 0 g 0 G 1 w 0 J 0 j 10 M [] 0 d")
	c.cw.matrix(pg.DisplayMatrix().Mul(Matrix{r.Dx() / w, 0, 0, r.Dy() / h, r.X0, r.Y0}))
	c.cw.op(fmt.Sprintf("cm /P%d Do Q", k))
	return nil
}

// DrawText draws s in font f at size, in col, from (x, y) on: the start of
// its baseline. It returns the width of what it drew. The text is one
// line, drawn character by character, left to right: there is no shaping,
// no kerning and no reordering. A character f lacks is drawn in a fallback
// font (Latin, Greek, Cyrillic), and as a question mark, or the glyph of
// an embedded font for what it lacks, when that has none either.
func (c *Canvas) DrawText(s string, x, y float64, f *CanvasFont, size float64, col color.Color) float64 {
	if f == nil || s == "" || !(size > 0) {
		return 0
	}
	e := f.encoder(c)
	if e == nil {
		return 0
	}
	c.paint(col, true)
	c.cw.op("BT")
	// Text is upright where y points down.
	c.cw.matrix(Matrix{1, 0, 0, -1, x, y})
	c.cw.op("Tm")
	e.size = size
	if f.fb == nil {
		e.selectFont(&c.cw, nil)
		if !slices.Contains(c.fonts, f) {
			c.fonts = append(c.fonts, f)
		}
	}
	show(&c.cw, e, s, 0, 0)
	c.cw.op("ET")
	return e.width(s) * size
}

// Canvas hears what its text shows, to embed the fonts with it.

func (c *Canvas) helvetica() pdf.Object             { return pdf.Null }
func (c *Canvas) fallback(*fallbackFont) pdf.Object { return pdf.Null }
func (c *Canvas) missing(rune)                      { c.lacking = true }

func (c *Canvas) used(fb *fallbackFont, gid opentype.GlyphIndex, r rune) {
	m := c.glyphs[fb]
	if m == nil {
		m = map[opentype.GlyphIndex]rune{}
		c.glyphs[fb] = m
		c.fbs = append(c.fbs, fb)
	}
	if _, ok := m[gid]; !ok {
		m[gid] = r
	}
}

// CanvasFont is a font to draw text with: one of the standard 14 fonts,
// which a file names and a viewer has, or a font program, which is
// embedded with the glyphs drawn. It may be used by several editors, also
// at once.
type CanvasFont struct {
	std string        // the standard font, or
	fb  *fallbackFont // the program
	res pdf.Name      // its name in the resources of a drawing

	once sync.Once
	f    *Font // the standard font, to measure with
}

// standardFonts are the standard 14 fonts.
var standardFonts = []string{
	"Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Helvetica-BoldOblique",
	"Times-Roman", "Times-Bold", "Times-Italic", "Times-BoldItalic",
	"Courier", "Courier-Bold", "Courier-Oblique", "Courier-BoldOblique",
	"Symbol", "ZapfDingbats",
}

// StandardFont returns one of the standard 14 fonts: Helvetica, Times-Roman
// and Courier with their -Bold, -Oblique (-Italic for Times) and
// -BoldOblique (-BoldItalic) styles, Symbol and ZapfDingbats. They are not
// embedded: a viewer draws them with a font of its own. The text fonts
// show the characters of Windows-1252.
func StandardFont(name string) (*CanvasFont, error) {
	k := slices.Index(standardFonts, name)
	if k < 0 {
		return nil, fmt.Errorf("cera: %q is not a standard font", name)
	}
	return &CanvasFont{std: name, res: pdf.Name(fmt.Sprintf("CeraStd%d", k))}, nil
}

// canvasFontID numbers the embedded fonts, for their names in resources.
var canvasFontID atomic.Int64

// NewCanvasFont returns the font of a TrueType or OpenType program, which
// must have a Unicode character map. The file gets a subset of it with the
// glyphs drawn. data is kept, and must not change. A variable font is
// drawn as its default instance. OpenType fonts whose CFF outlines are
// CID-keyed or variable (CFF2) cannot be used.
func NewCanvasFont(data []byte) (*CanvasFont, error) {
	fb := &fallbackFont{data: data, user: true}
	p := fb.program()
	if p == nil {
		return nil, errors.New("cera: not a TrueType or OpenType font with a Unicode character map")
	}
	if fb.cff {
		if _, err := p.SubsetCFF([]opentype.GlyphIndex{0}); err != nil {
			return nil, fmt.Errorf("cera: %w", err)
		}
	}
	fb.name = postScriptName(p)
	fb.res = pdf.Name(fmt.Sprintf("CeraFont%d", canvasFontID.Add(1)))
	return &CanvasFont{fb: fb, res: fb.res}, nil
}

// postScriptName returns the PostScript name of a font program (name 6 of
// its name table), as a file may spell it.
func postScriptName(p *opentype.Font) string {
	name := ""
	if t, ok := p.Table("name"); ok && len(t) >= 6 {
		n, strs := int(t[2])<<8|int(t[3]), int(t[4])<<8|int(t[5])
		for i := 0; i < n && 6+12*(i+1) <= len(t) && name == ""; i++ {
			rec := t[6+12*i:]
			u16 := func(k int) int { return int(rec[k])<<8 | int(rec[k+1]) }
			off, length := strs+u16(10), u16(8)
			if u16(6) != 6 || off+length > len(t) {
				continue
			}
			s := t[off : off+length]
			switch u16(0) {
			case 0, 3: // Unicode, Windows: UTF-16
				units := make([]uint16, len(s)/2)
				for k := range units {
					units[k] = uint16(s[2*k])<<8 | uint16(s[2*k+1])
				}
				name = string(utf16.Decode(units))
			case 1: // Macintosh
				name = string(s)
			}
		}
	}
	var b []byte
	for _, r := range name {
		if r > 0x20 && r < 0x7f && !bytes.ContainsRune([]byte("()<>[]{}/%#"), r) {
			b = append(b, byte(r))
		}
	}
	if len(b) == 0 {
		return "Font"
	}
	return string(b[:min(len(b), 63)])
}

// canvasDoc is the document standard fonts are measured in: it has the
// stand-ins the renderer draws them with, which have their widths.
var canvasDoc = sync.OnceValue(func() *Document {
	e := NewEditor()
	var b bytes.Buffer
	if e.AddPage(0, 72, 72) != nil || e.Save(&b) != nil {
		return nil
	}
	d, _ := Open(b.Bytes())
	return d
})

// font returns the standard font, loaded once.
func (f *CanvasFont) font() *Font {
	f.once.Do(func() {
		if d := canvasDoc(); d != nil {
			f.f = d.loadFont(f.dict())
		}
	})
	return f.f
}

// dict returns the font dictionary of a standard font.
func (f *CanvasFont) dict() pdf.Dict {
	entries := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("Font").Object()},
		{Key: "Subtype", Val: pdf.Name("Type1").Object()},
		{Key: "BaseFont", Val: pdf.Name(f.std).Object()},
	}
	if f.std != "Symbol" && f.std != "ZapfDingbats" {
		entries = append(entries, pdf.Entry{Key: "Encoding", Val: pdf.Name("WinAnsiEncoding").Object()})
	}
	return pdf.NewDict(entries...)
}

// encoder returns how the font shows text, telling fonts what it shows.
func (f *CanvasFont) encoder(fonts genFonts) *encoder {
	d := canvasDoc()
	if d == nil {
		return nil
	}
	if f.fb != nil {
		return &encoder{prim: f.fb, d: d, fonts: fonts}
	}
	sf := f.font()
	if sf == nil {
		return nil
	}
	return &encoder{f: sf, name: f.res, d: d, fonts: fonts}
}

// Width returns the width of s at size, as [Canvas.DrawText] draws it.
func (f *CanvasFont) Width(s string, size float64) float64 {
	e := f.encoder(renderFonts{})
	if e == nil {
		return 0
	}
	return e.width(s) * size
}

// Metrics returns the ascent and the descent (negative) of the font as
// fractions of its size.
func (f *CanvasFont) Metrics() (ascent, descent float64) {
	if f.fb != nil {
		return float64(f.fb.prog.Ascent()) / f.fb.perEm, float64(f.fb.prog.Descent()) / f.fb.perEm
	}
	if sf := f.font(); sf != nil {
		return sf.Metrics()
	}
	return 0.8, -0.2
}

// CanvasImage is an image to draw, encoded for a file once: drawn on many
// pages, and by several editors, it is one object in each file.
type CanvasImage struct {
	w, h   int
	stream *pdf.Stream
	mask   *pdf.Stream // its alpha, nil for an opaque image
}

// Size returns the size of the image in pixels.
func (im *CanvasImage) Size() (w, h int) { return im.w, im.h }

// NewCanvasImage returns img for drawing: its pixels compressed without
// loss, eight bits a component, grey for an [image.Gray] and RGB
// otherwise, with its alpha as a soft mask when it is not opaque.
func NewCanvasImage(img image.Image) *CanvasImage {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	cs := "DeviceRGB"
	var pix, alpha []byte
	switch src := img.(type) {
	case *image.Gray:
		cs = "DeviceGray"
		pix = make([]byte, w*h)
		for y := range h {
			copy(pix[y*w:(y+1)*w], src.Pix[y*src.Stride:])
		}
	case *image.NRGBA:
		pix, alpha = make([]byte, 3*w*h), make([]byte, w*h)
		for y := range h {
			row := src.Pix[y*src.Stride:]
			for x := range w {
				copy(pix[3*(y*w+x):], row[4*x:4*x+3])
				alpha[y*w+x] = row[4*x+3]
			}
		}
	default:
		pix, alpha = make([]byte, 3*w*h), make([]byte, w*h)
		for y := range h {
			for x := range w {
				c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
				k := y*w + x
				pix[3*k], pix[3*k+1], pix[3*k+2], alpha[k] = c.R, c.G, c.B, c.A
			}
		}
	}
	dict := func(cs string) pdf.Dict {
		return pdf.NewDict(
			pdf.Entry{Key: "Type", Val: pdf.Name("XObject").Object()},
			pdf.Entry{Key: "Subtype", Val: pdf.Name("Image").Object()},
			pdf.Entry{Key: "Width", Val: pdf.Integer(int64(w))},
			pdf.Entry{Key: "Height", Val: pdf.Integer(int64(h))},
			pdf.Entry{Key: "ColorSpace", Val: pdf.Name(cs).Object()},
			pdf.Entry{Key: "BitsPerComponent", Val: pdf.Integer(8)},
		)
	}
	im := &CanvasImage{w: w, h: h, stream: pdfwrite.Flate(dict(cs), pix)}
	if slices.ContainsFunc(alpha, func(a byte) bool { return a != 255 }) {
		im.mask = pdfwrite.Flate(dict("DeviceGray"), alpha)
	}
	return im
}

// NewCanvasJPEG returns a JPEG image for drawing, written into the file as
// it is: baseline or progressive, grey, colour or CMYK. data is kept, and
// must not change.
func NewCanvasJPEG(data []byte) (*CanvasImage, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("cera: %w", err)
	}
	entries := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("XObject").Object()},
		{Key: "Subtype", Val: pdf.Name("Image").Object()},
		{Key: "Width", Val: pdf.Integer(int64(cfg.Width))},
		{Key: "Height", Val: pdf.Integer(int64(cfg.Height))},
		{Key: "BitsPerComponent", Val: pdf.Integer(8)},
		{Key: "Filter", Val: pdf.Name("DCTDecode").Object()},
	}
	switch cfg.ColorModel {
	case color.GrayModel:
		entries = append(entries, pdf.Entry{Key: "ColorSpace", Val: pdf.Name("DeviceGray").Object()})
	case color.CMYKModel:
		// Four components are stored inverted, as Photoshop writes them.
		decode := pdf.Array{}
		for range 4 {
			decode = append(decode, pdf.Integer(1), pdf.Integer(0))
		}
		entries = append(entries,
			pdf.Entry{Key: "ColorSpace", Val: pdf.Name("DeviceCMYK").Object()},
			pdf.Entry{Key: "Decode", Val: decode.Object()})
	default:
		entries = append(entries, pdf.Entry{Key: "ColorSpace", Val: pdf.Name("DeviceRGB").Object()})
	}
	return &CanvasImage{w: cfg.Width, h: cfg.Height, stream: pdf.NewStream(pdf.NewDict(entries...), data)}, nil
}

// drawPlan writes what was drawn on the pages: a form XObject for each
// drawing, and once for the file the fonts, images and pages they show.
func (e *Editor) drawPlan(doc *pdfedit.Doc) {
	add := func(o pdf.Object) pdf.Object {
		doc.Objects = append(doc.Objects, o)
		return pdfedit.NewRef(len(doc.Objects) - 1).Object()
	}
	std := map[string]pdf.Object{"Helvetica": pdfedit.NewRef(0).Object()}
	images := map[*CanvasImage]pdf.Object{}
	pages := map[canvasPage]pdf.Object{}
	for i := range e.pages {
		for _, dr := range e.pages[i].draws {
			c := dr.c
			var res []pdf.Entry

			var fonts []pdf.Entry
			for _, f := range c.fonts {
				o, ok := std[f.std]
				if !ok {
					o = add(f.dict().Object())
					std[f.std] = o
				}
				fonts = append(fonts, pdf.Entry{Key: f.res, Val: o})
			}
			for _, fb := range c.fbs {
				fonts = append(fonts, pdf.Entry{Key: fb.res, Val: e.gen.fallback(fb)})
				for gid, r := range c.glyphs[fb] {
					e.gen.used(fb, gid, r)
				}
			}
			if len(fonts) > 0 {
				res = append(res, pdf.Entry{Key: "Font", Val: pdf.NewDict(fonts...).Object()})
			}

			var xo []pdf.Entry
			for k, im := range c.images {
				o, ok := images[im]
				if !ok {
					s := im.stream
					if im.mask != nil {
						data, _ := s.Unencrypted()
						s = pdf.NewStream(s.Dict.With("SMask", add(im.mask.Object())), data)
					}
					o = add(s.Object())
					images[im] = o
				}
				xo = append(xo, pdf.Entry{Key: pdf.Name(fmt.Sprintf("Im%d", k)), Val: o})
			}
			for k, cp := range c.pages {
				o, ok := pages[cp]
				if !ok {
					o = e.pageForm(doc, cp)
					pages[cp] = o
				}
				xo = append(xo, pdf.Entry{Key: pdf.Name(fmt.Sprintf("P%d", k)), Val: o})
			}
			if len(xo) > 0 {
				res = append(res, pdf.Entry{Key: "XObject", Val: pdf.NewDict(xo...).Object()})
			}

			var gs []pdf.Entry
			for k, g := range c.gs {
				gs = append(gs, pdf.Entry{Key: pdf.Name(fmt.Sprintf("GS%d", k)), Val: pdf.NewDict(
					pdf.Entry{Key: "Type", Val: pdf.Name("ExtGState").Object()},
					pdf.Entry{Key: "ca", Val: pdf.Real(g.alpha)},
					pdf.Entry{Key: "CA", Val: pdf.Real(g.alpha)},
					pdf.Entry{Key: "BM", Val: blendNames[g.blend].Object()},
				).Object()})
			}
			if len(gs) > 0 {
				res = append(res, pdf.Entry{Key: "ExtGState", Val: pdf.NewDict(gs...).Object()})
			}

			matrix := make(pdf.Array, 6)
			for k, v := range dr.m {
				matrix[k] = pdf.Real(v)
			}
			form := pdfwrite.Flate(pdf.NewDict(
				pdf.Entry{Key: "Type", Val: pdf.Name("XObject").Object()},
				pdf.Entry{Key: "Subtype", Val: pdf.Name("Form").Object()},
				pdf.Entry{Key: "BBox", Val: pdf.Array{pdf.Integer(0), pdf.Integer(0), pdf.Real(c.w), pdf.Real(c.h)}.Object()},
				pdf.Entry{Key: "Matrix", Val: matrix.Object()},
				pdf.Entry{Key: "Resources", Val: pdf.NewDict(res...).Object()},
			), c.cw)
			ref, _ := add(form.Object()).Ref()
			doc.Pages[i].Draws = append(doc.Pages[i].Draws, pdfedit.Draw{Form: ref, Under: dr.under})
		}
	}
}

// pageForm adds page cp as a form XObject: its content, decoded and
// joined, with its resources, in the box the page shows. The object is in
// the space of the page's document, whose objects its resources name.
func (e *Editor) pageForm(doc *pdfedit.Doc, cp canvasPage) pdf.Object {
	src := cp.src
	entries := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("XObject").Object()},
		{Key: "Subtype", Val: pdf.Name("Form").Object()},
	}
	box := letter
	if pg, err := src.Page(cp.index); err == nil {
		box = pg.Box
	}
	entries = append(entries, pdf.Entry{Key: "BBox", Val: pdf.Array{pdf.Real(box.X0), pdf.Real(box.Y0), pdf.Real(box.X1), pdf.Real(box.Y1)}.Object()})
	res := pdf.NewDict().Object()
	if pd, err := src.r.Page(cp.index + 1); err == nil {
		if r := pd.Get("Resources"); !r.IsNull() {
			res = r
		}
		// A page that is a transparency group is one as a form too.
		if g := pd.Get("Group"); !g.IsNull() {
			entries = append(entries, pdf.Entry{Key: "Group", Val: g})
		}
	}
	entries = append(entries, pdf.Entry{Key: "Resources", Val: res})
	dec, _ := src.r.PageContents(cp.index + 1)
	doc.Objects = append(doc.Objects, pdfwrite.Flate(pdf.NewDict(entries...), dec.Data).Object())
	k := len(doc.Objects) - 1
	if src != e.base {
		if doc.ObjectSrc == nil {
			doc.ObjectSrc = map[int]*pdf.Document{}
		}
		doc.ObjectSrc[k] = src.r
	}
	return pdfedit.NewRef(k).Object()
}
