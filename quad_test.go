package cera

import (
	"math"
	"testing"

	"github.com/timzifer/stilus"
)

func TestQuad(t *testing.T) {
	q := QuadOf(Rect{10, 20, 30, 25})
	if b := q.Bounds(); b != (Rect{10, 20, 30, 25}) {
		t.Errorf("bounds %v", b)
	}
	for _, c := range []struct {
		x, y float64
		in   bool
	}{{10, 20, true}, {20, 22, true}, {30, 25, true}, {9.9, 22, false}, {20, 25.1, false}} {
		if got := q.Contains(c.x, c.y); got != c.in {
			t.Errorf("Contains(%g, %g) = %v", c.x, c.y, got)
		}
	}

	// Turned by 45° about the origin: a diamond whose bounds are larger.
	r := QuadOf(Rect{0, 0, 10, 10}).Transform(stilus.Rotate(math.Pi / 4))
	b := r.Bounds()
	if !approx(b.Dx(), 10*math.Sqrt2) || !approx(b.Dy(), 10*math.Sqrt2) {
		t.Errorf("turned bounds %v", b)
	}
	if !r.Contains(0, 7) || r.Contains(b.X0+0.5, b.Y0+0.5) {
		t.Errorf("turned quad %v: centre or corner of its bounds wrong", r)
	}
	// Mirrored, the corners run the other way round.
	if m := QuadOf(Rect{0, 0, 10, 10}).Transform(Matrix{-1, 0, 0, 1, 0, 0}); !m.Contains(-5, 5) {
		t.Errorf("mirrored quad %v", m)
	}

	if (Quad{[4][2]float64{{1, 1}, {1, 1}, {1, 3}, {1, 3}}}).Contains(1, 2) {
		t.Error("a quad without area contains a point")
	}
}
