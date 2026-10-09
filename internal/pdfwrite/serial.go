package pdfwrite

import (
	"bufio"
	"crypto/md5"
	"fmt"
	"hash"
	"io"
	"math"
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

// Write writes the file to out: the header, the output objects in order,
// mapping source objects as it goes (which may copy more), and the
// cross-reference table and trailer. An object is dropped from memory once
// it is written. The writer must not be used after it.
func (w *Writer) Write(out io.Writer, t Trailer) error {
	c := &counter{w: out, h: md5.New()}
	b := bufio.NewWriterSize(c, 64<<10)
	e := &encoder{b: b, w: w}
	version := w.Version
	if version == "" {
		version = "1.7"
	}
	fmt.Fprintf(b, "%%PDF-%s\n%%\xe2\xe3\xcf\xd3\n", version)
	var offs []int64
	for i := 0; i < len(w.objs) && c.err == nil; i++ {
		s := w.objs[i]
		w.objs[i] = slot{}
		if s.imp != nil {
			s.o = s.imp.Map(s.o)
		}
		offs = append(offs, c.n+int64(b.Buffered()))
		fmt.Fprintf(b, "%d 0 obj\n", i+1)
		e.value(s.o, 0)
		b.WriteString("\nendobj\n")
	}
	xref := c.n + int64(b.Buffered())
	fmt.Fprintf(b, "xref\n0 %d\n0000000000 65535 f\r\n", len(offs)+1)
	for _, off := range offs {
		fmt.Fprintf(b, "%010d 00000 n\r\n", off)
	}
	if err := b.Flush(); err != nil {
		return err
	}
	sum := c.h.Sum(nil)
	first := t.ID
	if first == nil {
		first = sum
	}
	fmt.Fprintf(b, "trailer\n<</Size %d /Root %d 0 R", len(offs)+1, t.Root)
	if t.Info != 0 {
		fmt.Fprintf(b, " /Info %d 0 R", t.Info)
	}
	fmt.Fprintf(b, " /ID [<%x> <%x>]>>\nstartxref\n%d\n%%%%EOF\n", first, sum, xref)
	return b.Flush()
}

// An encoder writes output-space objects.
type encoder struct {
	b *bufio.Writer
	w *Writer
}

// value writes an output-space object. A stream is written in full only at
// the top of an object; nested, as anything else not a direct object, it
// is written as null.
func (e *encoder) value(o pdf.Object, depth int) {
	b := e.b
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
		for i, x := range a {
			if i > 0 {
				b.WriteByte(' ')
			}
			e.value(x, depth+1)
		}
		b.WriteByte(']')
	case pdf.KindDict:
		d, _ := o.Dict()
		e.dict(d, depth, -1)
	case pdf.KindStream:
		s, _ := o.Stream()
		data, ok := s.Unencrypted()
		if depth > 0 || !ok {
			b.WriteString("null")
			return
		}
		e.dict(s.Dict, depth, len(data))
		b.WriteString("\nstream\n")
		b.Write(data)
		b.WriteString("\nendstream")
	case pdf.KindRef:
		r, _ := o.Ref()
		if r.Num <= 0 || int(r.Num) > len(e.w.objs) {
			b.WriteString("null")
			return
		}
		fmt.Fprintf(b, "%d 0 R", r.Num)
	default:
		b.WriteString("null")
	}
}

// dict writes a dictionary; with a length of 0 or more, its /Length is
// replaced by it.
func (e *encoder) dict(d pdf.Dict, depth, length int) {
	b := e.b
	b.WriteString("<<")
	for k, v := range d.All() {
		if length >= 0 && k == "Length" {
			continue
		}
		writeName(b, k)
		b.WriteByte(' ')
		e.value(v, depth+1)
	}
	if length >= 0 {
		fmt.Fprintf(b, "/Length %d", length)
	}
	b.WriteString(">>")
}

// writeName writes a name, escaping what may not appear in it literally.
func writeName(b *bufio.Writer, n pdf.Name) {
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
func writeString(b *bufio.Writer, s []byte) {
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
