package cera

import (
	"math"

	"github.com/timzifer/stilus"
)

// A raster device draws an image by filling the parallelogram the unit
// square maps to with a shader that maps every device pixel back into the
// image: so an image goes through the clip stack and gets antialiased
// edges like any fill, and a band or tile samples only its own pixels.

// sampler reads a texture at device pixels.
type sampler struct {
	p *plane
	// m maps device space to the pixel space of p (x right, y down,
	// pixel (i, j) covering [i, i+1) × [j, j+1)).
	m        Matrix
	bilinear bool
}

// setup chooses the mip level of t for drawing it with toDevice, which
// maps its base pixel space to device space. Magnified images are sampled
// at the nearest pixel unless smooth is set; reduced ones bilinearly from
// the level at most twice as fine as the device.
func (s *sampler) setup(t *texture, toDevice Matrix, smooth bool) bool {
	inv, ok := toDevice.Invert()
	if !ok || !finite(inv) {
		return false
	}
	// Base pixels per device pixel along the device axes.
	r := min(math.Hypot(inv[0], inv[1]), math.Hypot(inv[2], inv[3]))
	k, top := 0, t.levels()
	for r >= 2 && k < top {
		r /= 2
		k++
	}
	s.p = t.level(k)
	if k > 0 {
		inv = inv.Mul(stilus.Scale(float64(s.p.w)/float64(t.base.w), float64(s.p.h)/float64(t.base.h)))
	}
	s.m = inv
	s.bilinear = smooth || r > 1+1e-6
	return true
}

