package pdfedit

import (
	"bytes"
	"fmt"
	"maps"
	"slices"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// The structure tree imported pages bring (PDF 2.0, 14.7): the elements of
// their document whose content is on them, with the elements above them,
// joined to the tree of the output. The keys by which pages, annotations
// and form XObjects find their elements in the parent tree are numbered
// anew, after those the output has.
//
// The tree of the output is the edited document's; where it has none, the
// first source that brings one gives it, as its own. Every further source
// adds a /Part under the output's /Document element, or under its root
// when it has no single one: the source's own /Document element turned
// into a /Part, or one made around its elements.

// Limits of what is read of a structure tree.
const (
	maxStructNodes = 1 << 18
	maxStructDepth = 64
	maxParentKeys  = 1 << 20
	maxRoleChain   = 16
	maxFormWalk    = 1 << 12
)

// structMerge is the structure tree of the output being put together.
type structMerge struct {
	// next is the next key of the output's parent tree, parents the
	// entries imported objects add to it.
	next    int64
	parents []parentEntry
	// base is the edited document's tree, when it has one: its root, the
	// root's dictionary and its single /Document element (zero for none).
	base     *source
	baseRoot pdf.Ref
	baseDict pdf.Dict
	baseDoc  pdf.Ref
	// lead is the source whose tree is the output's where the edited
	// document has none; root is the output root it is written to.
	lead *source
	root pdfwrite.Ref

	// What the output's root says, in the output's space: role map,
	// class map, identifiers and namespaces.
	roles   map[pdf.Name]pdf.Name
	classes map[pdf.Name]pdf.Object
	classOf map[pdf.Name]classDef
	ids     map[string]pdf.Object
	ns      pdf.Array
	nsSeen  map[pdf.Object]bool
}

// parentEntry is an entry of the output's parent tree that an imported
// object brings: the source's value for it, in the source's space.
type parentEntry struct {
	key int64
	x   *source
	val pdf.Object
}

// classDef is an attribute class of the output, with the document whose
// space its definition is in, to compare others with.
type classDef struct {
	d   *pdf.Document
	val pdf.Object
}

// srcStruct is the structure tree of one source.
type srcStruct struct {
	root    pdf.Dict
	parents map[int64]pdf.Object // the source's parent tree
	// tree holds every object of the tree that was read: elements, the
	// root, nodes of its parent tree. out holds the output numbers of the
	// elements that are kept; the others are written as null.
	tree map[int32]bool
	out  map[int32]pdfwrite.Ref
	// pageKey and formKey are the output keys of the first copy of a
	// source page and of a form XObject; annotOut the first copies of
	// annotations.
	pageKey  map[int32]int64
	formKey  map[int32]int64
	annotOut map[int32]pdfwrite.Ref
	// forms holds the form XObjects looked at, formN counts them.
	forms map[int32]bool
	formN int
	// roles and classes rename the structure types and attribute classes
	// the output has otherwise.
	roles   map[pdf.Name]pdf.Name
	classes map[pdf.Name]pdf.Name
}

// snode is an element that is kept, with the parts of it that are.
type snode struct {
	x    *source
	dict pdf.Dict
	out  pdfwrite.Ref
	kids []skid
	// pg is the page of its content where it and its kids name none.
	pg pdf.Object
}

// skid is a kept part of an element: an element, or content in the
// source's space (a marked-content identifier, a marked-content or object
// reference).
type skid struct {
	elem *snode
	obj  pdf.Object
}

// structured returns the sources whose structure tree is brought.
func (b *builder) structured() []*source {
	var out []*source
	for _, x := range b.sources {
		if !x.own && x.shared == nil && b.doc.Imports[x.d].Structure {
			out = append(out, x)
		}
	}
	return out
}

// prepareStructure reads the structure trees imported pages bring and
// numbers the keys of the pages and form XObjects that are written with
// them, before the pages are made.
func (b *builder) prepareStructure() {
	srcs := b.structured()
	if len(srcs) == 0 {
		return
	}
	st := &structMerge{}
	if own := b.own(); own != nil {
		cat := own.catalog()
		if r, ok := cat.Get("StructTreeRoot").Ref(); ok {
			if root, ok := own.obj(r).Dict(); ok {
				st.base, st.baseRoot, st.baseDict = own, r, root
				parents := readNumberTree(own.d, root.Get("ParentTree"))
				for k := range parents {
					st.next = max(st.next, k+1)
				}
				if n, ok := own.d.Resolve(root.Get("ParentTreeNextKey")).Int(); ok && n > st.next && n < 1<<31 {
					st.next = n
				}
			}
		}
	}
	for _, x := range srcs {
		cat, err := x.d.Catalog()
		if err != nil {
			continue
		}
		root, ok := x.d.GetDict(cat, "StructTreeRoot")
		if !ok {
			continue
		}
		x.st = &srcStruct{
			root:     root,
			parents:  readNumberTree(x.d, root.Get("ParentTree")),
			tree:     map[int32]bool{},
			out:      map[int32]pdfwrite.Ref{},
			pageKey:  map[int32]int64{},
			formKey:  map[int32]int64{},
			annotOut: map[int32]pdfwrite.Ref{},
			forms:    map[int32]bool{},
			roles:    map[pdf.Name]pdf.Name{},
			classes:  map[pdf.Name]pdf.Name{},
		}
		if r, ok := cat.Get("StructTreeRoot").Ref(); ok {
			x.st.tree[r.Num] = true
		}
		if st.base == nil && st.lead == nil {
			st.lead = x
		}
	}
	if st.base == nil && st.lead == nil {
		return
	}
	b.st = st
	// The pages first, in their order, then the forms they draw.
	for i, p := range b.doc.Pages {
		if p.Src == nil || p.Own {
			continue
		}
		x := b.bySrc[srcKey{p.Src, false}]
		if x.st == nil {
			continue
		}
		r, ok := x.d.PageRef(p.Index + 1)
		if !ok || x.pageOut[r.Num] != b.pageRefs[i] {
			continue // not the first copy
		}
		page, err := x.d.Page(p.Index + 1)
		if err != nil {
			continue
		}
		if k, ok := x.d.Resolve(page.Get("StructParents")).Int(); ok {
			if nk, ok := x.structKey(k); ok {
				x.st.pageKey[r.Num] = nk
			}
		}
	}
	for i, p := range b.doc.Pages {
		if p.Src == nil || p.Own {
			continue
		}
		x := b.bySrc[srcKey{p.Src, false}]
		if x.st == nil {
			continue
		}
		if r, ok := x.d.PageRef(p.Index + 1); ok && x.pageOut[r.Num] == b.pageRefs[i] {
			page, _ := x.d.Page(p.Index + 1)
			x.structForms(page.Get("Resources"))
		}
	}
}

// catalog returns the catalogue of the source as it is written.
func (x *source) catalog() pdf.Dict {
	cat, _ := x.d.Catalog()
	if x.own {
		if r, ok := x.d.Trailer().Get("Root").Ref(); ok {
			if p, ok := x.obj(r).Dict(); ok {
				cat = p
			}
		}
	}
	return cat
}

// structKey returns a new key of the output's parent tree for key k of
// the source's, false when the source's tree has no entry for it.
func (x *source) structKey(k int64) (int64, bool) {
	v, ok := x.st.parents[k]
	if !ok {
		return 0, false
	}
	st := x.b.st
	nk := st.next
	st.next++
	st.parents = append(st.parents, parentEntry{key: nk, x: x, val: v})
	return nk, true
}

// structForms numbers the keys of the form XObjects that resources res
// name, and those they name in turn, and gives the copies their keys. A
// form is copied once for all pages, and given one key.
func (x *source) structForms(res pdf.Object) {
	rd, _ := x.d.Resolve(res).Dict()
	xobjs, _ := x.d.Resolve(rd.Get("XObject")).Dict()
	for _, v := range xobjs.All() {
		r, ok := v.Ref()
		if !ok || x.st.forms[r.Num] || x.st.formN >= maxFormWalk {
			continue
		}
		x.st.forms[r.Num] = true
		x.st.formN++
		o := x.get(r.Num)
		s, ok := o.Stream()
		if !ok {
			continue
		}
		if sub, _ := x.d.Resolve(s.Dict.Get("Subtype")).Name(); sub != "Form" {
			continue
		}
		// The keys numbered anew, or dropped where they lead to nothing
		// in the output.
		var p Patch
		for _, key := range []pdf.Name{"StructParents", "StructParent"} {
			if !s.Dict.Has(key) {
				continue
			}
			k, ok := x.d.Resolve(s.Dict.Get(key)).Int()
			if ok && x.formTagged(k) {
				if nk, ok := x.structKey(k); ok {
					x.st.formKey[r.Num] = nk
					p.Set = append(p.Set, pdf.Entry{Key: key, Val: pdf.Integer(nk)})
					continue
				}
			}
			p.Del = append(p.Del, key)
		}
		if len(p.Set)+len(p.Del) > 0 {
			dict := apply(s.Dict, p)
			// Decrypted: the writer encrypts what it writes.
			if x.im.Patch == nil {
				x.im.Patch = map[int32]pdf.Object{}
			}
			x.im.Patch[r.Num] = pdf.NewStream(dict, x.d.Raw(s)).Object()
		}
		x.structForms(s.Dict.Get("Resources"))
	}
}

// formTagged reports whether an element that key k of the source's
// parent tree leads to is on a page in the output: a form XObject that
// only pages which did not come draw as tagged content is not given a
// key.
func (x *source) formTagged(k int64) bool {
	v := x.d.Resolve(x.st.parents[k])
	elems, ok := v.Array()
	if !ok {
		elems = pdf.Array{x.st.parents[k]}
	}
	for _, e := range elems {
		o := e
		for range maxStructDepth {
			d, ok := x.d.Resolve(o).Dict()
			if !ok {
				break
			}
			if pg := d.Get("Pg"); !pg.IsNull() {
				if x.onPage(pg) {
					return true
				}
				break
			}
			o = d.Get("P")
		}
	}
	return false
}

// structRef returns the output number of an object of the source's
// structure tree: a kept element; 0 for the others, which are written as
// null. ok is false for objects of no tree.
func (x *source) structRef(num int32) (pdfwrite.Ref, bool) {
	if x.st == nil {
		return 0, false
	}
	if n, ok := x.st.out[num]; ok {
		return n, true
	}
	return 0, x.st.tree[num]
}

// writeStructure writes the structure tree of the output, once the pages
// and their annotations have their numbers.
func (b *builder) writeStructure() {
	st := b.st
	if st == nil {
		return
	}
	st.readResult()
	// The lead's elements are the output's; its /Document element, or
	// the root, holds the parts of the other sources.
	var (
		tops      []*snode // of the output's root, where it is new
		container *snode   // the lead's /Document element
		added     pdf.Array
		parts     []*snode // under an element of the edited document
	)
	if lead := st.lead; lead != nil {
		st.adopt(lead, true)
		tops = lead.keptTree()
		if len(tops) == 1 && lead.role(tops[0].dict) == "Document" {
			container = tops[0]
		}
		if lang := b.sourceLang(lead); lang != nil && !bytes.Equal(lang, b.resultLang()) {
			for _, t := range tops {
				if !t.dict.Has("Lang") {
					t.dict = t.dict.With("Lang", pdf.String(lang))
				}
			}
		}
	}
	for _, x := range b.structured() {
		if x.st == nil || x == st.lead {
			continue
		}
		st.adopt(x, false)
		kept := x.keptTree()
		if len(kept) == 0 {
			continue
		}
		pt := kept[0]
		if len(kept) == 1 && x.role(pt.dict) == "Document" {
			pt.dict = pt.dict.With("S", pdf.Name("Part").Object())
		} else {
			pt = &snode{x: x, dict: pdf.NewDict(pdf.Entry{Key: "S", Val: pdf.Name("Part").Object()}), out: b.w.Alloc()}
			for _, t := range kept {
				pt.kids = append(pt.kids, skid{elem: t})
			}
		}
		if lang := b.sourceLang(x); lang != nil && !bytes.Equal(lang, b.resultLang()) && !pt.dict.Has("Lang") {
			pt.dict = pt.dict.With("Lang", pdf.String(lang))
		}
		switch {
		case container != nil:
			container.kids = append(container.kids, skid{elem: pt})
		case st.lead != nil:
			tops = append(tops, pt)
		default:
			parts = append(parts, pt)
			added = append(added, pt.out.Object())
		}
	}
	// Written in the order of the tree, so that an identifier the output
	// has twice is renamed where it comes later.
	if st.lead != nil {
		for _, t := range tops {
			t.write(st.root)
		}
	} else {
		parent := st.baseRoot
		if st.baseDoc.Num != 0 {
			parent = st.baseDoc
		}
		for _, pt := range parts {
			pt.write(st.base.ref(parent))
		}
	}
	parentTree := b.w.Add(st.parentTree().Object())

	if st.base != nil {
		own := st.base
		if len(added) > 0 {
			at := st.baseRoot
			if st.baseDoc.Num != 0 {
				at = st.baseDoc
			}
			d, _ := own.obj(at).Dict()
			kids := kidArray(own.d, d.Get("K"))
			for _, a := range added {
				r, _ := a.Ref()
				kids = append(kids, b.alias(pdfwrite.Ref(r.Num)))
			}
			b.change(own, at, pdf.Entry{Key: "K", Val: kids.Object()})
		}
		set := []pdf.Entry{
			{Key: "ParentTree", Val: b.alias(parentTree)},
			{Key: "ParentTreeNextKey", Val: pdf.Integer(st.next)},
		}
		set = append(set, st.rootMaps(b, true)...)
		b.change(own, st.baseRoot, set...)
		return
	}
	kids := make(pdf.Array, 0, len(tops))
	for _, t := range tops {
		kids = append(kids, t.out.Object())
	}
	entries := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("StructTreeRoot").Object()},
		{Key: "K", Val: kids.Object()},
		{Key: "ParentTree", Val: parentTree.Object()},
		{Key: "ParentTreeNextKey", Val: pdf.Integer(st.next)},
	}
	entries = append(entries, st.rootMaps(b, false)...)
	b.w.Set(st.root, pdf.NewDict(entries...).Object())
}

