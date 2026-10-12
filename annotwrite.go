package cera

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// Writing annotations (ADR 0013). The editor adds annotations to the pages
// of the edited document, changes those it has and removes them. An
// annotation is written with what describes it — geometry, colours, text,
// author, dates — and with an appearance stream made by the generator that
// draws annotations without one (annotgen.go, annotlook.go), so that every
// viewer shows what cera shows. Text gets its fonts as the appearances of
// form fields do (formfont.go).

// NewAnnotation describes an annotation to add to a page, or what an
// annotation of the page becomes. Its Markup holds what [Annotation.Markup]
// reads back, with geometry in points of the displayed page; which of its
// fields a type uses is said there.
//
// The types that can be written, with the geometry they need:
//
//   - "Text", a sticky note, "FreeText", "Square", "Circle", "Caret" and
//     "Stamp" (Icon, a standard stamp name, "Draft" by default): Bounds;
//   - "Highlight", "Underline", "Squiggly", "StrikeOut": Quads;
//   - "Ink": Ink; "Polygon", "PolyLine": Vertices; "Line": Line;
//   - "Link": Bounds or Quads, and Link, where it leads.
//
// A shape needs a border or an interior colour to show. A zero Opacity
// means opaque.
type NewAnnotation struct {
	Subtype string
	// Contents is the text of the annotation: of a note, of a free text,
	// or a comment on what another type marks. A note, and any other
	// annotation with a comment but a free text, gets a popup window.
	Contents string
	// Name identifies the annotation on its page (/NM).
	Name string
	// Flags are the flags of the annotation; zero stands for AnnotPrint,
	// and for a note also AnnotNoZoom and AnnotNoRotate, which keep its
	// icon the same size and upright.
	Flags AnnotFlags
	// Link is where a "Link" leads: a URI, a page of the edited document
	// with a view, a named destination, or another file.
	Link *LinkTarget
	Markup
}

// EditAnnotation returns the annotation of the page with the given Index
// as [Editor.SetAnnotation] takes it, to change what should change. It
// reports false for an annotation the editor cannot write: a widget, a
// popup, which is changed through its annotation, and types beyond those
// of [NewAnnotation].
func (p *Page) EditAnnotation(index int) (NewAnnotation, bool) {
	annots := p.Annotations()
	for k := range annots {
		a := &annots[k]
		if a.Index != index || !writable[a.Subtype] {
			continue
		}
		out := NewAnnotation{Subtype: a.Subtype, Contents: a.Contents, Name: a.Name, Flags: a.Flags}
		d := p.doc
		m := p.DisplayMatrix()
		shown := func(r Rect) Rect {
			return Quad{[4][2]float64{{r.X0, r.Y0}, {r.X1, r.Y0}, {r.X0, r.Y1}, {r.X1, r.Y1}}}.Transform(m).Bounds()
		}
		switch {
		case a.Markup != nil:
			out.Markup = *a.Markup
			if a.Subtype == "FreeText" {
				// The box of the text, without the room of a callout.
				out.Bounds = shown(d.inner(a))
			}
		case a.Subtype == "Link":
			out.Bounds = shown(a.Rect)
			if arr, _ := d.resolve(a.dict.Get("QuadPoints")).Array(); len(arr) >= 8 {
				for _, q := range d.quadPoints(a) {
					out.Quads = append(out.Quads, q.Transform(m))
				}
			}
			out.Color, _ = d.annotColor(a.dict.Get("C"))
			if d.generates(a) {
				out.BorderWidth, out.Dash = d.border(a.dict)
				switch s, _ := d.name(d.dict(a.dict.Get("BS")).Get("S")); {
				case s == "U":
					out.BorderStyle = BorderUnderline
				case len(out.Dash) > 0:
					out.BorderStyle = BorderDashed
				}
			}
			out.Opacity = 1
			if a.Link != nil {
				t := *a.Link
				out.Link = &t
			}
		}
		return out, true
	}
	return NewAnnotation{}, false
}

// writable lists the types the editor writes, and whether they have a
// border (/BS).
var writable = map[string]bool{
	"Text": true, "FreeText": true, "Line": true, "Square": true, "Circle": true,
	"Polygon": true, "PolyLine": true, "Highlight": true, "Underline": true,
	"Squiggly": true, "StrikeOut": true, "Caret": true, "Stamp": true, "Ink": true, "Link": true,
}

var bordered = map[string]bool{
	"FreeText": true, "Line": true, "Square": true, "Circle": true,
	"Polygon": true, "PolyLine": true, "Ink": true, "Link": true,
}

