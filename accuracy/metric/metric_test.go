package metric

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
	if d := Compare(a, b); d.Over != 0 || d.P99 != 0 || d.Max != 0 {
		t.Errorf("equal pages: %+v", d)
	}
	// One pixel off by 17 in blue, one by 16: only the first counts.
	b.SetRGBA(0, 0, color.RGBA{255, 255, 238, 255})
	b.SetRGBA(1, 0, color.RGBA{239, 255, 255, 255})
	d := Compare(a, b)
	if d.Over != 0.01 || d.Max != 17 || d.P99 != 16 {
		t.Errorf("got %+v, want 1%% over, max 17, p99 16", d)
	}
	// Two rows of 100 differing by 200: p99 is 200.
	for x := range 10 {
		b.SetRGBA(x, 5, color.RGBA{55, 255, 255, 255})
	}
	if d := Compare(a, b); d.P99 != 200 {
		t.Errorf("p99 %d, want 200", d.P99)
	}
	// A rendering one row short: the missing row differs.
	if d := Compare(fill(10, 9, white), fill(10, 10, white)); d.Over != 0.1 {
		t.Errorf("size mismatch: %+v", d)
	}
}
