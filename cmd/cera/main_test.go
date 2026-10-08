package main

import (
	"bytes"
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
	stripes := filepath.Join(t.TempDir(), "stripes.pdf")
	if err := os.WriteFile(stripes, stripesPDF(), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		file        string
		page        int
		transparent bool
	}{
		{stripes, 1, false},
		{stripes, 1, true},
		{"pdfjs/tracemonkey.pdf", 1, false},
		{"pdfjs/tracemonkey.pdf", 1, true},
		{"pdfjs/jbig2_symbol_offset.pdf", 1, false},
		{"pdfjs/transparency_group.pdf", 1, false},
		{"synthetic/contours-3000.pdf", 1, false},
	} {
		in := tc.file
		if !filepath.IsAbs(in) {
			in = filepath.Join("..", "..", "testdata", "corpus", filepath.FromSlash(tc.file))
			if _, err := os.Stat(in); err != nil {
				t.Logf("corpus not checked out: %v", err)
				continue
			}
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

// stripesPDF is a letter page of thin translucent stripes over a fill, which
// cera draws in bands.
func stripesPDF() []byte {
	var c bytes.Buffer
	c.WriteString("/H gs 0 0 1 rg 50 50 512 692 re f\n")
	for i := range 3000 {
		fmt.Fprintf(&c, "%d 0 0 rg 0 %g 612 0.12 re f\n", i%2, float64(i)*792/3000)
	}
	var b bytes.Buffer
	var offs []int
	obj := func(s string) {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), s)
	}
	b.WriteString("%PDF-1.7\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /ExtGState << /H << /ca 0.5 >> >> >> >>")
	obj(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", c.Len(), c.String()))
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	return b.Bytes()
}
