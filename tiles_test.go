package cera

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
)

// drawingPage is the content of a page of 200 × 100 points that is all
// edges: strokes from hairlines to wide ones at every slope, dashed
// ones, filled and stroked curves, translucent ones, and a frame under a
// clip that does not fall on pixels, over a backdrop that is not white.
func drawingPage() string {
	rng := rand.New(rand.NewPCG(211, 1))
	f := func(n float64) float64 { return math.Round(rng.Float64()*n*100) / 100 }
	var b strings.Builder
	b.WriteString("0.8 0.9 1 rg 0 0 200 100 re f\n")
	for i := range 160 {
		fmt.Fprintf(&b, "%.2f %.2f %.2f RG %.2f w ", rng.Float64(), rng.Float64(), rng.Float64(), []float64{0, 0.2, 0.35, 0.5, 1, 2.83}[rng.IntN(6)])
		if i%7 == 0 {
			b.WriteString("[3 2] 0 d ")
		} else {
			b.WriteString("[] 0 d ")
		}
		x, y := f(200), f(100)
		switch rng.IntN(4) {
		case 0: // nearly level
			fmt.Fprintf(&b, "%.2f %.2f m %.2f %.2f l S\n", x, y, f(200), y+rng.Float64()*0.3)
		case 1: // nearly upright
			fmt.Fprintf(&b, "%.2f %.2f m %.2f %.2f l S\n", x, y, x+rng.Float64()*0.3, f(100))
		case 2:
			fmt.Fprintf(&b, "%.2f %.2f m %.2f %.2f %.2f %.2f %.2f %.2f c S\n", x, y, f(200), f(100), f(200), f(100), f(200), f(100))
		default:
			fmt.Fprintf(&b, "%.2f %.2f m %.2f %.2f l %.2f %.2f l S\n", x, y, f(200), f(100), f(200), f(100))
		}
	}
	for i := range 30 {
		if i%3 == 0 {
			b.WriteString("/H gs ")
		} else {
			b.WriteString("/N gs ")
		}
		fmt.Fprintf(&b, "%.2f %.2f %.2f rg %.2f %.2f m %.2f %.2f %.2f %.2f %.2f %.2f c %.2f %.2f l f\n",
			rng.Float64(), rng.Float64(), rng.Float64(), f(200), f(100), f(200), f(100), f(200), f(100), f(200), f(100), f(200), f(100))
	}
	b.WriteString("/N gs q 20.25 15.4 150.3 60.7 re W n 0.8 g 20.3 15.5 150.4 60.9 re f 0 0.5 0 RG 2.83 w [] 0 d 20.3 15.5 150.4 60.9 re S Q\n")
	return b.String()
}

// TestRenderTiles holds a page drawn in bands and in tiles, and by
// several workers, against the page drawn at once: every pixel is the
// same. A viewer may cut a page as it likes, and how many workers drew
// it does not show (#211).
func TestRenderTiles(t *testing.T) {
	doc, err := Open(buildPDF([]string{drawingPage()}, "/Resources << /ExtGState << /H << /ca 0.5 >> /N << /ca 1 >> >> >>"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	white := color.RGBA{255, 255, 255, 255}
	for _, scale := range []float64{1, 1.5, 4} {
		box := p.Bounds(scale)
		want := image.NewRGBA(box)
		if err := p.Render(context.Background(), want, RenderOptions{Scale: scale, Background: white, Workers: 1}); err != nil {
			t.Fatal(err)
		}
		differ := func(name string, got *image.RGBA) {
			t.Helper()
			if bytes.Equal(got.Pix, want.Pix) {
				return
			}
			n, most := pixDiff(want, got)
			t.Errorf("scale %v, %s: %d pixels are not those of the page drawn at once, by up to %d", scale, name, n, most)
		}
		for _, size := range []image.Point{{box.Dx(), 37}, {53, box.Dy()}, {64, 64}, {256, 256}} {
			got := image.NewRGBA(box)
			for y := box.Min.Y; y < box.Max.Y; y += size.Y {
				for x := box.Min.X; x < box.Max.X; x += size.X {
					opt := RenderOptions{Scale: scale, Background: white, Workers: 1, Region: image.Rect(x, y, x+size.X, y+size.Y)}
					if err := p.Render(context.Background(), got, opt); err != nil {
						t.Fatal(err)
					}
				}
			}
			differ(fmt.Sprintf("tiles of %v", size), got)
		}
		// The tests draw in bands whatever a page costs (adaptWorkers).
		for _, workers := range []int{2, 5} {
			got := image.NewRGBA(box)
			if err := p.Render(context.Background(), got, RenderOptions{Scale: scale, Background: white, Workers: workers}); err != nil {
				t.Fatal(err)
			}
			differ(fmt.Sprintf("%d workers", workers), got)
		}
		grey := image.NewGray(box)
		if err := p.RenderGray(context.Background(), grey, RenderOptions{Scale: scale, Background: white, Workers: 1}); err != nil {
			t.Fatal(err)
		}
		for i, g := range grey.Pix {
			if c := want.Pix[4*i : 4*i+4]; g != color.GrayModel.Convert(color.RGBA{c[0], c[1], c[2], c[3]}).(color.Gray).Y {
				t.Errorf("scale %v: the page in grey is not the grey of the page", scale)
				break
			}
		}
	}
}
