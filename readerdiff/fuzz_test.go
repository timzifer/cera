package main

import (
	"testing"

	"github.com/go-pdfkit/reader"
	"github.com/timzifer/cera/internal/pdf"
)

// FuzzParseDiff parses the same bytes with both readers: they must agree on
// whether it is an object and on its value.
func FuzzParseDiff(f *testing.F) {
	for _, s := range []string{
		"<</A [1 2 0 R (x) <41>] /B <</C /D#20>>>>", "[1 -2.5 .3 --4 true null]",
		"(a\\(b\\)\\101\r\n)", "12 0 R", "<</A 1 /A 2>>", "9223372036854775808",
		"[1 0 R 2 0 R]", "/A#zz", "(\\", "<4 1 x>", "+.5", "-.", "[0.0000000000000000000000001]",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if depth(b) > pdf.MaxNesting-2 {
			return // nesting-limit
		}
		vo, vn, verr := reader.ParseObject(b)
		po, pn, perr := pdf.ParseObject(b)
		if (verr == nil) != (perr == nil) {
			t.Fatalf("v06 error %v, pdf error %v", verr, perr)
		}
		if verr != nil {
			return
		}
		if vn != pn {
			t.Fatalf("consumed v06 %d, pdf %d", vn, pn)
		}
		va := fromV06(nil, vo)
		if bigRef(va) {
			return // references are 32-bit; neither reader resolves these
		}
		a, c := va.text(), fromPDF(nil, po).text()
		if a != c {
			t.Fatalf("\nv06 %s\npdf %s", a, c)
		}
	})
}

// bigRef reports a reference whose numbers do not fit 32 bits.
func bigRef(v value) bool {
	if v.kind == "ref" && (v.ref[0] > 1<<31-1 || v.ref[1] > 1<<31-1) {
		return true
	}
	for _, e := range v.elems {
		if bigRef(e) {
			return true
		}
	}
	for _, e := range v.vals {
		if bigRef(e) {
			return true
		}
	}
	return false
}

// depth is how deeply brackets nest in b, roughly.
func depth(b []byte) int {
	d, m := 0, 0
	for _, c := range b {
		switch c {
		case '[', '<':
			d++
			m = max(m, d)
		case ']', '>':
			d--
		}
	}
	return m
}

// FuzzDecodeDiff runs the same filter chain with both readers.
func FuzzDecodeDiff(f *testing.F) {
	f.Add(byte(0), byte(12), byte(3), []byte{2, 1, 2, 3, 1, 4, 5, 6})
	f.Add(byte(1), byte(0), byte(0), []byte{0x80, 0x0B, 0x60, 0x50, 0x22, 0x0C, 0x0C, 0x85, 0x01})
	f.Add(byte(2), byte(0), byte(0), []byte("<~87cURD]i,\"Ebo80~>"))
	f.Add(byte(4), byte(0), byte(0), []byte{2, 'a', 'b', 'c', 254, 'x', 128})
	names := []string{"LZWDecode", "ASCII85Decode", "ASCIIHexDecode", "RunLengthDecode", "CCITTFaxDecode"}
	f.Fuzz(func(t *testing.T, which, pred, cols byte, data []byte) {
		name := names[int(which)%len(names)]
		p := pred % 16
		if p == 2 {
			p = 1 // tiff-subbyte: v0.6 refuses below 8 bits
		}
		vd := reader.Dict{"Filter": reader.Name(name), "DecodeParms": reader.Dict{
			"Predictor": reader.Integer(p), "Columns": reader.Integer(int64(cols) + 1),
			"K": reader.Integer(int64(int8(cols)) % 3),
		}}
		pd := pdf.NewDict(
			pdf.Entry{Key: "Filter", Val: pdf.Name(name).Object()},
			pdf.Entry{Key: "DecodeParms", Val: pdf.NewDict(
				pdf.Entry{Key: "Predictor", Val: pdf.Integer(int64(p))},
				pdf.Entry{Key: "Columns", Val: pdf.Integer(int64(cols) + 1)},
				pdf.Entry{Key: "K", Val: pdf.Integer(int64(int8(cols)) % 3)},
			).Object()},
		)
		v := reader.DecodeRecovering(vd, data, nil)
		w := pdf.DecodeRecovering(pd, data)
		if string(v.Data) != string(w.Data) || string(v.Undecoded) != string(w.Undecoded) ||
			v.Recovered != w.Recovered || string(v.Filter) != string(w.Filter) {
			t.Fatalf("%s: v06 %d/%d %v, pdf %d/%d %v", name, len(v.Data), len(v.Undecoded), v.Recovered,
				len(w.Data), len(w.Undecoded), w.Recovered)
		}
	})
}
