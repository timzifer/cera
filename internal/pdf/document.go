// Ported from github.com/go-pdfkit/reader v0.6.0 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/reader authors); see LICENSE-go-pdfkit.
// Changed: objects are published once per table and the table is swapped
// on repair, so a Document is safe for concurrent use.

package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// maxPageTreeNodes bounds a page tree walk.
const maxPageTreeNodes = 1 << 20

// MaxPageTreeDepth bounds how deeply page tree nodes may nest.
const MaxPageTreeDepth = 64

// inheritable lists the page attributes a node may take from an ancestor.
var inheritable = [...]Name{"Resources", "MediaBox", "CropBox", "Rotate"}

// A Document is a parsed PDF file: its cross-reference information, its
// trailer, and lazy access to every object in it. It is safe for concurrent
// use; objects it returns are shared and must not be modified.
type Document struct {
	src      source
	password string
	version  string

	tab      atomic.Pointer[table]
	repairMu sync.Mutex
	cache    streamCache

	resolveFn func(Object) Object
}

// Options configure [OpenWith].
type Options struct {
	// Password is tried as the user and as the owner password; the empty
	// password is tried as well.
	Password string
	// StreamCacheBytes bounds the cache of decoded streams: 0 means
	// DefaultStreamCacheBytes, a negative value turns the cache off.
	StreamCacheBytes int
}

// Open parses the cross-reference information of a PDF file held in memory,
// using the empty password. b must not change while the Document is used:
// objects alias it. A file whose tables are damaged is rebuilt by scanning
// it.
func Open(b []byte) (*Document, error) { return OpenWith(b, Options{}) }

// OpenWithPassword is Open with a password to try, both as the user and as
// the owner password; the empty one is tried as well.
func OpenWithPassword(b []byte, password string) (*Document, error) {
	return OpenWith(b, Options{Password: password})
}

// OpenWith is Open with options.
func OpenWith(b []byte, opt Options) (*Document, error) {
	return open(memSource(b), opt)
}

// OpenReaderAt is OpenWith for a file read on demand through r, size bytes
// long: only the parts of the file that are needed are read, and objects
// keep copies of what they hold rather than views of the file. Opening a
// damaged file reads all of it to rebuild its tables.
func OpenReaderAt(r io.ReaderAt, size int64, opt Options) (*Document, error) {
	return open(readerSource{r, size}, opt)
}

func open(src source, opt Options) (*Document, error) {
	d := &Document{src: src, password: opt.Password, version: version(src)}
	d.resolveFn = d.Resolve
	switch {
	case opt.StreamCacheBytes == 0:
		d.cache.init(DefaultStreamCacheBytes)
	case opt.StreamCacheBytes > 0:
		d.cache.init(opt.StreamCacheBytes)
	}
	ctx := getCtx()
	defer putCtx(ctx)

	t, err := d.loadXref()
	if err == nil {
		dec, derr := d.setUpDecryption(ctx, t, t.trailer)
		if derr != nil {
			return nil, derr
		}
		t.dec = dec
		t.resetCache()
		// The table is the document's from here: looking for the catalogue
		// may already rebuild it.
		d.tab.Store(t)
		_, cerr := d.catalog(ctx, t, t.trailer)
		if cerr == nil {
			return d, nil
		}
		// Tables that parse but lead nowhere are worse than none: rebuild.
		err = fmt.Errorf("pdf: the cross-reference tables do not lead to a catalogue: %w", cerr)
		t = d.tab.Load()
	}
	nt, rerr := d.repair(ctx, t)
	if rerr != nil {
		return nil, fmt.Errorf("%w (the cross-reference information was unusable too: %v)", rerr, err)
	}
	d.tab.Store(nt)
	return d, nil
}

// version reads the header version. A file read on demand is searched in
// its first megabyte for it, one in memory throughout.
func version(src source) string {
	b, _ := src.window(0, 1<<20)
	i := bytes.Index(b, []byte("%PDF-"))
	if i < 0 || i+8 > len(b) {
		return ""
	}
	return string(bytes.TrimRight(b[i+5:i+8], "\r\n \t"))
}

