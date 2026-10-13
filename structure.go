package cera

import (
	"iter"

	"github.com/timzifer/cera/internal/pdf"
)

// The logical structure of a tagged document (PDF 2.0, 14.7 and 14.8): a
// tree of elements — sections, headings, paragraphs, lists, tables with
// their cells, figures — over the marked content of the pages. It is what
// a screen reader reads a document by, and what an export to HTML keeps
// headings and tables with. Its elements name their content by the
// identifiers the content streams mark it with, which the text of a page
// carries too ([TextChar.MCID]).

// StructTree is the structure tree of a document.
type StructTree struct {
	// Elems are the elements at the top of the tree, mostly one, a
	// Document.
	Elems []*StructElem
	// Suspects says that the producer is not sure of the tree
	// (/MarkInfo /Suspects), which it made by guessing at a layout. cera
	// then orders the text of a page by its layout, not by the tree.
	Suspects bool
	// Truncated says that the tree has more elements or levels than are
	// read (docs/budgets.md), so that part of it is missing.
	Truncated bool

	ids map[string]*StructElem
}

// ByID returns the element with the identifier id, as the Headers of a
// table cell name its header cells, and nil for none. Of several elements
// with one identifier it is the first in the order of the tree.
func (t *StructTree) ByID(id string) *StructElem { return t.ids[id] }

// StructElem is an element of the structure tree.
type StructElem struct {
	// Type is the structure type as the document names it (/S), and Role
	// the type that stands for: what the role map of the document
	// (/RoleMap, and /RoleMapNS of the element's namespace) maps Type to,
	// and Type itself where it maps it to nothing. A document may call its
	// headings "Heading 1"; their Role is "H1". The standard types are
	// those of PDF 2.0, 14.8.4: "Document", "Sect", "H1", "P", "L", "LI",
	// "Table", "TR", "TH", "TD", "Figure", "Link" and the others.
	Type, Role string
	// ID identifies the element in the document (/ID); see
	// [StructTree.ByID].
	ID string
	// Title names the element (/T). Alt describes it for a reader who
	// does not see it, as for a figure. ActualText is the text the element
	// stands for and is read as, instead of the text of its content.
	// Expansion is what an abbreviation is short for (/E).
	Title, Alt, ActualText, Expansion string
	// Lang is the language of the element ("en-US"): its own /Lang, or
	// that of the nearest element above that has one, or the document's.
	Lang string
	// Page is the page the content of the element is on, its own and that
	// of the elements below it, and -1 for content on several pages. An
	// element without content is on the page it or one above it names
	// (/Pg), or -1.
	Page int
	// Kids are what the element consists of, in reading order.
	Kids []StructKid

	attr *StructAttr
}

// StructKid is a part of a structure element: an element below it, a
// sequence of marked content, or an annotation.
type StructKid struct {
	// Elem is the element, nil for content and annotations.
	Elem *StructElem
	// Page is the page of the content or the annotation, and for an
	// element its Page.
	Page int
	// MCID is the identifier of a marked-content sequence of Page, as in
	// [TextChar.MCID] and [MarkedContent.MCID]; -1 for none. Content
	// marked in a form XObject, which numbers it on its own, is left out.
	MCID int
	// Annot is the [Annotation.Index] of an annotation of Page that is
	// part of the element, as the annotation of a Link is; -1 for none.
	Annot int
}

// StructAttr are the attributes of a structure element that cera reads:
// those of tables and lists, and the box of a figure.
type StructAttr struct {
	// RowSpan and ColSpan are the rows and columns a table cell spans, 1
	// by default.
	RowSpan, ColSpan int
	// Headers are the identifiers of the header cells of a cell; see
	// [StructTree.ByID].
	Headers []string
	// Scope says what a header cell is the header of: "Row", "Column" or
	// "Both", and "" where it does not say.
	Scope string
	// Summary describes a table.
	Summary string
	// ListNumbering is how the items of a list are labelled: "None" by
	// default, "Disc", "Circle", "Square", "Decimal", "UpperRoman",
	// "LowerRoman", "UpperAlpha", "LowerAlpha" and others of PDF 2.0.
	ListNumbering string
	// BBox is the box of a figure, a formula or a table in default user
	// space of its page, as [Page.Boxes] are; the zero Rect for none.
	BBox Rect
}

// Attr returns the attributes of the element, with their defaults for
// what it does not say. Do not modify Headers.
func (e *StructElem) Attr() StructAttr {
	if e.attr != nil {
		return *e.attr
	}
	return StructAttr{RowSpan: 1, ColSpan: 1, ListNumbering: "None"}
}

// Children returns the elements below e, without its content.
func (e *StructElem) Children() iter.Seq[*StructElem] {
	return func(yield func(*StructElem) bool) {
		for _, k := range e.Kids {
			if k.Elem != nil && !yield(k.Elem) {
				return
			}
		}
	}
}

