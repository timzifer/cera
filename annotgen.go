package cera

import (
	"math"
	"strconv"

	"github.com/timzifer/cera/internal/pdf"
)

// Appearances for annotations without /AP, generated as content streams
// in default user space and run through the interpreter like any other
// content. Only the markup types whose look PDF 2.0 (12.5.6) fixes are
// generated: geometry, border, colours and line endings come from the
// annotation dictionary; sizes the specification leaves open (line
// endings, underline thickness) follow common viewers.

// csw writes a content stream.
type csw []byte

func (w *csw) num(v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		v = 0
	}
	*w = strconv.AppendFloat(*w, math.Round(v*1e4)/1e4, 'f', -1, 64)
	*w = append(*w, ' ')
}

func (w *csw) op(s string) { *w = append(append(*w, s...), '\n') }

func (w *csw) pt(x, y float64) { w.num(x); w.num(y) }

func (w *csw) moveTo(x, y float64) { w.pt(x, y); w.op("m") }

func (w *csw) lineTo(x, y float64) { w.pt(x, y); w.op("l") }

// color sets the stroke (or fill) colour from components c.
func (w *csw) color(c []float64, fill bool) {
	for _, v := range c {
		w.num(v)
	}
	ops := [...]string{1: "G", 3: "RG", 4: "K"}
	if fill {
		ops = [...]string{1: "g", 3: "rg", 4: "k"}
	}
	w.op(ops[len(c)])
}

// annotColor reads a colour array: absent is nil and false, an empty
// array (transparent) nil and true.
func (d *Document) annotColor(o pdf.Object) ([]float64, bool) {
	a, ok := d.resolve(o).Array()
	if !ok {
		return nil, false
	}
	if len(a) != 1 && len(a) != 3 && len(a) != 4 {
		return nil, true
	}
	c := make([]float64, len(a))
	for i, e := range a {
		v, _ := d.num(e)
		c[i] = clamp01(v)
	}
	return c, true
}

// border returns the border width and dash pattern of an annotation
// (/BS, else /Border).
func (d *Document) border(ad pdf.Dict) (w float64, dash []float64) {
	w = 1
	if bs := d.dict(ad.Get("BS")); !bs.IsZero() {
		if v, ok := d.num(bs.Get("W")); ok {
			w = math.Abs(v)
		}
		if s, _ := d.name(bs.Get("S")); s == "D" {
			dash = d.floats(bs.Get("D"))
			if len(dash) == 0 {
				dash = []float64{3}
			}
		}
		return w, dash
	}
	if b, ok := d.resolve(ad.Get("Border")).Array(); ok && len(b) >= 3 {
		if v, ok := d.num(b[2]); ok {
			w = math.Abs(v)
		}
		if len(b) >= 4 {
			dash = d.floats(b[3])
		}
	}
	return w, dash
}

// endingSize is the length of a line ending for border width w.
func endingSize(w float64) float64 { return 6 * max(w, 1) }

// annotPad is how far a generated appearance may reach beyond the
// annotation's rectangle.
func (in *interp) annotPad(a *Annotation) float64 {
	w, _ := in.doc.border(a.dict)
	return endingSize(w) + w + 1
}

