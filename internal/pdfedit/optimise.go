package pdfedit

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"math"
	"slices"
	"strings"

	"github.com/timzifer/cera/internal/content"
	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// What makes a new file smaller than its sources without changing what it
// shows: resources no content names are left out, and streams that are the
// same are written once. Both read the sources and decide what the
// importers copy; neither changes a byte of a stream.

// Drop names what a new file leaves out on request: parts of a document a
// viewer does not need to show it.
type Drop uint8

const (
	// DropThumbnails leaves out the thumbnail images of pages.
	DropThumbnails Drop = 1 << iota
	// DropPieceInfo leaves out the private data of the programs that made
	// the document, of the document, its pages and its form XObjects.
	DropPieceInfo
	// DropMetadata leaves out the metadata streams of everything but the
	// document itself.
	DropMetadata
)

// dropKeys adds the keys doc leaves out of every dictionary to those an
// importer drops.
func (b *builder) dropKeys(im *pdfwrite.Importer) {
	if b.upd != nil || b.doc.Drop&(DropPieceInfo|DropMetadata) == 0 {
		return
	}
	keys := map[pdf.Name]bool{}
	for k := range im.Drop {
		keys[k] = true
	}
	if b.doc.Drop&DropPieceInfo != 0 {
		keys["PieceInfo"] = true
	}
	if b.doc.Drop&DropMetadata != 0 {
		keys["Metadata"] = true
	}
	im.Drop = keys
}

// dropped reports whether the entry k of a page or of the catalogue is
// left out.
func (b *builder) dropped(k pdf.Name, page bool) bool {
	if b.upd != nil {
		return false
	}
	switch k {
	case "Thumb":
		return page && b.doc.Drop&DropThumbnails != 0
	case "PieceInfo":
		return b.doc.Drop&DropPieceInfo != 0
	case "Metadata":
		return page && b.doc.Drop&DropMetadata != 0
	}
	return false
}

// copier returns the source whose importer copies the objects of x: its
// own, or for pages imported from the edited document that of the
// document's own pages.
func (x *source) copier() *source {
	if x.shared != nil {
		return x.shared
	}
	return x
}

// get returns object num of the source as it is written: changed, or as
// the file has it; null when it is missing.
func (x *source) get(num int32) pdf.Object {
	if o, ok := x.copier().im.Patch[num]; ok {
		return o
	}
	o, err := x.d.Get(pdf.Ref{Num: num})
	if err != nil {
		return pdf.Null
	}
	return o
}

// deref follows the references of o to what is written for them. It
// reports false for a new object that is in the space of another document:
// one this source cannot look into.
func (x *source) deref(o pdf.Object) (pdf.Object, bool) {
	for range pdf.MaxRefChain {
		r, ok := o.Ref()
		if !ok {
			return o, true
		}
		if r.Gen == newGen {
			doc := &x.b.doc
			if r.Num < 0 || int(r.Num) >= len(doc.Objects) {
				return pdf.Null, false
			}
			space := doc.ObjectSrc[int(r.Num)]
			if space == nil {
				space = doc.Base
			}
			if space != x.d {
				return pdf.Null, false
			}
			o = doc.Objects[r.Num]
			continue
		}
		o = x.get(r.Num)
	}
	return pdf.Null, true
}

// resolve is deref as a function of one result: what cannot be looked
// into is null.
func (x *source) resolve(o pdf.Object) pdf.Object {
	v, _ := x.deref(o)
	return v
}

// decode returns the decoded data of a stream of the source, or of one made
// of its objects; false when it cannot be decoded to the end.
func (x *source) decode(s *pdf.Stream) ([]byte, bool) {
	var dec pdf.Decoded
	if s.Ref.Num > 0 {
		dec = x.d.Decode(s)
	} else {
		dec = x.d.DecodeBytes(s.Dict, x.d.Raw(s))
	}
	if dec.Recovered || dec.Image != "" {
		return nil, false
	}
	return dec.Data, true
}

// Unused resources.

// The kinds of resources content names.
var resourceKinds = [...]pdf.Name{"Font", "XObject", "ExtGState", "ColorSpace", "Pattern", "Shading", "Properties"}

const (
	resFont = iota
	resXObject
	resExtGState
	resColorSpace
	resPattern
	resShading
	resProperties
)

// kept are the entries of a kind that are used without being named: the
// colour spaces that stand in for the device ones (PDF 2.0, 8.6.5.6).
var kept = map[string]bool{"DefaultGray": true, "DefaultRGB": true, "DefaultCMYK": true}