// checked returns a as the editor keeps it — a copy with its defaults
// filled in — or what is wrong with it. A stamp that is added needs a
// name cera draws; one of the document may have a picture of its own.
func (a NewAnnotation) checked(added bool) (NewAnnotation, error) {
	if !writable[a.Subtype] {
		return a, fmt.Errorf("cera: annotations of type %q cannot be written", a.Subtype)
	}
	finite := func(v ...float64) bool {
		for _, x := range v {
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return false
			}
		}
		return true
	}
	colour := func(what string, c []float64) error {
		switch len(c) {
		case 0, 1, 3, 4:
		default:
			return fmt.Errorf("cera: %s has 1, 3 or 4 components, not %d", what, len(c))
		}
		for _, v := range c {
			if !(v >= 0 && v <= 1) {
				return fmt.Errorf("cera: component %g of %s is out of range [0, 1]", v, what)
			}
		}
		return nil
	}
	if err := colour("a colour", a.Color); err != nil {
		return a, err
	}
	if err := colour("an interior colour", a.Interior); err != nil {
		return a, err
	}
	if !(a.Opacity >= 0 && a.Opacity <= 1) {
		return a, fmt.Errorf("cera: opacity %g is out of range [0, 1]", a.Opacity)
	}
	if a.Opacity == 0 {
		a.Opacity = 1
	}
	if !(a.BorderWidth >= 0) || math.IsInf(a.BorderWidth, 0) {
		return a, fmt.Errorf("cera: border width %g", a.BorderWidth)
	}
	a.Color, a.Interior, a.Dash = slices.Clone(a.Color), slices.Clone(a.Interior), slices.Clone(a.Dash)

	box := func() error {
		b := a.Bounds
		if !finite(b.X0, b.Y0, b.X1, b.Y1) || !(b.Dx() > 0 && b.Dy() > 0) {
			return fmt.Errorf("cera: a %s annotation needs bounds that enclose an area", a.Subtype)
		}
		return nil
	}
	path := func(pts [][2]float64, least int) ([][2]float64, error) {
		for _, p := range pts {
			if !finite(p[0], p[1]) {
				return nil, fmt.Errorf("cera: a point of a %s annotation is not a number", a.Subtype)
			}
		}
		if len(pts) < least {
			return nil, fmt.Errorf("cera: a %s annotation needs %d points", a.Subtype, least)
		}
		return slices.Clone(pts), nil
	}
	shows := a.BorderWidth > 0 && len(a.Color) > 0 || len(a.Interior) > 0
	if a.Subtype != "Link" && bordered[a.Subtype] && a.BorderWidth > 0 && len(a.Color) == 0 && a.Subtype != "FreeText" {
		a.Color = []float64{0} // black, as the generator draws a border without /C
		shows = true
	}
	var err error
	switch a.Subtype {
	case "Text", "Caret":
		err = box()
	case "FreeText":
		if err = box(); err == nil && len(a.Callout) > 0 {
			if a.Callout, err = path(a.Callout, 2); err == nil && len(a.Callout) > 3 {
				err = errors.New("cera: a callout has two or three points")
			}
		}
	case "Square", "Circle":
		if err = box(); err == nil && !shows {
			err = fmt.Errorf("cera: a %s annotation needs a border or an interior colour", a.Subtype)
		}
	case "Stamp":
		if a.Icon == "" {
			a.Icon = "Draft"
		}
		if err = box(); err == nil && added && !stampNames[a.Icon] {
			err = fmt.Errorf("cera: %q is no standard stamp", a.Icon)
		}
	case "Highlight", "Underline", "Squiggly", "StrikeOut":
		var quads []Quad
		for _, q := range a.Quads {
			if q.area() > 1e-6 {
				quads = append(quads, q)
			}
		}
		if a.Quads = quads; len(quads) == 0 {
			err = fmt.Errorf("cera: a %s annotation needs quads that enclose an area", a.Subtype)
		}
		if len(a.Color) == 0 {
			a.Color = []float64{0}
			if a.Subtype == "Highlight" {
				a.Color = []float64{1, 1, 0}
			}
		}
	case "Ink":
		var strokes [][][2]float64
		for _, s := range a.Ink {
			if len(s) == 0 {
				continue
			}
			var pts [][2]float64
			if pts, err = path(s, 1); err != nil {
				break
			}
			strokes = append(strokes, pts)
		}
		if a.Ink = strokes; err == nil && len(strokes) == 0 {
			err = errors.New("cera: an Ink annotation needs a stroke")
		}
		if err == nil && !(a.BorderWidth > 0) {
			err = errors.New("cera: an Ink annotation needs a border width")
		}
	case "Polygon":
		if a.Vertices, err = path(a.Vertices, 3); err == nil && !shows {
			err = errors.New("cera: a Polygon annotation needs a border or an interior colour")
		}
	case "PolyLine":
		if a.Vertices, err = path(a.Vertices, 2); err == nil && !(a.BorderWidth > 0) {
			err = errors.New("cera: a PolyLine annotation needs a border width")
		}
	case "Line":
		if a.Line, err = path(a.Line, 2); err == nil && len(a.Line) != 2 {
			err = errors.New("cera: a line has two points")
		}
		if err == nil && !(a.BorderWidth > 0) {
			err = errors.New("cera: a Line annotation needs a border width")
		}
	case "Link":
		if a.Link == nil {
			return a, errors.New("cera: a Link annotation needs a target")
		}
		t := *a.Link
		t.Params = slices.Clone(t.Params)
		a.Link = &t
		if len(a.Quads) > 0 {
			a.Quads = slices.Clone(a.Quads)
			b := a.Quads[0].Bounds()
			for _, q := range a.Quads[1:] {
				qb := q.Bounds()
				b = Rect{min(b.X0, qb.X0), min(b.Y0, qb.Y0), max(b.X1, qb.X1), max(b.Y1, qb.Y1)}
			}
			a.Bounds = b
		}
		err = box()
	}
	if err != nil {
		return a, err
	}
	if a.Popup != nil {
		p := *a.Popup
		a.Popup = &p
	}
	if a.Reply != nil {
		r := *a.Reply
		a.Reply = &r
	}
	return a, nil
}