// markInfo returns the /MarkInfo of the output's catalogue where it has a
// structure tree that imported pages add to, null otherwise: what the
// edited document says, marked.
func (b *builder) markInfo() pdf.Object {
	if b.st == nil {
		return pdf.Null
	}
	entries := []pdf.Entry{{Key: "Marked", Val: pdf.Boolean(true)}}
	if own := b.own(); own != nil {
		mi, _ := own.d.Resolve(own.catalog().Get("MarkInfo")).Dict()
		for k, v := range mi.All() {
			if k != "Marked" {
				entries = append(entries, pdf.Entry{Key: k, Val: own.im.Map(v)})
			}
		}
	}
	return pdf.NewDict(entries...).Object()
}

// kidArray returns the kids of an element or a root as an array, in the
// source's space.
func kidArray(d *pdf.Document, k pdf.Object) pdf.Array {
	if a, ok := d.Resolve(k).Array(); ok {
		return slices.Clone(a)
	}
	if k.IsNull() {
		return nil
	}
	return pdf.Array{k}
}

// readResult reads what the output's tree says before anything is added:
// the edited document's, or the lead source's.
func (st *structMerge) readResult() {
	st.roles = map[pdf.Name]pdf.Name{}
	st.classes = map[pdf.Name]pdf.Object{}
	st.classOf = map[pdf.Name]classDef{}
	st.ids = map[string]pdf.Object{}
	st.nsSeen = map[pdf.Object]bool{}
	if st.lead != nil {
		st.root = st.lead.b.w.Alloc()
		return
	}
	own := st.base
	root := st.baseDict
	roles, _ := own.d.Resolve(root.Get("RoleMap")).Dict()
	for k, v := range roles.All() {
		if n, ok := own.d.Resolve(v).Name(); ok {
			st.roles[k] = n
		}
	}
	classes, _ := own.d.Resolve(root.Get("ClassMap")).Dict()
	for k, v := range classes.All() {
		st.classes[k] = own.im.Map(v)
		st.classOf[k] = classDef{own.d, v}
	}
	for id, v := range readNameTree(own.d, root.Get("IDTree")) {
		st.ids[id] = own.im.Map(v)
	}
	ns, _ := own.d.Resolve(root.Get("Namespaces")).Array()
	for _, v := range ns {
		o := own.im.Map(v)
		if !st.nsSeen[o] {
			st.nsSeen[o] = true
			st.ns = append(st.ns, o)
		}
	}
	// The single /Document element the parts go under.
	if kids := kidArray(own.d, root.Get("K")); len(kids) == 1 {
		if r, ok := kids[0].Ref(); ok {
			if d, ok := own.obj(r).Dict(); ok {
				if role(own.d, roles, sName(d)) == "Document" {
					st.baseDoc = r
				}
			}
		}
	}
}

