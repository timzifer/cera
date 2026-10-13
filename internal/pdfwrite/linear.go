package pdfwrite

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"fmt"
	"io"
	"math/bits"
	"slices"
	"strings"

	"github.com/timzifer/cera/internal/pdf"
)

// A linearised file (PDF 2.0, Annex F) shows its first page before the
// rest has arrived. Its parts, in the order of the file:
//
//	the header
//	the linearisation dictionary, the first object
//	the cross-reference section of the first page, whose trailer is the
//	    file's and leads on to the main section by /Prev
//	the catalogue and what a reader needs to open the document (part 4)
//	the hint stream (part 5)
//	the first page: its object, what it alone uses, what it shares (part 6)
//	the other pages, each with what it alone uses (part 7)
//	what several of them share (part 8)
//	everything else (part 9)
//	the main cross-reference section, of parts 7 to 9
//
// The objects of parts 7 to 9 have the low numbers, those of the first
// half follow. The hint stream says where the objects of each page are and
// which shared ones it needs, so that a reader fetches a page with few
// requests.
//
// Which part an object belongs to follows the rules qpdf checks a file by
// (QPDF_linearization.cc), so that its check passes: Acrobat's own files
// depart from the specification in places, and qpdf's reading of it is
// what other tools are measured against.

// Linear lays a new file out linearised.
type Linear struct {
	// Pages are the page objects, in the order of the pages. Their
	// attributes are their own: nothing is inherited from the page tree.
	Pages []Ref
}

// linCat is the part of the file an object goes to, by who refers to it.
// The order matters: of two, the lower one mostly wins.
type linCat int8

const (
	catRoot         linCat = iota // the catalogue
	catOpen                       // needed to open the document
	catFirstShared                // the first page and others
	catFirstPrivate               // the first page alone
	catOtherShared                // several other pages
	catOtherPrivate               // one other page
	catThumbShared                // several thumbnails
	catThumbPrivate               // one thumbnail
	catPages                      // the page tree
	catOutlinesObj                // the outline dictionary
	catOutlines                   // the outline
	catOther
)

// linState is where an object stands after the references seen so far:
// its category, and the first page (or thumbnail) that refers to it, -1
// for none.
type linState struct {
	cat  linCat
	page int32
}

// linUser is what a walk starts from: a page, a thumbnail, an entry of the
// catalogue or of the trailer.
type linUser struct {
	cat  linCat
	page int32
}

func (u linUser) isPage() bool { return u.cat == catFirstPrivate || u.cat == catOtherPrivate }

// update folds one more reference into s: from user u, directly (top) or
// through other objects.
func (s *linState) update(u linUser, top bool) {
	cat, page := u.cat, int32(-1)
	if u.isPage() || u.cat == catThumbPrivate {
		page = u.page
	}
	if top && cat == catOutlines {
		cat = catOutlinesObj
	}
	lo, hi := min(s.cat, cat), max(s.cat, cat)
	had := s.page
	if had <= 0 || page <= 0 {
		s.page = max(had, page)
	} else {
		s.page = min(had, page)
	}
	switch {
	case lo <= catFirstShared:
		// Part 4, or the first page and another: nothing changes that.
		s.cat = lo
	case lo == catFirstPrivate:
		s.cat = catFirstPrivate
		if hi == catOtherPrivate || hi == catOtherShared {
			s.cat = catFirstShared
		}
	case lo == catOtherShared:
		s.cat = catOtherShared
	case lo == catOtherPrivate:
		s.cat = catOtherPrivate
		if hi == catOtherPrivate && had != page {
			s.cat = catOtherShared
		}
	case lo == catThumbShared:
		s.cat = catThumbShared
	case lo == catThumbPrivate:
		s.cat = catThumbPrivate
		if hi == catThumbPrivate && had != page {
			s.cat = catThumbShared
		}
	default:
		s.cat = lo
	}
}

// A linUnit is one object of the file: an object of the writer, or an
// object stream with some of them.
type linUnit struct {
	num     int32   // its number in the file
	old     int32   // the writer's object, 0 for an object stream
	members []int32 // the writer's objects in an object stream
	shared  bool    // listed for the pages that need it
	data    []byte
}

