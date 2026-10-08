//go:build !purego

package cera

import (
	"image"
	"image/color"
	"unsafe"
)

// fillStream sets n bytes from p to the repeated little-endian v with
// non-temporal stores; p is 16-byte aligned, n a multiple of 64. They are
// ordered before later stores only by storeFence.
//
//go:noescape
func fillStream(p *byte, n int, v uint32)

func storeFence()

// fillRegionStream sets every pixel of r in dst to c like fillRegion, but
// writes past the caches. Most of a large region that is filled and then
// barely drawn on would otherwise be read from memory before it is written
// (#64).
func fillRegionStream(dst *image.RGBA, r image.Rectangle, c color.RGBA) {
	r = r.Intersect(dst.Rect)
	if r.Empty() {
		return
	}
	v := uint32(c.R) | uint32(c.G)<<8 | uint32(c.B)<<16 | uint32(c.A)<<24
	n := 4 * r.Dx()
	if n == dst.Stride {
		// The rows follow each other: one run.
		n *= r.Dy()
		r.Max.Y = r.Min.Y + 1
	}
	defer storeFence()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := dst.Pix[dst.PixOffset(r.Min.X, y):][:n:n]
		head := int(-uintptr(unsafe.Pointer(&row[0])) & 15)
		if head%4 != 0 || head+64 > n {
			// Pixels off the 4-byte grid, or a short row.
			fillRegion(dst, image.Rect(r.Min.X, y, r.Max.X, r.Max.Y), c)
			return
		}
		body := (n - head) &^ 63
		fillPixels(row[:head], c)
		fillStream(&row[head], body, v)
		fillPixels(row[head+body:], c)
	}
}

// fillPixels sets the pixels of row, a multiple of 4 bytes, to c.
func fillPixels(row []uint8, c color.RGBA) {
	for i := 0; i+3 < len(row); i += 4 {
		row[i], row[i+1], row[i+2], row[i+3] = c.R, c.G, c.B, c.A
	}
}
