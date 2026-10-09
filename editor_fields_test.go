package cera

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// fieldsFile is a page with a field of every kind that holds a value.
func fieldsFile(objStm bool) []byte {
	ap := "/AP <</N <</%s 30 0 R /Off 31 0 R>>>>"
	return testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R /OCProperties <</OCGs [40 0 R] /D <</OFF [40 0 R]>>>> /AcroForm <</Fields [10 0 R 11 0 R 12 0 R 13 0 R 14 0 R 17 0 R 18 0 R 41 0 R 42 0 R] /DA (/Helv 10 Tf 0 g) /DR <</Font <</Helv 20 0 R>>>>>>>>",
			2:  "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200]>>",
			3:  "<</Type /Page /Parent 2 0 R /Contents 43 0 R /Annots [10 0 R 11 0 R 15 0 R 16 0 R 13 0 R 14 0 R 17 0 R 18 0 R 41 0 R 42 0 R 44 0 R]>>",
			40: "<</Type /OCG /Name (Hidden)>>",
			41: "<</Type /Annot /Subtype /Widget /FT /Tx /T (layered) /V (in a hidden layer) /OC 40 0 R /Rect [150 130 290 150] /MK <</BG [1 0 0]>>>>",
			42: "<</Type /Annot /Subtype /Widget /FT /Tx /T (hidden) /V (hidden) /F 2 /Rect [150 160 290 180] /MK <</BG [1 0 0]>>>>",
			43: testpdf.Stream("", []byte("0.9 g 0 0 300 200 re f 1 0 0 1 5 5 cm")),
			44: "<</Type /Annot /Subtype /Square /Rect [200 100 250 120] /C [0 0 1]>>",
			17: "<</Type /Annot /Subtype /Widget /FT /Tx /T (turned) /Rect [260 100 290 190] /MK <</R 90 /BG [0.9 0.9 1] /BC [0 0 1]>> /BS <</W 1>>>>",
			18: "<</Type /Annot /Subtype /Widget /FT /Btn /T (plain) /Rect [70 130 90 150] /MK <</BC [0 0 0]>>>>",
			10: "<</Type /Annot /Subtype /Widget /FT /Tx /T (name) /V (old) /RV (<p>old</p>) /Rect [10 160 140 180]>>",
			11: "<</Type /Annot /Subtype /Widget /FT /Btn /T (agree) /V /Off /AS /Off /Rect [10 130 30 150] " + sprintf(ap, "Yes") + ">>",
			12: "<</FT /Btn /Ff 32768 /T (color) /V /red /Kids [15 0 R 16 0 R]>>",
			15: "<</Type /Annot /Subtype /Widget /Parent 12 0 R /AS /red /Rect [10 100 30 120] " + sprintf(ap, "red") + ">>",
			16: "<</Type /Annot /Subtype /Widget /Parent 12 0 R /AS /Off /Rect [40 100 60 120] " + sprintf(ap, "blue") + ">>",
			13: "<</Type /Annot /Subtype /Widget /FT /Ch /Ff 393216 /T (city) /Opt [[(B) (Berlin)] [(M) (M\xfcnchen)]] /V (B) /Rect [10 70 140 90]>>",
			14: "<</Type /Annot /Subtype /Widget /FT /Ch /Ff 2097152 /T (langs) /Opt [(de) (en) (fr)] /V (de) /I [0] /Rect [150 10 290 90]>>",
			20: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
			30: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 20 20]", []byte("0 0 1 rg 2 2 16 16 re f")),
			31: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 20 20]", []byte("0 0 0 RG 0.5 0.5 19 19 re S")),
		},
		Trailer: "/Root 1 0 R",
		ObjStm:  objStm,
	}.Bytes()
}