// annotEdits are the changes to the annotations of a page of the edited
// document. An annotation is named by its Index, its place in /Annots; one
// that the editor adds gets the next index after those and after the ones
// added before it.
type annotEdits struct {
	n     int                    // entries of /Annots
	set   map[int]*NewAnnotation // annotations of the document that change
	gone  map[int]bool           // removed ones
	added []*NewAnnotation
}

// annotPage returns the changes to the annotations of page i of the
// result, a page of the edited document, and the page.
func (e *Editor) annotPage(i int) (*annotEdits, *Page, error) {
	if err := e.check(i); err != nil {
		return nil, nil, err
	}
	ep := &e.pages[i]
	if !ep.own {
		return nil, nil, fmt.Errorf("cera: page %d is not a page of the edited document", i)
	}
	page, err := e.base.Page(ep.index)
	if err != nil {
		return nil, nil, err
	}
	if ep.annots == nil {
		arr, _ := e.base.resolve(page.dict.Get("Annots")).Array()
		ep.annots = &annotEdits{n: len(arr), set: map[int]*NewAnnotation{}, gone: map[int]bool{}}
	}
	return ep.annots, page, nil
}

// existing returns the annotation of the document with the given Index
// that the edits have not removed.
func (ae *annotEdits) existing(page *Page, index int) (*Annotation, error) {
	if ae.gone[index] {
		return nil, fmt.Errorf("cera: annotation %d was removed", index)
	}
	annots := page.Annotations()
	for k := range annots {
		if annots[k].Index == index {
			return &annots[k], nil
		}
	}
	return nil, fmt.Errorf("cera: the page has no annotation %d", index)
}

// target checks that the edits can write what a refers to: the annotation
// it replies to and the page its link leads to.
func (e *Editor) target(ae *annotEdits, page *Page, a *NewAnnotation, self int) error {
	if r := a.Reply; r != nil {
		switch {
		case r.To == self:
			return errors.New("cera: an annotation cannot reply to itself")
		case r.To >= ae.n:
			if r.To-ae.n >= len(ae.added) || ae.gone[r.To] {
				return fmt.Errorf("cera: the page has no annotation %d to reply to", r.To)
			}
		default:
			to, err := ae.existing(page, r.To)
			if err != nil {
				return err
			}
			if to.ref == (pdf.Ref{}) {
				return fmt.Errorf("cera: annotation %d is no object of its own and cannot be replied to", r.To)
			}
		}
	}
	if t := a.Link; t != nil && t.URI == "" && !t.External && t.Named == "" && (t.Page < 0 || t.Page >= e.base.NumPages()) {
		return fmt.Errorf("cera: a link to page %d of a document of %d pages", t.Page, e.base.NumPages())
	}
	return nil
}

// AddAnnotation adds a to page i of the result, a page of the edited
// document, on top of its annotations, and returns the Index it has there:
// the number of annotations the page has, counted with those added before.
// With it a later annotation replies to this one ([Markup.Reply]), and
// [Editor.SetAnnotation] and [Editor.RemoveAnnotation] take it back. Save
// and Update write the annotation with an appearance stream and, for a
// note or a comment, a popup; the page keeps it when it moves.
//
// Annotations the editor adds are not flattened by the same editor, and a
// flatten draws the document's annotations as they were, not as
// SetAnnotation changes them: write the changes first.
func (e *Editor) AddAnnotation(i int, a NewAnnotation) (int, error) {
	ae, page, err := e.annotPage(i)
	if err != nil {
		return 0, err
	}
	if a, err = a.checked(true); err != nil {
		return 0, err
	}
	index := ae.n + len(ae.added)
	if err := e.target(ae, page, &a, index); err != nil {
		return 0, err
	}
	ae.added = append(ae.added, &a)
	return index, nil
}

// SetAnnotation makes the annotation with the given Index on page i of the
// result what a describes: its text, colours, geometry, state. The type
// stays what it is. What a does not describe — entries cera does not read
// — stays in the annotation; its appearance is made anew. Take a from
// [Page.EditAnnotation] to change one thing and keep the rest.
func (e *Editor) SetAnnotation(i, index int, a NewAnnotation) error {
	ae, page, err := e.annotPage(i)
	if err != nil {
		return err
	}
	if a, err = a.checked(index >= ae.n); err != nil {
		return err
	}
	if err := e.target(ae, page, &a, index); err != nil {
		return err
	}
	if index >= ae.n {
		if index-ae.n >= len(ae.added) || ae.gone[index] {
			return fmt.Errorf("cera: the page has no annotation %d", index)
		}
		ae.added[index-ae.n] = &a
		return nil
	}
	old, err := ae.existing(page, index)
	if err != nil {
		return err
	}
	switch {
	case old.Subtype != a.Subtype:
		return fmt.Errorf("cera: annotation %d is a %s, not a %s", index, old.Subtype, a.Subtype)
	case old.ref == (pdf.Ref{}):
		return fmt.Errorf("cera: annotation %d is no object of its own and cannot be changed", index)
	}
	ae.set[index] = &a
	return nil
}

