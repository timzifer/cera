package cera

import (
	"math"
	"strings"
	"unicode"

	"github.com/timzifer/cera/internal/pdf"
)

// Appearances for annotations without /AP whose look the specification
// names but does not draw: the text of a FreeText annotation, the icons of
// Text and FileAttachment annotations, the caret, the standard stamps and
// the border of a link. Viewers agree on what they show, not on the
// shapes; these are plain ones. Text goes through the encoder of the form
// generator (formgen.go), with its fallback fonts.

// stampNames are the names of the standard stamps (PDF 2.0, table 184).
var stampNames = map[string]bool{
	"Approved": true, "Experimental": true, "NotApproved": true, "AsIs": true,
	"Expired": true, "NotForPublicRelease": true, "Confidential": true, "Final": true,
	"Sold": true, "Departmental": true, "ForComment": true, "TopSecret": true,
	"Draft": true, "ForPublicRelease": true,
}

// generates reports whether cera draws an appearance for a when it has
// none: for the types of generated, but a stamp only with a standard name
// and a link only with a border. A link without /Border and /BS has none:
// the specification gives it one a point wide, which Acrobat never drew,
// and other viewers follow Acrobat.
func (d *Document) generates(a *Annotation) bool {
	switch a.Subtype {
	case "Stamp":
		return stampNames[d.iconName(a, "Draft")]
	case "Link":
		ad := a.dict
		if ad.Get("BS").IsNull() && ad.Get("Border").IsNull() {
			return false
		}
		w, _ := d.border(ad)
		return w > 0
	}
	return generated[a.Subtype]
}

// iconName returns the /Name of a, or def.
func (d *Document) iconName(a *Annotation, def string) string {
	if n, ok := d.name(a.dict.Get("Name")); ok && n != "" {
		return string(n)
	}
	return def
}

// inner returns the rectangle of a less its /RD, the space a border
// effect or a callout takes.
func (d *Document) inner(a *Annotation) Rect {
	r := a.Rect
	if rd := d.floats(a.dict.Get("RD")); len(rd) == 4 {
		if in := (Rect{r.X0 + rd[0], r.Y0 + rd[1], r.X1 - rd[2], r.Y1 - rd[3]}); in.Dx() > 0 && in.Dy() > 0 {
			return in
		}
	}
	return r
}

// linkBorder draws the border of a link: its rectangle, or for the
// underline style its lower edge.
func (d *Document) linkBorder(w csw, a *Annotation) csw {
	ad := a.dict
	bw, dash := d.border(ad)
	c, ok := d.annotColor(ad.Get("C"))
	if !ok {
		c = []float64{0}
	}
	r := a.Rect
	if len(c) == 0 || r.Dx() <= bw || r.Dy() <= bw {
		return w
	}
	w.color(c, false)
	w.num(bw)
	w.op("w")
	if len(dash) > 0 {
		w = append(w, '[')
		for _, v := range dash {
			w.num(v)
		}
		w.op("] 0 d")
	}
	if s, _ := d.name(d.dict(ad.Get("BS")).Get("S")); s == "U" {
		w.moveTo(r.X0, r.Y0+bw/2)
		w.lineTo(r.X1, r.Y0+bw/2)
	} else {
		w.pt(r.X0+bw/2, r.Y0+bw/2)
		w.pt(r.Dx()-bw, r.Dy()-bw)
		w.op("re")
	}
	w.op("S")
	return w
}

// caret draws the caret of a Caret annotation, a wedge that points up
// between two characters.
func (d *Document) caret(w csw, a *Annotation) csw {
	c, ok := d.annotColor(a.dict.Get("C"))
	if !ok {
		c = []float64{0, 0, 1}
	}
	if len(c) == 0 {
		return w
	}
	r := d.inner(a)
	cx, h := (r.X0+r.X1)/2, r.Dy()
	w.color(c, true)
	w.moveTo(r.X0, r.Y0)
	w.pt(cx-r.Dx()/8, r.Y0+h/3)
	w.pt(cx, r.Y0+h*0.8)
	w.pt(cx, r.Y1)
	w.op("c")
	w.pt(cx, r.Y0+h*0.8)
	w.pt(cx+r.Dx()/8, r.Y0+h/3)
	w.pt(r.X1, r.Y0)
	w.op("c h f")
	return w
}

