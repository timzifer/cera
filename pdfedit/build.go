package pdfedit

import (
	"fmt"
	"io"
	"slices"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// A builder puts the objects of one save of a document together.
type builder struct {
	doc *Document
	w   *pdfwrite.Writer

	// sources are the sources of the document's pages, the base first,
	// then in the order their first page comes.
	sources []*imported
	bySrc   map[*Source]*imported

	catalog, pages, info pdfwrite.Ref
}

// imported is one source in a save: its importer and the output numbers
// of its pages.
type imported struct {
	b    *builder
	d    *pdf.Document
	im   *pdfwrite.Importer
	base bool // the file the document was opened from

	// isPage holds the object numbers of every source page, pageOut the
	// output number of the first copy of each one in the document.
	isPage  map[int32]bool
	pageOut map[int32]pdfwrite.Ref
	// skip holds source objects never copied: the /Encrypt dictionary.
	skip map[int32]bool
}

func newBuilder(doc *Document) *builder {
	b := &builder{doc: doc, w: pdfwrite.New(), bySrc: map[*Source]*imported{}}
	if doc.base != nil {
		b.source(doc.base)
	}
	for _, p := range doc.pages {
		b.source(p.src)
	}
	return b
}

// source returns the import state of s, made when s is first met.
func (b *builder) source(s *Source) *imported {
	if x, ok := b.bySrc[s]; ok {
		return x
	}
	x := &imported{
		b:       b,
		d:       s.d,
		im:      b.w.Import(s.d),
		base:    s == b.doc.base,
		isPage:  map[int32]bool{},
		pageOut: map[int32]pdfwrite.Ref{},
		skip:    map[int32]bool{},
	}
	x.im.MapRef = x.ref
	if !x.base {
		// /Parent leads up a tree the output does not have: the page
		// tree, a field's parents. Only the links pdfedit makes itself
		// stay.
		x.im.Drop = map[pdf.Name]bool{"Parent": true}
	}
	for i := range s.d.PageCount() {
		if r, ok := s.d.PageRef(i + 1); ok {
			x.isPage[r.Num] = true
		}
	}
	if r, ok := s.d.Trailer().Get("Encrypt").Ref(); ok {
		x.skip[r.Num] = true
	}
	b.bySrc[s] = x
	b.sources = append(b.sources, x)
	return x
}

// ref returns the output number of a source object, numbering it for
// copying when it is first met, or 0 when the reference is written as
// null: a page that is not in the document, a page tree node, a missing
// object. The base's catalogue is the output's.
func (x *imported) ref(r pdf.Ref) pdfwrite.Ref {
	if r.Num <= 0 || x.skip[r.Num] {
		return 0
	}
	if n, ok := x.pageOut[r.Num]; ok {
		return n
	}
	if x.isPage[r.Num] {
		return 0
	}
	if n, ok := x.im.Copied(r); ok {
		return n
	}
	o, err := x.d.Get(r)
	if err != nil || o.IsNull() {
		return 0
	}
	if d, ok := o.Dict(); ok {
		// Reached other than through /Parent: still not part of a page.
		switch t, _ := d.Get("Type").Name(); t {
		case "Catalog":
			if x.base {
				return x.b.catalog
			}
			return 0
		case "Page", "Pages", "XRef", "ObjStm":
			return 0
		}
	}
	return x.im.Copy(r)
}

// build makes the catalogue, the page tree and the pages.
func (b *builder) build() error {
	b.catalog = b.w.Alloc() // filled in below
	b.pages = b.w.Alloc()
	out := make([]pdfwrite.Ref, len(b.doc.pages))
	kids := make(pdf.Array, len(out))
	for i, p := range b.doc.pages {
		out[i] = b.w.Alloc()
		kids[i] = out[i].Object()
		x := b.bySrc[p.src]
		r, _ := x.d.PageRef(p.index + 1)
		if _, ok := x.pageOut[r.Num]; !ok {
			x.pageOut[r.Num] = out[i]
		}
	}
	// Pages are made once every page has its number, so links between
	// them can point at the copies.
	for i, p := range b.doc.pages {
		dict, err := b.bySrc[p.src].page(p)
		if err != nil {
			return err
		}
		b.w.Set(out[i], dict.Object())
	}

	b.w.Set(b.pages, pdf.NewDict(
		pdf.Entry{Key: "Type", Val: pdf.Name("Pages").Object()},
		pdf.Entry{Key: "Kids", Val: kids.Object()},
		pdf.Entry{Key: "Count", Val: pdf.Integer(int64(len(out)))},
	).Object())

	cat, err := b.catalogDict()
	if err != nil {
		return err
	}
	b.w.Set(b.catalog, cat.Object())

	// The information dictionary is the base's, or the first source's.
	x := b.sources[0]
	info := x.d.Trailer().Get("Info")
	if r, ok := info.Ref(); ok {
		b.info = x.ref(r)
	} else if info.Kind() == pdf.KindDict {
		b.info = b.w.Add(x.im.Map(info))
	}
	b.w.Version = b.version()
	return nil
}

// catalogDict makes the output's catalogue: the base's with a new page
// tree, or a new one with the language of the first source that has one.
// The optional content properties of every source are merged.
func (b *builder) catalogDict() (pdf.Dict, error) {
	entries := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("Catalog").Object()},
		{Key: "Pages", Val: b.pages.Object()},
	}
	if x := b.sources[0]; x.base {
		cat, err := x.d.Catalog()
		if err != nil {
			return pdf.Dict{}, fmt.Errorf("pdfedit: %w", err)
		}
		for k, v := range cat.All() {
			switch k {
			case "Type", "Pages", "OCProperties":
				continue
			}
			entries = append(entries, pdf.Entry{Key: k, Val: x.im.Map(v)})
		}
	} else {
		for _, x := range b.sources {
			cat, err := x.d.Catalog()
			if err != nil {
				return pdf.Dict{}, fmt.Errorf("pdfedit: %w", err)
			}
			if v := cat.Get("Lang"); !v.IsNull() {
				entries = append(entries, pdf.Entry{Key: "Lang", Val: x.im.Map(v)})
				break
			}
		}
	}
	if oc := b.ocProperties(); !oc.IsNull() {
		entries = append(entries, pdf.Entry{Key: "OCProperties", Val: oc})
	}
	return pdf.NewDict(entries...), nil
}

