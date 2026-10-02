package cera

import (
	"github.com/timzifer/stilus"
)

// A raster device draws an image by filling the parallelogram the unit
// square maps to with a stilus.ImageShader, which maps every device pixel
// back into the image: so an image goes through the clip stack and gets
// antialiased edges like any fill, and a band or tile samples only its own
// pixels.

// imageDraw is the per-device state of DrawImage, reused between images.
type imageDraw struct {
	shader stilus.ImageShader
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
	d.t.inked(paint.Color.A)
	if !d.t.knockout {
		d.drawImage(img, m, paint)
		return
	}
	box := d.koBegin(deviceBoxPoints(unitSquare[:], m, 1))
	d.drawImage(img, m, paint)
	d.koShape()
	d.drawImage(img, m, &opaque)
	d.koEnd(box)
}

func (d *RasterDevice) drawImage(img *Image, m Matrix, paint *Paint) {
	if d.C.Clip().Empty() {
		return
	}
	id := &d.img
	s := &id.shader
	s.Reset()
	defer s.Reset() // keep no image alive
	if img.Stencil || img.color == nil {
		if paint.Color.A == 0 {
			return
		}
		s.SetColor(stilus.PackRGBA(paint.Color))
	} else {
		if paint.Color.A == 0 || !s.SetImage(img.color, toUnit(img.W, img.H).Mul(m), img.Interpolate, paint.Color.A) {
			return
		}
	}
	if t := img.mask; t != nil {
		if !s.SetMask(t, toUnit(t.Base().W, t.Base().H).Mul(m), img.Interpolate) {
			return
		}
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
