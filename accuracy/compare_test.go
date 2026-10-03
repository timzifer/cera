package main

import (
	"image"
	"image/color"
	"testing"
)

func fill(w, h int, c color.RGBA) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return m
}

func TestCompare(t *testing.T) {
	white := color.RGBA{255, 255, 255, 255}
	a, b := fill(10, 10, white), fill(10, 10, white)
	if d := compare(a, b); d.over != 0 || d.p99 != 0 || d.max != 0 {
		t.Errorf("equal pages: %+v", d)
	}
	// One pixel off by 17 in blue, one by 16: only the first counts.
	b.SetRGBA(0, 0, color.RGBA{255, 255, 238, 255})
	b.SetRGBA(1, 0, color.RGBA{239, 255, 255, 255})
	d := compare(a, b)
	if d.over != 0.01 || d.max != 17 || d.p99 != 16 {
		t.Errorf("got %+v, want 1%% over, max 17, p99 16", d)
	}
	// Two rows of 100 differing by 200: p99 is 200.
	for x := range 10 {
		b.SetRGBA(x, 5, color.RGBA{55, 255, 255, 255})
	}
	if d := compare(a, b); d.p99 != 200 {
		t.Errorf("p99 %d, want 200", d.p99)
	}
	// A rendering one row short: the missing row differs.
	if d := compare(fill(10, 9, white), fill(10, 10, white)); d.over != 0.1 {
		t.Errorf("size mismatch: %+v", d)
	}
}

func TestThreshold(t *testing.T) {
	for _, c := range []struct{ v, want float64 }{{0, 0.05}, {1, 1.15}, {12.34, 13.63}} {
		if got := pinned(c.v); got != c.want {
			t.Errorf("pinned(%v) = %v, want %v", c.v, got, c.want)
		}
	}
}