// version returns the newest version of the sources, by their headers and
// catalogues, and at least 1.7.
func (b *builder) version() string {
	v := "1.7"
	for _, x := range b.sources {
		if h := x.d.Version(); validVersion(h) && h > v {
			v = h
		}
		cat, _ := x.d.Catalog()
		if n, ok := x.d.Resolve(cat.Get("Version")).Name(); ok && validVersion(string(n)) && string(n) > v {
			v = string(n)
		}
	}
	return v
}

// validVersion reports whether v reads as a PDF version, "1.4" or "2.0".
func validVersion(v string) bool {
	return len(v) == 3 && v[0] >= '1' && v[0] <= '9' && v[1] == '.' && v[2] >= '0' && v[2] <= '9'
}

// write writes the output.
func (b *builder) write(w io.Writer) error {
	t := pdfwrite.Trailer{Root: b.catalog, Info: b.info}
	if x := b.sources[0]; x.base {
		// The document stands for the file it was opened from.
		if id, ok := x.d.Resolve(x.d.Trailer().Get("ID")).Array(); ok && len(id) == 2 {
			if s, ok := x.d.Resolve(id[0]).Str(); ok && len(s) > 0 {
				t.ID = s
			}
		}
	}
	return b.w.Write(w, t)
}

// Page attributes that are not copied: the page tree is new and the
// rotation rewritten. For a page that is imported alone the annotations
// are rewritten too, and beads, thumbnails and the structure tree's key
// are dropped.
var (
	pageDropped     = map[pdf.Name]bool{"Type": true, "Parent": true, "Rotate": true}
	importedDropped = map[pdf.Name]bool{
		"Type": true, "Parent": true, "Rotate": true, "Annots": true,
		"B": true, "Thumb": true, "StructParents": true,
	}
)

