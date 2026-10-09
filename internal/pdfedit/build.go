// Package pdfedit puts edited documents together: the page tree, the
// catalogue, pages of the edited document with everything around them and
// pages imported from others alone (ADR 0013). It works on the reader's
// documents and writes through pdfwrite; cera's Editor is its API.
package pdfedit

import (
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// Page is one page of the output: page Index of Src, turned by Rotate more
// degrees (a multiple of 90). An own page is a page of the edited document,
// kept with everything around it; it appears at most once. Any other page
// is imported alone, from any document, the edited one included.
type Page struct {
	Src    *pdf.Document
	Index  int
	Rotate int
	Own    bool
}

// Doc is what is written: the pages of the edited document Base (nil for
// a new one) and others, in order.
type Doc struct {
	Base  *pdf.Document
	Pages []Page
	// Patches change objects of the edited document. Objects are new
	// ones, in its space; patches and they refer to them by NewRef.
	Patches []Patch
	Objects []pdf.Object
}

// MayAssemble reports whether the permissions of d allow reassembling it
// (bit 11 of /P), which writing it decrypted and importing its pages is.
func MayAssemble(d *pdf.Document) bool {
	prot, ok := d.Protection()
	return !ok || prot.Owner || prot.Permissions&pdf.PermAssemble != 0
}

// Write writes doc to w as a new file. Nothing is written when it fails,
// but for an error of w itself.
func Write(w io.Writer, doc Doc) error {
	if len(doc.Pages) == 0 {
		return errors.New("the document has no pages")
	}
	b := newBuilder(doc, nil)
	if err := b.build(); err != nil {
		return err
	}
	return b.write(w)
}

// Errors of Update.
var (
	ErrNoBase    = errors.New("a new document has no file to update")
	ErrRepaired  = errors.New("the file needed a repair")
	ErrEncrypted = errors.New("the file is encrypted")
)

// Update writes the file of doc.Base to w and appends to it what makes it
// doc (PDF 2.0, 7.5.6): the pages of the edited document keep their
// objects and numbers, deleted ones and what hung on them alone are freed,
// so that references to them read as null, and imported pages are copied
// in. A document whose pages are unchanged is written as it is. Nothing is
// written when it fails, but for an error of w itself.
func Update(w io.Writer, doc Doc) error {
	base := doc.Base
	if base == nil {
		return ErrNoBase
	}
	ub, ok := base.UpdateBase()
	if !ok {
		return ErrRepaired
	}
	if base.Encrypted() {
		return ErrEncrypted
	}
	if len(doc.Pages) == 0 {
		return errors.New("the document has no pages")
	}
	if unchanged(doc) && len(doc.Patches) == 0 {
		return base.WriteSource(w)
	}
	b := newBuilder(doc, &ub)
	if err := b.build(); err != nil {
		return err
	}
	return b.write(w)
}

// unchanged reports whether doc has the pages of its base, in their order
// and turned as they are.
func unchanged(doc Doc) bool {
	if len(doc.Pages) != doc.Base.PageCount() {
		return false
	}
	for i, p := range doc.Pages {
		if !p.Own || p.Index != i || p.Rotate%360 != 0 {
			return false
		}
	}
	return true
}

// A builder puts the objects of one output together.
type builder struct {
	doc Doc
	w   *pdfwrite.Writer
	// upd describes the edited file for an incremental update; nil when
	// a new file is written.
	upd *pdf.UpdateBase
	// patched holds the changed objects of the edited document, newOut
	// the output numbers of its new objects once referred to.
	patched map[int32]pdf.Object
	newOut  map[int32]pdfwrite.Ref

	// sources are the sources of the pages, the base first, then in the
	// order their first page comes, by document and whether their pages
	// are own.
	sources []*source
	bySrc   map[srcKey]*source

	catalog, pages, info pdfwrite.Ref
}

type srcKey struct {
	d   *pdf.Document
	own bool
}

// source is one source in an output: its importer and the output numbers
// of its pages.
type source struct {
	b   *builder
	d   *pdf.Document
	im  *pdfwrite.Importer
	own bool
	// shared is, for pages imported from the edited document, the source
	// of its own pages: what they share with them (fonts, images, layers)
	// is copied once.
	shared *source

	// isPage holds the object numbers of every source page, pageOut the
	// output number of the first copy of each one in the output.
	isPage  map[int32]bool
	pageOut map[int32]pdfwrite.Ref
	// skip holds source objects never copied: the /Encrypt dictionary,
	// and for own pages what hangs on deleted ones alone.
	skip map[int32]bool
}

func newBuilder(doc Doc, upd *pdf.UpdateBase) *builder {
	b := &builder{doc: doc, w: pdfwrite.New(), upd: upd, bySrc: map[srcKey]*source{}}
	if upd != nil {
		b.w = pdfwrite.NewUpdate(pdfwrite.Base{
			Size: upd.Size, Prev: upd.Startxref, Next: upd.Next, Stream: upd.XrefStream,
			Gen: doc.Base.Generation,
		})
	}
	if doc.Base != nil {
		b.patched, b.newOut = patches(doc), map[int32]pdfwrite.Ref{}
		b.source(doc.Base, true)
	}
	for _, p := range doc.Pages {
		b.source(p.Src, p.Own)
	}
	return b
}

// source returns the import state of d, made when it is first met.
func (b *builder) source(d *pdf.Document, own bool) *source {
	k := srcKey{d, own}
	if x, ok := b.bySrc[k]; ok {
		return x
	}
	x := &source{
		b:       b,
		d:       d,
		im:      b.w.Import(d),
		own:     own,
		isPage:  map[int32]bool{},
		pageOut: map[int32]pdfwrite.Ref{},
		skip:    map[int32]bool{},
	}
	x.im.MapRef = x.ref
	if own {
		x.im.Patch = b.patched
		// Fields of deleted pages leave holes in the lists of fields.
		x.im.Compact = map[pdf.Name]bool{"Fields": true, "Kids": true, "CO": true, "Annots": true}
	} else {
		// /Parent leads up a tree the output does not have: the page
		// tree, a field's parents. Only the links the builder makes
		// itself stay.
		x.im.Drop = map[pdf.Name]bool{"Parent": true}
		x.shared = b.bySrc[srcKey{d, true}]
	}
	for i := range d.PageCount() {
		if r, ok := d.PageRef(i + 1); ok {
			x.isPage[r.Num] = true
		}
	}
	if r, ok := d.Trailer().Get("Encrypt").Ref(); ok {
		x.skip[r.Num] = true
	}
	b.bySrc[k] = x
	b.sources = append(b.sources, x)
	return x
}

// ref returns the output number of a source object, numbering it for
// copying when it is first met, or 0 when the reference is written as
// null: a page that is not in the output, a page tree node, a missing
// object. The edited document's catalogue is the output's.
func (x *source) ref(r pdf.Ref) pdfwrite.Ref {
	if r.Gen == newGen && x.own {
		return x.newObject(r.Num)
	}
	if r.Num <= 0 || x.skip[r.Num] {
		return 0
	}
	if x.own && x.b.upd != nil {
		// An update keeps the edited file's objects where they are.
		if r.Num >= x.b.upd.Next {
			return 0
		}
		return pdfwrite.Ref(r.Num)
	}
	if n, ok := x.pageOut[r.Num]; ok {
		return n
	}
	if x.shared != nil {
		if n := x.shared.ref(r); n != x.b.catalog {
			return n
		}
		return 0
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
			if x.own {
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
	if b.upd != nil && unchanged(b.doc) {
		return b.buildPatches()
	}
	if b.upd != nil {
		r, ok := b.doc.Base.Trailer().Get("Root").Ref()
		if !ok {
			return ErrRepaired
		}
		b.catalog = pdfwrite.Ref(r.Num) // replaced below
	} else {
		b.catalog = b.w.Alloc() // filled in below
	}
	b.pages = b.w.Alloc()
	out := make([]pdfwrite.Ref, len(b.doc.Pages))
	kids := make(pdf.Array, len(out))
	for i, p := range b.doc.Pages {
		x := b.bySrc[srcKey{p.Src, p.Own}]
		r, ok := x.d.PageRef(p.Index + 1)
		if b.upd != nil && p.Own && ok && r.Num > 0 {
			out[i] = pdfwrite.Ref(r.Num) // replaced below
		} else {
			out[i] = b.w.Alloc()
		}
		kids[i] = out[i].Object()
		if _, ok := x.pageOut[r.Num]; !ok {
			x.pageOut[r.Num] = out[i]
		}
	}
	if b.doc.Base != nil {
		x := b.bySrc[srcKey{b.doc.Base, true}]
		x.pruneDeleted()
		if b.upd != nil {
			x.setPatched()
			x.freeDeleted()
		}
	}
	// Pages are made once every page has its number, so links between
	// them can point at the copies.
	for i, p := range b.doc.Pages {
		dict, err := b.bySrc[srcKey{p.Src, p.Own}].page(p)
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

	// The information dictionary is the edited document's, or the first
	// source's.
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

// freeDeleted frees, in an update, the deleted pages of the edited file
// and what hung on them alone, and replaces what listed them: the form
// and the fields that keep some of their kids.
func (x *source) freeDeleted() {
	for i := range x.d.PageCount() {
		if r, ok := x.d.PageRef(i + 1); ok {
			if _, kept := x.pageOut[r.Num]; !kept {
				x.b.w.Free(r.Num)
			}
		}
	}
	if len(x.skip) == 0 {
		return
	}
	for n := range x.skip {
		x.b.w.Free(n)
	}
	cat, _ := x.d.Catalog()
	if r, ok := cat.Get("AcroForm").Ref(); ok {
		x.b.w.Set(pdfwrite.Ref(r.Num), x.im.Map(x.obj(r)))
	}
	form, _ := x.d.GetDict(cat, "AcroForm")
	fields, _ := x.d.Resolve(form.Get("Fields")).Array()
	seen := map[int32]bool{}
	var walk func(o pdf.Object)
	walk = func(o pdf.Object) {
		r, ok := o.Ref()
		if !ok || x.skip[r.Num] || seen[r.Num] || len(seen) >= maxFieldNodes {
			return
		}
		seen[r.Num] = true
		node, _ := x.d.Resolve(o).Dict()
		kids, _ := x.d.Resolve(node.Get("Kids")).Array()
		lost := false
		for _, k := range kids {
			if kr, ok := k.Ref(); ok && x.skip[kr.Num] {
				lost = true
			}
			walk(k)
		}
		if lost {
			x.b.w.Set(pdfwrite.Ref(r.Num), x.im.Map(x.obj(r)))
		}
	}
	for _, f := range fields {
		walk(f)
	}
}

// maxFieldNodes bounds the walk of a form's field tree.
const maxFieldNodes = 1 << 16

// pruneDeleted marks what hangs on deleted pages of the edited document
// alone as not to be copied: their annotations, and form fields whose
// widgets were all on them.
func (x *source) pruneDeleted() {
	dead := map[int32]bool{}
	var kept []pdf.Dict
	for i := range x.d.PageCount() {
		r, ok := x.d.PageRef(i + 1)
		if !ok {
			continue
		}
		page, err := x.d.Page(i + 1)
		if err != nil {
			continue
		}
		if _, ok := x.pageOut[r.Num]; ok {
			kept = append(kept, page)
			continue
		}
		annots, _ := x.d.Resolve(page.Get("Annots")).Array()
		for _, a := range annots {
			if ar, ok := a.Ref(); ok {
				dead[ar.Num] = true
			}
		}
	}
	if len(dead) == 0 {
		return
	}
	// A kept page may list an annotation of a deleted one too.
	for _, page := range kept {
		annots, _ := x.d.Resolve(page.Get("Annots")).Array()
		for _, a := range annots {
			if ar, ok := a.Ref(); ok {
				delete(dead, ar.Num)
			}
		}
	}
	for n := range dead {
		x.skip[n] = true
	}
	// A field dies with all its kids.
	cat, _ := x.d.Catalog()
	form, _ := x.d.GetDict(cat, "AcroForm")
	fields, _ := x.d.Resolve(form.Get("Fields")).Array()
	visits := 0
	seen := map[int32]bool{}
	var walk func(o pdf.Object) bool // reports whether o is dead
	walk = func(o pdf.Object) bool {
		r, ok := o.Ref()
		if !ok {
			return false
		}
		if x.skip[r.Num] {
			return true
		}
		if seen[r.Num] || visits >= maxFieldNodes {
			return false
		}
		seen[r.Num] = true
		visits++
		node, _ := x.d.Resolve(o).Dict()
		kids, _ := x.d.Resolve(node.Get("Kids")).Array()
		if len(kids) == 0 {
			return false
		}
		all := true
		for _, k := range kids {
			if !walk(k) {
				all = false
			}
		}
		if all {
			x.skip[r.Num] = true
		}
		return all
	}
	for _, f := range fields {
		walk(f)
	}
}

// catalogDict makes the output's catalogue: the edited document's with a
// new page tree, or a new one with the language of the first source that
// has one. The optional content properties of every source are merged.
func (b *builder) catalogDict() (pdf.Dict, error) {
	entries := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("Catalog").Object()},
		{Key: "Pages", Val: b.pages.Object()},
	}
	if x := b.sources[0]; x.own {
		cat, err := x.d.Catalog()
		if err != nil {
			return pdf.Dict{}, err
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
				return pdf.Dict{}, err
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
	if b.upd != nil {
		// An update keeps the header: a newer version goes into the
		// catalogue.
		if v := b.version(); v > b.doc.Base.Version() {
			entries = append(entries, pdf.Entry{Key: "Version", Val: pdf.Name(v).Object()})
		}
	}
	return pdf.NewDict(entries...), nil
}

// version returns the newest version of the sources, by their headers and
// catalogues, and at least 1.7 for a new file.
func (b *builder) version() string {
	v := "1.7"
	if b.upd != nil {
		v = "1.0"
	}
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
	if b.upd != nil {
		if err := b.doc.Base.WriteSource(w); err != nil {
			return err
		}
	}
	if x := b.sources[0]; x.own {
		// The output stands for the edited file.
		if id, ok := x.d.Resolve(x.d.Trailer().Get("ID")).Array(); ok && len(id) == 2 {
			if s, ok := x.d.Resolve(id[0]).Str(); ok && len(s) > 0 {
				t.ID = s
			}
		}
	}
	return b.w.Write(w, t)
}

// Page attributes that are not copied: the page tree is new and the
// rotation rewritten. For an imported page the annotations are rewritten
// too, and beads, thumbnails and the structure tree's key are dropped.
var (
	ownDropped      = map[pdf.Name]bool{"Type": true, "Parent": true, "Rotate": true}
	importedDropped = map[pdf.Name]bool{
		"Type": true, "Parent": true, "Rotate": true, "Annots": true,
		"B": true, "Thumb": true, "StructParents": true,
	}
)

// page makes the dictionary of one output page.
func (x *source) page(p Page) (pdf.Dict, error) {
	src, err := x.d.Page(p.Index + 1) // with the inherited attributes
	if err != nil {
		return pdf.Dict{}, fmt.Errorf("page %d: %w", p.Index, err)
	}
	entries := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("Page").Object()},
		{Key: "Parent", Val: x.b.pages.Object()},
	}
	dropped := importedDropped
	if x.own {
		dropped = ownDropped
	}
	for k, v := range src.All() {
		if dropped[k] {
			continue
		}
		if k == "Annots" {
			// Written in place, without the annotations of deleted
			// pages.
			v = x.d.Resolve(v)
		}
		entries = append(entries, pdf.Entry{Key: k, Val: x.im.Map(v)})
	}
	if rot := Rotation(x.d.Resolve(src.Get("Rotate")), p.Rotate); rot != 0 {
		entries = append(entries, pdf.Entry{Key: "Rotate", Val: pdf.Integer(int64(rot))})
	}
	if !x.own {
		if annots := x.annots(src.Get("Annots")); len(annots) > 0 {
			entries = append(entries, pdf.Entry{Key: "Annots", Val: annots.Object()})
		}
	}
	return pdf.NewDict(entries...), nil
}

// ocProperties returns the optional content properties of the output: the
// one source document's that has them as they are, or those of several
// merged. The groups of all are listed; the default configuration is the
// first one's, with the groups switched on and off, the order, the radio
// button groups and the locked groups of every document. Alternate
// configurations are left out when merging. Pages imported from the edited
// document share its groups.
func (b *builder) ocProperties() pdf.Object {
	var with []*source
	done := map[*pdf.Document]bool{}
	for _, x := range b.sources {
		if done[x.d] {
			continue
		}
		cat, _ := x.d.Catalog()
		if _, ok := x.d.GetDict(cat, "OCProperties"); ok {
			with = append(with, x)
			done[x.d] = true
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

// obj returns object r of the edited document as it is to be written:
// patched, or as the file has it.
func (x *source) obj(r pdf.Ref) pdf.Object {
	if o, ok := x.b.patched[r.Num]; ok {
		return o
	}
	return x.d.Resolve(r.Object())
}

// setPatched replaces, in an update, the objects of the edited document
// that are patched.
func (x *source) setPatched() {
	for num, o := range x.b.patched {
		if !x.skip[num] {
			x.b.w.Set(pdfwrite.Ref(num), x.im.Map(o))
		}
	}
}

// buildPatches makes an update of a document whose pages are unchanged:
// the patched objects and the new ones alone.
func (b *builder) buildPatches() error {
	x := b.sources[0]
	r, ok := x.d.Trailer().Get("Root").Ref()
	if !ok {
		return ErrRepaired
	}
	b.catalog = pdfwrite.Ref(r.Num)
	if info, ok := x.d.Trailer().Get("Info").Ref(); ok {
		b.info = x.ref(info)
	}
	x.setPatched()
	return nil
}

// newObject returns the output number of new object i, numbering it when
// it is first referred to: new objects nothing refers to are not written.
func (x *source) newObject(i int32) pdfwrite.Ref {
	objs := x.b.doc.Objects
	if i < 0 || int(i) >= len(objs) {
		return 0
	}
	if n, ok := x.b.newOut[i]; ok {
		return n
	}
	n := x.b.w.Alloc()
	x.b.newOut[i] = n
	x.b.w.Set(n, x.im.Map(objs[i]))
	return n
}