// adopt joins the role map, class map and namespaces of source x to the
// output's, noting the types and classes of x to rename. The lead's are
// the output's.
func (st *structMerge) adopt(x *source, lead bool) {
	d, root := x.d, x.st.root
	roles, _ := d.Resolve(root.Get("RoleMap")).Dict()
	for k, v := range roles.All() {
		n, ok := d.Resolve(v).Name()
		if !ok {
			continue
		}
		had, ok := st.roles[k]
		switch {
		case !ok || lead:
			st.roles[k] = n
		case had != n:
			// The output maps the type otherwise: it is written as
			// what the source means by it.
			x.st.roles[k] = pdf.Name(role(d, roles, string(k)))
		}
	}
	classes, _ := d.Resolve(root.Get("ClassMap")).Dict()
	for k, v := range classes.All() {
		def, ok := st.classOf[k]
		if ok && !lead && !sameValue(def.d, def.val, d, v, 0) {
			name := k
			for i := 2; ; i++ {
				name = pdf.Name(fmt.Sprintf("%s_%d", k, i))
				if _, taken := st.classOf[name]; !taken {
					break
				}
			}
			x.st.classes[k] = name
			k = name
		} else if ok && !lead {
			continue
		}
		st.classes[k] = x.im.Map(v)
		st.classOf[k] = classDef{d, v}
	}
	ns, _ := d.Resolve(root.Get("Namespaces")).Array()
	for _, v := range ns {
		o := x.im.Map(v)
		if !o.IsNull() && !st.nsSeen[o] {
			st.nsSeen[o] = true
			st.ns = append(st.ns, o)
		}
	}
}

