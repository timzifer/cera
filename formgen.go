package cera

import (
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/go-opentype/opentype"
	"github.com/timzifer/cera/internal/pdf"
)

// Appearances of widgets (PDF 2.0, 12.7.4.3), generated from a field's
// value when the document's appearance cannot show it: the value differs
// from the saved one, /NeedAppearances is set, or the widget has no /AP.
// Check boxes and radio buttons switch between the states of their /AP
// and are generated only when the state has no appearance. The content is
// drawn in the widget's own box, rotated by /MK /R, with the /DA font
// from the form's /DR (Helvetica when it has none, or cannot encode the
// value, and a fallback font for what Helvetica lacks), and the colours
// and border of /MK and /BS.

// widgetLook decides how annotation a, the widget w with value v, is drawn:
// from the appearance stream ap, generated (gen), or not at all.
func (d *Document) widgetLook(a *Annotation, w *Widget, v Value) (ap *pdf.Stream, gen bool) {
	f := w.Field
	switch f.Type {
	case FieldSignature:
		return d.appearance(a, a.state), false
	case FieldPushButton:
		ap = d.appearance(a, a.state)
		return ap, ap == nil && a.dict.Get("AP").IsNull()
	case FieldCheckBox, FieldRadio:
		state := pdf.Name("Off")
		if v.text == w.OnState {
			state = pdf.Name(w.OnState)
		}
		if ap = d.appearance(a, state); ap != nil {
			return ap, false
		}
		// A state the appearance dictionary leaves out shows nothing,
		// as in other viewers; a widget without one is generated.
		return nil, d.dict(d.dict(a.dict.Get("AP")).Get("N")).IsZero()
	}
	ap = d.appearance(a, a.state)
	return ap, ap == nil || f.form.NeedAppearances || !v.Equal(f.Saved)
}

// helvRef stands for the document's Helvetica, the stand-in generated
// appearances use, in the resource dictionaries they are drawn with. No
// file defines a negative object number; Document.font recognizes it, and
// the numbers below it, which stand for the fallback fonts.
var helvRef = pdf.Ref{Num: -1}

// helvetica returns the document's Helvetica with WinAnsiEncoding.
func (d *Document) helvetica() *Font {
	d.ff.once.Do(func() {
		d.ff.helv = d.loadFont(pdf.NewDict(
			pdf.Entry{Key: "Type", Val: pdf.Name("Font").Object()},
			pdf.Entry{Key: "Subtype", Val: pdf.Name("Type1").Object()},
			pdf.Entry{Key: "BaseFont", Val: pdf.Name("Helvetica").Object()},
			pdf.Entry{Key: "Encoding", Val: pdf.Name("WinAnsiEncoding").Object()},
		))
	})
	return d.ff.helv
}

// encoder is the font of a generated appearance and how it shows text: the
// font of the field or Helvetica, and for the characters that font lacks
// the first fallback font that has them (formfont.go).
type encoder struct {
	f     *Font
	name  pdf.Name
	obj   pdf.Object // f in the resources
	d     *Document
	fonts genFonts
	// The text object being written: its size, and the fallback font
	// selected in it, nil for f.
	size float64
	cur  *fallbackFont
	used []*fallbackFont // the fallback fonts the appearance shows text with
}

// genGlyph is how an encoder shows a character: with code of its own font,
// or with a glyph of a fallback font.
type genGlyph struct {
	fb   *fallbackFont
	code uint16
	adv  float64 // in em
}

// own returns the code of r in the encoder's own font.
func (e *encoder) own(r rune) (byte, bool) {
	enc := e.f.encoding()
	c, ok := enc[r]
	if !ok && (r == '\t' || r == 0xa0) {
		c, ok = enc[' ']
	}
	return c, ok
}

// encodes reports whether the encoder's own font has every character of s.
func (e *encoder) encodes(s string) bool {
	for _, r := range s {
		if _, ok := e.own(r); !ok {
			return false
		}
	}
	return true
}

// glyph returns how r is shown: as '?' when no font has it.
func (e *encoder) glyph(r rune) genGlyph {
	c, ok := e.own(r)
	if !ok {
		if fb, gid, ok := e.d.fallbackGlyph(r); ok {
			return genGlyph{fb: fb, code: uint16(gid), adv: fb.advance(gid)}
		}
		e.fonts.missing(r)
		c = e.f.encoding()['?']
	}
	return genGlyph{code: uint16(c), adv: e.f.advance(int(c))}
}

