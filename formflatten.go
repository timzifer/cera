package cera

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// Flattening (ADR 0013) draws annotations into the content of their page,
// as the renderer draws them: with their appearance stream, or one made
// for them, their opacity, and in their optional content group.
//
// Flattening a form draws every widget the document shows — with its
// appearance stream, or one made for its value — and the form goes: its
// widgets leave the pages and /AcroForm the catalogue. Values written
// with SetFields are drawn too. Hidden widgets are dropped. The values can
// no longer change.
//
// Page content lies under every annotation the page keeps. So that the
// page still shows what it showed, an annotation that lay under a
// flattened one and overlaps it is flattened with it, and so on down the
// stack; annotations that touch no flattened one stay as they are. For
// the same reason an annotation stays that lies on one the page keeps and
// shows — on a widget, when the form is not flattened.

// Flatten records that the form of the edited document is flattened when
// the editor writes it. It returns [ErrSigned] for a signed document,
// whose signatures are fields too.
func (e *Editor) Flatten() error {
	if e.base == nil {
		return errors.New("cera: a new document has no form")
	}
	if e.base.signing().signed {
		return ErrSigned
	}
	e.flatten = true
	return nil
}

// FlattenAnnotations records that the annotations of the edited document
// are flattened when the editor writes it, for print or the archive: every
// annotation the document shows on screen is drawn into the content of its
// page and leaves the page, with its popup. Links stay, and so do
// annotations that show nothing (hidden ones, those without an appearance
// cera can draw) and the widgets of the form, which [Editor.Flatten]
// draws; an annotation that lies on a widget stays with it, so that it
// does not end up under the widget. Annotations the editor adds itself are
// not flattened.
func (e *Editor) FlattenAnnotations() error {
	if e.base == nil {
		return errors.New("cera: a new document has no annotations")
	}
	e.flattenAnnots = true
	return nil
}

// flattens reports whether the editor draws annotations into pages.
func (e *Editor) flattens() bool { return e.flatten || e.flattenAnnots }

// flattenPatches draws the widgets of the edited document, its
// annotations or both into its pages, as patches and new objects, and
// removes a flattened form.
func (e *Editor) flattenPatches(objs *[]pdf.Object) (out []pdfedit.Patch) {
	d := e.base
	var form *Form
	if e.flatten {
		form = d.Form()
	}
	if form == nil && !e.flattenAnnots {
		return nil
	}
	for i := range d.NumPages() {
		if p, ok := e.flattenPage(i, form, objs, &out); ok {
			out = append(out, p)
		}
	}
	if form != nil {
		if root, ok := d.r.Trailer().Get("Root").Ref(); ok {
			out = append(out, pdfedit.Patch{Ref: root, Del: []pdf.Name{"AcroForm"}})
		}
	}
	return out
}

// shown reports whether a is an annotation the renderer draws on screen
// that flattening may draw into the page: no link, no popup, no widget.
func (d *Document) shown(a *Annotation) bool {
	switch a.Subtype {
	case "Link", "Popup", "Widget":
		return false
	}
	if a.Flags&(AnnotHidden|AnnotNoView) != 0 {
		return false
	}
	return d.appearance(a, a.state) != nil || generated[a.Subtype]
}

// draws reports whether the renderer draws a, a link or a widget of page
// i that is not flattened.
func (d *Document) draws(a *Annotation, i int) bool {
	if w := d.Form().widget(i, a.Index); w != nil && a.Subtype == "Widget" {
		ap, gen := d.widgetLook(a, w, w.Field.Saved)
		return ap != nil || gen
	}
	return d.appearance(a, a.state) != nil
}

