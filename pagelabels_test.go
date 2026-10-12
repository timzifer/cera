package cera

import (
	"fmt"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
)

// labelsFile is a document of n pages with the given /PageLabels and more
// objects.
func labelsFile(n int, labels string, objs map[int]string) []byte {
	all := map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R /PageLabels " + labels + ">>",
	}
	var kids strings.Builder
	for i := range n {
		fmt.Fprintf(&kids, "%d 0 R ", 100+i)
		all[100+i] = fmt.Sprintf("<</Type /Page /Parent 2 0 R /Contents %d 0 R>>", 500+i)
		// What the page shows tells it from the others.
		all[500+i] = testpdf.Stream("", []byte(fmt.Sprintf("%d 0 0 rg 0 0 %d 10 re f", i%2, 10+i)))
	}
	all[2] = fmt.Sprintf("<</Type /Pages /Kids [%s] /Count %d /MediaBox [0 0 200 100]>>", kids.String(), n)
	for k, v := range objs {
		all[k] = v
	}
	return testpdf.File{Objs: all, Trailer: "/Root 1 0 R"}.Bytes()
}

// pageLabels returns the labels of the pages of d, separated by spaces;
// "-" stands for none.
func pageLabels(d *Document) string {
	var s []string
	for i := range d.NumPages() {
		l := d.PageLabel(i)
		if l == "" {
			l = "-"
		}
		s = append(s, l)
	}
	return strings.Join(s, " ")
}

func TestPageLabels(t *testing.T) {
	// Roman front matter, arabic numbers, an appendix with a prefix, and
	// letters that start at Y.
	doc := editOpen(t, labelsFile(12, "<</Nums [0 <</S /r>> 3 <</S /D>> 6 <</S /D /P (A-) /St 8>> 8 <</S /A /St 25>> 11 <</P (Index)>>]>>", nil))
	if got, want := pageLabels(doc), "i ii iii 1 2 3 A-8 A-9 Y Z AA Index"; got != want {
		t.Errorf("labels %q, want %q", got, want)
	}
	for label, want := range map[string]int{"i": 0, "iii": 2, "1": 3, "A-9": 7, "AA": 10, "Index": 11} {
		if got, ok := doc.PageByLabel(label); !ok || got != want {
			t.Errorf("PageByLabel(%q) = %d, %v, want %d", label, got, ok, want)
		}
	}
	for _, label := range []string{"", "iv", "4", "a-8", "12"} {
		if got, ok := doc.PageByLabel(label); ok {
			t.Errorf("PageByLabel(%q) = %d", label, got)
		}
	}
	if doc.PageLabel(-1) != "" || doc.PageLabel(12) != "" {
		t.Error("a label for a page the document does not have")
	}
	plain := editOpen(t, fieldsFile(false))
	if plain.PageLabel(0) != "" {
		t.Errorf("a document without page labels has the label %q", plain.PageLabel(0))
	}
	if _, ok := plain.PageByLabel("1"); ok {
		t.Error("PageByLabel in a document without page labels")
	}
	if got := roman(3999) + " " + roman(4004) + " " + roman(1994); got != "MMMCMXCIX MMMMIV MCMXCIV" {
		t.Errorf("roman numerals %q", got)
	}
}

// TestPageLabelsLenient reads a tree that is not as it should be: no entry
// for the first page, entries out of order and twice, a start below one, a
// page the document does not have, kids that lead back to their parent.
func TestPageLabelsLenient(t *testing.T) {
	doc := editOpen(t, labelsFile(8, "20 0 R", map[int]string{
		20: "<</Kids [21 0 R 22 0 R 20 0 R]>>",
		21: "<</Nums [5 <</S /D /St -4>> 2 <</S /R>> 2 <</S /a>> 99 <</S /D>> (x) <</S /D>> -1 <</S /D>>] /Kids [20 0 R]>>",
		22: "<</Nums [6 7 7 <</S /Q /P (p)>>]>>",
	}))
	if got, want := pageLabels(doc), "- - a b c 1 - p"; got != want {
		t.Errorf("labels %q, want %q", got, want)
	}
	if doc.PageLabel(0) != "" {
		t.Error("a label before the first range")
	}
}

// TestPageLabelsEdited keeps the labels with their pages when the editor
// deletes, moves and inserts pages.
func TestPageLabelsEdited(t *testing.T) {
	// A cover, i ii, then 1 2 3.
	src := labelsFile(6, "<</Nums [0 <</P (Cover)>> 1 <</S /r>> 3 <</S /D>>]>>", nil)
	doc := editOpen(t, src)
	if got := pageLabels(doc); got != "Cover i ii 1 2 3" {
		t.Fatalf("labels %q", got)
	}
	other := editOpen(t, labelsFile(2, "<</Nums [0 <</S /A>>]>>", nil))
	for _, tt := range []struct {
		name string
		edit func(e *Editor) error
		want string
	}{
		{"delete the cover", func(e *Editor) error { return e.DeletePages(0) }, "i ii 1 2 3"},
		{"delete in a range", func(e *Editor) error { return e.DeletePages(4) }, "Cover i ii 1 3"},
		{"move the last page to the front", func(e *Editor) error { return e.MovePage(5, 0) }, "3 Cover i ii 1 2"},
		{"insert from another document", func(e *Editor) error { return e.ImportPages(4, other, EditPage{Index: 0}, EditPage{Index: 1}) }, "Cover i ii 1 - - 2 3"},
		{"a copy of a page", func(e *Editor) error { return e.ImportPages(6, doc, EditPage{Index: 1}) }, "Cover i ii 1 2 3 -"},
		{"turn a page", func(e *Editor) error { return e.RotatePage(2, 90) }, "Cover i ii 1 2 3"},
	} {
		for _, mode := range []string{"save", "update"} {
			e := doc.Edit()
			if err := tt.edit(e); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			var out []byte
			if mode == "save" {
				out = editSave(t, e)
			} else {
				out = editUpdate(t, e, src)
			}
			if got := pageLabels(editOpen(t, out)); got != tt.want {
				t.Errorf("%s, %s: labels %q, want %q", tt.name, mode, got, tt.want)
			}
		}
	}
	// A document made of the pages of others has no labels.
	e := NewEditor()
	if err := e.ImportPages(0, doc, EditPage{Index: 3}); err != nil {
		t.Fatal(err)
	}
	if got := pageLabels(editOpen(t, editSave(t, e))); got != "-" {
		t.Errorf("a new document: labels %q", got)
	}
}