// Version reports the PDF version the header declares, "1.7" and the like.
func (d *Document) Version() string { return d.version }

// Trailer returns the file's trailer dictionary, the newest value of every
// key across the chain of cross-reference sections.
func (d *Document) Trailer() Dict { return d.tab.Load().trailer }

// Repaired reports whether the document's structure was rebuilt by scanning
// the file rather than read from its cross-reference tables.
func (d *Document) Repaired() bool { return d.tab.Load().repaired }

// Encrypted reports whether the file declares an /Encrypt dictionary.
func (d *Document) Encrypted() bool { return !d.Trailer().Get("Encrypt").IsNull() }

// Protection reports how the file is protected, and false when it is not.
func (d *Document) Protection() (Protection, bool) {
	dec := d.tab.Load().dec
	if dec == nil {
		return Protection{}, false
	}
	return Protection{Method: dec.methodName(), Revision: dec.revision, Permissions: dec.perm, Owner: dec.owner}, true
}

// Get returns the object an indirect reference names, or [Null] when the
// file does not define it.
func (d *Document) Get(r Ref) (Object, error) {
	ctx := getCtx()
	o, err := d.get(ctx, d.tab.Load(), r.Num)
	putCtx(ctx)
	return o, err
}

// Resolve follows indirect references until it reaches a direct object. A
// reference that cannot be resolved yields [Null].
func (d *Document) Resolve(o Object) Object {
	if o.kind != KindRef {
		return o
	}
	ctx := getCtx()
	o = d.resolve(ctx, d.tab.Load(), o)
	putCtx(ctx)
	return o
}

// GetDict resolves an entry of a dictionary to a dictionary.
func (d *Document) GetDict(from Dict, key Name) (Dict, bool) {
	return d.Resolve(from.Get(key)).Dict()
}

// resolve follows references in t.
func (d *Document) resolve(ctx *loadCtx, t *table, o Object) Object {
	for depth := 0; o.kind == KindRef; depth++ {
		if depth >= MaxRefChain {
			return Null
		}
		r, _ := o.Ref()
		v, err := d.get(ctx, t, r.Num)
		if err != nil {
			return Null
		}
		o = v
	}
	return o
}

// get loads object num through t.
func (d *Document) get(ctx *loadCtx, t *table, num int32) (Object, error) {
	e := t.entry(num)
	if e == nil || e.kind == 'f' {
		return Null, nil
	}
	if p := e.obj.Load(); p != nil {
		return *p, nil
	}
	if !ctx.enter(objKey(num)) {
		// An object whose loading needs itself: an object stream whose
		// /Length points into it, a /Length that is its own stream.
		return Null, nil
	}
	defer ctx.leave()
	var p *Object
	var err error
	if e.kind == 'o' {
		p, err = d.fromObjectStream(ctx, t, num, int32(e.off))
	} else {
		p, err = d.atOffset(ctx, t, num, e)
	}
	if err != nil {
		return Null, err
	}
	if !e.obj.CompareAndSwap(nil, p) {
		p = e.obj.Load()
	}
	return *p, nil
}

// atOffset reads an object written directly in the file. An offset that
// does not hold the expected object means the tables are wrong, which is
// common enough that it triggers a rebuild rather than an error.
func (d *Document) atOffset(ctx *loadCtx, t *table, num int32, e *xentry) (*Object, error) {
	if e.off < 0 || e.off >= d.src.size() {
		return d.retryAfterRepair(ctx, t, num, fmt.Errorf("pdf: object %d is at offset %d, outside the file", num, e.off))
	}
	got, p, err := d.parseAt(ctx, t, e.off)
	if err != nil {
		return d.retryAfterRepair(ctx, t, num, err)
	}
	if got.Num != num {
		return d.retryAfterRepair(ctx, t, num, fmt.Errorf("pdf: offset %d holds object %d, not %d", e.off, got.Num, num))
	}
	return p, nil
}