// flattenPage returns the change of page i that draws the widgets of form
// (nil: none) and the annotations to flatten, and drops them from the
// page's annotations.
func (e *Editor) flattenPage(i int, form *Form, objs *[]pdf.Object, patches *[]pdfedit.Patch) (pdfedit.Patch, bool) {
	d := e.base
	ref, ok := d.r.PageRef(i + 1)
	if !ok {
		return pdfedit.Patch{}, false
	}
	pd, err := d.r.Page(i + 1) // with the inherited resources
	if err != nil {
		return pdfedit.Patch{}, false
	}
	page, err := d.Page(i)
	if err != nil {
		return pdfedit.Patch{}, false
	}
	annots := page.Annotations()

	// What stays, from the bottom of the stack up: the widgets and links
	// that are not flattened, and what lies on one of them that shows.
	widgets := make([]*Widget, len(annots))
	boxes := make([]Rect, len(annots))
	stays := make([]bool, len(annots))
	var below []Rect
	for k := range annots {
		a := &annots[k]
		boxes[k] = a.Rect
		if a.Subtype == "Widget" {
			widgets[k] = form.widget(i, a.Index)
		}
		switch {
		case widgets[k] != nil:
			continue
		case d.shown(a):
			if d.appearance(a, a.state) == nil {
				pad := d.annotPad(a)
				boxes[k] = Rect{a.Rect.X0 - pad, a.Rect.Y0 - pad, a.Rect.X1 + pad, a.Rect.Y1 + pad}
			}
			stays[k] = overlaps(below, boxes[k])
		default:
			stays[k] = true
			if a.Flags&(AnnotHidden|AnnotNoView) != 0 || !d.draws(a, i) {
				continue
			}
		}
		if stays[k] {
			below = append(below, boxes[k])
		}
	}

	// What is flattened, from the top of the stack down: the widgets, all
	// annotations or those that overlap a flattened one above them.
	flat := make([]bool, len(annots))
	gone := map[int]bool{} // positions in /Annots that leave it
	var above []Rect
	for k := len(annots) - 1; k >= 0; k-- {
		a := &annots[k]
		switch {
		case widgets[k] != nil:
			gone[a.Index] = true
			if a.Flags&(AnnotHidden|AnnotNoView) != 0 {
				continue
			}
		case stays[k], !e.flattenAnnots && !overlaps(above, boxes[k]):
			continue
		}
		flat[k] = true
		above = append(above, boxes[k])
	}

	res := d.dict(pd.Get("Resources"))
	names := resourceNames{
		"XObject":    {dict: d.dict(res.Get("XObject")), prefix: "CeraFlat"},
		"ExtGState":  {dict: d.dict(res.Get("ExtGState")), prefix: "CeraGS"},
		"Properties": {dict: d.dict(res.Get("Properties")), prefix: "CeraOC"},
	}
	var cw csw
	drawn := map[pdf.Ref]bool{} // the flattened annotations
	for k := range annots {
		if !flat[k] {
			continue
		}
		a := &annots[k]
		ap, m, gs, ok := e.flatLook(page, a, widgets[k], objs, patches)
		if !ok {
			continue // nothing to draw: a widget goes, another annotation stays
		}
		gone[a.Index] = true
		drawn[a.ref] = true
		oc, hasOC := a.dict.Lookup("OC")
		if hasOC {
			cw.op("/OC /" + string(names.add("Properties", oc)) + " BDC")
		}
		cw.op("q")
		if !gs.IsZero() {
			cw.op("/" + string(names.add("ExtGState", gs.Object())) + " gs")
		}
		cw.matrix(m)
		cw.op("cm /" + string(names.add("XObject", ap)) + " Do Q")
		if hasOC {
			cw.op("EMC")
		}
	}
	if len(gone) == 0 {
		return pdfedit.Patch{}, false
	}
	// The popups of flattened annotations go with them.
	for k := range annots {
		a := &annots[k]
		if parent, ok := a.dict.Get("Parent").Ref(); ok && a.Subtype == "Popup" && drawn[parent] {
			gone[a.Index] = true
		}
	}
	arr, _ := d.resolve(pd.Get("Annots")).Array()
	var keep pdf.Array
	for k, o := range arr {
		if !gone[k] {
			keep = append(keep, o)
		}
	}
	set := []pdf.Entry{{Key: "Annots", Val: keep.Object()}}
	if len(cw) > 0 {
		// The page's own content goes between q and Q, so that what it
		// leaves in the graphics state does not move the annotations.
		contents := pdf.Array{newStream(objs, []byte("q\n"))}
		switch c := d.resolve(pd.Get("Contents")); {
		case c.Kind() == pdf.KindArray:
			a, _ := c.Array()
			contents = append(contents, a...)
		case !pd.Get("Contents").IsNull():
			contents = append(contents, pd.Get("Contents"))
		}
		contents = append(contents, newStream(objs, append([]byte("Q\n"), cw...)))
		resEntries := entries(res)
		for _, key := range []pdf.Name{"XObject", "ExtGState", "Properties"} {
			if n := names[key]; len(n.added) > 0 {
				resEntries = append(resEntries, pdf.Entry{Key: key, Val: pdf.NewDict(append(entries(n.dict), n.added...)...).Object()})
			}
		}
		set = append(set,
			pdf.Entry{Key: "Contents", Val: contents.Object()},
			pdf.Entry{Key: "Resources", Val: pdf.NewDict(resEntries...).Object()})
	}
	return pdfedit.Patch{Ref: ref, Set: set}, true
}

