package cera

import (
	"context"
	"fmt"
	"image"
	"strings"
	"testing"
)

// TestDeviceCMYKPatches paints flat DeviceCMYK patches (#20). Through the
// press profile they come out as Little CMS converts them with colord's
// SWOP TR 005 profile (transicc -t 1 -b), close to the press colours of
// the other renderers (MuPDF and Ghostscript draw cyan 0 174 239, their
// own profile); NaiveCMYK gives the device values.
func TestDeviceCMYKPatches(t *testing.T) {
	patches := []struct {
		cmyk        string
		swop, naive [3]uint8
	}{
		{"1 0 0 0", [3]uint8{0, 174, 240}, [3]uint8{0, 255, 255}},
		{"0 1 0 0", [3]uint8{236, 11, 141}, [3]uint8{255, 0, 255}},
		{"0 0 1 0", [3]uint8{255, 242, 0}, [3]uint8{255, 255, 0}},
		{"0 0 0 1", [3]uint8{43, 40, 41}, [3]uint8{0, 0, 0}},
		{"0.5 0 0 0", [3]uint8{118, 208, 246}, [3]uint8{128, 255, 255}},
		{"0 0.5 0.5 0", [3]uint8{245, 152, 125}, [3]uint8{255, 128, 128}},
		{"1 1 0 0", [3]uint8{55, 53, 147}, [3]uint8{0, 0, 255}},
		{"0 0 0 0.5", [3]uint8{151, 153, 155}, [3]uint8{128, 128, 128}},
		{"0 0 0 0", [3]uint8{255, 255, 255}, [3]uint8{255, 255, 255}},
	}
	var c strings.Builder
	for i, p := range patches {
		fmt.Fprintf(&c, "%s k %d 0 20 100 re f\n", p.cmyk, 20*i)
	}
	data := buildPDF([]string{c.String()}, "")
	for _, naive := range []bool{false, true} {
		doc, err := OpenWith(data, OpenOptions{NaiveCMYK: naive})
		if err != nil {
			t.Fatal(err)
		}
		img := renderDoc(t, doc)
		for i, p := range patches {
			want := p.swop
			if naive {
				want = p.naive
			}
			assertNear(t, img, 20*i+10, 50, rgba(want[0], want[1], want[2], 255), 2)
		}
	}
}

// renderDoc renders the first page of doc on white at 72 dpi.
func renderDoc(t *testing.T, doc *Document) *image.RGBA {
	t.Helper()
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(p.Bounds(1))
	if err := p.Render(context.Background(), img, RenderOptions{Background: white}); err != nil {
		t.Fatal(err)
	}
	return img
}

// BenchmarkDecodeCMYK decodes and draws a 1200 × 800 DeviceCMYK photo (a
// smooth one and one of noise) through the press profile and naively.
func BenchmarkDecodeCMYK(b *testing.B) {
	for _, kind := range []string{"smooth", "noise"} {
		px := make([]byte, 4*1200*800)
		for i := range 1200 * 800 {
			x, y := i%1200, i/1200
			c := px[4*i:][:4]
			if kind == "smooth" {
				c[0], c[1], c[2], c[3] = uint8(x/5), uint8(y*255/800), uint8((x+y)/8), uint8(x*y/4000)
			} else {
				c[0], c[1], c[2], c[3] = uint8(i*13), uint8(i*7+y), uint8(i*29), uint8(i*3+x)
			}
		}
		im := streamObj("/Subtype /Image /Width 1200 /Height 800 /ColorSpace /DeviceCMYK /BitsPerComponent 8", px)
		data := buildPDF([]string{"q 200 0 0 100 0 0 cm /Im0 Do Q"}, "/Resources << /XObject << /Im0 100 0 R >> >>", im)
		for _, naive := range []bool{false, true} {
			name := kind + "/swop"
			if naive {
				name = kind + "/naive"
			}
			b.Run(name, func(b *testing.B) {
				dst := image.NewRGBA(image.Rect(0, 0, 200, 100))
				for b.Loop() {
					doc, _ := OpenWith(data, OpenOptions{NaiveCMYK: naive})
					p, _ := doc.Page(0)
					if err := p.Render(context.Background(), dst, RenderOptions{Workers: 1}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