// page makes the dictionary of one output page.
func (x *imported) page(p docPage) (pdf.Dict, error) {
	src, err := x.d.Page(p.index + 1) // with the inherited attributes
	if err != nil {
		return pdf.Dict{}, fmt.Errorf("pdfedit: page %d: %w", p.index, err)
	}
	entries := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("Page").Object()},
		{Key: "Parent", Val: x.b.pages.Object()},
	}
	dropped := importedDropped
	if x.base {
		dropped = pageDropped
	}
	for k, v := range src.All() {
		if !dropped[k] {
			entries = append(entries, pdf.Entry{Key: k, Val: x.im.Map(v)})
		}
	}
	if rot := rotation(x.d.Resolve(src.Get("Rotate")), p.rotate); rot != 0 {
		entries = append(entries, pdf.Entry{Key: "Rotate", Val: pdf.Integer(int64(rot))})
	}
	if !x.base {
		if annots := x.annots(src.Get("Annots")); len(annots) > 0 {
			entries = append(entries, pdf.Entry{Key: "Annots", Val: annots.Object()})
		}
	}
	return pdf.NewDict(entries...), nil
}

// ocProperties returns the optional content properties of the output: the
// one source's that has them as they are, or those of several merged. The
// groups of all are listed; the default configuration is the first one's,
// with the groups switched on and off, the order, the radio button groups
// and the locked groups of every source. Alternate configurations are
// left out when merging.
func (b *builder) ocProperties() pdf.Object {
	var with []*imported
	for _, x := range b.sources {
		cat, _ := x.d.Catalog()
		if _, ok := x.d.GetDict(cat, "OCProperties"); ok {
			with = append(with, x)
		}
	}
	switch len(with) {
	case 0:
		return pdf.Null
	case 1:
		cat, _ := with[0].d.Catalog()
		return with[0].im.Map(cat.Get("OCProperties"))
	}
	var ocgs pdf.Array
	seen := map[pdf.Object]bool{}
	var d []pdf.Entry
	lists := map[pdf.Name]pdf.Array{}
	listKeys := []pdf.Name{"ON", "OFF", "Order", "RBGroups", "Locked"}
	for i, x := range with {
		cat, _ := x.d.Catalog()
		props, _ := x.d.GetDict(cat, "OCProperties")
		groups, _ := x.d.Resolve(props.Get("OCGs")).Array()
		for _, g := range groups {
			if o := x.im.Map(g); o.Kind() == pdf.KindRef && !seen[o] {
				seen[o] = true
				ocgs = append(ocgs, o)
			}
		}
		def, _ := x.d.GetDict(props, "D")
		for _, k := range listKeys {
			a, _ := x.d.Resolve(def.Get(k)).Array()
			for _, e := range a {
				o := x.im.Map(e)
				if o.IsNull() {
					continue
				}
				lists[k] = append(lists[k], o)
			}
		}
		if i == 0 {
			for k, v := range def.All() {
				if !slices.Contains(listKeys, k) {
					d = append(d, pdf.Entry{Key: k, Val: x.im.Map(v)})
				}
			}
		}
	}
	for _, k := range listKeys {
		if a, ok := lists[k]; ok {
			d = append(d, pdf.Entry{Key: k, Val: a.Object()})
		}
	}
	return pdf.NewDict(
		pdf.Entry{Key: "OCGs", Val: ocgs.Object()},
		pdf.Entry{Key: "D", Val: pdf.NewDict(d...).Object()},
	).Object()
}