// matrix writes m in full: the four digits of num would move an
// appearance scaled down from a large box.
func (w *csw) matrix(m Matrix) {
	for _, v := range m {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			v = 0
		}
		*w = strconv.AppendFloat(*w, v, 'f', -1, 64)
		*w = append(*w, ' ')
	}
}

// overlaps reports whether r shares area with one of the boxes.
func overlaps(boxes []Rect, r Rect) bool {
	for _, b := range boxes {
		if min(b.X1, r.X1) > max(b.X0, r.X0) && min(b.Y1, r.Y1) > max(b.Y0, r.Y0) {
			return true
		}
	}
	return false
}

// resourceNames names what flattening adds to the resources of a page, by
// resource category, with names the page does not use.
type resourceNames map[pdf.Name]*resourceCategory

type resourceCategory struct {
	dict   pdf.Dict // the page's
	prefix string
	next   int
	added  []pdf.Entry
}

// add puts o into the category key and returns its name.
func (r resourceNames) add(key pdf.Name, o pdf.Object) pdf.Name {
	c := r[key]
	for {
		name := pdf.Name(fmt.Sprintf("%s%d", c.prefix, c.next))
		c.next++
		if !c.dict.Has(name) {
			c.added = append(c.added, pdf.Entry{Key: name, Val: o})
			return name
		}
	}
}

