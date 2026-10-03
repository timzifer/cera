// Package metric compares renderings of a page (ADR 0010).
package metric

import (
	"image"
	"image/color"
)

// OverLevels is the difference in any channel, in 1/255 steps, past which
// a pixel counts as different (ADR 0010).
const OverLevels = 16

// Diff is the comparison of two renderings of a page.
type Diff struct {
	SizeA, SizeB image.Point // only the common area is compared
	Over         float64     // share of pixels differing by more than OverLevels in any channel
	P99          int         // 99th percentile of the largest channel difference
	Max          int
}

// Compare compares a and b, both opaque, pixel by pixel over their common
// area; a pixel's difference is the largest of its channels. A size
// mismatch counts the area only one covers as different.
func Compare(a, b *image.RGBA) Diff {
	d := Diff{SizeA: a.Rect.Size(), SizeB: b.Rect.Size()}
	w, h := min(d.SizeA.X, d.SizeB.X), min(d.SizeA.Y, d.SizeB.Y)
	var hist [256]int
	for y := range h {
		ra, rb := a.Pix[y*a.Stride:], b.Pix[y*b.Stride:]
		for x := range w {
			m := 0
			for c := range 3 {
				m = max(m, AbsDiff(ra[4*x+c], rb[4*x+c]))
			}
			hist[m]++
		}
	}
	total := max(d.SizeA.X*d.SizeA.Y, d.SizeB.X*d.SizeB.Y)
	hist[255] += total - w*h // what only one rendering covers
	if total == 0 {
		return d
	}
	var over, seen int
	d.P99 = -1
	for v, n := range hist {
		if v > OverLevels {
			over += n
		}
		if n > 0 {
			d.Max = v
		}
		if seen += n; d.P99 < 0 && seen*100 >= total*99 {
			d.P99 = v
		}
	}
	d.Over = float64(over) / float64(total)
	return d
}

// AbsDiff is |a-b|.
func AbsDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// DiffImage shows the reference as faint grey, red where cera is darker and
// blue where it is lighter, saturating at 64 levels.
func DiffImage(a, ref *image.RGBA) *image.RGBA {
	w, h := min(a.Rect.Dx(), ref.Rect.Dx()), min(a.Rect.Dy(), ref.Rect.Dy())
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			pa, pr := a.RGBAAt(x, y), ref.RGBAAt(x, y)
			la, lr := Luma(pa), Luma(pr)
			g := uint8(255 - (255-lr)/5)
			c := color.RGBA{g, g, g, 255}
			dc := 0
			for _, v := range [3]int{AbsDiff(pa.R, pr.R), AbsDiff(pa.G, pr.G), AbsDiff(pa.B, pr.B)} {
				dc = max(dc, v)
			}
			if dc > OverLevels/2 {
				k := min(255, dc*4)
				if la < lr {
					c = color.RGBA{255, uint8(255 - k), uint8(255 - k), 255}
				} else {
					c = color.RGBA{uint8(255 - k), uint8(255 - k), 255, 255}
				}
			}
			out.SetRGBA(x, y, c)
		}
	}
	return out
}

// Luma is the Rec. 601 luma of c.
func Luma(c color.RGBA) int {
	return (299*int(c.R) + 587*int(c.G) + 114*int(c.B)) / 1000
}
