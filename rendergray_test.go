package cera

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"testing"
	"time"
)

// TestRenderGray draws pages in grey: the pixels of the page drawn in
// colour and converted, band by band, tile by tile.
func TestRenderGray(t *testing.T) {
	doc := editOpen(t, optFile())
	for i := range doc.NumPages() {
		p, err := doc.Page(i)
		if err != nil {
			t.Fatal(err)
		}
		for _, bg := range []color.RGBA{white, {}, {0, 0, 128, 128}} {
			opt := RenderOptions{Scale: 3, Background: bg, Workers: 2}
			rgba := image.NewRGBA(p.Bounds(3))
			if err := p.Render(context.Background(), rgba, opt); err != nil {
				t.Fatal(err)
			}
			// On white, as a grey image has no transparency.
			flat := image.NewRGBA(rgba.Rect)
			draw.Draw(flat, flat.Rect, image.NewUniform(color.White), image.Point{}, draw.Src)
			draw.Draw(flat, flat.Rect, rgba, rgba.Rect.Min, draw.Over)
			want := image.NewGray(rgba.Rect)
			draw.Draw(want, want.Rect, flat, flat.Rect.Min, draw.Src)

			got := image.NewGray(p.Bounds(3))
			var st Stats
			var bands []image.Rectangle
			opt.Stats, opt.Band = &st, func(r image.Rectangle) { bands = append(bands, r) }
			if err := p.RenderGray(context.Background(), got, opt); err != nil {
				t.Fatal(err)
			}
			differ, most := 0, 0
			for k := range want.Pix {
				if d := int(want.Pix[k]) - int(got.Pix[k]); d != 0 {
					differ++
					most = max(most, d, -d)
				}
			}
			// A pixel that is not opaque may round another way over white.
			if differ > 0 && (bg.A == 255 || most > 1) {
				t.Errorf("page %d on %v: %d pixels differ from the page converted, by up to %d", i, bg, differ, most)
			}
			area := 0
			for _, r := range bands {
				area += r.Dx() * r.Dy()
			}
			if area != got.Rect.Dx()*got.Rect.Dy() || st.Ops == 0 {
				t.Errorf("page %d: bands of %d pixels were reported of %d; %d operations", i, area, got.Rect.Dx()*got.Rect.Dy(), st.Ops)
			}
		}
	}

	// A tile, and a region of it: the rest of the image is not touched.
	p, _ := doc.Page(0)
	whole := image.NewGray(p.Bounds(2))
	if err := p.RenderGray(context.Background(), whole, RenderOptions{Scale: 2, Background: white}); err != nil {
		t.Fatal(err)
	}
	tile := image.NewGray(image.Rect(100, 60, 300, 260))
	for k := range tile.Pix {
		tile.Pix[k] = 7
	}
	region := image.Rect(150, 100, 250, 200)
	if err := p.RenderGray(context.Background(), tile, RenderOptions{Scale: 2, Background: white, Region: region}); err != nil {
		t.Fatal(err)
	}
	for y := tile.Rect.Min.Y; y < tile.Rect.Max.Y; y++ {
		for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
			want := uint8(7)
			if image.Pt(x, y).In(region) {
				want = whole.GrayAt(x, y).Y
			}
			if got := tile.GrayAt(x, y).Y; got != want {
				t.Fatalf("the tile has %d at %d, %d, want %d", got, x, y, want)
			}
		}
	}
	// It is not all one grey.
	seen := map[uint8]bool{}
	for _, v := range whole.Pix {
		seen[v] = true
	}
	if len(seen) < 4 {
		t.Errorf("the page has %d greys", len(seen))
	}

	// Many bands: a page larger than one band of colour.
	big := image.NewGray(p.Bounds(8))
	var st Stats
	n := 0
	if err := p.RenderGray(context.Background(), big, RenderOptions{Scale: 8, Background: white, Stats: &st, Band: func(image.Rectangle) { n++ }}); err != nil {
		t.Fatal(err)
	}
	if n < 2 || big.Rect.Dx()*big.Rect.Dy() <= grayPixels {
		t.Errorf("%d bands for %d pixels", n, big.Rect.Dx()*big.Rect.Dy())
	}
	// The red image, 20 to 60 across and 100 to 140 up a page of 200,
	// is the grey of its colour, in the last band as in the first.
	px := image.NewRGBA(image.Rect(8*40, 8*80, 8*40+1, 8*80+1))
	if err := p.Render(context.Background(), px, RenderOptions{Scale: 8, Background: white}); err != nil {
		t.Fatal(err)
	}
	want := color.GrayModel.Convert(px.At(8*40, 8*80)).(color.Gray).Y
	if r, g := big.GrayAt(8*40, 8*80).Y, big.GrayAt(8*5, 8*5).Y; r != want || r > 128 || g != 255 {
		t.Errorf("the red image is %d, want %d; the paper %d", r, want, g)
	}

	// Past its deadline: the background, and the error Render gives.
	late := image.NewGray(p.Bounds(1))
	err := p.RenderGray(context.Background(), late, RenderOptions{Scale: 1, Background: color.RGBA{10, 10, 10, 255}, Deadline: time.Now().Add(-time.Second)})
	if !errors.Is(err, ErrDeadline) || late.Pix[0] != 10 || late.Pix[len(late.Pix)-1] != 10 {
		t.Errorf("past the deadline: %v, the image starts with %d", err, late.Pix[0])
	}
	// Nothing to draw.
	if err := p.RenderGray(context.Background(), image.NewGray(image.Rectangle{}), RenderOptions{}); err != nil {
		t.Errorf("an empty image: %v", err)
	}
}
