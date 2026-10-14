package pdf

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// TestEncodeCCITTG4 encodes bitmaps — blank, full, random, stripes, long
// runs past the make-up codes — and decodes them back.
func TestEncodeCCITTG4(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, tt := range []struct {
		name string
		w, h int
		px   func(x, y int) bool // black
	}{
		{"white", 33, 7, func(x, y int) bool { return false }},
		{"black", 33, 7, func(x, y int) bool { return true }},
		{"random", 61, 40, func(x, y int) bool { return rng.IntN(2) == 0 }},
		{"sparse", 200, 50, func(x, y int) bool { return rng.IntN(40) == 0 }},
		{"stripes", 100, 30, func(x, y int) bool { return (x/3+y)%5 < 2 }},
		{"diagonal", 80, 80, func(x, y int) bool { return x >= y && x < y+7 }},
		{"long runs", 6000, 3, func(x, y int) bool { return x > 2600+y*700 }},
		{"one pixel", 1, 1, func(x, y int) bool { return true }},
	} {
		stride := (tt.w + 7) / 8
		data := make([]byte, stride*tt.h)
		for y := range tt.h {
			for x := range tt.w {
				if !tt.px(x, y) {
					data[y*stride+x/8] |= 1 << (7 - uint(x%8)) // white
				}
			}
		}
		enc := EncodeCCITTG4(data, tt.w, tt.h)
		got, err := ccittDecode(enc, ccittParams{k: -1, columns: tt.w, rows: tt.h, endOfBlock: true})
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		// The padding bits of a row are not compared.
		mask := byte(0xff) << uint((8-tt.w%8)%8)
		for y := range tt.h {
			a, b := data[y*stride:(y+1)*stride], got[y*stride:(y+1)*stride]
			if !bytes.Equal(a[:stride-1], b[:stride-1]) || a[stride-1]&mask != b[stride-1]&mask {
				t.Fatalf("%s: row %d decodes as %x, want %x", tt.name, y, b, a)
			}
		}
		if tt.name == "white" && len(enc) > 16 {
			t.Errorf("a white page takes %d bytes", len(enc))
		}
	}
}
