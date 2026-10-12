package pdfedit

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// What imported pages bring from their document besides themselves: the
// entries of its outline that lead to them, and its named destinations on
// them. Both are rewritten for the output — an entry or a name whose page
// did not come is dropped — and joined to what the edited document has.

// Import says what the pages of a source bring with them.
type Import struct {
	// Outline brings the entries of the source's outline that lead to an
	// imported page, with the entries above them, under an entry titled
	// OutlineTitle, or at the top level when that is empty.
	Outline      bool
	OutlineTitle string
	// Destinations brings the source's named destinations on imported
	// pages, a name the output has renamed name_2, name_3 and so on.
	Destinations bool
}

// Limits of what is read of a source's outline and names.
const (
	maxOutlineDepth = 32
	maxOutlineItems = 1 << 16
	maxDests        = 1 << 18
)

// outItem is an outline entry for the output: its entries but for the
// links to its neighbours, in the output's space.
type outItem struct {
	entries []pdf.Entry
	kids    []*outItem
	open    bool // shows its kids
	ref     pdfwrite.Ref
}

// shown returns how many entries a viewer lists for the chain: its
// entries, and what is below those that are open.
func shown(items []*outItem) int64 {
	n := int64(len(items))
	for _, it := range items {
		if it.open {
			n += shown(it.kids)
		}
	}
	return n
}

// outlineItems reads the chain of outline entries of the source that
// starts at first and returns those that lead to a page in the output, or
// have such an entry below them.
func (x *source) outlineItems(first pdf.Object, depth int, seen map[int32]bool, n *int) []*outItem {
	var out []*outItem
	for o := first; *n < maxOutlineItems; {
		if r, ok := o.Ref(); ok {
			if seen[r.Num] {
				break
			}
			seen[r.Num] = true
		}
		it, ok := x.d.Resolve(o).Dict()
		if !ok {
			break
		}
		*n++
		item := &outItem{}
		// Where it leads: a destination, or the destination of a GoTo.
		dest := it.Get("Dest")
		if dest.IsNull() {
			if act, ok := x.d.Resolve(it.Get("A")).Dict(); ok {
				if s, _ := x.d.Resolve(act.Get("S")).Name(); s == "GoTo" {
					dest = act.Get("D")
				}
			}
		}
		leads := false
		if a := x.explicit(dest); len(a) > 0 {
			if r, ok := a[0].Ref(); ok && x.pageTarget(r) != 0 {
				item.entries = append(item.entries, pdf.Entry{Key: "Dest", Val: x.im.Map(a.Object())})
				leads = true
			}
		}
		if depth < maxOutlineDepth {
			item.kids = x.outlineItems(it.Get("First"), depth+1, seen, n)
		}
		if leads || len(item.kids) > 0 {
			c, _ := x.d.Resolve(it.Get("Count")).Int()
			item.open = c > 0
			for _, k := range []pdf.Name{"Title", "C", "F"} {
				if v := it.Get(k); !v.IsNull() {
					item.entries = append(item.entries, pdf.Entry{Key: k, Val: x.im.Map(v)})
				}
			}
			out = append(out, item)
		}
		o = it.Get("Next")
	}
	return out
}

// writeItems numbers the entries of a chain and writes them, linked to
// each other, to parent, and to prev and next where the chain goes on.
func (b *builder) writeItems(items []*outItem, parent pdfwrite.Ref, prev, next pdf.Object) {
	for _, it := range items {
		it.ref = b.w.Alloc()
	}
	for i, it := range items {
		e := append(it.entries, pdf.Entry{Key: "Parent", Val: parent.Object()})
		switch {
		case i > 0:
			e = append(e, pdf.Entry{Key: "Prev", Val: items[i-1].ref.Object()})
		case !prev.IsNull():
			e = append(e, pdf.Entry{Key: "Prev", Val: prev})
		}
		switch {
		case i+1 < len(items):
			e = append(e, pdf.Entry{Key: "Next", Val: items[i+1].ref.Object()})
		case !next.IsNull():
			e = append(e, pdf.Entry{Key: "Next", Val: next})
		}
		if len(it.kids) > 0 {
			b.writeItems(it.kids, it.ref, pdf.Null, pdf.Null)
			// The number of entries it shows, negative while it is closed.
			count := shown(it.kids)
			if !it.open {
				count = -count
			}
			e = append(e,
				pdf.Entry{Key: "First", Val: it.kids[0].ref.Object()},
				pdf.Entry{Key: "Last", Val: it.kids[len(it.kids)-1].ref.Object()},
				pdf.Entry{Key: "Count", Val: pdf.Integer(count)},
			)
		}
		b.w.Set(it.ref, pdf.NewDict(e...).Object())
	}
}