// A pruner finds the resources nothing names in what one importer copies.
//
// A context is what is drawn with a resource dictionary of its own: a
// page, a form XObject, a tiling pattern, a Type 3 font. Its content is
// scanned for the names it uses. Dictionaries are shared — one for all
// pages, one of fonts for several forms — so a name stays where any
// context that has the dictionary uses it: a dictionary that is an object
// is written as one object again, without what none of them names, and
// never once per context. The file can only get smaller for it. A context
// whose content cannot be read keeps the dictionary it has.
type pruner struct {
	x    *source // the copier
	ctxs []*resContext
	seen map[int32]bool // the objects looked at
	// used holds the names in use, by the dictionary they are in.
	used map[holder]map[string]bool
	// newRes and newSub hold what is written for the resource
	// dictionaries and those of one kind that are objects: null for one
	// that stays.
	newRes, newSub map[int32]pdf.Object
	// fixed holds the dictionaries that stay as they are because more
	// than content has them: the resources of the form, with which a
	// viewer writes what is typed into a field.
	fixed map[int32]bool
}

// A resContext is one context whose content was read.
type resContext struct {
	page  int        // the output page, -1 for an object
	num   int32      // the object
	res   pdf.Object // its /Resources, as it has them
	rd    pdf.Dict
	subs  [len(resourceKinds)]pdf.Dict
	names [len(resourceKinds)]map[string]bool
}

// A holder is the dictionary the names of one kind are in: an object of
// its own (kind -1), part of a resource dictionary that is an object, or
// part of the one a context has to itself.
type holder struct {
	num  int32
	kind int
	ctx  *resContext
}

// refNum returns the number of the object of the source o refers to, 0
// when it is none.
func refNum(o pdf.Object) int32 {
	if r, ok := o.Ref(); ok && r.Gen != newGen && r.Num > 0 {
		return r.Num
	}
	return 0
}

func (c *resContext) holder(kind int) holder {
	if n := refNum(c.rd.Get(resourceKinds[kind])); n > 0 {
		return holder{num: n, kind: -1}
	}
	if n := refNum(c.res); n > 0 {
		return holder{num: n, kind: kind}
	}
	return holder{ctx: c, kind: kind}
}

// pruner returns the pruner of what the importer of x copies.
func (x *source) pruner() *pruner {
	c := x.copier()
	if c.pr == nil {
		c.pr = &pruner{
			x: c, seen: map[int32]bool{}, used: map[holder]map[string]bool{},
			newRes: map[int32]pdf.Object{}, newSub: map[int32]pdf.Object{}, fixed: map[int32]bool{},
		}
		cat, _ := c.d.Catalog()
		form, _ := c.resolve(cat.Get("AcroForm")).Dict()
		dr := form.Get("DR")
		c.pr.fixed[refNum(dr)] = true
		rd, _ := c.resolve(dr).Dict()
		for _, v := range rd.All() {
			c.pr.fixed[refNum(v)] = true
		}
	}
	return c.pr
}

// usage is what the content of one context names.
type usage struct {
	pr    *pruner
	names [len(resourceKinds)]map[string]bool
	// todo holds the names whose objects are still to be looked at.
	todo []usedName
	// bad says that some content could not be read to the end: nothing is
	// known about what it names.
	bad bool
	sc  content.Scanner
}

type usedName struct {
	kind int
	name string
}

func (u *usage) use(kind int, sc *content.Scanner, o *content.Operand) {
	if o == nil || o.Kind != content.Name {
		return
	}
	name := string(sc.Text(o))
	if u.names[kind] == nil {
		u.names[kind] = map[string]bool{}
	}
	if !u.names[kind][name] {
		u.names[kind][name] = true
		u.todo = append(u.todo, usedName{kind, name})
	}
}

// scan notes the resources the content data names.
func (u *usage) scan(data []byte) {
	sc := &u.sc
	sc.Reset(data)
	for {
		op, ok := sc.Next()
		if !ok {
			break
		}
		switch string(op) {
		case "Tf":
			u.use(resFont, sc, sc.FromEnd(1))
		case "Do":
			u.use(resXObject, sc, sc.Last())
		case "gs":
			u.use(resExtGState, sc, sc.Last())
		case "cs", "CS":
			u.use(resColorSpace, sc, sc.Last())
		case "scn", "SCN":
			u.use(resPattern, sc, sc.Last())
		case "sh":
			u.use(resShading, sc, sc.Last())
		case "BDC", "DP":
			u.use(resProperties, sc, sc.Last())
		case "BI":
			// The colour space of an inline image may be a named one,
			// alone or as the base of an indexed space.
			dict, _ := sc.Image()
			for _, key := range [...]string{"CS", "ColorSpace"} {
				v := sc.DictGet(dict, key)
				if v == nil {
					continue
				}
				u.use(resColorSpace, sc, v)
				sc.Elems(v, func(e *content.Operand) bool {
					u.use(resColorSpace, sc, e)
					return true
				})
			}
		}
	}
	if sc.Errors() > 0 {
		u.bad = true
	}
}