// windowed runs parse over the file from off on. A file in memory is parsed
// in one go; one read on demand is read a window at a time, a larger one
// whenever the object may run past the end of the last.
func (d *Document) windowed(off int64, parse func(p *parser) error) error {
	for size := firstWindow; ; size *= 4 {
		b, mem := d.src.window(off, size)
		p := newParser(b, 0)
		p.partial = !whole(d.src, off, b, mem)
		p.noAlias = !mem
		err := parse(p)
		partial := p.partial
		p.free()
		if err == nil || !partial || size >= MaxObjectWindow {
			if errors.Is(err, errNeedMore) {
				err = &SyntaxError{int(off), "object larger than MaxObjectWindow"}
			}
			return err
		}
	}
}

// parseAt parses the indirect object at off, decrypting its strings and
// marking its stream for decryption with t's key.
func (d *Document) parseAt(ctx *loadCtx, t *table, off int64) (ref Ref, obj *Object, err error) {
	err = d.windowed(off, func(p *parser) error {
		ref, obj, err = d.parseIndirect(ctx, t, p)
		return err
	})
	return ref, obj, err
}

func (d *Document) parseIndirect(ctx *loadCtx, t *table, p *parser) (Ref, *Object, error) {
	p.length = func(r Ref) Object {
		o, err := d.get(ctx, t, r.Num)
		if err != nil {
			return Null
		}
		return d.resolve(ctx, t, o)
	}
	dec := t.dec
	var cryptHook func(Ref, bool, []Entry) func([]byte) []byte
	if dec != nil && dec.strings != cryptNone {
		p.copyStrings = true
		cryptHook = func(r Ref, stream bool, e []Entry) func([]byte) []byte {
			if stream && isXRef(e) {
				return nil
			}
			return dec.stringCrypt(r.Num, r.Gen)
		}
	}
	ref, obj, err := p.indirect(cryptHook)
	if err != nil {
		return ref, nil, err
	}
	if st, ok := obj.Stream(); ok && dec != nil && dec.streams != cryptNone &&
		!dec.skips(ref.Num) && !isXRef(st.Dict.e) && !streamIsPlain(st.Dict) &&
		(!dec.plainMetadata || !hasType(st.Dict.e, "Metadata")) {
		st.dec = dec
	}
	return ref, obj, nil
}

func isXRef(e []Entry) bool { return hasType(e, "XRef") }

// hasType reports whether a dictionary's /Type is t.
func hasType(e []Entry, t Name) bool {
	for _, x := range e {
		if x.Key == "Type" {
			n, _ := x.Val.Name()
			return n == t
		}
	}
	return false
}

// retryAfterRepair rebuilds the tables once and looks the object up again.
func (d *Document) retryAfterRepair(ctx *loadCtx, t *table, num int32, cause error) (*Object, error) {
	if t.repaired {
		return new(Object), nil
	}
	nt, err := d.repairOnce(ctx, t)
	if err != nil {
		return nil, cause
	}
	p := new(Object)
	if e := nt.entry(num); e != nil && e.kind == 'n' && e.off < d.src.size() && e.off >= 0 {
		if got, o, err := d.parseAt(ctx, nt, e.off); err == nil && got.Num == num {
			p = o
		}
	}
	if e := nt.entry(num); e != nil {
		// What the lookup found is what the rebuilt table holds from now on.
		e.obj.Store(p)
	}
	return p, nil
}

// repairOnce rebuilds the tables of t unless another call already has, and
// makes the new table the document's.
func (d *Document) repairOnce(ctx *loadCtx, t *table) (*table, error) {
	d.repairMu.Lock()
	defer d.repairMu.Unlock()
	if cur := d.tab.Load(); cur != nil && cur != t {
		return cur, nil
	}
	nt, err := d.repair(ctx, t)
	if err != nil {
		return nil, err
	}
	d.tab.Store(nt)
	return nt, nil
}

// fromObjectStream reads an object held inside a /Type /ObjStm stream.
func (d *Document) fromObjectStream(ctx *loadCtx, t *table, num, stm int32) (*Object, error) {
	objs, err := d.objectStream(ctx, t, stm)
	if err != nil {
		return nil, err
	}
	if p, ok := objs[num]; ok {
		return p, nil
	}
	// The index is a hint; a stream that does not hold the object simply
	// does not define it.
	return new(Object), nil
}

