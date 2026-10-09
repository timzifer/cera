package cera

import "math"

// Quad is a quadrilateral, such as the box of a glyph on a rotated or
// skewed baseline. P[0] to P[1] runs along the text, P[0] to P[2] across
// it towards the next line, and P[3] is the corner opposite P[0]: for
// upright horizontal text, upper left, upper right, lower left and lower
// right, the order of the QuadPoints of a text markup annotation.
type Quad struct {
	P [4][2]float64
}

// QuadOf returns the quad of r: upper left, upper right, lower left,
// lower right, with y down.
func QuadOf(r Rect) Quad {
	return Quad{[4][2]float64{{r.X0, r.Y0}, {r.X1, r.Y0}, {r.X0, r.Y1}, {r.X1, r.Y1}}}
}

// Bounds returns the smallest rectangle around q.
func (q Quad) Bounds() Rect {
	r := Rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, p := range q.P {
		r.X0, r.X1 = min(r.X0, p[0]), max(r.X1, p[0])
		r.Y0, r.Y1 = min(r.Y0, p[1]), max(r.Y1, p[1])
	}
	return r
}

// Contains reports whether (x, y) is inside q or on its edges. q is taken
// to be convex, as every quad an affine map makes of a rectangle is. A
// quad without area, such as the box of a glyph that does not advance,
// contains nothing.
func (q Quad) Contains(x, y float64) bool {
	// The edges in order around q: P0, P1, P3, P2.
	ring := [4][2]float64{q.P[0], q.P[1], q.P[3], q.P[2]}
	area := 0.0
	for i, a := range ring {
		b := ring[(i+1)%4]
		area += a[0]*b[1] - b[0]*a[1]
	}
	if area == 0 {
		return false
	}
	var pos, neg bool
	for i, a := range ring {
		b := ring[(i+1)%4]
		c := (b[0]-a[0])*(y-a[1]) - (b[1]-a[1])*(x-a[0])
		pos = pos || c > 0
		neg = neg || c < 0
	}
	return !pos || !neg
}

// Transform returns q mapped by m.
func (q Quad) Transform(m Matrix) Quad {
	for i, p := range q.P {
		q.P[i][0], q.P[i][1] = m.Apply(p[0], p[1])
	}
	return q
}