// renderState renders the first page of d showing state, or the values
// the file holds when state is nil.
func renderState(t *testing.T, d *Document, state *FormState) *image.RGBA {
	t.Helper()
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	dst := image.NewRGBA(p.Bounds(2))
	err = p.Render(context.Background(), dst, RenderOptions{
		Scale: 2, Background: color.RGBA{255, 255, 255, 255}, Workers: 1, Form: state,
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func sprintf(f, s string) string { return string(bytes.ReplaceAll([]byte(f), []byte("%s"), []byte(s))) }

// fill sets a value in every field of the form of d.
func fill(t *testing.T, d *Document, values map[string]Value) *FormState {
	t.Helper()
	form := d.Form()
	if form == nil {
		t.Fatal("no form")
	}
	state := form.NewState()
	for name, v := range values {
		if err := state.SetValue(form.Field(name), v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	return state
}

func TestSetFields(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values map[string]Value
	}{
		{"pdfdoc", map[string]Value{
			"name":   TextValue("Zoë (€) \\ x"),
			"agree":  StateValue("Yes"),
			"color":  StateValue("blue"),
			"city":   ChoiceValue(1),
			"langs":  ChoiceValue(0, 2),
			"turned": TextValue("up"),
			"plain":  StateValue("Yes"),
		}},
		{"unicode", map[string]Value{
			"name":  TextValue("漢字 ✓"),
			"color": StateValue("Off"),
			"city":  TextValue("Köln"),
			"langs": ChoiceValue(),
		}},
	} {
		for _, stm := range []bool{false, true} {
			src := fieldsFile(stm)
			for _, mode := range []string{"save", "update"} {
				t.Run(tt.name+"/"+mode, func(t *testing.T) {
					doc := editOpen(t, src)
					e := doc.Edit()
					if err := e.SetFields(fill(t, doc, tt.values)); err != nil {
						t.Fatal(err)
					}
					var out []byte
					if mode == "save" {
						out = editSave(t, e)
					} else {
						out = editUpdate(t, e, src)
					}
					form := editOpen(t, out).Form()
					if form == nil {
						t.Fatal("no form after saving")
					}
					for name, want := range tt.values {
						if got := form.Field(name).Saved; !got.Equal(want) {
							t.Errorf("%s: saved %+v, want %+v", name, got, want)
						}
					}
					if form.NeedAppearances {
						t.Error("/NeedAppearances is set")
					}
					// The saved appearances look as cera draws the values.
					want := renderState(t, doc, fill(t, doc, tt.values))
					got := renderState(t, editOpen(t, out), nil)
					if !bytes.Equal(want.Pix, got.Pix) {
						t.Error("the saved file renders unlike the filled form")
					}
					if old := renderState(t, doc, nil); bytes.Equal(old.Pix, got.Pix) {
						t.Error("the saved file renders like the old values")
					}
					d, _ := pdf.Open(out)
					get := func(num int32) pdf.Dict {
						o, _ := d.Get(pdf.Ref{Num: num})
						dd, _ := o.Dict()
						return dd
					}
					if get(10).Has("RV") {
						t.Error("the rich text value stays")
					}
					if mode == "update" {
						// The widgets are where they were.
						wantAS := map[int32]string{15: "Off", 16: "Off"}
						if tt.name == "pdfdoc" {
							wantAS = map[int32]string{11: "Yes", 15: "Off", 16: "blue"}
						}
						for num, want := range wantAS {
							if as, _ := get(num).Get("AS").Name(); string(as) != want {
								t.Errorf("widget %d /AS %v, want %s", num, get(num).Get("AS"), want)
							}
						}
						if tt.name == "pdfdoc" {
							if i, _ := get(14).Get("I").Array(); len(i) != 2 {
								t.Errorf("/I %v", get(14).Get("I"))
							}
						} else if get(14).Has("V") || get(14).Has("I") {
							t.Errorf("an empty selection keeps /V or /I: %v", get(14))
						}
					}
				})
			}
		}
	}
}

// TestSetFieldsUnchanged writes nothing for values the document holds.
func TestSetFieldsUnchanged(t *testing.T) {
	src := fieldsFile(false)
	doc := editOpen(t, src)
	e := doc.Edit()
	state := fill(t, doc, map[string]Value{"name": TextValue("old"), "color": StateValue("red")})
	if err := e.SetFields(state); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := e.Update(&out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), src) {
		t.Error("an update of unchanged values is not the file")
	}
}

// TestSetFieldsPages writes values and deletes and imports pages at once.
func TestSetFieldsPages(t *testing.T) {
	src := fieldsFile(false)
	doc := editOpen(t, src)
	e := doc.Edit()
	if err := e.SetFields(fill(t, doc, map[string]Value{"name": TextValue("new"), "agree": StateValue("Yes")})); err != nil {
		t.Fatal(err)
	}
	if err := e.ImportPages(1, editOpen(t, testpdf.SampleFile(false)), EditPage{Index: 0}); err != nil {
		t.Fatal(err)
	}
	for _, out := range [][]byte{editSave(t, e), editUpdate(t, e, src)} {
		od := editOpen(t, out)
		if od.NumPages() != 2 {
			t.Fatalf("%d pages", od.NumPages())
		}
		f := od.Form()
		if v := f.Field("name").Saved; v.Text() != "new" {
			t.Errorf("name %q", v.Text())
		}
		if v := f.Field("agree").Saved; v.State() != "Yes" {
			t.Errorf("agree %q", v.State())
		}
	}
}

func TestSetFieldsErrors(t *testing.T) {
	a, b := editOpen(t, fieldsFile(false)), editOpen(t, fieldsFile(false))
	if err := a.Edit().SetFields(b.Form().NewState()); err == nil {
		t.Error("the state of another document was accepted")
	}
	if err := NewEditor().SetFields(a.Form().NewState()); err == nil {
		t.Error("a new document accepted a state")
	}
	if err := a.Edit().SetFields(nil); err == nil {
		t.Error("a nil state was accepted")
	}
}

func TestEncodeText(t *testing.T) {
	// ESC is left out: in a text string it starts a language mark.
	for _, s := range []string{"", "plain", "Zoë €", "漢字", "þÿx", "ï»¿x", "\u00ad"} {
		got := textString(pdf.String(encodeText(s)))
		if got != s {
			t.Errorf("%q: read back %q (%x)", s, got, encodeText(s))
		}
	}
	if b := encodeText("Zoë €"); len(b) != 5 {
		t.Errorf("Zoë € is not PDFDocEncoding: %x", b)
	}
}
