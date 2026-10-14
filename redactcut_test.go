package cera

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
)

// cutShapes are paths that reach into the area [100 60 200 140] of
// cutFile, each with a number in it that is only in the area. 77.77 and
// so on are coordinates of points in the area.
var cutShapes = []string{
	// A filled circle half in the area.
	"1 0 0 rg 80 100 m 80 122.091 97.909 140 120 140 c 142.091 140 160 122.091 160 100 c 160 77.909 142.091 60 120 60 c 97.909 60 80 77.909 80 100 c f",
	// A stroked curve through it.
	"0 0 1 RG 3 w 20 20 m 150.11 177.77 150.22 22.33 280 180 c S",
	// A filled open path that starts in the area.
	"0 0.5 0 rg 155.55 99.99 m 250 70 l 250 130 l f",
	// A stroked polyline that starts and ends in it, and a corner in it,
	// with round joins.
	"1 0 1 RG 2 w 1 j 130.13 80.08 m 220 80 l 220 30 l 160 30 l 144.44 111.11 l 170 190 l 188.88 133.33 l S",
	// A clip that reaches into it, with a fill through it.
	"q 60 70 m 166.66 98.76 l 60 130 l h W n 0 1 1 rg 0 0 300 200 re f Q",
	// A rectangle across it, closed and stroked by s and b.
	"0.5 0.5 0 RG 1 w 0 0.8 0.8 rg 90 90 120.12 10.01 re b",
	// A line there and back, closed by s, with round ends.
	"0 0 0 RG 2 w 1 J 1 j 10 10 m 290 190 l s",
	// A dashed line through it: not cut, but clipped.
	"0 0 0 RG [3 2] 0 d 1 w 0 100 m 300 105.55 l S [] 0 d",
}

func cutFile(content string) []byte {
	return testpdf.File{Objs: map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R>>",
		2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200]>>",
		3: "<</Type /Page /Parent 2 0 R /Contents 4 0 R>>",
		4: testpdf.Stream("", []byte(content)),
	}, Trailer: "/Root 1 0 R"}.Bytes()
}

var cutArea0 = Rect{100, 60, 200, 140}

