package cera

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/go-opentype/fonts/arimo"
	"github.com/timzifer/cera/internal/testpdf"
)

// TestFonts lists the fonts the pages show text in: embedded or not,
// subset or not, composite and Type 3, in hidden layers too, each with its
// pages.
func TestFonts(t *testing.T) {
	widths := strings.TrimSpace(strings.Repeat("600 ", 95))
	src := testpdf.File{Objs: map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R /OCProperties <</OCGs [40 0 R] /D <</OFF [40 0 R]>>>>>>",
		2: "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 300 200]" +
			" /Resources <</Font <</A 10 0 R /H 20 0 R /C 30 0 R /T 35 0 R /U 21 0 R>> /Properties <</L 40 0 R>>>>>>",
		3:  "<</Type /Page /Parent 2 0 R /Contents 5 0 R>>",
		4:  "<</Type /Page /Parent 2 0 R /Contents 6 0 R>>",
		5:  testpdf.Stream("", []byte("BT /A 12 Tf 10 100 Td (Arimo) Tj /H 12 Tf (Helvetica) Tj ET /L BDC BT /C 12 Tf <0024> Tj ET EMC")),
		6:  testpdf.Stream("", []byte("BT /H 12 Tf 10 100 Td (again) Tj /T 12 Tf (a) Tj ET")),
		10: "<</Type /Font /Subtype /TrueType /BaseFont /ABCDEF+Arimo /FirstChar 32 /LastChar 126 /Widths [" + widths + "] /Encoding /WinAnsiEncoding /FontDescriptor 11 0 R>>",
		11: "<</Type /FontDescriptor /FontName /ABCDEF+Arimo /Flags 32 /FontBBox [0 -200 1000 900] /ItalicAngle 0 /Ascent 900 /Descent -200 /CapHeight 700 /StemV 80 /FontFile2 12 0 R>>",
		12: testpdf.Stream("/Length1 "+strconv.Itoa(len(arimo.TTF)), arimo.TTF),
		20: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica>>",
		21: "<</Type /Font /Subtype /Type1 /BaseFont /Unused>>",
		30: "<</Type /Font /Subtype /Type0 /BaseFont /Arimo /Encoding /Identity-H /DescendantFonts [31 0 R]>>",
		31: "<</Type /Font /Subtype /CIDFontType2 /BaseFont /Arimo /CIDSystemInfo <</Registry (Adobe) /Ordering (Identity) /Supplement 0>> /FontDescriptor 11 0 R /CIDToGIDMap /Identity>>",
		35: "<</Type /Font /Subtype /Type3 /FontBBox [0 0 1 1] /FontMatrix [1 0 0 1 0 0] /CharProcs <</a 36 0 R>> /Encoding <</Differences [97 /a]>> /FirstChar 97 /LastChar 97 /Widths [1] /Resources <<>>>>",
		36: testpdf.Stream("", []byte("1 0 d0 0 0 1 1 re f")),
		40: "<</Type /OCG /Name (Off)>>",
	}, Trailer: "/Root 1 0 R"}.Bytes()
	doc := editOpen(t, src)
	fonts, err := doc.Fonts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fonts {
		s := f.Name() + " " + f.Type()
		if f.Subset() {
			s += " subset"
		}
		if f.Embedded() {
			s += " embedded"
		}
		if f.DrawnWith() != "" {
			s += " stand-in"
		}
		got = append(got, s+" "+strconv.Itoa(len(f.Pages))+" "+pagesOf(f.Pages))
	}
	want := []string{
		"Arimo TrueType subset embedded 1 0",
		"Helvetica Type1 stand-in 2 0 1",
		"Arimo Type0/CIDFontType2 embedded 1 0",
		" Type3 embedded 1 1",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("fonts:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if n := fonts[1].DrawnWith(); !strings.Contains(n, "Heros") && !strings.Contains(n, "Arimo") {
		t.Errorf("Helvetica is drawn with %q", n)
	}
}

func pagesOf(p []int) string {
	s := make([]string, len(p))
	for i, v := range p {
		s[i] = strconv.Itoa(v)
	}
	return strings.Join(s, " ")
}
