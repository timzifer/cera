package pdfedit

import (
	"bytes"
	"math"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// rotation adds extra degrees to a page's /Rotate, read the way cera reads
// it, and returns 0, 90, 180 or 270.
func rotation(o pdf.Object, extra int) int {
	q := 0
	if v, ok := o.Float(); ok && !math.IsNaN(v) && !math.IsInf(v, 0) {
		q = int(math.Mod(math.Round(v/90), 4))
	}
	q += extra / 90
	return (q%4 + 4) % 4 * 90
}

// annots copies the annotations of a page that still make sense alone.
// Each copy of a page gets copies of its annotations, since an annotation
// belongs to one page.
func (x *imported) annots(o pdf.Object) pdf.Array {
	arr, _ := x.d.Resolve(o).Array()
	type kept struct {
		dict pdf.Dict
		num  pdfwrite.Ref // output number; 0 for an annotation written in place
	}
	var keep []kept
	local := map[int32]pdfwrite.Ref{} // source annotation → its copy on this page
	for _, e := range arr {
		ad, ok := x.d.Resolve(e).Dict()
		if !ok || x.d.Resolve(e).Kind() == pdf.KindStream {
			continue
		}
		ad, ok = x.link(ad)
		if !ok {
			continue
		}
		k := kept{dict: ad}
		if r, ok := e.Ref(); ok {
			if _, dup := local[r.Num]; dup {
				continue
			}
			k.num = x.b.w.Alloc()
			local[r.Num] = k.num
		}
		keep = append(keep, k)
	}
	out := make(pdf.Array, 0, len(keep))
	for _, k := range keep {
		var entries []pdf.Entry
		for key, v := range k.dict.All() {
			switch key {
			case "P", "StructParent":
				continue
			case "Parent", "Popup", "IRT":
				// Only references between annotations of this page
				// survive.
				r, ok := v.Ref()
				if !ok {
					continue
				}
				n, ok := local[r.Num]
				if !ok {
					continue
				}
				v = n.Object()
			default:
				v = x.im.Map(v)
			}
			entries = append(entries, pdf.Entry{Key: key, Val: v})
		}
		dict := pdf.NewDict(entries...)
		if k.num == 0 {
			out = append(out, dict.Object())
			continue
		}
		x.b.w.Set(k.num, dict.Object())
		out = append(out, k.num.Object())
	}
	return out
}

// link checks the destination of a link annotation. A link into the
// document stays only when it goes to a copied page; its destination is
// then written explicitly. Other annotations pass unchanged.
func (x *imported) link(ad pdf.Dict) (pdf.Dict, bool) {
	if sub, _ := x.d.Resolve(ad.Get("Subtype")).Name(); sub != "Link" {
		return ad, true
	}
	var dest pdf.Object
	if v, ok := ad.Lookup("Dest"); ok {
		dest = v
	} else {
		act, _ := x.d.Resolve(ad.Get("A")).Dict()
		if s, _ := x.d.Resolve(act.Get("S")).Name(); s != "GoTo" {
			return ad, true // a URI, a remote file, JavaScript, ...
		}
		dest = act.Get("D")
	}
	a := x.explicit(dest)
	if len(a) == 0 {
		return ad, false
	}
	r, ok := a[0].Ref()
	if !ok {
		return ad, false
	}
	if _, ok := x.pageOut[r.Num]; !ok {
		return ad, false
	}
	var entries []pdf.Entry
	for k, v := range ad.All() {
		if k != "A" && k != "Dest" {
			entries = append(entries, pdf.Entry{Key: k, Val: v})
		}
	}
	entries = append(entries, pdf.Entry{Key: "Dest", Val: a.Object()})
	return pdf.NewDict(entries...), true
}

// explicit resolves a destination to its array: an explicit destination,
// or a named one looked up in the catalogue's /Dests and in the /Dests
// name tree.
func (x *imported) explicit(o pdf.Object) pdf.Array {
	o = x.d.Resolve(o)
	var key []byte
	switch o.Kind() {
	case pdf.KindArray:
		a, _ := o.Array()
		return a
	case pdf.KindName:
		n, _ := o.Name()
		key = []byte(n)
	case pdf.KindString:
		key, _ = o.Str()
	default:
		return nil
	}
	cat, err := x.d.Catalog()
	if err != nil {
		return nil
	}
	v := pdf.Null
	if dests, ok := x.d.GetDict(cat, "Dests"); ok {
		v = dests.Get(pdf.Name(key))
	}
	if v.IsNull() {
		names, _ := x.d.GetDict(cat, "Names")
		v = x.nameTree(names.Get("Dests"), key)
	}
	v = x.d.Resolve(v)
	if dd, ok := v.Dict(); ok {
		v = x.d.Resolve(dd.Get("D"))
	}
	a, _ := v.Array()
	return a
}

// maxNameTreeNodes bounds a name tree search.
const maxNameTreeNodes = 1 << 14

// nameTree finds key in the name tree rooted at root.
func (x *imported) nameTree(root pdf.Object, key []byte) pdf.Object {
	stack := []pdf.Object{root}
	seen := map[int32]bool{}
	for n := 0; len(stack) > 0 && n < maxNameTreeNodes; n++ {
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
		names, _ := x.d.Resolve(node.Get("Names")).Array()
		for i := 0; i+1 < len(names); i += 2 {
			if k, ok := x.d.Resolve(names[i]).Str(); ok && bytes.Equal(k, key) {
				return names[i+1]
			}
		}
		kids, _ := x.d.Resolve(node.Get("Kids")).Array()
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, kids[i])
		}
	}
	return pdf.Null
}