// Structure returns the structure tree of a tagged document, nil for a
// document without one. It is read once and shared; do not modify it.
//
// The text of an element is that of its marked content: the characters of
// the page's [PageText] with the identifier of a kid, in the order of the
// kids, unless the element or one above it has an ActualText.
func (d *Document) Structure() *StructTree {
	d.structTreeOnce.Do(func() {
		defer func() {
			if recover() != nil {
				d.structTree = nil // a broken tree is no tree
			}
		}()
		d.structTree = d.readStructure()
	})
	return d.structTree
}

func (d *Document) readStructure() *StructTree {
	cat, err := d.r.Catalog()
	if err != nil {
		return nil
	}
	root := d.dict(cat.Get("StructTreeRoot"))
	if root.IsZero() {
		return nil
	}
	t := &StructTree{
		Suspects: d.boolean(d.dict(cat.Get("MarkInfo")).Get("Suspects")),
		ids:      map[string]*StructElem{},
	}
	w := &structWalk{d: d, tree: t, roleMap: d.dict(root.Get("RoleMap")), classMap: d.dict(root.Get("ClassMap")),
		seen: map[pdf.Ref]bool{}, annots: map[int]map[pdf.Ref]int{}}
	var top StructElem
	w.kids(&top, root.Get("K"), -1, textString(d.resolve(cat.Get("Lang"))), 0)
	for c := range top.Children() {
		t.Elems = append(t.Elems, c)
	}
	return t
}

// structWalk reads a structure tree: seen holds the elements read, so that
// one named twice is read once, and n counts what was read against
// maxStructNodes.
type structWalk struct {
	d                 *Document
	tree              *StructTree
	roleMap, classMap pdf.Dict
	seen              map[pdf.Ref]bool
	n                 int
	annots            map[int]map[pdf.Ref]int // the place in /Annots of the annotations of a page
}

// more counts one more thing read and reports whether the budget allows
// it.
func (w *structWalk) more() bool {
	if w.n >= maxStructNodes {
		w.tree.Truncated = true
		return false
	}
	w.n++
	return true
}

// noPage is the page of an element without content, while the tree is
// read.
const noPage = -2

// kids reads /K of e, an element at depth whose content is on page pg
// unless it says otherwise (-1 for no page) and whose language is lang,
// and returns the page of what it read: noPage for nothing, -1 for
// several.
func (w *structWalk) kids(e *StructElem, k pdf.Object, pg int, lang string, depth int) int {
	d := w.d
	on := noPage
	add := func(kid StructKid, page int) {
		e.Kids = append(e.Kids, kid)
		switch {
		case page == noPage:
		case on == noPage:
			on = page
		case on != page:
			on = -1
		}
	}
	one := func(o pdf.Object) {
		ref, isRef := o.Ref()
		o = d.resolve(o)
		if v, ok := o.Int(); ok {
			if id := mcid(float64(v)); id >= 0 && pg >= 0 && w.more() {
				add(StructKid{Page: pg, MCID: id, Annot: -1}, pg)
			}
			return
		}
		kd, ok := o.Dict()
		if !ok {
			return
		}
		page := pg
		if r, ok := kd.Get("Pg").Ref(); ok {
			page = d.pageIndex(r)
		}
		switch t, _ := d.name(kd.Get("Type")); t {
		case "MCR":
			// Marked content in a form XObject (/Stm) is numbered in the
			// form's own content, not the page's.
			if v, ok := d.num(kd.Get("MCID")); ok && page >= 0 && !kd.Has("Stm") {
				if id := mcid(v); id >= 0 && w.more() {
					add(StructKid{Page: page, MCID: id, Annot: -1}, page)
				}
			}
		case "OBJR":
			if r, ok := kd.Get("Obj").Ref(); ok && page >= 0 {
				if i, ok := w.annot(page, r); ok && w.more() {
					add(StructKid{Page: page, MCID: -1, Annot: i}, page)
				}
			}
		default:
			if isRef {
				if w.seen[ref] {
					return
				}
				w.seen[ref] = true
			}
			if depth >= maxStructDepth {
				w.tree.Truncated = true
				return
			}
			if !w.more() {
				return
			}
			c, on := w.elem(kd, page, lang, depth+1)
			add(StructKid{Elem: c, Page: c.Page, MCID: -1, Annot: -1}, on)
		}
	}
	if a, ok := d.resolve(k).Array(); ok {
		for _, o := range a {
			if w.n >= maxStructNodes {
				w.tree.Truncated = true
				break
			}
			one(o)
		}
		return on
	}
	one(k)
	return on
}

