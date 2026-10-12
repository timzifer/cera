package cera

import (
	"fmt"
	"math"
	"time"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
)

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
// document, on top of its annotations; Save and Update write it with an
// appearance stream. The page keeps the highlight when it moves.
func (e *Editor) AddHighlight(i int, h Highlight) error {
	if err := e.check(i); err != nil {
		return err
	}
	if !e.pages[i].own {
		return fmt.Errorf("cera: page %d is not a page of the edited document", i)
	}
	switch h.Kind {
	case "":
		h.Kind = "Highlight"
	case "Highlight", "Underline", "StrikeOut", "Squiggly":
	default:
		return fmt.Errorf("cera: unknown text markup %q", h.Kind)
	}
	var quads []Quad
	for _, q := range h.Quads {
		if q.area() > 1e-6 {
			quads = append(quads, q)
		}
	}
	if len(quads) == 0 {
		return fmt.Errorf("cera: a highlight needs quads that enclose an area")
	}
	switch len(h.Color) {
	case 0, 1, 3, 4:
	default:
		return fmt.Errorf("cera: a colour has 1, 3 or 4 components, not %d", len(h.Color))
	}
	if h.Opacity < 0 || h.Opacity > 1 || math.IsNaN(h.Opacity) {
		return fmt.Errorf("cera: opacity %g is out of range [0, 1]", h.Opacity)
	}
	h.Quads = quads
	h.Color = append([]float64(nil), h.Color...)
	e.pages[i].highlights = append(e.pages[i].highlights, h)
	return nil
}

// annotated reports whether highlights were added.
func (e *Editor) annotated() bool {
	for _, p := range e.pages {
		if len(p.highlights) > 0 {
			return true
		}
	}
	return false
}

// highlightPatches adds the annotations of the highlights to patches and
// objs: the annotations and their appearances, and a change of /Annots of
// their pages after the changes patches already make to it.
func (e *Editor) highlightPatches(patches []pdfedit.Patch, objs *[]pdf.Object) []pdfedit.Patch {
	d := e.base
	for _, ep := range e.pages {
		if !ep.own || len(ep.highlights) == 0 {
			continue
		}
		ref, ok := d.r.PageRef(ep.index + 1)
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
		annots := e.annots(patches, ref, page)
		for _, h := range ep.highlights {
			annots = append(annots, addHighlight(objs, h, ref, inv)...)
		}
		patches = append(patches, pdfedit.Patch{Ref: ref, Set: []pdf.Entry{{Key: "Annots", Val: annots.Object()}}})
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

// addHighlight adds the annotation of h on the page ref to objs, with its
// appearance and popup, and returns the references to the annotation and
// the popup, which go into /Annots. inv maps the displayed page to its
// user space.
func addHighlight(objs *[]pdf.Object, h Highlight, page pdf.Ref, inv Matrix) []pdf.Object {
	var qp []float64
	r := Rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, q := range h.Quads {
		q = q.Transform(inv)
		for _, p := range q.P {
			qp = append(qp, p[0], p[1])
			r = Rect{min(r.X0, p[0]), min(r.Y0, p[1]), max(r.X1, p[0]), max(r.Y1, p[1])}
		}
	}
	color := h.Color
	if len(color) == 0 {
		color = []float64{0}
		if h.Kind == "Highlight" {
			color = []float64{1, 1, 0}
		}
	}
	opacity := h.Opacity
	if opacity == 0 {
		opacity = 1
	}

	// The appearance: the shapes the renderer draws for an annotation
	// without one, in user space, so that its matrix is the identity.
	var w csw
	w.op("/GS0 gs")
	if h.Kind == "Highlight" {
		w.color(color, true)
	} else {
		w.color(color, false)
	}
	for i := 0; i+8 <= len(qp); i += 8 {
		markup(&w, h.Kind, qp[i:i+8])
	}
	gs := []pdf.Entry{{Key: "Type", Val: pdf.Name("ExtGState").Object()}, {Key: "CA", Val: pdf.Real(opacity)}, {Key: "ca", Val: pdf.Real(opacity)}}
	if h.Kind == "Highlight" {
		gs = append(gs, pdf.Entry{Key: "BM", Val: pdf.Name("Multiply").Object()})
	}
	bbox := rectArray(r)
	*objs = append(*objs, pdf.NewStream(pdf.NewDict(
		pdf.Entry{Key: "Type", Val: pdf.Name("XObject").Object()},
		pdf.Entry{Key: "Subtype", Val: pdf.Name("Form").Object()},
		pdf.Entry{Key: "BBox", Val: bbox},
		pdf.Entry{Key: "Resources", Val: pdf.NewDict(pdf.Entry{Key: "ExtGState", Val: pdf.NewDict(
			pdf.Entry{Key: "GS0", Val: pdf.NewDict(gs...).Object()},
		).Object()}).Object()},
	), w).Object())
	ap := pdfedit.NewRef(len(*objs) - 1).Object()

	nums := func(v []float64) pdf.Object {
		a := make(pdf.Array, len(v))
		for i, x := range v {
			a[i] = pdf.Real(x)
		}
		return a.Object()
	}
	entries := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("Annot").Object()},
		{Key: "Subtype", Val: pdf.Name(h.Kind).Object()},
		{Key: "Rect", Val: bbox},
		{Key: "QuadPoints", Val: nums(qp)},
		{Key: "C", Val: nums(color)},
		{Key: "F", Val: pdf.Integer(int64(AnnotPrint))},
		{Key: "P", Val: page.Object()},
		{Key: "AP", Val: pdf.NewDict(pdf.Entry{Key: "N", Val: ap}).Object()},
	}
	if h.Opacity != 0 {
		entries = append(entries, pdf.Entry{Key: "CA", Val: pdf.Real(opacity)})
	}
	text := func(key pdf.Name, s string) {
		if s != "" {
			entries = append(entries, pdf.Entry{Key: key, Val: pdf.String(encodeText(s))})
		}
	}
	text("Contents", h.Note)
	text("T", h.Author)
	text("NM", h.Name)
	if !h.Date.IsZero() {
		text("M", pdfDate(h.Date))
		text("CreationDate", pdfDate(h.Date))
	}
	at := len(*objs)
	if h.Note != "" {
		entries = append(entries, pdf.Entry{Key: "Popup", Val: pdfedit.NewRef(at + 1).Object()})
	}
	*objs = append(*objs, pdf.NewDict(entries...).Object())
	if h.Note != "" {
		// Closed, beside the top right of the highlight.
		pop := Rect{r.X1, r.Y1 - 100, r.X1 + 180, r.Y1}
		*objs = append(*objs, pdf.NewDict(
			pdf.Entry{Key: "Type", Val: pdf.Name("Annot").Object()},
			pdf.Entry{Key: "Subtype", Val: pdf.Name("Popup").Object()},
			pdf.Entry{Key: "Rect", Val: rectArray(pop)},
			pdf.Entry{Key: "Parent", Val: pdfedit.NewRef(at).Object()},
			pdf.Entry{Key: "P", Val: page.Object()},
			pdf.Entry{Key: "Open", Val: pdf.Boolean(false)},
		).Object())
	}
	if h.Note != "" {
		return []pdf.Object{pdfedit.NewRef(at).Object(), pdfedit.NewRef(at + 1).Object()}
	}
	return []pdf.Object{pdfedit.NewRef(at).Object()}
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
