package cera

import (
	"math"

	"github.com/go-pdfkit/reader"
)

// Annotations (PDF 2.0, 12.5) are drawn after the page content from their
// normal appearance streams, or from an appearance cera generates for the
// markup types whose look the specification fixes. The display list
// records every annotation that can be drawn under a tag of its own, so a
// render chooses which to show without interpreting the page again.

// AnnotFlags are the flags of an annotation (/F).
type AnnotFlags uint32

// Annotation flags.
const (
	AnnotInvisible AnnotFlags = 1 << iota
	AnnotHidden
	AnnotPrint
	AnnotNoZoom
	AnnotNoRotate
	AnnotNoView
	AnnotReadOnly
	AnnotLocked
	AnnotToggleNoView
	AnnotLockedContents
)

// Annotation is an annotation of a page.
type Annotation struct {
	Index   int    // position in /Annots
	Subtype string // "Text", "Link", "Widget", "Square", ...
	Rect    Rect   // in default user space
	Flags   AnnotFlags
	// Contents is the text of the annotation, or its alternate
	// description.
	Contents string
	Name     string // /NM
	// Link is the target of a Link annotation; nil for other types and
	// for links that go nowhere cera can name.
	Link *LinkTarget

	dict  reader.Dict
	state reader.Name // /AS
	ref   reader.Ref
}

// LinkTarget is where a link goes: a URI, or a place in the document.
type LinkTarget struct {
	URI string
	// Page is the 0-based target page, -1 if none.
	Page int
	// Fit is the kind of view of an explicit destination ("XYZ", "Fit",
	// "FitH", "FitV", "FitR", "FitB", "FitBH", "FitBV") and Params its
	// numbers in default user space of the target page; NaN stands for
	// null (unchanged).
	Fit    string
	Params []float64
	// Named is the name of a named destination, which Page, Fit and
	// Params resolve when the document defines it.
	Named string
}

// AnnotMode selects the annotations a render draws.
type AnnotMode uint8

// Annotation modes.
const (
	// AnnotsView draws the annotations meant for the screen: all but
	// those flagged NoView.
	AnnotsView AnnotMode = iota
	// AnnotsPrint draws the annotations flagged Print.
	AnnotsPrint
	// AnnotsNone draws the page content alone.
	AnnotsNone
)

// annotFilter is what a render or run says about annotations.
type annotFilter struct {
	mode AnnotMode
	skip func(index int) bool
}

// shows reports whether an annotation with flags f at index i is drawn.
func (af *annotFilter) shows(i int, f AnnotFlags) bool {
	switch af.mode {
	case AnnotsNone:
		return false
	case AnnotsPrint:
		if f&AnnotPrint == 0 {
			return false
		}
	default:
		if f&AnnotNoView != 0 {
			return false
		}
	}
	return af.skip == nil || !af.skip(i)
}

// Annotations returns the annotations of the page in the order of
// /Annots, read once. Unreadable entries are left out. The slice is
// shared; do not modify it.
func (p *Page) Annotations() []Annotation {
	p.annOnce.Do(func() {
		defer func() {
			if recover() != nil {
				p.annots, p.annBad = nil, 0
			}
		}()
		p.annots, p.annBad = p.doc.readAnnots(p.dict["Annots"])
	})
	return p.annots
}

func (d *Document) readAnnots(o reader.Object) (annots []Annotation, bad int) {
	arr, _ := reader.ToArray(d.resolve(o))
	for i, e := range arr {
		ad := d.dict(e)
		r, ok := d.rect(ad["Rect"])
		if ad == nil || !ok {
			if !emptyRect(d, ad) {
				bad++
			}
			continue
		}
		sub, _ := d.name(ad["Subtype"])
		a := Annotation{Index: i, Subtype: string(sub), Rect: r, dict: ad}
		a.ref, _ = e.(reader.Ref)
		if f, ok := d.integer(ad["F"]); ok {
			a.Flags = AnnotFlags(uint32(f))
		}
		a.Contents = textString(d.resolve(ad["Contents"]))
		a.Name = textString(d.resolve(ad["NM"]))
		a.state, _ = d.name(ad["AS"])
		if sub == "Link" {
			a.Link = d.linkTarget(ad)
		}
		annots = append(annots, a)
	}
	return annots, bad
}

