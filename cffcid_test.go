package cera

import (
	"bytes"
	"fmt"
	"image"
	"testing"
)

// cidCFF builds a bare CID-keyed CFF program (Adobe-Identity-0) whose
// glyph 1 (CID 1) and glyph 2 (CID 2) are squares of size units, with one
// Font DICT per entry of fdMatrices (nil: none), glyph g in Font DICT
// sel[g], and topMatrix in the Top DICT unless nil.
func cidCFF(size int, topMatrix []float64, fdMatrices [][]float64, sel []byte) []byte {
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
			case c == '-':
				nibs = append(nibs, 14)
			case c == 'e' && s[i+1] == '-':
				nibs = append(nibs, 12)
				i++
			case c == 'e':
				nibs = append(nibs, 11)
				if s[i+1] == '+' {
					i++
				}
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
	matrixOp := func(m []float64) []byte {
		var b []byte
		for _, v := range m {
			b = append(b, real(v)...)
		}
		return append(b, 12, 7)
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
	square := append(append(append(num(0), num(0)...), 21), // rmoveto
		append(append(append(append(append(append(num(size), num(0)...), num(0)...), num(size)...), num(-size)...), num(0)...), 5, 14)...) // rlineto endchar
	charStrings := index([]byte{14}, square, square)
	charset := []byte{0, 0, 1, 0, 2} // format 0: glyphs 1 and 2 are CIDs 1 and 2
	fdSelect := append([]byte{0}, sel...)
	private := []byte{139, 21} // nominalWidthX 0

	// Every offset is five bytes, so the Top DICT has the same size
	// whatever they are: build it once to measure, then for real.
	layout := func(charsetAt, fdSelectAt, csAt, fdArrayAt, privAt int) (top, fdArray []byte) {
		top = append(append(append(num(391), num(392)...), num(0)...), 12, 30) // ROS
		if topMatrix != nil {
			top = append(top, matrixOp(topMatrix)...)
		}
		top = append(append(top, off(charsetAt)...), 15)
		top = append(append(top, off(csAt)...), 17)
		top = append(append(top, off(fdArrayAt)...), 12, 36)
		top = append(append(top, off(fdSelectAt)...), 12, 37)
		var fds [][]byte
		for _, m := range fdMatrices {
			var d []byte
			if m != nil {
				d = matrixOp(m)
			}
			d = append(append(append(d, off(len(private))...), off(privAt)...), 18)
			fds = append(fds, d)
		}
		return top, index(fds...)
	}
	head := []byte{1, 0, 4, 4}
	build := func(charsetAt, fdSelectAt, csAt, fdArrayAt, privAt int) ([]byte, []int) {
		top, fdArray := layout(charsetAt, fdSelectAt, csAt, fdArrayAt, privAt)
		var b bytes.Buffer
		b.Write(head)
		b.Write(index([]byte("T")))
		b.Write(index(top))
		b.Write(index([]byte("Adobe"), []byte("Identity")))
		b.Write(index()) // Global Subrs
		at := []int{b.Len()}
		b.Write(charset)
		at = append(at, b.Len())
		b.Write(fdSelect)
		at = append(at, b.Len())
		b.Write(charStrings)
		at = append(at, b.Len())
		b.Write(fdArray)
		at = append(at, b.Len())
		b.Write(private)
		return b.Bytes(), at
	}
	_, at := build(0, 0, 0, 0, 0)
	b, _ := build(at[0], at[1], at[2], at[3], at[4])
	return b
}

func TestCFFFDScales(t *testing.T) {
	const k = 1.0 / 2048
	for _, tc := range []struct {
		name   string
		top    []float64
		fds    [][]float64
		sel    []byte
		want   [3]float64 // x scale of glyphs 0, 1, 2; 0 = nil result
		absent bool
	}{
		{name: "fd only", fds: [][]float64{{k, 0, 0, k, 0, 0}}, sel: []byte{0, 0, 0}, want: [3]float64{k, k, k}},
		{name: "top identity", top: []float64{1, 0, 0, 1, 0, 0}, fds: [][]float64{{0.001, 0, 0, 0.001, 0, 0}}, sel: []byte{0, 0, 0}, want: [3]float64{0.001, 0.001, 0.001}},
		{name: "both a thousandth", top: []float64{0.001, 0, 0, 0.001, 0, 0}, fds: [][]float64{{0.001, 0, 0, 0.001, 0, 0}}, sel: []byte{0, 0, 0}, want: [3]float64{0.001, 0.001, 0.001}},
		{name: "per glyph", fds: [][]float64{{0.001, 0, 0, 0.001, 0, 0}, {k, 0, 0, k, 0, 0}}, sel: []byte{0, 1, 0}, want: [3]float64{0.001, k, 0.001}},
		{name: "top only", top: []float64{k, 0, 0, k, 0, 0}, fds: [][]float64{nil}, sel: []byte{0, 0, 0}, absent: true},
		{name: "none", fds: [][]float64{nil}, sel: []byte{0, 0, 0}, absent: true},
	} {
		s := readCFFFDScales(cidCFF(2048, tc.top, tc.fds, tc.sel))
		if tc.absent {
			if s != nil {
				t.Errorf("%s: %+v, want nil", tc.name, s)
			}
			continue
		}
		if s == nil {
			t.Errorf("%s: nil", tc.name)
			continue
		}
		for g, w := range tc.want {
			if sx, sy := s.scale(g); !approx(sx*1e6, w*1e6) || !approx(sy*1e6, w*1e6) {
				t.Errorf("%s: glyph %d scale %g %g, want %g", tc.name, g, sx, sy, w)
			}
		}
	}
	// Truncated and foreign programs are nothing to scale.
	full := cidCFF(2048, nil, [][]float64{{k, 0, 0, k, 0, 0}}, []byte{0, 0, 0})
	for n := range len(full) {
		readCFFFDScales(full[:n])
	}
	if readCFFFDScales([]byte("OTTO\x00\x01")) != nil {
		t.Error("an OpenType font read as bare CFF")
	}
}

// TestCIDCFFFontMatrixInFDArray draws a CID-keyed CFF glyph of 2048
// units per em, given in the FDArray only, at 50 points: it must be 50
// pixels wide, not twice that (#19).
func TestCIDCFFFontMatrixInFDArray(t *testing.T) {
	const k = 1.0 / 2048
	prog := cidCFF(2048, nil, [][]float64{{k, 0, 0, k, 0, 0}}, []byte{0, 0, 0})
	font := `<< /Type /Font /Subtype /Type0 /BaseFont /ABCDEF+Square /Encoding /Identity-H
		/DescendantFonts [<< /Type /Font /Subtype /CIDFontType0 /BaseFont /ABCDEF+Square
		/CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >>
		/FontDescriptor 101 0 R /DW 1000 >>] >>`
	desc := "<< /Type /FontDescriptor /FontName /ABCDEF+Square /Flags 4 /FontBBox [0 0 2048 2048] /FontFile3 102 0 R >>"
	stream := fmt.Sprintf("<< /Subtype /CIDFontType0C /Length %d >>\nstream\n%s\nendstream", len(prog), prog)
	img, st, err := renderPage(t, textPDF("BT /F1 50 Tf 10 20 Td <0001> Tj ET", font, desc, stream), 0, RenderOptions{Background: white})
	if err != nil {
		t.Fatal(err)
	}
	if st.Glyphs != 1 || len(st.Unsupported) != 0 {
		t.Errorf("stats %+v", st)
	}
	// The square spans x 10..60 and, 100 high, y 30..80.
	bounds := image.Rectangle{}
	for y := range 100 {
		for x := range 200 {
			if img.RGBAAt(x, y) != white {
				bounds = bounds.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	if want := image.Rect(10, 30, 60, 80); bounds != want {
		t.Errorf("glyph covers %v, want %v", bounds, want)
	}
}
