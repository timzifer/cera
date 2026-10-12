package cera

import (
	"math"

	"github.com/timzifer/cera/internal/pdf"
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
	// Rect is the rectangle in default user space, which
	// Page.DisplayMatrix maps to points of the displayed page;
	// Markup.Bounds is the result for a markup annotation.
	Rect  Rect
	Flags AnnotFlags
	// Contents is the text of the annotation, or its alternate
	// description.
	Contents string
	Name     string // /NM
	// Link is the target of a Link annotation; nil for other types and
	// for links that go nowhere cera can name.
	Link *LinkTarget
	// Markup is what a markup annotation says about itself — author,
	// dates, colours, geometry, popup, replies; nil for the other types:
	// Link, Popup, Widget and those that hold media or printing marks.
	Markup *Markup

	dict  pdf.Dict
	state pdf.Name // /AS
	ref   pdf.Ref
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
	// External reports a link out of the document: to another PDF file
	// (/GoToR), where Page, Fit, Params and Named are in that file, or to
	// a file or application (/Launch). File names it. cera follows
	// neither, and a viewer should ask before it does.
	External bool
	File     string
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
	mode        AnnotMode
	skip        func(index int) bool
	linkBorders bool
}

// annotLinkBorder marks, among the flags an annotation is recorded with,
// what cera draws for a link without an appearance stream: its border,
// shown only where the options ask for it. No file sets the bit; the
// specification defines ten flags.
const annotLinkBorder AnnotFlags = 1 << 31

// shows reports whether an annotation with flags f at index i is drawn.
func (af *annotFilter) shows(i int, f AnnotFlags) bool {
	if f&annotLinkBorder != 0 && !af.linkBorders {
		return false
	}
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
		p.annots, p.annBad = p.doc.readAnnots(p.dict.Get("Annots"))
		p.readMarkup(p.annots)
	})
	return p.annots
}

