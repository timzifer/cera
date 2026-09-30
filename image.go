package cera

import (
	"encoding/binary"
	"image"
	"math/bits"
	"sync"
	"sync/atomic"
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

// planeKind is how a plane stores its samples.
type planeKind uint8

const (
	planeRGBA  planeKind = iota // pix32, premultiplied, native layout
	planeIndex                  // pix8, indexes into pal
	planeBits                   // pix8, one bit a pixel (MSB first) indexing pal[0] or pal[1]
)

// palette maps samples to premultiplied colours in the native layout of
// stilus (the memory order of image.RGBA).
type palette [256]uint32

// plane is one level of an image: w × h pixels.
type plane struct {
	kind   planeKind
	w, h   int
	stride int // pixels per row for RGBA and Index, bytes for Bits
	pix8   []uint8
	pix32  []uint32
	pal    *palette
}

// at returns the premultiplied colour of pixel (x, y), which must be
// inside the plane.
func (p *plane) at(x, y int) uint32 {
	switch p.kind {
	case planeRGBA:
		return p.pix32[y*p.stride+x]
	case planeIndex:
		return p.pal[p.pix8[y*p.stride+x]]
	}
	return p.pal[p.pix8[y*p.stride+x>>3]>>(7-uint(x)&7)&1]
}

func (p *plane) bytes() int { return len(p.pix8) + 4*len(p.pix32) }

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

// Shared palettes: opaque greys and levels of alpha (white premultiplied,
// so all four channels are the level).
var grayPal, alphaPal = func() (*palette, *palette) {
	var g, a palette
	for i := range 256 {
		v := uint8(i)
		g[i] = pack(v, v, v, 255)
		a[i] = pack(v, v, v, v)
	}
	return &g, &a
}()

// maxMip bounds the mip levels (a reduction by 2048): block sums of four
// 8-bit channels must fit 32 bits.
const maxMip = 11

// texture is a plane and its mip levels, made on demand. Levels may be
// requested by several raster workers at once.
type texture struct {
	base plane
	// gray and alpha say that every colour of base is an opaque grey or
	// a level of alpha, so that its levels can be one byte a pixel.
	gray, alpha bool

	mu   sync.Mutex
	mips [maxMip + 1]atomic.Pointer[plane]
}

func newTexture(p plane) *texture {
	t := &texture{base: p}
	if p.kind != planeRGBA {
		n := 256
		if p.kind == planeBits {
			n = 2
		}
		t.gray, t.alpha = true, true
		for _, c := range p.pal[:n] {
			r, g, b, a := unpack(c)
			t.gray = t.gray && r == g && g == b && a == 255
			t.alpha = t.alpha && r == g && g == b && b == a
		}
	}
	return t
}

// levels returns the number of levels below base: halvings until the
// plane is one pixel, at most maxMip.
func (t *texture) levels() int {
	n := max(t.base.w, t.base.h) - 1
	return min(bits.Len(uint(n)), maxMip)
}

// mipBytes estimates the memory all levels of t can take.
func (t *texture) mipBytes() int {
	px := t.base.w * t.base.h / 3
	if t.gray || t.alpha {
		return px
	}
	return 4 * px
}

// level returns mip level k (0 is base), making it if needed.
func (t *texture) level(k int) *plane {
	k = min(k, t.levels())
	if k <= 0 {
		return &t.base
	}
	if p := t.mips[k].Load(); p != nil {
		return p
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if p := t.mips[k].Load(); p != nil {
		return p
	}
	p := t.downsample(k)
	t.mips[k].Store(p)
	return p
}

// downsample averages base over blocks of 2^k × 2^k pixels (smaller at
// the right and bottom edges). Levels are made from base directly, so a
// deep level costs no intermediate ones.
func (t *texture) downsample(k int) *plane {
	src := &t.base
	f := 1 << k
	w, h := (src.w+f-1)>>k, (src.h+f-1)>>k
	out := &plane{w: w, h: h, stride: w}
	single := t.gray || t.alpha
	if single {
		out.kind, out.pix8, out.pal = planeIndex, make([]uint8, w*h), grayPal
		if !t.gray {
			out.pal = alphaPal
		}
	} else {
		out.kind, out.pix32 = planeRGBA, make([]uint32, w*h)
	}
	sums := make([]uint32, 4*w)
	var ones []uint32
	if src.kind == planeBits {
		ones = make([]uint32, w)
	}
	for dy := range h {
		y0, y1 := dy<<k, min((dy+1)<<k, src.h)
		if ones != nil {
			t.countBits(ones, k, y0, y1)
		} else {
			clear(sums)
			for sy := y0; sy < y1; sy++ {
				for sx := range src.w {
					c := src.at(sx, sy)
					s := sums[4*(sx>>k):][:4]
					s[0] += c & 0xff
					s[1] += c >> 8 & 0xff
					s[2] += c >> 16 & 0xff
					s[3] += c >> 24
				}
			}
		}
		rows := uint32(y1 - y0)
		for dx := range w {
			n := rows * uint32(min(f, src.w-dx<<k))
			var c uint32
			if ones != nil {
				// Blend the two palette entries by the share of ones.
				c = mixCount(src.pal[0], src.pal[1], ones[dx], n)
			} else {
				s := sums[4*dx:][:4]
				c = (s[0]+n/2)/n | (s[1]+n/2)/n<<8 | (s[2]+n/2)/n<<16 | (s[3]+n/2)/n<<24
			}
			if single {
				r, _, _, a := unpack(c)
				if t.gray {
					out.pix8[dy*w+dx] = r
				} else {
					out.pix8[dy*w+dx] = a
				}
			} else {
				out.pix32[dy*w+dx] = c
			}
		}
	}
	return out
}

// countBits sets ones[dx] to the number of set bits of the bit plane base
// in rows [y0, y1) and columns [dx<<k, (dx+1)<<k).
func (t *texture) countBits(ones []uint32, k, y0, y1 int) {
	src := &t.base
	clear(ones)
	for sy := y0; sy < y1; sy++ {
		row := src.pix8[sy*src.stride:][:(src.w+7)/8]
		if k >= 3 {
			// Blocks are whole bytes; the last byte may hold padding.
			for i, b := range row {
				if rest := src.w - 8*i; rest < 8 {
					b &= 0xff << (8 - uint(rest))
				}
				ones[(8*i)>>k] += uint32(bits.OnesCount8(b))
			}
			continue
		}
		for sx := range src.w {
			ones[sx>>k] += uint32(row[sx>>3] >> (7 - uint(sx)&7) & 1)
		}
	}
}

// mixCount returns the average of n pixels of which ones are c1 and the
// others c0, per channel and rounded.
func mixCount(c0, c1, ones, n uint32) uint32 {
	zeros := n - ones
	var out uint32
	for s := uint(0); s < 32; s += 8 {
		v := (c0>>s&0xff)*zeros + (c1>>s&0xff)*ones
		out |= (v + n/2) / n << s
	}
	return out
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
				c = im.color.base.at(x, y)
			}
			if m := im.mask; m != nil {
				a := m.base.at(x*m.base.w/im.W, y*m.base.h/im.H) & 0xff
				c = scale255(c, a)
			}
			binary.NativeEndian.PutUint32(out.Pix[4*(y*im.W+x):], c)
		}
	}
	return out
}