// rootMaps returns the entries of the output's root that merging changes:
// role map, class map, identifiers and namespaces. For the edited
// document's root (patch) they are in its space.
func (st *structMerge) rootMaps(b *builder, patch bool) []pdf.Entry {
	val := func(o pdf.Object) pdf.Object {
		if !patch {
			return o
		}
		return b.alias(b.w.Add(o))
	}
	var out []pdf.Entry
	if len(st.roles) > 0 {
		e := make([]pdf.Entry, 0, len(st.roles))
		for _, k := range slices.Sorted(maps.Keys(st.roles)) {
			e = append(e, pdf.Entry{Key: k, Val: st.roles[k].Object()})
		}
		out = append(out, pdf.Entry{Key: "RoleMap", Val: val(pdf.NewDict(e...).Object())})
	}
	if len(st.classes) > 0 {
		e := make([]pdf.Entry, 0, len(st.classes))
		for _, k := range slices.Sorted(maps.Keys(st.classes)) {
			e = append(e, pdf.Entry{Key: k, Val: st.classes[k]})
		}
		out = append(out, pdf.Entry{Key: "ClassMap", Val: val(pdf.NewDict(e...).Object())})
	}
	if len(st.ids) > 0 {
		names := make(pdf.Array, 0, 2*len(st.ids))
		for _, id := range slices.Sorted(maps.Keys(st.ids)) {
			names = append(names, pdf.String([]byte(id)), st.ids[id])
		}
		out = append(out, pdf.Entry{Key: "IDTree", Val: val(pdf.NewDict(pdf.Entry{Key: "Names", Val: names.Object()}).Object())})
	}
	if len(st.ns) > 0 {
		out = append(out, pdf.Entry{Key: "Namespaces", Val: val(slices.Clone(st.ns).Object())})
	}
	return out
}