type linearizer struct {
	w    *Writer
	root int32
	vals []pdf.Object // the writer's objects, by number - 1

	state  []linState // by the writer's number
	placed []bool
	thumb  []bool  // a thumbnail refers to it
	stamp  []int32 // the walk that saw it last
	walk   int32
	// pageObjs lists, per page, the objects a walk from it reached.
	pageObjs [][]int32
	pageOf   map[int32]int

	renum []int32    // the number in the file, by the writer's number
	unit  []*linUnit // the unit an object is, or is in, by the writer's number
}

// name returns o as a name, following one reference.
func (l *linearizer) name(o pdf.Object) pdf.Name {
	if r, ok := o.Ref(); ok {
		if r.Num <= 0 || int(r.Num) > len(l.vals) {
			return ""
		}
		o = l.vals[r.Num-1]
	}
	n, _ := o.Name()
	return n
}

// null reports whether o is null or refers to nothing.
func (l *linearizer) null(o pdf.Object) bool {
	if r, ok := o.Ref(); ok {
		return r.Num <= 0 || int(r.Num) > len(l.vals) || l.vals[r.Num-1].IsNull()
	}
	return o.IsNull()
}

type linFrame struct {
	v   pdf.Object
	u   linUser
	top bool
}

// sorted returns the entries of d by key.
func sorted(d pdf.Dict) []pdf.Entry {
	es := slices.Clone(d.Entries())
	slices.SortFunc(es, func(a, b pdf.Entry) int { return strings.Compare(string(a.Key), string(b.Key)) })
	return es
}

// traverse walks what start leads to for user u, without crossing into
// other pages or up the page tree, and folds u into every object it meets.
// The order of the walk is qpdf's — the entries of a dictionary by key,
// the last one first — because what a page and its thumbnail both lead to
// counts for the one that gets there first.
func (l *linearizer) traverse(u linUser, start pdf.Object) {
	l.walk++
	stack := []linFrame{{start, u, true}}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		v, pageNode := f.v, false
		if r, ok := v.Ref(); ok {
			num := r.Num
			if num <= 0 || int(num) > len(l.vals) || l.stamp[num] == l.walk {
				continue
			}
			l.stamp[num] = l.walk
			v = l.vals[num-1]
			if d, ok := v.Dict(); ok && v.Kind() == pdf.KindDict {
				switch l.name(d.Get("Type")) {
				case "Page":
					pageNode = true
					if !f.top {
						continue
					}
				case "Pages":
					if u.isPage() {
						continue
					}
				}
			}
			l.state[num].update(f.u, f.top)
			l.placed[num] = true
			if f.u.cat == catThumbPrivate {
				l.thumb[num] = true
			}
			if f.u.isPage() {
				l.pageObjs[f.u.page] = append(l.pageObjs[f.u.page], num)
			}
		}
		switch v.Kind() {
		case pdf.KindArray:
			a, _ := v.Array()
			for _, e := range a {
				stack = append(stack, linFrame{e, f.u, false})
			}
		case pdf.KindDict, pdf.KindStream:
			d, _ := v.Dict()
			for _, e := range sorted(d) {
				switch {
				case l.null(e.Val):
				case pageNode && e.Key == "Thumb":
					stack = append(stack, linFrame{e.Val, linUser{catThumbPrivate, f.u.page}, false})
				case pageNode && e.Key == "Parent":
					// Not back up the page tree.
				default:
					stack = append(stack, linFrame{e.Val, f.u, false})
				}
			}
		}
	}
}

// refs calls f for every reference in v.
func refs(v pdf.Object, depth int, f func(num int32)) {
	if depth > maxDepth {
		return
	}
	switch v.Kind() {
	case pdf.KindRef:
		r, _ := v.Ref()
		f(r.Num)
	case pdf.KindArray:
		a, _ := v.Array()
		for _, e := range a {
			refs(e, depth+1, f)
		}
	case pdf.KindDict, pdf.KindStream:
		d, _ := v.Dict()
		for _, e := range d.All() {
			refs(e, depth+1, f)
		}
	}
}

