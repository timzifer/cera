// Package pdfwrite writes PDF files made of objects built in memory and of
// objects copied from a source document.
//
// Values live in one of two spaces. Objects given to [Writer.Set] and
// [Writer.Add] are in the output space: their references are output
// references ([Ref.Object]) and are written as they are. Values read from
// the source document are in the source space and enter the output only
// through [Writer.Map], which rebuilds them with every source reference
// translated by the writer's mapping. A source object copied as a whole
// ([Writer.Copy]) is translated when it is written, so copying follows the
// references of what is written, each object once.
//
// The file is written with a classic cross-reference table and is not
// encrypted: streams copied from an encrypted source are written decrypted,
// still filtered, with a leading /Crypt filter removed.
package pdfwrite

import (
	"bytes"
	"fmt"
	"math"
	"strconv"

	"github.com/timzifer/cera/internal/pdf"
)

// maxDepth bounds how deeply direct objects nest in what is mapped and
// written; the parser bounds it on reading already, this guards the
// recursion.
const maxDepth = 256

// Ref is the number of an output object. The zero Ref is no object: a
// reference mapped to it is written as null.
type Ref int32

// Object returns the reference to r as an output-space value.
func (r Ref) Object() pdf.Object { return pdf.Ref{Num: int32(r)}.Object() }

// A slot is one output object. A source slot holds a source object, mapped
// when it is written.
type slot struct {
	o      pdf.Object
	source bool
}

// A Writer collects the objects of a new file.
type Writer struct {
	src *pdf.Document

	// MapRef translates a source reference to an output one, or to 0 to
	// write null. When nil, every reference is copied ([Writer.Copy]).
	MapRef func(pdf.Ref) Ref
	// Drop holds keys left out of every source dictionary that is mapped,
	// stream dictionaries included.
	Drop map[pdf.Name]bool

	objs   []slot
	copied map[int32]Ref
	buf    bytes.Buffer
}

// New returns a writer that copies from src, which may be nil when nothing
// is copied.
func New(src *pdf.Document) *Writer {
	return &Writer{src: src, copied: map[int32]Ref{}}
}

// Alloc numbers an output object whose value is set later; it is written
// as null if it never is.
func (w *Writer) Alloc() Ref {
	w.objs = append(w.objs, slot{o: pdf.Null})
	return Ref(len(w.objs))
}

// Set sets the value of output object r to the output-space value o.
func (w *Writer) Set(r Ref, o pdf.Object) { w.objs[r-1] = slot{o: o} }

// Add adds an output object with the output-space value o.
func (w *Writer) Add(o pdf.Object) Ref {
	r := w.Alloc()
	w.Set(r, o)
	return r
}

// Copied returns the output number of source object r if it has been
// copied.
func (w *Writer) Copied(r pdf.Ref) (Ref, bool) {
	n, ok := w.copied[r.Num]
	return n, ok
}

// Copy returns the output number of source object r, numbering it for
// copying when it is first met, or 0 when r is missing or null.
func (w *Writer) Copy(r pdf.Ref) Ref {
	if n, ok := w.copied[r.Num]; ok {
		return n
	}
	if r.Num <= 0 {
		return 0
	}
	o, err := w.src.Get(r)
	if err != nil || o.IsNull() {
		return 0
	}
	w.objs = append(w.objs, slot{o: o, source: true})
	n := Ref(len(w.objs))
	w.copied[r.Num] = n
	return n
}

// mapRef translates one source reference.
func (w *Writer) mapRef(r pdf.Ref) Ref {
	if w.MapRef != nil {
		return w.MapRef(r)
	}
	return w.Copy(r)
}

// Map returns the source-space value o rebuilt in the output space: source
// references translated, the keys of Drop left out of dictionaries, and a
// stream given its bytes as stored, decrypted but still filtered.
func (w *Writer) Map(o pdf.Object) pdf.Object { return w.mapValue(o, 0) }

