package cera

import (
	"errors"
	"fmt"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
)

// Flattening a form (ADR 0013): every widget the document shows is drawn
// into the content of its page, as the renderer draws it — its appearance
// stream, or one made for its value — and the form goes: its widgets
// leave the pages and /AcroForm the catalogue. Values written with
// SetFields are drawn too. Hidden widgets are dropped; a widget in an
// optional content group stays in it. The values can no longer change.
//
// The widgets become page content, under every annotation the page keeps:
// one that a widget covered before now covers the widget.

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

// flattenPatches draws the widgets of the edited document into its pages
// and removes the form, as patches and new objects.
func (e *Editor) flattenPatches(objs *[]pdf.Object) (out []pdfedit.Patch) {
	d := e.base
	form := d.Form()
	if form == nil {
		return nil
	}
	for i := range d.NumPages() {
		ws := form.PageWidgets(i)
		if len(ws) == 0 {
			continue
		}
		if p, ok := e.flattenPage(i, ws, objs, &out); ok {
			out = append(out, p)
		}
	}
	if root, ok := d.r.Trailer().Get("Root").Ref(); ok {
		out = append(out, pdfedit.Patch{Ref: root, Del: []pdf.Name{"AcroForm"}})
	}
	return out
}

// flattenPage returns the change of page i that draws its widgets ws and
// drops them from its annotations.
func (e *Editor) flattenPage(i int, ws []*Widget, objs *[]pdf.Object, patches *[]pdfedit.Patch) (pdfedit.Patch, bool) {
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
	byIndex := map[int]*Annotation{}
	annots := page.Annotations()
	for k := range annots {
		byIndex[annots[k].Index] = &annots[k]
	}

	res := d.dict(pd.Get("Resources"))
	xobjects := entries(d.dict(res.Get("XObject")))
	props := entries(d.dict(res.Get("Properties")))
	var cw csw
	n := 0
	for _, w := range ws {
		a := byIndex[w.Annotation]
		if a == nil || w.Flags&(AnnotHidden|AnnotNoView) != 0 {
			continue
		}
		ap, m, ok := e.flatAppearance(a, w, objs, patches)
		if !ok {
			continue
		}
		name := pdf.Name(fmt.Sprintf("CeraFlat%d", n))
		xobjects = append(xobjects, pdf.Entry{Key: name, Val: ap})
		oc, hasOC := w.dict.Lookup("OC")
		if hasOC {
			pn := pdf.Name(fmt.Sprintf("CeraOC%d", n))
			props = append(props, pdf.Entry{Key: pn, Val: oc})
			cw.op("/OC /" + string(pn) + " BDC")
		}
		cw.op("q")
		for _, x := range m {
			cw.num(x)
		}
		cw.op("cm /" + string(name) + " Do Q")
		if hasOC {
			cw.op("EMC")
		}
		n++
	}

	// The page's own content goes between q and Q, so that what it leaves
	// in the graphics state does not move the widgets.
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
	resEntries = append(resEntries, pdf.Entry{Key: "XObject", Val: pdf.NewDict(xobjects...).Object()})
	if len(props) > 0 {
		resEntries = append(resEntries, pdf.Entry{Key: "Properties", Val: pdf.NewDict(props...).Object()})
	}

	// The annotations that are no widgets of the form stay.
	widget := map[int]bool{}
	for _, w := range ws {
		widget[w.Annotation] = true
	}
	arr, _ := d.resolve(pd.Get("Annots")).Array()
	var keep pdf.Array
	for k, o := range arr {
		if !widget[k] {
			keep = append(keep, o)
		}
	}
	return pdfedit.Patch{Ref: ref, Set: []pdf.Entry{
		{Key: "Contents", Val: contents.Object()},
		{Key: "Resources", Val: pdf.NewDict(resEntries...).Object()},
		{Key: "Annots", Val: keep.Object()},
	}}, true
}

// flatAppearance returns the appearance stream widget w (annotation a)
// shows, as the renderer chooses it, and the matrix that draws it on the
// page, the renderer's too (PDF 2.0, 12.5.5). It is made for the widget's
// value when the document's appearance cannot show it. An appearance
// stream that lacks what a form XObject needs, /Subtype /Form and /BBox,
// which annotations tolerate but content does not, gets it by a patch.
func (e *Editor) flatAppearance(a *Annotation, w *Widget, objs *[]pdf.Object, patches *[]pdfedit.Patch) (pdf.Object, Matrix, bool) {
	d := e.base
	v, ok := e.fields[w.Field]
	if !ok {
		v = w.Field.Saved
	}
	st, gen := d.widgetLook(a, w, v)
	switch {
	case gen:
		ref, ok := e.appearance(w, v, objs)
		if !ok {
			return pdf.Null, Matrix{}, false
		}
		made, _ := (*objs)[len(*objs)-1].Stream()
		return ref.Object(), appearanceMatrix(d, made, w.Rect), true
	case st != nil && st.Ref.Num > 0:
		m := appearanceMatrix(d, st, w.Rect)
		var set []pdf.Entry
		if sub, _ := d.name(st.Dict.Get("Subtype")); sub != "Form" {
			set = append(set, pdf.Entry{Key: "Subtype", Val: pdf.Name("Form").Object()})
		}
		if _, ok := d.rect(st.Dict.Get("BBox")); !ok {
			// Without a box the appearance is put at the rectangle's
			// corner unscaled: its box is the rectangle's size.
			set = append(set, pdf.Entry{Key: "BBox", Val: pdf.Array{
				pdf.Integer(0), pdf.Integer(0), pdf.Real(w.Rect.Dx()), pdf.Real(w.Rect.Dy())}.Object()})
		}
		if len(set) > 0 {
			*patches = append(*patches, pdfedit.Patch{Ref: st.Ref, Set: set})
		}
		return st.Ref.Object(), m, true
	}
	return pdf.Null, Matrix{}, false
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
