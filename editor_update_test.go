package cera

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// editUpdate updates e and checks that the result starts with the edited
// file and reads without a repair.
func editUpdate(t *testing.T, e *Editor, src []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := e.Update(&out); err != nil {
		t.Fatal(err)
	}
	b := out.Bytes()
	if !bytes.HasPrefix(b, src) {
		t.Fatal("the update does not start with the file")
	}
	d, err := pdf.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	if d.Repaired() {
		t.Fatalf("the update needed a repair:\n%s", b[len(src):])
	}
	return b
}

// sameAsSave checks that an update renders as a new file of the same
// editor does, page by page.
func sameAsSave(t *testing.T, e *Editor, upd []byte) {
	t.Helper()
	saved := editSave(t, e)
	ud, sd := editOpen(t, upd), editOpen(t, saved)
	if ud.NumPages() != sd.NumPages() {
		t.Fatalf("%d pages, saved %d", ud.NumPages(), sd.NumPages())
	}
	for i := range ud.NumPages() {
		got, up := editRender(t, ud, i)
		want, sp := editRender(t, sd, i)
		if up.Rotate != sp.Rotate || got.Bounds() != want.Bounds() || !bytes.Equal(got.Pix, want.Pix) {
			t.Errorf("page %d renders unlike the saved file", i)
		}
	}
}

func TestUpdateUnchanged(t *testing.T) {
	src := testpdf.SampleFile(true)
	var out bytes.Buffer
	if err := editOpen(t, src).Edit().Update(&out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), src) {
		t.Fatal("an unchanged document is not written as it is")
	}
}

func TestUpdatePages(t *testing.T) {
	other := testpdf.SampleFile(true)
	for _, stm := range []bool{false, true} {
		t.Run(fmt.Sprintf("xrefstream=%v", stm), func(t *testing.T) {
			src := testpdf.SampleFile(stm)
			doc := editOpen(t, src)
			e := doc.Edit()
			od := editOpen(t, other)
			for i, err := range []error{
				e.DeletePages(1),
				e.MovePage(2, 0),
				e.RotatePage(1, 90),
				e.ImportPages(1, od, EditPage{Index: testpdf.PageD}),
				e.ImportPages(0, doc, EditPage{Index: testpdf.PageA, Rotate: 180}),
			} {
				if err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
			}
			upd := editUpdate(t, e, src)
			sameAsSave(t, e, upd)

			tail := string(upd[len(src):])
			if got := strings.Contains(tail, "/Type /XRef"); got != stm {
				t.Errorf("cross-reference stream %v, want %v:\n%s", got, stm, tail)
			}
			if !stm && !strings.Contains(tail, "\nxref\n") {
				t.Errorf("no cross-reference table:\n%s", tail)
			}
			d, _ := pdf.Open(upd)
			// The deleted page B is free: what pointed at it reads null.
			cat, _ := d.Catalog()
			names, _ := d.GetDict(cat, "Names")
			dests, _ := d.GetDict(names, "Dests")
			arr, _ := d.Resolve(dests.Get("Names")).Array()
			dest, _ := d.Resolve(arr[1]).Array()
			if !d.Resolve(dest[0]).IsNull() {
				t.Errorf("destination %v still leads to the deleted page", dest)
			}
			// The layers of both files.
			cfg := editOpen(t, upd).Layers()
			if cfg == nil || len(cfg.Layers) != 2 || cfg.Layers[0].Visible || cfg.Layers[1].Visible {
				t.Errorf("layers %+v", cfg)
			}
			// Outline and form stay.
			if !cat.Has("Outlines") || !cat.Has("AcroForm") {
				t.Errorf("catalogue %v", cat)
			}
		})
	}
}

// TestUpdateFields deletes a page with an update: fields whose widgets
// were all on it are freed, the others keep the kids that are left.
func TestUpdateFields(t *testing.T) {
	src := formFile()
	e := editOpen(t, src).Edit()
	if err := e.DeletePages(1); err != nil {
		t.Fatal(err)
	}
	upd := editUpdate(t, e, src)
	f := editOpen(t, upd).Form()
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
	d, _ := pdf.Open(upd)
	for _, n := range []int32{4, 13, 14, 15, 16} {
		if o, _ := d.Get(pdf.Ref{Num: n}); !o.IsNull() {
			t.Errorf("object %d is not free: %v", n, o)
		}
	}
	b, _ := d.Get(pdf.Ref{Num: 11})
	bd, _ := b.Dict()
	if kids, _ := d.Resolve(bd.Get("Kids")).Array(); len(kids) != 1 {
		t.Errorf("field b /Kids %v", kids)
	}
}

