package cera

import (
	"image/color"
	"math"

	"github.com/timzifer/cera/internal/pdf"
)

// The document outline (PDF 2.0, 12.3.3), the bookmarks a viewer shows in
// a side panel: a tree of entries, each with a title and, mostly, a place
// in the document. Its entries are linked by /First and /Next; /Last,
// /Prev, /Parent and the number in /Count are not needed to read it and
// are often wrong, so they are not trusted.

// OutlineItem is an entry of the document outline.
type OutlineItem struct {
	Title string
	// Target is where the entry leads, as for a link: resolve it with
	// [Document.Destination]. An entry that names a structure element
	// and no destination leads to the page of the element. Target is nil
	// for an entry without destination and action, a heading of its
	// children; an action cera does not follow is reported by its type
	// ([LinkTarget.Action]).
	Target *LinkTarget
	// Open says that the children are shown when the outline is first
	// displayed.
	Open bool
	// Bold and Italic are the style of the title, Color its colour,
	// black by default.
	Bold, Italic bool
	Color        color.RGBA
	Children     []OutlineItem

	// from is where the entry was read from, for an editor that writes it
	// again (outlinewrite.go).
	from *outlineFrom
}

// Outline returns the entries at the top of the document outline, nil for
// a document without one. It is read once and shared; do not modify it.
func (d *Document) Outline() []OutlineItem {
	d.outlineOnce.Do(func() {
		defer func() {
			if recover() != nil {
				d.outline = nil // a broken outline is no outline
			}
		}()
		cat, err := d.r.Catalog()
		if err != nil {
			return
		}
		root := d.dict(cat.Get("Outlines"))
		if root.IsZero() {
			return
		}
		w := outlineWalk{d: d, seen: map[pdf.Ref]bool{}}
		d.outline = w.items(root.Get("First"), 1)
	})
	return d.outline
}

// structureDest returns the page of a structure element, the target of an
// entry that names one (/SE) and no destination: nil if it has no page.
// The page is the element's (/Pg) or that of the nearest element above it.
func (d *Document) structureDest(o pdf.Object) *LinkTarget {
	se := d.dict(o)
	for range maxOutlineDepth {
		if se.IsZero() {
			return nil
		}
		if pg, ok := se.Get("Pg").Ref(); ok {
			if i := d.pageIndex(pg); i >= 0 {
				nan := math.NaN()
				return &LinkTarget{Page: i, Fit: "XYZ", Params: []float64{nan, nan, nan}}
			}
			return nil
		}
		se = d.dict(se.Get("P"))
	}
	return nil
}

// outlineWalk reads an outline: seen holds the entries read, by which a
// chain that comes back to one of them ends, and n counts them.
type outlineWalk struct {
	d    *Document
	seen map[pdf.Ref]bool
	n    int
}

// items reads the chain of entries that starts at first, at depth.
func (w *outlineWalk) items(first pdf.Object, depth int) []OutlineItem {
	d := w.d
	var out []OutlineItem
	for o := first; w.n < maxOutlineItems; {
		if ref, ok := o.Ref(); ok {
			if w.seen[ref] {
				break
			}
			w.seen[ref] = true
		}
		it := d.dict(o)
		if it.IsZero() {
			break
		}
		w.n++
		item := OutlineItem{
			Title:  textString(d.resolve(it.Get("Title"))),
			Target: d.linkTarget(it),
			Color:  color.RGBA{A: 255},
		}
		if len(item.Title) > maxInfoString {
			item.Title = item.Title[:maxInfoString]
		}
		if item.Target == nil {
			item.Target = d.structureDest(it.Get("SE"))
		}
		if n, ok := d.integer(it.Get("Count")); ok && n > 0 {
			item.Open = true
		}
		if f, ok := d.integer(it.Get("F")); ok {
			item.Italic, item.Bold = f&1 != 0, f&2 != 0
		}
		if c, _ := d.annotColor(it.Get("C")); len(c) == 3 {
			item.Color = color.RGBA{unit8(c[0]), unit8(c[1]), unit8(c[2]), 255}
		}
		if ref, ok := o.Ref(); ok {
			item.from = &outlineFrom{doc: d, ref: ref, target: copyTarget(item.Target)}
		}
		if depth < maxOutlineDepth {
			item.Children = w.items(it.Get("First"), depth+1)
		}
		out = append(out, item)
		o = it.Get("Next")
	}
	return out
}