// parentTree returns the output's parent tree: the edited document's
// entries and those imported objects bring, in one flat number tree.
func (st *structMerge) parentTree() pdf.Dict {
	var nums pdf.Array
	if st.base != nil {
		parents := readNumberTree(st.base.d, st.baseDict.Get("ParentTree"))
		for _, k := range slices.Sorted(maps.Keys(parents)) {
			nums = append(nums, pdf.Integer(k), st.base.im.Map(parents[k]))
		}
	}
	for _, e := range st.parents {
		v := e.x.d.Resolve(e.val)
		if a, ok := v.Array(); ok {
			// Indexed by marked-content identifier: an element that is
			// not kept leaves a null in its place.
			m := make(pdf.Array, len(a))
			for i, el := range a {
				m[i] = e.x.elemRef(el)
			}
			nums = append(nums, pdf.Integer(e.key), m.Object())
			continue
		}
		if m := e.x.elemRef(e.val); !m.IsNull() {
			nums = append(nums, pdf.Integer(e.key), m)
		}
	}
	return pdf.NewDict(pdf.Entry{Key: "Nums", Val: nums.Object()})
}

// keptTree reads the source's tree and returns its top elements that are
// kept: those with content on an imported page, or with such an element
// below them. The kept elements are numbered.
func (x *source) keptTree() []*snode {
	n := 0
	var tops []*snode
	for _, k := range kidArray(x.d, x.st.root.Get("K")) {
		if e := x.keptElem(k, pdf.Null, 0, &n); e != nil {
			tops = append(tops, e)
		}
	}
	return tops
}