// emptyRect reports an annotation with an empty rectangle, which many
// writers use for annotations that are not meant to be seen (a popup
// never opened, a signature without appearance): not an error.
func emptyRect(d *Document, ad reader.Dict) bool {
	a, ok := reader.ToArray(d.resolve(ad["Rect"]))
	return ad != nil && ok && len(a) == 4
}

// linkTarget reads the destination or action of a link.
func (d *Document) linkTarget(ad reader.Dict) *LinkTarget {
	if dest, ok := ad["Dest"]; ok {
		return d.destination(dest)
	}
	act := d.dict(ad["A"])
	switch s, _ := d.name(act["S"]); s {
	case "URI":
		b, ok := reader.ToString(d.resolve(act["URI"]))
		if !ok {
			return nil
		}
		return &LinkTarget{URI: string(b), Page: -1}
	case "GoTo":
		return d.destination(act["D"])
	}
	return nil
}

// destination reads an explicit or named destination.
func (d *Document) destination(o reader.Object) *LinkTarget {
	o = d.resolve(o)
	t := &LinkTarget{Page: -1}
	switch v := o.(type) {
	case reader.Name:
		t.Named = string(v)
	case reader.String:
		t.Named = textString(v)
	case reader.Array:
		d.explicitDest(t, v)
		return t
	default:
		return nil
	}
	if dest := d.namedDest(t.Named); dest != nil {
		d.explicitDest(t, dest)
	}
	return t
}

// explicitDest fills t from [page /Fit params...].
func (d *Document) explicitDest(t *LinkTarget, a reader.Array) {
	if len(a) == 0 {
		return
	}
	switch p := a[0].(type) {
	case reader.Ref:
		t.Page = d.pageIndex(p)
	default:
		if n, ok := d.integer(p); ok && n >= 0 && n < d.NumPages() {
			t.Page = n // remote-style destinations number pages
		}
	}
	if len(a) > 1 {
		f, _ := d.name(a[1])
		t.Fit = string(f)
		for _, o := range a[2:] {
			v, ok := d.num(o)
			if !ok {
				v = math.NaN()
			}
			t.Params = append(t.Params, v)
		}
	}
}

// pageIndex returns the 0-based index of the page ref, -1 if none.
func (d *Document) pageIndex(ref reader.Ref) int {
	d.pageIdxOnce.Do(func() {
		d.pageIdx = make(map[reader.Ref]int, d.NumPages())
		for i := range d.NumPages() {
			if r, ok := d.r.PageRef(i + 1); ok {
				d.pageIdx[r] = i
			}
		}
	})
	if i, ok := d.pageIdx[ref]; ok {
		return i
	}
	return -1
}

// namedDest looks a named destination up in /Dests of the catalog and in
// the /Dests name tree.
func (d *Document) namedDest(name string) reader.Array {
	cat, err := d.r.Catalog()
	if err != nil {
		return nil
	}
	o := d.dict(cat["Dests"])[reader.Name(name)]
	if o == nil {
		o = d.nameTree(d.dict(cat["Names"])["Dests"], name)
	}
	o = d.resolve(o)
	if dd, ok := reader.ToDict(o); ok {
		o = d.resolve(dd["D"])
	}
	a, _ := reader.ToArray(o)
	return a
}