// generate appends the appearance of a to b.
func (in *interp) generate(b []byte, a *Annotation) []byte {
	d := in.doc
	ad := a.dict
	w := csw(b)
	bw, dash := d.border(ad)
	sc, ok := d.annotColor(ad.Get("C"))
	if !ok {
		sc = []float64{0} // black
		if a.Subtype == "Highlight" {
			sc = []float64{1, 1, 0}
		}
	}
	fc, _ := d.annotColor(ad.Get("IC"))
	stroke := bw > 0 && len(sc) > 0
	if stroke {
		w.color(sc, false)
		w.num(bw)
		w.op("w")
		if len(dash) > 0 {
			w = append(w, '[')
			for _, v := range dash {
				w.num(v)
			}
			w.op("] 0 d")
		}
	}
	if len(fc) > 0 {
		w.color(fc, true)
	}
	paint := func(closed bool) {
		switch {
		case closed && len(fc) > 0 && stroke:
			w.op("b")
		case closed && len(fc) > 0:
			w.op("f")
		case stroke && closed:
			w.op("s")
		case stroke:
			w.op("S")
		default:
			w.op("n")
		}
	}

	switch a.Subtype {
	case "Square", "Circle":
		r := a.Rect
		if rd := d.floats(ad.Get("RD")); len(rd) == 4 {
			r = Rect{r.X0 + rd[0], r.Y0 + rd[1], r.X1 - rd[2], r.Y1 - rd[3]}
		}
		if stroke {
			r = Rect{r.X0 + bw/2, r.Y0 + bw/2, r.X1 - bw/2, r.Y1 - bw/2}
		}
		if r.Dx() <= 0 || r.Dy() <= 0 {
			return b
		}
		if a.Subtype == "Square" {
			w.pt(r.X0, r.Y0)
			w.pt(r.Dx(), r.Dy())
			w.op("re")
		} else {
			ellipse(&w, r)
		}
		paint(true)

	case "Line":
		l := d.floats(ad.Get("L"))
		if len(l) != 4 {
			return b
		}
		x1, y1, x2, y2 := l[0], l[1], l[2], l[3]
		if ll, ok := d.num(ad.Get("LL")); ok && ll != 0 {
			ex, ey := unit(x2-x1, y2-y1)
			nx, ny := ey, -ex // clockwise from start to end
			sgn := math.Copysign(1, ll)
			lle, _ := d.num(ad.Get("LLE"))
			llo, _ := d.num(ad.Get("LLO"))
			for _, p := range [2][2]float64{{x1, y1}, {x2, y2}} {
				o0, o1 := sgn*math.Abs(llo), ll+sgn*math.Abs(lle)
				w.moveTo(p[0]+o0*nx, p[1]+o0*ny)
				w.lineTo(p[0]+o1*nx, p[1]+o1*ny)
			}
			x1, y1, x2, y2 = x1+ll*nx, y1+ll*ny, x2+ll*nx, y2+ll*ny
		}
		w.moveTo(x1, y1)
		w.lineTo(x2, y2)
		paint(false)
		in.endings(&w, ad, [][2]float64{{x1, y1}, {x2, y2}}, bw, paint)

	case "PolyLine", "Polygon":
		v := d.floats(ad.Get("Vertices"))
		pts := pairs(v)
		if len(pts) < 2 {
			return b
		}
		w.moveTo(pts[0][0], pts[0][1])
		for _, p := range pts[1:] {
			w.lineTo(p[0], p[1])
		}
		if a.Subtype == "Polygon" {
			w.op("h")
			paint(true)
		} else {
			paint(false)
			in.endings(&w, ad, pts, bw, paint)
		}

	case "Ink":
		ink, _ := d.resolve(ad.Get("InkList")).Array()
		w.op("1 J 1 j")
		for _, o := range ink {
			pts := pairs(d.floats(o))
			if len(pts) == 0 {
				continue
			}
			w.moveTo(pts[0][0], pts[0][1])
			if len(pts) == 1 {
				w.lineTo(pts[0][0], pts[0][1]) // a dot, with round caps
			}
			for _, p := range pts[1:] {
				w.lineTo(p[0], p[1])
			}
		}
		if stroke {
			w.op("S")
		} else {
			w.op("n")
		}

	case "Highlight", "Underline", "StrikeOut", "Squiggly":
		if len(sc) == 0 {
			return b
		}
		quads := d.floats(ad.Get("QuadPoints"))
		if len(quads) < 8 {
			r := a.Rect
			quads = []float64{r.X0, r.Y1, r.X1, r.Y1, r.X0, r.Y0, r.X1, r.Y0}
		}
		if a.Subtype == "Highlight" {
			w.color(sc, true)
		} else {
			w.color(sc, false)
		}
		for i := 0; i+8 <= len(quads); i += 8 {
			markup(&w, a.Subtype, quads[i:i+8])
		}
	}
	return w
}

// markup draws one quadrilateral of a text markup annotation. Writers
// disagree on the order of the four points (the specification says
// counterclockwise from the lower left, Acrobat writes upper left, upper
// right, lower left, lower right): the first two are taken as one edge
// along the text, the others as the opposite edge, and which is the
// bottom follows from the direction of the first.
func markup(w *csw, kind string, q []float64) {
	ex, ey := unit(q[2]-q[0], q[3]-q[1])
	if ex == 0 && ey == 0 {
		return
	}
	nx, ny := -ey, ex // up, for text running along e
	// The other edge, ordered along e.
	ox0, oy0, ox1, oy1 := q[4], q[5], q[6], q[7]
	if (ox1-ox0)*ex+(oy1-oy0)*ey < 0 {
		ox0, oy0, ox1, oy1 = ox1, oy1, ox0, oy0
	}
	up := ((ox0+ox1)-(q[0]+q[2]))/2*nx + ((oy0+oy1)-(q[1]+q[3]))/2*ny
	bx0, by0, bx1, by1 := q[0], q[1], q[2], q[3] // bottom edge
	tx0, ty0, tx1, ty1 := ox0, oy0, ox1, oy1
	if up < 0 {
		bx0, by0, bx1, by1, tx0, ty0, tx1, ty1 = tx0, ty0, tx1, ty1, bx0, by0, bx1, by1
	}
	h := math.Abs(up)
	if h == 0 {
		return
	}
	switch kind {
	case "Highlight":
		w.moveTo(bx0, by0)
		w.lineTo(bx1, by1)
		w.lineTo(tx1, ty1)
		w.lineTo(tx0, ty0)
		w.op("h f")
	case "Underline", "StrikeOut":
		t := h / 14
		off := t
		if kind == "StrikeOut" {
			off = h * 0.375
		}
		w.num(t)
		w.op("w")
		w.moveTo(bx0+off*nx, by0+off*ny)
		w.lineTo(bx1+off*nx, by1+off*ny)
		w.op("S")
	case "Squiggly":
		t := h / 16
		amp, step := h/16, h/6
		length := (bx1-bx0)*ex + (by1-by0)*ey
		w.num(t)
		w.op("w 1 j")
		for i, s := 0, 0.0; s <= length; i, s = i+1, s+step {
			o := amp * float64(1+i%2*2) // alternately amp and 3·amp up
			x, y := bx0+s*ex+o*nx, by0+s*ey+o*ny
			if i == 0 {
				w.moveTo(x, y)
			} else {
				w.lineTo(x, y)
			}
		}
		w.op("S")
	}
}

