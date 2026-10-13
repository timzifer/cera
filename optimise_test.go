package cera

import (
	"bytes"
	"compress/lzw"
	"encoding/ascii85"
	"encoding/hex"
	"fmt"
	"image/color"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// optObjs is a document of three pages that share one resource dictionary
// with everything any of them draws: the first page draws one half of it,
// the other two the other half. A form and a Type 3 font have the same
// dictionary, the patterns one of their own with an image they do not
// draw. /Mark tells the objects apart in what is written.
func optObjs() map[int]string {
	one := "BT /F1 12 Tf 20 170 Td (One) Tj /T3 20 Tf 60 0 Td (a) Tj ET\n" +
		"q 40 0 0 40 20 100 cm /Im1 Do Q\n" +
		"/Fm1 Do /Fm2 Do\n" +
		"/L1 BDC /G1 gs 0 0 1 rg 70 100 30 30 re f EMC\n" +
		"/Pattern cs /P1 scn 110 100 30 30 re f\n" +
		"q 150 100 30 30 re W n /S1 sh Q\n" +
		"q 10 0 0 10 150 20 cm BI /W 1 /H 1 /CS /Cs1 /BPC 1 ID \x00 EI Q\n"
	two := "BT /F2 12 Tf 20 170 Td (Two) Tj ET\n" +
		"q 40 0 0 40 20 100 cm /Im2 Do Q\n" +
		"/L2 BDC /G2 gs 0 1 0 rg 70 100 30 30 re f EMC\n" +
		"/Pattern cs /P2 scn 110 100 30 30 re f\n" +
		"q 150 100 30 30 re W n /S2 sh Q\n" +
		"/Cs2 cs 0.5 sc 20 20 30 30 re f\n"
	image := func(mark string, r, g, b byte) string {
		return testpdf.Stream("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Mark /"+mark,
			bytes.Repeat([]byte{r, g, b}, 4))
	}
	pattern := func(mark, paint string) string {
		return testpdf.Stream("/Type /Pattern /PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 4 4] /XStep 4 /YStep 4 /Resources <</XObject <</Im2 31 0 R>> /Font <</F1 11 0 R>>>> /Mark /"+mark,
			[]byte(paint+" 0 0 2 2 re f"))
	}
	shading := func(mark string, r, g, b float64) string {
		return fmt.Sprintf("<</ShadingType 2 /ColorSpace /DeviceRGB /Coords [150 100 180 130] /Mark /%s"+
			" /Function <</FunctionType 2 /Domain [0 1] /C0 [1 1 1] /C1 [%g %g %g] /N 1>>>>", mark, r, g, b)
	}
	return map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R /Metadata 60 0 R /PieceInfo <</App <</Private (CATPRIVATE)>>>>>>",
		2: "<</Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 /MediaBox [0 0 200 200] /Resources 10 0 R>>",
		3: "<</Type /Page /Parent 2 0 R /Contents 20 0 R /Annots [70 0 R] /Thumb 50 0 R /Metadata 61 0 R" +
			" /PieceInfo <</App <</Private (PAGEPRIVATE)>>>>>>",
		4: "<</Type /Page /Parent 2 0 R /Contents 21 0 R>>",
		5: "<</Type /Page /Parent 2 0 R /Contents 22 0 R>>",
		10: "<</Font <</F1 11 0 R /F2 12 0 R /T3 13 0 R>>" +
			" /XObject <</Im1 30 0 R /Im2 31 0 R /Fm1 32 0 R /Fm2 33 0 R /Im3 34 0 R>>" +
			" /ExtGState <</G1 <</ca 0.5 /Mark /GS1>> /G2 <</ca 0.25 /Mark /GS2>> /G3 <</LW 2 /Mark /GS3>>>>" +
			" /ColorSpace <</Cs1 [/Indexed /DeviceRGB 1 <ff000000ff00>] /Cs2 [/CalGray <</WhitePoint [0.95 1 1.09] /Mark /CS2>>]" +
			" /DefaultRGB [/CalRGB <</WhitePoint [0.95 1 1.09] /Mark /DEFRGB>>]>>" +
			" /Pattern <</P1 40 0 R /P2 41 0 R>> /Shading <</S1 42 0 R /S2 43 0 R>>" +
			" /Properties <</L1 45 0 R /L2 46 0 R>> /ProcSet [/PDF /Text]>>",
		11: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
		12: "<</Type /Font /Subtype /Type1 /BaseFont /Courier /Encoding /WinAnsiEncoding>>",
		13: "<</Type /Font /Subtype /Type3 /FontBBox [0 0 1000 1000] /FontMatrix [0.001 0 0 0.001 0 0] /CharProcs <</a 14 0 R>>" +
			" /Encoding <</Type /Encoding /Differences [97 /a]>> /FirstChar 97 /LastChar 97 /Widths [1000] /Resources 10 0 R>>",
		14: testpdf.Stream("", []byte("1000 0 d0 /G3 gs 100 100 600 600 re S")),
		20: testpdf.Stream("", []byte(one)),
		21: testpdf.Stream("", []byte(two)),
		22: testpdf.Stream("", []byte(two+"1 0 0 RG 5 5 190 190 re S\n")),
		30: image("IM1", 255, 0, 0),
		31: image("IM2", 0, 255, 0),
		// A form with the resources of all, of which it names one font;
		// a form without any, which draws with the page's.
		32: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 200 200] /Resources 10 0 R /Mark /FM1 /PieceInfo <</App <</Private (FORMPRIVATE)>>>>",
			[]byte("BT /F2 10 Tf 20 60 Td (Form) Tj ET")),
		33: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 200 200] /Mark /FM2", []byte("q 20 0 0 20 150 150 cm /Im3 Do Q")),
		34: strings.Replace(image("IM3", 0, 0, 255), "/Mark", "/Metadata 62 0 R /Mark", 1),
		40: pattern("PAT1", "1 0 0 rg"),
		41: pattern("PAT2", "0 0 1 rg"),
		42: shading("SH1", 1, 0, 0),
		43: shading("SH2", 0, 0, 1),
		45: "<</Mark /PROP1>>",
		46: "<</Mark /PROP2>>",
		50: image("THUMB", 9, 9, 9),
		60: testpdf.Stream("/Type /Metadata /Subtype /XML", []byte("<x:xmpmeta>DOCXMP</x:xmpmeta>")),
		61: testpdf.Stream("/Type /Metadata /Subtype /XML", []byte("<x:xmpmeta>PAGEXMP</x:xmpmeta>")),
		62: testpdf.Stream("/Type /Metadata /Subtype /XML", []byte("<x:xmpmeta>IMAGEXMP</x:xmpmeta>")),
		// An appearance without resources of its own.
		70: "<</Type /Annot /Subtype /Stamp /Rect [100 20 140 60] /AP <</N 71 0 R>>>>",
		71: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 40 40] /Mark /AP", []byte("/G2 gs 1 0 1 rg 0 0 40 40 re f")),
	}
}

