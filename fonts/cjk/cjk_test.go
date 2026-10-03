package cjk_test

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/timzifer/cera"
	"github.com/timzifer/cera/fonts/cjk"
	"github.com/timzifer/cera/fonts/cjk/japan1"
	"github.com/timzifer/cera/fonts/cjk/korea1"
)

// pdf builds a one-page document: 200 x 200 points, content c, font F1.
func pdf(c, font string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(c), c),
		font,
	}
	var b strings.Builder
	b.WriteString("%PDF-1.7\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return []byte(b.String())
}

// ucs2 writes s as a hex string of UTF-16 code units.
func ucs2(s string) string {
	var b strings.Builder
	b.WriteByte('<')
	for _, u := range utf16.Encode([]rune(s)) {
		fmt.Fprintf(&b, "%04X", u)
	}
	b.WriteByte('>')
	return b.String()
}

func font(cmap string) string {
	return `<< /Type /Font /Subtype /Type0 /BaseFont /KozMinPr6N-Regular /Encoding /` + cmap + `
		/DescendantFonts [<< /Type /Font /Subtype /CIDFontType0 /BaseFont /KozMinPr6N-Regular
		/CIDSystemInfo << /Registry (Adobe) /Ordering (Japan1) /Supplement 6 >>
		/FontDescriptor << /Type /FontDescriptor /FontName /KozMinPr6N-Regular /Flags 4 >> >>] >>`
}

func render(t *testing.T, data []byte, fonts cera.FontProvider) (*image.RGBA, cera.Stats, string) {
	t.Helper()
	doc, err := cera.OpenWith(data, cera.OpenOptions{Fonts: fonts})
	if err != nil {
		t.Fatal(err)
	}
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	var st cera.Stats
	img := image.NewRGBA(p.Bounds(1))
	err = p.Render(context.Background(), img, cera.RenderOptions{Background: color.RGBA{255, 255, 255, 255}, Stats: &st})
	if err != nil {
		t.Fatal(err)
	}
	text, err := p.Text(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return img, st, text.String()
}

func inked(img *image.RGBA, r image.Rectangle) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if c := img.RGBAAt(x, y); c.R < 128 {
				n++
			}
		}
	}
	return n
}

func TestJapaneseHorizontal(t *testing.T) {
	data := pdf("BT /F1 40 Tf 10 100 Td "+ucs2("日本語")+" Tj ET", font("UniJIS-UCS2-H"))
	img, st, text := render(t, data, cjk.Provider{japan1.Collection})
	if len(st.Unsupported) != 0 || st.Glyphs != 3 {
		t.Errorf("stats %+v", st)
	}
	if text != "日本語" {
		t.Errorf("text %q", text)
	}
	// Three glyphs of 40 points side by side on the baseline at y 100.
	for i := range 3 {
		r := image.Rect(10+40*i, 60, 50+40*i, 105)
		if n := inked(img, r); n < 50 {
			t.Errorf("glyph %d: %d inked pixels", i, n)
		}
	}
	// Without a provider: nothing drawn, counted by collection.
	img, st, text = render(t, data, nil)
	if st.Unsupported["font-missing-japan1"] != 1 || inked(img, img.Bounds()) != 0 {
		t.Errorf("without provider: %+v", st)
	}
	if text != "日本語" {
		t.Errorf("text without provider %q", text)
	}
}

func TestJapaneseVertical(t *testing.T) {
	data := pdf("BT /F1 40 Tf 80 180 Td "+ucs2("縦書き。")+" Tj ET", font("UniJIS-UCS2-V"))
	img, st, text := render(t, data, cjk.Provider{japan1.Collection})
	if len(st.Unsupported) != 0 || st.Glyphs != 4 {
		t.Errorf("stats %+v", st)
	}
	if text != "縦書き。" {
		t.Errorf("text %q", text)
	}
	// One column centred on x 80, from y 180 down an em per glyph; nothing
	// to the right of it.
	for i := range 4 {
		y := 20 + 40*i // top of the glyph in device space
		if n := inked(img, image.Rect(60, y, 100, y+40)); n < 10 {
			t.Errorf("glyph %d: %d inked pixels", i, n)
		}
	}
	if n := inked(img, image.Rect(105, 0, 200, 200)); n != 0 {
		t.Errorf("%d inked pixels right of the column", n)
	}
	// The ideographic full stop in its vertical form sits in the upper
	// right of its em, not the lower left as horizontally.
	if inked(img, image.Rect(80, 140, 100, 160)) == 0 || inked(img, image.Rect(60, 160, 80, 180)) != 0 {
		t.Error("。 is not in its vertical form")
	}
}

func TestProviderChoice(t *testing.T) {
	p := cjk.Provider{japan1.Collection, korea1.Collection}
	for _, tc := range []struct {
		req  cera.FontRequest
		want []byte
	}{
		{cera.FontRequest{Name: "X", Ordering: "Korea1"}, korea1.Collection.Program},
		{cera.FontRequest{Name: "Batang", Ordering: "Identity"}, korea1.Collection.Program},
		{cera.FontRequest{Name: "Unknown", Ordering: "Identity"}, japan1.Collection.Program},
		{cera.FontRequest{Name: "X", Ordering: "GB1"}, nil},
		{cera.FontRequest{Name: "Arial"}, nil},
	} {
		got, ok := p.Font(tc.req)
		if ok != (tc.want != nil) || (ok && &got[0] != &tc.want[0]) {
			t.Errorf("%+v: ok %v", tc.req, ok)
		}
	}
}
