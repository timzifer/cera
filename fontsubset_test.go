package cera

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/go-opentype/fonts/arimo"
	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// manyGlyphCFF builds a bare CFF program of n glyphs (ISOAdobe charset,
// standard encoding): glyph i a square of 100 + 7i units, so that each
// has an outline of its own.
func manyGlyphCFF(n int) []byte {
	num := func(v int) []byte { return []byte{28, byte(v >> 8), byte(v)} }
	off := func(v int) []byte { return []byte{29, byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }
	index := func(objs ...[]byte) []byte {
		if len(objs) == 0 {
			return []byte{0, 0}
		}
		b := []byte{byte(len(objs) >> 8), byte(len(objs)), 4}
		o := 1
		put := func(v int) { b = append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v)) }
		put(o)
		for _, x := range objs {
			o += len(x)
			put(o)
		}
		for _, x := range objs {
			b = append(b, x...)
		}
		return b
	}
	glyphs := [][]byte{{14}}
	for i := 1; i < n; i++ {
		s := 100 + 7*i
		var g []byte
		for _, v := range []int{600, 50, 0} { // width, rmoveto
			g = append(g, num(v)...)
		}
		g = append(g, 21)
		for _, v := range []int{s, 0, 0, s, -s, 0} {
			g = append(g, num(v)...)
		}
		glyphs = append(glyphs, append(g, 5, 14))
	}
	charStrings := index(glyphs...)
	private := []byte{139, 21}
	top := func(csAt, privAt int) []byte {
		t := append(off(csAt), 17)
		return append(append(append(t, off(len(private))...), off(privAt)...), 18)
	}
	build := func(csAt, privAt int) ([]byte, int, int) {
		var b bytes.Buffer
		b.Write([]byte{1, 0, 4, 4})
		b.Write(index([]byte("Squares")))
		b.Write(index(top(csAt, privAt)))
		b.Write(index())
		b.Write(index())
		cs := b.Len()
		b.Write(charStrings)
		priv := b.Len()
		b.Write(private)
		return b.Bytes(), cs, priv
	}
	_, cs, priv := build(0, 0)
	b, _, _ := build(cs, priv)
	return b
}

// fontFile is a page that shows content with the font F1 (object 10),
// whose descriptor is object 11 and program object 12; extra are more
// objects, form the /AcroForm of the catalogue.
func fontFile(content, font, descriptor, program, form string, extra map[int]string) []byte {
	cat := "<</Type /Catalog /Pages 2 0 R"
	if form != "" {
		cat += " /AcroForm " + form
	}
	objs := map[int]string{
		1:  cat + ">>",
		2:  "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200]>>",
		3:  "<</Type /Page /Parent 2 0 R /Contents 4 0 R /Resources <</Font <</F1 10 0 R>>>>>>",
		4:  testpdf.Stream("", []byte(content)),
		10: font,
		11: descriptor,
		12: program,
	}
	for k, v := range extra {
		objs[k] = v
	}
	return testpdf.File{Objs: objs, Trailer: "/Root 1 0 R"}.Bytes()
}

// programSize returns the stored size of the program of F1 in d, and the
// names of the font and its descriptor.
func programSize(t *testing.T, d *Document) (int, string, string, pdf.Dict) {
	t.Helper()
	pd, _ := d.r.Page(1)
	f := d.dict(d.dict(d.dict(pd.Get("Resources")).Get("Font")).Get("F1"))
	base, _ := d.name(f.Get("BaseFont"))
	fd := f
	if a, _ := d.resolve(f.Get("DescendantFonts")).Array(); len(a) > 0 {
		fd = d.dict(a[0])
	}
	desc := d.dict(fd.Get("FontDescriptor"))
	name, _ := d.name(desc.Get("FontName"))
	for _, k := range []pdf.Name{"FontFile2", "FontFile3"} {
		if s, ok := d.resolve(desc.Get(k)).Stream(); ok {
			return len(d.r.Raw(s)), string(base), string(name), desc
		}
	}
	t.Fatal("no program")
	return 0, "", "", pdf.Dict{}
}

