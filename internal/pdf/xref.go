// Ported from github.com/go-pdfkit/reader v0.6.0 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/reader authors); see LICENSE-go-pdfkit.
// Changed: each section is read on its own and merged, so a hybrid file's
// /XRefStm entries are not hidden by the free entries of its table.

package pdf

import (
	"bytes"
	"fmt"
	"math"

	"github.com/timzifer/cera/internal/pdfsyntax"
)

// MaxXrefSections bounds a /Prev chain, which a file is free to make a
// cycle.
const MaxXrefSections = 1024

// MaxObjects bounds how many entries the cross-reference information may
// define.
const MaxObjects = 8 << 20

// startxrefWindow is how far back from the end startxref is looked for.
const startxrefWindow = 4096

// findStartxref reads the offset the file's last startxref names.
func (d *Document) findStartxref() (int64, error) {
	from := max(d.src.size()-startxrefWindow, 0)
	b, _ := d.src.window(from, startxrefWindow)
	b = b[:min(len(b), startxrefWindow)]
	i := bytes.LastIndex(b, []byte("startxref"))
	if i < 0 {
		return 0, fmt.Errorf("pdf: no startxref in the last %d bytes", startxrefWindow)
	}
	l := &lexer{buf: b, pos: i + len("startxref")}
	t, err := l.next()
	if err != nil {
		return 0, err
	}
	if t.kind != tokInteger || t.num.Int < 0 || t.num.Int >= d.src.size() {
		return 0, &SyntaxError{t.pos, "startxref does not name an offset inside the file"}
	}
	return t.num.Int, nil
}

// xrefReader collects the entries of the sections of a /Prev chain.
type xrefReader struct {
	d   *Document
	acc xrefAcc
	sec int32 // the section being read, counting from 1
	stm bool  // reading the /XRefStm part of a hybrid section
	// last is the offset of the newest section, lastStream whether it
	// is a cross-reference stream.
	last       int64
	lastStream bool
	trailer    []Entry
	seenKey    map[Name]bool
}

// loadXref follows the chain of cross-reference sections from the last one
// back through /Prev, taking the newest definition of every object and of
// every trailer entry. It returns what it read even when it fails: a
// rebuild still looks at the trailer.
func (d *Document) loadXref() (*table, error) {
	x := &xrefReader{d: d, seenKey: map[Name]bool{}}
	err := x.load()
	t := newTable(&x.acc)
	t.startxref, t.xrefStream = x.last, x.lastStream
	t.trailer = NewDict(x.trailer...)
	if len(x.trailer) == 0 {
		t.trailer = Dict{}
	}
	if err == nil && x.acc.count == 0 {
		err = fmt.Errorf("pdf: the cross-reference table is empty")
	}
	return t, err
}

func (x *xrefReader) load() error {
	off, err := x.d.findStartxref()
	if err != nil {
		return err
	}
	seen := map[int64]bool{}
	for n := 0; off > 0 && n < MaxXrefSections; n++ {
		if seen[off] {
			return fmt.Errorf("pdf: cross-reference sections form a cycle at offset %d", off)
		}
		seen[off] = true
		x.sec++
		x.stm = false
		if x.sec == 1 {
			x.last = off
		}
		tr, err := x.readSection(off)
		if err != nil {
			return err
		}
		x.mergeTrailer(tr)
		// A hybrid file keeps a second, stream-shaped section for the
		// objects its table deliberately hides from old readers: those
		// entries win over the table's free ones.
		if h, ok := tr.Get("XRefStm").Int(); ok && h > 0 && !seen[h] {
			seen[h] = true
			x.stm = true
			if htr, err := x.readSection(h); err == nil {
				x.mergeTrailer(htr)
			}
		}
		if x.acc.count > MaxObjects {
			return fmt.Errorf("pdf: more than %d cross-reference entries", MaxObjects)
		}
		p, ok := tr.Get("Prev").Int()
		if !ok || p <= 0 {
			break
		}
		off = p
	}
	return nil
}

// mergeTrailer keeps the first value seen for each key, sections being read
// newest first.
func (x *xrefReader) mergeTrailer(tr Dict) {
	for _, e := range tr.e {
		if !x.seenKey[e.Key] {
			x.seenKey[e.Key] = true
			x.trailer = append(x.trailer, e)
		}
	}
}

