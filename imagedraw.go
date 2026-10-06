package cera

import (
	"image"
	"image/color"
	"math"

	"github.com/timzifer/stilus"
)

// A raster device draws an image by filling the parallelogram the unit
// square maps to with a stilus.ImageShader, which maps every device pixel
// back into the image: so an image goes through the clip stack and gets
// antialiased edges like any fill, and a band or tile samples only its own
// pixels.

// ImageFilter sets how a raster device samples a magnified image that does
// not ask for /Interpolate.
type ImageFilter uint8

const (
	// ImageNearest samples the nearest pixel, the literal reading of the
	// spec (and what Ghostscript does): pixel art, QR codes and scans stay
	// crisp.
	ImageNearest ImageFilter = iota
	// ImageSmooth samples bilinearly while the image is magnified less
	// than smoothLimit times, as PDFium, MuPDF and Poppler all do; images
	// magnified further stay crisp, as in all three.
	ImageSmooth
)

// smoothLimit is the magnification from which ImageSmooth samples the
// nearest pixel. PDFium, MuPDF and Poppler smooth below 2×; from 2× MuPDF
// stops (PDFium at 3×, Poppler at 4×, Ghostscript never smooths).
const smoothLimit = 2

// smooth reports whether img is drawn smoothed when drawn through m (the
// unit square to the device) under f.
func (f ImageFilter) smooth(img *Image, m Matrix) bool {
	if img.Interpolate {
		return true
	}
	if f != ImageSmooth {
		return false
	}
	// Device pixels per sample along the image's rows and columns.
	sx := math.Hypot(m[0], m[1]) / float64(img.W)
	sy := math.Hypot(m[2], m[3]) / float64(img.H)
	return sx < smoothLimit && sy < smoothLimit
}

// imageDraw is the per-device state of DrawImage, reused between images.
type imageDraw struct {
	shader stilus.ImageShader
	bits   bitShader
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
	if d.C.Clip().Empty() || paint.Color.A == 0 {
		return
	}
	id := &d.img
	// The image and its mask take the same filter, so that their edges
	// line up.
	smooth := d.ImageFilter.smooth(img, m)
	var sh stilus.Shader
	if id.setupBits(img, m, smooth, paint.Color, d.C.Clip()) {
		sh = &id.bits
		defer id.bits.release() // keep no image alive
	} else {
		s := &id.shader
		s.Reset()
		defer s.Reset() // keep no image alive
		if img.Stencil || img.color == nil {
			s.SetColor(stilus.PackRGBA(paint.Color))
		} else if !s.SetImage(img.color, toUnit(img.W, img.H).Mul(m), smooth, paint.Color.A) {
			return
		}
		if t := img.mask; t != nil {
			if !s.SetMask(t, toUnit(t.Base().W, t.Base().H).Mul(m), smooth) {
				return
			}
		}
		sh = s
	}
	if id.path.Empty() {
		id.path.MoveTo(0, 0)
		id.path.LineTo(1, 0)
		id.path.LineTo(1, 1)
		id.path.LineTo(0, 1)
		id.path.Close()
	}
	id.paint.Shader = sh
	d.C.Fill(&id.path, m, NonZero, &id.paint)
	id.paint.Shader = nil
}

// setupBits sets up the bitShader for img, if it is a one-bit image
// without a mask or a stencil of one bit, drawn as bitShader draws: in its
// colours times the constant alpha c.A, or the colour c through the
// stencil, as stilus.ImageShader would.
func (id *imageDraw) setupBits(img *Image, m Matrix, smooth bool, c color.RGBA, clip image.Rectangle) bool {
	var p *plane
	var pal [2]uint32
	switch {
	case img.color != nil && img.mask == nil && !img.Stencil:
		p = img.color.Base()
		if p.Kind != stilus.PlaneBits {
			return false
		}
		for i := range pal {
			pal[i] = p.Pal[i]
			if c.A != 255 {
				pal[i] = scale255(pal[i], uint32(c.A))
			}
		}
	case img.color == nil && img.mask != nil:
		p = img.mask.Base()
		if p.Kind != stilus.PlaneBits {
			return false
		}
		// Mask colours are levels of alpha: all four channels are equal.
		col := stilus.PackRGBA(c)
		for i := range pal {
			pal[i] = scale255(col, uint32(uint8(p.Pal[i])))
		}
	default:
		return false
	}
	return id.bits.setup(p, toUnit(p.W, p.H).Mul(m), smooth, pal, clip)
}