// width returns the width of s in em.
func (e *encoder) width(s string) float64 {
	w := 0.0
	for _, r := range s {
		w += e.glyph(r).adv
	}
	return w
}

// selectFont makes fb, or the encoder's own font for nil, the font of the
// text object.
func (e *encoder) selectFont(w *csw, fb *fallbackFont) {
	name := e.name
	if fb != nil {
		name = fb.res
		if !slices.Contains(e.used, fb) {
			e.used = append(e.used, fb)
		}
	}
	*w = append(*w, '/')
	*w = append(*w, name...)
	*w = append(*w, ' ')
	w.num(e.size)
	w.op("Tf")
	e.cur = fb
}

// resources returns the resources of what the encoder wrote.
func (e *encoder) resources() pdf.Dict {
	fonts := []pdf.Entry{{Key: e.name, Val: e.obj}}
	for _, fb := range e.used {
		fonts = append(fonts, pdf.Entry{Key: fb.res, Val: e.fonts.fallback(fb)})
	}
	return pdf.NewDict(pdf.Entry{Key: "Font", Val: pdf.NewDict(fonts...).Object()})
}

// encoding returns the code of each character a simple font can show.
func (f *Font) encoding() map[rune]byte {
	f.encOnce.Do(func() {
		f.enc = map[rune]byte{}
		if f.composite() || f.Type3() {
			return
		}
		for c := 255; c >= 0; c-- { // the lowest code wins
			s, ok := f.Text(c)
			if !ok {
				continue
			}
			r, n := utf8.DecodeRuneInString(s)
			if n == len(s) && r != utf8.RuneError {
				f.enc[r] = byte(c)
			}
		}
	})
	return f.enc
}

// genFont returns the font for showing the texts of w: the /DA font if it
// can encode all of them, else Helvetica, with the fallback fonts for what
// Helvetica lacks.
func (d *Document) genFont(w *Widget, texts []string, fonts genFonts) *encoder {
	return d.drFont(w.Field.form, w.fontRes, texts, fonts)
}

// drFont returns the font for showing texts: the font named res in the
// /DR of form (nil: none) if it can encode all of them, else Helvetica.
func (d *Document) drFont(form *Form, res pdf.Name, texts []string, fonts genFonts) *encoder {
	if form != nil && res != "" {
		if o := d.dict(form.dr.Get("Font")).Get(res); !o.IsNull() {
			if f := d.font(o); f != nil && !f.composite() && !f.Type3() {
				e := &encoder{f: f, name: res, obj: o, d: d, fonts: fonts}
				if !slices.ContainsFunc(texts, func(t string) bool { return !e.encodes(t) }) {
					return e
				}
			}
		}
	}
	return &encoder{f: d.helvetica(), name: "CeraHelv", obj: fonts.helvetica(), d: d, fonts: fonts}
}

// widgetMatrix maps the widget's box, [0 W]×[0 H] with its content
// upright, onto its rectangle rotated by /MK /R.
func widgetMatrix(w *Widget) (m Matrix, W, H float64) {
	r := w.Rect
	switch w.Rotation {
	case 90:
		return Matrix{0, 1, -1, 0, r.X1, r.Y0}, r.Dy(), r.Dx()
	case 180:
		return Matrix{-1, 0, 0, -1, r.X1, r.Y1}, r.Dx(), r.Dy()
	case 270:
		return Matrix{0, -1, 1, 0, r.X0, r.Y1}, r.Dy(), r.Dx()
	}
	return Matrix{1, 0, 0, 1, r.X0, r.Y0}, r.Dx(), r.Dy()
}

// List box selections are highlighted in this colour, as in Acrobat.
var selectionColor = []float64{0.6, 0.75862, 0.86275}