func finite(m Matrix) bool {
	for _, v := range m {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// sample writes the colours of pixels [x, x+len(dst)) of row y. Every
// pixel's coordinates are computed from its own position rather than
// accumulated along the span, so that a pixel samples the same texel
// whichever band, tile or span it is drawn in.
func (s *sampler) sample(y, x int, dst []uint32) {
	p, m := s.p, &s.m
	fy := float64(y) + 0.5
	u0 := m[2]*fy + m[4] + m[0]*0.5
	v0 := m[3]*fy + m[5] + m[1]*0.5
	du, dv := m[0], m[1]
	if !s.bilinear {
		if dv == 0 {
			// Axis-aligned: the row is the same for the whole span.
			j := clampIndex(v0, p.h)
			switch p.kind {
			case planeRGBA:
				row := p.pix32[j*p.stride:][:p.w]
				for i := range dst {
					dst[i] = row[clampIndex(u0+du*float64(x+i), p.w)]
				}
			case planeIndex:
				row, pal := p.pix8[j*p.stride:][:p.w], p.pal
				for i := range dst {
					dst[i] = pal[row[clampIndex(u0+du*float64(x+i), p.w)]]
				}
			default:
				row, pal := p.pix8[j*p.stride:][:p.stride], p.pal
				for i := range dst {
					k := clampIndex(u0+du*float64(x+i), p.w)
					dst[i] = pal[row[k>>3]>>(7-uint(k)&7)&1]
				}
			}
			return
		}
		for i := range dst {
			fx := float64(x + i)
			dst[i] = p.at(clampIndex(u0+du*fx, p.w), clampIndex(v0+dv*fx, p.h))
		}
		return
	}
	// Bilinear: weights from the distance to the four nearest centres.
	u0, v0 = u0-0.5, v0-0.5
	if dv == 0 {
		// Axis-aligned: two rows for the whole span, mixed once per
		// column pair, which neighbouring pixels share when the image is
		// magnified.
		y0, y1, ty := split(v0, p.h)
		last := -1
		var c0, c1 uint32
		switch p.kind {
		case planeRGBA:
			r0, r1 := p.pix32[y0*p.stride:][:p.w], p.pix32[y1*p.stride:][:p.w]
			for i := range dst {
				x0, x1, tx := split(u0+du*float64(x+i), p.w)
				if x0 != last {
					c0, c1, last = lerp(r0[x0], r1[x0], ty), lerp(r0[x1], r1[x1], ty), x0
				}
				dst[i] = lerp(c0, c1, tx)
			}
		case planeIndex:
			r0, r1, pal := p.pix8[y0*p.stride:][:p.w], p.pix8[y1*p.stride:][:p.w], p.pal
			for i := range dst {
				x0, x1, tx := split(u0+du*float64(x+i), p.w)
				if x0 != last {
					c0, c1, last = lerp(pal[r0[x0]], pal[r1[x0]], ty), lerp(pal[r0[x1]], pal[r1[x1]], ty), x0
				}
				dst[i] = lerp(c0, c1, tx)
			}
		default:
			r0, r1, pal := p.pix8[y0*p.stride:][:p.stride], p.pix8[y1*p.stride:][:p.stride], p.pal
			for i := range dst {
				x0, x1, tx := split(u0+du*float64(x+i), p.w)
				if x0 != last {
					s0, s1 := 7-uint(x0)&7, 7-uint(x1)&7
					c0 = lerp(pal[r0[x0>>3]>>s0&1], pal[r1[x0>>3]>>s0&1], ty)
					c1 = lerp(pal[r0[x1>>3]>>s1&1], pal[r1[x1>>3]>>s1&1], ty)
					last = x0
				}
				dst[i] = lerp(c0, c1, tx)
			}
		}
		return
	}
	for i := range dst {
		fx := float64(x + i)
		x0, x1, tx := split(u0+du*fx, p.w)
		y0, y1, ty := split(v0+dv*fx, p.h)
		a := lerp(p.at(x0, y0), p.at(x1, y0), tx)
		b := lerp(p.at(x0, y1), p.at(x1, y1), tx)
		dst[i] = lerp(a, b, ty)
	}
}

// clampIndex returns the pixel of [0, n) that coordinate u falls in,
// clamped to the edges (NaN gives 0).
func clampIndex(u float64, n int) int {
	if !(u > 0) {
		return 0
	}
	if u >= float64(n) {
		return n - 1
	}
	return int(u)
}

// split returns the pixels on either side of u, clamped to [0, n), and
// the weight of the second in 1/256.
func split(u float64, n int) (i0, i1 int, t uint32) {
	if !(u > 0) {
		return 0, 0, 0
	}
	if u >= float64(n-1) {
		return n - 1, n - 1, 0
	}
	i := int(u)
	return i, i + 1, uint32((u - float64(i)) * 256)
}

// lerp mixes two premultiplied pixels channel-wise: a·(256-t)/256 +
// b·t/256, t in [0, 256].
func lerp(a, b, t uint32) uint32 {
	if a == b {
		return a
	}
	s := 256 - t
	rb := ((a&0x00ff00ff)*s + (b&0x00ff00ff)*t) >> 8 & 0x00ff00ff
	ag := ((a>>8&0x00ff00ff)*s + (b>>8&0x00ff00ff)*t) & 0xff00ff00
	return rb | ag
}

// imageShader paints an image: its colours (or a solid stencil colour),
// times the constant alpha, times its mask.
type imageShader struct {
	col, mask sampler
	hasCol    bool
	hasMask   bool
	color     uint32 // premultiplied, when !hasCol
	alpha     uint32 // constant alpha of a coloured image, 0-255
	buf       []uint32
}

func (s *imageShader) ShadeSpan(y, x int, dst []uint32) {
	if s.hasCol {
		s.col.sample(y, x, dst)
		if s.alpha != 255 {
			for i, c := range dst {
				dst[i] = scale255(c, s.alpha)
			}
		}
	} else {
		for i := range dst {
			dst[i] = s.color
		}
	}
	if !s.hasMask {
		return
	}
	if cap(s.buf) < len(dst) {
		s.buf = make([]uint32, len(dst)+len(dst)/2+64)
	}
	m := s.buf[:len(dst)]
	s.mask.sample(y, x, m)
	for i, c := range m {
		// Mask colours are levels of alpha: all four channels are equal.
		switch a := c & 0xff; a {
		case 0:
			dst[i] = 0
		case 255:
		default:
			dst[i] = scale255(dst[i], a)
		}
	}
}

// imageDraw is the per-device state of DrawImage, reused between images.
type imageDraw struct {
	shader imageShader
	paint  Paint
	path   Path
}

// unitSquare is the box an image occupies in its user space.
var unitSquare = [2]stilus.Point{{X: 0, Y: 0}, {X: 1, Y: 1}}

// toUnit maps the pixel space of a w × h plane to the unit square.
func toUnit(w, h int) Matrix {
	return Matrix{1 / float64(w), 0, 0, -1 / float64(h), 0, 1}
}

// DrawImage paints img, whose unit square m maps to device space: a stencil
// in paint, any other image in its own colours with the constant alpha
// paint.Color.A.
func (d *RasterDevice) DrawImage(img *Image, m Matrix, paint *Paint) {
	if d.C.Clip().Empty() {
		return
	}
	id := &d.img
	s := &id.shader
	*s = imageShader{buf: s.buf}
	defer func() { s.col.p, s.mask.p = nil, nil }() // keep no image alive
	if img.Stencil || img.color == nil {
		if paint.Color.A == 0 {
			return
		}
		s.color = stilus.PackRGBA(paint.Color)
	} else {
		if s.alpha = uint32(paint.Color.A); s.alpha == 0 {
			return
		}
		if !s.col.setup(img.color, toUnit(img.W, img.H).Mul(m), img.Interpolate) {
			return
		}
		s.hasCol = true
	}
	if t := img.mask; t != nil {
		if !s.mask.setup(t, toUnit(t.base.w, t.base.h).Mul(m), img.Interpolate) {
			return
		}
		s.hasMask = true
	}
	if id.path.Empty() {
		id.path.MoveTo(0, 0)
		id.path.LineTo(1, 0)
		id.path.LineTo(1, 1)
		id.path.LineTo(0, 1)
		id.path.Close()
	}
	id.paint.Shader = s
	d.C.Fill(&id.path, m, NonZero, &id.paint)
}