// keptElem returns the element o, with pg the page above it, if it is
// kept.
func (x *source) keptElem(o pdf.Object, pg pdf.Object, depth int, n *int) *snode {
	r, isRef := o.Ref()
	if isRef {
		if x.st.tree[r.Num] {
			return nil // seen: a cycle, or an element listed twice
		}
		x.st.tree[r.Num] = true
	}
	d, ok := x.d.Resolve(o).Dict()
	if !ok || depth >= maxStructDepth || *n >= maxStructNodes {
		return nil
	}
	*n++
	e := &snode{x: x, dict: d, pg: pg}
	if p := d.Get("Pg"); !p.IsNull() {
		e.pg = p
	}
	for _, k := range kidArray(x.d, d.Get("K")) {
		v := x.d.Resolve(k)
		switch v.Kind() {
		case pdf.KindInteger:
			if x.onPage(e.pg) {
				e.kids = append(e.kids, skid{obj: v})
			}
		case pdf.KindDict:
			kd, _ := v.Dict()
			t, _ := x.d.Resolve(kd.Get("Type")).Name()
			kpg := e.pg
			if p := kd.Get("Pg"); !p.IsNull() {
				kpg = p
			}
			switch t {
			case "MCR":
				if x.onPage(kpg) {
					e.kids = append(e.kids, skid{obj: v})
				}
			case "OBJR":
				if x.onPage(kpg) && !x.objRef(kd.Get("Obj")).IsNull() {
					e.kids = append(e.kids, skid{obj: v})
				}
			default:
				if c := x.keptElem(k, e.pg, depth+1, n); c != nil {
					e.kids = append(e.kids, skid{elem: c})
				}
			}
		}
	}
	if len(e.kids) == 0 {
		return nil
	}
	e.out = x.b.w.Alloc()
	if isRef {
		x.st.out[r.Num] = e.out
	}
	return e
}

// elemRef returns the output reference of an element of the source's
// tree, null for one that is not kept.
func (x *source) elemRef(o pdf.Object) pdf.Object {
	if r, ok := o.Ref(); ok {
		if n, ok := x.st.out[r.Num]; ok {
			return n.Object()
		}
	}
	return pdf.Null
}

// onPage reports whether pg is a page of the source that is in the output.
func (x *source) onPage(pg pdf.Object) bool {
	r, ok := pg.Ref()
	return ok && x.pageOut[r.Num] != 0
}

// objRef returns the object an object reference of the structure tree
// names, in the output's space: the first copy of an annotation, or the
// object; null for an annotation that did not come.
func (x *source) objRef(o pdf.Object) pdf.Object {
	r, ok := o.Ref()
	if !ok {
		return pdf.Null
	}
	if n, ok := x.st.annotOut[r.Num]; ok {
		return n.Object()
	}
	if d, ok := x.d.Resolve(o).Dict(); ok && d.Has("Rect") {
		return pdf.Null // an annotation of a page that did not come
	}
	return x.im.Map(o)
}