// RemoveAnnotation removes the annotation with the given Index from page i
// of the result, with its popup and with the annotations that reply to it.
// A widget of the form is not removed this way.
func (e *Editor) RemoveAnnotation(i, index int) error {
	ae, page, err := e.annotPage(i)
	if err != nil {
		return err
	}
	if index >= ae.n {
		if index-ae.n >= len(ae.added) || ae.gone[index] {
			return fmt.Errorf("cera: the page has no annotation %d", index)
		}
	} else {
		old, err := ae.existing(page, index)
		if err != nil {
			return err
		}
		if old.Subtype == "Widget" && e.base.Form().widget(page.index, index) != nil {
			return fmt.Errorf("cera: annotation %d is a widget of the form", index)
		}
	}
	ae.gone[index] = true
	delete(ae.set, index)
	return nil
}

// Highlight is a text markup annotation to add to a page: a highlight, or
// a line under or through text.
type Highlight struct {
	// Kind is "Highlight" (the default), "Underline", "StrikeOut" or
	// "Squiggly".
	Kind string
	// Quads are the areas marked, in points of the displayed page, as
	// PageText.Quads returns them for a range: corners in QuadPoints
	// order, the first two along the top edge of the text. Quads that
	// enclose no area are left out.
	Quads []Quad
	// Color is the colour, gray, RGB or CMYK components from 0 to 1;
	// yellow for a highlight and black for the others when empty.
	Color []float64
	// Opacity is the constant opacity from 0 to 1 (/CA); 0 means opaque.
	// A highlight multiplies with what is below it either way.
	Opacity float64
	// Note is the text of the annotation (/Contents); a note adds a popup
	// that viewers show it in.
	Note string
	// Author (/T), Name (/NM, unique on the page) and Date (/M and
	// /CreationDate, left out when zero) describe the annotation.
	Author string
	Name   string
	Date   time.Time
}

// AddHighlight adds h to page i of the result, a page of the edited
// document, on top of its annotations: [Editor.AddAnnotation] for text
// markup.
func (e *Editor) AddHighlight(i int, h Highlight) error {
	switch h.Kind {
	case "":
		h.Kind = "Highlight"
	case "Highlight", "Underline", "StrikeOut", "Squiggly":
	default:
		return fmt.Errorf("cera: unknown text markup %q", h.Kind)
	}
	_, err := e.AddAnnotation(i, NewAnnotation{
		Subtype: h.Kind, Contents: h.Note, Name: h.Name,
		Markup: Markup{Quads: h.Quads, Color: h.Color, Opacity: h.Opacity, Author: h.Author, Created: h.Date, Modified: h.Date},
	})
	return err
}

// annotated reports whether annotations were added, changed or removed.
func (e *Editor) annotated() bool {
	for _, p := range e.pages {
		if ae := p.annots; ae != nil && (len(ae.added) > 0 || len(ae.set) > 0 || len(ae.gone) > 0) {
			return true
		}
	}
	return false
}

// annotPatches adds the changes to annotations to patches and objs: new
// annotations with their appearances and popups, the changed entries of
// those that change, and /Annots of their pages after the changes patches
// already make to it.
func (e *Editor) annotPatches(patches []pdfedit.Patch, objs *[]pdf.Object) []pdfedit.Patch {
	d := e.base
	for _, ep := range e.pages {
		ae := ep.annots
		if !ep.own || ae == nil || len(ae.added)+len(ae.set)+len(ae.gone) == 0 {
			continue
		}
		pageRef, ok := d.r.PageRef(ep.index + 1)
		if !ok {
			continue
		}
		page, err := d.Page(ep.index)
		if err != nil {
			continue
		}
		inv, ok := page.DisplayMatrix().Invert()
		if !ok {
			continue
		}
		w := annotWriter{e: e, page: page, pageRef: pageRef, inv: inv, objs: objs}
		base := page.Annotations()
		byIndex := map[int]*Annotation{}
		for k := range base {
			byIndex[base[k].Index] = &base[k]
		}

		// What goes: the removed annotations, their popups, and what
		// replies to something that goes.
		gone := map[int]bool{}
		for k := range ae.gone {
			gone[k] = true
		}
		replyTo := func(k int) (int, bool) {
			switch {
			case k >= ae.n:
				if r := ae.added[k-ae.n].Reply; r != nil {
					return r.To, true
				}
			case ae.set[k] != nil:
				if r := ae.set[k].Reply; r != nil {
					return r.To, true
				}
			case byIndex[k] != nil && byIndex[k].Markup != nil && byIndex[k].Markup.Reply != nil:
				return byIndex[k].Markup.Reply.To, true
			}
			return 0, false
		}
		for more := true; more; {
			more = false
			for k := 0; k < ae.n+len(ae.added); k++ {
				if to, ok := replyTo(k); ok && gone[to] && !gone[k] {
					gone[k], more = true, true
				}
			}
		}
		goneRefs := map[pdf.Ref]bool{}
		for k := range gone {
			if a := byIndex[k]; a != nil {
				if a.ref != (pdf.Ref{}) {
					goneRefs[a.ref] = true
				}
				if a.Markup != nil && a.Markup.Popup != nil {
					gone[a.Markup.Popup.Index] = true
				}
			}
		}
		for k := range gone {
			if a := byIndex[k]; a != nil && a.ref != (pdf.Ref{}) {
				goneRefs[a.ref] = true
			}
		}

		// The new annotations get their objects first, so that one can
		// reply to another.
		slot := map[int]int{}
		for k := range ae.added {
			if !gone[ae.n+k] {
				slot[ae.n+k] = len(*objs)
				*objs = append(*objs, pdf.Null)
			}
		}
		w.ref = func(index int) (pdf.Object, bool) {
			if s, ok := slot[index]; ok {
				return pdfedit.NewRef(s).Object(), true
			}
			if a := byIndex[index]; a != nil && a.ref != (pdf.Ref{}) && !gone[index] {
				return a.ref.Object(), true
			}
			return pdf.Null, false
		}

		arr := e.annots(patches, pageRef, page)
		aligned := len(arr) == ae.n // no flatten moved the entries
		var annots pdf.Array
		for pos, o := range arr {
			if ref, ok := o.Ref(); ok && goneRefs[ref] || !ok && aligned && gone[pos] {
				continue
			}
			annots = append(annots, o)
		}
		var popups pdf.Array
		for _, index := range slices.Sorted(maps.Keys(ae.set)) {
			old := byIndex[index]
			if old == nil || gone[index] {
				continue
			}
			set, del, popup := w.entries(ae.set[index], old)
			patches = append(patches, pdfedit.Patch{Ref: old.ref, Set: set, Del: del})
			if popup.patch != nil {
				patches = append(patches, *popup.patch)
			}
			if !popup.obj.IsNull() {
				popups = append(popups, popup.obj)
			}
		}
		for k, a := range ae.added {
			s, ok := slot[ae.n+k]
			if !ok {
				continue
			}
			set, _, popup := w.entries(a, nil)
			self := pdfedit.NewRef(s).Object()
			if !popup.obj.IsNull() {
				set = append(set, pdf.Entry{Key: "Popup", Val: popup.obj})
				ref, _ := popup.obj.Ref()
				pd, _ := (*objs)[ref.Num].Dict()
				(*objs)[ref.Num] = pd.With("Parent", self).Object()
				popups = append(popups, popup.obj)
			}
			(*objs)[s] = pdf.NewDict(set...).Object()
			annots = append(annots, self)
		}
		annots = append(annots, popups...)
		patches = append(patches, pdfedit.Patch{Ref: pageRef, Set: []pdf.Entry{{Key: "Annots", Val: annots.Object()}}})
	}
	return patches
}

