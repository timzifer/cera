package main

import (
	"fmt"
	"image"
	"image/draw"
	stdpng "image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/timzifer/cera"
)

// The three ways of writing PNGs write the same pixels.
func TestPNGModes(t *testing.T) {
	for _, tc := range []struct {
		file        string
		page        int
		transparent bool
	}{
		{"pdfjs/tracemonkey.pdf", 1, false},
		{"pdfjs/tracemonkey.pdf", 1, true},
		{"pdfjs/jbig2_symbol_offset.pdf", 1, false},
		{"pdfjs/transparency_group.pdf", 1, false},
		{"synthetic/contours-3000.pdf", 1, false},
	} {
		in := filepath.Join("..", "..", "testdata", "corpus", filepath.FromSlash(tc.file))
		if _, err := os.Stat(in); err != nil {
			t.Skipf("corpus not checked out: %v", err)
		}
		var want *image.RGBA
		for _, mode := range []string{"stdlib", "encode", "stream"} {
			name := fmt.Sprintf("%s, transparent %v, %s", tc.file, tc.transparent, mode)
			out := filepath.Join(t.TempDir(), "p-%d.png")
			if err := run(in, cera.OpenOptions{}, 150, tc.page, out, tc.transparent, time.Minute, false, 0, cera.AnnotsView, cera.ImageNearest, nil, mode); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			got := decode(t, fmt.Sprintf(out, tc.page))
			if want == nil {
				want = got
				continue
			}
			if got.Rect != want.Rect || !slices.Equal(got.Pix, want.Pix) {
				t.Errorf("%s: pixels differ from image/png's", name)
			}
		}
	}
}

func decode(t *testing.T, name string) *image.RGBA {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := stdpng.Decode(f)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	rgba := image.NewRGBA(m.Bounds())
	draw.Draw(rgba, rgba.Rect, m, m.Bounds().Min, draw.Src)
	return rgba
}
