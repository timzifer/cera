package cera

import (
	"context"
	"fmt"
	"image"
	"maps"
	"math"
	"slices"
	"sync"
	"testing"

	"github.com/go-opentype/fonts/arimo"
	"github.com/go-opentype/fonts/cousine"
	"github.com/go-opentype/opentype"
	"github.com/go-pdfkit/reader"

	"github.com/timzifer/cera/internal/stdfont"
)

// pageText opens data with a font provider and extracts its text.
func pageText(t *testing.T, data []byte, fonts FontProvider) *PageText {
	t.Helper()
	doc, err := OpenWith(data, OpenOptions{Fonts: fonts})
	if err != nil {
		t.Fatal(err)
	}
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	text, err := p.Text(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// renderWith renders page 0 of data with a font provider.
func renderWith(t *testing.T, data []byte, fonts FontProvider) (*image.RGBA, Stats) {
	t.Helper()
	doc, err := OpenWith(data, OpenOptions{Fonts: fonts})
	if err != nil {
		t.Fatal(err)
	}
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	var st Stats
	img := image.NewRGBA(p.Bounds(1))
	if err := p.Render(context.Background(), img, RenderOptions{Background: white, Stats: &st}); err != nil {
		t.Fatal(err)
	}
	return img, st
}

func approx(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestSymbolStandIn(t *testing.T) {
	data := textPDF("BT /F1 40 Tf 10 30 Td (abp) Tj ET", "<< /Type /Font /Subtype /Type1 /BaseFont /Symbol >>")
	img, st, err := renderPage(t, data, 0, RenderOptions{Background: white})
	if err != nil {
		t.Fatal(err)
	}
	if st.Glyphs != 3 || len(st.Unsupported) != 0 {
		t.Errorf("stats %+v", st)
	}
	// Each glyph is drawn, to its AFM width: alpha 631, beta 549.
	for i, x := range []float64{10, 10 + 0.631*40, 10 + (0.631+0.549)*40} {
		r := image.Rect(int(x)+2, 40, int(x)+20, 75)
		if inked(img, r) == 0 {
			t.Errorf("glyph %d not drawn", i)
		}
	}
	text := pageText(t, data, nil)
	if s := text.String(); s != "αβπ" {
		t.Errorf("text %q", s)
	}
	if got := text.Chars[1].Origin[0]; !approx(got, 10+0.631*40) {
		t.Errorf("beta at x %v", got)
	}
}

func TestSymbolDifferences(t *testing.T) {
	// The document renames code 65; the rest keeps the built-in encoding.
	font := "<< /Type /Font /Subtype /Type1 /BaseFont /Symbol /Encoding << /Differences [65 /Omega] >> >>"
	data := textPDF("BT /F1 40 Tf 10 30 Td (AD) Tj ET", font)
	if s := pageText(t, data, nil).String(); s != "ΩΔ" {
		t.Errorf("text %q", s)
	}
	img, _, _ := renderPage(t, data, 0, RenderOptions{Background: white})
	if inked(img, image.Rect(10, 40, 70, 75)) == 0 {
		t.Error("nothing drawn")
	}
}

func TestZapfDingbatsStandIn(t *testing.T) {
	font := "<< /Type /Font /Subtype /Type1 /BaseFont /ZapfDingbats >>"
	data := textPDF("BT /F1 40 Tf 10 30 Td (4l) Tj ET", font)
	img, st, err := renderPage(t, data, 0, RenderOptions{Background: white})
	if err != nil {
		t.Fatal(err)
	}
	if st.Glyphs != 2 || len(st.Unsupported) != 0 {
		t.Errorf("stats %+v", st)
	}
	if inked(img, image.Rect(10, 40, 40, 75)) == 0 || inked(img, image.Rect(45, 40, 75, 75)) == 0 {
		t.Error("glyphs not drawn")
	}
	text := pageText(t, data, nil)
	if s := text.String(); s != "✔●" {
		t.Errorf("text %q", s)
	}
	// a20 (the check) is 846 wide.
	if got := text.Chars[1].Origin[0]; !approx(got, 10+0.846*40) {
		t.Errorf("second glyph at x %v", got)
	}
}

func TestClassOf(t *testing.T) {
	for _, tc := range []struct {
		req     FontRequest
		fam     *family
		stretch float64
	}{
		{FontRequest{Name: "Arial-BoldMT"}, &sansFamily, 0},
		{FontRequest{Name: "ArialNarrow,Bold"}, &sansFamily, 0.82},
		{FontRequest{Name: "TimesNewRomanPS-BoldItalicMT"}, &serifFamily, 0},
		{FontRequest{Name: "CourierNewPSMT"}, &courierFamily, 0},
		{FontRequest{Name: "Courier-BoldOblique"}, &courierFamily, 0},
		{FontRequest{Name: "LucidaSansTypewriter"}, &monoFamily, 0},
		{FontRequest{Name: "Consolas"}, &monoFamily, 0},
		{FontRequest{Name: "Georgia,Italic"}, &serifFamily, 0},
		{FontRequest{Name: "Calibri-Light"}, &sansFamily, 0.9},
		{FontRequest{Name: "HelveticaNeueLTStd-Roman"}, &sansFamily, 0},
		{FontRequest{Name: "Verdana"}, &sansFamily, 0},
		{FontRequest{Name: "Cambria"}, &serifFamily, 0},
		{FontRequest{Name: "UniversCondensed"}, &sansFamily, 0.82},
		{FontRequest{Name: "FooBar", Serif: true}, &serifFamily, 0},
		{FontRequest{Name: "FooBar", FixedPitch: true}, &courierFamily, 0},
		{FontRequest{Name: "FooMono", FixedPitch: true}, &monoFamily, 0},
		{FontRequest{Name: "FooBar"}, &sansFamily, 0},
	} {
		c := classOf(tc.req)
		if c.fam != tc.fam || c.stretch != tc.stretch {
			t.Errorf("%s: family %p stretch %v", tc.req.Name, c.fam, c.stretch)
		}
	}
}

// recorder is a FontProvider that hands out one program and remembers what
// it was asked for.
type recorder struct {
	mu   sync.Mutex
	ttf  []byte
	reqs []FontRequest
}

func (r *recorder) Font(req FontRequest) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	return r.ttf, r.ttf != nil
}

func TestFontRequest(t *testing.T) {
	font := `<< /Type /Font /Subtype /TrueType /BaseFont /ABCDEF+Frobnicate /FirstChar 72 /LastChar 73 /Widths [600 600]
		/FontDescriptor << /Type /FontDescriptor /FontName /Frobnicate /Flags 34 /FontWeight 700 /ItalicAngle -12 >> >>`
	p := &recorder{ttf: cousine.TTF}
	img, st := renderWith(t, textPDF("BT /F1 40 Tf 10 30 Td (HI) Tj ET", font), p)
	if len(p.reqs) != 1 {
		t.Fatalf("%d requests", len(p.reqs))
	}
	want := FontRequest{Name: "Frobnicate", Serif: true, Bold: true, Italic: true, Weight: 700}
	if p.reqs[0] != want {
		t.Errorf("request %+v, want %+v", p.reqs[0], want)
	}
	if st.Glyphs != 2 || len(st.Unsupported) != 0 || inked(img, image.Rect(10, 40, 60, 75)) == 0 {
		t.Errorf("stats %+v", st)
	}
	// A provider that has nothing: the built-in stand-ins.
	p = &recorder{}
	_, st = renderWith(t, textPDF("BT /F1 40 Tf 10 30 Td (HI) Tj ET", font), p)
	if len(p.reqs) != 1 || st.Glyphs != 2 || len(st.Unsupported) != 0 {
		t.Errorf("without a program: %d requests, stats %+v", len(p.reqs), st)
	}
}

// cidFont is a composite font of Adobe-Japan1 with encoding enc (a name
// or a reference) and descendant extras, no program.
func cidFont(enc, extra string) string {
	return `<< /Type /Font /Subtype /Type0 /BaseFont /Ryumin-Light /Encoding ` + enc + `
		/DescendantFonts [<< /Type /Font /Subtype /CIDFontType0 /BaseFont /Ryumin-Light
		/CIDSystemInfo << /Registry (Adobe) /Ordering (Japan1) /Supplement 2 >> ` + extra + ` >>] >>`
}

func TestCompositeByUnicode(t *testing.T) {
	// UTF-16 codes through UniJIS-UCS2-H to Japan1 CIDs, drawn with a
	// Unicode-keyed program through what the CIDs stand for.
	data := textPDF("BT /F1 40 Tf 10 30 Td <00480049> Tj ET", cidFont("/UniJIS-UCS2-H", "/DW 1000"))
	p := &recorder{ttf: arimo.TTF}
	img, st := renderWith(t, data, p)
	if st.Glyphs != 2 || len(st.Unsupported) != 0 {
		t.Errorf("stats %+v", st)
	}
	if len(p.reqs) != 1 || p.reqs[0].Ordering != "Japan1" || p.reqs[0].Registry != "Adobe" || p.reqs[0].Supplement != 2 {
		t.Errorf("requests %+v", p.reqs)
	}
	// H at 10, I at 50 (DW 1000).
	if inked(img, image.Rect(10, 40, 40, 75)) == 0 || inked(img, image.Rect(50, 40, 70, 75)) == 0 {
		t.Error("glyphs not drawn")
	}
	if s := pageText(t, data, nil).String(); s != "HI" {
		t.Errorf("text %q", s)
	}
}

func TestEmbeddedCMap(t *testing.T) {
	// One-byte codes 41-5A for the proportional capitals of Japan1 (CIDs
	// 34-59), two-byte codes through UniJIS-UCS2-H.
	prog := `/CIDInit /ProcSet findresource begin 12 dict begin begincmap
/CIDSystemInfo << /Registry (Adobe) /Ordering (Japan1) /Supplement 2 >> def
/CMapName /Test-H def
2 begincodespacerange <00> <7F> <8000> <FFFF> endcodespacerange
1 begincidrange <41> <5A> 34 endcidrange
endcmap CMapName currentdict /CMap defineresource pop end end`
	cmapObj := fmt.Sprintf("<< /Type /CMap /CMapName /Test-H /Length %d >>\nstream\n%s\nendstream", len(prog), prog)
	data := textPDF("BT /F1 40 Tf 10 30 Td (H) Tj <8001> Tj (I) Tj ET", cidFont("101 0 R", "/DW 500"), cmapObj)
	text := pageText(t, data, nil)
	if s := text.String(); s != "H�I" {
		t.Errorf("text %q", s)
	}
	if len(text.Chars) == 3 && !approx(text.Chars[2].Origin[0], 10+2*20) {
		t.Errorf("I at %v", text.Chars[2].Origin[0])
	}
	_, st := renderWith(t, data, &recorder{ttf: arimo.TTF})
	if st.Glyphs != 3 || len(st.Unsupported) != 0 {
		t.Errorf("stats %+v", st)
	}
}

func TestVerticalMetrics(t *testing.T) {
	// Identity-V: CID 34 ('A') with default metrics, CID 35 ('B') with
	// its own: half an em down, origin 300 left and 700 up.
	font := cidFont("/Identity-V", "/W [34 35 600] /W2 [35 [-500 300 700]]")
	data := textPDF("BT /F1 10 Tf 100 90 Td 2 Tc <002200230022> Tj ET", font)
	text := pageText(t, data, nil)
	if len(text.Chars) != 3 {
		t.Fatalf("%d chars", len(text.Chars))
	}
	// Device y is down from the top (100). The pen starts at 10 and moves
	// by w1·size + Tc: -10 + 2, then -5 + 2.
	for i, y := range []float64{10, 18, 21} {
		if c := text.Chars[i]; !approx(c.Origin[0], 100) || !approx(c.Origin[1], y) {
			t.Errorf("char %d at %v, want (100, %v)", i, c.Origin, y)
		}
	}
	if s := text.String(); s != "ABA" {
		t.Errorf("text %q", s)
	}
	// Vertical text boxes are an em wide, centred on the pen, and reach
	// from the pen down by the advance.
	b := text.Chars[1].Box
	if !approx(b.X0, 100-5) || !approx(b.X1, 100+5) || !approx(b.Y0, 18) || !approx(b.Y1, 23) {
		t.Errorf("box %+v", b)
	}
	_, st := renderWith(t, data, &recorder{ttf: arimo.TTF})
	if st.Glyphs != 3 || len(st.Unsupported) != 0 {
		t.Errorf("stats %+v", st)
	}
}

func TestUnknownCMap(t *testing.T) {
	data := textPDF("BT /F1 10 Tf 10 50 Td <0022> Tj ET", cidFont("/No-Such-CMap", ""))
	_, st := renderWith(t, data, &recorder{ttf: arimo.TTF})
	if st.Unsupported["cmap-missing"] != 1 || st.Glyphs != 1 {
		t.Errorf("stats %+v", st)
	}
}

func TestEmbolden(t *testing.T) {
	sq := func(x0, y0, x1, y1 float64, ccw bool) []opentype.Segment {
		pts := []opentype.Point{{X: x0, Y: y0}, {X: x1, Y: y0}, {X: x1, Y: y1}, {X: x0, Y: y1}}
		if !ccw {
			pts[1], pts[3] = pts[3], pts[1]
		}
		s := []opentype.Segment{{Op: opentype.SegMoveTo, P: [2]opentype.Point{pts[0]}}}
		for _, p := range append(pts[1:], pts[0]) {
			s = append(s, opentype.Segment{Op: opentype.SegLineTo, P: [2]opentype.Point{p}})
		}
		return append(s, opentype.Segment{Op: opentype.SegClose})
	}
	// A ring: outer contour counterclockwise, counter clockwise the other way.
	for _, ccw := range []bool{true, false} {
		in := append(sq(0, 0, 100, 100, ccw), sq(30, 30, 70, 70, !ccw)...)
		orig := slices.Clone(in)
		out := embolden(in, 10)
		want := append(sq(-5, -5, 105, 105, ccw), sq(35, 35, 65, 65, !ccw)...)
		for i := range want {
			for j := range 2 {
				g, w := out[i].P[j], want[i].P[j]
				if math.Abs(g.X-w.X) > 1e-9 || math.Abs(g.Y-w.Y) > 1e-9 {
					t.Fatalf("ccw %v: segment %d point %d at %v, want %v", ccw, i, j, g, w)
				}
			}
		}
		if !slices.Equal(in, orig) {
			t.Fatal("input changed")
		}
	}
}

func TestStandardStandIns(t *testing.T) {
	d, err := Open(textPDF("", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"))
	if err != nil {
		t.Fatal(err)
	}
	load := func(name string, extra reader.Dict) *Font {
		dict := reader.Dict{"Type": reader.Name("Font"), "Subtype": reader.Name("Type1"), "BaseFont": reader.Name(name)}
		maps.Copy(dict, extra)
		return d.loadFont(dict)
	}
	for _, tc := range []struct {
		name  string
		fam   *family
		style int
		width float64 // of M, in em
	}{
		{"Helvetica", &sansFamily, 0, 0.833},
		{"Helvetica-BoldOblique", &sansFamily, 3, 0.833},
		{"Times-Roman", &serifFamily, 0, 0.889},
		{"Times-Italic", &serifFamily, 2, 0.833},
		{"Courier", &courierFamily, 0, 0.6},
		{"Courier-Bold", &courierFamily, 1, 0.6},
		{"Courier-Oblique", &courierFamily, 2, 0.6},
		{"Courier-BoldOblique", &courierFamily, 3, 0.6},
	} {
		if !stdfont.Gyre {
			tc.fam = map[*family]*family{&sansFamily: &arimoFamily, &serifFamily: &tinosFamily, &courierFamily: &monoFamily}[tc.fam]
		}
		f := load(tc.name, nil)
		if f.program != tc.fam[tc.style].get() {
			t.Errorf("%s: not drawn with its stand-in", tc.name)
			continue
		}
		if a := f.advance('M'); math.Abs(a-tc.width) > 0.001 {
			t.Errorf("%s: M advances %v em, want %v", tc.name, a, tc.width)
		}
		if want := tc.fam == &courierFamily && tc.style&1 == 0; (f.bolden > 0) != want {
			t.Errorf("%s: bolden %v", tc.name, f.bolden)
		}
	}
	// TeX Gyre has no Cyrillic: Arimo, Tinos and Cousine take over.
	cyrillic := reader.Dict{"Encoding": reader.Dict{"Differences": reader.Array{reader.Integer(192),
		reader.Name("afii10017"), reader.Name("afii10018"), reader.Name("afii10019")}}}
	for _, tc := range []struct {
		name string
		fam  *family
	}{{"Helvetica", &arimoFamily}, {"Times-Roman", &tinosFamily}, {"Courier", &monoFamily}} {
		if f := load(tc.name, cyrillic); f.program != tc.fam[0].get() {
			t.Errorf("Cyrillic %s: not drawn with the fallback", tc.name)
		}
	}
}