// write writes element e, under parent, and the elements below it.
func (e *snode) write(parent pdfwrite.Ref) {
	x := e.x
	entries := []pdf.Entry{{Key: "P", Val: parent.Object()}}
	for k, v := range e.dict.All() {
		switch k {
		case "P", "K", "Pg":
			continue
		case "S":
			if n, ok := x.d.Resolve(v).Name(); ok {
				if to, ok := x.st.roles[n]; ok {
					v = to.Object()
				}
			}
		case "C":
			v = x.className(x.d.Resolve(v))
		case "ID":
			v = x.b.st.addID(x.d, v, e.out)
			if v.IsNull() {
				continue
			}
		case "Ref":
			a, _ := x.d.Resolve(v).Array()
			m := make(pdf.Array, 0, len(a))
			for _, r := range a {
				if o := x.elemRef(r); !o.IsNull() {
					m = append(m, o)
				}
			}
			if len(m) == 0 {
				continue
			}
			v = m.Object()
		default:
			v = x.im.Map(v)
		}
		entries = append(entries, pdf.Entry{Key: k, Val: v})
	}
	if x.onPage(e.pg) {
		entries = append(entries, pdf.Entry{Key: "Pg", Val: x.im.Map(e.pg)})
	}
	kids := make(pdf.Array, 0, len(e.kids))
	for _, k := range e.kids {
		if k.elem != nil {
			kids = append(kids, k.elem.out.Object())
			continue
		}
		if kd, ok := k.obj.Dict(); ok {
			kids = append(kids, x.contentRef(kd).Object())
			continue
		}
		kids = append(kids, k.obj)
	}
	if len(kids) == 1 {
		entries = append(entries, pdf.Entry{Key: "K", Val: kids[0]})
	} else {
		entries = append(entries, pdf.Entry{Key: "K", Val: kids.Object()})
	}
	x.b.w.Set(e.out, pdf.NewDict(entries...).Object())
	for _, k := range e.kids {
		if k.elem != nil {
			k.elem.write(e.out)
		}
	}
}

// contentRef writes a marked-content or object reference in the output's
// space.
func (x *source) contentRef(kd pdf.Dict) pdf.Dict {
	entries := make([]pdf.Entry, 0, kd.Len())
	for k, v := range kd.All() {
		if k == "Obj" {
			v = x.objRef(v)
		} else {
			v = x.im.Map(v)
		}
		if !v.IsNull() {
			entries = append(entries, pdf.Entry{Key: k, Val: v})
		}
	}
	return pdf.NewDict(entries...)
}

// className writes the attribute classes of an element, /C: a name or an
// array of names, each followed by a revision number or not.
func (x *source) className(v pdf.Object) pdf.Object {
	rename := func(o pdf.Object) pdf.Object {
		if n, ok := x.d.Resolve(o).Name(); ok {
			if to, ok := x.st.classes[n]; ok {
				return to.Object()
			}
			return n.Object()
		}
		return x.im.Map(o)
	}
	if a, ok := v.Array(); ok {
		m := make(pdf.Array, len(a))
		for i, o := range a {
			m[i] = rename(o)
		}
		return m.Object()
	}
	return rename(v)
}

// addID enters the identifier v of an element written as out in the
// output's identifier tree, renamed id_2, id_3 and so on where the output
// has it, and returns it as written.
func (st *structMerge) addID(d *pdf.Document, v pdf.Object, out pdfwrite.Ref) pdf.Object {
	s, ok := d.Resolve(v).Str()
	if !ok {
		return pdf.Null
	}
	id := string(s)
	for i := 2; ; i++ {
		if _, taken := st.ids[id]; !taken {
			break
		}
		id = fmt.Sprintf("%s_%d", s, i)
	}
	st.ids[id] = out.Object()
	return pdf.String([]byte(id))
}

// role returns the structure type an element of x stands for, by the role
// map of its tree.
func (x *source) role(d pdf.Dict) string {
	roles, _ := x.d.Resolve(x.st.root.Get("RoleMap")).Dict()
	return role(x.d, roles, sName(d))
}

