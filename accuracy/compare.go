package main

import (
	"image"
	"image/color"
)

// overLevels is the difference in any channel, in 1/255 steps, past which
// a pixel counts as different (ADR 0010).
const overLevels = 16

// diff is the comparison of a page rendered by cera and by the reference.
type diff struct {
	sizeA, sizeB image.Point // only the common area is compared
	over         float64     // share of pixels differing by more than overLevels in any channel
	p99          int         // 99th percentile of the largest channel difference
	max          int
}

// compare compares a and b, both opaque, pixel by pixel over their common
// area; a pixel's difference is the largest of its channels. A size
// mismatch counts the area only one covers as different.
func compare(a, b *image.RGBA) diff {
	d := diff{sizeA: a.Rect.Size(), sizeB: b.Rect.Size()}
	w, h := min(d.sizeA.X, d.sizeB.X), min(d.sizeA.Y, d.sizeB.Y)
	var hist [256]int
	for y := range h {
		ra, rb := a.Pix[y*a.Stride:], b.Pix[y*b.Stride:]
		for x := range w {
			m := 0
			for c := range 3 {
				m = max(m, absDiff(ra[4*x+c], rb[4*x+c]))
			}
			hist[m]++
		}
	}
	total := max(d.sizeA.X*d.sizeA.Y, d.sizeB.X*d.sizeB.Y)
	hist[255] += total - w*h // what only one rendering covers
	if total == 0 {
		return d
	}
	var over, seen int
	d.p99 = -1
	for v, n := range hist {
		if v > overLevels {
			over += n
		}
		if n > 0 {
			d.max = v
		}
		if seen += n; d.p99 < 0 && seen*100 >= total*99 {
			d.p99 = v
		}
	}
	d.over = float64(over) / float64(total)
	return d
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// diffImage shows the reference as faint grey, red where cera is darker and
// blue where it is lighter, saturating at 64 levels.
func diffImage(a, ref *image.RGBA) *image.RGBA {
	w, h := min(a.Rect.Dx(), ref.Rect.Dx()), min(a.Rect.Dy(), ref.Rect.Dy())
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			pa, pr := a.RGBAAt(x, y), ref.RGBAAt(x, y)
			la, lr := luma(pa), luma(pr)
			g := uint8(255 - (255-lr)/5)
			c := color.RGBA{g, g, g, 255}
			dc := 0
			for _, v := range [3]int{absDiff(pa.R, pr.R), absDiff(pa.G, pr.G), absDiff(pa.B, pr.B)} {
				dc = max(dc, v)
			}
			if dc > overLevels/2 {
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

func luma(c color.RGBA) int {
	return (299*int(c.R) + 587*int(c.G) + 114*int(c.B)) / 1000
}
