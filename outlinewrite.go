package cera

import (
	"errors"
	"fmt"
	"image/color"
	"math"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
)

// The outline of a result: set as a whole, or the edited document's
// without the entries of pages that are gone.
//
// The entries are those of reading ([OutlineItem]). An entry that comes
// from the outline of the edited document and leads where it led is
// written with what it had — its destination, its action, the structure
// element it names —, so that renaming, reordering and removing entries
// loses nothing of the others, also not what cera reads and does not
// write. An entry that is new, or leads somewhere new, is written from
// its Target.

// outlineFrom is where an entry was read from: the document, the object,
// and the target it had.
type outlineFrom struct {
	doc    *Document
	ref    pdf.Ref
	target *LinkTarget
}

// outlineNode is an entry of the outline an editor has set.
type outlineNode struct {
	title        string
	open         bool
	bold, italic bool
	color        color.RGBA
	// One of the three says where it leads, or none: the entry of the
	// edited document's outline it stands for, a page of the result by
	// its identity with the view of it, or a target that is no page.
	from   pdf.Ref
	page   int
	fit    string
	params []float64
	target *LinkTarget
	kids   []outlineNode
}

func copyTarget(t *LinkTarget) *LinkTarget {
	if t == nil {
		return nil
	}
	c := *t
	c.Params = append([]float64(nil), t.Params...)
	c.Embedded = append([]string(nil), t.Embedded...)
	return &c
}

// internal reports whether t leads to a page of the document it is in by
// an explicit destination.
func (t *LinkTarget) internal() bool {
	return t.URI == "" && !t.External && t.Named == "" && t.NamedAction == "" && t.Page >= 0
}

// newID returns an identity for a page of the result: what an outline
// entry holds on to when pages move.
func (e *Editor) newID() int {
	e.lastID++
	return e.lastID
}

// pageOf returns the index in the result of the page with identity id, -1
// when it is gone.
func (e *Editor) pageOf(id int) int {
	for i := range e.pages {
		if e.pages[i].id == id {
			return i
		}
	}
	return -1
}

// resultTarget returns the target t of the edited document as one of the
// result: its page is the index the page has there. It is nil for an
// explicit destination on a page that is gone.
func (e *Editor) resultTarget(t *LinkTarget) *LinkTarget {
	if t == nil || t.External || t.URI != "" || t.NamedAction != "" || t.Page < 0 {
		return copyTarget(t)
	}
	c := copyTarget(t)
	c.Page = -1
	for i := range e.pages {
		if p := &e.pages[i]; p.own && p.index == t.Page {
			c.Page = i
			break
		}
	}
	if c.Page < 0 && c.Named == "" {
		return nil
	}
	return c
}

// Outline returns the outline of the result as it stands: the one
// [Editor.SetOutline] set, or that of the edited document. The targets
// name pages of the result; an entry whose page is no longer there has
// none. Changed and given back to SetOutline, it is the outline with
// those changes and nothing else lost.
func (e *Editor) Outline() []OutlineItem {
	if e.outlineSet {
		byRef := map[pdf.Ref]*OutlineItem{}
		if e.base != nil {
			var walk func(items []OutlineItem)
			walk = func(items []OutlineItem) {
				for i := range items {
					if f := items[i].from; f != nil {
						byRef[f.ref] = &items[i]
					}
					walk(items[i].Children)
				}
			}
			walk(e.base.Outline())
		}
		return e.outlineItems(e.outline, byRef)
	}
	if e.base == nil {
		return nil
	}
	return e.resultOutline(e.base.Outline())
}

// resultOutline returns entries of the edited document's outline with the
// targets of the result.
func (e *Editor) resultOutline(items []OutlineItem) []OutlineItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]OutlineItem, len(items))
	for i, it := range items {
		out[i] = it
		out[i].Target = e.resultTarget(it.Target)
		if it.from != nil {
			out[i].from = &outlineFrom{doc: it.from.doc, ref: it.from.ref, target: copyTarget(out[i].Target)}
		}
		out[i].Children = e.resultOutline(it.Children)
	}
	return out
}

// outlineItems returns the entries the editor has set, as they read.
func (e *Editor) outlineItems(nodes []outlineNode, byRef map[pdf.Ref]*OutlineItem) []OutlineItem {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]OutlineItem, len(nodes))
	for i, n := range nodes {
		it := OutlineItem{Title: n.title, Open: n.open, Bold: n.bold, Italic: n.italic, Color: n.color}
		switch {
		case n.from.Num != 0:
			if was := byRef[n.from]; was != nil {
				it.Target = e.resultTarget(was.Target)
			}
			it.from = &outlineFrom{doc: e.base, ref: n.from, target: copyTarget(it.Target)}
		case n.page != 0:
			if k := e.pageOf(n.page); k >= 0 {
				it.Target = &LinkTarget{Page: k, Fit: n.fit, Params: append([]float64(nil), n.params...)}
			}
		default:
			it.Target = copyTarget(n.target)
		}
		it.Children = e.outlineItems(n.kids, byRef)
		out[i] = it
	}
	return out
}