// objectStream parses an object stream once and keeps its contents.
func (d *Document) objectStream(ctx *loadCtx, t *table, num int32) (map[int32]*Object, error) {
	t.stmMu.Lock()
	objs, ok := t.objStms[num]
	t.stmMu.Unlock()
	if ok {
		return objs, nil
	}
	if !ctx.enter(stmKey(num)) {
		return nil, nil
	}
	defer ctx.leave()
	objs, err := d.parseObjectStream(ctx, t, num)
	t.stmMu.Lock()
	if prev, ok := t.objStms[num]; ok {
		objs = prev
	} else {
		t.objStms[num] = objs
	}
	t.stmMu.Unlock()
	return objs, err
}

func (d *Document) parseObjectStream(ctx *loadCtx, t *table, num int32) (map[int32]*Object, error) {
	objs := map[int32]*Object{}
	o, err := d.get(ctx, t, num)
	if err != nil {
		return objs, err
	}
	s, ok := o.Stream()
	if !ok {
		return objs, nil
	}
	// An object stream that stops in the middle still holds whole objects
	// in the part that did decode.
	dec := d.decode(ctx, t, s)
	if dec.Image != "" {
		return objs, nil
	}
	data := dec.Data
	n := int(intOr(s.Dict.Get("N"), 0))
	first := int(intOr(s.Dict.Get("First"), 0))
	if n <= 0 || first <= 0 || first > len(data) {
		return objs, nil
	}
	l := &lexer{buf: data[:first]}
	type pair struct {
		num int32
		off int
	}
	pairs := make([]pair, 0, min(n, first/2+1))
	for range n {
		t1, err1 := l.next()
		t2, err2 := l.next()
		if err1 != nil || err2 != nil || t1.kind != tokInteger || t2.kind != tokInteger {
			break
		}
		pairs = append(pairs, pair{clamp32(t1.num.Int), first + int(t2.num.Int)})
	}
	for _, pr := range pairs {
		if pr.off < 0 || pr.off > len(data) || pr.num < 0 {
			continue
		}
		p := newParser(data, pr.off)
		o, err := p.object()
		p.free()
		if err != nil {
			continue
		}
		objs[pr.num] = &o
	}
	return objs, nil
}

// Catalog returns the document catalogue the trailer's /Root names.
func (d *Document) Catalog() (Dict, error) {
	ctx := getCtx()
	defer putCtx(ctx)
	t := d.tab.Load()
	return d.catalog(ctx, t, t.trailer)
}

func (d *Document) catalog(ctx *loadCtx, t *table, trailer Dict) (Dict, error) {
	if trailer.IsZero() {
		return Dict{}, fmt.Errorf("pdf: the file has no trailer")
	}
	o := trailer.Get("Root")
	if r, ok := o.Ref(); ok {
		v, err := d.get(ctx, t, r.Num)
		if err != nil {
			return Dict{}, err
		}
		o = d.resolve(ctx, t, v)
	}
	cat, ok := o.Dict()
	if !ok {
		return Dict{}, fmt.Errorf("pdf: /Root is a %s, not a dictionary", o.kind)
	}
	if _, ok := d.resolve(ctx, t, cat.Get("Pages")).Dict(); !ok {
		return Dict{}, fmt.Errorf("pdf: the catalogue has no page tree")
	}
	return cat, nil
}

// PageCount reports how many pages the document has.
func (d *Document) PageCount() int { return len(d.pageRefs(d.tab.Load())) }

// PageRef returns the reference of the i'th page, counting from one.
func (d *Document) PageRef(i int) (Ref, bool) {
	refs := d.pageRefs(d.tab.Load())
	if i < 1 || i > len(refs) {
		return Ref{}, false
	}
	return refs[i-1], true
}

// Page returns the i'th page's dictionary, counting from one, with the
// attributes it inherits from its ancestors filled in. It is built once.
func (d *Document) Page(i int) (Dict, error) {
	info, err := d.pageInfo(i)
	if err != nil {
		return Dict{}, err
	}
	return info.dict, nil
}

