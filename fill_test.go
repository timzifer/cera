package cera

import (
	"image"
	"image/color"
	"slices"
	"testing"
)

// fillRegionStream fills exactly what fillRegion fills, at any alignment
// of the rows and for rows shorter than its 64-byte blocks.
func TestFillRegionStream(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 131, 40))
	for i := range base.Pix {
		base.Pix[i] = uint8(i * 7)
	}
	for _, c := range []color.RGBA{{255, 255, 255, 255}, {1, 2, 3, 4}, {}} {
		for _, sub := range []image.Rectangle{base.Rect, image.Rect(1, 1, 131, 40), image.Rect(3, 2, 70, 39)} {
			for _, r := range []image.Rectangle{
				image.Rect(0, 0, 131, 40),
				image.Rect(1, 3, 130, 39),
				image.Rect(5, 5, 9, 30),    // rows of 16 bytes
				image.Rect(2, 0, 21, 1),    // one row of 76 bytes
				image.Rect(7, 4, 120, 4),   // empty
				image.Rect(-5, -5, 200, 9), // beyond dst
			} {
				want := image.NewRGBA(base.Rect)
				copy(want.Pix, base.Pix)
				got := image.NewRGBA(base.Rect)
				copy(got.Pix, base.Pix)
				fillRegion(want.SubImage(sub).(*image.RGBA), r.Intersect(sub), c)
				fillRegionStream(got.SubImage(sub).(*image.RGBA), r, c)
				if !slices.Equal(want.Pix, got.Pix) {
					t.Errorf("colour %v, image %v, region %v: differs", c, sub, r)
				}
			}
		}
	}
}

func BenchmarkFillRegion(b *testing.B) {
	dst := image.NewRGBA(image.Rect(0, 0, 1241, 1754)) // A4 at 150 dpi
	white := color.RGBA{255, 255, 255, 255}
	b.SetBytes(int64(len(dst.Pix)))
	b.Run("copy", func(b *testing.B) {
		for b.Loop() {
			fillRegion(dst, dst.Rect, white)
		}
	})
	b.Run("stream", func(b *testing.B) {
		for b.Loop() {
			fillRegionStream(dst, dst.Rect, white)
		}
	})
}

func BenchmarkFillRegionRows(b *testing.B) {
	dst := image.NewRGBA(image.Rect(0, 0, 1300, 1754))
	r := image.Rect(0, 0, 1241, 1754) // not the whole stride: row by row
	white := color.RGBA{255, 255, 255, 255}
	b.SetBytes(int64(4 * r.Dx() * r.Dy()))
	b.Run("copy", func(b *testing.B) {
		for b.Loop() {
			fillRegion(dst, r, white)
		}
	})
	b.Run("stream", func(b *testing.B) {
		for b.Loop() {
			fillRegionStream(dst, r, white)
		}
	})
}
