package cera

import (
	"image"
	"math"

	"github.com/timzifer/stilus"
)

// A scan (CCITT, JBIG2) or a stencil is a one-bit image, and it is mostly
// drawn magnified, by whole device pixels per sample. stilus.ImageShader
// maps every device pixel back through floats, clamps it and extracts its
// bit. bitShader picks exactly the samples it would, but finds the source
// column of each device column once per image, and expands a sample row
// into a device row once for all the device rows on it.

// bitShader paints a one-bit plane in two colours, sampled at the nearest
// pixel along the device axes: the case where stilus.Sampler reads the
// base plane bit by bit (not smoothed, and reduced by at most 1+1e-6 on
// one axis at least, so no mip level).
type bitShader struct {
	p   *plane
	pal [2]uint32 // premultiplied, constant alpha applied
	// m maps device space to the pixel space of p, as stilus.Sampler's.
	m stilus.Matrix
	// cols[i] is the source column of device column x0+i.
	x0   int
	cols []int32
	// row is source row j expanded to those device columns (j < 0: none).
	row []uint32
	j   int
}

// setup prepares s to paint p, whose pixel space toDevice maps to device
// space, in the colours pal, for the device columns of clip. It reports
// false where stilus.ImageShader samples another way, or where the image
// misses the clip's columns.
func (s *bitShader) setup(p *plane, toDevice stilus.Matrix, smooth bool, pal [2]uint32, clip image.Rectangle) bool {
	if p.Kind != stilus.PlaneBits || smooth || p.W <= 0 || p.H <= 0 {
		return false
	}
	inv, ok := toDevice.Invert()
	if !ok || inv[1] != 0 || inv[2] != 0 {
		return false
	}
	for _, v := range inv {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	// As stilus.Sampler.Setup: sampled bilinearly, or from a mip level,
	// once reduced by more than 1+1e-6 on the less reduced axis.
	if min(math.Abs(inv[0]), math.Abs(inv[3])) > 1+1e-6 {
		return false
	}
	// The device columns the image covers, within the clip.
	a := toDevice[4]
	b := toDevice[0]*float64(p.W) + a
	lo, hi := math.Floor(min(a, b)), math.Ceil(max(a, b))
	x0, x1 := max(clip.Min.X, int(max(lo, -1<<30))), min(clip.Max.X, int(min(hi, 1<<30)))
	if x1 <= x0 {
		return false
	}
	s.p, s.pal, s.m, s.x0, s.j = p, pal, inv, x0, -1
	if cap(s.cols) < x1-x0 {
		s.cols, s.row = make([]int32, x1-x0), make([]uint32, x1-x0)
	}
	s.cols, s.row = s.cols[:x1-x0], s.row[:x1-x0]
	// Sampler.Sample's u for column x: u0 + du·x, u0 = m[2]·fy + m[4] +
	// m[0]·0.5 with m[2] = 0, the same floats in the same order.
	u0, du := 0+inv[4]+inv[0]*0.5, inv[0]
	for i := range s.cols {
		s.cols[i] = int32(sampleIndex(u0+du*float64(x0+i), p.W))
	}
	return true
}

// release drops the shader's reference to its plane.
func (s *bitShader) release() { s.p = nil }

// ShadeSpan implements stilus.Shader.
func (s *bitShader) ShadeSpan(y, x int, dst []uint32) {
	p, m := s.p, &s.m
	fy := float64(y) + 0.5
	j := sampleIndex(m[3]*fy+m[5]+m[1]*0.5, p.H)
	src := p.Pix8[j*p.Stride:][:(p.W+7)/8]
	if x < s.x0 || x+len(dst) > s.x0+len(s.row) {
		// Outside the columns set up: as Sampler.Sample does.
		u0, du := m[2]*fy+m[4]+m[0]*0.5, m[0]
		for i := range dst {
			k := sampleIndex(u0+du*float64(x+i), p.W)
			dst[i] = s.pal[src[k>>3]>>(7-uint(k)&7)&1]
		}
		return
	}
	if j != s.j {
		s.expand(src)
		s.j = j
	}
	copy(dst, s.row[x-s.x0:])
}

// expand sets row to the source row src at device resolution.
func (s *bitShader) expand(src []uint8) {
	row := s.row[:len(s.cols)]
	for i, k := range s.cols {
		row[i] = s.pal[src[k>>3]>>(7-uint(k)&7)&1]
	}
}

// sampleIndex returns the pixel of [0, n) that coordinate u falls in,
// clamped to the edges (NaN gives 0), as stilus's sampler finds it.
func sampleIndex(u float64, n int) int {
	if !(u > 0) {
		return 0
	}
	if u >= float64(n) {
		return n - 1
	}
	return int(u)
}
