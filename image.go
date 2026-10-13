package cera

import (
	"encoding/binary"
	"image"
	"sync"

	"github.com/timzifer/stilus"
)

// Images are held as stilus textures, the way their samples come: a
// picture of one component of at most eight bits (a scan, a grey or
// indexed image) keeps one byte a pixel and a palette, a one-bit picture
// (a fax, a JBIG2 page, a stencil mask) one bit a pixel, and only colour
// pictures take four bytes a pixel. Drawing an image much smaller than its
// samples reads a mip level of the texture, made once when first needed
// and kept with the image (see stilus.Texture).

type (
	plane   = stilus.Plane
	palette = stilus.Palette
	texture = stilus.Texture
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

	color *texture // nil for a stencil
	// mask is the image's alpha at its own resolution: the stencil
	// itself, a soft mask (/SMask), a stencil mask (/Mask stream) or a
	// colour key (/Mask array). Its colours are levels of alpha.
	mask *texture
	// softMask reports that mask is the image's /SMask, which replaces
	// the soft mask of the graphics state.
	softMask bool
	size     int // bytes, including mip levels, for the cache
	// opaque reports that every sample of the image is opaque: it has
	// colours, no mask and no alpha of its own.
	opaque bool

	// through holds the image with its colours passed through transfer
	// functions, one for each that painted it.
	throughMu sync.Mutex
	through   map[*transfer]*Image
}

// transferred returns the image as the transfer function t makes it: its
// colours through the function, its shape as it is. An image of one
// component or one bit keeps its samples and gets another palette; a
// colour image is copied. The result is kept with the image.
func (img *Image) transferred(t *transfer) *Image {
	if t == nil || img.color == nil {
		return img
	}
	img.throughMu.Lock()
	defer img.throughMu.Unlock()
	if out := img.through[t]; out != nil {
		return out
	}
	p := *img.color.Base()
	switch p.Kind {
	case stilus.PlaneRGBA:
		pix := make([]uint32, len(p.Pix32))
		for i, v := range p.Pix32 {
			pix[i] = t.pixel(v)
		}
		p.Pix32 = pix
	default:
		pal := *p.Pal
		for i, v := range pal {
			pal[i] = t.pixel(v)
		}
		p.Pal = &pal
	}
	out := &Image{
		W: img.W, H: img.H, Stencil: img.Stencil, Interpolate: img.Interpolate,
		color: stilus.NewTexture(p), mask: img.mask, softMask: img.softMask, size: img.size, opaque: img.opaque,
	}
	if img.through == nil {
		img.through = map[*transfer]*Image{}
	}
	img.through[t] = out
	return out
}

// opaquePlane reports whether every pixel of p is opaque.
func opaquePlane(p *plane) bool {
	switch p.Kind {
	case stilus.PlaneRGBA:
		for _, c := range p.Pix32 {
			if _, _, _, a := unpack(c); a != 255 {
				return false
			}
		}
		return true
	case stilus.PlaneIndex:
		var seen [256]bool
		for _, v := range p.Pix8 {
			if !seen[v] {
				if _, _, _, a := unpack(p.Pal[v]); a != 255 {
					return false
				}
				seen[v] = true
			}
		}
		return true
	}
	_, _, _, a0 := unpack(p.Pal[0])
	_, _, _, a1 := unpack(p.Pal[1])
	return a0 == 255 && a1 == 255
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
