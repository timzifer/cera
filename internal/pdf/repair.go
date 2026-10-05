// Ported from github.com/go-pdfkit/reader v0.6.0 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/reader authors); see LICENSE-go-pdfkit.
// Changed: the rebuild produces a new table instead of resetting the
// document's, so readers of the old one are not disturbed.

package pdf

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"
)

// repair rebuilds the cross-reference information by reading the file
// itself rather than what it says about itself: every "N G obj" header in
// order, the later definition of an object number winning, plus whatever
// trailer or catalogue can be found. old supplies the trailer and the file
// key read so far.
func (d *Document) repair(ctx *loadCtx, old *table) (*table, error) {
	var acc xrefAcc
	for _, h := range d.objectHeaders() {
		acc.put(h.num, rawEntry{kind: 'n', off: h.offset, gen: h.gen})
	}
	t := newTable(&acc)
	t.repaired = true
	if old != nil {
		t.trailer, t.dec = old.trailer, old.dec
	}
	if acc.count == 0 {
		return nil, fmt.Errorf("pdf: the file holds no indirect objects")
	}
	// A rebuild reads object streams, and in an encrypted file it cannot
	// read one until the file key is known: establish it first.
	if err := d.establishDecryption(ctx, t); err != nil {
		return nil, err
	}
	// Objects held in object streams are invisible to a header scan; take
	// them from every object stream the scan did find.
	d.indexObjectStreams(ctx, t)
	d.loadRepairedTrailer(ctx, t)
	if t.trailer.IsZero() {
		return nil, fmt.Errorf("pdf: no document catalogue found")
	}
	return t, nil
}

// setUpDecryption reads /Encrypt of a trailer and derives the file key; nil
// when the trailer names none.
func (d *Document) setUpDecryption(ctx *loadCtx, t *table, trailer Dict) (*decryptor, error) {
	eo := trailer.Get("Encrypt")
	if eo.IsNull() {
		return nil, nil
	}
	r, isRef := eo.Ref()
	if isRef {
		v, err := d.get(ctx, t, r.Num)
		if err != nil {
			return nil, err
		}
		eo = d.resolve(ctx, t, v)
	}
	enc, ok := eo.Dict()
	if !ok {
		return nil, fmt.Errorf("pdf: /Encrypt is a %s, not a dictionary", eo.kind)
	}
	var id []byte
	if arr, ok := trailer.Get("ID").Array(); ok && len(arr) > 0 {
		id, _ = arr[0].Str()
	}
	dec, err := newDecryptor(enc, id, d.password, func(o Object) Object { return d.resolve(ctx, t, o) })
	if err != nil {
		return nil, err
	}
	dec.skipObj, dec.skipKnown = r.Num, isRef
	return dec, nil
}

// establishDecryption derives the file key from a trailer the scan can see,
// for a rebuild that runs before the tables could name /Encrypt.
func (d *Document) establishDecryption(ctx *loadCtx, t *table) error {
	if t.dec != nil || !containsAnywhere(d.src, []byte("/Encrypt")) {
		// /Encrypt can only be named by a trailer, and no trailer is
		// compressed: a file without those bytes is not encrypted.
		return nil
	}
	for _, tr := range d.trailerCandidates(ctx, t) {
		if tr.IsZero() || tr.Get("Encrypt").IsNull() {
			continue
		}
		dec, err := d.setUpDecryption(ctx, t, tr)
		if err != nil {
			return err
		}
		t.dec = dec
		// Whatever was read while the key was unknown was read wrong.
		t.resetCache()
		return nil
	}
	return nil
}

// trailerCandidates lists every dictionary that could carry trailer
// entries: what the tables managed to read, the file's trailer keywords,
// then the dictionary of each cross-reference stream, highest object number
// first.
func (d *Document) trailerCandidates(ctx *loadCtx, t *table) []Dict {
	out := append([]Dict{t.trailer}, d.trailers()...)
	var streams []Dict
	for _, num := range t.nums {
		o, _ := d.get(ctx, t, num)
		s, ok := o.Stream()
		if !ok {
			continue
		}
		if n, ok := s.Dict.Get("Type").Name(); ok && n == "XRef" {
			streams = append(streams, s.Dict)
		}
	}
	slices.Reverse(streams)
	return append(out, streams...)
}

// loadRepairedTrailer finds a trailer that leads to a catalogue: the file's
// own trailer dictionaries first, newest first; then any object that calls
// itself a catalogue; and failing both, a catalogue built over whatever
// pages survive.
func (d *Document) loadRepairedTrailer(ctx *loadCtx, t *table) {
	previous := t.trailer
	for _, tr := range d.trailers() {
		if _, err := d.catalog(ctx, t, tr); err == nil {
			t.trailer = tr
			return
		}
	}
	if !previous.IsZero() {
		if _, err := d.catalog(ctx, t, previous); err == nil {
			t.trailer = previous
			return
		}
	}
	t.trailer = Dict{}
	for _, num := range d.objectsOfType(ctx, t, "Catalog") {
		tr := NewDict(Entry{"Root", Ref{Num: num}.Object()})
		if _, err := d.catalog(ctx, t, tr); err == nil {
			t.trailer = tr
			return
		}
	}
	d.synthesiseCatalogue(ctx, t)
}

