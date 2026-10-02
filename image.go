package cera

import (
	"encoding/binary"
	"image"

	"github.com/timzifer/stilus"
)

// Images are held the way their samples come: a picture of one component
// of at most eight bits (a scan, a grey or indexed image) keeps one byte a
// pixel and a palette, a one-bit picture (a fax, a JBIG2 page, a stencil
// mask) one bit a pixel and two palette entries, and only colour pictures
// take four bytes a pixel. A 600 dpi bilevel A4 scan is 4 MB this way
// instead of 140.
//
// Drawing an image much smaller than its samples reads a mip level: the
// picture averaged over blocks of 2^k × 2^k samples, made once when first
// needed and kept with the image. Sampling a level that is at most twice
// as fine as the device, bilinearly, gives a downscaled image without
// aliasing at a cost per device pixel that does not grow with the image.
// Levels of a grey or alpha picture stay one byte a pixel.

// Planes, palettes and textures are stilus's: an image is drawn by its
// ImageShader, which picks and makes the mip levels.
type (
	plane   = stilus.Plane
	palette = stilus.Palette
)

// Plane kinds.
const (
	planeRGBA  = stilus.PlaneRGBA  // Pix32, premultiplied, native layout
	planeIndex = stilus.PlaneIndex // Pix8, indexes into Pal
	planeBits  = stilus.PlaneBits  // Pix8, one bit a pixel (MSB first) indexing Pal[0] or Pal[1]
)

// pack returns r, g, b, a (premultiplied) as a native pixel.
func pack(r, g, b, a uint8) uint32 {
	c := [4]byte{r, g, b, a}
	return binary.NativeEndian.Uint32(c[:])
}

// unpack is the inverse of pack.
func unpack(c uint32) (r, g, b, a uint8) {
	var v [4]byte
	binary.NativeEndian.PutUint32(v[:], c)
	return v[0], v[1], v[2], v[3]
}

// Image is a decoded image XObject or inline image, as a Device receives
// it. It is immutable and shared: by the renders of a page, by the pages
// of a document (through the document's image cache), and by the workers
// drawing it. Devices must not modify it.
//
// An image occupies the unit square of the user space it is drawn in: its
// first sample row is at the top (y = 1), its first column at x = 0.
type Image struct {
	// W and H are the size of the image in samples.
	W, H int
	// Stencil reports an image mask (/ImageMask true): the image has no
	// colours and paints the fill paint through its shape.
	Stencil bool
	// Interpolate reports the image's /Interpolate flag: it asks for
	// smoothing when the image is magnified.
	Interpolate bool

	color *stilus.Texture // nil for a stencil
	// mask is the image's alpha at its own resolution: the stencil
	// itself, a soft mask (/SMask), a stencil mask (/Mask stream) or a
	// colour key (/Mask array). Its colours are levels of alpha.
	mask *stilus.Texture
	size int // bytes, including mip levels, for the cache
}

// RGBA returns the image at its own resolution with its mask applied (a
// stencil in opaque black), for devices that extract images rather than
// draw them. It allocates the result.
func (im *Image) RGBA() *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, im.W, im.H))
	black := pack(0, 0, 0, 255)
	for y := range im.H {
		for x := range im.W {
			c := black
			if im.color != nil {
				c = im.color.Base().At(x, y)
			}
			if m := im.mask; m != nil {
				b := m.Base()
				a := b.At(x*b.W/im.W, y*b.H/im.H) & 0xff
				c = scale255(c, a)
			}
			binary.NativeEndian.PutUint32(out.Pix[4*(y*im.W+x):], c)
		}
	}
	return out
}
