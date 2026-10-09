package cera

import (
	"fmt"
	"slices"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
)

// fieldSummary lists the fields of d: name, kind, widgets and value.
func fieldSummary(t *testing.T, d *Document) []string {
	t.Helper()
	form := d.Form()
	if form == nil {
		return nil
	}
	var out []string
	for _, f := range form.Fields {
		out = append(out, fmt.Sprintf("%s:%v:%d:%q:%v", f.Name, f.Type, len(f.Widgets), f.Saved.Text(), f.Saved.Selected()))
	}
	slices.Sort(out)
	return out
}

// TestImportFields imports a page with its fields into a new document: the
// fields come as they were, with their values, and the page looks the
// same.
func TestImportFields(t *testing.T) {
	src := fieldsFile(true)
	sd := editOpen(t, src)
	e := NewEditor()
	if err := e.ImportPages(0, sd, EditPage{Index: 0, Fields: true}); err != nil {
		t.Fatal(err)
	}
	out := editSave(t, e)
	od := editOpen(t, out)
	want, got := fieldSummary(t, sd), fieldSummary(t, od)
	if fmt.Sprint(want) != fmt.Sprint(got) {
		t.Errorf("fields\n got %v\nwant %v", got, want)
	}
	for _, f := range od.Form().Fields {
		if f.da == "" {
			t.Errorf("%s: no /DA", f.Name)
		}
	}
	editSame(t, out, sel(src, 0))

	// The fields can be filled in and saved again.
	e2 := od.Edit()
	if err := e2.SetFields(fill(t, od, map[string]Value{"name": TextValue("again"), "color": StateValue("blue")})); err != nil {
		t.Fatal(err)
	}
	again := editOpen(t, editUpdate(t, e2, out)).Form()
	if v := again.Field("name").Saved.Text(); v != "again" {
		t.Errorf("name %q", v)
	}
	if v := again.Field("color").Saved.State(); v != "blue" {
		t.Errorf("color %q", v)
	}
}

// TestImportFieldsRename imports the edited document's own page with its
// fields: the copies are renamed, and the document's fields stay.
func TestImportFieldsRename(t *testing.T) {
	src := fieldsFile(false)
	doc := editOpen(t, src)
	e := doc.Edit()
	if err := e.ImportPages(1, doc, EditPage{Index: 0, Fields: true}); err != nil {
		t.Fatal(err)
	}
	for _, out := range [][]byte{editSave(t, e), editUpdate(t, e, src)} {
		od := editOpen(t, out)
		f := od.Form()
		if f == nil || len(f.Fields) != 2*len(doc.Form().Fields) {
			t.Fatalf("form %v", fieldSummary(t, od))
		}
		for _, name := range []string{"name", "name_2", "color", "color_2"} {
			if f.Field(name) == nil {
				t.Errorf("no field %q in %v", name, fieldSummary(t, od))
			}
		}
		if n := len(f.Field("color_2").Widgets); n != 2 {
			t.Errorf("color_2 has %d widgets", n)
		}
		for _, w := range f.Field("name_2").Widgets {
			if w.Page != 1 {
				t.Errorf("name_2 on page %d", w.Page)
			}
		}
	}
}

// TestImportFieldsTwice imports a page twice: its fields come once, with
// the first copy; the second has plain widgets, which show only the
// appearance streams they have.
func TestImportFieldsTwice(t *testing.T) {
	sd := editOpen(t, fieldsFile(false))
	e := NewEditor()
	if err := e.ImportPages(0, sd, EditPage{Index: 0, Fields: true}, EditPage{Index: 0, Fields: true}); err != nil {
		t.Fatal(err)
	}
	od := editOpen(t, editSave(t, e))
	if got, want := len(od.Form().Fields), len(sd.Form().Fields); got != want {
		t.Errorf("%d fields, want %d", got, want)
	}
	for _, f := range od.Form().Fields {
		for _, w := range f.Widgets {
			if w.Page != 0 {
				t.Errorf("%s has a widget on page %d", f.Name, w.Page)
			}
		}
	}
}

// TestImportFieldsPart imports one page of a form whose fields span two:
// the fields reaching it come, with the kids on it.
func TestImportFieldsPart(t *testing.T) {
	sd := editOpen(t, formFile())
	e := NewEditor()
	if err := e.ImportPages(0, sd, EditPage{Index: 0, Fields: true}); err != nil {
		t.Fatal(err)
	}
	od := editOpen(t, editSave(t, e))
	got := fieldSummary(t, od)
	if len(got) != 2 || od.Form().Field("a") == nil || od.Form().Field("b") == nil {
		t.Fatalf("fields %v", got)
	}
	if n := len(od.Form().Field("b").Widgets); n != 1 {
		t.Errorf("b has %d widgets", n)
	}
}

// TestImportFieldsSigned leaves a signature field behind.
func TestImportFieldsSigned(t *testing.T) {
	sd := editOpen(t, signedFile(true, 0, false))
	e := NewEditor()
	if err := e.ImportPages(0, sd, EditPage{Index: 0, Fields: true}); err != nil {
		t.Fatal(err)
	}
	od := editOpen(t, editSave(t, e))
	if od.Form().Field("sig") != nil {
		t.Error("the signature field came along")
	}
	if od.Form().Field("name") == nil || od.Form().Field("city") == nil {
		t.Errorf("fields %v", fieldSummary(t, od))
	}
	if od.signing().signed {
		t.Error("the result is signed")
	}
}

// TestImportWithoutFields keeps the old behaviour: widgets alone.
func TestImportWithoutFields(t *testing.T) {
	e := NewEditor()
	if err := e.ImportPages(0, editOpen(t, fieldsFile(false)), EditPage{Index: 0}); err != nil {
		t.Fatal(err)
	}
	if f := editOpen(t, editSave(t, e)).Form(); f != nil {
		t.Errorf("fields %v", len(f.Fields))
	}
}

// TestImportFieldsNoParent imports a radio group whose kids lack /Parent:
// the tree is read from /Kids, as cera reads forms.
func TestImportFieldsNoParent(t *testing.T) {
	ap := "/AP <</N <</%s 30 0 R /Off 31 0 R>>>>"
	src := testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R /AcroForm <</Fields [12 0 R]>>>>",
			2:  "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100]>>",
			3:  "<</Type /Page /Parent 2 0 R /Annots [15 0 R 16 0 R]>>",
			12: "<</FT /Btn /Ff 32768 /T (g) /V /b /Kids [15 0 R 16 0 R]>>",
			15: "<</Type /Annot /Subtype /Widget /AS /Off /Rect [10 10 30 30] " + sprintf(ap, "a") + ">>",
			16: "<</Type /Annot /Subtype /Widget /AS /b /Rect [40 10 60 30] " + sprintf(ap, "b") + ">>",
			30: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 20 20]", []byte("0 0 1 rg 2 2 16 16 re f")),
			31: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 20 20]", []byte("0 0 0 RG 0.5 0.5 19 19 re S")),
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
	e := NewEditor()
	if err := e.ImportPages(0, editOpen(t, src), EditPage{Index: 0, Fields: true}); err != nil {
		t.Fatal(err)
	}
	f := editOpen(t, editSave(t, e)).Form()
	if f == nil || len(f.Fields) != 1 || f.Field("g") == nil || len(f.Field("g").Widgets) != 2 || f.Field("g").Saved.State() != "b" {
		t.Fatalf("fields %+v", f)
	}
}