func saveSubset(t *testing.T, src []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := editOpen(t, src).Edit().SaveWith(&out, SaveOptions{SubsetFonts: true}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// TestSubsetFonts subsets programs embedded whole: a simple TrueType
// font, a composite one with a /CIDSet, and a bare CFF program; what the
// page shows and reads stays.
func TestSubsetFonts(t *testing.T) {
	ttf := testpdf.Stream("/Length1 "+strconv.Itoa(len(arimo.TTF)), arimo.TTF)
	widths := strings.TrimSpace(strings.Repeat("600 ", 95))
	cff := manyGlyphCFF(120)
	for _, tt := range []struct {
		name                      string
		content, font, desc, prog string
		extra                     map[int]string
		smallerThan               float64 // share of the program left at most
	}{
		{"TrueType", "BT /F1 30 Tf 10 120 Td (Hello, World) Tj ET",
			"<</Type /Font /Subtype /TrueType /BaseFont /Arimo /FirstChar 32 /LastChar 126 /Widths [" + widths + "] /Encoding /WinAnsiEncoding /FontDescriptor 11 0 R>>",
			"<</Type /FontDescriptor /FontName /Arimo /Flags 32 /FontBBox [0 -200 1000 900] /ItalicAngle 0 /Ascent 900 /Descent -200 /CapHeight 700 /StemV 80 /FontFile2 12 0 R>>",
			ttf, nil, 0.2},
		{"composite", "BT /F1 30 Tf 10 120 Td <0024002500260027> Tj ET",
			"<</Type /Font /Subtype /Type0 /BaseFont /Arimo /Encoding /Identity-H /DescendantFonts [13 0 R]>>",
			"<</Type /FontDescriptor /FontName /Arimo /Flags 32 /FontBBox [0 -200 1000 900] /ItalicAngle 0 /Ascent 900 /Descent -200 /CapHeight 700 /StemV 80 /FontFile2 12 0 R /CIDSet 14 0 R>>",
			ttf, map[int]string{
				13: "<</Type /Font /Subtype /CIDFontType2 /BaseFont /Arimo /CIDSystemInfo <</Registry (Adobe) /Ordering (Identity) /Supplement 0>> /FontDescriptor 11 0 R /DW 600 /CIDToGIDMap /Identity>>",
				14: testpdf.Stream("", bytes.Repeat([]byte{0xff}, 128)),
			}, 0.2},
		{"CFF", "BT /F1 30 Tf 10 120 Td (ABC) Tj ET",
			"<</Type /Font /Subtype /Type1 /BaseFont /Squares /FirstChar 32 /LastChar 126 /Widths [" + widths + "] /FontDescriptor 11 0 R>>",
			"<</Type /FontDescriptor /FontName /Squares /Flags 32 /FontBBox [0 0 1000 1000] /ItalicAngle 0 /Ascent 900 /Descent -200 /CapHeight 700 /StemV 80 /FontFile3 12 0 R>>",
			testpdf.Stream("/Subtype /Type1C", cff), nil, 0.6},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := fontFile(tt.content, tt.font, tt.desc, tt.prog, "", tt.extra)
			doc := editOpen(t, src)
			before, _, _, _ := programSize(t, doc)
			od := editOpen(t, saveSubset(t, src))
			after, base, name, desc := programSize(t, od)
			if float64(after) > tt.smallerThan*float64(before) {
				t.Errorf("the program is %d bytes, was %d", after, before)
			}
			want := "Arimo"
			if tt.name == "CFF" {
				want = "Squares"
			}
			if !isTagged(base) || !isTagged(name) || base[7:] != want || name[7:] != want {
				t.Errorf("names %q, %q", base, name)
			}
			if desc.Has("CIDSet") {
				t.Error("/CIDSet stays")
			}
			p0, _ := doc.Page(0)
			p1, _ := od.Page(0)
			if n, most := pixDiff(renderAt(t, p0, 2), renderAt(t, p1, 2)); n != 0 {
				t.Errorf("%d pixels differ, by up to %d", n, most)
			}
			if a, b := textOf(t, doc, 0).String(), textOf(t, od, 0).String(); a != b {
				t.Errorf("the text reads %q, was %q", b, a)
			}
		})
	}
}

// TestSubsetFontsWhole keeps programs whole that the form uses, and
// counts the glyphs of hidden layers and of annotations.
func TestSubsetFontsWhole(t *testing.T) {
	ttf := testpdf.Stream("/Length1 "+strconv.Itoa(len(arimo.TTF)), arimo.TTF)
	widths := strings.TrimSpace(strings.Repeat("600 ", 95))
	font := "<</Type /Font /Subtype /TrueType /BaseFont /Arimo /FirstChar 32 /LastChar 126 /Widths [" + widths + "] /Encoding /WinAnsiEncoding /FontDescriptor 11 0 R>>"
	desc := "<</Type /FontDescriptor /FontName /Arimo /Flags 32 /FontBBox [0 -200 1000 900] /ItalicAngle 0 /Ascent 900 /Descent -200 /CapHeight 700 /StemV 80 /FontFile2 12 0 R>>"

	// The font of the form's /DR.
	src := fontFile("BT /F1 30 Tf 10 120 Td (Form) Tj ET", font, desc, ttf, "<</Fields [] /DR <</Font <</F1 10 0 R>>>> /DA (/F1 0 Tf 0 g)>>", nil)
	before, _, _, _ := programSize(t, editOpen(t, src))
	after, base, _, _ := programSize(t, editOpen(t, saveSubset(t, src)))
	if after != before || isTagged(base) {
		t.Errorf("the form's font: %d bytes, was %d, %q", after, before, base)
	}

	// Glyphs of a hidden layer and of an annotation's appearance.
	src = fontFile("/OC /L BDC BT /F1 30 Tf 10 120 Td (Hidden) Tj ET EMC", font, desc, ttf, "", map[int]string{
		20: "<</Type /OCG /Name (Off)>>",
		21: "<</Type /Annot /Subtype /FreeText /Rect [10 10 200 60] /AP <</N 22 0 R>>>>",
		22: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 190 50] /Resources <</Font <</F1 10 0 R>>>>", []byte("BT /F1 20 Tf 5 15 Td (Note) Tj ET")),
	})
	src = bytes.Replace(src, []byte("/Type /Catalog /Pages 2 0 R"), []byte("/Type /Catalog /Pages 2 0 R /OCProperties <</OCGs [20 0 R] /D <</OFF [20 0 R]>>>>"), 1)
	src = bytes.Replace(src, []byte("/Resources <</Font <</F1 10 0 R>>>>>>"), []byte("/Resources <</Font <</F1 10 0 R>> /Properties <</L 20 0 R>>>> /Annots [21 0 R]>>"), 1)
	doc := editOpen(t, src)
	od := editOpen(t, saveSubset(t, src))
	if _, base, _, _ := programSize(t, od); !isTagged(base) {
		t.Fatalf("not subset: %q", base)
	}
	for _, vis := range []*Visibility{{}, nil} {
		p0, _ := doc.Page(0)
		p1, _ := od.Page(0)
		a, b := renderWithLayers(t, p0, vis), renderWithLayers(t, p1, vis)
		if n, most := pixDiff(a, b); n != 0 {
			t.Errorf("layers %v: %d pixels differ, by up to %d", vis, n, most)
		}
	}
}