// annots returns /Annots of the page ref as patches leave it.
func (e *Editor) annots(patches []pdfedit.Patch, ref pdf.Ref, page *Page) pdf.Array {
	for k := len(patches) - 1; k >= 0; k-- {
		if patches[k].Ref != ref {
			continue
		}
		for _, s := range patches[k].Set {
			if s.Key == "Annots" {
				a, _ := s.Val.Array()
				return append(pdf.Array(nil), a...)
			}
		}
	}
	a, _ := e.base.resolve(page.dict.Get("Annots")).Array()
	return append(pdf.Array(nil), a...)
}

// annotWriter writes the annotations of one page.
type annotWriter struct {
	e       *Editor
	page    *Page
	pageRef pdf.Ref
	inv     Matrix // from the displayed page to its user space
	objs    *[]pdf.Object
	// ref returns the reference to the annotation with an Index.
	ref func(index int) (pdf.Object, bool)
}

// popupChange is what writing an annotation does about its popup: a
// change of the one it has, or a new one, obj, for /Annots.
type popupChange struct {
	patch *pdfedit.Patch
	obj   pdf.Object
}

// managed are the entries the editor writes: those an annotation has and
// a NewAnnotation does not ask for are removed. Entries that draw what a
// NewAnnotation does not describe — leader lines, a border effect, a
// default style — go too, so that the annotation and its new appearance
// agree.
var managed = []pdf.Name{
	"Contents", "NM", "T", "Subj", "M", "CreationDate", "RC", "IT", "C", "IC", "CA",
	"BS", "Border", "QuadPoints", "InkList", "Vertices", "L", "LE", "CL", "DA", "Q", "Name",
	"IRT", "RT", "State", "StateModel", "AP", "AS", "RD", "BE", "DS", "LL", "LLE", "LLO",
	"Cap", "Rotate", "Dest", "A", "Path",
}

// user returns the points pts of the displayed page in user space, as
// numbers.
func (w *annotWriter) user(pts ...[2]float64) (pdf.Array, Rect) {
	out := make(pdf.Array, 0, 2*len(pts))
	b := Rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, p := range pts {
		x, y := w.inv.Apply(p[0], p[1])
		out = append(out, pdf.Real(x), pdf.Real(y))
		b = Rect{min(b.X0, x), min(b.Y0, y), max(b.X1, x), max(b.Y1, y)}
	}
	return out, b
}

// rect returns the rectangle r of the displayed page in user space.
func (w *annotWriter) rect(r Rect) Rect {
	_, b := w.user([2]float64{r.X0, r.Y0}, [2]float64{r.X1, r.Y0}, [2]float64{r.X0, r.Y1}, [2]float64{r.X1, r.Y1})
	return b
}

func grow(r Rect, by float64) Rect { return Rect{r.X0 - by, r.Y0 - by, r.X1 + by, r.Y1 + by} }

func union(a, b Rect) Rect {
	return Rect{min(a.X0, b.X0), min(a.Y0, b.Y0), max(a.X1, b.X1), max(a.Y1, b.Y1)}
}