// freeText draws a FreeText annotation: its box, filled with /C and
// outlined in the colour of /DA with the border of /BS, the callout line
// of /CL, and /Contents wrapped into the box in the font, size and colour
// of /DA — the size and colour of the default style string /DS where it
// gives them — aligned as /Q says.
func (d *Document) freeText(w csw, a *Annotation, fonts genFonts) (csw, pdf.Dict) {
	ad := a.dict
	daStr, _ := d.resolve(ad.Get("DA")).Str()
	da := parseDA(string(daStr))
	size, tc := da.size, da.comps
	if ds := textString(d.resolve(ad.Get("DS"))); ds != "" {
		if s, c := parseStyle(ds); s > 0 || c != nil {
			if s > 0 {
				size = s
			}
			if c != nil {
				tc = c
			}
		}
	}
	if size <= 0 {
		size = 12
	}
	bw, dash := d.border(ad)
	box := d.inner(a)
	bg, _ := d.annotColor(ad.Get("C"))

	if len(bg) > 0 {
		w.color(bg, true)
		shape(&w, box, false)
		w.op("f")
	}
	if bw > 0 {
		w.color(da.comps, false)
		w.num(bw)
		w.op("w")
		if len(dash) > 0 {
			w = append(w, '[')
			for _, v := range dash {
				w.num(v)
			}
			w.op("] 0 d")
		}
		if box.Dx() > bw && box.Dy() > bw {
			shape(&w, Rect{box.X0 + bw/2, box.Y0 + bw/2, box.X1 - bw/2, box.Y1 - bw/2}, false)
			w.op("S")
		}
		if cl := pairs(d.floats(ad.Get("CL"))); len(cl) == 2 || len(cl) == 3 {
			w.moveTo(cl[0][0], cl[0][1])
			for _, p := range cl[1:] {
				w.lineTo(p[0], p[1])
			}
			w.op("S")
			if le, _ := d.name(ad.Get("LE")); le != "" && le != "None" {
				ux, uy := unit(cl[0][0]-cl[1][0], cl[0][1]-cl[1][1])
				w.color(da.comps, true)
				ending(&w, le, cl[0][0], cl[0][1], ux, uy, endingSize(bw), func(closed bool) {
					if closed {
						w.op("b")
					} else {
						w.op("S")
					}
				})
			}
		}
		if len(dash) > 0 {
			w.op("[] 0 d")
		}
	}

	text := a.Contents
	in := Rect{box.X0 + bw, box.Y0 + bw, box.X1 - bw, box.Y1 - bw}
	if text == "" || in.Dx() <= 2*textPad || in.Dy() <= 0 {
		return w, pdf.Dict{}
	}
	e := d.drFont(d.Form(), da.font, []string{text}, fonts)
	al := AlignLeft
	if q, ok := d.integer(ad.Get("Q")); ok && q >= 0 && q <= 2 {
		al = Align(q)
	}
	w.op("q")
	clipBox(&w, in)
	textBlock(&w, e, tc, text, in, size, al)
	w.op("Q")
	return w, e.resources()
}