// role follows the role map of a tree from type t to the type it stands
// for.
func role(d *pdf.Document, roles pdf.Dict, t string) string {
	for range maxRoleChain {
		n, ok := d.Resolve(roles.Get(pdf.Name(t))).Name()
		if !ok || string(n) == t {
			break
		}
		t = string(n)
	}
	return t
}

// sName returns the structure type of an element.
func sName(d pdf.Dict) string {
	n, _ := d.Get("S").Name()
	return string(n)
}

// sourceLang returns the language of the catalogue of x, nil for none.
func (b *builder) sourceLang(x *source) []byte {
	s, _ := x.d.Resolve(x.catalog().Get("Lang")).Str()
	return s
}

// resultLang returns the language of the output's catalogue: the edited
// document's, or the first source's that has one.
func (b *builder) resultLang() []byte {
	if own := b.own(); own != nil {
		return b.sourceLang(own)
	}
	for _, x := range b.sources {
		if s := b.sourceLang(x); s != nil {
			return s
		}
	}
	return nil
}

// readNumberTree returns the entries of a number tree.
func readNumberTree(d *pdf.Document, root pdf.Object) map[int64]pdf.Object {
	out := map[int64]pdf.Object{}
	stack := []pdf.Object{root}
	seen := map[int32]bool{}
	for n := 0; len(stack) > 0 && n < maxNameTreeNodes && len(out) < maxParentKeys; n++ {
		o := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if r, ok := o.Ref(); ok {
			if seen[r.Num] {
				continue
			}
			seen[r.Num] = true
		}
		node, ok := d.Resolve(o).Dict()
		if !ok {
			continue
		}
		nums, _ := d.Resolve(node.Get("Nums")).Array()
		for i := 0; i+1 < len(nums); i += 2 {
			if k, ok := d.Resolve(nums[i]).Int(); ok {
				if _, dup := out[k]; !dup {
					out[k] = nums[i+1]
				}
			}
		}
		kids, _ := d.Resolve(node.Get("Kids")).Array()
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, kids[i])
		}
	}
	return out
}

// readNameTree returns the entries of a name tree.
func readNameTree(d *pdf.Document, root pdf.Object) map[string]pdf.Object {
	out := map[string]pdf.Object{}
	stack := []pdf.Object{root}
	seen := map[int32]bool{}
	for n := 0; len(stack) > 0 && n < maxNameTreeNodes && len(out) < maxParentKeys; n++ {
		o := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if r, ok := o.Ref(); ok {
			if seen[r.Num] {
				continue
			}
			seen[r.Num] = true
		}
		node, ok := d.Resolve(o).Dict()
		if !ok {
			continue
		}
		names, _ := d.Resolve(node.Get("Names")).Array()
		for i := 0; i+1 < len(names); i += 2 {
			if k, ok := d.Resolve(names[i]).Str(); ok {
				if _, dup := out[string(k)]; !dup {
					out[string(k)] = names[i+1]
				}
			}
		}
		kids, _ := d.Resolve(node.Get("Kids")).Array()
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, kids[i])
		}
	}
	return out
}

// sameValue reports whether a of document da and b of db are the same
// value, references followed.
func sameValue(da *pdf.Document, a pdf.Object, db *pdf.Document, b pdf.Object, depth int) bool {
	if depth > 8 {
		return false
	}
	a, b = da.Resolve(a), db.Resolve(b)
	if a.Kind() != b.Kind() {
		return false
	}
	switch a.Kind() {
	case pdf.KindArray:
		x, _ := a.Array()
		y, _ := b.Array()
		if len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameValue(da, x[i], db, y[i], depth+1) {
				return false
			}
		}
		return true
	case pdf.KindDict:
		x, _ := a.Dict()
		y, _ := b.Dict()
		if x.Len() != y.Len() {
			return false
		}
		for k, v := range x.All() {
			w, ok := y.Lookup(k)
			if !ok || !sameValue(da, v, db, w, depth+1) {
				return false
			}
		}
		return true
	case pdf.KindStream:
		return false
	}
	return a.String() == b.String()
}