// TestRedactCut redacts paths that reach into an area: what they paint
// outside it is as it was, and the numbers of the points in it are not in
// the file, but for the dashed line, which is only clipped.
func TestRedactCut(t *testing.T) {
	for i, shape := range cutShapes {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			src := cutFile(shape)
			doc := editOpen(t, src)
			e := doc.Edit()
			if err := e.Redact(0, []Quad{QuadOf(cutArea0)}, nil); err != nil {
				t.Fatal(err)
			}
			out := editSave(t, e)
			od := editOpen(t, out)
			p0, _ := doc.Page(0)
			p1, _ := od.Page(0)
			for _, scale := range []float64{1, 3} {
				a, b := renderAt(t, p0, scale), renderAt(t, p1, scale)
				// Outside the area, a pixel away from it.
				m := p0.deviceMatrix(scale)
				x0, y1 := m.Apply(cutArea0.X0, cutArea0.Y0)
				x1, y0 := m.Apply(cutArea0.X1, cutArea0.Y1)
				in := image.Rect(int(math.Floor(x0))-1, int(math.Floor(y0))-1, int(math.Ceil(x1))+1, int(math.Ceil(y1))+1)
				n, most := 0, 0
				for y := a.Rect.Min.Y; y < a.Rect.Max.Y; y++ {
					for x := a.Rect.Min.X; x < a.Rect.Max.X; x++ {
						if (image.Point{x, y}).In(in) {
							if c := b.RGBAAt(x, y); c != white && x > in.Min.X && x < in.Max.X-1 && y > in.Min.Y && y < in.Max.Y-1 {
								t.Fatalf("scale %v: the area has %v at %d, %d", scale, c, x, y)
							}
							continue
						}
						ca, cb := a.RGBAAt(x, y), b.RGBAAt(x, y)
						if d := max(absDiff(ca.R, cb.R), absDiff(ca.G, cb.G), absDiff(ca.B, cb.B)); d > 0 {
							n++
							most = max(most, d)
						}
					}
				}
				// A curve cut into pieces is flattened anew along them;
				// lines are as they were.
				tol := 2
				if strings.Contains(shape, " c ") {
					tol = 24
				}
				if n > 0 && most > tol {
					t.Errorf("scale %v: %d pixels outside the area differ, by up to %d", scale, n, most)
				}
			}
			all := everything(t, out)
			dashed := strings.Contains(shape, " d ")
			for _, secret := range []string{"77.77", "99.99", "130.13", "80.08", "144.44", "111.11", "188.88", "133.33", "166.66", "98.76", "105.55"} {
				if strings.Contains(shape, secret) && bytes.Contains(all, []byte(secret)) != dashed {
					t.Errorf("%s is in the file: %v", secret, !dashed)
				}
			}
		})
	}
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// renderAt renders p at scale on white.
func renderAt(t *testing.T, p *Page, scale float64) *image.RGBA {
	t.Helper()
	dst := image.NewRGBA(p.Bounds(scale))
	if err := p.Render(context.Background(), dst, RenderOptions{Scale: scale, Background: white, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	return dst
}

// TestCutGeometry cuts segments out of an area: what is left has no
// point in it but on the lines that stand for runs in it.
func TestCutGeometry(t *testing.T) {
	q := QuadOf(cutArea0)
	for i, path := range []string{
		"80 100 m 80 122.091 97.909 140 120 140 c 142.091 140 160 122.091 160 100 c 160 77.909 142.091 60 120 60 c 97.909 60 80 77.909 80 100 c",
		"20 20 m 150.11 177.77 150.22 22.33 280 180 c",
		"155.55 99.99 m 250 70 l 250 130 l",
		"130.13 80.08 m 220 80 l 220 30 l 160 30 l 144.44 111.11 l 170 190 l 188.88 133.33 l",
		"60 70 m 166.66 98.76 l 60 130 l h",
		"90 90 120.12 10.01 re",
		"120 100 m 130 110 l 150 90 160 95 v 180 100 140 120 y h",
	} {
		subs, ok := parsePath([]byte(path))
		if !ok {
			t.Fatalf("%d: %q does not parse", i, path)
		}
		for _, d := range []float64{0, 3} {
			a, ok := newCutArea(q, d)
			if !ok {
				t.Fatal("no area")
			}
			for _, mode := range [][2]bool{{true, false}, {false, true}, {true, true}} {
				cut, ok := a.cut(subs, mode[0], mode[1])
				if !ok {
					continue
				}
				for _, s := range cut {
					if a.inside(s.start) {
						t.Errorf("%d: a subpath starts in the area at %v", i, s.start)
					}
					for _, seg := range s.segs {
						if a.inside(seg.end()) {
							t.Errorf("%d: a segment ends in the area at %v", i, seg.end())
						}
						if seg.cubic {
							for k := 1; k < 10; k++ {
								if p := seg.at(float64(k) / 10); a.inside(p) {
									t.Errorf("%d: a curve passes %v in the area", i, p)
								}
							}
						}
					}
				}
			}
		}
	}
	if _, ok := parsePath([]byte("10 10 l")); ok {
		t.Error("a line without a start parsed")
	}
	if _, ok := parsePath([]byte("10 10 m 5 Tj")); ok {
		t.Error("an operator that makes no path parsed")
	}
}

// TestRedactOverlay writes the text redaction annotations ask for over
// their areas: in the size and colour of /DA, centred by /Q, repeated by
// /Repeat, and in a fallback font where Helvetica lacks a character.
func TestRedactOverlay(t *testing.T) {
	objs := redactObjs("")
	objs[3] = strings.Replace(objs[3], "23 0 R]", "23 0 R 52 0 R 53 0 R 54 0 R]", 1)
	objs[52] = "<</Type /Annot /Subtype /Redact /Rect [100 100 200 120] /OverlayText (CONFIDENTIAL) /DA (/Helv 10 Tf 1 0 0 rg) /Q 1>>"
	objs[53] = "<</Type /Annot /Subtype /Redact /Rect [10 5 120 50] /IC [0 0 0] /OverlayText (X) /DA (/Helv 0 Tf 1 g) /Repeat true>>"
	objs[54] = "<</Type /Annot /Subtype /Redact /Rect [200 130 290 150] /OverlayText <FEFF03A903A9>>>"
	doc := editOpen(t, testpdf.File{Objs: objs, Trailer: "/Root 1 0 R"}.Bytes())
	e := doc.Edit()
	if n, err := e.ApplyRedactions(); err != nil || n != 3 {
		t.Fatalf("%d applied: %v", n, err)
	}
	od := editOpen(t, editSave(t, e))
	pt := textOf(t, od, 0)
	count := func(s string, r Rect) int {
		n := 0
		for _, c := range pt.Chars {
			x := (c.Quad.P[0][0] + c.Quad.P[3][0]) / 2
			y := (c.Quad.P[0][1] + c.Quad.P[3][1]) / 2
			p, _ := od.Page(0)
			inv, _ := p.DisplayMatrix().Invert()
			ux, uy := inv.Apply(x, y)
			if c.Text != "" && strings.Contains(s, c.Text) && ux > r.X0 && ux < r.X1 && uy > r.Y0 && uy < r.Y1 {
				n++
			}
		}
		return n
	}
	if n := count("CONFIDENTIAL", Rect{100, 100, 200, 120}); n != len("CONFIDENTIAL") {
		t.Errorf("%d characters of CONFIDENTIAL in its area", n)
	}
	if n := count("X", Rect{10, 5, 120, 50}); n < 20 {
		t.Errorf("X is %d times in its area", n)
	}
	if n := count("Ω", Rect{200, 130, 290, 150}); n != 2 {
		t.Errorf("Ω is %d times in its area", n)
	}
	p, _ := od.Page(0)
	img := renderView(t, p)
	red, black := 0, 0
	m := p.DisplayMatrix()
	x0, y1 := m.Apply(100, 100)
	x1, y0 := m.Apply(200, 120)
	for y := int(y0); y < int(y1); y++ {
		for x := int(x0); x < int(x1); x++ {
			if c := img.RGBAAt(x, y); c.R > 200 && c.G < 80 && c.B < 80 {
				red++
			}
		}
	}
	x0, y1 = m.Apply(10, 5)
	x1, y0 = m.Apply(120, 50)
	for y := int(y0) + 1; y < int(y1)-1; y++ {
		for x := int(x0) + 1; x < int(x1)-1; x++ {
			if img.RGBAAt(x, y) == (color.RGBA{0, 0, 0, 255}) {
				black++
			}
		}
	}
	if red < 50 {
		t.Errorf("%d red pixels of CONFIDENTIAL", red)
	}
	if total := (int(x1) - int(x0) - 2) * (int(y1) - int(y0) - 2); black == total || black < total/2 {
		t.Errorf("%d of %d pixels of the box under the white Xs are black", black, total)
	}
}