// alias returns a reference, in the space of the edited document, to the
// output object r: for a changed object of the edited document that
// names one made for the output.
func (b *builder) alias(r pdfwrite.Ref) pdf.Object {
	i := int32(len(b.doc.Objects))
	b.doc.Objects = append(b.doc.Objects, pdf.Null)
	b.newOut[i] = r
	return NewRef(int(i)).Object()
}

// change replaces an object of the edited document that is a dictionary
// by one with the entries set.
func (b *builder) change(x *source, r pdf.Ref, set ...pdf.Entry) {
	d, _ := x.obj(r).Dict()
	o := apply(d, Patch{Set: set}).Object()
	b.patched[r.Num] = o
	// Where the object has its place in the output already, it is set
	// there: an update keeps the numbers, and a new file may have taken
	// the object as it was.
	if b.upd != nil {
		b.w.Set(pdfwrite.Ref(r.Num), x.im.Map(o))
	} else if n, ok := x.im.Copied(r); ok {
		b.w.Set(n, x.im.Map(o))
	}
}

// mergeOutlines joins the outline entries imported pages bring to the
// outline of the output: after the entries of the edited document, each
// source's under an entry of its own where it asks for one.
func (b *builder) mergeOutlines() {
	var top []*outItem
	for _, x := range b.sources {
		opt := b.doc.Imports[x.d]
		if x.own || x.shared != nil || !opt.Outline {
			continue
		}
		cat, err := x.d.Catalog()
		if err != nil {
			continue
		}
		root, _ := x.d.GetDict(cat, "Outlines")
		n := 0
		items := x.outlineItems(root.Get("First"), 1, map[int32]bool{}, &n)
		if len(items) == 0 {
			continue
		}
		if opt.OutlineTitle != "" {
			items = []*outItem{{
				entries: []pdf.Entry{{Key: "Title", Val: pdf.String(textString(opt.OutlineTitle))}},
				kids:    items,
			}}
		}
		top = append(top, items...)
	}
	if len(top) == 0 {
		return
	}
	// The outline of the edited document, when it has one that is an
	// object of its own with entries: the new ones follow its last.
	if own := b.sources[0]; own.own {
		cat, _ := own.d.Catalog()
		if r, ok := own.d.Trailer().Get("Root").Ref(); ok {
			if p, ok := own.obj(r).Dict(); ok {
				cat = p
			}
		}
		if rootRef, ok := cat.Get("Outlines").Ref(); ok {
			root, _ := own.obj(rootRef).Dict()
			if lastRef, ok := root.Get("Last").Ref(); ok && !root.Get("First").IsNull() {
				rootOut := own.ref(rootRef)
				b.writeItems(top, rootOut, own.ref(lastRef).Object(), pdf.Null)
				count, _ := own.d.Resolve(root.Get("Count")).Int()
				b.change(own, lastRef, pdf.Entry{Key: "Next", Val: b.alias(top[0].ref)})
				b.change(own, rootRef,
					pdf.Entry{Key: "Last", Val: b.alias(top[len(top)-1].ref)},
					pdf.Entry{Key: "Count", Val: pdf.Integer(max(count, 0) + shown(top))})
				return
			}
		}
	}
	root := b.w.Alloc()
	b.writeItems(top, root, pdf.Null, pdf.Null)
	b.w.Set(root, pdf.NewDict(
		pdf.Entry{Key: "Type", Val: pdf.Name("Outlines").Object()},
		pdf.Entry{Key: "First", Val: top[0].ref.Object()},
		pdf.Entry{Key: "Last", Val: top[len(top)-1].ref.Object()},
		pdf.Entry{Key: "Count", Val: pdf.Integer(shown(top))},
	).Object())
	b.outlines = root.Object()
}

// textString encodes s as a text string: as it is when it is ASCII, else
// as UTF-16BE with a byte order mark.
func textString(s string) []byte {
	ascii := true
	for i := 0; i < len(s); i++ {
		ascii = ascii && s[i] < 0x80
	}
	if ascii {
		return []byte(s)
	}
	out := []byte{0xfe, 0xff}
	for _, r := range s {
		if r > 0xffff {
			r -= 0x10000
			hi, lo := 0xd800+(r>>10), 0xdc00+(r&0x3ff)
			out = append(out, byte(hi>>8), byte(hi), byte(lo>>8), byte(lo))
			continue
		}
		out = append(out, byte(r>>8), byte(r))
	}
	return out
}