// A pageInfo is what a page's dictionary says, read once.
type pageInfo struct {
	dict Dict
	// key stands for the page's joined content streams in the cache.
	key *Stream
}

func (d *Document) pageInfo(i int) (*pageInfo, error) {
	t := d.tab.Load()
	refs := d.pageRefs(t)
	if i < 1 || i > len(refs) {
		return nil, fmt.Errorf("pdf: page %d is out of range (the document has %d)", i, len(refs))
	}
	slot := &t.pageInfo[i-1]
	if p := slot.Load(); p != nil {
		return p, nil
	}
	o, err := d.Get(refs[i-1])
	if err != nil {
		return nil, err
	}
	page, ok := o.Dict()
	if !ok {
		return nil, fmt.Errorf("pdf: page %d is a %s, not a dictionary", i, o.kind)
	}
	p := &pageInfo{dict: d.withInherited(page), key: &Stream{Ref: refs[i-1]}}
	if !slot.CompareAndSwap(nil, p) {
		p = slot.Load()
	}
	return p, nil
}

// PageContents returns the page's content streams decoded and joined, a
// line feed between them. Recovered reports that at least one of them could
// not be decoded cleanly; a stream filtered as an image is not content and
// is reported, not joined.
func (d *Document) PageContents(i int) (Decoded, error) {
	info, err := d.pageInfo(i)
	if err != nil {
		return Decoded{}, err
	}
	o := d.Resolve(info.dict.Get("Contents"))
	if s, ok := o.Stream(); ok {
		return contentPart(d.Decode(s)), nil
	}
	a, ok := o.Array()
	if !ok {
		return Decoded{}, nil
	}
	return d.cache.get(info.key, func() Decoded {
		var out Decoded
		for _, e := range a {
			s, ok := d.Resolve(e).Stream()
			if !ok {
				continue
			}
			part := contentPart(d.DecodeUncached(s))
			if part.Recovered && !out.Recovered {
				out.Recovered, out.Cause, out.Filter = true, part.Cause, part.Filter
				out.Undecoded = part.Undecoded
			}
			if len(part.Data) == 0 {
				continue
			}
			if len(out.Data) > 0 {
				out.Data = append(out.Data, '\n')
			}
			out.Data = append(out.Data, part.Data...)
		}
		return out
	}), nil
}

// contentPart is one decoded content stream. An image filter has no
// business there: the bytes it holds are salvage, not content.
func contentPart(dec Decoded) Decoded {
	if dec.Image != "" {
		return Decoded{
			Undecoded: dec.Data,
			Recovered: true,
			Filter:    dec.Image,
			Cause:     fmt.Errorf("pdf: a content stream is filtered as an image (/%s)", dec.Image),
		}
	}
	return dec
}

