package cera

import (
	"bytes"
	"fmt"
	"image"
	"testing"
)

// plainCFF builds a bare CFF program (not CID-keyed, ISOAdobe charset,
// standard encoding) whose glyph 1, "space", is a square of 500 units,
// with FontMatrix m written as reals, and the given Private DICT (by
// default only nominalWidthX 0).
func plainCFF(m [6]float64, private ...byte) []byte {
	num := func(v int) []byte { return []byte{28, byte(v >> 8), byte(v)} }
	off := func(v int) []byte { return []byte{29, byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }
	real := func(v float64) []byte {
		s := fmt.Sprintf("%g", v)
		var nibs []byte
		for i := 0; i < len(s); i++ {
			switch c := s[i]; {
			case c >= '0' && c <= '9':
				nibs = append(nibs, c-'0')
			case c == '.':
				nibs = append(nibs, 10)
			case c == '-' && i > 0:
				nibs = append(nibs, 12) // e-
			case c == '-':
				nibs = append(nibs, 14)
			}
		}
		nibs = append(nibs, 15)
		if len(nibs)%2 == 1 {
			nibs = append(nibs, 15)
		}
		out := []byte{30}
		for i := 0; i < len(nibs); i += 2 {
			out = append(out, nibs[i]<<4|nibs[i+1])
		}
		return out
	}
	index := func(objs ...[]byte) []byte {
		if len(objs) == 0 {
			return []byte{0, 0}
		}
		b := []byte{byte(len(objs) >> 8), byte(len(objs)), 4}
		o := 1
		put := func(v int) { b = append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v)) }
		put(o)
		for _, x := range objs {
			o += len(x)
			put(o)
		}
		for _, x := range objs {
			b = append(b, x...)
		}
		return b
	}
	var square []byte
	for _, v := range []int{500, 0, 0} { // width, rmoveto
		square = append(square, num(v)...)
	}
	square = append(square, 21)
	for _, v := range []int{500, 0, 0, 500, -500, 0} {
		square = append(square, num(v)...)
	}
	square = append(square, 5, 14) // rlineto endchar
	charStrings := index([]byte{14}, square)
	if private == nil {
		private = []byte{139, 21} // nominalWidthX 0
	}
	top := func(csAt, privAt int) []byte {
		var t []byte
		for _, v := range m {
			t = append(t, real(v)...)
		}
		t = append(t, 12, 7) // FontMatrix
		t = append(append(t, off(csAt)...), 17)
		return append(append(append(t, off(len(private))...), off(privAt)...), 18)
	}
	build := func(csAt, privAt int) ([]byte, int, int) {
		var b bytes.Buffer
		b.Write([]byte{1, 0, 4, 4})
		b.Write(index([]byte("T")))
		b.Write(index(top(csAt, privAt)))
		b.Write(index()) // String INDEX
		b.Write(index()) // Global Subrs
		cs := b.Len()
		b.Write(charStrings)
		priv := b.Len()
		b.Write(private)
		return b.Bytes(), cs, priv
	}
	_, cs, priv := build(0, 0)
	b, _, _ := build(cs, priv)
	return b
}

// An oblique face whose slant is in its FontMatrix is drawn slanted (borb
// 0374.pdf, Helvetica-BlackOblique).
func TestFontMatrixShear(t *testing.T) {
	for _, c := range []struct {
		name    string
		m       [6]float64
		slanted bool
	}{
		{"upright", [6]float64{0.001, 0, 0, 0.001, 0, 0}, false},
		{"oblique", [6]float64{0.001, 0, 0.001, 0.001, 0, 0}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			prog := plainCFF(c.m)
			font := "<< /Type /Font /Subtype /Type1 /BaseFont /ABCDEF+Test /FirstChar 32 /LastChar 32 /Widths [500] /FontDescriptor 101 0 R >>"
			desc := "<< /Type /FontDescriptor /FontName /ABCDEF+Test /Flags 32 /FontFile3 102 0 R >>"
			stream := fmt.Sprintf("<< /Subtype /Type1C /Length %d >>\nstream\n%s\nendstream", len(prog), prog)
			img, st, err := renderPage(t, textPDF("BT /F1 100 Tf 50 50 Td ( ) Tj ET", font, desc, stream), 0,
				RenderOptions{Background: white, Workers: 1})
			if err != nil {
				t.Fatal(err)
			}
			if len(st.Unsupported) != 0 {
				t.Errorf("unsupported %v", st.Unsupported)
			}
			// The square covers x 50..100, y 50..100 (image rows 0..50);
			// slanted by 45°, its top row runs from x 100 to 150.
			topLeft := inked(img, image.Rect(52, 2, 60, 8))
			topRight := inked(img, image.Rect(130, 2, 140, 8))
			bottom := inked(img, image.Rect(60, 42, 90, 48))
			if bottom == 0 || (topLeft > 0) != !c.slanted || (topRight > 0) != c.slanted {
				t.Errorf("ink: top left %d, top right %d, bottom %d", topLeft, topRight, bottom)
			}
		})
	}
}

func TestType1Matrix(t *testing.T) {
	prog, _, _ := type1Program("G65")
	if m, ok := type1Matrix(prog); !ok || m != [6]float64{0.001, 0, 0, 0.001, 0, 0} {
		t.Errorf("PFA: %v %v", m, ok)
	}
	slanted := bytes.Replace(prog, []byte("[0.001 0 0 0.001 0 0]"), []byte("{0.001 0 0.000212 0.001 0 0}"), 1)
	if _, ok := fontShear("FontFile", slanted); !ok {
		t.Error("slanted Type 1 matrix not read")
	}
	if _, ok := fontShear("FontFile", prog); ok {
		t.Error("a plain scale read as a shear")
	}
}
