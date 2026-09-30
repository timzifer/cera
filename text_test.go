package cera

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/go-opentype/fonts/arimo"
	"github.com/timzifer/stilus"
)

const helvetica = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"

// textPDF builds a page with font F1 (object 100) and the given content.
func textPDF(content, font string, objs ...string) []byte {
	return buildPDF([]string{content}, "/Resources << /Font << /F1 100 0 R >> >>", append([]string{font}, objs...)...)
}

// inked counts the pixels of r that are not white.
func inked(img *image.RGBA, r image.Rectangle) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if img.RGBAAt(x, y) != white {
				n++
			}
		}
	}
	return n
}

func TestTextStandardFont(t *testing.T) {
	img, st, err := renderPage(t, textPDF("BT /F1 40 Tf 10 30 Td (HI) Tj ET", helvetica), 0, RenderOptions{Background: white})
	if err != nil {
		t.Fatal(err)
	}
	if st.Glyphs != 2 || st.Errors != 0 || len(st.Unsupported) != 0 {
		t.Errorf("stats %+v", st)
	}
	// H at x 10..~39, baseline at device y 70, cap height ~29 pt: its left
	// stem is ink, the middle of the letter above the bar is not.
	assertPixel(t, img, 15, 50, color.RGBA{0, 0, 0, 255})
	if n := inked(img, image.Rect(0, 0, 200, 38)); n != 0 {
		t.Errorf("%d inked pixels above the text", n)
	}
	if n := inked(img, image.Rect(10, 40, 80, 70)); n < 300 {
		t.Errorf("only %d inked pixels in the text", n)
	}
}

func TestTextEmbeddedTrueType(t *testing.T) {
	font := "<< /Type /Font /Subtype /TrueType /BaseFont /ABCDEF+Arimo /FirstChar 65 /LastChar 65 /Widths [667] /FontDescriptor 101 0 R >>"
	desc := "<< /Type /FontDescriptor /FontName /ABCDEF+Arimo /Flags 32 /FontFile2 102 0 R >>"
	prog := fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(arimo.TTF), arimo.TTF)
	img, st, err := renderPage(t, textPDF("BT /F1 60 Tf 20 20 Td (A) Tj ET", font, desc, prog), 0, RenderOptions{Background: white})
	if err != nil {
		t.Fatal(err)
	}
	if st.Glyphs != 1 || len(st.Unsupported) != 0 {
		t.Errorf("stats %+v", st)
	}
	// The apex of the A is at about x 20+20, y 80-43.
	if n := inked(img, image.Rect(20, 35, 62, 80)); n < 300 {
		t.Errorf("only %d inked pixels", n)
	}
	assertPixel(t, img, 40, 70, white) // the counter below the bar... is open
}

