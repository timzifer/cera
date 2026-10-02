package cera

import "github.com/timzifer/stilus"

// A raster device paints a tile of a tiling pattern as one fill of the
// clip's box with stilus's ImageShader in wrap mode: every device pixel is
// mapped back into the tile, taken modulo its size, so rotated and skewed
// patterns repeat exactly and without seams. A stencil tile is a mask
// painted in the pattern's colour.

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

func (d *RasterDevice) fillTile(t *Tile, m Matrix, paint *Paint) {
	clip := d.C.Clip()
	if clip.Empty() || paint.Color.A == 0 {
		return
	}
	s := &d.shade
	s.img.Reset()
	defer s.img.Reset() // keep no texture alive
	if t.Stencil {
		s.img.SetColor(stilus.PackRGBA(paint.Color))
		if !s.img.SetMaskWrap(t.tex, m, false) {
			return
		}
	} else if !s.img.SetImageWrap(t.tex, m, false, paint.Color.A) {
		return
	}
	s.paint = Paint{Shader: &s.img}
	d.C.Fill(d.t.rectOf(clip), stilus.Identity, NonZero, &s.paint)
	s.paint = Paint{}
}