// TestUpdateTwice updates an update: the sections chain.
func TestUpdateTwice(t *testing.T) {
	src := testpdf.SampleFile(false)
	e := editOpen(t, src).Edit()
	if err := e.DeletePages(3); err != nil {
		t.Fatal(err)
	}
	first := editUpdate(t, e, src)
	e2 := editOpen(t, first).Edit()
	if err := e2.MovePage(0, 2); err != nil {
		t.Fatal(err)
	}
	second := editUpdate(t, e2, first)
	sameAsSave(t, e2, second)
	if n := strings.Count(string(second), "startxref"); n != 3 {
		t.Errorf("%d sections, want 3", n)
	}
}

// TestUpdateGenerations keeps the generations of the file's objects in the
// references an update writes.
func TestUpdateGenerations(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	objs := []struct {
		num, gen int
		body     string
	}{
		{1, 0, "<</Type /Catalog /Pages 2 0 R>>"},
		{2, 0, "<</Type /Pages /Kids [3 2 R 4 0 R] /Count 2 /MediaBox [0 0 100 100]>>"},
		{3, 2, "<</Type /Page /Parent 2 0 R /Contents 5 1 R>>"},
		{4, 0, "<</Type /Page /Parent 2 0 R>>"},
		{5, 1, "<</Length 26>>\nstream\n1 0 0 rg 0 0 50 50 re f\n\nendstream"},
	}
	offs := map[int]int{}
	for _, o := range objs {
		offs[o.num] = b.Len()
		fmt.Fprintf(&b, "%d %d obj\n%s\nendobj\n", o.num, o.gen, o.body)
	}
	xref := b.Len()
	b.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for _, o := range objs {
		fmt.Fprintf(&b, "%010d %05d n \n", offs[o.num], o.gen)
	}
	fmt.Fprintf(&b, "trailer\n<</Size 6 /Root 1 0 R>>\nstartxref\n%d\n%%%%EOF", xref)
	src := b.Bytes()

	e := editOpen(t, src).Edit()
	if err := e.DeletePages(1); err != nil {
		t.Fatal(err)
	}
	if err := e.RotatePage(0, 90); err != nil {
		t.Fatal(err)
	}
	upd := editUpdate(t, e, src)
	tail := string(upd[len(src):])
	if strings.Contains(tail, "/Version") {
		t.Errorf("the update raises the version:\n%s", tail)
	}
	for _, want := range []string{"3 2 obj", "5 1 R", "00001 f"} {
		if !strings.Contains(tail, want) {
			t.Errorf("no %q in the update:\n%s", want, tail)
		}
	}
	sameAsSave(t, e, upd)
}

func TestUpdateErrors(t *testing.T) {
	var out bytes.Buffer
	ne := NewEditor()
	if err := ne.ImportPages(0, editOpen(t, testpdf.SampleFile(false)), EditPage{Index: 0}); err != nil {
		t.Fatal(err)
	}
	src := testpdf.SampleFile(false)
	shifted := append([]byte("%PDF-1.7\n\n\n"), src[len("%PDF-1.7\n"):]...) // offsets off by two
	for name, e := range map[string]*Editor{
		"new":       ne,
		"encrypted": editOpen(t, testpdf.Encrypted("", -4)).Edit(),
		"repaired":  editOpen(t, shifted).Edit(),
	} {
		if err := e.Update(&out); !errors.Is(err, ErrNoUpdate) || out.Len() != 0 {
			t.Errorf("%s: err %v, %d bytes", name, err, out.Len())
		}
	}
	e := editOpen(t, src).Edit()
	if err := e.DeletePages(0); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 100, len(src) + 50} {
		if err := e.Update(&failing{n: n}); !errors.Is(err, errFull) {
			t.Errorf("after %d bytes: err %v", n, err)
		}
	}
}