// parseStyle reads the font size in points and the colour of a default
// style string (/DS), CSS as rich text uses it: "font: Helvetica 12pt;
// color: #FF0000". Zero and nil stand for what it does not give.
func parseStyle(s string) (size float64, color []float64) {
	for _, decl := range strings.Split(s, ";") {
		key, val, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		key, val = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(val)
		switch key {
		case "font", "font-size":
			for _, tok := range strings.Fields(val) {
				if n, ok := strings.CutSuffix(strings.ToLower(tok), "pt"); ok {
					if v, ok := parseNum(n); ok && v > 0 {
						size = v
					}
				}
			}
		case "color":
			if len(val) == 7 && val[0] == '#' {
				var c [3]float64
				ok := true
				for i := range c {
					hi, lo := unhex(val[1+2*i]), unhex(val[2+2*i])
					ok = ok && hi >= 0 && lo >= 0
					c[i] = float64(hi*16+lo) / 255
				}
				if ok {
					color = c[:]
				}
			}
		}
	}
	return size, color
}

func unhex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// iconLine is the width of the outlines of icons, in the space of the
// icon.
const iconLine = 0.8

// icon draws the icon of a Text or FileAttachment annotation: a shape in
// a square of 20 units fitted into the rectangle, filled with /C (yellow
// by default) and outlined in black.
func (d *Document) icon(w csw, a *Annotation, fonts genFonts) (csw, pdf.Dict) {
	c, ok := d.annotColor(a.dict.Get("C"))
	if !ok || len(c) == 0 {
		c = []float64{1, 0.82, 0}
	}
	r := a.Rect
	s := min(r.Dx(), r.Dy()) / 20
	if s <= 0 {
		return w, pdf.Dict{}
	}
	w.op("q")
	for _, v := range [6]float64{s, 0, 0, s, r.X0 + (r.Dx()-20*s)/2, r.Y0 + (r.Dy()-20*s)/2} {
		w.num(v)
	}
	w.op("cm")
	w.color(c, true)
	w.op("0 G")
	w.num(iconLine)
	w.op("w 1 j 1 J")

	// label writes s in Helvetica, centred at x, on the baseline y.
	var res pdf.Dict
	label := func(s string, size, x, y float64) {
		e := d.drFont(nil, "", []string{s}, fonts)
		beginText(&w, e, size, []float64{0})
		show(&w, e, s, x-e.width(s)*size/2, y)
		w.op("ET")
		res = e.resources()
	}
	name := "Note"
	if a.Subtype == "FileAttachment" {
		name = "PushPin"
	}
	switch d.iconName(a, name) {
	case "Comment":
		w.op("2 7 m 2 18 l 18 18 l 18 7 l 10 7 l 5 2 l 6 7 l h B")
		w.op("5 14.5 m 15 14.5 l 5 11 m 15 11 l S")
	case "Help":
		ellipse(&w, Rect{2, 2, 18, 18})
		w.op("B")
		label("?", 14, 10, 5)
	case "Insert":
		w.op("10 17 m 17 3 l 10 7 l 3 3 l h B")
	case "Key":
		ellipse(&w, Rect{2, 10, 10, 18})
		w.op("B")
		ellipse(&w, Rect{4.5, 13.5, 6.5, 15.5})
		w.op("S 1.6 w 9 11 m 17 3 l S 14 6 m 16 8 l 12 8 m 14 10 l S")
	case "NewParagraph":
		w.op("10 18 m 15 11 l 5 11 l h B")
		label("NP", 9, 10, 2)
	case "Paragraph":
		w.op("3 2 14 16 re B")
		label("¶", 14, 10, 5)
	case "Paperclip":
		w.op("1.4 w 6 5 m 6 14 l 6 19 13 19 13 14 c 13 5 l 13 1 8 1 8 5 c 8 13 l 8 15.5 11 15.5 11 13 c 11 6 l S")
	case "Graph":
		w.op("5 3 3 6 re 9.5 3 3 12 re 14 3 3 9 re B")
		w.op("3 18 m 3 3 l 18 3 l S")
	case "Tag":
		w.op("2 10 m 7 16 l 18 16 l 18 4 l 7 4 l h B")
		ellipse(&w, Rect{6, 8.8, 8.4, 11.2})
		w.op("S")
	case "PushPin":
		w.op("7 18 m 13 18 l 12 12 l 15 9 l 5 9 l 8 12 l h B")
		w.op("10 9 m 10 2 l S")
	default: // Note, and names cera does not know
		w.op("3 2 m 17 2 l 17 13 l 12 18 l 3 18 l h B")
		w.op("12 18 m 12 13 l 17 13 l S")
		w.op("5.5 6 m 14.5 6 l 5.5 9 m 14.5 9 l 5.5 12 m 10 12 l S")
	}
	w.op("Q")
	return w, res
}

