package pdfwrite

import (
	"bufio"
	"crypto/md5"
	"fmt"
	"hash"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"

	"github.com/timzifer/cera/internal/pdf"
)

// Trailer holds what the trailer names.
type Trailer struct {
	Root Ref
	Info Ref // 0 for none
	// ID is the first element of /ID, the file's permanent identifier,
	// kept when the output stands for a file that had one. When nil it is
	// made from the content, as the second element always is.
	ID []byte
}

// counter counts and hashes what passes to w and keeps its first error.
type counter struct {
	w   io.Writer
	n   int64
	h   hash.Hash
	err error
}

func (c *counter) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	c.h.Write(p)
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.err = err
	return n, err
}

// sink writes through a buffer. The buffer keeps its first error, which
// Write reports when it flushes, so the calls here do not return it.
type sink struct{ b *bufio.Writer }

func (o sink) str(s string)              { _, _ = o.b.WriteString(s) }
func (o sink) byte(c byte)               { _ = o.b.WriteByte(c) }
func (o sink) bytes(p []byte)            { _, _ = o.b.Write(p) }
func (o sink) printf(f string, a ...any) { _, _ = fmt.Fprintf(o.b, f, a...) }

// Write writes the file to out: the header, the output objects in order,
// mapping source objects as it goes (which may copy more), and the
// cross-reference table and trailer. An object is dropped from memory once
// it is written. The writer must not be used after it.
func (w *Writer) Write(out io.Writer, t Trailer) error {
	if w.base != nil {
		return w.writeUpdate(out, t)
	}
	c := &counter{w: out, h: md5.New()}
	bw := bufio.NewWriterSize(c, 64<<10)
	b := sink{bw}
	e := &encoder{b: b, w: w}
	version := w.Version
	if version == "" {
		version = "1.7"
	}
	b.printf("%%PDF-%s\n%%\xe2\xe3\xcf\xd3\n", version)
	var offs []int64
	for i := 0; i < len(w.objs) && c.err == nil; i++ {
		s := w.objs[i]
		w.objs[i] = slot{}
		if s.imp != nil {
			s.o = s.imp.Map(s.o)
		}
		offs = append(offs, c.n+int64(bw.Buffered()))
		b.printf("%d 0 obj\n", i+1)
		e.value(s.o, 0)
		b.str("\nendobj\n")
	}
	xref := c.n + int64(bw.Buffered())
	b.printf("xref\n0 %d\n0000000000 65535 f\r\n", len(offs)+1)
	for _, off := range offs {
		b.printf("%010d 00000 n\r\n", off)
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	sum := c.h.Sum(nil)
	first := t.ID
	if first == nil {
		first = sum
	}
	b.printf("trailer\n<</Size %d /Root %d 0 R", len(offs)+1, t.Root)
	if t.Info != 0 {
		b.printf(" /Info %d 0 R", t.Info)
	}
	b.printf(" /ID [<%x> <%x>]>>\nstartxref\n%d\n%%%%EOF\n", first, sum, xref)
	return bw.Flush()
}

// An encoder writes output-space objects.
type encoder struct {
	b sink
	w *Writer
	// crypt, when set, encrypts the strings and stream of object num,
	// generation gen, the one being written.
	crypt    *pdf.Encryptor
	num, gen int32
}

// object sets the object written next, whose strings and stream crypt
// encrypts, if set.
func (e *encoder) object(num, gen int32, crypt *pdf.Encryptor) {
	e.num, e.gen, e.crypt = num, gen, crypt
}

// value writes an output-space object. A stream is written in full only at
// the top of an object; nested, as anything else not a direct object, it
// is written as null.
func (e *encoder) value(o pdf.Object, depth int) {
	b := e.b
	if depth > maxDepth {
		b.str("null")
		return
	}
	switch o.Kind() {
	case pdf.KindBool:
		v, _ := o.Bool()
		b.str(strconv.FormatBool(v))
	case pdf.KindInteger:
		v, _ := o.Int()
		b.str(strconv.FormatInt(v, 10))
	case pdf.KindReal:
		v, _ := o.Float()
		if math.IsNaN(v) || math.IsInf(v, 0) {
			v = 0
		}
		b.str(strconv.FormatFloat(v, 'f', -1, 64))
	case pdf.KindString:
		s, _ := o.Str()
		if e.crypt != nil {
			s = e.crypt.String(e.num, e.gen, s)
		}
		writeString(b, s)
	case pdf.KindName:
		n, _ := o.Name()
		writeName(b, n)
	case pdf.KindArray:
		a, _ := o.Array()
		b.byte('[')
		for i, x := range a {
			if i > 0 {
				b.byte(' ')
			}
			e.value(x, depth+1)
		}
		b.byte(']')
	case pdf.KindDict:
		d, _ := o.Dict()
		e.dict(d, depth, -1)
	case pdf.KindStream:
		s, _ := o.Stream()
		data, ok := s.Unencrypted()
		if depth > 0 || !ok {
			b.str("null")
			return
		}
		if e.crypt != nil {
			data = e.crypt.Stream(e.num, e.gen, s.Dict, data)
		}
		e.dict(s.Dict, depth, len(data))
		b.str("\nstream\n")
		b.bytes(data)
		b.str("\nendstream")
	case pdf.KindRef:
		r, _ := o.Ref()
		g, ok := e.w.gen(r.Num)
		if !ok {
			b.str("null")
			return
		}
		b.printf("%d %d R", r.Num, g)
	default:
		b.str("null")
	}
}

// dict writes a dictionary; with a length of 0 or more, its /Length is
// replaced by it.
func (e *encoder) dict(d pdf.Dict, depth, length int) {
	b := e.b
	b.str("<<")
	for k, v := range d.All() {
		if length >= 0 && k == "Length" {
			continue
		}
		writeName(b, k)
		b.byte(' ')
		e.value(v, depth+1)
	}
	if length >= 0 {
		b.printf("/Length %d", length)
	}
	b.str(">>")
}

// writeName writes a name, escaping what may not appear in it literally.
func writeName(b sink, n pdf.Name) {
	b.byte('/')
	for i := 0; i < len(n); i++ {
		c := n[i]
		if c < 0x21 || c > 0x7e || c == '#' || isDelim(c) {
			b.printf("#%02X", c)
			continue
		}
		b.byte(c)
	}
}

func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// writeString writes a literal string. Parentheses and backslashes are
// escaped, and line ends, which a reader would normalise.
func writeString(b sink, s []byte) {
	b.byte('(')
	for _, c := range s {
		switch c {
		case '(', ')', '\\':
			b.byte('\\')
			b.byte(c)
		case '\r':
			b.str(`\r`)
		case '\n':
			b.str(`\n`)
		default:
			b.byte(c)
		}
	}
	b.byte(')')
}

// gen returns the generation a reference to object num is written with,
// and false for a number that names no object.
func (w *Writer) gen(num int32) (int32, bool) {
	switch {
	case num <= 0:
		return 0, false
	case num >= w.first:
		return 0, num <= int32(w.last())
	case w.base == nil:
		return 0, false
	}
	g, ok := w.base.Gen(num)
	if _, replaced := w.replaced[num]; replaced {
		return g, true
	}
	return g, ok
}

// An xentry is an entry of the cross-reference section of an update.
type xentry struct {
	num  int32
	off  int64
	gen  int32
	free bool
}

// writeUpdate writes an incremental update to out, which already holds the
// file it extends: the objects replaced, in ascending order, the new ones,
// and a cross-reference section of them all with the trailer, chained to
// the file's newest section by /Prev.
func (w *Writer) writeUpdate(out io.Writer, t Trailer) error {
	base := w.base
	c := &counter{w: out, h: md5.New(), n: base.Size}
	bw := bufio.NewWriterSize(c, 64<<10)
	b := sink{bw}
	e := &encoder{b: b, w: w}
	// The file need not end with a line break.
	b.str("\n")
	var xs []xentry
	for _, num := range slices.Sorted(maps.Keys(w.replaced)) {
		g, _ := base.Gen(num)
		xs = append(xs, xentry{num: num, off: c.n + int64(bw.Buffered()), gen: g})
		b.printf("%d %d obj\n", num, g)
		e.object(num, g, base.Crypt)
		e.value(w.replaced[num], 0)
		b.str("\nendobj\n")
	}
	for i := 0; i < len(w.objs) && c.err == nil; i++ {
		s := w.objs[i]
		w.objs[i] = slot{}
		if s.imp != nil {
			s.o = s.imp.Map(s.o)
		}
		num := w.first + int32(i)
		xs = append(xs, xentry{num: num, off: c.n + int64(bw.Buffered())})
		b.printf("%d 0 obj\n", num)
		e.object(num, 0, base.Crypt)
		e.value(s.o, 0)
		b.str("\nendobj\n")
	}
	for num := range w.freed {
		g, _ := base.Gen(num)
		xs = append(xs, xentry{num: num, gen: min(g+1, 65535), free: true})
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	sum := c.h.Sum(nil)
	first := t.ID
	if first == nil {
		first = sum
	}
	trailer := []pdf.Entry{
		{Key: "Prev", Val: pdf.Integer(base.Prev)},
		{Key: "Root", Val: t.Root.Object()},
	}
	if t.Info != 0 {
		trailer = append(trailer, pdf.Entry{Key: "Info", Val: t.Info.Object()})
	}
	if !base.Encrypt.IsNull() {
		trailer = append(trailer, pdf.Entry{Key: "Encrypt", Val: base.Encrypt})
	}
	trailer = append(trailer, pdf.Entry{Key: "ID", Val: pdf.Array{pdf.String(first), pdf.String(sum)}.Object()})
	// The cross-reference stream and the trailer are never encrypted.
	e.object(0, 0, nil)

	xref := c.n + int64(bw.Buffered())
	byNum := func(a, b xentry) int { return int(a.num - b.num) }
	if base.Stream {
		// The stream is an object of the update itself, numbered last.
		num := int32(w.last()) + 1
		xs = append(xs, xentry{num: num, off: xref})
		slices.SortFunc(xs, byNum)
		data, widths, index := xrefRows(xs)
		dict := pdf.NewDict(append(trailer,
			pdf.Entry{Key: "Type", Val: pdf.Name("XRef").Object()},
			pdf.Entry{Key: "Size", Val: pdf.Integer(int64(num) + 1)},
			pdf.Entry{Key: "W", Val: widths.Object()},
			pdf.Entry{Key: "Index", Val: index.Object()},
		)...)
		b.printf("%d 0 obj\n", num)
		e.value(Flate(dict, data).Object(), 0)
		b.str("\nendobj\n")
	} else {
		slices.SortFunc(xs, byNum)
		b.str("xref\n")
		for i := 0; i < len(xs); {
			j := i + 1
			for j < len(xs) && xs[j].num == xs[j-1].num+1 {
				j++
			}
			b.printf("%d %d\n", xs[i].num, j-i)
			for _, x := range xs[i:j] {
				if x.free {
					b.printf("0000000000 %05d f\r\n", x.gen)
				} else {
					b.printf("%010d %05d n\r\n", x.off, x.gen)
				}
			}
			i = j
		}
		b.str("trailer\n")
		e.value(pdf.NewDict(append(trailer,
			pdf.Entry{Key: "Size", Val: pdf.Integer(int64(w.last()) + 1)})...).Object(), 0)
		b.str("\n")
	}
	b.printf("startxref\n%d\n%%%%EOF\n", xref)
	return bw.Flush()
}

// xrefRows lays out the entries of a cross-reference stream: the rows,
// the field widths and the subsections.
func xrefRows(xs []xentry) ([]byte, pdf.Array, pdf.Array) {
	maxOff := int64(0)
	for _, x := range xs {
		maxOff = max(maxOff, x.off)
	}
	w2 := 1
	for w2 < 8 && maxOff>>(8*w2) > 0 {
		w2++
	}
	var rows []byte
	var index pdf.Array
	for i, x := range xs {
		if i == 0 || x.num != xs[i-1].num+1 {
			index = append(index, pdf.Integer(int64(x.num)), pdf.Integer(0))
		}
		n, _ := index[len(index)-1].Int()
		index[len(index)-1] = pdf.Integer(n + 1)
		typ, off := byte(1), x.off
		if x.free {
			typ, off = 0, 0
		}
		rows = append(rows, typ)
		for k := w2 - 1; k >= 0; k-- {
			rows = append(rows, byte(off>>(8*k)))
		}
		rows = append(rows, byte(x.gen>>8), byte(x.gen))
	}
	widths := pdf.Array{pdf.Integer(1), pdf.Integer(int64(w2)), pdf.Integer(2)}
	return rows, widths, index
}