// categorise finds the part of every object: by walks from the pages, the
// entries of the trailer and those of the catalogue, in that order.
func (l *linearizer) categorise(t Trailer, pages []Ref) {
	n := len(l.vals)
	l.state = make([]linState, n+1)
	for i := range l.state {
		l.state[i] = linState{catOther, -1}
	}
	l.placed, l.thumb, l.stamp = make([]bool, n+1), make([]bool, n+1), make([]int32, n+1)
	l.pageObjs = make([][]int32, len(pages))
	for i, p := range pages {
		u := linUser{catOtherPrivate, int32(i)}
		if i == 0 {
			u.cat = catFirstPrivate
		}
		l.traverse(u, p.Object())
	}
	if l.w.Crypt != nil && l.w.Encrypt != 0 {
		l.traverse(linUser{catOpen, -1}, l.w.Encrypt.Object())
	}
	if t.Info != 0 {
		l.traverse(linUser{catOther, -1}, t.Info.Object())
	}
	cat, _ := l.vals[l.root-1].Dict()
	for _, e := range sorted(cat) {
		if l.null(e.Val) {
			continue
		}
		u := linUser{catOther, -1}
		switch e.Key {
		case "ViewerPreferences", "PageMode", "Threads", "OpenAction", "AcroForm":
			u.cat = catOpen
		case "Outlines":
			u.cat = catOutlines
		case "Pages":
			u.cat = catPages
		}
		l.traverse(u, e.Val)
	}
	l.state[l.root].update(linUser{catRoot, -1}, true)
	l.placed[l.root] = true
	// A page is its page's, whatever else refers to it.
	for i, p := range pages {
		l.state[p] = linState{catOtherPrivate, int32(i)}
		if i == 0 {
			l.state[p].cat = catFirstPrivate
		}
		l.placed[p] = true
	}
	for i := range l.pageObjs {
		slices.Sort(l.pageObjs[i])
		l.pageObjs[i] = slices.Compact(l.pageObjs[i])
	}
}

// orphans returns the objects no walk reached that something written
// still refers to — a page that is not in the page tree —, with what they
// lead to. They are written last. Objects nothing refers to are not
// written at all.
func (l *linearizer) orphans() []int32 {
	var out []int32
	var todo []int32
	see := func(num int32) {
		if num > 0 && int(num) <= len(l.vals) && !l.placed[num] {
			l.placed[num] = true
			todo = append(todo, num)
		}
	}
	for num := int32(1); int(num) <= len(l.vals); num++ {
		if l.placed[num] {
			refs(l.vals[num-1], 0, see)
		}
	}
	for len(todo) > 0 {
		num := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		out = append(out, num)
		refs(l.vals[num-1], 0, see)
	}
	slices.Sort(out)
	return out
}

// units makes the objects of the file of the writer's objects nums, which
// are of one kind and one page: each by itself, or, when the file is
// packed, those that may be in an object stream collected into some, after
// the others. Not in one are streams, the catalogue, pages, the encryption
// dictionary, the outline dictionary, which the hints name, and what a
// thumbnail refers to, whose place depends on the order of a walk.
func (l *linearizer) units(nums []int32, shared bool) []*linUnit {
	var out []*linUnit
	var packed []int32
	for _, num := range nums {
		_, isPage := l.pageOf[num]
		if l.w.Packed && l.vals[num-1].Kind() != pdf.KindStream && num != l.root && !isPage &&
			Ref(num) != l.w.Encrypt && !l.thumb[num] && l.state[num].cat != catOutlinesObj {
			packed = append(packed, num)
			continue
		}
		u := &linUnit{old: num, shared: shared}
		l.unit[num] = u
		out = append(out, u)
	}
	for len(packed) > 0 {
		n := min(len(packed), objStmSize)
		u := &linUnit{members: packed[:n], shared: shared}
		for _, num := range u.members {
			l.unit[num] = u
		}
		out = append(out, u)
		packed = packed[n:]
	}
	return out
}

// number numbers units and then, from *next, leaves the numbers of the
// objects in their object streams to members.
func number(units []*linUnit, next *int32) {
	for _, u := range units {
		u.num = *next
		*next++
	}
}