// stamp draws a standard stamp: its name in capitals in a frame, in /C or
// the colour stamps of its kind have.
func (d *Document) stamp(w csw, a *Annotation, fonts genFonts) (csw, pdf.Dict) {
	name := d.iconName(a, "Draft")
	c, ok := d.annotColor(a.dict.Get("C"))
	if !ok || len(c) == 0 {
		switch name {
		case "Approved", "Final", "ForPublicRelease":
			c = []float64{0, 0.5, 0.1}
		case "AsIs", "Departmental", "Experimental", "ForComment":
			c = []float64{0, 0.2, 0.6}
		default:
			c = []float64{0.75, 0, 0}
		}
	}
	r := a.Rect
	lw := max(min(r.Dx(), r.Dy())/16, 0.5)
	in := Rect{r.X0 + lw/2, r.Y0 + lw/2, r.X1 - lw/2, r.Y1 - lw/2}
	if in.Dx() <= 4*lw || in.Dy() <= 4*lw {
		return w, pdf.Dict{}
	}
	w.color(c, false)
	w.num(lw)
	w.op("w")
	roundRect(&w, in, min(in.Dx(), in.Dy())/5)
	w.op("S")

	// "NotForPublicRelease" reads NOT FOR PUBLIC RELEASE.
	var label []rune
	for i, ch := range name {
		if i > 0 && unicode.IsUpper(ch) {
			label = append(label, ' ')
		}
		label = append(label, unicode.ToUpper(ch))
	}
	text := string(label)
	e := d.drFont(nil, "", []string{text}, fonts)
	asc, desc := lineHeight(e)
	pad := 2.5 * lw
	size := min((in.Dy()-2*pad)/(asc-desc), (in.Dx()-2*pad)/max(e.width(text), 0.1))
	if size <= 0 {
		return w, pdf.Dict{}
	}
	beginText(&w, e, size, c)
	show(&w, e, text, (r.X0+r.X1)/2-e.width(text)*size/2, (r.Y0+r.Y1)/2-(asc+desc)*size/2)
	w.op("ET")
	return w, e.resources()
}

// roundRect appends r with corners of radius rad as a path.
func roundRect(w *csw, r Rect, rad float64) {
	rad = max(min(rad, r.Dx()/2, r.Dy()/2), 0)
	k := rad * (1 - 4*(math.Sqrt2-1)/3) // from the corner to the control point
	w.moveTo(r.X0+rad, r.Y0)
	w.lineTo(r.X1-rad, r.Y0)
	w.pt(r.X1-k, r.Y0)
	w.pt(r.X1, r.Y0+k)
	w.pt(r.X1, r.Y0+rad)
	w.op("c")
	w.lineTo(r.X1, r.Y1-rad)
	w.pt(r.X1, r.Y1-k)
	w.pt(r.X1-k, r.Y1)
	w.pt(r.X1-rad, r.Y1)
	w.op("c")
	w.lineTo(r.X0+rad, r.Y1)
	w.pt(r.X0+k, r.Y1)
	w.pt(r.X0, r.Y1-k)
	w.pt(r.X0, r.Y1-rad)
	w.op("c")
	w.lineTo(r.X0, r.Y0+rad)
	w.pt(r.X0, r.Y0+k)
	w.pt(r.X0+k, r.Y0)
	w.pt(r.X0+rad, r.Y0)
	w.op("c h")
}
