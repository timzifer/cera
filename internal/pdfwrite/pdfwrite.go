// Package pdfwrite writes PDF files made of objects built in memory and of
// objects copied from source documents.
//
// Values live in one of two spaces. Objects given to [Writer.Set] and
// [Writer.Add] are in the output space: their references are output
// references ([Ref.Object]) and are written as they are. Values read from a
// source document are in that source's space and enter the output only
// through its [Importer]: [Importer.Map] rebuilds them with every source
// reference translated by the importer's mapping. A source object copied as
// a whole ([Importer.Copy]) is translated when it is written, so copying
// follows the references of what is written, each object once per source.
//
// The file is written with a classic cross-reference table and is not
// encrypted: streams copied from an encrypted source are written decrypted,
// still filtered, with a leading /Crypt filter removed.
package pdfwrite

import (
	"bytes"
	"compress/zlib"
	"slices"

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

// A slot is one output object. A slot with an importer holds an object of
// that importer's source, mapped when it is written.
type slot struct {
	o   pdf.Object
	imp *Importer
}

// A Writer collects the objects of a new file, or of an incremental update
// of an existing one.
type Writer struct {
	// Version is the version the header declares; "1.7" when empty. An
	// update writes no header.
	Version string

	objs  []slot // new objects, numbered from first
	first int32

	// For an update: the file it extends, the existing objects it
	// replaces and those it frees.
	base     *Base
	replaced map[int32]pdf.Object
	freed    map[int32]bool
}

// Base is the file an incremental update extends (PDF 2.0, 7.5.6).
type Base struct {
	Size   int64 // the file's length in bytes
	Prev   int64 // the offset of its newest cross-reference section
	Next   int32 // the first number free for new objects
	Stream bool  // write a cross-reference stream rather than a table
	// Gen returns the generation of an existing object, and false for
	// one the file does not define.
	Gen func(num int32) (int32, bool)
	// Crypt encrypts what the update writes for an encrypted file, and
	// Encrypt is the trailer's /Encrypt for it: a reference to the file's
	// encryption dictionary, or the dictionary itself, in the output
	// space. Crypt is nil for a file that is not encrypted.
	Crypt   *pdf.Encryptor
	Encrypt pdf.Object
}

// New returns an empty writer of a new file.
func New() *Writer { return &Writer{first: 1} }

// NewUpdate returns a writer of an update of the file b describes. The
// file's objects keep their numbers: an output reference below b.Next is
// a reference to one of them, and [Writer.Set] on it replaces it. New
// objects are numbered from b.Next.
func NewUpdate(b Base) *Writer {
	return &Writer{first: max(b.Next, 1), base: &b, replaced: map[int32]pdf.Object{}, freed: map[int32]bool{}}
}

// last returns the number of the newest object.
func (w *Writer) last() Ref { return Ref(w.first + int32(len(w.objs)) - 1) }

// Alloc numbers an output object whose value is set later; it is written
// as null if it never is.
func (w *Writer) Alloc() Ref {
	w.objs = append(w.objs, slot{o: pdf.Null})
	return w.last()
}

// Set sets the value of output object r to the output-space value o. In an
// update, setting an existing object replaces it.
func (w *Writer) Set(r Ref, o pdf.Object) {
	if int32(r) < w.first {
		if w.base != nil && r > 0 {
			w.replaced[int32(r)] = o
			delete(w.freed, int32(r))
		}
		return
	}
	w.objs[int32(r)-w.first] = slot{o: o}
}

// Free removes an existing object in an update: references to it read as
// null.
func (w *Writer) Free(num int32) {
	if w.base != nil && num > 0 && num < w.first {
		w.freed[num] = true
		delete(w.replaced, num)
	}
}

// Add adds an output object with the output-space value o.
func (w *Writer) Add(o pdf.Object) Ref {
	r := w.Alloc()
	w.Set(r, o)
	return r
}

// Flate returns a stream of data compressed with the Flate filter, under
// dict with /Filter set.
func Flate(dict pdf.Dict, data []byte) *pdf.Stream {
	var b bytes.Buffer
	z := zlib.NewWriter(&b)
	z.Write(data)
	z.Close()
	return pdf.NewStream(dict.With("Filter", pdf.Name("FlateDecode").Object()), b.Bytes())
}

// An Importer brings the objects of one source document into a writer.
type Importer struct {
	w   *Writer
	src *pdf.Document

	// MapRef translates a source reference to an output one, or to 0 to
	// write null. When nil, every reference is copied ([Importer.Copy]).
	MapRef func(pdf.Ref) Ref
	// Drop holds keys left out of every source dictionary that is mapped,
	// stream dictionaries included.
	Drop map[pdf.Name]bool
	// Compact holds keys whose arrays lose the entries mapped to null, in
	// every source dictionary that is mapped: lists that may not have
	// holes, such as the kids of a form field.
	Compact map[pdf.Name]bool
	// Patch holds source objects to copy in place of what the source
	// has, by object number: objects the caller changed.
	Patch map[int32]pdf.Object

	copied map[int32]Ref
}

// Import returns an importer of objects of src. Each importer copies an
// object once; two importers of the same source copy it twice.
func (w *Writer) Import(src *pdf.Document) *Importer {
	return &Importer{w: w, src: src, copied: map[int32]Ref{}}
}

// Copied returns the output number of source object r if it has been
// copied.
func (im *Importer) Copied(r pdf.Ref) (Ref, bool) {
	n, ok := im.copied[r.Num]
	return n, ok
}

// Copy returns the output number of source object r, numbering it for
// copying when it is first met, or 0 when r is missing or null.
func (im *Importer) Copy(r pdf.Ref) Ref {
	if n, ok := im.copied[r.Num]; ok {
		return n
	}
	if r.Num <= 0 {
		return 0
	}
	o, err := im.src.Get(r)
	if p, ok := im.Patch[r.Num]; ok {
		o, err = p, nil
	}
	if err != nil || o.IsNull() {
		return 0
	}
	w := im.w
	w.objs = append(w.objs, slot{o: o, imp: im})
	n := w.last()
	im.copied[r.Num] = n
	return n
}

// mapRef translates one source reference.
func (im *Importer) mapRef(r pdf.Ref) Ref {
	if im.MapRef != nil {
		return im.MapRef(r)
	}
	return im.Copy(r)
}

// Map returns the source-space value o rebuilt in the output space: source
// references translated, the keys of Drop left out of dictionaries, and a
// stream given its bytes as stored, decrypted but still filtered.
func (im *Importer) Map(o pdf.Object) pdf.Object { return im.mapValue(o, 0) }

func (im *Importer) mapValue(o pdf.Object, depth int) pdf.Object {
	if depth > maxDepth {
		return pdf.Null
	}
	switch o.Kind() {
	case pdf.KindRef:
		r, _ := o.Ref()
		if n := im.mapRef(r); n > 0 {
			return n.Object()
		}
		return pdf.Null
	case pdf.KindArray:
		a, _ := o.Array()
		out := make(pdf.Array, len(a))
		for i, e := range a {
			out[i] = im.mapValue(e, depth+1)
		}
		return out.Object()
	case pdf.KindDict:
		d, _ := o.Dict()
		return im.mapDict(d, depth, nil).Object()
	case pdf.KindStream:
		s, _ := o.Stream()
		return im.mapStream(s, depth)
	}
	return o
}

// mapDict maps the entries of d, leaving out those of Drop and of skip.
func (im *Importer) mapDict(d pdf.Dict, depth int, skip map[pdf.Name]bool) pdf.Dict {
	entries := make([]pdf.Entry, 0, d.Len())
	for k, v := range d.All() {
		if im.Drop[k] || skip[k] {
			continue
		}
		m := im.mapValue(v, depth+1)
		if a, ok := m.Array(); ok && im.Compact[k] {
			m = slices.DeleteFunc(a, pdf.Object.IsNull).Object()
		}
		entries = append(entries, pdf.Entry{Key: k, Val: m})
	}
	return pdf.NewDict(entries...)
}

// streamKeys are the stream dictionary entries mapStream writes itself.
var streamKeys = map[pdf.Name]bool{"Length": true, "Filter": true, "DecodeParms": true}

// mapStream maps a source stream: its bytes as stored, decrypted but still
// filtered, under its mapped dictionary. A leading /Crypt filter goes: the
// output is not encrypted.
func (im *Importer) mapStream(s *pdf.Stream, depth int) pdf.Object {
	data := im.src.Raw(s)
	filter, parms := s.Dict.Get("Filter"), s.Dict.Get("DecodeParms")
	if f, p, ok := dropCrypt(im.src.Resolve(filter), im.src.Resolve(parms)); ok {
		filter, parms = f, p
	}
	dict := im.mapDict(s.Dict, depth, streamKeys)
	if !filter.IsNull() {
		dict = dict.With("Filter", im.mapValue(filter, depth+1))
	}
	if !parms.IsNull() {
		dict = dict.With("DecodeParms", im.mapValue(parms, depth+1))
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
