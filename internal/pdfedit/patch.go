package pdfedit

import (
	"github.com/timzifer/cera/internal/pdf"
)

// Patch changes an object of the edited document: the entries of Set are
// set, those named in Del removed. Values are in the edited document's
// space.
type Patch struct {
	Ref pdf.Ref
	Set []pdf.Entry
	Del []pdf.Name
}

// patches applies the patches of doc, and /NeedAppearances, to the
// dictionaries of the edited document they change. It returns them by
// object number, and the interactive form to write into the catalogue
// when it is a direct object there and changes.
func patches(doc Doc) (map[int32]pdf.Object, pdf.Object) {
	d := doc.Base
	out := map[int32]pdf.Object{}
	dict := func(r pdf.Ref) (pdf.Dict, bool) {
		if o, ok := out[r.Num]; ok {
			return o.Dict()
		}
		o, err := d.Get(r)
		if err != nil {
			return pdf.Dict{}, false
		}
		dd, ok := o.Dict()
		return dd, ok && o.Kind() == pdf.KindDict
	}
	for _, p := range doc.Patches {
		dd, ok := dict(p.Ref)
		if !ok {
			continue
		}
		out[p.Ref.Num] = apply(dd, p).Object()
	}
	if !doc.NeedAppearances {
		return out, pdf.Null
	}
	cat, err := d.Catalog()
	if err != nil {
		return out, pdf.Null
	}
	set := Patch{Set: []pdf.Entry{{Key: "NeedAppearances", Val: pdf.Boolean(true)}}}
	if r, ok := cat.Get("AcroForm").Ref(); ok {
		if dd, ok := dict(r); ok {
			out[r.Num] = apply(dd, set).Object()
		}
		return out, pdf.Null
	}
	if dd, ok := cat.Get("AcroForm").Dict(); ok {
		return out, apply(dd, set).Object()
	}
	return out, pdf.Null
}

// apply returns d changed by p.
func apply(d pdf.Dict, p Patch) pdf.Dict {
	del := map[pdf.Name]bool{}
	for _, k := range p.Del {
		del[k] = true
	}
	for _, e := range p.Set {
		del[e.Key] = true
	}
	entries := make([]pdf.Entry, 0, d.Len()+len(p.Set))
	for k, v := range d.All() {
		if !del[k] {
			entries = append(entries, pdf.Entry{Key: k, Val: v})
		}
	}
	return pdf.NewDict(append(entries, p.Set...)...)
}