// SetOutline makes items the outline of the result, in place of the one
// the edited document has; none makes a result without an outline. A
// Target names a page of the result as it is now and stays with that page
// when pages are moved afterwards; when the page is deleted the entry
// leads nowhere, or goes with [Editor.PruneOutline]. Besides pages, an
// entry may lead to an address (URI), a named destination, another file,
// or do one of the named actions; an action cera only reports cannot be
// made anew.
//
// Entries taken from [Editor.Outline] or from the outline of the edited
// document, with the target they had, are written as the document has
// them. The outlines that [Editor.ImportPagesWith] brings are not added to
// an outline that is set: it is the whole outline.
func (e *Editor) SetOutline(items []OutlineItem) error {
	n := 0
	nodes, err := e.outlineNodes(items, 1, &n)
	if err != nil {
		return err
	}
	e.outline, e.outlineSet = nodes, true
	return nil
}

// PruneOutline leaves the entries out of the outline that led to a page
// which is not in the result — after [Editor.DeletePages], or in a result
// made of some pages of a document. An entry with entries below it that
// still lead somewhere stays, as their heading. It holds for the outline
// as it is written, whatever is deleted afterwards.
func (e *Editor) PruneOutline() { e.outlinePrune = true }

// outlined reports whether the editor changes the outline.
func (e *Editor) outlined() bool { return e.outlineSet || e.outlinePrune }

func (e *Editor) outlineNodes(items []OutlineItem, depth int, n *int) ([]outlineNode, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if depth > maxOutlineDepth {
		return nil, fmt.Errorf("cera: an outline of more than %d levels", maxOutlineDepth)
	}
	out := make([]outlineNode, len(items))
	for i, it := range items {
		if *n++; *n > maxOutlineItems {
			return nil, fmt.Errorf("cera: an outline of more than %d entries", maxOutlineItems)
		}
		node := outlineNode{title: it.Title, open: it.Open, bold: it.Bold, italic: it.Italic, color: it.Color}
		t := it.Target
		switch f := it.from; {
		case f != nil && e.base != nil && f.doc == e.base && (t == nil && f.target == nil || t != nil && f.target != nil && sameTarget(t, f.target)):
			node.from = f.ref
		case t == nil:
		case !writableLink(t):
			if t.Action == "" {
				return nil, fmt.Errorf("cera: the outline entry %q has a target that leads nowhere", it.Title)
			}
			return nil, fmt.Errorf("cera: the outline entry %q: the action %q cannot be written", it.Title, t.Action)
		case t.internal():
			if t.Page >= len(e.pages) {
				return nil, fmt.Errorf("cera: the outline entry %q leads to page %d of %d", it.Title, t.Page, len(e.pages))
			}
			node.page, node.fit, node.params = e.pages[t.Page].id, t.Fit, append([]float64(nil), t.Params...)
			if node.fit == "" {
				node.fit = "Fit"
			}
		default:
			node.target = copyTarget(t)
		}
		kids, err := e.outlineNodes(it.Children, depth+1, n)
		if err != nil {
			return nil, err
		}
		node.kids = kids
		out[i] = node
	}
	return out, nil
}

// outlinePlan describes the outline for the internal writer.
func (e *Editor) outlinePlan(doc *pdfedit.Doc) {
	if !e.outlined() {
		return
	}
	nodes := e.outline
	if !e.outlineSet {
		// The document's own, to be pruned: with what imported pages
		// bring after it, as without.
		if e.base == nil || len(e.base.Outline()) == 0 {
			return
		}
		n := 0
		var err error
		if nodes, err = e.outlineNodes(e.Outline(), 1, &n); err != nil {
			return
		}
	}
	doc.OutlineSet, doc.OutlineAlone, doc.OutlinePrune = true, e.outlineSet, e.outlinePrune
	doc.Outline = e.outlineOut(nodes)
}