func (l *linearizer) members(units []*linUnit, next *int32) {
	for _, u := range units {
		if u.old != 0 {
			l.renum[u.old] = u.num
		}
		for _, m := range u.members {
			l.renum[m] = *next
			*next++
		}
	}
}

// encode writes the bytes of unit u.
func (l *linearizer) encode(u *linUnit) {
	w := l.w
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	e := &encoder{b: sink{bw}, w: w, renum: l.renum, pos: func() int64 { return 0 }}
	crypt := w.Crypt
	var o pdf.Object
	if u.old != 0 {
		o = l.vals[u.old-1]
		if Ref(u.old) == w.Encrypt {
			crypt = nil // the encryption dictionary is not encrypted
		}
	} else {
		// Inside an object stream nothing is encrypted but the stream.
		var head, body bytes.Buffer
		pb := bufio.NewWriter(&body)
		pe := &encoder{b: sink{pb}, w: w, renum: l.renum, pos: func() int64 { return 0 }}
		for _, m := range u.members {
			_ = pb.Flush()
			fmt.Fprintf(&head, "%d %d ", l.renum[m], body.Len())
			pe.object(l.renum[m], 0, nil)
			pe.value(l.vals[m-1], 0)
			_ = pb.WriteByte('\n')
		}
		_ = pb.Flush()
		dict := pdf.NewDict(
			pdf.Entry{Key: "Type", Val: pdf.Name("ObjStm").Object()},
			pdf.Entry{Key: "N", Val: pdf.Integer(int64(len(u.members)))},
			pdf.Entry{Key: "First", Val: pdf.Integer(int64(head.Len()))},
		)
		o = Flate(dict, append(head.Bytes(), body.Bytes()...)).Object()
	}
	e.b.printf("%d 0 obj\n", u.num)
	e.object(u.num, 0, crypt)
	e.value(o, 0)
	e.b.str("\nendobj\n")
	_ = bw.Flush()
	u.data = buf.Bytes()
}

// bitWriter packs the fields of the hint tables, the high bit first.
type bitWriter struct {
	buf  []byte
	free int // bits free in the last byte
}

func (b *bitWriter) write(v uint64, n int) {
	for n > 0 {
		if b.free == 0 {
			b.buf = append(b.buf, 0)
			b.free = 8
		}
		k := min(n, b.free)
		chunk := byte(v>>(n-k)) & byte(1<<k-1)
		b.buf[len(b.buf)-1] |= chunk << (b.free - k)
		b.free -= k
		n -= k
	}
}

// flush ends a row of a table: the next one starts at a byte.
func (b *bitWriter) flush() { b.free = 0 }