// withInherited copies the page and fills in what its ancestors provide.
func (d *Document) withInherited(page Dict) Dict {
	var missing []Name
	for _, k := range inheritable {
		if !page.Has(k) {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return page
	}
	out := make([]Entry, len(page.e), len(page.e)+len(missing))
	copy(out, page.e)
	node := page
	for depth := 0; depth < MaxRefChain && len(missing) > 0; depth++ {
		parent, ok := d.GetDict(node, "Parent")
		if !ok {
			break
		}
		kept := missing[:0]
		for _, k := range missing {
			if parent.Has(k) {
				out = append(out, Entry{k, parent.Get(k)})
			} else {
				kept = append(kept, k)
			}
		}
		missing = kept
		node = parent
	}
	return Dict{normalise(out)}
}

// pageRefs walks t's page tree once, in document order.
func (d *Document) pageRefs(t *table) []Ref {
	t.pagesOnce.Do(func() {
		ctx := getCtx()
		defer putCtx(ctx)
		cat, err := d.catalog(ctx, t, t.trailer)
		if err != nil {
			t.pages = []Ref{}
			return
		}
		w := pageWalk{d: d, ctx: ctx, t: t, seen: map[Ref]bool{}, pages: []Ref{}}
		w.walk(cat.Get("Pages"), 0)
		if len(w.pages) == 0 {
			// A tree that yields nothing: every object that calls itself a
			// page, in object-number order.
			for _, num := range d.objectsOfType(ctx, t, "Page") {
				w.pages = append(w.pages, Ref{Num: num})
			}
		}
		t.pages = w.pages
		t.pageInfo = make([]atomic.Pointer[pageInfo], len(w.pages))
	})
	return t.pages
}

type pageWalk struct {
	d     *Document
	ctx   *loadCtx
	t     *table
	seen  map[Ref]bool
	pages []Ref
}

// walk appends the leaves of a page tree node in order.
func (w *pageWalk) walk(node Object, depth int) {
	if depth > MaxPageTreeDepth || len(w.pages) >= maxPageTreeNodes {
		return
	}
	ref, isRef := node.Ref()
	if isRef {
		if w.seen[ref] {
			return
		}
		w.seen[ref] = true
	}
	dict, ok := w.d.resolve(w.ctx, w.t, node).Dict()
	if !ok {
		return
	}
	if t, ok := dict.Get("Type").Name(); ok && t == "Page" {
		if isRef {
			w.pages = append(w.pages, ref)
		}
		return
	}
	kids, ok := w.d.resolve(w.ctx, w.t, dict.Get("Kids")).Array()
	if !ok {
		if isRef {
			w.pages = append(w.pages, ref)
		}
		return
	}
	for _, kid := range kids {
		w.walk(kid, depth+1)
	}
}

// objectsOfType lists, in object-number order, the objects whose /Type is
// the given name.
func (d *Document) objectsOfType(ctx *loadCtx, t *table, want Name) []int32 {
	var out []int32
	for _, num := range t.nums {
		o, err := d.get(ctx, t, num)
		if err != nil {
			continue
		}
		dict, ok := o.Dict()
		if !ok {
			continue
		}
		if n, ok := dict.Get("Type").Name(); ok && n == want {
			out = append(out, num)
		}
	}
	return out
}

// Raw returns a stream's bytes as stored, decrypted but still filtered.
func (d *Document) Raw(s *Stream) []byte {
	if s.dec != nil {
		return s.dec.decryptStream(s.Ref.Num, s.Ref.Gen, s.raw)
	}
	return s.raw
}

// Decode applies a stream's filter chain, salvaging what it can from a
// chain that cannot be run to the end and saying that it did so. The result
// is cached and shared: its bytes must not be written to.
func (d *Document) Decode(s *Stream) Decoded {
	if s.Ref.Num <= 0 {
		return d.DecodeUncached(s)
	}
	return d.cache.get(s, func() Decoded { return d.DecodeUncached(s) })
}

// DecodeUncached is Decode without the cache, for streams whose consumer
// keeps what it makes of them: images, font programs.
func (d *Document) DecodeUncached(s *Stream) Decoded {
	ctx := getCtx()
	defer putCtx(ctx)
	return d.decode(ctx, d.tab.Load(), s)
}

// DecodeStrict applies a stream's filter chain; a chain that cannot be run
// to the end is an error. When the chain ends in an image filter, its name
// is returned with the bytes still encoded in it.
func (d *Document) DecodeStrict(s *Stream) ([]byte, Name, error) {
	r := d.Decode(s)
	if r.Recovered {
		return nil, "", r.Cause
	}
	return r.Data, r.Image, nil
}

func (d *Document) decode(ctx *loadCtx, t *table, s *Stream) Decoded {
	resolve := func(o Object) Object { return d.resolve(ctx, t, o) }
	return decodeChain(s.Dict, d.Raw(s), resolve)
}

// DecodeBytes applies the filter chain of dict to raw, following references
// through the document, without the cache: for data that is not a stream
// of the file, such as an inline image, or that is decoded once.
func (d *Document) DecodeBytes(dict Dict, raw []byte) Decoded {
	ctx := getCtx()
	defer putCtx(ctx)
	t := d.tab.Load()
	return decodeChain(dict, raw, func(o Object) Object { return d.resolve(ctx, t, o) })
}

// Resolver returns the document's Resolve as a function value.
func (d *Document) Resolver() func(Object) Object { return d.resolveFn }