func optFile() []byte { return testpdf.File{Objs: optObjs(), Trailer: "/Root 1 0 R"}.Bytes() }

// marks returns the /Mark names in a file, each once, in a row.
func marks(t *testing.T, file []byte) string {
	t.Helper()
	all := string(everything(t, file))
	var out []string
	for _, m := range []string{"IM1", "IM2", "IM3", "FM1", "FM2", "GS1", "GS2", "GS3", "CS2", "DEFRGB", "PAT1", "PAT2", "SH1", "SH2", "PROP1", "PROP2", "THUMB", "AP"} {
		if strings.Contains(all, "/Mark /"+m) {
			out = append(out, m)
		}
	}
	return strings.Join(out, " ")
}

// namesOf lists the names of a resource dictionary of d, by kind.
func namesOf(d *pdf.Document, res pdf.Object) string {
	rd, _ := d.Resolve(res).Dict()
	var out []string
	for k, v := range rd.All() {
		sub, ok := d.Resolve(v).Dict()
		if !ok {
			continue // the procedure set
		}
		for name := range sub.All() {
			out = append(out, string(k)+":"+string(name))
		}
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}

// samePages checks that the pages of out render like pages idx of src.
func samePages(t *testing.T, name string, src, out []byte, idx ...int) {
	t.Helper()
	sd, od := editOpen(t, src), editOpen(t, out)
	if od.NumPages() != len(idx) {
		t.Fatalf("%s: %d pages, want %d", name, od.NumPages(), len(idx))
	}
	for i, x := range idx {
		want, _ := editRender(t, sd, x)
		got, _ := editRender(t, od, i)
		if n, most := pixDiff(want, got); n != 0 {
			t.Errorf("%s: page %d differs from page %d of the source in %d pixels, by up to %d", name, i, x, n, most)
		}
	}
}

// TestSavePruneResources leaves out the resources no content names: of the
// pages, and of the forms, patterns and Type 3 fonts they draw. A
// dictionary several of them have is written once, with what any names.
func TestSavePruneResources(t *testing.T) {
	src := optFile()
	doc := editOpen(t, src)
	prune := SaveOptions{PruneResources: true}

	// The first page alone: what the others drew goes, with the pruning.
	e := doc.Edit()
	if err := e.DeletePages(1, 2); err != nil {
		t.Fatal(err)
	}
	if got, want := marks(t, saveWith(t, e, SaveOptions{})), "IM1 IM2 IM3 FM1 FM2 GS1 GS2 GS3 CS2 DEFRGB PAT1 PAT2 SH1 SH2 PROP1 PROP2 THUMB AP"; got != want {
		t.Errorf("saved as it is, the file has\n%s, want\n%s", got, want)
	}
	out := saveWith(t, e, prune)
	editStrict(t, out)
	// GS2 is named by the appearance that has no resources, GS3 by the
	// glyph of the Type 3 font, the default colour space by nothing.
	if got, want := marks(t, out), "IM1 IM3 FM1 FM2 GS1 GS2 GS3 DEFRGB PAT1 SH1 PROP1 THUMB AP"; got != want {
		t.Errorf("pruned, the file has\n%s, want\n%s", got, want)
	}
	samePages(t, "the first page", src, out, 0)
	all := string(everything(t, out))
	if strings.Contains(all, "/Courier") != true || strings.Contains(all, "Indexed") != true {
		t.Error("the font of the form or the colour space of the inline image is gone")
	}

	// The page, the form and the font have one dictionary, as they had:
	// with what any of them names. The pattern has its own, and names
	// nothing of it.
	const union = "ColorSpace:Cs1 ColorSpace:DefaultRGB ExtGState:G1 ExtGState:G2 ExtGState:G3 Font:F1 Font:F2 Font:T3" +
		" Pattern:P1 Properties:L1 Shading:S1 XObject:Fm1 XObject:Fm2 XObject:Im1 XObject:Im3"
	od := editStrict(t, out)
	page, err := od.Page(1)
	if err != nil {
		t.Fatal(err)
	}
	shared := page.Get("Resources")
	if _, ok := shared.Ref(); !ok {
		t.Errorf("the page has the resources %v", shared)
	}
	size, _ := od.Trailer().Get("Size").Int()
	found := 0
	for n := int32(1); n < int32(size); n++ {
		o, _ := od.Get(pdf.Ref{Num: n})
		d, _ := o.Dict()
		mark, _ := d.Get("Mark").Name()
		subtype, _ := d.Get("Subtype").Name()
		want := union
		switch {
		case mark == "FM1", subtype == "Type3":
			if d.Get("Resources") != shared {
				t.Errorf("%s %s has the resources %v, the page %v", mark, subtype, d.Get("Resources"), shared)
			}
		case mark == "PAT1":
			want = ""
		default:
			continue
		}
		found++
		if got := namesOf(od, d.Get("Resources")); got != want {
			t.Errorf("%s %s has the resources %s, want %s", mark, subtype, got, want)
		}
	}
	if found != 3 {
		t.Errorf("%d of the form, the pattern and the font found", found)
	}

	// All pages: every entry of the dictionary they share is named by
	// one of them, and it stays the one it is.
	out = saveWith(t, doc.Edit(), prune)
	samePages(t, "all pages", src, out, 0, 1, 2)
	od = editStrict(t, out)
	var res [3]pdf.Object
	for i := range res {
		page, err := od.Page(i + 1)
		if err != nil {
			t.Fatal(err)
		}
		res[i] = page.Get("Resources")
	}
	if _, ok := res[0].Ref(); !ok || res[0] != res[1] || res[1] != res[2] {
		t.Errorf("the pages have the resources %v, %v and %v", res[0], res[1], res[2])
	}
	if got := namesOf(od, res[0]); !strings.Contains(got, "XObject:Im1 XObject:Im2 XObject:Im3") || !strings.Contains(got, "ExtGState:G1 ExtGState:G2 ExtGState:G3") {
		t.Errorf("the pages have the resources %s", got)
	}
	if plain := saveWith(t, doc.Edit(), SaveOptions{}); len(out) >= len(plain) {
		t.Errorf("pruned, the document is %d bytes; as it is, %d", len(out), len(plain))
	}

	// Pages with dictionaries of their own that share the one of the
	// fonts: it is written once, with the fonts either names.
	shareObjs := map[int]string{
		1:  "<</Type /Catalog /Pages 2 0 R>>",
		2:  "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 200 200]>>",
		3:  "<</Type /Page /Parent 2 0 R /Contents 6 0 R /Resources <</Font 5 0 R /ExtGState <</A <</LW 1>> /B <</LW 2>>>>>>>>",
		4:  "<</Type /Page /Parent 2 0 R /Contents 7 0 R /Resources <</Font 5 0 R>>>>",
		5:  "<</F1 8 0 R /F2 9 0 R /F3 10 0 R>>",
		6:  testpdf.Stream("", []byte("BT /F1 12 Tf 20 170 Td (One) Tj ET /B gs 20 20 50 50 re S")),
		7:  testpdf.Stream("", []byte("BT /F2 12 Tf 20 170 Td (Two) Tj ET")),
		8:  "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica>>",
		9:  "<</Type /Font /Subtype /Type1 /BaseFont /Courier>>",
		10: "<</Type /Font /Subtype /Type1 /BaseFont /Times-Roman>>",
	}
	shareSrc := testpdf.File{Objs: shareObjs, Trailer: "/Root 1 0 R"}.Bytes()
	out = saveWith(t, editOpen(t, shareSrc).Edit(), prune)
	samePages(t, "a shared dictionary of fonts", shareSrc, out, 0, 1)
	od = editStrict(t, out)
	var fonts [2]pdf.Object
	for i := range fonts {
		page, err := od.Page(i + 1)
		if err != nil {
			t.Fatal(err)
		}
		pres, _ := od.Resolve(page.Get("Resources")).Dict()
		fonts[i] = pres.Get("Font")
		if got, want := namesOf(od, page.Get("Resources")), [2]string{"ExtGState:B Font:F1 Font:F2", "Font:F1 Font:F2"}[i]; got != want {
			t.Errorf("page %d has the resources %s, want %s", i, got, want)
		}
	}
	if _, ok := fonts[0].Ref(); !ok || fonts[0] != fonts[1] {
		t.Errorf("the pages have the fonts %v and %v", fonts[0], fonts[1])
	}
	if strings.Contains(string(everything(t, out)), "Times-Roman") {
		t.Error("the font no page names is written")
	}

	// A page that is drawn on is pruned too, and has the drawing.
	e = doc.Edit()
	if err := e.DeletePages(1, 2); err != nil {
		t.Fatal(err)
	}
	if err := e.Draw(0, true, func(c *Canvas) { c.FillRect(Rect{X0: 5, Y0: 5, X1: 15, Y1: 15}, color.RGBA{0, 128, 0, 255}) }); err != nil {
		t.Fatal(err)
	}
	plain, out := saveWith(t, e, SaveOptions{}), saveWith(t, e, prune)
	if got := marks(t, out); strings.Contains(got, "IM2") || !strings.Contains(got, "IM1") {
		t.Errorf("drawn on and pruned, the file has %s", got)
	}
	samePages(t, "drawn on", plain, out, 0)

	// Content that cannot be read to the end: nothing is known of what it
	// names, and everything stays.
	objs := optObjs()
	objs[20] = testpdf.Stream("", []byte("BT /F1 12 Tf 20 170 Td (One) Tj ET ) ] /Im1 Do"))
	bad := editOpen(t, testpdf.File{Objs: objs, Trailer: "/Root 1 0 R"}.Bytes())
	e = bad.Edit()
	if err := e.DeletePages(1, 2); err != nil {
		t.Fatal(err)
	}
	if got := marks(t, saveWith(t, e, prune)); !strings.Contains(got, "IM2") || !strings.Contains(got, "SH2") {
		t.Errorf("with content that does not read, the file has %s", got)
	}

	// Pages imported into a new document are pruned as well.
	ne := NewEditor()
	if err := ne.ImportPages(0, doc, EditPage{Index: 1}); err != nil {
		t.Fatal(err)
	}
	out = saveWith(t, ne, prune)
	if got, want := marks(t, out), "IM2 GS2 CS2 DEFRGB PAT2 SH2 PROP2"; got != want {
		t.Errorf("imported and pruned, the file has\n%s, want\n%s", got, want)
	}
	samePages(t, "imported", src, out, 1)
}

// dupObjs is a document of two pages that each have their own copy of
// everything.
func dupObjs() map[int]string {
	content := []byte("q 40 0 0 40 20 100 cm /Im Do Q /Fm Do q 20 0 0 20 100 100 cm /Im2 Do Q")
	image := func(extra string, v byte) string {
		return testpdf.Stream("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8 "+extra, bytes.Repeat([]byte{v}, 4))
	}
	form := func(layer int) string {
		return testpdf.Stream(fmt.Sprintf("/Type /XObject /Subtype /Form /BBox [0 0 200 200] /OC %d 0 R", layer), []byte("0 0 1 rg 100 20 40 40 re f"))
	}
	return map[int]string{
		1:  "<</Type /Catalog /Pages 2 0 R /OCProperties <</OCGs [40 0 R 41 0 R] /D <</Order [40 0 R 41 0 R]>>>>>>",
		2:  "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 200 200]>>",
		3:  "<</Type /Page /Parent 2 0 R /Contents 5 0 R /Resources <</Font <</F1 10 0 R>> /XObject <</Im 20 0 R /Fm 30 0 R /Im2 24 0 R>>>>>>",
		4:  "<</Type /Page /Parent 2 0 R /Contents 6 0 R /Resources <</Font <</F1 11 0 R>> /XObject <</Im 21 0 R /Fm 31 0 R /Im2 25 0 R>>>>>>",
		5:  testpdf.Stream("", content),
		6:  testpdf.Stream("", content),
		10: "<</Type /Font /Subtype /TrueType /BaseFont /ABCDEF+Some /FontDescriptor 12 0 R /Encoding /WinAnsiEncoding>>",
		11: "<</Type /Font /Subtype /TrueType /BaseFont /ABCDEF+Some /FontDescriptor 13 0 R /Encoding /WinAnsiEncoding>>",
		12: "<</Type /FontDescriptor /FontName /ABCDEF+Some /Flags 32 /FontFile2 14 0 R>>",
		13: "<</Type /FontDescriptor /FontName /ABCDEF+Some /Flags 32 /FontFile2 15 0 R>>",
		14: testpdf.Stream("/Length1 11", []byte("FONTPROGRAM")),
		15: testpdf.Stream("/Length1 11", []byte("FONTPROGRAM")),
		// The same image twice, each with its own copy of its mask.
		20: image("/SMask 22 0 R", 80),
		21: image("/SMask 23 0 R", 80),
		22: image("", 200),
		23: image("", 200),
		// The same image twice, told apart by its metadata alone.
		24: image("/Metadata 26 0 R", 30),
		25: image("/Metadata 27 0 R", 30),
		26: testpdf.Stream("/Type /Metadata /Subtype /XML", []byte("<x:xmpmeta>first</x:xmpmeta>")),
		27: testpdf.Stream("/Type /Metadata /Subtype /XML", []byte("<x:xmpmeta>second</x:xmpmeta>")),
		// The same form on two layers that read the same and are two.
		30: form(40),
		31: form(41),
		40: "<</Type /OCG /Name (Layer)>>",
		41: "<</Type /OCG /Name (Layer)>>",
	}
}

// census counts the objects of a file by what they are.
func census(t *testing.T, file []byte) string {
	t.Helper()
	d := editStrict(t, file)
	size, _ := d.Trailer().Get("Size").Int()
	n := map[string]int{}
	for num := int32(1); num < int32(size); num++ {
		o, _ := d.Get(pdf.Ref{Num: num})
		dict, _ := o.Dict()
		typ, _ := dict.Get("Type").Name()
		subtype, _ := dict.Get("Subtype").Name()
		_, stream := o.Stream()
		switch {
		case subtype == "Image":
			n["images"]++
		case subtype == "Form":
			n["forms"]++
		case typ == "Font":
			n["fonts"]++
		case typ == "FontDescriptor":
			n["descriptors"]++
		case typ == "OCG":
			n["layers"]++
		case typ == "Metadata":
			n["metadata"]++
		case stream && dict.Has("Length1"):
			n["programs"]++
		case stream && typ != "ObjStm" && typ != "XRef":
			n["contents"]++
		}
	}
	return fmt.Sprintf("%d images, %d forms, %d fonts, %d descriptors, %d programs, %d contents, %d layers, %d metadata",
		n["images"], n["forms"], n["fonts"], n["descriptors"], n["programs"], n["contents"], n["layers"], n["metadata"])
}

// TestSaveJoinDuplicates writes what is the same once: streams and fonts,
// of one document and of several, and not what is told apart by more than
// what it holds.
func TestSaveJoinDuplicates(t *testing.T) {
	src := testpdf.File{Objs: dupObjs(), Trailer: "/Root 1 0 R"}.Bytes()
	doc := editOpen(t, src)
	if got, want := census(t, saveWith(t, doc.Edit(), SaveOptions{})), "6 images, 2 forms, 2 fonts, 2 descriptors, 2 programs, 2 contents, 2 layers, 2 metadata"; got != want {
		t.Errorf("saved as it is, the file has %s", got)
	}
	// The images with different metadata stay two, the forms on two
	// layers too, and the layers.
	out := saveWith(t, doc.Edit(), SaveOptions{JoinDuplicates: true})
	if got, want := census(t, out), "4 images, 2 forms, 1 fonts, 1 descriptors, 1 programs, 1 contents, 2 layers, 2 metadata"; got != want {
		t.Errorf("joined, the file has %s", got)
	}
	samePages(t, "joined", src, out, 0, 1)
	// Without the metadata nothing tells them apart.
	out = saveWith(t, doc.Edit(), SaveOptions{JoinDuplicates: true, Drop: DropObjectMetadata})
	if got, want := census(t, out), "3 images, 2 forms, 1 fonts, 1 descriptors, 1 programs, 1 contents, 2 layers, 0 metadata"; got != want {
		t.Errorf("joined without metadata, the file has %s", got)
	}
	samePages(t, "joined without metadata", src, out, 0, 1)
	od := editOpen(t, out)
	if layers := od.Layers(); layers == nil || len(layers.Layers) != 2 {
		t.Errorf("the document has the layers %+v", layers)
	}

	// Pages of two documents, and of one document twice.
	other := editOpen(t, src)
	e := NewEditor()
	if err := e.ImportPages(0, doc, EditPage{Index: 0}, EditPage{Index: 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.ImportPages(2, other, EditPage{Index: 0}); err != nil {
		t.Fatal(err)
	}
	if got, want := census(t, saveWith(t, e, SaveOptions{})), "9 images, 3 forms, 3 fonts, 3 descriptors, 3 programs, 3 contents, 4 layers, 3 metadata"; got != want {
		t.Errorf("merged, the file has %s", got)
	}
	out = saveWith(t, e, SaveOptions{JoinDuplicates: true})
	if got, want := census(t, out), "4 images, 3 forms, 1 fonts, 1 descriptors, 1 programs, 1 contents, 4 layers, 2 metadata"; got != want {
		t.Errorf("merged and joined, the file has %s", got)
	}
	samePages(t, "merged and joined", src, out, 0, 1, 0)

	// An edited document with a page of another.
	e = doc.Edit()
	if err := e.ImportPages(2, other, EditPage{Index: 1}); err != nil {
		t.Fatal(err)
	}
	out = saveWith(t, e, SaveOptions{JoinDuplicates: true, ObjectStreams: ObjectStreamsAlways})
	if got, want := census(t, out), "4 images, 3 forms, 1 fonts, 1 descriptors, 1 programs, 1 contents, 4 layers, 2 metadata"; got != want {
		t.Errorf("extended and joined, the file has %s", got)
	}
	samePages(t, "extended and joined", src, out, 0, 1, 1)
}

// runLength packs b as the run length filter reads it, without looking for
// runs.
func runLength(b []byte) []byte {
	var out []byte
	for len(b) > 0 {
		n := min(len(b), 128)
		out = append(append(out, byte(n-1)), b[:n]...)
		b = b[n:]
	}
	return append(out, 128)
}

// TestSaveCompress stores with the Flate filter what the document has
// plain or packed worse, and leaves the rest.
func TestSaveCompress(t *testing.T) {
	content := []byte(strings.Repeat("0 0 1 rg 20 20 100 100 re f\n", 60))
	pixels := bytes.Repeat([]byte{10, 200, 30, 10, 200, 31}, 128)
	jpeg := append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{0x5a, 0xa5, 0x00, 0xff}, 60)...)
	var a85 bytes.Buffer
	w85 := ascii85.NewEncoder(&a85)
	if _, err := w85.Write(jpeg); err != nil {
		t.Fatal(err)
	}
	w85.Close()
	a85.WriteString("~>")
	var lz bytes.Buffer
	wl := lzw.NewWriter(&lz, lzw.MSB, 8)
	if _, err := wl.Write(pixels); err != nil {
		t.Fatal(err)
	}
	wl.Close()
	image := func(mark, filter string, data []byte) string {
		return testpdf.Stream("/Type /XObject /Subtype /Image /Width 16 /Height 16 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Mark /"+mark+" "+filter, data)
	}
	objs := map[int]string{
		1:  "<</Type /Catalog /Pages 2 0 R /Metadata 20 0 R>>",
		2:  "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200]>>",
		3:  "<</Type /Page /Parent 2 0 R /Contents [4 0 R 5 0 R] /Resources <</XObject <</A 10 0 R /B 11 0 R /C 12 0 R /D 13 0 R /E 14 0 R /F 15 0 R>>>>>>",
		4:  testpdf.Stream("/Mark /PLAIN", content),
		5:  testpdf.Stream("/Mark /SHORT", []byte("q Q")),
		10: image("RAW", "", pixels),
		11: image("HEX", "/Filter /ASCIIHexDecode", []byte(hex.EncodeToString(pixels)+">")),
		12: image("A85DCT", "/Filter [/ASCII85Decode /DCTDecode] /DecodeParms [null <</ColorTransform 1>>]", a85.Bytes()),
		13: image("LZW", "/Filter /LZWDecode", lz.Bytes()),
		14: image("RL", "/Filter [/RunLengthDecode]", runLength(pixels)),
		15: image("FLATE", "/Filter /FlateDecode", testpdf.Deflate(pixels)),
		20: testpdf.Stream("/Type /Metadata /Subtype /XML", []byte("<x:xmpmeta>"+strings.Repeat("DOCXMP ", 40)+"</x:xmpmeta>")),
	}
	src := testpdf.File{Objs: objs, Trailer: "/Root 1 0 R"}.Bytes()
	doc := editOpen(t, src)
	plain := saveWith(t, doc.Edit(), SaveOptions{})
	out := saveWith(t, doc.Edit(), SaveOptions{Compress: true})
	if len(out) >= len(plain) {
		t.Errorf("compressed, the file is %d bytes; as it is, %d", len(out), len(plain))
	}
	samePages(t, "compressed", src, out, 0)
	if !bytes.Contains(out, []byte("DOCXMP DOCXMP")) {
		t.Error("the metadata of the document is not plain")
	}

	sd, err := pdf.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	od := editStrict(t, out)
	byMark := func(d *pdf.Document) map[pdf.Name]*pdf.Stream {
		m := map[pdf.Name]*pdf.Stream{}
		size, _ := d.Trailer().Get("Size").Int()
		for n := int32(1); n < int32(size); n++ {
			o, _ := d.Get(pdf.Ref{Num: n})
			if s, ok := o.Stream(); ok {
				mark, _ := s.Dict.Get("Mark").Name()
				m[mark] = s
			}
		}
		return m
	}
	was, is := byMark(sd), byMark(od)
	for mark, want := range map[pdf.Name]string{
		"PLAIN": "FlateDecode", "SHORT": "null", "RAW": "FlateDecode", "HEX": "FlateDecode",
		"A85DCT": "DCTDecode", "LZW": "FlateDecode", "RL": "FlateDecode", "FLATE": "FlateDecode",
	} {
		a, b := was[mark], is[mark]
		if a == nil || b == nil {
			t.Fatalf("%s is missing", mark)
		}
		if got := b.Dict.Get("Filter").String(); strings.TrimPrefix(got, "/") != want {
			t.Errorf("%s is under %s, want %s", mark, got, want)
		}
		da, db := sd.Decode(a), od.Decode(b)
		if da.Recovered || db.Recovered || !bytes.Equal(da.Data, db.Data) || da.Image != db.Image {
			t.Errorf("%s decodes to %d bytes (%v), was %d", mark, len(db.Data), db.Cause, len(da.Data))
		}
		if len(od.Raw(b)) > len(sd.Raw(a)) {
			t.Errorf("%s grew from %d to %d bytes", mark, len(sd.Raw(a)), len(od.Raw(b)))
		}
	}
	if p := is["A85DCT"].Dict.Get("DecodeParms"); !strings.Contains(p.String(), "ColorTransform") || !bytes.Equal(od.Raw(is["A85DCT"]), jpeg) {
		t.Errorf("the image under ASCII85 has the parameters %v and %d bytes", p, len(od.Raw(is["A85DCT"])))
	}
	if p := is["LZW"].Dict.Get("DecodeParms"); !p.IsNull() {
		t.Errorf("the image that was under LZW has the parameters %v", p)
	}
}

// TestSaveDrop leaves out what a viewer does not need, and all of the
// options together write an encrypted document too.
func TestSaveDrop(t *testing.T) {
	src := optFile()
	doc := editOpen(t, src)
	has := func(file []byte) string {
		all := string(everything(t, file))
		var out []string
		for _, s := range []string{"CATPRIVATE", "PAGEPRIVATE", "FORMPRIVATE", "THUMB", "DOCXMP", "PAGEXMP", "IMAGEXMP"} {
			if strings.Contains(all, s) {
				out = append(out, s)
			}
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		drop Drop
		want string
	}{
		{0, "CATPRIVATE PAGEPRIVATE FORMPRIVATE THUMB DOCXMP PAGEXMP IMAGEXMP"},
		{DropThumbnails, "CATPRIVATE PAGEPRIVATE FORMPRIVATE DOCXMP PAGEXMP IMAGEXMP"},
		{DropPieceInfo, "THUMB DOCXMP PAGEXMP IMAGEXMP"},
		{DropObjectMetadata, "CATPRIVATE PAGEPRIVATE FORMPRIVATE THUMB DOCXMP"},
		{DropThumbnails | DropPieceInfo | DropObjectMetadata, "DOCXMP"},
	} {
		out := saveWith(t, doc.Edit(), SaveOptions{Drop: c.drop})
		editStrict(t, out)
		if got := has(out); got != c.want {
			t.Errorf("drop %b: the file has %q, want %q", c.drop, got, c.want)
		}
		samePages(t, "dropped", src, out, 0, 1, 2)
	}
	if err := doc.Edit().SaveWith(io.Discard, SaveOptions{Drop: 1 << 9}); err == nil {
		t.Error("an unknown part was dropped")
	}

	// Everything at once.
	all := SaveOptions{
		PruneResources: true, JoinDuplicates: true, Compress: true, ObjectStreams: ObjectStreamsAlways,
		Drop: DropThumbnails | DropPieceInfo | DropObjectMetadata,
	}
	out := saveWith(t, doc.Edit(), all)
	samePages(t, "everything", src, out, 0, 1, 2)
	if plain := saveWith(t, doc.Edit(), SaveOptions{}); len(out) >= len(plain)*3/4 {
		t.Errorf("with everything the file is %d bytes; as it is, %d", len(out), len(plain))
	}
	// Saved again, it is what it is.
	again := saveWith(t, editOpen(t, out).Edit(), all)
	if len(again) > len(out)+16 {
		t.Errorf("saved again the file grew from %d to %d bytes", len(out), len(again))
	}
	samePages(t, "everything, twice", src, again, 0, 1, 2)

	// An update takes none of it.
	var upd bytes.Buffer
	if err := doc.Edit().Update(&upd); err != nil || !bytes.Equal(upd.Bytes(), src) {
		t.Errorf("an update of an unchanged document is %d bytes of %d: %v", upd.Len(), len(src), err)
	}

	// An encrypted document stays encrypted and reads as it did.
	ed, err := OpenWithPassword(testpdf.EncryptedWith("aes256", "user", -1), "user")
	if err != nil {
		t.Fatal(err)
	}
	ee := ed.Edit()
	ee.unguarded = true
	var enc bytes.Buffer
	if err := ee.SaveWith(&enc, all); err != nil {
		t.Fatal(err)
	}
	od, err := OpenWithPassword(enc.Bytes(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if !od.Encrypted() || od.NumPages() != ed.NumPages() || od.Form().Field("secret").Saved.Text() != "old" {
		t.Errorf("encrypted %v, %d pages, the field reads %q", od.Encrypted(), od.NumPages(), od.Form().Field("secret").Saved.Text())
	}
	for i := range ed.NumPages() {
		a, _ := editRender(t, ed, i)
		b, _ := editRender(t, od, i)
		if n, _ := pixDiff(a, b); n != 0 {
			t.Errorf("encrypted: page %d differs in %d pixels", i, n)
		}
	}
}

// TestSaveSmallerEdited saves edited documents with everything that makes
// them smaller: they are what they are without it.
func TestSaveSmallerEdited(t *testing.T) {
	all := SaveOptions{
		PruneResources: true, JoinDuplicates: true, Compress: true, ObjectStreams: ObjectStreamsAlways,
		Drop: DropThumbnails | DropPieceInfo | DropObjectMetadata,
	}
	same := func(name string, plain, small []byte) {
		t.Helper()
		pd, sd := editOpen(t, plain), editOpen(t, small)
		if pd.NumPages() != sd.NumPages() {
			t.Fatalf("%s: %d pages, want %d", name, sd.NumPages(), pd.NumPages())
		}
		for i := range pd.NumPages() {
			a, _ := editRender(t, pd, i)
			b, _ := editRender(t, sd, i)
			if n, most := pixDiff(a, b); n != 0 {
				t.Errorf("%s: page %d differs in %d pixels, by up to %d", name, i, n, most)
			}
			if at, bt := textOf(t, pd, i).String(), textOf(t, sd, i).String(); at != bt {
				t.Errorf("%s: page %d reads %q, want %q", name, i, bt, at)
			}
		}
		if len(small) >= len(plain) {
			t.Errorf("%s: %d bytes, as it is %d", name, len(small), len(plain))
		}
	}

	// A redaction: the pages it rewrote, and nothing of what it took out.
	doc := editOpen(t, redactFile(""))
	e := doc.Edit()
	if err := e.Redact(0, redactAreas(t, doc, 0, "SECRET ", "HIDDEN", "TOPSECRET", "SECRET2", "SECRET3"), color.Black); err != nil {
		t.Fatal(err)
	}
	plain, small := saveWith(t, e, SaveOptions{}), saveWith(t, e, all)
	same("redacted", plain, small)
	// The second page still draws the form with its text, as it did.
	for _, s := range []string{"SECRET ", "HIDDEN", "TOPSECRET", "SECRET2", "SECRET3", "SECRETVALUE"} {
		if was, is := bytes.Contains(everything(t, plain), []byte(s)), bytes.Contains(everything(t, small), []byte(s)); is && !was {
			t.Errorf("%q, which was redacted, is in the file", s)
		} else if s == "HIDDEN" && (was || is) {
			t.Errorf("%q is in the file: as it is %v, smaller %v", s, was, is)
		}
	}

	// A form filled in, a highlight, a drawing, a page of another
	// document, and the form flattened.
	for _, flatten := range []bool{false, true} {
		doc = editOpen(t, fieldsFile(false))
		e = doc.Edit()
		state := doc.Form().NewState()
		if err := state.SetValue(doc.Form().Field("name"), TextValue("smaller")); err != nil {
			t.Fatal(err)
		}
		if err := e.SetFields(state); err != nil {
			t.Fatal(err)
		}
		if err := e.AddHighlight(0, Highlight{Quads: []Quad{QuadOf(Rect{X0: 20, Y0: 20, X1: 80, Y1: 40})}}); err != nil {
			t.Fatal(err)
		}
		if err := e.Draw(0, false, func(c *Canvas) { c.FillRect(Rect{X0: 5, Y0: 5, X1: 15, Y1: 15}, color.RGBA{0, 128, 0, 255}) }); err != nil {
			t.Fatal(err)
		}
		if err := e.ImportPages(1, editOpen(t, optFile()), EditPage{Index: 0}, EditPage{Index: 2}); err != nil {
			t.Fatal(err)
		}
		if flatten {
			if err := e.Flatten(); err != nil {
				t.Fatal(err)
			}
		}
		plain, small = saveWith(t, e, SaveOptions{}), saveWith(t, e, all)
		same(fmt.Sprintf("edited, flattened %v", flatten), plain, small)
		if !flatten {
			if got, want := formString(editOpen(t, small)), formString(editOpen(t, plain)); got != want {
				t.Errorf("the form reads\n%s, want\n%s", got, want)
			}
		}
	}
}