// endings draws the line endings (/LE) at the ends of the polyline pts.
func (in *interp) endings(w *csw, ad pdf.Dict, pts [][2]float64, bw float64, paint func(closed bool)) {
	le, _ := in.doc.resolve(ad.Get("LE")).Array()
	if len(le) != 2 || len(pts) < 2 {
		return
	}
	n := len(pts)
	ends := [2][4]float64{
		{pts[0][0], pts[0][1], pts[0][0] - pts[1][0], pts[0][1] - pts[1][1]},
		{pts[n-1][0], pts[n-1][1], pts[n-1][0] - pts[n-2][0], pts[n-1][1] - pts[n-2][1]},
	}
	size := endingSize(bw)
	for i, e := range ends {
		kind, _ := in.doc.name(le[i])
		ux, uy := unit(e[2], e[3]) // outward
		if ux == 0 && uy == 0 {
			continue
		}
		ending(w, kind, e[0], e[1], ux, uy, size, paint)
	}
}

// ending draws one line ending of kind at (x, y), with (ux, uy) the unit
// vector pointing out of the line.
func ending(w *csw, kind pdf.Name, x, y, ux, uy, size float64, paint func(closed bool)) {
	nx, ny := -uy, ux
	r := size / 2
	at := func(along, across float64) (float64, float64) {
		return x + along*ux + across*nx, y + along*uy + across*ny
	}
	poly := func(closed bool, p ...[2]float64) {
		for i, q := range p {
			px, py := at(q[0], q[1])
			if i == 0 {
				w.moveTo(px, py)
			} else {
				w.lineTo(px, py)
			}
		}
		if closed {
			w.op("h")
		}
		paint(closed)
	}
	switch kind {
	case "OpenArrow":
		poly(false, [2]float64{-size, r}, [2]float64{0, 0}, [2]float64{-size, -r})
	case "ClosedArrow":
		poly(true, [2]float64{-size, r}, [2]float64{0, 0}, [2]float64{-size, -r})
	case "ROpenArrow":
		poly(false, [2]float64{size, r}, [2]float64{0, 0}, [2]float64{size, -r})
	case "RClosedArrow":
		poly(true, [2]float64{size, r}, [2]float64{0, 0}, [2]float64{size, -r})
	case "Square":
		poly(true, [2]float64{-r, -r}, [2]float64{r, -r}, [2]float64{r, r}, [2]float64{-r, r})
	case "Diamond":
		poly(true, [2]float64{-r, 0}, [2]float64{0, -r}, [2]float64{r, 0}, [2]float64{0, r})
	case "Circle":
		ellipse(w, Rect{x - r, y - r, x + r, y + r})
		paint(true)
	case "Butt":
		poly(false, [2]float64{0, r}, [2]float64{0, -r})
	case "Slash":
		s, c := math.Sincos(math.Pi / 6) // 60° to the line
		poly(false, [2]float64{r * s, r * c}, [2]float64{-r * s, -r * c})
	}
}

// ellipse appends the ellipse inscribed in r as four Béziers.
func ellipse(w *csw, r Rect) {
	const k = 0.5522847498
	cx, cy := (r.X0+r.X1)/2, (r.Y0+r.Y1)/2
	rx, ry := r.Dx()/2, r.Dy()/2
	w.moveTo(cx+rx, cy)
	w.pt(cx+rx, cy+k*ry)
	w.pt(cx+k*rx, cy+ry)
	w.pt(cx, cy+ry)
	w.op("c")
	w.pt(cx-k*rx, cy+ry)
	w.pt(cx-rx, cy+k*ry)
	w.pt(cx-rx, cy)
	w.op("c")
	w.pt(cx-rx, cy-k*ry)
	w.pt(cx-k*rx, cy-ry)
	w.pt(cx, cy-ry)
	w.op("c")
	w.pt(cx+k*rx, cy-ry)
	w.pt(cx+rx, cy-k*ry)
	w.pt(cx+rx, cy)
	w.op("c h")
}

func unit(x, y float64) (float64, float64) {
	l := math.Hypot(x, y)
	if !(l > 0) || math.IsInf(l, 0) {
		return 0, 0
	}
	return x / l, y / l
}

func pairs(v []float64) [][2]float64 {
	p := make([][2]float64, 0, len(v)/2)
	for i := 0; i+1 < len(v); i += 2 {
		p = append(p, [2]float64{v[i], v[i+1]})
	}
	return p
}