// generateWidget appends the appearance of w showing v to b, in default
// user space, and returns the resources it needs.
func (in *interp) generateWidget(b []byte, w *Widget, v Value) ([]byte, pdf.Dict) {
	cw := csw(b)
	m, W, H := widgetMatrix(w)
	if W <= 0 || H <= 0 {
		return b, pdf.Dict{}
	}
	cw.op("q")
	for _, x := range m {
		cw.num(x)
	}
	cw.op("cm")
	cw, res := in.doc.widgetContent(cw, w, v, W, H, renderFonts{})
	cw.op("Q")
	return cw, res
}

// widgetContent appends the appearance of w showing v to cw, drawn in the
// widget's box [0 W]×[0 H] with its content upright, and returns the
// resources it needs. fonts names in them the fonts used when the form's
// font cannot show the text: Helvetica, and the fallback fonts.
func (d *Document) widgetContent(cw csw, w *Widget, v Value, W, H float64, fonts genFonts) (csw, pdf.Dict) {
	f := w.Field
	wd := w.dict

	mk := d.dict(wd.Get("MK"))
	bg, _ := d.annotColor(mk.Get("BG"))
	bc, _ := d.annotColor(mk.Get("BC"))
	a := &w.Appearance
	bw := a.BorderWidth
	if len(bc) == 0 {
		bw = 0
	}
	round := f.Type == FieldRadio
	box := Rect{0, 0, W, H}

	// Background and border.
	if len(bg) > 0 {
		cw.color(bg, true)
		shape(&cw, box, round)
		cw.op("f")
	}
	inset := bw
	if bw > 0 {
		half := Rect{bw / 2, bw / 2, W - bw/2, H - bw/2}
		cw.color(bc, false)
		cw.num(bw)
		cw.op("w")
		switch a.BorderStyle {
		case BorderUnderline:
			cw.moveTo(0, bw/2)
			cw.lineTo(W, bw/2)
			cw.op("S")
		case BorderDashed:
			cw.op("[3] 0 d")
			shape(&cw, half, round)
			cw.op("S [] 0 d")
		default:
			shape(&cw, half, round)
			cw.op("S")
		}
		if a.BorderStyle == BorderBeveled || a.BorderStyle == BorderInset {
			inset = 2 * bw
			light, dark := []float64{1}, []float64{0.75}
			if a.BorderStyle == BorderInset {
				light = []float64{0.5}
			} else if len(bg) > 0 {
				dark = make([]float64, len(bg))
				for i, c := range bg {
					dark[i] = c / 2
				}
				if len(bg) == 4 { // CMYK darkens the other way
					for i, c := range bg {
						dark[i] = c + (1-c)/2
					}
				}
			}
			bevel(&cw, W, H, bw, round, light, dark)
		}
	}

	var res pdf.Dict
	inner := Rect{inset, inset, W - inset, H - inset}
	if inner.Dx() <= 0 || inner.Dy() <= 0 {
		return cw, pdf.Dict{}
	}
	tc := parseDA(f.da).comps
	if s, ok := d.resolve(wd.Get("DA")).Str(); ok {
		tc = parseDA(string(s)).comps
	}

	switch f.Type {
	case FieldCheckBox, FieldRadio:
		if v.text != w.OnState || v.text == "" {
			break
		}
		ca := a.Caption
		if ca == "" {
			ca = "4" // check
			if round {
				ca = "l" // dot
			}
		}
		s := min(inner.Dx(), inner.Dy())
		if a.FontSize > 0 {
			s = min(s, a.FontSize)
		}
		cw.color(tc, true)
		cw.color(tc, false)
		mark(&cw, ca[0], inner, s)

	case FieldText:
		text := v.text
		if f.scripts.format != nil {
			// The field shows its value formatted, a negative number in
			// red where the format says so.
			var red bool
			if text, red = f.scripts.format.text(text); red {
				tc = []float64{1, 0, 0}
			}
		}
		if f.Flags&FfPassword != 0 {
			text = strings.Repeat("*", utf8.RuneCountInString(text))
		}
		multi := f.Flags&FfMultiline != 0
		if !multi {
			text = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(text)
		}
		e := d.genFont(w, []string{text}, fonts)
		clipBox(&cw, inner)
		switch {
		case multi:
			textBlock(&cw, e, tc, text, inner, a.FontSize, a.Align)
		case f.Flags&FfComb != 0 && f.MaxLen > 0:
			combLine(&cw, e, tc, text, inner, a.FontSize, f.MaxLen, bc, bw)
		default:
			textLine(&cw, e, tc, text, inner, a.FontSize, a.Align)
		}
		res = e.resources()

	case FieldComboBox:
		text := v.text
		if v.kind == ValueChoice && len(v.sel) > 0 {
			text = f.Options[v.sel[0]].Text
		}
		e := d.genFont(w, []string{text}, fonts)
		clipBox(&cw, inner)
		textLine(&cw, e, tc, text, inner, a.FontSize, a.Align)
		res = e.resources()

	case FieldListBox:
		texts := make([]string, len(f.Options))
		for i, o := range f.Options {
			texts[i] = o.Text
		}
		e := d.genFont(w, texts, fonts)
		clipBox(&cw, inner)
		listRows(&cw, e, tc, f, v, inner, a.FontSize, a.Align)
		res = e.resources()

	case FieldPushButton:
		if a.Caption == "" {
			break
		}
		e := d.genFont(w, []string{a.Caption}, fonts)
		clipBox(&cw, inner)
		textLine(&cw, e, tc, a.Caption, inner, a.FontSize, AlignCenter)
		res = e.resources()
	}
	return cw, res
}