// flatLook returns what draws annotation a of page p, a widget w of the
// form or (w nil) another annotation, as the renderer draws it
// (interp.annotation): a form XObject, the matrix that puts it on the page
// (PDF 2.0, 12.5.5) and the graphics state it is drawn with, zero for the
// default. A widget shows the value written with SetFields; its
// appearance is made when the document's cannot show it. An opaque
// appearance stream of the document is drawn as it is — one that lacks
// what a form XObject needs, /Subtype /Form and /BBox, which annotations
// tolerate but content does not, gets it by a patch.
func (e *Editor) flatLook(p *Page, a *Annotation, w *Widget, objs *[]pdf.Object, patches *[]pdfedit.Patch) (form pdf.Object, m Matrix, gs pdf.Dict, ok bool) {
	d := e.base
	var (
		ap  *pdf.Stream
		gen bool
		wv  Value
	)
	if w != nil {
		wv, ok = e.fields[w.Field]
		if !ok {
			wv = w.Field.Saved
		}
		ap, gen = d.widgetLook(a, w, wv)
	} else {
		ap = d.appearance(a, a.state)
		gen = ap == nil && generated[a.Subtype]
	}

	// The renderer draws an annotation that is not opaque, and a
	// generated highlight, which multiplies, as an isolated group.
	alpha := 1.0
	if v, ok := d.num(a.dict.Get("CA")); ok {
		alpha = clamp01(v)
	}
	multiply := gen && a.Subtype == "Highlight"
	grouped := unit8(alpha) != 255 || multiply
	if unit8(alpha) == 0 {
		return pdf.Null, Matrix{}, pdf.Dict{}, false
	}
	dict := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("XObject").Object()},
		{Key: "Subtype", Val: pdf.Name("Form").Object()},
	}
	if grouped {
		state := []pdf.Entry{
			{Key: "Type", Val: pdf.Name("ExtGState").Object()},
			{Key: "CA", Val: pdf.Real(alpha)},
			{Key: "ca", Val: pdf.Real(alpha)},
		}
		if multiply {
			state = append(state, pdf.Entry{Key: "BM", Val: pdf.Name("Multiply").Object()})
		}
		gs = pdf.NewDict(state...)
		dict = append(dict, pdf.Entry{Key: "Group", Val: pdf.NewDict(
			pdf.Entry{Key: "S", Val: pdf.Name("Transparency").Object()},
			pdf.Entry{Key: "I", Val: pdf.Boolean(true)},
		).Object()})
	}
	made := func(box Rect, res pdf.Dict, content []byte) pdf.Object {
		if res.IsZero() {
			res = pdf.NewDict()
		}
		dict = append(dict,
			pdf.Entry{Key: "BBox", Val: rectArray(box)},
			pdf.Entry{Key: "Resources", Val: res.Object()})
		*objs = append(*objs, pdfwrite.Flate(pdf.NewDict(dict...), content).Object())
		return pdfedit.NewRef(len(*objs) - 1).Object()
	}

	m = flagMatrix(a, p.Rotate, 1)
	switch {
	case gen && w != nil:
		wm, W, H := widgetMatrix(w)
		if W <= 0 || H <= 0 {
			return pdf.Null, Matrix{}, pdf.Dict{}, false
		}
		content, res := d.widgetContent(nil, w, wv, W, H, e.gen)
		// The renderer does not clip a generated appearance. A box that
		// ended at the widget's edge would clip its border there, and the
		// edge of the clip and the edge of the border, both antialiased,
		// would lighten it.
		return made(Rect{-1, -1, W + 1, H + 1}, res, content), wm.Mul(m), gs, true
	case gen:
		pad := d.annotPad(a)
		box := Rect{a.Rect.X0 - pad, a.Rect.Y0 - pad, a.Rect.X1 + pad, a.Rect.Y1 + pad}
		if !grouped {
			// Only a group bounds what the renderer draws: a line may
			// leave the rectangle of its annotation.
			box = Rect{min(box.X0, p.Box.X0), min(box.Y0, p.Box.Y0), max(box.X1, p.Box.X1), max(box.Y1, p.Box.Y1)}
		}
		return made(box, pdf.Dict{}, d.generateAnnot(nil, a)), m, gs, true
	case ap != nil && ap.Ref.Num > 0:
		var set []pdf.Entry
		if sub, _ := d.name(ap.Dict.Get("Subtype")); sub != "Form" {
			set = append(set, pdf.Entry{Key: "Subtype", Val: pdf.Name("Form").Object()})
		}
		if _, ok := d.rect(ap.Dict.Get("BBox")); !ok {
			// Without a box the appearance is put at the rectangle's
			// corner unscaled: its box is the rectangle's size.
			set = append(set, pdf.Entry{Key: "BBox", Val: pdf.Array{
				pdf.Integer(0), pdf.Integer(0), pdf.Real(a.Rect.Dx()), pdf.Real(a.Rect.Dy())}.Object()})
		}
		if len(set) > 0 {
			*patches = append(*patches, pdfedit.Patch{Ref: ap.Ref, Set: set})
		}
		am := appearanceMatrix(d, ap, a.Rect)
		if !grouped {
			return ap.Ref.Object(), am.Mul(m), gs, true
		}
		// The group: a form around the appearance, whose own box clips
		// it. A second clip at the same edge would lighten the edge.
		var cw csw
		cw.matrix(am)
		cw.op("cm /A Do")
		res := pdf.NewDict(pdf.Entry{Key: "XObject", Val: pdf.NewDict(pdf.Entry{Key: "A", Val: ap.Ref.Object()}).Object()})
		box := Rect{a.Rect.X0 - 1, a.Rect.Y0 - 1, a.Rect.X1 + 1, a.Rect.Y1 + 1}
		return made(box, res, cw), m, gs, true
	}
	return pdf.Null, Matrix{}, pdf.Dict{}, false
}

// entries returns the entries of d, to add to.
func entries(d pdf.Dict) []pdf.Entry {
	out := make([]pdf.Entry, 0, d.Len()+1)
	for k, v := range d.All() {
		out = append(out, pdf.Entry{Key: k, Val: v})
	}
	return out
}

// newStream adds a content stream of data to the new objects and returns
// the reference to it.
func newStream(objs *[]pdf.Object, data []byte) pdf.Object {
	*objs = append(*objs, pdf.NewStream(pdf.NewDict(), data).Object())
	return pdfedit.NewRef(len(*objs) - 1).Object()
}