// stream notes what the content of stream s names.
func (u *usage) stream(s *pdf.Stream) {
	data, ok := u.pr.x.decode(s)
	if !ok {
		u.bad = true
		return
	}
	u.scan(data)
}

// drawn looks at an object content draws with the resources of u: a form
// XObject, a tiling pattern, the group of a soft mask, an appearance. With
// resources of its own it is a context; without, it is drawn with those of
// u, as forms once were (PDF 2.0, 8.10.2), and what it names counts here.
func (u *usage) drawn(o pdf.Object, depth int) {
	v, ok := u.pr.x.deref(o)
	s, isStream := v.Stream()
	if !ok || !isStream {
		return
	}
	if t, _ := s.Dict.Get("Subtype").Name(); t == "Image" {
		return
	}
	if !s.Dict.Has("Resources") {
		u.stream(s)
		return
	}
	u.pr.object(o, depth+1)
}

// font looks at a font content sets: the glyphs of a Type 3 font are
// content.
func (u *usage) font(o pdf.Object, depth int) {
	v, ok := u.pr.x.deref(o)
	d, isDict := v.Dict()
	if !ok || !isDict || v.Kind() != pdf.KindDict {
		return
	}
	if t, _ := d.Get("Subtype").Name(); t != "Type3" {
		return
	}
	if d.Has("Resources") {
		u.pr.object(o, depth+1)
		return
	}
	u.glyphs(d)
}

// glyphs notes what the glyph procedures of Type 3 font d name.
func (u *usage) glyphs(d pdf.Dict) {
	x := u.pr.x
	procs, ok := x.deref(d.Get("CharProcs"))
	pd, _ := procs.Dict()
	if !ok {
		u.bad = true
		return
	}
	for _, v := range pd.All() {
		o, ok := x.deref(v)
		if s, isStream := o.Stream(); ok && isStream {
			u.stream(s)
		} else if !ok {
			u.bad = true
		}
	}
}

// maxPruneDepth bounds how deeply forms, patterns and fonts with resources
// of their own nest in what is pruned; deeper ones keep what they have.
const maxPruneDepth = 32

// object looks at an object of the source that has resources of its own,
// once.
func (pr *pruner) object(o pdf.Object, depth int) {
	num := refNum(o)
	if num == 0 || pr.seen[num] || depth > maxPruneDepth {
		return
	}
	pr.seen[num] = true
	v := pr.x.get(num)
	switch v.Kind() {
	case pdf.KindStream:
		s, _ := v.Stream()
		pr.context(-1, num, s.Dict.Get("Resources"), depth, func(u *usage) { u.stream(s) })
	case pdf.KindDict:
		d, _ := v.Dict()
		pr.context(-1, num, d.Get("Resources"), depth, func(u *usage) { u.glyphs(d) })
	}
}

// context reads the content of one context — scan notes what it names —
// and of what that draws with the same resources, and keeps the context
// where all of it could be read.
func (pr *pruner) context(page int, num int32, res pdf.Object, depth int, scan func(u *usage)) {
	x := pr.x
	rv, ok := x.deref(res)
	rd, isDict := rv.Dict()
	if !ok || !isDict || rv.Kind() != pdf.KindDict {
		return
	}
	c := &resContext{page: page, num: num, res: res, rd: rd}
	for i, k := range resourceKinds {
		sv, ok := x.deref(rd.Get(k))
		if !ok {
			return
		}
		if sv.Kind() == pdf.KindDict {
			c.subs[i], _ = sv.Dict()
		}
	}
	u := &usage{pr: pr}
	scan(u)
	for len(u.todo) > 0 && !u.bad {
		n := u.todo[len(u.todo)-1]
		u.todo = u.todo[:len(u.todo)-1]
		o := c.subs[n.kind].Get(pdf.Name(n.name))
		switch n.kind {
		case resXObject, resPattern:
			u.drawn(o, depth)
		case resFont:
			u.font(o, depth)
		case resExtGState:
			gs, ok := x.deref(o)
			gd, _ := gs.Dict()
			mask, ok2 := x.deref(gd.Get("SMask"))
			if md, isDict := mask.Dict(); ok && ok2 && isDict {
				u.drawn(md.Get("G"), depth)
			}
		}
	}
	if u.bad {
		return
	}
	c.names = u.names
	pr.ctxs = append(pr.ctxs, c)
}

