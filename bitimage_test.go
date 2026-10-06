package cera

import (
	"image"
	"image/color"
	"math/rand/v2"
	"testing"

	"github.com/timzifer/stilus"
)

// TestBitShaderMatchesImageShader holds bitShader against the shader it
// stands in for: for one-bit images and stencils, magnified, at about
// their own size and flipped, at fractional offsets and under constant
// alpha, every span must come out as stilus.ImageShader paints it.
func TestBitShaderMatchesImageShader(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for n := range 300 {
		w, h := 1+rng.IntN(90), 1+rng.IntN(40)
		stride := (w+7)/8 + rng.IntN(2)
		p := plane{Kind: stilus.PlaneBits, W: w, H: h, Stride: stride, Pix8: make([]uint8, stride*h)}
		for i := range p.Pix8 {
			p.Pix8[i] = uint8(rng.Uint32())
		}
		stencil := n%3 == 0
		var pal stilus.Palette
		if stencil {
			pal[0], pal[1] = pack(255, 255, 255, 255), 0
		} else {
			pal[0], pal[1] = pack(0, 0, 0, 255), pack(200, 120, 40, 255)
		}
		p.Pal = &pal
		img := &Image{W: w, H: h, Stencil: stencil}
		if stencil {
			img.mask = stilus.NewTexture(p)
		} else {
			img.color = stilus.NewTexture(p)
		}
		// Device size: the image's own size up to 13× larger,
		// either way round, at a fractional offset.
		sx := float64(w) * (1 + 12*rng.Float64())
		sy := float64(h) * (1 + 12*rng.Float64())
		if rng.IntN(4) == 0 {
			sx = -sx
		}
		if rng.IntN(5) == 0 {
			sx /= 3 // reduced along x, perhaps: one axis is enough
		}
		if rng.IntN(3) != 0 {
			sy = -sy
		}
		m := Matrix{sx, 0, 0, sy, 400 + 300*rng.Float64(), 300 + 200*rng.Float64()}
		c := color.RGBA{30, 60, 90, 255}
		if rng.IntN(3) == 0 {
			c = color.RGBA{10, 20, 30, uint8(rng.IntN(255))}
		}
		// A clip that cuts the image's columns, or not.
		lo, hi := int(min(m[4], m[4]+sx)), int(max(m[4], m[4]+sx))
		clip := image.Rect(lo-20+rng.IntN((hi-lo)/2+20), 0, hi+20-rng.IntN((hi-lo)/2+20), 1000)

		var id imageDraw
		if !id.setupBits(img, m, false, c, clip) {
			t.Fatalf("case %d: bitShader not set up for %v", n, m)
		}
		var ref stilus.ImageShader
		if stencil {
			ref.SetColor(stilus.PackRGBA(c))
			ref.SetMask(img.mask, toUnit(w, h).Mul(m), false)
		} else {
			ref.SetImage(img.color, toUnit(w, h).Mul(m), false, c.A)
		}
		for range 40 {
			y := rng.IntN(1000)
			x := clip.Min.X - 20 + rng.IntN(clip.Dx()+40)
			l := 1 + rng.IntN(300)
			got, want := make([]uint32, l), make([]uint32, l)
			id.bits.ShadeSpan(y, x, got)
			ref.ShadeSpan(y, x, want)
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("case %d (%d×%d, m %v, alpha %d): pixel (%d, %d) = %08x, want %08x",
						n, w, h, m, c.A, x+i, y, got[i], want[i])
				}
			}
		}
	}
}

// TestBitShaderDeclines checks that images bitShader cannot draw as
// stilus.ImageShader does are left to it.
func TestBitShaderDeclines(t *testing.T) {
	p := plane{Kind: stilus.PlaneBits, W: 100, H: 50, Stride: 13, Pix8: make([]uint8, 13*50), Pal: stilus.GrayPalette}
	img := &Image{W: 100, H: 50, color: stilus.NewTexture(p)}
	clip := image.Rect(0, 0, 1000, 1000)
	c := color.RGBA{0, 0, 0, 255}
	var id imageDraw
	for _, tc := range []struct {
		m      Matrix
		smooth bool
	}{
		{Matrix{50, 0, 0, 25, 0, 0}, false},    // reduced
		{Matrix{99, 0, 0, 49, 0, 0}, false},    // a little reduced
		{Matrix{200, 0, 0, 100, 0, 0}, true},   // smoothed
		{Matrix{200, 10, 0, 100, 0, 0}, false}, // skewed
		{Matrix{0, 100, 200, 0, 0, 0}, false},  // turned
		{Matrix{0, 0, 0, 0, 0, 0}, false},      // singular
	} {
		if id.setupBits(img, tc.m, tc.smooth, c, clip) {
			t.Errorf("bitShader set up for %v (smooth %v)", tc.m, tc.smooth)
		}
	}
	img.mask = img.color
	if id.setupBits(img, Matrix{200, 0, 0, 100, 0, 0}, false, c, clip) {
		t.Error("bitShader set up for an image with a mask")
	}
}
