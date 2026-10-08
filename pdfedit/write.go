package pdfedit

import (
	"fmt"
	"math"
	"strconv"

	"github.com/timzifer/cera/internal/pdf"
)

// maxDepth bounds how deeply direct objects nest in what is written; the
// parser bounds it on reading already, this guards the recursion.
const maxDepth = 256

// ref returns the output number of a source object, numbering it for
// copying when it is first met, or 0 when the reference is written as
// null: a page that is not copied, a page tree node, a missing object.
func (x *extractor) ref(r pdf.Ref) int32 {
	if r.Num <= 0 || x.skip[r.Num] {
		return 0
	}
	if n, ok := x.pageOut[r.Num]; ok {
		return n
	}
	if x.isPage[r.Num] {
		return 0
	}
	if n, ok := x.copied[r.Num]; ok {
		return n
	}
	o, err := x.d.Get(r)
	if err != nil || o.IsNull() {
		return 0
	}
	if d, ok := o.Dict(); ok {
		// Reached other than through /Parent: still not part of a page.
		switch t, _ := d.Get("Type").Name(); t {
		case "Page", "Pages", "Catalog", "XRef", "ObjStm":
			return 0
		}
	}
	n := x.alloc(o)
	x.copied[r.Num] = n
	return n
}

// write serialises the output objects, which may add more as it goes, and
// the cross-reference table and trailer.
func (x *extractor) write() []byte {
	b := &x.buf
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	var offs []int
	for i := 0; i < len(x.objs); i++ {
		offs = append(offs, b.Len())
		fmt.Fprintf(b, "%d 0 obj\n", i+1)
		o := x.objs[i]
		if s, ok := o.Stream(); ok {
			x.stream(s)
		} else {
			x.value(o, 0)
		}
		b.WriteString("\nendobj\n")
	}
	xref := b.Len()
	fmt.Fprintf(b, "xref\n0 %d\n0000000000 65535 f\r\n", len(offs)+1)
	for _, off := range offs {
		fmt.Fprintf(b, "%010d 00000 n\r\n", off)
	}
	fmt.Fprintf(b, "trailer\n<</Size %d /Root %d 0 R", len(offs)+1, catalogNum)
	if x.infoNum != 0 {
		fmt.Fprintf(b, " /Info %d 0 R", x.infoNum)
	}
	fmt.Fprintf(b, ">>\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}

// stream writes a source stream: its bytes as stored, decrypted but still
// filtered, under its own dictionary with the length they have now. A
// leading /Crypt filter goes: the output is not encrypted.
func (x *extractor) stream(s *pdf.Stream) {
	data := x.d.Raw(s)
	dict := s.Dict
	filter, parms := dict.Get("Filter"), dict.Get("DecodeParms")
	if f, p, ok := dropCrypt(x.d.Resolve(filter), x.d.Resolve(parms)); ok {
		filter, parms = f, p
	}
	b := &x.buf
	b.WriteString("<<")
	for k, v := range dict.All() {
		switch k {
		case "Length", "Filter", "DecodeParms", "Parent":
			continue
		}
		x.name(k)
		b.WriteByte(' ')
		x.value(v, 1)
	}
	if !filter.IsNull() {
		b.WriteString("/Filter ")
		x.value(filter, 1)
	}
	if !parms.IsNull() {
		b.WriteString("/DecodeParms ")
		x.value(parms, 1)
	}
	fmt.Fprintf(b, "/Length %d>>\nstream\n", len(data))
	b.Write(data)
	b.WriteString("\nendstream")
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

// value writes a direct object. Source references are numbered for
// copying, output references written as they are.
func (x *extractor) value(o pdf.Object, depth int) {
	b := &x.buf
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
		x.str(s)
	case pdf.KindName:
		n, _ := o.Name()
		x.name(n)
	case pdf.KindArray:
		a, _ := o.Array()
		b.WriteByte('[')
		for i, e := range a {
			if i > 0 {
				b.WriteByte(' ')
			}
			x.value(e, depth+1)
		}
		b.WriteByte(']')
	case pdf.KindDict:
		d, _ := o.Dict()
		b.WriteString("<<")
		for k, v := range d.All() {
			// /Parent leads up a tree the output does not have: the page
			// tree, a field's parents. Only the links Extract makes itself
			// stay.
			if r, ok := v.Ref(); k == "Parent" && (!ok || r.Num >= 0) {
				continue
			}
			x.name(k)
			b.WriteByte(' ')
			x.value(v, depth+1)
		}
		b.WriteString(">>")
	case pdf.KindRef:
		r, _ := o.Ref()
		n := -r.Num
		if r.Num > 0 {
			n = x.ref(r)
		}
		if n <= 0 {
			b.WriteString("null")
			return
		}
		fmt.Fprintf(b, "%d 0 R", n)
	default:
		// Null, and a stream where a direct object belongs.
		b.WriteString("null")
	}
}

// name writes a name, escaping what may not appear in it literally.
func (x *extractor) name(n pdf.Name) {
	b := &x.buf
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

// str writes a literal string. Parentheses and backslashes are escaped,
// and line ends, which a reader would normalise.
func (x *extractor) str(s []byte) {
	b := &x.buf
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