// nameTree finds key in the name tree rooted at o.
func (d *Document) nameTree(o reader.Object, key string) reader.Object {
	node := d.dict(o)
	for depth := 0; node != nil && depth < 32; depth++ {
		if names, ok := reader.ToArray(d.resolve(node["Names"])); ok {
			for i := 0; i+1 < len(names); i += 2 {
				if k, ok := reader.ToString(d.resolve(names[i])); ok && textString(reader.String(k)) == key {
					return names[i+1]
				}
			}
			return nil
		}
		kids, _ := reader.ToArray(d.resolve(node["Kids"]))
		var next reader.Dict
		for _, k := range kids {
			kd := d.dict(k)
			lim, _ := reader.ToArray(d.resolve(kd["Limits"]))
			if len(lim) == 2 {
				lo, _ := reader.ToString(d.resolve(lim[0]))
				hi, _ := reader.ToString(d.resolve(lim[1]))
				if key < textString(reader.String(lo)) || key > textString(reader.String(hi)) {
					continue
				}
			}
			next = kd
			break
		}
		node = next
	}
	return nil
}

// appearance returns the normal appearance stream of a in state (its
// /AS, unless a widget's value says otherwise), nil if it has none (or
// none for the state).
func (d *Document) appearance(a *Annotation, state reader.Name) *reader.Stream {
	ap := d.dict(a.dict["AP"])
	if ap == nil {
		return nil
	}
	n := d.resolve(ap["N"])
	if s, ok := reader.ToStream(n); ok {
		return s
	}
	if nd, ok := reader.ToDict(n); ok && state != "" {
		return d.stream(nd[state])
	}
	return nil
}

// generated lists the types whose appearance cera draws when /AP is
// missing.
var generated = map[string]bool{
	"Square": true, "Circle": true, "Line": true, "PolyLine": true, "Polygon": true, "Ink": true,
	"Highlight": true, "Underline": true, "StrikeOut": true, "Squiggly": true,
}

// drawAnnots draws the annotations of page p after its content. A display
// list gets all that can be drawn, each under a tag of its own; any other
// device only those af shows.
func (in *interp) drawAnnots(p *Page, base Matrix, scale float64, af *annotFilter) {
	annots := p.Annotations()
	in.st.Errors += p.annBad
	if p.doc.Form(); p.doc.xfa {
		in.st.unsupported("xfa")
	}
	if in.rec == nil && af.mode == AnnotsNone {
		return
	}
	for i := range annots {
		if in.err != nil || in.expired() {
			return
		}
		a := &annots[i]
		if a.Flags&AnnotHidden != 0 || a.Subtype == "Popup" {
			continue
		}
		if in.rec == nil && !af.shows(a.Index, a.Flags) {
			continue
		}
		in.annotation(p, a, base, scale)
	}
}