// entries returns the entries of the annotation a describes, with its
// appearance, and what to do about its popup. old is the annotation of the
// document that a changes, nil for a new one: then the entries are all it
// has, else those to set and to remove.
func (w *annotWriter) entries(a *NewAnnotation, old *Annotation) (set []pdf.Entry, del []pdf.Name, popup popupChange) {
	name := func(s string) pdf.Object { return pdf.Name(s).Object() }
	text := func(key pdf.Name, s string) {
		if s != "" {
			set = append(set, pdf.Entry{Key: key, Val: pdf.String(encodeText(s))})
		}
	}
	nums := func(v []float64) pdf.Object {
		arr := make(pdf.Array, len(v))
		for i, x := range v {
			arr[i] = pdf.Real(x)
		}
		return arr.Object()
	}

	flags := a.Flags
	if flags == 0 {
		flags = AnnotPrint
		if a.Subtype == "Text" {
			flags |= AnnotNoZoom | AnnotNoRotate
		}
	}
	text("Contents", a.Contents)
	text("NM", a.Name)
	text("T", a.Author)
	text("Subj", a.Subject)
	text("RC", a.RichText)
	text("State", a.State)
	text("StateModel", a.StateModel)
	if !a.Modified.IsZero() {
		text("M", pdfDate(a.Modified))
	}
	if !a.Created.IsZero() {
		text("CreationDate", pdfDate(a.Created))
	}
	if a.Intent != "" {
		set = append(set, pdf.Entry{Key: "IT", Val: name(a.Intent)})
	}
	if len(a.Color) > 0 {
		set = append(set, pdf.Entry{Key: "C", Val: nums(a.Color)})
	}
	if a.Opacity < 1 {
		set = append(set, pdf.Entry{Key: "CA", Val: pdf.Real(a.Opacity)})
	}
	if r := a.Reply; r != nil {
		if to, ok := w.ref(r.To); ok {
			rt := "R"
			if r.Group {
				rt = "Group"
			}
			set = append(set, pdf.Entry{Key: "IRT", Val: to}, pdf.Entry{Key: "RT", Val: name(rt)})
		}
	}
	bw := 0.0
	if bordered[a.Subtype] {
		bw = a.BorderWidth
		bs := []pdf.Entry{{Key: "W", Val: pdf.Real(bw)}}
		switch a.BorderStyle {
		case BorderDashed:
			dash := a.Dash
			if len(dash) == 0 {
				dash = []float64{3}
			}
			bs = append(bs, pdf.Entry{Key: "S", Val: name("D")}, pdf.Entry{Key: "D", Val: nums(dash)})
		case BorderBeveled:
			bs = append(bs, pdf.Entry{Key: "S", Val: name("B")})
		case BorderInset:
			bs = append(bs, pdf.Entry{Key: "S", Val: name("I")})
		case BorderUnderline:
			bs = append(bs, pdf.Entry{Key: "S", Val: name("U")})
		default:
			bs = append(bs, pdf.Entry{Key: "S", Val: name("S")})
		}
		set = append(set, pdf.Entry{Key: "BS", Val: pdf.NewDict(bs...).Object()})
	}
	// pad is how far lines reach beyond their points: joins, and endings.
	pad := endingSize(bw) + bw + 1
	interior := func() {
		if len(a.Interior) > 0 {
			set = append(set, pdf.Entry{Key: "IC", Val: nums(a.Interior)})
		}
	}
	turn := func() {
		if w.page.Rotate != 0 {
			set = append(set, pdf.Entry{Key: "Rotate", Val: pdf.Integer(int64(w.page.Rotate))})
		}
	}

	var rect Rect
	switch a.Subtype {
	case "Text":
		rect = w.rect(a.Bounds)
		if a.Icon != "" {
			set = append(set, pdf.Entry{Key: "Name", Val: name(a.Icon)})
		}
	case "Caret":
		rect = w.rect(a.Bounds)
		turn()
	case "Stamp":
		rect = w.rect(a.Bounds)
		set = append(set, pdf.Entry{Key: "Name", Val: name(a.Icon)})
		turn()
	case "Square", "Circle":
		rect = w.rect(a.Bounds)
		interior()
	case "FreeText":
		rect = w.rect(a.Bounds)
		da := a.DefaultAppearance
		if da == "" {
			da = "/Helv 12 Tf 0 g"
		}
		set = append(set, pdf.Entry{Key: "DA", Val: pdf.String([]byte(da))}, pdf.Entry{Key: "Q", Val: pdf.Integer(int64(a.Align))})
		if len(a.Callout) > 0 {
			cl, b := w.user(a.Callout...)
			box := rect
			rect = union(rect, grow(b, pad))
			set = append(set, pdf.Entry{Key: "CL", Val: cl.Object()},
				pdf.Entry{Key: "RD", Val: nums([]float64{box.X0 - rect.X0, box.Y0 - rect.Y0, rect.X1 - box.X1, rect.Y1 - box.Y1})})
			if le := a.LineEndings[0]; le != "" {
				set = append(set, pdf.Entry{Key: "LE", Val: name(le)})
			}
		}
		turn()
	case "Highlight", "Underline", "Squiggly", "StrikeOut":
		var pts [][2]float64
		for _, q := range a.Quads {
			pts = append(pts, q.P[:]...)
		}
		qp, b := w.user(pts...)
		rect = b
		if a.Subtype != "Highlight" {
			// The ends of a line, and of a squiggle more so, reach a
			// little beyond the text.
			rect = grow(b, 1+min(b.Dx(), b.Dy())/10)
		}
		set = append(set, pdf.Entry{Key: "QuadPoints", Val: qp.Object()})
	case "Ink":
		var list pdf.Array
		rect = Rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
		for _, s := range a.Ink {
			pts, b := w.user(s...)
			list = append(list, pts.Object())
			rect = union(rect, b)
		}
		rect = grow(rect, pad)
		set = append(set, pdf.Entry{Key: "InkList", Val: list.Object()})
	case "Polygon", "PolyLine":
		pts, b := w.user(a.Vertices...)
		rect = grow(b, pad)
		set = append(set, pdf.Entry{Key: "Vertices", Val: pts.Object()})
		interior()
		if a.Subtype == "PolyLine" {
			set = append(set, pdf.Entry{Key: "LE", Val: w.endings(a)})
		}
	case "Line":
		pts, b := w.user(a.Line...)
		rect = grow(b, pad)
		set = append(set, pdf.Entry{Key: "L", Val: pts.Object()}, pdf.Entry{Key: "LE", Val: w.endings(a)})
		interior()
	case "Link":
		rect = w.rect(a.Bounds)
		if len(a.Quads) > 0 {
			var pts [][2]float64
			for _, q := range a.Quads {
				pts = append(pts, q.P[:]...)
			}
			qp, _ := w.user(pts...)
			set = append(set, pdf.Entry{Key: "QuadPoints", Val: qp.Object()})
		}
		set = append(set, w.link(a.Link))
	}
	set = append(set,
		pdf.Entry{Key: "Type", Val: name("Annot")},
		pdf.Entry{Key: "Subtype", Val: name(a.Subtype)},
		pdf.Entry{Key: "Rect", Val: rectArray(rect)},
		pdf.Entry{Key: "F", Val: pdf.Integer(int64(flags))},
		pdf.Entry{Key: "P", Val: w.pageRef.Object()},
	)

	// The appearance, from the annotation as it will be: what it keeps of
	// the old one, and the entries set.
	var all []pdf.Entry
	written := map[pdf.Name]bool{}
	for _, s := range set {
		written[s.Key] = true
	}
	if old != nil {
		for k, v := range old.dict.All() {
			if !written[k] && !slices.Contains(managed, k) {
				all = append(all, pdf.Entry{Key: k, Val: v})
			}
		}
		for _, k := range managed {
			if !written[k] && old.dict.Has(k) {
				del = append(del, k)
			}
		}
	}
	tmp := Annotation{Subtype: a.Subtype, Rect: rect, Flags: flags, Contents: a.Contents, dict: pdf.NewDict(append(all, set...)...)}
	if ap, ok := w.appearance(&tmp, a.Opacity); ok {
		set = append(set, pdf.Entry{Key: "AP", Val: pdf.NewDict(pdf.Entry{Key: "N", Val: ap}).Object()})
		del = slices.DeleteFunc(del, func(k pdf.Name) bool { return k == "AP" })
	} else if a.Subtype == "Stamp" {
		// A stamp of the document that cera has no picture for keeps the
		// one it has.
		del = slices.DeleteFunc(del, func(k pdf.Name) bool { return k == "AP" || k == "AS" })
	}

	// The popup: a note has one, and so has every annotation with a
	// comment but a free text, which shows its text itself.
	if a.Subtype == "Link" || a.Subtype == "FreeText" || a.Subtype != "Text" && a.Contents == "" && a.Popup == nil {
		return set, del, popup
	}
	pr := Rect{rect.X1, rect.Y1 - 100, rect.X1 + 180, rect.Y1} // beside the top right
	open := false
	if p := a.Popup; p != nil {
		open = p.Open
		if p.Bounds.Dx() > 0 && p.Bounds.Dy() > 0 {
			pr = w.rect(p.Bounds)
		}
	}
	if old != nil && old.Markup != nil && old.Markup.Popup != nil {
		if a.Popup != nil {
			if ref, ok := w.ref(old.Markup.Popup.Index); ok {
				r, _ := ref.Ref()
				popup.patch = &pdfedit.Patch{Ref: r, Set: []pdf.Entry{{Key: "Rect", Val: rectArray(pr)}, {Key: "Open", Val: pdf.Boolean(open)}}}
			}
		}
		return set, del, popup
	}
	pd := []pdf.Entry{
		{Key: "Type", Val: name("Annot")},
		{Key: "Subtype", Val: name("Popup")},
		{Key: "Rect", Val: rectArray(pr)},
		{Key: "P", Val: w.pageRef.Object()},
		{Key: "Open", Val: pdf.Boolean(open)},
	}
	if old != nil {
		pd = append(pd, pdf.Entry{Key: "Parent", Val: old.ref.Object()})
	}
	*w.objs = append(*w.objs, pdf.NewDict(pd...).Object())
	popup.obj = pdfedit.NewRef(len(*w.objs) - 1).Object()
	if old != nil {
		set = append(set, pdf.Entry{Key: "Popup", Val: popup.obj})
	}
	return set, del, popup
}