func (w *Writer) mapValue(o pdf.Object, depth int) pdf.Object {
	if depth > maxDepth {
		return pdf.Null
	}
	switch o.Kind() {
	case pdf.KindRef:
		r, _ := o.Ref()
		if n := w.mapRef(r); n > 0 {
			return n.Object()
		}
		return pdf.Null
	case pdf.KindArray:
		a, _ := o.Array()
		out := make(pdf.Array, len(a))
		for i, e := range a {
			out[i] = w.mapValue(e, depth+1)
		}
		return out.Object()
	case pdf.KindDict:
		d, _ := o.Dict()
		return w.mapDict(d, depth, nil).Object()
	case pdf.KindStream:
		s, _ := o.Stream()
		return w.mapStream(s, depth)
	}
	return o
}

// mapDict maps the entries of d, leaving out those of Drop and of skip.
func (w *Writer) mapDict(d pdf.Dict, depth int, skip map[pdf.Name]bool) pdf.Dict {
	entries := make([]pdf.Entry, 0, d.Len())
	for k, v := range d.All() {
		if w.Drop[k] || skip[k] {
			continue
		}
		entries = append(entries, pdf.Entry{Key: k, Val: w.mapValue(v, depth+1)})
	}
	return pdf.NewDict(entries...)
}

// streamKeys are the stream dictionary entries mapStream writes itself.
var streamKeys = map[pdf.Name]bool{"Length": true, "Filter": true, "DecodeParms": true}

// mapStream maps a source stream: its bytes as stored, decrypted but still
// filtered, under its mapped dictionary. A leading /Crypt filter goes: the
// output is not encrypted.
func (w *Writer) mapStream(s *pdf.Stream, depth int) pdf.Object {
	data := w.src.Raw(s)
	filter, parms := s.Dict.Get("Filter"), s.Dict.Get("DecodeParms")
	if f, p, ok := dropCrypt(w.src.Resolve(filter), w.src.Resolve(parms)); ok {
		filter, parms = f, p
	}
	dict := w.mapDict(s.Dict, depth, streamKeys)
	if !filter.IsNull() {
		dict = dict.With("Filter", w.mapValue(filter, depth+1))
	}
	if !parms.IsNull() {
		dict = dict.With("DecodeParms", w.mapValue(parms, depth+1))
	}
	return pdf.NewStream(dict, data).Object()
}

// dropCrypt removes a leading /Crypt filter and its parameters.
func dropCrypt(filter, parms pdf.Object) (pdf.Object, pdf.Object, bool) {
	if n, ok := filter.Name(); ok {
		if n != "Crypt" {
			return filter, parms, false
		}
		return pdf.Null, pdf.Null, true
	}
	a, ok := filter.Array()
	if !ok || len(a) == 0 {
		return filter, parms, false
	}
	if n, _ := a[0].Name(); n != "Crypt" {
		return filter, parms, false
	}
	filter = a[1:].Object()
	if len(a) == 1 {
		filter = pdf.Null
	}
	if pa, ok := parms.Array(); ok && len(pa) > 0 {
		parms = pa[1:].Object()
		if len(pa) == 1 {
			parms = pdf.Null
		}
	} else {
		parms = pdf.Null
	}
	return filter, parms, true
}

// Trailer holds what the trailer names.
type Trailer struct {
	Root Ref
	Info Ref // 0 for none
}

