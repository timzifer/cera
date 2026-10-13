package cera

import (
	"context"
	"image"
	"sync"
)

// grayPixels is how many pixels of a page RenderGray draws in colour at a
// time: 4 MB of RGBA, whatever the size of the page.
const grayPixels = 1 << 20

// grayBands are the colour bands RenderGray draws into.
var grayBands = sync.Pool{New: func() any { return new([]byte) }}

// RenderGray draws the page into dst as [Page.Render] does, in grey: for
// a monochrome printer, a fax, text recognition. dst has the pixel
// coordinates Render's has, and opt means what it means there; where the
// background is not opaque the page is drawn on white.
//
// The page is drawn in colour, a band of it at a time, and each band is
// converted as PDF converts DeviceRGB to DeviceGray (PDF 2.0, 10.4.2.2:
// 0.3 R + 0.59 G + 0.11 B, here with the weights of [color.GrayModel]) —
// the pixels Render draws into tiles of that size, converted, without
// holding the page in colour: a page of 100 megapixels takes 100 MB and
// not 500.
func (p *Page) RenderGray(ctx context.Context, dst *image.Gray, opt RenderOptions) (err error) {
	region := dst.Bounds()
	if !opt.Region.Empty() {
		region = region.Intersect(opt.Region)
	}
	band, stats := opt.Band, opt.Stats
	opt.Band, opt.Region = nil, image.Rectangle{}
	var first Stats
	if stats != nil {
		opt.Stats = new(Stats)
	}
	buf := grayBands.Get().(*[]byte)
	defer grayBands.Put(buf)
	rows := max(1, min(grayPixels/max(region.Dx(), 1), 4096))
	if region.Empty() {
		// Nothing to draw: what Render says of the page all the same.
		region = image.Rectangle{}
	}
	for y, n := region.Min.Y, 0; y < region.Max.Y || n == 0; y, n = y+rows, n+1 {
		r := image.Rect(region.Min.X, y, region.Max.X, min(y+rows, region.Max.Y))
		if need := 4 * r.Dx() * r.Dy(); cap(*buf) < need {
			*buf = make([]byte, need)
		}
		tile := &image.RGBA{Pix: (*buf)[:4*r.Dx()*r.Dy()], Stride: 4 * r.Dx(), Rect: r}
		if rerr := p.Render(ctx, tile, opt); rerr != nil && err == nil {
			err = rerr
		}
		if n == 0 && stats != nil {
			// The first band says whether the page was interpreted for it.
			first.set(opt.Stats)
		}
		for yy := r.Min.Y; yy < r.Max.Y; yy++ {
			src := tile.Pix[tile.PixOffset(r.Min.X, yy):][:4*r.Dx()]
			out := dst.Pix[dst.PixOffset(r.Min.X, yy):][:r.Dx()]
			for x := range out {
				s := src[4*x:][:4]
				// Premultiplied colour over white, then the weights of
				// color.GrayModel on 16-bit components.
				w := 255 - uint32(s[3])
				out[x] = uint8((19595*(uint32(s[0])+w)*257 + 38470*(uint32(s[1])+w)*257 + 7471*(uint32(s[2])+w)*257 + 1<<15) >> 24)
			}
		}
		if band != nil && !r.Empty() {
			band(r)
		}
	}
	if stats != nil {
		stats.set(&first)
		stats.Reused = first.Reused
	}
	return err
}