// endings returns /LE of a line.
func (w *annotWriter) endings(a *NewAnnotation) pdf.Object {
	le := pdf.Array{pdf.Name("None").Object(), pdf.Name("None").Object()}
	for i, e := range a.LineEndings {
		if e != "" {
			le[i] = pdf.Name(e).Object()
		}
	}
	return le.Object()
}

// link returns the entry that says where a link leads: /Dest for a place
// in the document, /A for an action.
func (w *annotWriter) link(t *LinkTarget) pdf.Entry {
	d := w.e.base
	action := func(e ...pdf.Entry) pdf.Entry {
		return pdf.Entry{Key: "A", Val: pdf.NewDict(append([]pdf.Entry{{Key: "Type", Val: pdf.Name("Action").Object()}}, e...)...).Object()}
	}
	// view returns the view of a destination after its page.
	view := func(page pdf.Object) pdf.Object {
		fit := t.Fit
		if fit == "" {
			fit = "Fit"
		}
		dest := pdf.Array{page, pdf.Name(fit).Object()}
		for _, v := range t.Params {
			if math.IsNaN(v) {
				dest = append(dest, pdf.Null)
			} else {
				dest = append(dest, pdf.Real(v))
			}
		}
		return dest.Object()
	}
	switch {
	case t.URI != "":
		return action(pdf.Entry{Key: "S", Val: pdf.Name("URI").Object()}, pdf.Entry{Key: "URI", Val: pdf.String([]byte(t.URI))})
	case t.External:
		file := pdf.Entry{Key: "F", Val: pdf.String(encodeText(t.File))}
		switch {
		case t.Named != "":
			return action(pdf.Entry{Key: "S", Val: pdf.Name("GoToR").Object()}, file, pdf.Entry{Key: "D", Val: pdf.String(encodeText(t.Named))})
		case t.Page >= 0:
			return action(pdf.Entry{Key: "S", Val: pdf.Name("GoToR").Object()}, file, pdf.Entry{Key: "D", Val: view(pdf.Integer(int64(t.Page)))})
		}
		return action(pdf.Entry{Key: "S", Val: pdf.Name("Launch").Object()}, file)
	case t.Named != "":
		return pdf.Entry{Key: "Dest", Val: pdf.String(encodeText(t.Named))}
	}
	page := pdf.Null
	if ref, ok := d.r.PageRef(t.Page + 1); ok {
		page = ref.Object()
	}
	return pdf.Entry{Key: "Dest", Val: view(page)}
}