func (e *Editor) outlineOut(nodes []outlineNode) []pdfedit.OutlineItem {
	out := make([]pdfedit.OutlineItem, len(nodes))
	for i, n := range nodes {
		it := pdfedit.OutlineItem{Page: -1, Open: n.open, Kids: e.outlineOut(n.kids)}
		it.Entries = []pdf.Entry{{Key: "Title", Val: pdf.String(encodeText(n.title))}}
		if f := b2i(n.italic) | b2i(n.bold)<<1; f != 0 {
			it.Entries = append(it.Entries, pdf.Entry{Key: "F", Val: pdf.Integer(int64(f))})
		}
		if c := n.color; c.R|c.G|c.B != 0 {
			it.Entries = append(it.Entries, pdf.Entry{Key: "C", Val: pdf.Array{
				pdf.Real(float64(c.R) / 255), pdf.Real(float64(c.G) / 255), pdf.Real(float64(c.B) / 255)}.Object()})
		}
		switch {
		case n.from.Num != 0:
			it.From = n.from
		case n.page != 0:
			if it.Page = e.pageOf(n.page); it.Page < 0 {
				it.Lost = true
				break
			}
			it.View = pdf.Array{pdf.Name(n.fit).Object()}
			for _, v := range n.params {
				if math.IsNaN(v) {
					it.View = append(it.View, pdf.Null)
				} else {
					it.View = append(it.View, pdf.Real(v))
				}
			}
		case n.target != nil:
			it.Entries = append(it.Entries, linkEntry(n.target, func(int) pdf.Object { return pdf.Null }))
		}
		out[i] = it
	}
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// LabelStyle is how the pages of a [LabelRange] are numbered.
type LabelStyle byte

const (
	LabelNone        LabelStyle = 0   // no number: the prefix alone
	LabelDecimal     LabelStyle = 'D' // 1, 2, 3
	LabelRomanUpper  LabelStyle = 'R' // I, II, III
	LabelRomanLower  LabelStyle = 'r' // i, ii, iii
	LabelLetterUpper LabelStyle = 'A' // A to Z, then AA to ZZ
	LabelLetterLower LabelStyle = 'a' // a to z, then aa to zz
)

// LabelRange labels the pages from Start on, up to the next range: with
// Prefix and then a number in Style that counts on from First.
type LabelRange struct {
	// Start is the index of the first page of the range.
	Start  int
	Style  LabelStyle
	Prefix string
	// First is the number of the page at Start: 1 or more, and 1 for 0.
	First int
}

// PageLabels returns the ranges that label the pages of the document, in
// the order of the pages; nil for a document without page labels, whose
// pages are 1, 2, 3. [Document.PageLabel] gives the label of one page.
func (d *Document) PageLabels() []LabelRange {
	ranges := d.labelRanges()
	if len(ranges) == 0 {
		return nil
	}
	out := make([]LabelRange, len(ranges))
	for i, r := range ranges {
		out[i] = LabelRange{Start: r.start, Style: LabelStyle(r.style), Prefix: r.prefix, First: r.first}
	}
	return out
}

// SetPageLabels labels the pages of the result: each range the pages from
// its Start to the next one. Pages before the first range are numbered 1,
// 2, 3; no ranges make a result without page labels. The ranges are of
// the pages as they are when the result is written, by their place: set
// them when the pages are in order. Without it the result keeps the
// labels its pages have, with the pages.
func (e *Editor) SetPageLabels(ranges []LabelRange) error {
	set := make([]LabelRange, len(ranges))
	for i, r := range ranges {
		switch r.Style {
		case LabelNone, LabelDecimal, LabelRomanUpper, LabelRomanLower, LabelLetterUpper, LabelLetterLower:
		default:
			return fmt.Errorf("cera: label style %q", byte(r.Style))
		}
		if r.Start < 0 || r.Start >= len(e.pages) {
			return fmt.Errorf("cera: a label range from page %d of %d", r.Start, len(e.pages))
		}
		if i > 0 && r.Start <= ranges[i-1].Start {
			return errors.New("cera: label ranges are in the order of their pages, one for each start")
		}
		if r.First < 0 {
			return fmt.Errorf("cera: a label range that starts with the number %d", r.First)
		}
		if r.First == 0 {
			r.First = 1
		}
		set[i] = r
	}
	e.labels, e.labelsSet = set, true
	return nil
}

// setLabels returns the /PageLabels of the ranges set, the zero
// dictionary for none.
func (e *Editor) setLabels() pdf.Dict {
	var nums pdf.Array
	for i, r := range e.labels {
		if r.Start >= len(e.pages) {
			break
		}
		if i == 0 && r.Start > 0 {
			// The tree has to say how the first page is labelled.
			nums = append(nums, pdf.Integer(0), pdf.NewDict(pdf.Entry{Key: "S", Val: pdf.Name("D").Object()}).Object())
		}
		var entries []pdf.Entry
		if r.Style != LabelNone {
			entries = append(entries, pdf.Entry{Key: "S", Val: pdf.Name(string(rune(r.Style))).Object()})
			if r.First != 1 {
				entries = append(entries, pdf.Entry{Key: "St", Val: pdf.Integer(int64(r.First))})
			}
		}
		if r.Prefix != "" {
			entries = append(entries, pdf.Entry{Key: "P", Val: pdf.String(encodeText(r.Prefix))})
		}
		nums = append(nums, pdf.Integer(int64(r.Start)), pdf.NewDict(entries...).Object())
	}
	if len(nums) == 0 {
		return pdf.Dict{}
	}
	return pdf.NewDict(pdf.Entry{Key: "Nums", Val: nums.Object()})
}
