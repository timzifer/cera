package main

import (
	"fmt"
	"strconv"

	"github.com/go-pdfkit/reader"
)

// v06 is go-pdfkit/reader v0.6.0, the reader cera used before its own.
type v06 struct{}

func init() { register(v06{}) }

func (v06) name() string { return "v06" }

func (v06) snapshot(data []byte, password string) *snapshot {
	s := &snapshot{}
	d, err := reader.OpenWithPassword(data, password)
	if err != nil {
		s.set("open", "error")
		return s
	}
	s.set("open", "ok")
	s.set("version", d.Version())
	s.set("repaired", strconv.FormatBool(d.Repaired()))
	conv := func(o reader.Object) value { return fromV06(d, o) }
	tr := conv(d.Trailer())
	s.set("trailer", tr.text())

	w := &walker{snap: s, seen: map[[2]int]bool{}}
	w.resolve = func(r [2]int) (value, error) {
		o, err := d.Get(reader.Ref{Num: r[0], Gen: r[1]})
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
			w.visit(value{kind: "ref", ref: [2]int{r.Num, r.Gen}})
		}
		p, err := d.Page(i + 1)
		if err != nil {
			s.set(fmt.Sprintf("page/%d", i), "error")
			continue
		}
		pv := conv(p)
		s.set(fmt.Sprintf("page/%d", i), pv.text())
	}
	w.visit(tr)
	w.run()
	return s
}

// fromV06 converts a v0.6 object; streams are decoded with recovery.
func fromV06(d *reader.Document, o reader.Object) value {
	switch o := o.(type) {
	case nil, reader.Null:
		return value{kind: "null"}
	case reader.Bool:
		return value{kind: "bool", bool: bool(o)}
	case reader.Integer:
		return value{kind: "num", num: float64(o)}
	case reader.Real:
		return value{kind: "num", num: float64(o)}
	case reader.String:
		return value{kind: "str", str: []byte(o)}
	case reader.Name:
		return value{kind: "name", str: []byte(o)}
	case reader.Ref:
		return value{kind: "ref", ref: [2]int{o.Num, o.Gen}}
	case reader.Array:
		v := value{kind: "array", elems: make([]value, len(o))}
		for i, e := range o {
			v.elems[i] = fromV06(d, e)
		}
		return v
	case reader.Dict:
		return dictV06(d, o, "dict")
	case *reader.Stream:
		v := dictV06(d, o.Dict, "stream")
		dec := d.DecodeStreamRecovering(o)
		v.stream = &streamInfo{
			raw:       o.Raw,
			data:      dec.Data,
			undecoded: dec.Undecoded,
			image:     string(dec.Image),
			filter:    string(dec.Filter),
			recovered: dec.Recovered,
			failed:    dec.Cause != nil,
		}
		return v
	}
	return value{kind: fmt.Sprintf("%T", o)}
}

func dictV06(d *reader.Document, o reader.Dict, kind string) value {
	keys := make([]string, 0, len(o))
	vals := make([]value, 0, len(o))
	for k, e := range o {
		keys = append(keys, string(k))
		vals = append(vals, fromV06(d, e))
	}
	keys, vals = sortDict(keys, vals)
	return value{kind: kind, keys: keys, vals: vals}
}