// dest is a named destination.
type dest struct {
	name []byte
	val  pdf.Object // in the output's space
}

// dests returns the named destinations of the source: those of the /Dests
// dictionary of its catalogue and of its /Dests name tree, with their
// values as the source has them.
func (x *source) dests() []dest {
	var out []dest
	cat, err := x.d.Catalog()
	if err != nil {
		return nil
	}
	if r, ok := x.d.Trailer().Get("Root").Ref(); ok && x.own {
		if p, ok := x.obj(r).Dict(); ok {
			cat = p
		}
	}
	if old, ok := x.d.GetDict(cat, "Dests"); ok {
		for k, v := range old.All() {
			out = append(out, dest{[]byte(k), v})
		}
	}
	names, _ := x.d.GetDict(cat, "Names")
	stack := []pdf.Object{names.Get("Dests")}
	seen := map[int32]bool{}
	for n := 0; len(stack) > 0 && n < maxNameTreeNodes && len(out) < maxDests; n++ {
		o := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if r, ok := o.Ref(); ok {
			if seen[r.Num] {
				continue
			}
			seen[r.Num] = true
		}
		node, ok := x.d.Resolve(o).Dict()
		if !ok {
			continue
		}
		pairs, _ := x.d.Resolve(node.Get("Names")).Array()
		for i := 0; i+1 < len(pairs); i += 2 {
			if k, ok := x.d.Resolve(pairs[i]).Str(); ok {
				out = append(out, dest{k, pairs[i+1]})
			}
		}
		kids, _ := x.d.Resolve(node.Get("Kids")).Array()
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, kids[i])
		}
	}
	return out
}

// mergeDests joins the named destinations imported pages bring to those
// of the output, in one name tree written anew: names are kept in order,
// and a tree cannot take more without being rebuilt.
func (b *builder) mergeDests() {
	var all []dest
	taken := map[string]bool{}
	var own *source
	if x := b.sources[0]; x.own {
		own = x
		for _, d := range x.dests() {
			if !taken[string(d.name)] {
				taken[string(d.name)] = true
				all = append(all, dest{d.name, x.im.Map(d.val)})
			}
		}
	}
	added := false
	for _, x := range b.sources {
		if x.own || x.shared != nil || !b.doc.Imports[x.d].Destinations {
			continue
		}
		for _, d := range x.dests() {
			// The destination itself, also where the source wraps it in
			// a dictionary: on a page of the output, or left out.
			v := x.d.Resolve(d.val)
			if dd, ok := v.Dict(); ok {
				v = x.d.Resolve(dd.Get("D"))
			}
			a, _ := v.Array()
			if len(a) == 0 {
				continue
			}
			if r, ok := a[0].Ref(); !ok || x.pageTarget(r) == 0 {
				continue
			}
			name := d.name
			for i := 2; taken[string(name)]; i++ {
				name = fmt.Appendf(nil, "%s_%d", d.name, i)
			}
			taken[string(name)] = true
			all = append(all, dest{name, x.im.Map(a.Object())})
			added = true
		}
	}
	if !added {
		return
	}
	slices.SortFunc(all, func(a, b dest) int { return bytes.Compare(a.name, b.name) })
	pairs := make(pdf.Array, 0, 2*len(all))
	for _, d := range all {
		pairs = append(pairs, pdf.String(d.name), d.val)
	}
	tree := b.w.Add(pdf.NewDict(pdf.Entry{Key: "Names", Val: pairs.Object()}).Object())
	// The /Names of the catalogue: what the edited document has there,
	// with this tree.
	entries := []pdf.Entry{{Key: "Dests", Val: tree.Object()}}
	if own != nil {
		cat, _ := own.d.Catalog()
		if r, ok := own.d.Trailer().Get("Root").Ref(); ok {
			if p, ok := own.obj(r).Dict(); ok {
				cat = p
			}
		}
		names, _ := own.d.GetDict(cat, "Names")
		for k, v := range names.All() {
			if k != "Dests" {
				entries = append(entries, pdf.Entry{Key: k, Val: own.im.Map(v)})
			}
		}
	}
	b.names2 = pdf.NewDict(entries...).Object()
	// The names of the old dictionary are in the tree now.
	b.dropDests = true
}