// page looks at output page i, whose dictionary is page: at its content
// streams and at the appearance streams of its annotations — with the
// page's resources where they have none of their own.
func (pr *pruner) page(i int, page pdf.Dict) {
	x := pr.x
	pr.context(i, 0, page.Get("Resources"), 0, func(u *usage) {
		c, ok := x.deref(page.Get("Contents"))
		if !ok {
			u.bad = true
			return
		}
		var data []byte
		switch c.Kind() {
		case pdf.KindStream:
			s, _ := c.Stream()
			if data, ok = x.decode(s); !ok {
				u.bad = true
				return
			}
		case pdf.KindArray:
			// The streams are one content: a token may not end where a
			// stream does, an operation may.
			a, _ := c.Array()
			for _, e := range a {
				o, ok := x.deref(e)
				s, isStream := o.Stream()
				if !ok {
					u.bad = true
					return
				}
				if !isStream {
					continue
				}
				part, ok := x.decode(s)
				if !ok {
					u.bad = true
					return
				}
				data = append(append(data, part...), '\n')
			}
		}
		u.scan(data)

		annots, _ := x.deref(page.Get("Annots"))
		list, _ := annots.Array()
		for _, a := range list {
			av, _ := x.deref(a)
			ad, _ := av.Dict()
			ap, _ := x.deref(ad.Get("AP"))
			apd, _ := ap.Dict()
			for _, v := range apd.All() {
				o, _ := x.deref(v)
				switch o.Kind() {
				case pdf.KindStream:
					u.drawn(v, 0)
				case pdf.KindDict:
					states, _ := o.Dict()
					for _, sv := range states.All() {
						u.drawn(sv, 0)
					}
				}
			}
		}
	})
}

// finish writes the dictionaries that lose entries anew: with the pages,
// and with the objects, which are changed for it.
func (pr *pruner) finish() {
	x := pr.x
	for _, c := range pr.ctxs {
		for i := range resourceKinds {
			if c.subs[i].IsZero() {
				continue
			}
			h := c.holder(i)
			if pr.used[h] == nil {
				pr.used[h] = map[string]bool{}
			}
			for name := range c.names[i] {
				pr.used[h][name] = true
			}
		}
	}
	for _, c := range pr.ctxs {
		res := pr.result(c)
		if res.IsNull() {
			continue
		}
		if c.page >= 0 {
			x.b.pageRes[c.page] = res
			continue
		}
		var out pdf.Object
		switch o := x.get(c.num); o.Kind() {
		case pdf.KindStream:
			// Decrypted: the writer encrypts what it writes.
			s, _ := o.Stream()
			out = pdf.NewStream(s.Dict.With("Resources", res), x.d.Raw(s)).Object()
		case pdf.KindDict:
			d, _ := o.Dict()
			out = d.With("Resources", res).Object()
		default:
			continue
		}
		if x.im.Patch == nil {
			x.im.Patch = map[int32]pdf.Object{}
		}
		x.im.Patch[c.num] = out
	}
}

// result returns the resources context c is written with, in the source's
// space: null when it keeps those it has, and for a dictionary that is an
// object the reference to the new object that is written in its place.
func (pr *pruner) result(c *resContext) pdf.Object {
	n := refNum(c.res)
	if n > 0 {
		if res, ok := pr.newRes[n]; ok || pr.fixed[n] {
			return res
		}
	}
	changed := false
	entries := make([]pdf.Entry, 0, c.rd.Len())
	for k, v := range c.rd.All() {
		i := slices.Index(resourceKinds[:], k)
		if i < 0 || c.subs[i].IsZero() {
			entries = append(entries, pdf.Entry{Key: k, Val: v})
			continue
		}
		h := c.holder(i)
		names := pr.used[h]
		used := make([]pdf.Entry, 0, c.subs[i].Len())
		for name, o := range c.subs[i].All() {
			if names[string(name)] || i == resColorSpace && kept[string(name)] {
				used = append(used, pdf.Entry{Key: name, Val: o})
			}
		}
		switch {
		case len(used) == c.subs[i].Len(), h.kind < 0 && pr.fixed[h.num]:
			entries = append(entries, pdf.Entry{Key: k, Val: v})
			continue
		case len(used) == 0:
		case h.kind < 0:
			sub, ok := pr.newSub[h.num]
			if !ok {
				sub = pr.x.newObj(pdf.NewDict(used...).Object())
				pr.newSub[h.num] = sub
			}
			entries = append(entries, pdf.Entry{Key: k, Val: sub})
		default:
			entries = append(entries, pdf.Entry{Key: k, Val: pdf.NewDict(used...).Object()})
		}
		changed = true
	}
	res := pdf.Null
	if changed {
		res = pdf.NewDict(entries...).Object()
		if n > 0 {
			res = pr.x.newObj(res)
		}
	}
	if n > 0 {
		pr.newRes[n] = res
	}
	return res
}

