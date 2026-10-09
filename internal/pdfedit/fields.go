package pdfedit

import (
	"fmt"
	"slices"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// Pages imported with their fields (Page.Fields) bring the part of the
// field tree of their document that leads to their widgets. It comes with
// the first copy of a page; widgets of a page imported again stay plain
// annotations, or there would be two fields of one. A field whose name the
// output already has, from the edited document or another import, is
// renamed at its root: "name_2". What the form of the source gave every
// field — /DA, /Q — is written onto the roots it imports, and the fonts of
// its /DR join the output's under names it does not use yet. Signature
// fields stay plain annotations: a copied signature would sign nothing.

// fieldCopy is what one source brings of its form.
type fieldCopy struct {
	out     map[int32]pdfwrite.Ref // field node → its copy
	nodes   []int32                // field nodes in the order they were met
	widgets map[int32]pdfwrite.Ref // widget annotation → its copy
	roots   []pdfwrite.Ref         // the copies of root fields, widgets included
	// parent is the field tree as the form's /Fields and /Kids give it,
	// as cera reads forms: a kid whose /Parent is missing or wrong still
	// belongs to the node that lists it. Roots map to the zero Ref.
	parent map[int32]pdf.Ref
}

// maxFieldDepth bounds a walk up a field tree.
const maxFieldDepth = 64

// form returns the source's form dictionary.
func (x *source) form() pdf.Dict {
	cat, _ := x.d.Catalog()
	f, _ := x.d.GetDict(cat, "AcroForm")
	return f
}

// tree returns the field tree of the source's form, read once.
func (x *source) tree() map[int32]pdf.Ref {
	fc := x.fieldCopy()
	if fc.parent != nil {
		return fc.parent
	}
	fc.parent = map[int32]pdf.Ref{}
	var walk func(o pdf.Object, parent pdf.Ref, depth int)
	walk = func(o pdf.Object, parent pdf.Ref, depth int) {
		r, ok := o.Ref()
		if !ok || depth > maxFieldDepth || len(fc.parent) >= maxFieldNodes {
			return
		}
		if _, seen := fc.parent[r.Num]; seen {
			return
		}
		fc.parent[r.Num] = parent
		node, _ := x.d.Resolve(o).Dict()
		kids, _ := x.d.Resolve(node.Get("Kids")).Array()
		for _, k := range kids {
			walk(k, r, depth+1)
		}
	}
	fields, _ := x.d.Resolve(x.form().Get("Fields")).Array()
	for _, f := range fields {
		walk(f, pdf.Ref{}, 0)
	}
	return fc.parent
}

// fieldWidget reports whether annotation r is the widget of a field that
// can be imported: in the field tree, and not of a signature.
func (x *source) fieldWidget(r pdf.Ref) bool {
	tree := x.tree()
	if _, ok := tree[r.Num]; !ok {
		return false
	}
	for range maxFieldDepth {
		node, _ := x.d.Resolve(r.Object()).Dict()
		if ft, ok := x.d.Resolve(node.Get("FT")).Name(); ok {
			return ft != "Sig"
		}
		if r = tree[r.Num]; r.Num == 0 {
			break
		}
	}
	return true
}

// fieldRef returns the output number of field node r, numbering it and
// its ancestors when first met; their dictionaries are written by
// finishFields.
func (x *source) fieldRef(r pdf.Ref) pdfwrite.Ref {
	fc := x.fieldCopy()
	if n, ok := fc.out[r.Num]; ok {
		return n
	}
	n := x.b.w.Alloc()
	fc.out[r.Num] = n
	fc.nodes = append(fc.nodes, r.Num)
	if p := x.tree()[r.Num]; p.Num != 0 {
		x.fieldRef(p)
	}
	return n
}

func (x *source) fieldCopy() *fieldCopy {
	if x.fields == nil {
		x.fields = &fieldCopy{out: map[int32]pdfwrite.Ref{}, widgets: map[int32]pdfwrite.Ref{}}
	}
	return x.fields
}

// rootEntries returns the entries a root field brings from the form of
// its source: /DA and /Q where the field has none, and its name made
// unique in the output.
func (x *source) rootEntries(node pdf.Dict) []pdf.Entry {
	var out []pdf.Entry
	form := x.form()
	for _, k := range [...]pdf.Name{"DA", "Q"} {
		if v := form.Get(k); !node.Has(k) && !v.IsNull() {
			out = append(out, pdf.Entry{Key: k, Val: x.im.Map(v)})
		}
	}
	if t, ok := x.d.Resolve(node.Get("T")).Str(); ok {
		out = append(out, pdf.Entry{Key: "T", Val: pdf.String(x.b.uniqueName(t))})
	}
	return out
}

// uniqueName returns name, or name_2, name_3 … if the output has it.
func (b *builder) uniqueName(name []byte) []byte {
	if b.names == nil {
		b.names = map[string]bool{}
		if b.doc.Base != nil {
			cat, _ := b.doc.Base.Catalog()
			form, _ := b.doc.Base.GetDict(cat, "AcroForm")
			fields, _ := b.doc.Base.Resolve(form.Get("Fields")).Array()
			for _, f := range fields {
				fd, _ := b.doc.Base.Resolve(f).Dict()
				if t, ok := b.doc.Base.Resolve(fd.Get("T")).Str(); ok {
					b.names[string(t)] = true
				}
			}
		}
	}
	out := slices.Clone(name)
	for i := 2; b.names[string(out)]; i++ {
		out = fmt.Appendf(slices.Clone(name), "_%d", i)
	}
	b.names[string(out)] = true
	return out
}

// finishFields writes the field nodes the source's widgets led to: their
// kids are those copied, their parents the copies.
func (x *source) finishFields() {
	fc := x.fields
	if fc == nil {
		return
	}
	for _, num := range fc.nodes {
		r := pdf.Ref{Num: num}
		node, _ := x.d.Resolve(r.Object()).Dict()
		var entries []pdf.Entry
		parent := fc.parent[num]
		root := parent.Num == 0
		if !root {
			entries = append(entries, pdf.Entry{Key: "Parent", Val: fc.out[parent.Num].Object()})
		}
		for k, v := range node.All() {
			switch k {
			case "Parent":
				continue // from the tree
			case "Kids":
				kids, _ := x.d.Resolve(v).Array()
				var keep pdf.Array
				for _, kid := range kids {
					kr, ok := kid.Ref()
					if !ok {
						continue
					}
					if n, ok := fc.out[kr.Num]; ok {
						keep = append(keep, n.Object())
					} else if n, ok := fc.widgets[kr.Num]; ok {
						keep = append(keep, n.Object())
					}
				}
				v = keep.Object()
			case "T":
				if root {
					continue // rootEntries writes it, unique
				}
				v = x.im.Map(v)
			default:
				v = x.im.Map(v)
			}
			entries = append(entries, pdf.Entry{Key: k, Val: v})
		}
		if root {
			entries = append(entries, x.rootEntries(node)...)
			fc.roots = append(fc.roots, fc.out[num])
		}
		x.b.w.Set(fc.out[num], pdf.NewDict(entries...).Object())
	}
}

// formDict returns the interactive form of the output when pages bring
// fields: the edited document's, or a new one, with the imported roots
// added to /Fields and the fonts of their forms to /DR. It returns null
// when no fields are imported.
func (b *builder) formDict() pdf.Object {
	var roots pdf.Array
	var from []*source
	for _, x := range b.sources {
		if x.fields != nil && len(x.fields.roots) > 0 {
			for _, r := range x.fields.roots {
				roots = append(roots, r.Object())
			}
			from = append(from, x)
		}
	}
	if len(roots) == 0 {
		return pdf.Null
	}
	var entries, drOther, fonts []pdf.Entry
	var fields pdf.Array
	have := map[pdf.Name]bool{}
	need := false
	if x := b.sources[0]; x.own {
		form := x.form()
		if r, ok := b.ownCatalog().Get("AcroForm").Ref(); ok {
			form, _ = x.obj(r).Dict()
		} else if f, ok := b.ownCatalog().Get("AcroForm").Dict(); ok {
			form = f
		}
		for k, v := range form.All() {
			switch k {
			case "Fields":
				a, _ := x.im.Map(x.d.Resolve(v)).Array()
				fields = append(fields, a...)
			case "DR":
				dr, _ := x.d.Resolve(v).Dict()
				for k2, v2 := range dr.All() {
					if k2 != "Font" {
						drOther = append(drOther, pdf.Entry{Key: k2, Val: x.im.Map(v2)})
					}
				}
				fd, _ := x.d.GetDict(dr, "Font")
				for fk, fv := range fd.All() {
					fonts = append(fonts, pdf.Entry{Key: fk, Val: x.im.Map(fv)})
					have[fk] = true
				}
			case "NeedAppearances":
				need, _ = x.d.Resolve(v).Bool()
			default:
				entries = append(entries, pdf.Entry{Key: k, Val: x.im.Map(v)})
			}
		}
	} else if len(from) > 0 {
		// A new form: the default appearance of the first source.
		if da := from[0].form().Get("DA"); !da.IsNull() {
			entries = append(entries, pdf.Entry{Key: "DA", Val: from[0].im.Map(da)})
		}
	}
	for _, x := range from {
		form := x.form()
		dr, _ := x.d.GetDict(form, "DR")
		fd, _ := x.d.GetDict(dr, "Font")
		for fk, fv := range fd.All() {
			if !have[fk] {
				fonts = append(fonts, pdf.Entry{Key: fk, Val: x.im.Map(fv)})
				have[fk] = true
			}
		}
		if n, _ := x.d.Resolve(form.Get("NeedAppearances")).Bool(); n {
			need = true
		}
	}
	fields = append(fields, roots...)
	entries = append(entries, pdf.Entry{Key: "Fields", Val: fields.Object()})
	dr := drOther
	if len(fonts) > 0 {
		dr = append(dr, pdf.Entry{Key: "Font", Val: pdf.NewDict(fonts...).Object()})
	}
	if len(dr) > 0 {
		entries = append(entries, pdf.Entry{Key: "DR", Val: pdf.NewDict(dr...).Object()})
	}
	if need {
		entries = append(entries, pdf.Entry{Key: "NeedAppearances", Val: pdf.Boolean(true)})
	}
	return pdf.NewDict(entries...).Object()
}

// ownCatalog returns the edited document's catalogue as it is written:
// patched, or as the file has it.
func (b *builder) ownCatalog() pdf.Dict {
	x := b.sources[0]
	cat, _ := x.d.Catalog()
	if r, ok := x.d.Trailer().Get("Root").Ref(); ok {
		if p, ok := x.obj(r).Dict(); ok && x.obj(r).Kind() == pdf.KindDict {
			return p
		}
	}
	return cat
}