func TestTextPositioning(t *testing.T) {
	c := `BT /F1 10 Tf 12 TL 10 80 Td (Hello) Tj [(W) 120 (orld)] TJ T* [(big) -400 (gap)] TJ 0 -12 Td 2 Tc 5 Tw (a b) Tj ET`
	doc, _ := Open(textPDF(c, helvetica))
	p, _ := doc.Page(0)
	txt, err := p.Text(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := txt.String(), "HelloWorld\nbig gap\na b"; got != want {
		t.Errorf("text %q, want %q", got, want)
	}
	// "Hello" is 22.78 pt wide in Helvetica 10; W follows it.
	w := txt.Chars[5]
	if w.Text != "W" || w.Origin[0] < 32.7 || w.Origin[0] > 32.9 || w.Origin[1] != 20 {
		t.Errorf("W at %v", w.Origin)
	}
	// The TJ adjustment moves "orld" 1.2 pt left.
	o := txt.Chars[6]
	if want := w.Origin[0] + 9.44 - 1.2; o.Origin[0] < want-0.05 || o.Origin[0] > want+0.05 {
		t.Errorf("o at %v, want %v", o.Origin[0], want)
	}
	// Character and word spacing: a (5.56) + 2, space (2.78) + 2 + 5.
	a, sp, b := txt.Chars[len(txt.Chars)-3], txt.Chars[len(txt.Chars)-2], txt.Chars[len(txt.Chars)-1]
	if d := sp.Origin[0] - a.Origin[0]; d < 7.5 || d > 7.6 {
		t.Errorf("a advances %v", d)
	}
	if d := b.Origin[0] - sp.Origin[0]; d < 9.7 || d > 9.8 {
		t.Errorf("space advances %v", d)
	}
}

func TestTextRenderModes(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	for _, c := range []struct {
		mode  int
		stem  color.RGBA // inside the stem of the I
		clear bool       // outside the glyph stays white
	}{
		{0, color.RGBA{0, 0, 0, 255}, true},
		{3, white, true},
		{7, red, true}, // clip only: the red rectangle shows through the I
		{4, red, true},
	} {
		content := fmt.Sprintf("BT %d Tr /F1 80 Tf 40 20 Td (I) Tj ET 1 0 0 rg 0 0 200 100 re f", c.mode)
		if c.mode == 0 || c.mode == 3 {
			content = fmt.Sprintf("BT %d Tr /F1 80 Tf 40 20 Td (I) Tj ET", c.mode)
		}
		img, _, err := renderPage(t, textPDF(content, helvetica), 0, RenderOptions{Background: white})
		if err != nil {
			t.Fatal(err)
		}
		// The I of Helvetica 80 is a stem from x 40+7.4 to 40+15.6, y up to 57.
		if got := img.RGBAAt(51, 50); got != c.stem {
			t.Errorf("mode %d: stem %v, want %v", c.mode, got, c.stem)
		}
		if got := img.RGBAAt(100, 50); c.clear && got != white {
			t.Errorf("mode %d: outside %v", c.mode, got)
		}
	}

	// Stroked text draws the outline, not the inside.
	img, st, _ := renderPage(t, textPDF("BT 1 Tr 1 w /F1 80 Tf 40 20 Td (I) Tj ET", helvetica), 0, RenderOptions{Background: white})
	if st.Strokes != 1 {
		t.Errorf("stats %+v", st)
	}
	assertPixel(t, img, 51, 50, white)
	if n := inked(img, image.Rect(45, 20, 58, 80)); n < 100 {
		t.Errorf("only %d inked pixels", n)
	}
}

func TestTextClipEmpty(t *testing.T) {
	// A clipping text object that shows only spaces clips everything.
	img, _, err := renderPage(t, textPDF("BT 7 Tr /F1 20 Tf 10 10 Td ( ) Tj ET 1 0 0 rg 0 0 200 100 re f", helvetica), 0, RenderOptions{Background: white})
	if err != nil {
		t.Fatal(err)
	}
	if n := inked(img, img.Bounds()); n != 0 {
		t.Errorf("%d pixels painted", n)
	}
}

func TestType3(t *testing.T) {
	font := "<< /Type /Font /Subtype /Type3 /FontBBox [0 0 1000 1000] /FontMatrix [0.001 0 0 0.001 0 0] " +
		"/CharProcs << /sq 101 0 R /gr 102 0 R >> /Encoding << /Differences [65 /sq /gr] >> " +
		"/FirstChar 65 /LastChar 66 /Widths [1000 1000] >>"
	sq := "1000 0 0 0 1000 1000 d1 0 1 0 rg 0 0 1000 1000 re f" // uncoloured: rg ignored
	gr := "1000 0 d0 0 1 0 rg 0 0 500 1000 re f"                // coloured
	stream := func(s string) string { return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(s), s) }
	data := textPDF("BT 0 0 1 rg /F1 20 Tf 10 10 Td (AB) Tj ET", font, stream(sq), stream(gr))
	img, st, err := renderPage(t, data, 0, RenderOptions{Background: white})
	if err != nil {
		t.Fatal(err)
	}
	if st.Glyphs != 2 {
		t.Errorf("stats %+v", st)
	}
	assertPixel(t, img, 20, 80, color.RGBA{0, 0, 255, 255}) // A: 10..30
	assertPixel(t, img, 35, 80, color.RGBA{0, 255, 0, 255}) // B: 30..40 green
	assertPixel(t, img, 45, 80, white)

	// A glyph that shows its own font is refused, not recursed into.
	loop := "1000 0 d0 BT /F1 1 Tf (A) Tj ET"
	_, st, err = renderPage(t, textPDF("BT /F1 20 Tf (A) Tj ET", font, stream(loop), stream(gr)), 0, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Glyphs != 1 {
		t.Errorf("recursive glyph: stats %+v", st)
	}
}

// TestGlyphCacheMatchesPaths compares cached glyph masks with the same
// glyphs filled as paths.
func TestGlyphCacheMatchesPaths(t *testing.T) {
	data := textPDF("BT /F1 11 Tf 3 80 Td (The quick brown fox jumps over) Tj 0 -30 Td 0.3 Tc (the lazy dog 0123456789) Tj ET", helvetica)
	doc, _ := Open(data)
	p, _ := doc.Page(0)
	for _, scale := range []float64{1, 2.5} {
		cached := image.NewRGBA(p.Bounds(scale))
		if err := p.Render(context.Background(), cached, RenderOptions{Scale: scale, Background: white, Workers: 1}); err != nil {
			t.Fatal(err)
		}
		// The same glyphs as fills of their outlines.
		direct := image.NewRGBA(p.Bounds(scale))
		fillRegion(direct, direct.Rect, white)
		dev := &pathGlyphs{}
		dev.C = stilus.NewCanvas(direct)
		if err := p.Run(context.Background(), dev, scale, nil); err != nil {
			t.Fatal(err)
		}
		var diff, maxd int
		for i := range cached.Pix {
			d := int(cached.Pix[i]) - int(direct.Pix[i])
			if d < 0 {
				d = -d
			}
			diff += d
			maxd = max(maxd, d)
		}
		// Quantizing the origin to a quarter pixel moves edges by at most
		// an eighth of a pixel.
		if maxd > 72 || diff > len(cached.Pix)*2 {
			t.Errorf("scale %v: max difference %d, total %d", scale, maxd, diff)
		}
	}
}

// pathGlyphs fills glyphs as paths.
type pathGlyphs struct{ RasterDevice }

func (d *pathGlyphs) FillGlyphs(run *GlyphRun, paint *Paint) {
	for i := range run.Glyphs {
		if g := run.Glyphs[i]; g.Outline != nil {
			d.C.Fill(g.Outline, g.M, NonZero, paint)
		}
	}
}

func TestTextSteadyStateAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("sync.Pool drops items under the race detector")
	}
	var b strings.Builder
	b.WriteString("BT /F1 8 Tf 10 TL 5 95 Td ")
	for range 9 {
		b.WriteString("(Pack my box with five dozen liquor jugs) ' ")
	}
	b.WriteString("ET")
	doc, _ := Open(textPDF(b.String(), helvetica))
	p, _ := doc.Page(0)
	dst := image.NewRGBA(p.Bounds(2))
	opt := RenderOptions{Scale: 2, Workers: 1}
	if err := p.Render(context.Background(), dst, opt); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(20, func() {
		if err := p.Render(context.Background(), dst, opt); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 1 {
		t.Errorf("%v allocations per cached render", allocs)
	}
}

func BenchmarkRenderText(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("BT /F1 3 Tf 3.6 TL 2 98 Td ")
	for range 27 {
		sb.WriteString("(The quick brown fox jumps over the lazy dog. Pack my box with five dozen liquor jugs.) ' ")
	}
	sb.WriteString("ET")
	doc, _ := Open(textPDF(sb.String(), helvetica))
	p, _ := doc.Page(0)
	dst := image.NewRGBA(p.Bounds(4))
	b.Run("cached", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if err := p.Render(context.Background(), dst, RenderOptions{Scale: 4, Workers: 1}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("record", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			p.Release()
			if err := p.Render(context.Background(), dst, RenderOptions{Scale: 4, Workers: 1}); err != nil {
				b.Fatal(err)
			}
		}
	})
}