// newObj adds a new object in the space of the source and returns the
// reference to it.
func (x *source) newObj(o pdf.Object) pdf.Object {
	doc := &x.b.doc
	i := len(doc.Objects)
	doc.Objects = append(doc.Objects, o)
	if x.d != doc.Base {
		if !x.b.srcOwn {
			// The caller's map stays as it is.
			src := make(map[int]*pdf.Document, len(doc.ObjectSrc)+1)
			for k, v := range doc.ObjectSrc {
				src[k] = v
			}
			doc.ObjectSrc, x.b.srcOwn = src, true
		}
		doc.ObjectSrc[i] = x.d
	}
	return NewRef(i).Object()
}

// prunePages finds the resources no content of the output names: of its
// pages, and of the forms, patterns and fonts they draw with resources of
// their own.
func (b *builder) prunePages() {
	b.pageRes = make([]pdf.Object, len(b.doc.Pages))
	for i, p := range b.doc.Pages {
		if p.Src == nil {
			continue
		}
		x := b.bySrc[srcKey{p.Src, p.Own}]
		if page, err := x.pageDict(p); err == nil {
			x.pruner().page(i, page)
		}
	}
	for _, x := range b.sources {
		if x.pr != nil {
			x.pr.finish()
		}
	}
}

// Duplicates.

// A sum stands for the value of an object: two objects with the same sum
// are written the same.
type sum [sha256.Size]byte

// maxSumDepth bounds how far references are followed into what an object
// refers to, and maxSumNest how deeply direct values nest in one; an object
// beyond either is like no other.
const (
	maxSumDepth = 64
	maxSumNest  = 256
)

// joins reports whether an object like o is written once for all that are
// the same: a stream, a font. Other objects may be the same and still be
// two — two layers of one name, two fields.
func joins(o pdf.Object) bool {
	switch o.Kind() {
	case pdf.KindStream:
		return true
	case pdf.KindDict:
		d, _ := o.Dict()
		t, _ := d.Get("Type").Name()
		return t == "Font" || t == "FontDescriptor"
	}
	return false
}

// itself reports whether the dictionary d is told from others by more
// than what it holds — a page, a layer, an annotation, a field, a node of
// a tree: references to two of them are never to the same.
func itself(d pdf.Dict) bool {
	switch t, _ := d.Get("Type").Name(); t {
	case "Page", "Pages", "Catalog", "OCG", "Annot", "StructElem", "StructTreeRoot", "Outlines", "Sig":
		return true
	}
	return d.Has("Parent") || d.Has("Kids") || d.Has("FT") || d.Has("Rect") && d.Has("Subtype")
}

// join returns the output number of source object r, which is one of
// those written once: that of an object with the same value where one is
// written already.
func (x *source) join(r pdf.Ref) pdfwrite.Ref {
	b := x.b
	s := x.sum(r.Num, 0)
	if n, ok := b.joined[s]; ok {
		x.im.Alias(r, n)
		return n
	}
	n := x.im.Copy(r)
	if n > 0 {
		if b.joined == nil {
			b.joined = map[sum]pdfwrite.Ref{}
		}
		b.joined[s] = n
	}
	return n
}

// docID numbers the documents of an output, to tell their objects apart.
func (b *builder) docID(d *pdf.Document) uint64 {
	if b.docIDs == nil {
		b.docIDs = map[*pdf.Document]uint64{}
	}
	id, ok := b.docIDs[d]
	if !ok {
		id = uint64(len(b.docIDs) + 1)
		b.docIDs[d] = id
	}
	return id
}