// elem reads the element ed at depth and what is below it, and returns it
// with the page of its content as kids does.
func (w *structWalk) elem(ed pdf.Dict, pg int, lang string, depth int) (*StructElem, int) {
	d := w.d
	text := func(key pdf.Name) string {
		s := textString(d.resolve(ed.Get(key)))
		if len(s) > maxInfoString {
			s = s[:maxInfoString]
		}
		return s
	}
	e := &StructElem{Title: text("T"), Alt: text("Alt"), ActualText: text("ActualText"), Expansion: text("E"), Lang: lang}
	if t, ok := d.name(ed.Get("S")); ok {
		e.Type, e.Role = string(t), string(w.role(t, d.dict(ed.Get("NS"))))
	}
	if l := text("Lang"); l != "" {
		e.Lang = l
	}
	if id, ok := d.resolve(ed.Get("ID")).Str(); ok && len(id) > 0 && len(id) <= maxInfoString {
		e.ID = string(id)
		if _, dup := w.tree.ids[e.ID]; !dup {
			w.tree.ids[e.ID] = e
		}
	}
	e.attr = w.attributes(ed)
	on := w.kids(e, ed.Get("K"), pg, e.Lang, depth)
	e.Page = on
	if on == noPage {
		e.Page = pg
	}
	return e, on
}

// role returns the type that t of the namespace ns stands for: t for the
// zero Dict is a type of the document's role map, or a standard one.
func (w *structWalk) role(t pdf.Name, ns pdf.Dict) pdf.Name {
	d := w.d
	for range maxStructDepth { // a map that leads in a circle ends here
		if ns.IsZero() {
			next, ok := d.name(w.roleMap.Get(t))
			if !ok || next == t {
				return t
			}
			t = next
			continue
		}
		// A type of another namespace is a standard type, or one of a
		// further namespace: [type namespace].
		to := d.resolve(d.dict(ns.Get("RoleMapNS")).Get(t))
		if next, ok := to.Name(); ok {
			return next
		}
		a, _ := to.Array()
		if len(a) != 2 {
			return t
		}
		next, ok := d.name(a[0])
		if !ok {
			return t
		}
		t, ns = next, d.dict(a[1])
	}
	return t
}

// annot returns the place of the annotation r in /Annots of page.
func (w *structWalk) annot(page int, r pdf.Ref) (int, bool) {
	m, ok := w.annots[page]
	if !ok {
		m = map[pdf.Ref]int{}
		if pd, err := w.d.r.Page(page + 1); err == nil {
			annots, _ := w.d.resolve(pd.Get("Annots")).Array()
			for i, o := range annots {
				if ref, ok := o.Ref(); ok {
					if _, dup := m[ref]; !dup {
						m[ref] = i
					}
				}
			}
		}
		w.annots[page] = m
	}
	i, ok := m[r]
	return i, ok
}

// attributes reads the attributes of the element ed that StructAttr has:
// those of its attribute objects (/A), and of its classes (/C) what these
// do not say. It is nil for an element that says none of them.
func (w *structWalk) attributes(ed pdf.Dict) *StructAttr {
	if !ed.Has("A") && !ed.Has("C") {
		return nil
	}
	d := w.d
	var objs []pdf.Dict
	// add takes an attribute object, or an array of them with revision
	// numbers between.
	add := func(o pdf.Object) {
		o = d.resolve(o)
		list, ok := o.Array()
		if !ok {
			list = pdf.Array{o}
		}
		for _, x := range list {
			x = d.resolve(x)
			ad, ok := x.Dict()
			if s, isStream := x.Stream(); isStream {
				ad, ok = s.Dict, true
			}
			if ok && w.more() {
				objs = append(objs, ad)
			}
		}
	}
	add(ed.Get("A"))
	classes := d.resolve(ed.Get("C"))
	list, ok := classes.Array()
	if !ok {
		list = pdf.Array{classes}
	}
	for _, c := range list {
		if name, ok := d.name(c); ok && w.more() {
			add(w.classMap.Get(name))
		}
	}
	find := func(key pdf.Name) pdf.Object {
		for _, ad := range objs {
			if o, ok := ad.Lookup(key); ok {
				return d.resolve(o)
			}
		}
		return pdf.Null
	}
	a := StructAttr{RowSpan: 1, ColSpan: 1, ListNumbering: "None"}
	said := false
	if v, ok := d.integer(find("RowSpan")); ok && v > 1 {
		a.RowSpan, said = v, true
	}
	if v, ok := d.integer(find("ColSpan")); ok && v > 1 {
		a.ColSpan, said = v, true
	}
	if ids, ok := find("Headers").Array(); ok {
		for _, o := range ids {
			if id, ok := d.resolve(o).Str(); ok && len(id) <= maxInfoString && w.more() {
				a.Headers, said = append(a.Headers, string(id)), true
			}
		}
	}
	if s, ok := find("Scope").Name(); ok && (s == "Row" || s == "Column" || s == "Both") {
		a.Scope, said = string(s), true
	}
	if s := textString(find("Summary")); s != "" {
		a.Summary, said = s[:min(len(s), maxInfoString)], true
	}
	if s, ok := find("ListNumbering").Name(); ok && s != "" {
		a.ListNumbering, said = string(s), true
	}
	if r, ok := d.rect(find("BBox")); ok {
		a.BBox, said = r, true
	}
	if !said {
		return nil
	}
	return &a
}