// shape appends r, or the ellipse in it, as a path.
func shape(w *csw, r Rect, round bool) {
	if round {
		ellipse(w, r)
		return
	}
	w.pt(r.X0, r.Y0)
	w.pt(r.Dx(), r.Dy())
	w.op("re")
}

// bevel paints the light upper-left and the dark lower-right inner edge of
// a beveled or inset border.
func bevel(w *csw, W, H, bw float64, round bool, light, dark []float64) {
	if round {
		// Half circles inside the border, upper left and lower right.
		r := Rect{1.5 * bw, 1.5 * bw, W - 1.5*bw, H - 1.5*bw}
		cx, cy := (r.X0+r.X1)/2, (r.Y0+r.Y1)/2
		rx, ry := r.Dx()/2, r.Dy()/2
		w.num(bw)
		w.op("w")
		for i, c := range [2][]float64{light, dark} {
			w.color(c, false)
			a0 := math.Pi/4 + float64(i)*math.Pi
			for k := 0; k <= 16; k++ {
				s, co := math.Sincos(a0 + float64(k)*math.Pi/16)
				if k == 0 {
					w.moveTo(cx+rx*co, cy+ry*s)
				} else {
					w.lineTo(cx+rx*co, cy+ry*s)
				}
			}
			w.op("S")
		}
		return
	}
	w.color(light, true)
	poly := func(p ...[2]float64) {
		for i, q := range p {
			if i == 0 {
				w.moveTo(q[0], q[1])
			} else {
				w.lineTo(q[0], q[1])
			}
		}
		w.op("h f")
	}
	poly([2]float64{bw, bw}, [2]float64{bw, H - bw}, [2]float64{W - bw, H - bw},
		[2]float64{W - 2*bw, H - 2*bw}, [2]float64{2 * bw, H - 2*bw}, [2]float64{2 * bw, 2 * bw})
	w.color(dark, true)
	poly([2]float64{W - bw, H - bw}, [2]float64{W - bw, bw}, [2]float64{bw, bw},
		[2]float64{2 * bw, 2 * bw}, [2]float64{W - 2*bw, 2 * bw}, [2]float64{W - 2*bw, H - 2*bw})
}

func clipBox(w *csw, r Rect) {
	shape(w, r, false)
	w.op("W n")
}

