package main

import (
	"image"
	"image/color"
	"testing"
)

func solid(w, h int, c color.RGBA) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = c.R, c.G, c.B, 255
	}
	return m
}

func TestMedian(t *testing.T) {
	v := [][3]int{{10}, {200}, {30}, {20}}
	if m, lo, hi := median(v, []int{0, 1, 2}, 0); m != 30 || lo != 10 || hi != 200 {
		t.Errorf("odd: %d %d %d", m, lo, hi)
	}
	if m, _, _ := median(v, []int{0, 2, 3}, 0); m != 20 {
		t.Errorf("odd: %d", m)
	}
	if m, _, _ := median(v, []int{0, 3}, 0); m != 15 {
		t.Errorf("even: %d", m)
	}
}

func TestAnalyse(t *testing.T) {
	white, grey := color.RGBA{255, 255, 255, 255}, color.RGBA{100, 100, 100, 255}
	names := []string{"cera", "pdfium", "mupdf", "poppler"}
	imgs := []*image.RGBA{solid(4, 1, white), solid(4, 1, white), solid(4, 1, white), solid(4, 1, white)}
	// Pixel 0: paper everywhere. Pixel 1: all agree on grey. Pixel 2: cera
	// alone draws. Pixel 3: the references disagree.
	for _, img := range imgs {
		img.SetRGBA(1, 0, grey)
	}
	imgs[0].SetRGBA(2, 0, grey)
	imgs[1].SetRGBA(3, 0, grey)
	st, _, omap := analyse(names, imgs, nil)
	if st.ink != 3 {
		t.Errorf("ink %d, want 3", st.ink)
	}
	if st.outlier["cera"] != 1 || st.outlier["pdfium"] != 1 || st.outlier["mupdf"] != 0 {
		t.Errorf("outliers %v", st.outlier)
	}
	// Pixel 3: without pdfium the others agree on white; without cera
	// they disagree. So cera's references disagree there: contested.
	if st.contested != 1 {
		t.Errorf("contested %d, want 1", st.contested)
	}
	if st.pair["cera"]["pdfium"] != 2 || st.pair["mupdf"]["poppler"] != 0 {
		t.Errorf("pairs %v", st.pair)
	}
	if c := omap.RGBAAt(2, 0); [3]uint8{c.R, c.G, c.B} != outlierColor {
		t.Errorf("outlier map at cera's outlier: %v", c)
	}
}

func TestAnalyseExact(t *testing.T) {
	white, black := color.RGBA{255, 255, 255, 255}, color.RGBA{0, 0, 0, 255}
	ex := solid(2, 1, white)
	ex.SetRGBA(0, 0, black)
	heavy := solid(2, 1, black) // draws twice the ink
	st, _, _ := analyse([]string{"cera", "a", "b"}, []*image.RGBA{heavy, ex, ex}, ex)
	e := st.exact["cera"]
	if r := e.ink / e.ref; r != 2 {
		t.Errorf("ink ratio %v, want 2", r)
	}
	if e.over != 1 || st.exact["a"].over != 0 {
		t.Errorf("over %d / %d", e.over, st.exact["a"].over)
	}
}