func (d *Document) readAnnots(o pdf.Object) (annots []Annotation, bad int) {
	arr, _ := d.resolve(o).Array()
	for i, e := range arr {
		ad := d.dict(e)
		r, ok := d.rect(ad.Get("Rect"))
		if ad.IsZero() || !ok {
			if !emptyRect(d, ad) {
				bad++
			}
			continue
		}
		sub, _ := d.name(ad.Get("Subtype"))
		a := Annotation{Index: i, Subtype: string(sub), Rect: r, dict: ad}
		a.ref, _ = e.Ref()
		if f, ok := d.integer(ad.Get("F")); ok {
			a.Flags = AnnotFlags(uint32(f))
		}
		a.Contents = textString(d.resolve(ad.Get("Contents")))
		a.Name = textString(d.resolve(ad.Get("NM")))
		a.state, _ = d.name(ad.Get("AS"))
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
func emptyRect(d *Document, ad pdf.Dict) bool {
	a, ok := d.resolve(ad.Get("Rect")).Array()
	return !ad.IsZero() && ok && len(a) == 4
}

// linkTarget reads the destination or action of a link.
func (d *Document) linkTarget(ad pdf.Dict) *LinkTarget {
	if dest, ok := ad.Lookup("Dest"); ok {
		return d.destination(dest)
	}
	act := d.dict(ad.Get("A"))
	switch s, _ := d.name(act.Get("S")); s {
	case "URI":
		b, ok := d.resolve(act.Get("URI")).Str()
		if !ok {
			return nil
		}
		return &LinkTarget{URI: string(b), Page: -1}
	case "GoTo":
		return d.destination(act.Get("D"))
	case "GoToR":
		t := &LinkTarget{Page: -1, External: true, File: d.fileSpec(act.Get("F"))}
		switch o := d.resolve(act.Get("D")); o.Kind() {
		case pdf.KindName:
			n, _ := o.Name()
			t.Named = string(n)
		case pdf.KindString:
			t.Named = textString(o)
		case pdf.KindArray:
			// The page is a number in the other file, not a page of this
			// document.
			a, _ := o.Array()
			if len(a) > 0 {
				if n, ok := d.integer(a[0]); ok && n >= 0 {
					t.Page = n
				}
				view := &LinkTarget{}
				d.explicitDest(view, append(pdf.Array{pdf.Null}, a[1:]...))
				t.Fit, t.Params = view.Fit, view.Params
			}
		}
		return t
	case "Launch":
		f := act.Get("F")
		if f.IsNull() {
			// /Win, /Mac, /Unix: the file of a platform.
			f = d.dict(act.Get("Win")).Get("F")
		}
		return &LinkTarget{Page: -1, External: true, File: d.fileSpec(f)}
	}
	return nil
}

// fileSpec returns the file name of a file specification: a string, or a
// dictionary with /UF or /F.
func (d *Document) fileSpec(o pdf.Object) string {
	o = d.resolve(o)
	if fd, ok := o.Dict(); ok {
		if s := textString(d.resolve(fd.Get("UF"))); s != "" {
			return s
		}
		o = d.resolve(fd.Get("F"))
	}
	return textString(o)
}

// destination reads an explicit or named destination.
func (d *Document) destination(o pdf.Object) *LinkTarget {
	o = d.resolve(o)
	t := &LinkTarget{Page: -1}
	switch o.Kind() {
	case pdf.KindName:
		n, _ := o.Name()
		t.Named = string(n)
	case pdf.KindString:
		t.Named = textString(o)
	case pdf.KindArray:
		a, _ := o.Array()
		d.explicitDest(t, a)
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
func (d *Document) explicitDest(t *LinkTarget, a pdf.Array) {
	if len(a) == 0 {
		return
	}
	if p, ok := a[0].Ref(); ok {
		t.Page = d.pageIndex(p)
	} else if n, ok := d.integer(a[0]); ok && n >= 0 && n < d.NumPages() {
		t.Page = n // remote-style destinations number pages
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
func (d *Document) pageIndex(ref pdf.Ref) int {
	d.pageIdxOnce.Do(func() {
		d.pageIdx = make(map[pdf.Ref]int, d.NumPages())
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
func (d *Document) namedDest(name string) pdf.Array {
	cat, err := d.r.Catalog()
	if err != nil {
		return nil
	}
	o := d.dict(cat.Get("Dests")).Get(pdf.Name(name))
	if o.IsNull() {
		o = d.nameTree(d.dict(cat.Get("Names")).Get("Dests"), name)
	}
	o = d.resolve(o)
	if dd, ok := o.Dict(); ok {
		o = d.resolve(dd.Get("D"))
	}
	a, _ := o.Array()
	return a
}

// nameTree finds key in the name tree rooted at o.
func (d *Document) nameTree(o pdf.Object, key string) pdf.Object {
	node := d.dict(o)
	for depth := 0; !node.IsZero() && depth < 32; depth++ {
		if names, ok := d.resolve(node.Get("Names")).Array(); ok {
			for i := 0; i+1 < len(names); i += 2 {
				if k, ok := d.resolve(names[i]).Str(); ok && textString(pdf.String(k)) == key {
					return names[i+1]
				}
			}
			return pdf.Null
		}
		kids, _ := d.resolve(node.Get("Kids")).Array()
		var next pdf.Dict
		for _, k := range kids {
			kd := d.dict(k)
			lim, _ := d.resolve(kd.Get("Limits")).Array()
			if len(lim) == 2 {
				lo, _ := d.resolve(lim[0]).Str()
				hi, _ := d.resolve(lim[1]).Str()
				if key < textString(pdf.String(lo)) || key > textString(pdf.String(hi)) {
					continue
				}
			}
			next = kd
			break
		}
		node = next
	}
	return pdf.Null
}

// appearance returns the normal appearance stream of a in state (its
// /AS, unless a widget's value says otherwise), nil if it has none (or
// none for the state).
func (d *Document) appearance(a *Annotation, state pdf.Name) *pdf.Stream {
	ap := d.dict(a.dict.Get("AP"))
	if ap.IsZero() {
		return nil
	}
	n := d.resolve(ap.Get("N"))
	if s, ok := n.Stream(); ok {
		return s
	}
	if nd, ok := n.Dict(); ok && state != "" {
		return d.stream(nd.Get(state))
	}
	return nil
}

// generated lists the types whose appearance cera draws when /AP is
// missing; see Document.generates for stamps and links.
var generated = map[string]bool{
	"Square": true, "Circle": true, "Line": true, "PolyLine": true, "Polygon": true, "Ink": true,
	"Highlight": true, "Underline": true, "StrikeOut": true, "Squiggly": true,
	"FreeText": true, "Text": true, "FileAttachment": true, "Caret": true,
}

// nothingToDraw lists the types that show nothing on the page without an
// appearance stream: they are no loss, and not counted as annot-no-ap.
var nothingToDraw = map[string]bool{
	"Link": true, "Widget": true, "Popup": true, "Sound": true, "Movie": true, "Screen": true,
	"3D": true, "RichMedia": true, "PrinterMark": true, "TrapNet": true, "Projection": true,
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
		if i >= maxAnnots {
			in.st.unsupported("annot-budget")
			return
		}
		a := &annots[i]
		if a.Flags&AnnotHidden != 0 || a.Subtype == "Popup" {
			continue
		}
		flags := a.Flags &^ annotLinkBorder
		if a.Subtype == "Link" && p.doc.appearance(a, a.state) == nil {
			flags |= annotLinkBorder
		}
		if in.rec == nil && !af.shows(a.Index, flags) {
			continue
		}
		in.annotation(p, a, flags, base, scale)
	}
}

// annotation draws a with its appearance stream or a generated one. A
// display list records it under flags, those of a and annotLinkBorder.
func (in *interp) annotation(p *Page, a *Annotation, flags AnnotFlags, base Matrix, scale float64) {
	doc := in.doc
	var (
		ap  *pdf.Stream
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
		gen = ap == nil && doc.generates(a)
	}
	if ap == nil && !gen {
		if _, hasAP := a.dict.Lookup("AP"); !hasAP && !nothingToDraw[a.Subtype] {
			in.st.unsupported("annot-no-ap")
		}
		return
	}
	oc, hasOC := a.dict.Lookup("OC")
	var expr *ocExpr
	if hasOC {
		var bad bool
		if expr, bad = doc.membership(oc); bad {
			in.st.unsupported("oc-bad")
		}
	}
	switch {
	case in.rec != nil:
		in.setOC(in.rec.annotTag(a.Index, flags, expr))
	case expr != nil && !expr.eval(in.ocVis, in.ocZoom):
		return
	}
	defer in.setOC(0)

	ctm := flagMatrix(a, p.Rotate, scale).Mul(base)

	in.initState(ctm)
	in.base = ctm
	// /CA is the opacity of an appearance cera makes. An appearance
	// stream says how transparent it is itself: the entry is not used
	// then (PDF 2.0, table 173), and writers that set both, as Acrobat
	// does, would be applied twice.
	alpha := 1.0
	if v, ok := doc.num(a.dict.Get("CA")); ok && gen {
		alpha = clamp01(v)
	}
	g := Group{Isolated: true, Blend: BlendNormal, Alpha: unit8(alpha)}
	if gen && a.Subtype == "Highlight" {
		g.Blend = BlendMultiply
	}
	grouped := g.Alpha != 255 || g.Blend != BlendNormal
	box := a.Rect
	if gen && w == nil {
		pad := doc.annotPad(a)
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
		var res pdf.Dict
		in.annotBuf, res = in.generateWidget(in.annotBuf[:0], w, wv)
		in.exec(in.annotBuf, res, 1)
	case gen:
		var res pdf.Dict
		in.annotBuf, res = doc.generateAnnot(in.annotBuf[:0], a, renderFonts{})
		in.exec(in.annotBuf, res, 1)
	default:
		in.gs.ctm = appearanceMatrix(doc, ap, a.Rect).Mul(ctm)
		in.form(ap, pdf.Dict{}, 0)
	}
	in.unwind(0)
	in.popClips(in.gs.clips)
	in.gs.clips = 0
	if grouped {
		in.dev.EndGroup()
	}
}

// flagMatrix places a on a page turned by rotate degrees and shown at
// scale: NoZoom keeps the size of the annotation and NoRotate its
// orientation on screen, both about its upper-left corner.
func flagMatrix(a *Annotation, rotate int, scale float64) Matrix {
	if a.Flags&(AnnotNoZoom|AnnotNoRotate) == 0 {
		return identity
	}
	x, y := a.Rect.X0, a.Rect.Y1
	m := Matrix{1, 0, 0, 1, -x, -y}
	if a.Flags&AnnotNoZoom != 0 {
		m = m.Mul(Matrix{1 / scale, 0, 0, 1 / scale, 0, 0})
	}
	if a.Flags&AnnotNoRotate != 0 && rotate != 0 {
		s, c := math.Sincos(float64(rotate) * math.Pi / 180)
		m = m.Mul(Matrix{c, s, -s, c, 0, 0})
	}
	return m.Mul(Matrix{1, 0, 0, 1, x, y})
}

// appearanceMatrix returns the matrix A of PDF 2.0, 12.5.5, which maps
// the appearance's BBox, transformed by its Matrix, onto rect.
func appearanceMatrix(d *Document, s *pdf.Stream, rect Rect) Matrix {
	bbox, ok := d.rect(s.Dict.Get("BBox"))
	if !ok {
		return Matrix{1, 0, 0, 1, rect.X0, rect.Y0}
	}
	fm := identity
	if a, ok := d.resolve(s.Dict.Get("Matrix")).Array(); ok && len(a) == 6 {
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