// mark draws the ZapfDingbats character ch of a check box or radio button
// as a shape of size s centred in r, in the current colours.
func mark(w *csw, ch byte, r Rect, s float64) {
	x0, y0 := (r.X0+r.X1-s)/2, (r.Y0+r.Y1-s)/2
	at := func(u, v float64) (float64, float64) { return x0 + u*s, y0 + v*s }
	poly := func(p ...[2]float64) {
		for i, q := range p {
			x, y := at(q[0], q[1])
			if i == 0 {
				w.moveTo(x, y)
			} else {
				w.lineTo(x, y)
			}
		}
		w.op("h f")
	}
	switch ch {
	case 'l': // circle
		x, y := at(0.2, 0.2)
		ellipse(w, Rect{x, y, x + 0.6*s, y + 0.6*s})
		w.op("f")
	case '8': // cross
		w.num(0.15 * s)
		w.op("w 0 J")
		x, y := at(0.15, 0.15)
		w.moveTo(x, y)
		x, y = at(0.85, 0.85)
		w.lineTo(x, y)
		x, y = at(0.15, 0.85)
		w.moveTo(x, y)
		x, y = at(0.85, 0.15)
		w.lineTo(x, y)
		w.op("S")
	case 'u': // diamond
		poly([2]float64{0.5, 0.05}, [2]float64{0.95, 0.5}, [2]float64{0.5, 0.95}, [2]float64{0.05, 0.5})
	case 'n': // square
		poly([2]float64{0.15, 0.15}, [2]float64{0.85, 0.15}, [2]float64{0.85, 0.85}, [2]float64{0.15, 0.85})
	case 'H': // star
		var p [10][2]float64
		for i := range p {
			rad := 0.47
			if i%2 == 1 {
				rad = 0.19
			}
			s, c := math.Sincos(math.Pi/2 + float64(i)*math.Pi/5)
			p[i] = [2]float64{0.5 + rad*c, 0.48 + rad*s}
		}
		poly(p[:]...)
	default: // '4', a check
		poly([2]float64{0.05, 0.5}, [2]float64{0.2, 0.62}, [2]float64{0.38, 0.38},
			[2]float64{0.82, 0.92}, [2]float64{0.95, 0.8}, [2]float64{0.38, 0.1})
	}
}

// textPad is the space between a field's border and its text.
const textPad = 2

// lineHeight returns the height of a line of e at size 1.
func lineHeight(e *encoder) (asc, desc float64) {
	asc, desc = e.f.Metrics()
	if asc-desc < 0.5 {
		asc, desc = 0.8, -0.2
	}
	return asc, desc
}

// show appends the text s of e at (x, y), a move from the start of the
// line as Td makes it. Each stretch of s that another font shows is a
// string of its own, placed by the widths the encoder measured, so that
// the line is as long as it was laid out whatever widths a font declares;
// the start of the line is then put back to (x, y).
func show(w *csw, e *encoder, s string, x, y float64) {
	w.pt(x, y)
	w.op("Td")
	const hex = "0123456789ABCDEF"
	*w = append(*w, '<')
	font, start := e.cur, len(*w) // of the string being written
	at, moved := 0.0, 0.0         // the pen and the start of the line, from x
	for _, r := range s {
		g := e.glyph(r)
		if g.fb != font {
			if len(*w) == start {
				*w = (*w)[:start-1] // nothing in it
			} else {
				w.op("> Tj")
			}
			e.selectFont(w, g.fb)
			if at != moved {
				w.pt(at-moved, 0)
				w.op("Td")
				moved = at
			}
			*w = append(*w, '<')
			font, start = g.fb, len(*w)
		}
		if g.fb != nil {
			e.fonts.used(g.fb, opentype.GlyphIndex(g.code), r)
			*w = append(*w, hex[g.code>>12], hex[g.code>>8&15])
		}
		*w = append(*w, hex[g.code>>4&15], hex[g.code&15])
		at += g.adv * e.size
	}
	w.op("> Tj")
	if moved != 0 {
		w.pt(-moved, 0)
		w.op("Td")
	}
}

// beginText starts a text object in font e at size with colour tc.
func beginText(w *csw, e *encoder, size float64, tc []float64) {
	w.op("BT")
	e.size = size
	e.selectFont(w, nil)
	w.color(tc, true)
}

// textLine draws one line of text in r, vertically centred; size 0 fits
// it to the height, and shrinks it to the width.
func textLine(w *csw, e *encoder, tc []float64, s string, r Rect, size float64, al Align) {
	asc, desc := lineHeight(e)
	avail := r.Dx() - 2*textPad
	tw := e.width(s)
	if size <= 0 {
		size = (r.Dy() - 2) / (asc - desc)
		if tw > 0 && tw*size > avail {
			size = avail / tw
		}
		size = max(size, 0.5)
	}
	y := r.Y0 + (r.Dy()-(asc-desc)*size)/2 - desc*size
	x := alignX(r, tw*size, al)
	beginText(w, e, size, tc)
	show(w, e, s, x, y)
	w.op("ET")
}