// set records an entry unless a newer section, or this one, already
// defined the object. In a hybrid section the /XRefStm part may replace a
// free entry of the table.
func (x *xrefReader) set(num int64, e rawEntry) {
	if num < 0 || num > math.MaxInt32 {
		return
	}
	e.sec, e.table = x.sec, !x.stm
	old := x.acc.get(int32(num))
	hidden := x.stm && old.sec == x.sec && old.table && old.kind == 'f'
	if old.kind != 0 && !hidden {
		return
	}
	x.acc.put(int32(num), e)
}

// readSection reads whichever of the two forms is at off into sec and
// returns its trailer dictionary.
func (x *xrefReader) readSection(off int64) (tr Dict, err error) {
	if off < 0 || off >= x.d.src.size() {
		return Dict{}, fmt.Errorf("pdf: cross-reference offset %d is outside the file", off)
	}
	err = x.d.windowed(off, func(p *parser) error {
		t, err := p.lex.next()
		if err != nil {
			return err
		}
		if p.lex.isKeyword(t, "xref") {
			tr, err = x.readTable(p)
			return err
		}
		p.lex.pos = 0
		_, obj, err := p.indirect(nil)
		if err != nil {
			return err
		}
		s, ok := obj.Stream()
		if !ok {
			return &SyntaxError{int(off), "neither an xref table nor an xref stream"}
		}
		if x.sec == 1 && !x.stm {
			x.lastStream = true
		}
		tr, err = x.readStream(s)
		return err
	})
	return tr, err
}

// readTable reads the classic subsection form, the xref keyword already
// consumed, up to and including its trailer dictionary.
func (x *xrefReader) readTable(p *parser) (Dict, error) {
	for {
		save := p.lex.pos
		t, err := p.lex.next()
		if err != nil {
			return Dict{}, err
		}
		if p.lex.isKeyword(t, "trailer") {
			o, err := p.object()
			if err != nil {
				return Dict{}, err
			}
			tr, ok := o.Dict()
			if !ok || o.kind != KindDict {
				return Dict{}, &SyntaxError{save, "the trailer is not a dictionary"}
			}
			return tr, nil
		}
		if t.kind != tokInteger {
			return Dict{}, &SyntaxError{t.pos, "a cross-reference subsection header was expected"}
		}
		start := t.num.Int
		count, err := p.expectUint("a subsection entry count")
		if err != nil {
			return Dict{}, err
		}
		for i := int64(0); i < count; i++ {
			if x.fastEntry(p, start+i) {
				continue
			}
			if err := x.readTableEntry(p, start+i); err != nil {
				return Dict{}, err
			}
		}
	}
}

// fastEntry reads one entry written the way the specification says, ten
// digits, five digits and n or f, without lexing it, and reports whether it
// could.
func (x *xrefReader) fastEntry(p *parser, num int64) bool {
	b := p.lex.buf
	i := pdfsyntax.SkipSpace(b, p.lex.pos)
	if i+18 > len(b) || b[i+10] != ' ' || b[i+16] != ' ' {
		return false
	}
	var off, gen int64
	for _, c := range b[i : i+10] {
		if c < '0' || c > '9' {
			return false
		}
		off = off*10 + int64(c-'0')
	}
	for _, c := range b[i+11 : i+16] {
		if c < '0' || c > '9' {
			return false
		}
		gen = gen*10 + int64(c-'0')
	}
	kind := b[i+17]
	if (kind != 'n' && kind != 'f') || (i+18 < len(b) && isRegular(b[i+18])) {
		return false
	}
	p.lex.pos = i + 18
	if kind == 'n' {
		x.set(num, rawEntry{kind: 'n', off: off, gen: int32(gen)})
	} else {
		x.set(num, rawEntry{kind: 'f'})
	}
	return true
}

