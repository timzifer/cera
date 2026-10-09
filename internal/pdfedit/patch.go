package pdfedit

import (
	"math"

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

// newGen marks the references of patches to Doc.Objects. No file has
// it: the reader never makes a negative generation but -1.
const newGen = math.MinInt32

// NewRef returns the reference that stands for Doc.Objects[i] in patches
// and in other new objects.
func NewRef(i int) pdf.Ref { return pdf.Ref{Num: int32(i), Gen: newGen} }

// patches applies the patches of doc to the dictionaries of the edited
// document they change, and returns them by object number.
func patches(doc Doc) map[int32]pdf.Object {
	d := doc.Base
	out := map[int32]pdf.Object{}
	for _, p := range doc.Patches {
		var dd pdf.Dict
		if o, ok := out[p.Ref.Num]; ok {
			dd, _ = o.Dict()
		} else if o, err := d.Get(p.Ref); err == nil && o.Kind() == pdf.KindDict {
			dd, _ = o.Dict()
		} else {
			continue
		}
		out[p.Ref.Num] = apply(dd, p).Object()
	}
	return out
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