// appearance makes the appearance stream of the annotation a as the
// renderer draws one without: the generated content in user space, in a
// form whose box is the rectangle, so that it is drawn where it is. An
// annotation that is not opaque, and a highlight, which multiplies with
// the page, is a group drawn with that opacity and blend mode. It reports
// false for an annotation that shows nothing, a link without a border.
func (w *annotWriter) appearance(a *Annotation, opacity float64) (pdf.Object, bool) {
	e := w.e
	d := e.base
	if e.noAppearance || !d.generates(a) {
		return pdf.Null, false
	}
	content, res := d.generateAnnot(nil, a, e.gen)
	if res.IsZero() {
		res = pdf.NewDict()
	}
	form := func(res pdf.Dict, content []byte, extra ...pdf.Entry) pdf.Object {
		dict := pdf.NewDict(append([]pdf.Entry{
			{Key: "Type", Val: pdf.Name("XObject").Object()},
			{Key: "Subtype", Val: pdf.Name("Form").Object()},
			{Key: "BBox", Val: rectArray(a.Rect)},
			{Key: "Resources", Val: res.Object()},
		}, extra...)...)
		*w.objs = append(*w.objs, pdfwrite.Flate(dict, content).Object())
		return pdfedit.NewRef(len(*w.objs) - 1).Object()
	}
	multiply := a.Subtype == "Highlight"
	if unit8(opacity) == 255 && !multiply {
		return form(res, content), true
	}
	group := form(res, content, pdf.Entry{Key: "Group", Val: pdf.NewDict(
		pdf.Entry{Key: "S", Val: pdf.Name("Transparency").Object()},
		pdf.Entry{Key: "I", Val: pdf.Boolean(true)},
	).Object()})
	gs := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("ExtGState").Object()},
		{Key: "CA", Val: pdf.Real(opacity)},
		{Key: "ca", Val: pdf.Real(opacity)},
	}
	if multiply {
		gs = append(gs, pdf.Entry{Key: "BM", Val: pdf.Name("Multiply").Object()})
	}
	return form(pdf.NewDict(
		pdf.Entry{Key: "ExtGState", Val: pdf.NewDict(pdf.Entry{Key: "GS0", Val: pdf.NewDict(gs...).Object()}).Object()},
		pdf.Entry{Key: "XObject", Val: pdf.NewDict(pdf.Entry{Key: "Fm0", Val: group}).Object()},
	), []byte("/GS0 gs /Fm0 Do\n")), true
}

// rectArray returns r as a PDF rectangle.
func rectArray(r Rect) pdf.Object {
	return pdf.Array{pdf.Real(r.X0), pdf.Real(r.Y0), pdf.Real(r.X1), pdf.Real(r.Y1)}.Object()
}

// pdfDate formats t as a PDF date (PDF 2.0, 7.9.4).
func pdfDate(t time.Time) string {
	s := t.Format("D:20060102150405")
	_, off := t.Zone()
	if off == 0 {
		return s + "Z"
	}
	sign := '+'
	if off < 0 {
		sign, off = '-', -off
	}
	return fmt.Sprintf("%s%c%02d'%02d'", s, sign, off/3600, off/60%60)
}
