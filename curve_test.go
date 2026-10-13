package cera

import (
	"context"
	"image"
	"image/color"
	"testing"
)

// TestCurveNotNumbers draws curves whose points are not numbers once the
// page's matrix is applied: a coordinate too large for a single-precision
// number is infinite, and on a page turned by a quarter infinity times
// zero is no number at all. The rasterizer flattened such a curve without
// end, which ended the process and not the render. The pages are drawn,
// with what of them can be.
func TestCurveNotNumbers(t *testing.T) {
	const huge = "99999999999999999999999999999999999999999999"
	for _, tt := range []struct{ name, content string }{
		{"control point", "0 0 m " + huge + " 1 2 2 3 3 c f"},
		{"end point", "0 0 m 1 1 2 2 " + huge + " 3 c f"},
		{"both coordinates", "0 0 m " + huge + " " + huge + " 2 2 3 3 c f"},
		{"v", "0 0 m " + huge + " 1 3 3 v f"},
		{"y", "0 0 m " + huge + " 1 3 3 y f"},
		{"stroked", "2 w 0 0 m " + huge + " 1 2 2 3 3 c S"},
		{"dashed", "[3 2] 0 d 0 0 m " + huge + " 1 2 2 3 3 c S"},
		{"clip", "0 0 m " + huge + " 1 2 2 3 3 c W n 0 0 100 100 re f"},
		{"turned again", "0 1 -1 0 0 0 cm 0 0 m 1 " + huge + " 2 2 3 3 c f"},
		{"negative", "0 0 m -" + huge + " 1 2 2 3 3 c f"},
	} {
		for _, rotate := range []string{"", "/Rotate 90", "/Rotate 180", "/Rotate 270"} {
			t.Run(tt.name+rotate, func(t *testing.T) {
				// What is drawn beside the curve is drawn.
				doc, err := Open(buildPDF([]string{"1 0 0 rg 150 50 20 20 re f " + tt.content}, rotate))
				if err != nil {
					t.Fatal(err)
				}
				p, err := doc.Page(0)
				if err != nil {
					t.Fatal(err)
				}
				for _, workers := range []int{1, 3} {
					dst := image.NewRGBA(p.Bounds(1))
					opt := RenderOptions{Scale: 1, Background: color.RGBA{255, 255, 255, 255}, Workers: workers}
					if err := p.Render(context.Background(), dst, opt); err != nil {
						t.Fatal(err)
					}
					red := 0
					for i := 0; i < len(dst.Pix); i += 4 {
						if dst.Pix[i] == 255 && dst.Pix[i+1] == 0 && dst.Pix[i+2] == 0 {
							red++
						}
					}
					if tt.name != "clip" && red < 300 {
						t.Errorf("%d workers: %d pixels of the square beside the curve, want 400", workers, red)
					}
				}
				if _, err := p.Text(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