// Bytes serialises the output objects, mapping source objects as it goes
// (which may copy more), and the cross-reference table and trailer. The
// writer must not be used after it.
func (w *Writer) Bytes(t Trailer) []byte {
	b := &w.buf
	b.Reset()
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	var offs []int
	for i := 0; i < len(w.objs); i++ {
		o := w.objs[i]
		if o.source {
			o.o = w.Map(o.o)
		}
		offs = append(offs, b.Len())
		fmt.Fprintf(b, "%d 0 obj\n", i+1)
		w.value(o.o, 0)
		b.WriteString("\nendobj\n")
	}
	xref := b.Len()
	fmt.Fprintf(b, "xref\n0 %d\n0000000000 65535 f\r\n", len(offs)+1)
	for _, off := range offs {
		fmt.Fprintf(b, "%010d 00000 n\r\n", off)
	}
	fmt.Fprintf(b, "trailer\n<</Size %d /Root %d 0 R", len(offs)+1, t.Root)
	if t.Info != 0 {
		fmt.Fprintf(b, " /Info %d 0 R", t.Info)
	}
	fmt.Fprintf(b, ">>\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}

// value writes an output-space object. A stream is written in full only at
// the top of an object; nested, as anything else not a direct object, it
// is written as null.
func (w *Writer) value(o pdf.Object, depth int) {
	b := &w.buf
	if depth > maxDepth {
		b.WriteString("null")
		return
	}
	switch o.Kind() {
	case pdf.KindBool:
		v, _ := o.Bool()
		b.WriteString(strconv.FormatBool(v))
	case pdf.KindInteger:
		v, _ := o.Int()
		b.WriteString(strconv.FormatInt(v, 10))
	case pdf.KindReal:
		v, _ := o.Float()
		if math.IsNaN(v) || math.IsInf(v, 0) {
			v = 0
		}
		b.WriteString(strconv.FormatFloat(v, 'f', -1, 64))
	case pdf.KindString:
		s, _ := o.Str()
		writeString(b, s)
	case pdf.KindName:
		n, _ := o.Name()
		writeName(b, n)
	case pdf.KindArray:
		a, _ := o.Array()
		b.WriteByte('[')
		for i, e := range a {
			if i > 0 {
				b.WriteByte(' ')
			}
			w.value(e, depth+1)
		}
		b.WriteByte(']')
	case pdf.KindDict:
		d, _ := o.Dict()
		w.dict(d, depth, nil)
	case pdf.KindStream:
		s, _ := o.Stream()
		data, ok := s.Unencrypted()
		if depth > 0 || !ok {
			b.WriteString("null")
			return
		}
		w.dict(s.Dict, depth, streamLength(len(data)))
		b.WriteString("\nstream\n")
		b.Write(data)
		b.WriteString("\nendstream")
	case pdf.KindRef:
		r, _ := o.Ref()
		if r.Num <= 0 || int(r.Num) > len(w.objs) {
			b.WriteString("null")
			return
		}
		fmt.Fprintf(b, "%d 0 R", r.Num)
	default:
		b.WriteString("null")
	}
}

// streamLength is the /Length entry of a stream of n bytes.
func streamLength(n int) *pdf.Entry {
	return &pdf.Entry{Key: "Length", Val: pdf.Integer(int64(n))}
}

// dict writes a dictionary; with length, its /Length is replaced by it.
func (w *Writer) dict(d pdf.Dict, depth int, length *pdf.Entry) {
	b := &w.buf
	b.WriteString("<<")
	for k, v := range d.All() {
		if length != nil && k == "Length" {
			continue
		}
		writeName(b, k)
		b.WriteByte(' ')
		w.value(v, depth+1)
	}
	if length != nil {
		writeName(b, length.Key)
		b.WriteByte(' ')
		w.value(length.Val, depth+1)
	}
	b.WriteString(">>")
}

// writeName writes a name, escaping what may not appear in it literally.
func writeName(b *bytes.Buffer, n pdf.Name) {
	b.WriteByte('/')
	for i := 0; i < len(n); i++ {
		c := n[i]
		if c < 0x21 || c > 0x7e || c == '#' || isDelim(c) {
			fmt.Fprintf(b, "#%02X", c)
			continue
		}
		b.WriteByte(c)
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
func writeString(b *bytes.Buffer, s []byte) {
	b.WriteByte('(')
	for _, c := range s {
		switch c {
		case '(', ')', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\r':
			b.WriteString(`\r`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte(')')
}