// synthesiseCatalogue builds a catalogue over whatever objects still call
// themselves pages.
func (d *Document) synthesiseCatalogue(ctx *loadCtx, t *table) {
	var kids Array
	for _, num := range d.objectsOfType(ctx, t, "Page") {
		kids = append(kids, Ref{Num: num}.Object())
	}
	if len(kids) == 0 {
		return
	}
	pages := NewDict(
		Entry{"Type", Name("Pages").Object()},
		Entry{"Kids", kids.Object()},
		Entry{"Count", Integer(int64(len(kids)))},
	)
	root := NewDict(Entry{"Type", Name("Catalog").Object()}, Entry{"Pages", pages.Object()})
	t.trailer = NewDict(Entry{"Root", root.Object()})
}

// indexObjectStreams adds the objects held inside every object stream the
// scan found, without overwriting an object written directly in the file.
// Streams are walked latest in the file first: a later definition wins.
func (d *Document) indexObjectStreams(ctx *loadCtx, t *table) {
	type located struct {
		offset int64
		num    int32
	}
	var found []located
	for _, num := range t.nums {
		o, err := d.get(ctx, t, num)
		if err != nil {
			continue
		}
		s, ok := o.Stream()
		if !ok {
			continue
		}
		if n, ok := s.Dict.Get("Type").Name(); ok && n == "ObjStm" {
			found = append(found, located{t.entry(num).off, num})
		}
	}
	slices.SortFunc(found, func(a, b located) int { return cmp.Compare(b.offset, a.offset) })
	for _, f := range found {
		objs, _ := d.objectStream(ctx, t, f.num)
		nums := make([]int32, 0, len(objs))
		for n := range objs {
			nums = append(nums, n)
		}
		slices.Sort(nums)
		for _, n := range nums {
			if t.entry(n) != nil {
				continue
			}
			t.add(n, f.num, objs[n])
		}
	}
}

// An objectHeader is one "N G obj" found by scanning.
type objectHeader struct {
	num, gen int32
	offset   int64
}

// scanMargin is half the overlap of the chunks a file read on demand is
// scanned in: a chunk answers for the keywords more than this far from
// its edges, so it sees the bytes around each of them.
const scanMargin = 128

// objectHeaders finds every indirect object header in the file, in the
// order they appear.
func (d *Document) objectHeaders() []objectHeader {
	var out []objectHeader
	d.scanKeyword([]byte("obj"), func(b []byte, at int, base int64) {
		if at+3 < len(b) && isRegular(b[at+3]) {
			return
		}
		if h, ok := headerBefore(b, at); ok {
			h.offset += base
			out = append(out, h)
		}
	})
	return out
}

// scanKeyword calls f for every occurrence of kw in the file, in order,
// with the chunk it is in, its position there and the chunk's offset.
func (d *Document) scanKeyword(kw []byte, f func(b []byte, at int, base int64)) {
	size := d.src.size()
	scanChunks(d.src, 2*scanMargin, func(b []byte, base int64) {
		lo, hi := 0, len(b)
		if base > 0 {
			lo = scanMargin
		}
		if base+int64(len(b)) < size {
			hi = len(b) - scanMargin
		}
		for i := lo; i < hi; {
			j := bytes.Index(b[i:], kw)
			if j < 0 || i+j >= hi {
				return
			}
			f(b, i+j, base)
			i += j + len(kw)
		}
	})
}

// headerBefore reads the "N G" that must precede an obj keyword at at.
func headerBefore(b []byte, at int) (objectHeader, bool) {
	p := skipSpaceBack(b, at)
	if p == at {
		return objectHeader{}, false
	}
	gen, p, ok := digitsBack(b, p)
	if !ok {
		return objectHeader{}, false
	}
	q := skipSpaceBack(b, p)
	if q == p {
		return objectHeader{}, false
	}
	num, p, ok := digitsBack(b, q)
	if !ok {
		return objectHeader{}, false
	}
	return objectHeader{num: num, gen: gen, offset: int64(p)}, true
}

func skipSpaceBack(b []byte, p int) int {
	for p > 0 && isSpace(b[p-1]) {
		p--
	}
	return p
}

// digitsBack reads a decimal number that ends at p, going backwards.
func digitsBack(b []byte, p int) (int32, int, bool) {
	end := p
	for p > 0 && b[p-1] >= '0' && b[p-1] <= '9' {
		p--
	}
	if p == end || end-p > 10 {
		return 0, p, false
	}
	v := int64(0)
	for _, c := range b[p:end] {
		v = v*10 + int64(c-'0')
	}
	return clamp32(v), p, true
}

// trailers finds the file's trailer dictionaries, newest first.
func (d *Document) trailers() []Dict {
	var at []int64
	d.scanKeyword([]byte("trailer"), func(_ []byte, i int, base int64) {
		at = append(at, base+int64(i))
	})
	var out []Dict
	for k := len(at) - 1; k >= 0; k-- {
		var o Object
		err := d.windowed(at[k]+int64(len("trailer")), func(p *parser) error {
			var err error
			o, err = p.objectChecked()
			return err
		})
		if err != nil {
			continue
		}
		if tr, ok := o.Dict(); ok {
			out = append(out, tr)
		}
	}
	return out
}