func alignX(r Rect, width float64, al Align) float64 {
	switch al {
	case AlignCenter:
		return r.X0 + (r.Dx()-width)/2
	case AlignRight:
		return r.X1 - textPad - width
	}
	return r.X0 + textPad
}

// combLine draws s into max equal cells across r, a character centred in
// each, with dividers in the border colour.
func combLine(w *csw, e *encoder, tc []float64, s string, r Rect, size float64, n int, bc []float64, bw float64) {
	asc, desc := lineHeight(e)
	cell := r.Dx() / float64(n)
	if size <= 0 {
		size = max(min((r.Dy()-2)/(asc-desc), cell/max(e.width("W"), 0.5)), 0.5)
	}
	if bw > 0 && len(bc) > 0 {
		w.color(bc, false)
		w.num(bw)
		w.op("w")
		for i := 1; i < n; i++ {
			x := r.X0 + float64(i)*cell
			w.moveTo(x, r.Y0)
			w.lineTo(x, r.Y1)
		}
		w.op("S")
	}
	y := r.Y0 + (r.Dy()-(asc-desc)*size)/2 - desc*size
	beginText(w, e, size, tc)
	px, py := 0.0, 0.0
	i := 0
	for _, ch := range s {
		if i >= n {
			break
		}
		cs := string(ch)
		x := r.X0 + (float64(i)+0.5)*cell - e.width(cs)*size/2
		show(w, e, cs, x-px, y-py) // Td moves relative to the line start
		px, py = x, y
		i++
	}
	w.op("ET")
}

// wrap breaks s into lines no wider than width em at size 1.
func wrap(e *encoder, s string, width float64) []string {
	var lines []string
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Split(para, " ") {
			cand := word
			if line != "" {
				cand = line + " " + word
			}
			if e.width(cand) <= width || line == "" && e.width(word) <= width {
				line = cand
				continue
			}
			if line != "" {
				lines = append(lines, line)
			}
			// A word wider than the line is broken between characters.
			line = ""
			for _, ch := range word {
				if line != "" && e.width(line+string(ch)) > width {
					lines = append(lines, line)
					line = ""
				}
				line += string(ch)
			}
		}
		lines = append(lines, line)
	}
	return lines
}

// textBlock draws s wrapped into r from the top; size 0 starts at 12
// points and shrinks until the text fits.
func textBlock(w *csw, e *encoder, tc []float64, s string, r Rect, size float64, al Align) {
	asc, desc := lineHeight(e)
	avail := r.Dx() - 2*textPad
	auto := size <= 0
	if auto {
		size = 12
	}
	var lines []string
	for {
		lines = wrap(e, s, avail/size)
		if !auto || size <= 4 || float64(len(lines))*(asc-desc)*size <= r.Dy()-2*textPad {
			break
		}
		size = max(size-0.5, 4)
	}
	lead := (asc - desc) * size
	y := r.Y1 - textPad - asc*size
	beginText(w, e, size, tc)
	px, py := 0.0, 0.0
	for _, l := range lines {
		if y+asc*size < r.Y0 {
			break
		}
		x := alignX(r, e.width(l)*size, al)
		show(w, e, l, x-px, y-py)
		px, py = x, y
		y -= lead
	}
	w.op("ET")
}

// listRows draws the options of a list box from its TopIndex, the
// selected ones highlighted.
func listRows(w *csw, e *encoder, tc []float64, f *Field, v Value, r Rect, size float64, al Align) {
	asc, desc := lineHeight(e)
	if size <= 0 {
		size = 12
	}
	lead := (asc - desc) * size
	top := min(f.TopIndex, max(len(f.Options)-1, 0))
	y := r.Y1 - 1
	for i := top; i < len(f.Options) && y > r.Y0; i++ {
		y -= lead
		selected := false
		for _, j := range v.sel {
			selected = selected || i == j
		}
		if selected {
			w.color(selectionColor, true)
			w.pt(r.X0, y)
			w.pt(r.Dx(), lead)
			w.op("re f")
		}
		beginText(w, e, size, tc)
		show(w, e, f.Options[i].Text, alignX(r, e.width(f.Options[i].Text)*size, al), y-desc*size)
		w.op("ET")
	}
}