// linearisable reports whether the file can be laid out linearised: it has
// pages, each an object of its own, and a catalogue.
func (w *Writer) linearisable(t Trailer) bool {
	in := func(r Ref) bool { return r > 0 && int(r) <= len(w.objs) }
	if w.Linear == nil || w.base != nil || len(w.Linear.Pages) == 0 || !in(t.Root) || t.Info != 0 && !in(t.Info) {
		return false
	}
	if s := w.objs[t.Root-1]; s.imp != nil || s.o.Kind() != pdf.KindDict {
		return false
	}
	seen := map[Ref]bool{t.Root: true}
	for _, p := range w.Linear.Pages {
		if !in(p) || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}

// writeLinear writes the file linearised. Unlike the other forms it is
// laid out before it is written — the first object says how long the file
// is, the hints where everything in it lies — so its objects are held in
// memory, serialised, until the last one is known.
func (w *Writer) writeLinear(out io.Writer, t Trailer) error {
	pages := w.Linear.Pages
	var vals []pdf.Object
	for i := 0; i < len(w.objs); i++ {
		s := w.objs[i]
		w.objs[i] = slot{}
		if s.imp != nil {
			s.o = s.imp.Map(s.o)
		}
		vals = append(vals, s.o)
	}
	n := len(vals)
	l := &linearizer{w: w, root: int32(t.Root), vals: vals, pageOf: map[int32]int{}}
	for i, p := range pages {
		l.pageOf[int32(p)] = i
	}
	l.categorise(t, pages)
	orphans := l.orphans()

	// The objects by part, in the writer's order within each.
	var open, firstPrivate, firstShared, otherShared, pagesObj, thumbShared, outlinesObj, outlines, other []int32
	private := make([][]int32, len(pages))
	thumbs := make([][]int32, len(pages))
	orphan := map[int32]bool{}
	for _, num := range orphans {
		orphan[num] = true
	}
	for num := int32(1); int(num) <= n; num++ {
		if !l.placed[num] || orphan[num] || num == l.root {
			continue
		}
		if _, ok := l.pageOf[num]; ok {
			continue
		}
		s := l.state[num]
		if s.page < 0 || int(s.page) >= len(pages) {
			s.page = 0 // of no page: only for the categories that have none
		}
		switch s.cat {
		case catRoot, catOpen:
			open = append(open, num)
		case catFirstShared:
			firstShared = append(firstShared, num)
		case catFirstPrivate:
			firstPrivate = append(firstPrivate, num)
		case catOtherShared:
			otherShared = append(otherShared, num)
		case catOtherPrivate:
			private[s.page] = append(private[s.page], num)
		case catThumbShared:
			thumbShared = append(thumbShared, num)
		case catThumbPrivate:
			thumbs[s.page] = append(thumbs[s.page], num)
		case catPages:
			pagesObj = append(pagesObj, num)
		case catOutlinesObj:
			outlinesObj = append(outlinesObj, num)
		case catOutlines:
			outlines = append(outlines, num)
		default:
			other = append(other, num)
		}
	}
	root, _ := vals[l.root-1].Dict()
	outlinesFirst := l.name(root.Get("PageMode")) == "UseOutlines" && !l.null(root.Get("Outlines"))

	l.unit = make([]*linUnit, n+1)
	part4 := append(l.units([]int32{l.root}, false), l.units(open, false)...)
	part6 := append(l.units([]int32{int32(pages[0])}, false), l.units(firstPrivate, false)...)
	part6 = append(part6, l.units(firstShared, true)...)
	outlineUnits := append(l.units(outlinesObj, false), l.units(outlines, false)...)
	if outlinesFirst {
		part6 = append(part6, outlineUnits...)
	}
	pageUnits := make([][]*linUnit, len(pages))
	pageUnits[0] = part6
	var second []*linUnit
	for i := 1; i < len(pages); i++ {
		pageUnits[i] = append(l.units([]int32{int32(pages[i])}, false), l.units(private[i], false)...)
		second = append(second, pageUnits[i]...)
	}
	part8 := l.units(otherShared, true)
	second = append(second, part8...)
	second = append(second, l.units(pagesObj, false)...)
	for i := range thumbs {
		second = append(second, l.units(thumbs[i], false)...)
	}
	second = append(second, l.units(thumbShared, false)...)
	if !outlinesFirst {
		second = append(second, outlineUnits...)
	}
	second = append(second, l.units(other, false)...)
	second = append(second, l.units(orphans, false)...)

	// The numbers: the second half, then the first.
	l.renum = make([]int32, n+1)
	next := int32(1)
	number(second, &next)
	mainXref := int32(0)
	if w.Packed {
		mainXref = next
		next++
	}
	l.members(second, &next)
	firstStart := next
	linDict := next
	next++
	firstXref := int32(0)
	if w.Packed {
		firstXref = next
		next++
	}
	number(part4, &next)
	hint := next
	next++
	number(part6, &next)
	l.members(part4, &next)
	l.members(part6, &next)
	total := next

	// The bytes of the objects, and the identifier made of them.
	sum := md5.New()
	bodies := int64(0)
	for _, part := range [][]*linUnit{part4, part6, second} {
		for _, u := range part {
			l.encode(u)
			if u.old != 0 {
				vals[u.old-1] = pdf.Null
			}
			sum.Write(u.data)
			bodies += int64(len(u.data))
		}
	}
	l.vals = nil
	id2 := sum.Sum(nil)
	id1 := t.ID
	if id1 == nil {
		id1 = id2
	}
	id := fmt.Sprintf("/ID [<%x> <%x>]", id1, id2)
	trailer := fmt.Sprintf("/Root %d 0 R", l.renum[t.Root])
	if t.Info != 0 && l.renum[t.Info] != 0 {
		trailer += fmt.Sprintf(" /Info %d 0 R", l.renum[t.Info])
	}
	if w.Crypt != nil && w.Encrypt != 0 {
		trailer += fmt.Sprintf(" /Encrypt %d 0 R", l.renum[w.Encrypt])
	}
	trailer += " " + id

	version := w.Version
	if version == "" || w.Packed && version < "1.5" {
		version = "1.7"
	}
	header := fmt.Sprintf("%%PDF-%s\n%%\xe2\xe3\xcf\xd3\n", version)

	// Where everything lies, first as if there were no hint stream: that
	// is how the hints count, and the stream is made of them.
	const linRoom = 256 // of the linearisation dictionary, padded
	offWidth := 4       // bytes of an offset in a cross-reference stream
	for bodies+int64(64*total)+1<<20 >= 1<<(8*offWidth) {
		offWidth++
	}
	off := make([]int64, total) // by number in the file; 0 for one in an object stream
	// first returns the cross-reference section of the first half, whose
	// length does not depend on what is in it.
	first := func(prev int64) []byte {
		var b bytes.Buffer
		if !w.Packed {
			fmt.Fprintf(&b, "xref\n%d %d\n", firstStart, total-firstStart)
			for num := firstStart; num < total; num++ {
				fmt.Fprintf(&b, "%010d 00000 n\r\n", off[num])
			}
			fmt.Fprintf(&b, "trailer\n<</Size %d /Prev %-20d %s>>\nstartxref\n0\n%%%%EOF\n", total, prev, trailer)
			return b.Bytes()
		}
		width := 3 + offWidth
		rows := make([]byte, int(total-firstStart)*width)
		row := func(num int32, typ byte, a int64, c int) {
			r := rows[int(num-firstStart)*width:][:width]
			r[0] = typ
			for k := range offWidth {
				r[1+k] = byte(a >> (8 * (offWidth - 1 - k)))
			}
			r[width-2], r[width-1] = byte(c>>8), byte(c)
		}
		for num := firstStart; num < total; num++ {
			row(num, 1, off[num], 0)
		}
		for _, part := range [][]*linUnit{part4, part6} {
			for _, u := range part {
				for k, m := range u.members {
					row(l.renum[m], 2, int64(u.num), k)
				}
			}
		}
		fmt.Fprintf(&b, "%d 0 obj\n<</Type /XRef /Size %d /Index [%d %d] /W [1 %d 2] /Prev %-20d %s /Length %d>>\nstream\n",
			firstXref, total, firstStart, total-firstStart, offWidth, prev, trailer, len(rows))
		b.Write(rows)
		b.WriteString("\nendstream\nendobj\n")
		return b.Bytes()
	}
	pos := int64(len(header))
	off[linDict] = pos
	pos += linRoom
	firstOff := pos
	if w.Packed {
		off[firstXref] = pos
	}
	pos += int64(len(first(0)))
	place := func(units []*linUnit) {
		for _, u := range units {
			off[u.num] = pos
			pos += int64(len(u.data))
		}
	}
	place(part4)
	hintOff := pos
	place(part6)
	firstEnd := pos
	place(second)
	mainOff := pos

	// The hint stream (F.4): the page offset table, the shared object
	// table and, for a document with an outline, the outline table.
	length := func(units []*linUnit) (n int64) {
		for _, u := range units {
			n += int64(len(u.data))
		}
		return n
	}
	shared := append(slices.Clone(part6), part8...)
	index := map[*linUnit]int{}
	for i, u := range shared {
		index[u] = i
	}
	type pageHint struct {
		objects int
		length  int64
		shared  []int
	}
	hints := make([]pageHint, len(pages))
	minObjects, maxObjects := len(pageUnits[0]), 0
	minLength, maxLength := length(pageUnits[0]), int64(0)
	maxShared := 0
	for i := range pages {
		h := &hints[i]
		h.objects, h.length = len(pageUnits[i]), length(pageUnits[i])
		if i > 0 {
			seen := map[*linUnit]bool{}
			var us []*linUnit
			for _, num := range l.pageObjs[i] {
				if u := l.unit[num]; u != nil && u.shared && !seen[u] {
					seen[u] = true
					us = append(us, u)
				}
			}
			slices.SortFunc(us, func(a, b *linUnit) int { return int(a.num - b.num) })
			for _, u := range us {
				h.shared = append(h.shared, index[u])
			}
		}
		minObjects, maxObjects = min(minObjects, h.objects), max(maxObjects, h.objects)
		minLength, maxLength = min(minLength, h.length), max(maxLength, h.length)
		maxShared = max(maxShared, len(h.shared))
	}
	var hb bitWriter
	bitsObjects, bitsLength := bits.Len(uint(maxObjects-minObjects)), bits.Len64(uint64(maxLength-minLength))
	bitsShared, bitsIndex := bits.Len(uint(maxShared)), bits.Len(uint(len(shared)))
	hb.write(uint64(minObjects), 32)             // 1: the least number of objects of a page
	hb.write(uint64(off[pages0(pageUnits)]), 32) // 2: where the first page's object is
	hb.write(uint64(bitsObjects), 16)            // 3
	hb.write(uint64(minLength), 32)              // 4: the least length of a page
	hb.write(uint64(bitsLength), 16)             // 5
	hb.write(0, 32)                              // 6: the least offset of content
	hb.write(0, 16)                              // 7
	hb.write(uint64(minLength), 32)              // 8: the least length of content
	hb.write(uint64(bitsLength), 16)             // 9
	hb.write(uint64(bitsShared), 16)             // 10
	hb.write(uint64(bitsIndex), 16)              // 11
	hb.write(0, 16)                              // 12: no numerators
	hb.write(4, 16)                              // 13: the denominator, unused
	for _, h := range hints {
		hb.write(uint64(h.objects-minObjects), bitsObjects)
	}
	hb.flush()
	for _, h := range hints {
		hb.write(uint64(h.length-minLength), bitsLength)
	}
	hb.flush()
	for _, h := range hints {
		hb.write(uint64(len(h.shared)), bitsShared)
	}
	hb.flush()
	for _, h := range hints {
		for _, s := range h.shared {
			hb.write(uint64(s), bitsIndex)
		}
	}
	hb.flush()
	// The numerators and the offsets of content have no bits.
	for _, h := range hints {
		hb.write(uint64(h.length-minLength), bitsLength)
	}
	hb.flush()

	sharedAt := len(hb.buf)
	minGroup, maxGroup := int64(len(shared[0].data)), int64(0)
	for _, u := range shared {
		minGroup, maxGroup = min(minGroup, int64(len(u.data))), max(maxGroup, int64(len(u.data)))
	}
	bitsGroup := bits.Len64(uint64(maxGroup - minGroup))
	firstSharedNum, firstSharedOff := int32(0), int64(0)
	if len(part8) > 0 {
		firstSharedNum, firstSharedOff = part8[0].num, off[part8[0].num]
	}
	hb.write(uint64(firstSharedNum), 32) // 1: the first object of part 8
	hb.write(uint64(firstSharedOff), 32) // 2
	hb.write(uint64(len(part6)), 32)     // 3: the objects of the first page
	hb.write(uint64(len(shared)), 32)    // 4: with those of part 8
	hb.write(0, 16)                      // 5: a group is one object
	hb.write(uint64(minGroup), 32)       // 6
	hb.write(uint64(bitsGroup), 16)      // 7
	for _, u := range shared {
		hb.write(uint64(int64(len(u.data))-minGroup), bitsGroup)
	}
	hb.flush()
	for range shared {
		hb.write(0, 1) // no signature
	}
	hb.flush()

	hintDict := []pdf.Entry{{Key: "S", Val: pdf.Integer(int64(sharedAt))}}
	if len(outlineUnits) > 0 {
		hintDict = append(hintDict, pdf.Entry{Key: "O", Val: pdf.Integer(int64(len(hb.buf)))})
		hb.write(uint64(outlineUnits[0].num), 32)
		hb.write(uint64(off[outlineUnits[0].num]), 32)
		hb.write(uint64(len(outlineUnits)), 32)
		hb.write(uint64(length(outlineUnits)), 32)
	}
	var hintBuf bytes.Buffer
	{
		bw := bufio.NewWriter(&hintBuf)
		e := &encoder{b: sink{bw}, w: w, renum: l.renum, pos: func() int64 { return 0 }}
		e.b.printf("%d 0 obj\n", hint)
		e.object(hint, 0, w.Crypt)
		e.value(Flate(pdf.NewDict(hintDict...), hb.buf).Object(), 0)
		e.b.str("\nendobj\n")
		_ = bw.Flush()
	}
	hintLen := int64(hintBuf.Len())

	// Now with the hint stream where it is.
	for num := range off {
		if off[num] >= hintOff {
			off[num] += hintLen
		}
	}
	off[hint] = hintOff
	firstEnd += hintLen
	mainOff += hintLen

	// The main cross-reference section, of the second half.
	var main bytes.Buffer
	beforeZero := mainOff - 1
	if w.Packed {
		off[mainXref] = mainOff
		xs := []xentry{{num: 0, gen: 65535, free: true}}
		for num := int32(1); num <= mainXref; num++ {
			xs = append(xs, xentry{num: num, off: off[num]})
		}
		for _, u := range second {
			for k, m := range u.members {
				xs = append(xs, xentry{num: l.renum[m], stm: u.num, idx: int32(k)})
			}
		}
		slices.SortFunc(xs, func(a, b xentry) int { return int(a.num - b.num) })
		data, widths, _ := xrefRows(xs)
		dict := pdf.NewDict(
			pdf.Entry{Key: "Type", Val: pdf.Name("XRef").Object()},
			pdf.Entry{Key: "Size", Val: pdf.Integer(int64(firstStart))},
			pdf.Entry{Key: "W", Val: widths.Object()},
			pdf.Entry{Key: "ID", Val: pdf.Array{pdf.String(id1), pdf.String(id2)}.Object()},
		)
		bw := bufio.NewWriter(&main)
		e := &encoder{b: sink{bw}, w: w, pos: func() int64 { return 0 }}
		e.b.printf("%d 0 obj\n", mainXref)
		e.value(Flate(dict, data).Object(), 0)
		e.b.str("\nendobj\n")
		_ = bw.Flush()
	} else {
		fmt.Fprintf(&main, "xref\n0 %d", firstStart)
		beforeZero = mainOff + int64(main.Len())
		main.WriteString("\n0000000000 65535 f\r\n")
		for num := int32(1); num < firstStart; num++ {
			fmt.Fprintf(&main, "%010d 00000 n\r\n", off[num])
		}
		fmt.Fprintf(&main, "trailer\n<</Size %d %s>>\n", firstStart, id)
	}
	fmt.Fprintf(&main, "startxref\n%d\n%%%%EOF\n", firstOff)
	fileLen := mainOff + int64(main.Len())

	lin := fmt.Sprintf("%d 0 obj\n<</Linearized 1 /L %d /H [ %d %d ] /O %d /E %d /N %d /T %d>>\nendobj",
		linDict, fileLen, hintOff, hintLen, l.renum[pages[0]], firstEnd, len(pages), beforeZero)
	lin += strings.Repeat(" ", linRoom-1-len(lin)) + "\n"

	bw := bufio.NewWriterSize(out, 64<<10)
	b := sink{bw}
	b.str(header)
	b.str(lin)
	b.bytes(first(mainOff))
	write := func(units []*linUnit) {
		for _, u := range units {
			b.bytes(u.data)
			u.data = nil
		}
	}
	write(part4)
	b.bytes(hintBuf.Bytes())
	write(part6)
	write(second)
	b.bytes(main.Bytes())
	return bw.Flush()
}

// pages0 returns the number in the file of the first page's object.
func pages0(pageUnits [][]*linUnit) int32 { return pageUnits[0][0].num }