// readTableEntry reads one "offset generation n|f" line.
func (x *xrefReader) readTableEntry(p *parser, num int64) error {
	t1, err := p.lex.next()
	if err != nil {
		return err
	}
	if t1.kind != tokInteger {
		return &SyntaxError{t1.pos, "a cross-reference entry offset was expected"}
	}
	t2, err := p.lex.next()
	if err != nil {
		return err
	}
	if t2.kind != tokInteger {
		return &SyntaxError{t2.pos, "a cross-reference entry generation was expected"}
	}
	t3, err := p.lex.next()
	if err != nil {
		return err
	}
	kw := p.lex.text(t3)
	if t3.kind != tokKeyword || len(kw) != 1 || (kw[0] != 'n' && kw[0] != 'f') {
		return &SyntaxError{t3.pos, "a cross-reference entry must end in n or f"}
	}
	if kw[0] == 'n' {
		x.set(num, rawEntry{kind: 'n', off: t1.num.Int, gen: clamp32(t2.num.Int)})
		return nil
	}
	x.set(num, rawEntry{kind: 'f'})
	return nil
}

// readXrefStream reads the /Type /XRef stream form. Its own data is never
// encrypted, and indirect references in its dictionary are not followed.
func (x *xrefReader) readStream(s *Stream) (Dict, error) {
	r := DecodeRecovering(s.Dict, s.raw)
	if r.Recovered {
		return Dict{}, r.Cause
	}
	if r.Image != "" {
		return Dict{}, fmt.Errorf("pdf: the cross-reference stream is filtered as an image")
	}
	data := r.Data
	widths, err := xrefWidths(s.Dict)
	if err != nil {
		return Dict{}, err
	}
	index, err := xrefIndex(s.Dict)
	if err != nil {
		return Dict{}, err
	}
	row := widths[0] + widths[1] + widths[2]
	if row == 0 {
		return Dict{}, fmt.Errorf("pdf: the cross-reference stream's /W entries are all zero")
	}
	pos := 0
	for i := 0; i+1 < len(index); i += 2 {
		for k := int64(0); k < index[i+1]; k++ {
			if pos+row > len(data) {
				// A section that claims more rows than it carries still
				// contributes the rows it does carry.
				return s.Dict, nil
			}
			f1 := xrefField(data[pos:], widths[0], 1)
			f2 := xrefField(data[pos+widths[0]:], widths[1], 0)
			f3 := xrefField(data[pos+widths[0]+widths[1]:], widths[2], 0)
			pos += row
			num := index[i] + k
			switch f1 {
			case 1:
				x.set(num, rawEntry{kind: 'n', off: f2, gen: int32(f3)})
			case 2:
				x.set(num, rawEntry{kind: 'o', off: f2})
			default:
				x.set(num, rawEntry{kind: 'f'})
			}
		}
	}
	return s.Dict, nil
}

// xrefWidths reads /W.
func xrefWidths(d Dict) ([3]int, error) {
	var w [3]int
	arr, ok := d.Get("W").Array()
	if !ok || len(arr) < 3 {
		return w, fmt.Errorf("pdf: the cross-reference stream has no usable /W")
	}
	for i := range 3 {
		n, ok := arr[i].Int()
		if !ok || n < 0 || n > 8 {
			return w, fmt.Errorf("pdf: /W[%d] is not a byte width", i)
		}
		w[i] = int(n)
	}
	return w, nil
}

// xrefIndex reads /Index, defaulting to the whole range /Size describes.
func xrefIndex(d Dict) ([]int64, error) {
	arr, ok := d.Get("Index").Array()
	if !ok {
		size, ok := d.Get("Size").Int()
		if !ok || size < 0 {
			return nil, fmt.Errorf("pdf: the cross-reference stream has neither /Index nor /Size")
		}
		return []int64{0, size}, nil
	}
	out := make([]int64, 0, len(arr))
	for _, e := range arr {
		n, ok := e.Int()
		if !ok || n < 0 {
			return nil, fmt.Errorf("pdf: /Index holds a value that is not a count")
		}
		out = append(out, n)
	}
	return out, nil
}

// xrefField reads one big-endian field of the given width, or its default
// when the width is zero.
func xrefField(b []byte, width int, def int64) int64 {
	if width == 0 {
		return def
	}
	v := int64(0)
	for i := range width {
		v = v<<8 | int64(b[i])
	}
	return v
}