// annotation draws a with its appearance stream or a generated one.
func (in *interp) annotation(p *Page, a *Annotation, base Matrix, scale float64) {
	doc := in.doc
	var (
		ap  *reader.Stream
		gen bool
		w   *Widget // a widget of the form, drawn with its value wv
		wv  Value
	)
	if a.Subtype == "Widget" {
		w = doc.Form().widget(p.index, a.Index)
	}
	if w != nil {
		wv = w.Field.Saved
		if v, ok := in.formVals[w.Field]; ok {
			wv = v
		}
		ap, gen = doc.widgetLook(a, w, wv)
	} else {
		ap = doc.appearance(a, a.state)
		gen = ap == nil && generated[a.Subtype]
	}
	if ap == nil && !gen {
		if _, hasAP := a.dict["AP"]; !hasAP && a.Subtype != "Link" && a.Subtype != "Widget" {
			in.st.unsupported("annot-no-ap")
		}
		return
	}
	oc, hasOC := a.dict["OC"]
	var expr *ocExpr
	if hasOC {
		var bad bool
		if expr, bad = doc.membership(oc); bad {
			in.st.unsupported("oc-bad")
		}
	}
	switch {
	case in.rec != nil:
		in.setOC(in.rec.annotTag(a.Index, a.Flags, expr))
	case expr != nil && !expr.eval(in.ocVis, in.ocZoom):
		return
	}
	defer in.setOC(0)

	// NoZoom keeps the size of the annotation and NoRotate its
	// orientation on screen, both about its upper-left corner.
	m := identity
	if a.Flags&(AnnotNoZoom|AnnotNoRotate) != 0 {
		x, y := a.Rect.X0, a.Rect.Y1
		m = Matrix{1, 0, 0, 1, -x, -y}
		if a.Flags&AnnotNoZoom != 0 {
			m = m.Mul(Matrix{1 / scale, 0, 0, 1 / scale, 0, 0})
		}
		if a.Flags&AnnotNoRotate != 0 && p.Rotate != 0 {
			s, c := math.Sincos(float64(p.Rotate) * math.Pi / 180)
			m = m.Mul(Matrix{c, s, -s, c, 0, 0})
		}
		m = m.Mul(Matrix{1, 0, 0, 1, x, y})
	}
	ctm := m.Mul(base)

	in.initState(ctm)
	in.base = ctm
	alpha := 1.0
	if v, ok := doc.num(a.dict["CA"]); ok {
		alpha = clamp01(v)
	}
	g := Group{Isolated: true, Blend: BlendNormal, Alpha: unit8(alpha)}
	if gen && a.Subtype == "Highlight" {
		g.Blend = BlendMultiply
	}
	grouped := g.Alpha != 255 || g.Blend != BlendNormal
	box := a.Rect
	if gen && w == nil {
		pad := in.annotPad(a)
		box = Rect{box.X0 - pad, box.Y0 - pad, box.X1 + pad, box.Y1 + pad}
	}
	if grouped {
		if g.Alpha == 0 {
			return
		}
		in.st.Groups++
		in.dev.BeginGroup(box, ctm, &g)
	}
	switch {
	case gen && w != nil:
		var res reader.Dict
		in.annotBuf, res = in.generateWidget(in.annotBuf[:0], w, wv)
		in.exec(in.annotBuf, res, 1)
	case gen:
		in.annotBuf = in.generate(in.annotBuf[:0], a)
		in.exec(in.annotBuf, nil, 1)
	default:
		in.gs.ctm = appearanceMatrix(doc, ap, a.Rect).Mul(ctm)
		in.form(ap, nil, 0)
	}
	in.unwind(0)
	in.popClips(in.gs.clips)
	in.gs.clips = 0
	if grouped {
		in.dev.EndGroup()
	}
}

// appearanceMatrix returns the matrix A of PDF 2.0, 12.5.5, which maps
// the appearance's BBox, transformed by its Matrix, onto rect.
func appearanceMatrix(d *Document, s *reader.Stream, rect Rect) Matrix {
	bbox, ok := d.rect(s.Dict["BBox"])
	if !ok {
		return Matrix{1, 0, 0, 1, rect.X0, rect.Y0}
	}
	fm := identity
	if a, ok := reader.ToArray(d.resolve(s.Dict["Matrix"])); ok && len(a) == 6 {
		var m Matrix
		valid := true
		for i, o := range a {
			var ok bool
			m[i], ok = d.num(o)
			valid = valid && ok
		}
		if valid {
			fm = m
		}
	}
	x0, y0 := math.Inf(1), math.Inf(1)
	x1, y1 := math.Inf(-1), math.Inf(-1)
	for _, c := range [4][2]float64{{bbox.X0, bbox.Y0}, {bbox.X1, bbox.Y0}, {bbox.X0, bbox.Y1}, {bbox.X1, bbox.Y1}} {
		x, y := fm.Apply(c[0], c[1])
		x0, x1 = min(x0, x), max(x1, x)
		y0, y1 = min(y0, y), max(y1, y)
	}
	sx, sy := 1.0, 1.0
	if x1-x0 > 1e-9 {
		sx = rect.Dx() / (x1 - x0)
	}
	if y1-y0 > 1e-9 {
		sy = rect.Dy() / (y1 - y0)
	}
	return Matrix{sx, 0, 0, sy, rect.X0 - x0*sx, rect.Y0 - y0*sy}
}
