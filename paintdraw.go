package cera

import "github.com/timzifer/stilus"

// A raster device paints shadings and tiles with stilus's shaders over the
// current clip: axial and radial shadings with LinearGradient and
// RadialGradient over a ramp with knots, function-based ones with an
// ImageShader on their sampled grid, meshes with a MeshShader shared by
// all workers, and tiles with an ImageShader that wraps around the tile.
// Each is one fill of the clip's box (or of the shading's own area), so
// it is clipped and antialiased like any fill.

// paintDraw is the per-device state of FillShading and FillTile, reused.
type paintDraw struct {
	lin   stilus.LinearGradient
	rad   stilus.RadialGradient
	img   stilus.ImageShader
	paint Paint
	path  Path
}

func (d *RasterDevice) FillShading(sh *Shading, m Matrix, alpha uint8) {
	d.t.inked(alpha)
	if !d.t.knockout {
		d.fillShading(sh, m, alpha)
		return
	}
	box := d.koBegin(d.C.Clip())
	d.fillShading(sh, m, alpha)
	d.koShape()
	d.fillShading(sh, m, 255)
	d.koEnd(box)
}

func (d *RasterDevice) FillTile(t *Tile, m Matrix, paint *Paint) {
	d.t.inked(paint.Color.A)
	if !d.t.knockout {
		d.fillTile(t, m, paint)
		return
	}
	box := d.koBegin(d.C.Clip())
	d.fillTile(t, m, paint)
	d.koShape()
	d.fillTile(t, m, &opaque)
	d.koEnd(box)
}

// fillClip fills the clip's box with s.
func (d *RasterDevice) fillClip(s stilus.Shader) {
	pd := &d.pd
	pd.paint = Paint{Shader: s}
	d.C.Fill(d.t.rectOf(d.C.Clip()), stilus.Identity, NonZero, &pd.paint)
	pd.paint.Shader = nil
}

// fillRect fills r under m with paint.
func (d *RasterDevice) fillRect(r Rect, m Matrix, paint *Paint) {
	pd := &d.pd
	pd.path.Reset()
	pd.path.Rect(float32(r.X0), float32(r.Y0), float32(r.Dx()), float32(r.Dy()))
	d.C.Fill(&pd.path, m, NonZero, paint)
}

func (d *RasterDevice) fillShading(sh *Shading, m Matrix, alpha uint8) {
	if d.C.Clip().Empty() || alpha == 0 {
		return
	}
	pd := &d.pd
	if sh.hasBBox {
		d.C.ClipRect(stilus.Rect{X0: sh.bbox.X0, Y0: sh.bbox.Y0, X1: sh.bbox.X1, Y1: sh.bbox.Y1}, m)
		defer d.C.PopClip()
	}
	var bg uint32
	if sh.useBg && sh.hasBg {
		bg = stilus.PackRGBA(scaleColor(sh.bg, alpha))
	}
	switch sh.Type {
	case 2, 3:
		var s stilus.Shader
		g := &pd.lin.Gradient
		if sh.Type == 3 {
			g = &pd.rad.Gradient
		}
		g.Ramp, g.Knots, g.Extend, g.Alpha, g.Outside = sh.ramp, sh.knots, sh.extend, alpha, bg
		c := &sh.coords
		if sh.Type == 2 {
			if !pd.lin.Set(c[0], c[1], c[2], c[3], m) {
				return
			}
			s = &pd.lin
		} else {
			if !pd.rad.Set(c[0], c[1], c[2], c[3], c[4], c[5], m) {
				return
			}
			s = &pd.rad
		}
		d.fillClip(s)
		g.Ramp, g.Knots = nil, nil
	case 1:
		if bg != 0 {
			d.fillSolidClip(bg)
		}
		gm := sh.gridM.Mul(m)
		pd.img.Reset()
		if !pd.img.SetImage(sh.grid, gm, true, alpha) {
			return
		}
		pd.paint = Paint{Shader: &pd.img}
		d.fillRect(Rect{0, 0, float64(sh.gridW), float64(sh.gridH)}, gm, &pd.paint)
		pd.paint.Shader = nil
		pd.img.Reset()
	default:
		if bg != 0 {
			d.fillSolidClip(bg)
		}
		s := sh.shader(m, alpha)
		if s == nil {
			return
		}
		pd.paint = Paint{Shader: s}
		d.fillRect(sh.meshBox, m, &pd.paint)
		pd.paint.Shader = nil
	}
}

// fillSolidClip fills the clip's box with the premultiplied colour c.
func (d *RasterDevice) fillSolidClip(c uint32) {
	pd := &d.pd
	pd.paint = Paint{Color: stilus.UnpackRGBA(c)}
	d.C.Fill(d.t.rectOf(d.C.Clip()), stilus.Identity, NonZero, &pd.paint)
}

func (d *RasterDevice) fillTile(t *Tile, m Matrix, paint *Paint) {
	if d.C.Clip().Empty() {
		return
	}
	s := &d.pd.img
	s.Reset()
	defer s.Reset() // keep no texture alive
	if t.Stencil {
		if paint.Color.A == 0 {
			return
		}
		s.SetColor(stilus.PackRGBA(paint.Color))
		if !s.SetMaskWrap(t.tex, m, false) {
			return
		}
	} else if paint.Color.A == 0 || !s.SetImageWrap(t.tex, m, false, paint.Color.A) {
		return
	}
	d.fillClip(s)
}
