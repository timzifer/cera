package main

import (
	"fmt"
	"strconv"

	"github.com/timzifer/cera/internal/pdf"
)

// pdfBackend is cera's own reader, internal/pdf.
type pdfBackend struct{}

func init() { register(pdfBackend{}) }

func (pdfBackend) name() string { return "pdf" }

func (pdfBackend) snapshot(data []byte, password string) *snapshot {
	s := &snapshot{}
	d, err := pdf.OpenWithPassword(data, password)
	if err != nil {
		s.set("open", "error")
		return s
	}
	s.set("open", "ok")
	s.set("version", d.Version())
	s.set("repaired", strconv.FormatBool(d.Repaired()))
	conv := func(o pdf.Object) value { return fromPDF(d, o) }
	tr := conv(d.Trailer().Object())
	if d.Trailer().IsZero() {
		tr = value{kind: "dict"}
	}
	s.set("trailer", tr.text())

	w := &walker{snap: s, seen: map[[2]int]bool{}}
	w.resolve = func(r [2]int) (value, error) {
		o, err := d.Get(pdf.Ref{Num: int32(r[0]), Gen: int32(r[1])})
		if err != nil {
			return value{}, err
		}
		return conv(o), nil
	}
	n := d.PageCount()
	s.set("pages", strconv.Itoa(n))
	for i := range n {
		if r, ok := d.PageRef(i + 1); ok {
			s.set(fmt.Sprintf("page/%d/ref", i), fmt.Sprintf("%d %d R", r.Num, r.Gen))
			w.visit(value{kind: "ref", ref: [2]int{int(r.Num), int(r.Gen)}})
		}
		p, err := d.Page(i + 1)
		if err != nil {
			s.set(fmt.Sprintf("page/%d", i), "error")
			continue
		}
		s.set(fmt.Sprintf("page/%d", i), conv(p.Object()).text())
		if c, err := d.PageContents(i + 1); err == nil {
			s.set(fmt.Sprintf("contents/%d", i), contentsText(c.Data, c.Undecoded, string(c.Filter), c.Recovered))
		}
	}
	w.visit(tr)
	w.run()
	return s
}

// fromPDF converts an internal/pdf object; streams are decoded.
func fromPDF(d *pdf.Document, o pdf.Object) value {
	switch o.Kind() {
	case pdf.KindNull:
		return value{kind: "null"}
	case pdf.KindBool:
		b, _ := o.Bool()
		return value{kind: "bool", bool: b}
	case pdf.KindInteger, pdf.KindReal:
		f, _ := o.Float()
		return value{kind: "num", num: f}
	case pdf.KindString:
		b, _ := o.Str()
		return value{kind: "str", str: b}
	case pdf.KindName:
		n, _ := o.Name()
		return value{kind: "name", str: []byte(n)}
	case pdf.KindRef:
		r, _ := o.Ref()
		return value{kind: "ref", ref: [2]int{int(r.Num), int(r.Gen)}}
	case pdf.KindArray:
		a, _ := o.Array()
		v := value{kind: "array", elems: make([]value, len(a))}
		for i, e := range a {
			v.elems[i] = fromPDF(d, e)
		}
		return v
	case pdf.KindDict:
		dict, _ := o.Dict()
		return dictPDF(d, dict, "dict")
	case pdf.KindStream:
		st, _ := o.Stream()
		v := dictPDF(d, st.Dict, "stream")
		dec := d.Decode(st)
		v.stream = &streamInfo{
			raw:       d.Raw(st),
			data:      dec.Data,
			undecoded: dec.Undecoded,
			image:     string(dec.Image),
			filter:    string(dec.Filter),
			recovered: dec.Recovered,
			failed:    dec.Cause != nil,
		}
		return v
	}
	return value{kind: o.Kind().String()}
}

func dictPDF(d *pdf.Document, dict pdf.Dict, kind string) value {
	keys := make([]string, 0, dict.Len())
	vals := make([]value, 0, dict.Len())
	for k, e := range dict.All() {
		keys = append(keys, string(k))
		vals = append(vals, fromPDF(d, e))
	}
	keys, vals = sortDict(keys, vals)
	return value{kind: kind, keys: keys, vals: vals}
}
