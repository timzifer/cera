package cera

import (
	"bytes"
	"strings"
	"testing"

	"github.com/go-opentype/fonts/notosansjp"
	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// textFieldsFile is a form with a text field, a multiline one and a comb
// of five cells, none with an appearance, in Helvetica.
func textFieldsFile() []byte {
	return testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R /AcroForm <</Fields [10 0 R 11 0 R 12 0 R] /DA (/Helv 10 Tf 0 g) /DR <</Font <</Helv 20 0 R>>>>>>>>",
			2:  "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200]>>",
			3:  "<</Type /Page /Parent 2 0 R /Annots [10 0 R 11 0 R 12 0 R]>>",
			10: "<</Type /Annot /Subtype /Widget /FT /Tx /T (line) /Rect [10 160 290 180] /MK <</BC [0 0 0]>>>>",
			11: "<</Type /Annot /Subtype /Widget /FT /Tx /Ff 4096 /T (block) /Rect [10 60 150 150] /MK <</BC [0 0 0]>>>>",
			12: "<</Type /Annot /Subtype /Widget /FT /Tx /Ff 16777216 /MaxLen 5 /T (comb) /Rect [160 120 290 150] /MK <</BC [0 0 0]>>>>",
			20: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

// japanese supplies Noto Sans JP for Adobe-Japan1, as fonts/cjk does.
type japanese struct{}

func (japanese) Font(req FontRequest) ([]byte, bool) {
	return notosansjp.TTF, req.Ordering == "Japan1"
}

// writtenFont returns the fallback font named res in the appearance of
// the widget that is object num of the file, and its parts.
func writtenFont(t *testing.T, file []byte, num int32, res pdf.Name) (font, cid, desc pdf.Dict, d *pdf.Document) {
	t.Helper()
	d, err := pdf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	dict := func(o pdf.Object) pdf.Dict {
		dd, _ := d.Resolve(o).Dict()
		return dd
	}
	w, _ := d.Get(pdf.Ref{Num: num})
	wd, _ := w.Dict()
	ap, ok := d.Resolve(dict(wd.Get("AP")).Get("N")).Stream()
	if !ok {
		t.Fatalf("object %d has no appearance", num)
	}
	font = dict(dict(dict(ap.Dict.Get("Resources")).Get("Font")).Get(res))
	kids, _ := d.Resolve(font.Get("DescendantFonts")).Array()
	if len(kids) != 1 {
		t.Fatalf("font %s of object %d: %v", res, num, font)
	}
	cid = dict(kids[0])
	return font, cid, dict(cid.Get("FontDescriptor")), d
}

// TestFallbackFont writes values Helvetica cannot show with a subset of a
// fallback font: every viewer shows them, and they can be copied.
func TestFallbackFont(t *testing.T) {
	src := textFieldsFile()
	doc := editOpen(t, src)
	values := map[string]Value{
		"line":  TextValue("Zürich Привет Ελλάδα €"),
		"block": TextValue("Первая строка\nи вторая, которая длиннее поля"),
		"comb":  TextValue("aЖbΩ"),
	}
	state := fill(t, doc, values)
	live := renderState(t, doc, state)
	if n, _ := pixDiff(live, renderState(t, doc, nil)); n == 0 {
		t.Fatal("the values do not show")
	}
	e := doc.Edit()
	if err := e.SetFields(state); err != nil {
		t.Fatal(err)
	}
	if lacking := e.MissingGlyphs(); len(lacking) > 0 {
		t.Errorf("fields with missing glyphs: %v", lacking)
	}
	for _, mode := range []string{"save", "update"} {
		t.Run(mode, func(t *testing.T) {
			var out []byte
			if mode == "save" {
				out = editSave(t, e)
				if again := editSave(t, e); !bytes.Equal(out, again) {
					t.Error("a second save writes another file")
				}
			} else {
				out = editUpdate(t, e, src)
			}
			od := editOpen(t, out)
			if od.Form().NeedAppearances {
				t.Error("/NeedAppearances is set")
			}
			if n, most := pixDiff(live, renderState(t, od, nil)); n > 0 {
				t.Errorf("the saved file renders unlike the filled form: %d pixels, by up to %d", n, most)
			}

			num := int32(10)
			if mode == "save" {
				// The widgets are the page's annotations, in order.
				d, _ := pdf.Open(out)
				page, _ := d.Page(1)
				annots, _ := d.Resolve(page.Get("Annots")).Array()
				ref, _ := annots[0].Ref()
				num = ref.Num
			}
			font, cid, desc, d := writtenFont(t, out, num, "CeraFb0")
			if s, _ := font.Get("Subtype").Name(); s != "Type0" {
				t.Errorf("font %v", font)
			}
			if enc, _ := font.Get("Encoding").Name(); enc != "Identity-H" {
				t.Errorf("/Encoding %v", font.Get("Encoding"))
			}
			if base, _ := font.Get("BaseFont").Name(); len(base) < 8 || base[6] != '+' || !strings.HasSuffix(string(base), "Arimo") {
				t.Errorf("/BaseFont %v, want a subset of Arimo", base)
			}
			if w, _ := d.Resolve(cid.Get("W")).Array(); len(w) == 0 {
				t.Error("no /W")
			}
			if _, ok := d.Resolve(cid.Get("CIDToGIDMap")).Stream(); !ok {
				t.Errorf("/CIDToGIDMap %v", cid.Get("CIDToGIDMap"))
			}
			file, ok := d.Resolve(desc.Get("FontFile2")).Stream()
			if !ok {
				t.Fatal("no /FontFile2")
			}
			program, _, err := d.DecodeStrict(file)
			if err != nil {
				t.Fatal(err)
			}
			if len(program) == 0 || len(program) > 20<<10 {
				t.Errorf("the program has %d bytes, want a subset", len(program))
			}
			tu, ok := d.Resolve(font.Get("ToUnicode")).Stream()
			if !ok {
				t.Fatal("no /ToUnicode")
			}
			cmap, _, _ := d.DecodeStrict(tu)
			// П of the first field.
			if !bytes.Contains(cmap, []byte("<041F>")) {
				t.Errorf("/ToUnicode lacks U+041F:\n%s", cmap)
			}
		})
	}
}

// TestFallbackFontFlatten draws a value in a fallback font into the page.
func TestFallbackFontFlatten(t *testing.T) {
	doc := editOpen(t, stackFile(false))
	state := fill(t, doc, map[string]Value{"name": TextValue("Привет, мир")})
	e := doc.Edit()
	if err := e.SetFields(state); err != nil {
		t.Fatal(err)
	}
	if err := e.Flatten(); err != nil {
		t.Fatal(err)
	}
	got := renderState(t, editOpen(t, editSave(t, e)), nil)
	if n, most := pixDiff(renderState(t, doc, state), got); n > 0 {
		t.Errorf("the flattened page renders unlike the form: %d pixels, by up to %d", n, most)
	}
}

// TestFallbackFontProvider shows Japanese with the font the document's
// provider has for it, and names the fields whose values no font shows.
func TestFallbackFontProvider(t *testing.T) {
	src := textFieldsFile()
	values := map[string]Value{"line": TextValue("日本語のテキスト ok"), "comb": TextValue("\U0001F600")}

	plain := editOpen(t, src)
	e := plain.Edit()
	if err := e.SetFields(fill(t, plain, values)); err != nil {
		t.Fatal(err)
	}
	if got := e.MissingGlyphs(); len(got) != 2 || got[0].Name != "line" || got[1].Name != "comb" {
		t.Errorf("without a provider, fields with missing glyphs: %v", got)
	}

	doc, err := OpenWith(src, OpenOptions{Fonts: japanese{}})
	if err != nil {
		t.Fatal(err)
	}
	state := fill(t, doc, values)
	live := renderState(t, doc, state)
	e = doc.Edit()
	if err := e.SetFields(state); err != nil {
		t.Fatal(err)
	}
	if got := e.MissingGlyphs(); len(got) != 1 || got[0].Name != "comb" {
		t.Errorf("fields with missing glyphs: %v", got)
	}
	out := editSave(t, e)
	// A reader without the provider shows the file alike, but for the
	// fields that /NeedAppearances makes it draw again.
	od, err := OpenWith(out, OpenOptions{Fonts: japanese{}})
	if err != nil {
		t.Fatal(err)
	}
	if !od.Form().NeedAppearances {
		t.Error("/NeedAppearances is not set for the value no font shows")
	}
	if n, most := pixDiff(live, renderState(t, od, nil)); n > 0 {
		t.Errorf("the saved file renders unlike the filled form: %d pixels, by up to %d", n, most)
	}
	if len(out) > 200<<10 {
		t.Errorf("the file has %d bytes: the font is not a subset", len(out))
	}
	d, _ := pdf.Open(out)
	page, _ := d.Page(1)
	annots, _ := d.Resolve(page.Get("Annots")).Array()
	ref, _ := annots[0].Ref()
	font, _, _, _ := writtenFont(t, out, ref.Num, "CeraFb1")
	if base, _ := font.Get("BaseFont").Name(); !strings.HasSuffix(string(base), "+Fallback-Japan1") {
		t.Errorf("/BaseFont %v", base)
	}
}
