package cera

import (
	"image"

	"github.com/timzifer/stilus"
)

// A raster device paints a shading over the current clip, within the
// shading's BBox: axial and radial shadings and the background as fills
// of the clip's rectangle with a shader (over a ramp with knots at
// stitching bounds), a function-based shading as a texture over its
// domain, and a mesh as one fill of its box with a stilus MeshShader that
// every worker shares, without a layer.

// shadeDraw is the per-device state of FillShading, reused between
// shadings.
type shadeDraw struct {
	lin   stilus.LinearGradient
	rad   stilus.RadialGradient
	img   stilus.ImageShader
	paint Paint
	path  Path
}

// FillShading paints sh over the clip with the alpha of paint.
func (d *RasterDevice) FillShading(sh *Shading, m Matrix, paint *Paint) {
	d.t.inked(paint.Color.A)
	if !d.t.knockout {
		d.fillShading(sh, m, paint.Color.A)
		return
	}
	box := d.koBegin(d.C.Clip())
	d.fillShading(sh, m, paint.Color.A)
	d.koShape()
	d.fillShading(sh, m, 255)
	d.koEnd(box)
}

func (d *RasterDevice) fillShading(sh *Shading, m Matrix, alpha uint8) {
	if alpha == 0 || d.C.Clip().Empty() {
		return
	}
	if sh.HasBBox {
		d.C.ClipRect(stilus.Rect{X0: sh.BBox.X0, Y0: sh.BBox.Y0, X1: sh.BBox.X1, Y1: sh.BBox.Y1}, m)
		defer d.C.PopClip()
	}
	clip := d.C.Clip()
	if clip.Empty() {
		return
	}
	s := &d.shade
	if sh.Background.A != 0 {
		s.paint = Paint{Color: scaleColor(sh.Background, alpha)}
		d.C.Fill(d.t.rectOf(clip), stilus.Identity, NonZero, &s.paint)
	}
	switch sh.Type {
	case 1:
		// Texture pixels to the domain (its first row at the bottom),
		// the domain to shading space, shading space to device space.
		n := float64(sh.Texture.Base().W)
		dom := sh.Domain
		toDomain := Matrix{dom.Dx() / n, 0, 0, dom.Dy() / n, dom.X0, dom.Y0}
		toShading := sh.Matrix.Mul(m)
		s.img.Reset()
		defer s.img.Reset()
		if !s.img.SetImage(sh.Texture, toDomain.Mul(toShading), true, alpha) {
			return
		}
		s.path.Reset()
		s.path.Rect(float32(dom.X0), float32(dom.Y0), float32(dom.Dx()), float32(dom.Dy()))
		s.paint = Paint{Shader: &s.img}
		d.C.Fill(&s.path, toShading, NonZero, &s.paint)
	case 2:
		g := &s.lin
		g.Ramp, g.Knots, g.Extend, g.Alpha = sh.Ramp, sh.Knots, sh.Extend, alpha
		defer func() { g.Ramp, g.Knots = nil, nil }()
		c := &sh.Coords
		if g.Set(c[0], c[1], c[2], c[3], m) {
			s.paint = Paint{Shader: g}
			d.C.Fill(d.t.rectOf(clip), stilus.Identity, NonZero, &s.paint)
		}
	case 3:
		g := &s.rad
		g.Ramp, g.Knots, g.Extend, g.Alpha = sh.Ramp, sh.Knots, sh.Extend, alpha
		defer func() { g.Ramp, g.Knots = nil, nil }()
		c := &sh.Coords
		if g.Set(c[0], c[1], c[2], c[3], c[4], c[5], m) {
			s.paint = Paint{Shader: g}
			d.C.Fill(d.t.rectOf(clip), stilus.Identity, NonZero, &s.paint)
		}
	default:
		d.fillMesh(sh, m, alpha, clip)
	}
	s.paint = Paint{}
}

// fillMesh fills the box of a mesh within clip with its shader.
func (d *RasterDevice) fillMesh(sh *Shading, m Matrix, alpha uint8, clip image.Rectangle) {
	b := sh.Bounds
	corners := [2]stilus.Point{{X: float32(b.X0), Y: float32(b.Y0)}, {X: float32(b.X1), Y: float32(b.Y1)}}
	if deviceBoxPoints(corners[:], m, 1).Intersect(clip).Empty() {
		return
	}
	ms := sh.meshShader(m, alpha)
	if ms == nil {
		return
	}
	s := &d.shade
	s.path.Reset()
	s.path.Rect(float32(b.X0), float32(b.Y0), float32(b.Dx()), float32(b.Dy()))
	s.paint = Paint{Shader: ms}
	d.C.Fill(&s.path, m, NonZero, &s.paint)
}

// shadingBox returns the device pixels sh can paint under m, before
// clipping: its BBox, the domain of a function-based shading or the
// vertices of a mesh, and everything for the others.
func shadingBox(sh *Shading, m Matrix) image.Rectangle {
	all := image.Rect(-1<<30, -1<<30, 1<<30, 1<<30)
	box := func(r Rect, m Matrix) image.Rectangle {
		corners := [2]stilus.Point{{X: float32(r.X0), Y: float32(r.Y0)}, {X: float32(r.X1), Y: float32(r.Y1)}}
		return deviceBoxPoints(corners[:], m, 1)
	}
	if sh.HasBBox {
		all = box(sh.BBox, m)
	}
	if sh.Background.A != 0 {
		return all
	}
	switch sh.Type {
	case 1:
		return all.Intersect(box(sh.Domain, sh.Matrix.Mul(m)))
	case 2, 3:
		return all
	}
	return all.Intersect(box(sh.Bounds, m))
}
