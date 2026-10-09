package cera

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"slices"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
	"github.com/timzifer/cera/internal/testpdf"
)

func editOpen(t *testing.T, b []byte) *Document {
	t.Helper()
	d, err := Open(b)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// editSave saves e and checks that the result reads without a repair:
// every offset of its table holds the object it names.
func editSave(t *testing.T, e *Editor) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := e.Save(&out); err != nil {
		t.Fatal(err)
	}
	editStrict(t, out.Bytes())
	return out.Bytes()
}

func editStrict(t *testing.T, b []byte) *pdf.Document {
	t.Helper()
	d, err := pdf.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	size, _ := d.Trailer().Get("Size").Int()
	for n := int32(1); n < int32(size); n++ {
		if o, err := d.Get(pdf.Ref{Num: n}); err != nil || o.IsNull() {
			t.Fatalf("object %d: %v %v", n, o, err)
		}
	}
	if d.Repaired() || d.Encrypted() {
		t.Fatalf("repaired %v, encrypted %v", d.Repaired(), d.Encrypted())
	}
	return d
}

func editRender(t *testing.T, d *Document, i int) (*image.RGBA, *Page) {
	t.Helper()
	p, err := d.Page(i)
	if err != nil {
		t.Fatal(err)
	}
	dst := image.NewRGBA(p.Bounds(2))
	err = p.Render(context.Background(), dst, RenderOptions{
		Scale: 2, Background: color.RGBA{255, 255, 255, 255}, Workers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst, p
}

// from is a page of a result and where it came from.
type from struct {
	src []byte
	p   EditPage
}

// editSame checks that each page of out renders like the source page it
// was made from, pixel for pixel; a page turned on the way is checked by
// its /Rotate.
func editSame(t *testing.T, out []byte, pages []from) {
	t.Helper()
	od := editOpen(t, out)
	if od.NumPages() != len(pages) {
		t.Fatalf("%d pages, want %d", od.NumPages(), len(pages))
	}
	for i, f := range pages {
		want, sp := editRender(t, editOpen(t, f.src), f.p.Index)
		got, op := editRender(t, od, i)
		if op.Rotate != pdfedit.Rotation(pdf.Integer(int64(sp.Rotate)), f.p.Rotate) {
			t.Errorf("page %d: /Rotate %d, source %d + %d", i, op.Rotate, sp.Rotate, f.p.Rotate)
		}
		if f.p.Rotate%360 != 0 {
			continue // turned: checked by /Rotate alone
		}
		if want.Bounds() != got.Bounds() || !bytes.Equal(want.Pix, got.Pix) {
			t.Errorf("page %d renders unlike page %d of its source", i, f.p.Index)
		}
	}
}

func sel(src []byte, idx ...int) []from {
	out := make([]from, len(idx))
	for i, x := range idx {
		out[i] = from{src, EditPage{Index: x}}
	}
	return out
}

// TestEditRewrite saves a document as it is: every page, with what the
// catalogue holds around them.
func TestEditRewrite(t *testing.T) {
	src := testpdf.SampleFile(true)
	out := editSave(t, editOpen(t, src).Edit())
	editSame(t, out, sel(src, 0, 1, 2, 3))
	d := editStrict(t, out)
	cat, _ := d.Catalog()
	var keys []string
	for k := range cat.All() {
		keys = append(keys, string(k))
	}
	slices.Sort(keys)
	want := "[AcroForm Lang Names OCProperties Outlines PageLabels Pages StructTreeRoot Type]"
	if fmt.Sprint(keys) != want {
		t.Errorf("catalogue keys %v, want %s", keys, want)
	}

	// A named destination still leads to its page, now in the output.
	names, _ := d.GetDict(cat, "Names")
	dests, _ := d.GetDict(names, "Dests")
	arr, _ := d.Resolve(dests.Get("Names")).Array()
	dest, _ := d.Resolve(arr[1]).Array()
	p1, _ := d.PageRef(2)
	if r, _ := dest[0].Ref(); r != p1 {
		t.Errorf("destination %v, want page 2 (%v)", dest, p1)
	}

	// The first page keeps its structure key, bead and thumbnail, and its
	// annotations their page and popup.
	page, _ := d.Page(1)
	for _, k := range []pdf.Name{"StructParents", "B", "Thumb"} {
		if !page.Has(k) {
			t.Errorf("page 1 lost /%s", k)
		}
	}
	annots, _ := d.Resolve(page.Get("Annots")).Array()
	if len(annots) != 7 {
		t.Errorf("%d annotations, want all 7", len(annots))
	}
	p0, _ := d.PageRef(1)
	for _, a := range annots {
		ad, _ := d.Resolve(a).Dict()
		switch s, _ := ad.Get("Subtype").Name(); s {
		case "Square":
			if r, _ := ad.Get("P").Ref(); r != p0 {
				t.Errorf("square /P %v, want %v", ad.Get("P"), p0)
			}
		case "Popup":
			if ad.Get("Parent").IsNull() {
				t.Error("the popup lost its /Parent")
			}
		}
	}
}

// TestEditImport inserts pages of another file into a document.
func TestEditImport(t *testing.T) {
	base, other := testpdf.SampleFile(false), testpdf.SampleFile(true)
	e := editOpen(t, base).Edit()
	od := editOpen(t, other)
	if err := e.ImportPages(1, od, EditPage{Index: testpdf.PageD}); err != nil {
		t.Fatal(err)
	}
	if err := e.ImportPages(e.NumPages(), od, EditPage{Index: testpdf.PageA, Rotate: 90}); err != nil {
		t.Fatal(err)
	}
	out := editSave(t, e)
	editSame(t, out, []from{
		{base, EditPage{Index: 0}}, {other, EditPage{Index: testpdf.PageD}},
		{base, EditPage{Index: 1}}, {base, EditPage{Index: 2}}, {base, EditPage{Index: 3}},
		{other, EditPage{Index: testpdf.PageA, Rotate: 90}},
	})
	cfg := editOpen(t, out).Layers()
	if cfg == nil || len(cfg.Layers) != 2 || cfg.Layers[0].Visible || cfg.Layers[1].Visible {
		t.Fatalf("layers %+v", cfg)
	}
	d := editStrict(t, out)
	cat, _ := d.Catalog()
	if !cat.Has("Outlines") || !cat.Has("AcroForm") {
		t.Errorf("catalogue %v", cat)
	}
	for _, i := range []int{2, 6} {
		if page, _ := d.Page(i); page.Has("B") || page.Has("StructParents") {
			t.Errorf("imported page %d: %v", i, page)
		}
	}
}

// TestEditImportOwn imports a page of the edited document itself: a copy
// that shares the document's layer and image.
func TestEditImportOwn(t *testing.T) {
	src := testpdf.SampleFile(false)
	doc := editOpen(t, src)
	e := doc.Edit()
	if err := e.ImportPages(4, doc, EditPage{Index: testpdf.PageD}); err != nil {
		t.Fatal(err)
	}
	out := editSave(t, e)
	editSame(t, out, sel(src, 0, 1, 2, 3, testpdf.PageD))
	cfg := editOpen(t, out).Layers()
	if cfg == nil || len(cfg.Layers) != 1 || cfg.Layers[0].Visible {
		t.Fatalf("layers %+v", cfg)
	}
	if n := countImages(t, out); n != 1 {
		t.Errorf("%d images, want the shared one", n)
	}
}

func countImages(t *testing.T, b []byte) int {
	d := editStrict(t, b)
	n := 0
	size, _ := d.Trailer().Get("Size").Int()
	for i := int32(1); i < int32(size); i++ {
		o, _ := d.Get(pdf.Ref{Num: i})
		if s, ok := o.Stream(); ok && s.Dict.Has("Width") && !s.Dict.Has("Type") {
			continue // the thumbnail
		}
		if dict, _ := o.Dict(); dict.Has("Width") {
			n++
		}
	}
	return n
}

// TestEditMerge puts pages of two files into a new document; what the
// pages of one share is copied once.
func TestEditMerge(t *testing.T) {
	a, b := testpdf.SampleFile(false), testpdf.SampleFile(true)
	da, db := editOpen(t, a), editOpen(t, b)
	e := NewEditor()
	for _, step := range []struct {
		d   *Document
		sel []EditPage
	}{
		{da, []EditPage{{Index: testpdf.PageA}}},
		{db, []EditPage{{Index: testpdf.PageD}}},
		{da, []EditPage{{Index: testpdf.PageD}, {Index: testpdf.PageB}}},
	} {
		if err := e.ImportPages(e.NumPages(), step.d, step.sel...); err != nil {
			t.Fatal(err)
		}
	}
	out := editSave(t, e)
	editSame(t, out, []from{
		{a, EditPage{Index: testpdf.PageA}}, {b, EditPage{Index: testpdf.PageD}},
		{a, EditPage{Index: testpdf.PageD}}, {a, EditPage{Index: testpdf.PageB}},
	})
	if n := countImages(t, out); n != 2 {
		t.Errorf("%d images, want one per source", n)
	}
	d := editStrict(t, out)
	info, _ := d.GetDict(d.Trailer(), "Info")
	if title, _ := info.Get("Title").Str(); string(title) != "Sample" {
		t.Errorf("/Info /Title %q", title)
	}
	cfg := editOpen(t, out).Layers()
	if cfg == nil || len(cfg.Layers) != 2 || cfg.Layers[0].Visible || cfg.Layers[1].Visible {
		t.Fatalf("layers %+v", cfg)
	}
}

// TestEditPages deletes, moves and turns pages.
func TestEditPages(t *testing.T) {
	src := testpdf.SampleFile(false)
	e := editOpen(t, src).Edit()
	steps := []error{
		e.DeletePages(1),    // A C D
		e.MovePage(2, 0),    // D A C
		e.RotatePage(1, 90), // D A' C
		e.RotatePage(1, -90),
		e.RotatePage(2, 180), // D A C''
	}
	for i, err := range steps {
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	out := editSave(t, e)
	editSame(t, out, []from{
		{src, EditPage{Index: testpdf.PageD}}, {src, EditPage{Index: testpdf.PageA}},
		{src, EditPage{Index: testpdf.PageC, Rotate: 180}},
	})
	// The named destination pointed at the deleted page.
	d := editStrict(t, out)
	cat, _ := d.Catalog()
	names, _ := d.GetDict(cat, "Names")
	dests, _ := d.GetDict(names, "Dests")
	arr, _ := d.Resolve(dests.Get("Names")).Array()
	if dest, _ := d.Resolve(arr[1]).Array(); len(dest) == 0 || !dest[0].IsNull() {
		t.Errorf("destination %v, want no page", dest)
	}
	// Links on page A to the deleted page B stay on the document's own
	// page: their target is gone, not the annotation.
	page, _ := d.Page(2)
	annots, _ := d.Resolve(page.Get("Annots")).Array()
	if len(annots) != 7 {
		t.Errorf("%d annotations on A", len(annots))
	}
}

// formFile is a two-page file with a field on each page, one with a
// widget on both and one whose kid is on the second page alone.
func formFile() []byte {
	return testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R /AcroForm <</Fields [10 0 R 11 0 R 13 0 R] /CO [10 0 R 15 0 R]>>>>",
			2:  "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 200 100]>>",
			3:  "<</Type /Page /Parent 2 0 R /Annots [10 0 R 12 0 R]>>",
			4:  "<</Type /Page /Parent 2 0 R /Annots [16 0 R 14 0 R 15 0 R]>>",
			10: "<</Type /Annot /Subtype /Widget /FT /Tx /T (a) /Rect [10 10 90 30] /P 3 0 R>>",
			11: "<</FT /Tx /T (b) /Kids [12 0 R 16 0 R]>>",
			12: "<</Type /Annot /Subtype /Widget /Parent 11 0 R /Rect [10 40 90 60] /P 3 0 R>>",
			16: "<</Type /Annot /Subtype /Widget /Parent 11 0 R /Rect [10 70 90 90] /P 4 0 R>>",
			13: "<</T (c) /Kids [14 0 R]>>",
			14: "<</FT /Tx /T (d) /Parent 13 0 R /Kids [15 0 R]>>",
			15: "<</Type /Annot /Subtype /Widget /Parent 14 0 R /Rect [10 10 90 30] /P 4 0 R>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

// TestEditDeleteFields deletes a page and with it the fields whose widgets
// were all on it.
func TestEditDeleteFields(t *testing.T) {
	src := formFile()
	doc := editOpen(t, src)
	if f := doc.Form(); f == nil || len(f.Fields) != 3 {
		t.Fatalf("source form %+v", f)
	}
	e := doc.Edit()
	if err := e.DeletePages(1); err != nil {
		t.Fatal(err)
	}
	out := editSave(t, e)
	f := editOpen(t, out).Form()
	if f == nil {
		t.Fatal("no form")
	}
	var names []string
	for _, fl := range f.Fields {
		names = append(names, fmt.Sprintf("%s:%d", fl.Name, len(fl.Widgets)))
	}
	if fmt.Sprint(names) != "[a:1 b:1]" {
		t.Errorf("fields %v, want a and b with one widget each", names)
	}
	d := editStrict(t, out)
	cat, _ := d.Catalog()
	form, _ := d.GetDict(cat, "AcroForm")
	for _, k := range []pdf.Name{"Fields", "CO"} {
		a, _ := d.Resolve(form.Get(k)).Array()
		for _, x := range a {
			if x.IsNull() {
				t.Errorf("/%s %v has a hole", k, a)
			}
		}
	}
	if co, _ := d.Resolve(form.Get("CO")).Array(); len(co) != 1 {
		t.Errorf("/CO %v", co)
	}
}

// TestEditSaveTwice checks that saving does not change the editor and
// that the output is the same each time.
func TestEditSaveTwice(t *testing.T) {
	e := NewEditor()
	if err := e.ImportPages(0, editOpen(t, testpdf.SampleFile(false)), EditPage{Index: 0}, EditPage{Index: 2}); err != nil {
		t.Fatal(err)
	}
	if first, second := editSave(t, e), editSave(t, e); !bytes.Equal(first, second) {
		t.Fatal("two saves differ")
	}
}

// TestEditID checks the file identifier: made from the content for a new
// document, its first part kept for an edited one.
func TestEditID(t *testing.T) {
	id := func(b []byte) (string, string) {
		d := editStrict(t, b)
		a, _ := d.Resolve(d.Trailer().Get("ID")).Array()
		if len(a) != 2 {
			t.Fatalf("/ID %v", d.Trailer().Get("ID"))
		}
		x, _ := a[0].Str()
		y, _ := a[1].Str()
		return string(x), string(y)
	}
	src := testpdf.Encrypted("", -4) // has an /ID
	doc := editOpen(t, src)
	first, second := id(editSave(t, doc.Edit()))
	sa, _ := doc.r.Resolve(doc.r.Trailer().Get("ID")).Array()
	if s, _ := sa[0].Str(); first != string(s) {
		t.Errorf("first /ID part %x, want the source's %x", first, s)
	}
	if second == first || len(second) != 16 {
		t.Errorf("second /ID part %x", second)
	}
	e := NewEditor()
	if err := e.ImportPages(0, doc, EditPage{Index: 0}); err != nil {
		t.Fatal(err)
	}
	if first, second := id(editSave(t, e)); first != second || len(first) != 16 {
		t.Errorf("/ID %x %x", first, second)
	}
}

func TestEditErrors(t *testing.T) {
	var out bytes.Buffer
	if err := NewEditor().Save(&out); err == nil || out.Len() != 0 {
		t.Errorf("empty document: err %v, %d bytes", err, out.Len())
	}
	src := editOpen(t, testpdf.SampleFile(false))
	e := src.Edit()
	for name, err := range map[string]error{
		"import past the end":   e.ImportPages(0, src, EditPage{Index: 0}, EditPage{Index: 9}),
		"insert past the end":   e.ImportPages(5, src, EditPage{Index: 0}),
		"rotation":              e.ImportPages(0, src, EditPage{Index: 0, Rotate: 45}),
		"delete past the end":   e.DeletePages(0, 4),
		"move past the end":     e.MovePage(0, 4),
		"turn past the end":     e.RotatePage(-1, 90),
		"turn by an odd amount": e.RotatePage(0, 30),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if e.NumPages() != 4 {
		t.Errorf("failed calls changed the editor: %d pages", e.NumPages())
	}

	locked := editOpen(t, testpdf.Encrypted("", -4&^(1<<10)))
	if err := locked.Edit().Save(&out); !errors.Is(err, ErrNoAssembly) || out.Len() != 0 {
		t.Errorf("no assembly: err %v, %d bytes", err, out.Len())
	}
	if err := e.ImportPages(0, locked, EditPage{Index: 0}); !errors.Is(err, ErrNoAssembly) {
		t.Errorf("import without assembly: err %v", err)
	}
}

// failing fails after n bytes.
type failing struct{ n int }

var errFull = errors.New("full")

func (f *failing) Write(p []byte) (int, error) {
	if len(p) > f.n {
		n := f.n
		f.n = 0
		return n, errFull
	}
	f.n -= len(p)
	return len(p), nil
}

func TestEditWriteError(t *testing.T) {
	e := editOpen(t, testpdf.SampleFile(false)).Edit()
	for _, n := range []int{0, 100, 1000} {
		if err := e.Save(&failing{n: n}); !errors.Is(err, errFull) {
			t.Errorf("after %d bytes: err %v", n, err)
		}
	}
}

func FuzzEdit(f *testing.F) {
	f.Add(testpdf.SampleFile(false), uint8(1), uint8(2))
	f.Add(testpdf.SampleFile(true), uint8(0), uint8(3))
	f.Add(formFile(), uint8(1), uint8(0))
	f.Add(fieldsFile(true), uint8(2), uint8(1))
	f.Add(testpdf.EncryptedWith("aes256", "", -4), uint8(1), uint8(0))
	f.Add(testpdf.Encrypted("", -4), uint8(0), uint8(1))
	f.Fuzz(func(t *testing.T, data []byte, a, b uint8) {
		d, err := Open(data)
		if err != nil || d.NumPages() == 0 {
			return
		}
		n := d.NumPages()
		e := d.Edit()
		// The calls may fail; the editor is then unchanged.
		if n > 1 {
			_ = e.DeletePages(int(a) % n)
		}
		_ = e.ImportPages(0, d, EditPage{Index: int(b) % n, Rotate: int(a%4) * 90})
		_ = e.MovePage(0, e.NumPages()-1)
		if form := d.Form(); form != nil {
			state := form.NewState()
			for _, f := range form.Fields {
				_ = state.SetValue(f, TextValue(string(rune('a'+a%26))))
				_ = state.SetValue(f, ChoiceValue(int(b)%max(len(f.Options), 1)))
			}
			_ = e.SetFields(state)
		}
		var out bytes.Buffer
		if e.Save(&out) != nil {
			return
		}
		o, err := pdf.Open(out.Bytes())
		if err != nil {
			t.Fatalf("the output does not open: %v", err)
		}
		if o.PageCount() != e.NumPages() || o.Repaired() {
			t.Fatalf("%d pages, want %d; repaired %v", o.PageCount(), e.NumPages(), o.Repaired())
		}
		out.Reset()
		if e.Update(&out) != nil {
			return
		}
		if !bytes.HasPrefix(out.Bytes(), data) {
			t.Fatal("the update does not start with the file")
		}
		o, err = pdf.Open(out.Bytes())
		if err != nil {
			t.Fatalf("the update does not open: %v", err)
		}
		if o.PageCount() != e.NumPages() || o.Repaired() {
			t.Fatalf("update: %d pages, want %d; repaired %v", o.PageCount(), e.NumPages(), o.Repaired())
		}
	})
}