// only returns a sum that object num of the source has alone.
func (x *source) only(num int32) (s sum) {
	h := sha256.New()
	var buf [17]byte
	buf[0] = 'I'
	binary.BigEndian.PutUint64(buf[1:], x.b.docID(x.d))
	binary.BigEndian.PutUint64(buf[9:], uint64(num))
	h.Write(buf[:])
	h.Sum(s[:0])
	return s
}

// sum returns the sum of object num of the source: of its value with the
// values of everything it refers to, so that two objects have the same sum
// when nothing tells them apart. What is told apart by more than its value
// (itself), what refers to itself and what lies too deep has a sum of its
// own.
func (x *source) sum(num int32, depth int) sum {
	c := x.copier()
	if s, ok := c.sums[num]; ok {
		return s
	}
	if c.summing[num] || depth > maxSumDepth {
		return x.only(num)
	}
	if c.sums == nil {
		c.sums, c.summing = map[int32]sum{}, map[int32]bool{}
	}
	o := x.get(num)
	var s sum
	if d, ok := o.Dict(); ok && o.Kind() == pdf.KindDict && itself(d) {
		s = x.only(num)
	} else {
		c.summing[num] = true
		h := sha256.New()
		if x.encode(h, o, depth, 0) {
			h.Sum(s[:0])
		} else {
			s = x.only(num)
		}
		delete(c.summing, num)
	}
	c.sums[num] = s
	return s
}

// encode writes the value o to h in a form that is the same for equal
// values: a reference as the sum of what it refers to, one to a new object
// as itself. Entries the importer leaves out are left out. It reports
// false for a value that nests too deeply.
func (x *source) encode(h hash.Hash, o pdf.Object, depth, nest int) bool {
	if nest > maxSumNest {
		return false
	}
	var buf [9]byte
	put := func(tag byte, v uint64) {
		buf[0] = tag
		binary.BigEndian.PutUint64(buf[1:], v)
		h.Write(buf[:])
	}
	switch o.Kind() {
	case pdf.KindNull:
		put('z', 0)
	case pdf.KindBool:
		v, _ := o.Bool()
		if v {
			put('b', 1)
		} else {
			put('b', 0)
		}
	case pdf.KindInteger:
		v, _ := o.Int()
		put('i', uint64(v))
	case pdf.KindReal:
		v, _ := o.Float()
		put('r', math.Float64bits(v))
	case pdf.KindString:
		v, _ := o.Str()
		put('s', uint64(len(v)))
		h.Write(v)
	case pdf.KindName:
		v, _ := o.Name()
		put('n', uint64(len(v)))
		h.Write([]byte(v))
	case pdf.KindArray:
		a, _ := o.Array()
		put('a', uint64(len(a)))
		for _, e := range a {
			if !x.encode(h, e, depth, nest+1) {
				return false
			}
		}
	case pdf.KindDict:
		d, _ := o.Dict()
		return x.encodeDict(h, d, false, depth, nest)
	case pdf.KindStream:
		s, _ := o.Stream()
		put('S', 0)
		if !x.encodeDict(h, s.Dict, true, depth, nest) {
			return false
		}
		data := x.d.Raw(s)
		put('D', uint64(len(data)))
		h.Write(data)
	case pdf.KindRef:
		r, _ := o.Ref()
		if r.Gen == newGen {
			put('N', uint64(r.Num))
			break
		}
		s := x.sum(r.Num, depth+1)
		put('o', 0)
		h.Write(s[:])
	}
	return true
}

// encodeDict writes a dictionary, its entries by key; a stream's without
// its length, which says nothing the data does not.
func (x *source) encodeDict(h hash.Hash, d pdf.Dict, stream bool, depth, nest int) bool {
	drop := x.copier().im.Drop
	entries := make([]pdf.Entry, 0, d.Len())
	for k, v := range d.All() {
		if drop[k] || stream && k == "Length" {
			continue
		}
		entries = append(entries, pdf.Entry{Key: k, Val: v})
	}
	slices.SortFunc(entries, func(a, b pdf.Entry) int { return strings.Compare(string(a.Key), string(b.Key)) })
	var buf [9]byte
	buf[0] = 'd'
	binary.BigEndian.PutUint64(buf[1:], uint64(len(entries)))
	h.Write(buf[:])
	for _, e := range entries {
		buf[0] = 'k'
		binary.BigEndian.PutUint64(buf[1:], uint64(len(e.Key)))
		h.Write(buf[:])
		h.Write([]byte(e.Key))
		if !x.encode(h, e.Val, depth, nest+1) {
			return false
		}
	}
	return true
}
