package pdfedit

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
)

func save(t *testing.T, doc *Document) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := doc.Save(&out, SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	strict(t, out.Bytes())
	return out.Bytes()
}

func source(t *testing.T, b []byte) *Source {
	t.Helper()
	s, err := OpenSource(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// part is a page of the output and where it came from.
type part struct {
	src []byte
	p   Page
}

// sameRendersFrom checks that each output page renders like the source
// page it was made from.
func sameRendersFrom(t *testing.T, out []byte, parts []part) {
	t.Helper()
	if n := open(t, out).NumPages(); n != len(parts) {
		t.Fatalf("%d pages, want %d", n, len(parts))
	}
	for i, pt := range parts {
		// Extract the one page for the comparison sameRenders makes.
		var one bytes.Buffer
		doc, err := Open(out)
		if err != nil {
			t.Fatal(err)
		}
		s := doc.base
		single := New()
		if err := single.ImportPages(s, []Page{{Index: i}}); err != nil {
			t.Fatal(err)
		}
		if err := single.Save(&one, SaveOptions{}); err != nil {
			t.Fatal(err)
		}
		t.Run(fmt.Sprint("page ", i), func(t *testing.T) {
			sameRenders(t, pt.src, one.Bytes(), []Page{pt.p})
		})
	}
}

// TestOpenRewrite saves a document as it was opened: every page, with what
// the catalogue holds around them.
func TestOpenRewrite(t *testing.T) {
	src := sampleFile(true)
	doc, err := Open(src)
	if err != nil {
		t.Fatal(err)
	}
	out := save(t, doc)
	sameRenders(t, src, out, pages(pageA, pageB, pageC, pageD))
	d := strict(t, out)
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
	p0, _ := d.PageRef(1)
	var square, popup pdf.Dict
	for _, a := range annots {
		ad, _ := d.Resolve(a).Dict()
		switch s, _ := ad.Get("Subtype").Name(); s {
		case "Square":
			square = ad
		case "Popup":
			popup = ad
		}
	}
	if r, _ := square.Get("P").Ref(); r != p0 {
		t.Errorf("square /P %v, want %v", square.Get("P"), p0)
	}
	if popup.Get("Parent").IsNull() {
		t.Error("the popup lost its /Parent")
	}
	if n := len(annots); n != 7 {
		t.Errorf("%d annotations, want all 7", n)
	}
}

// TestOpenImport adds a page of another file to an opened document.
func TestOpenImport(t *testing.T) {
	base, other := sampleFile(false), sampleFile(true)
	doc, err := Open(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.ImportPages(source(t, other), []Page{{Index: pageD}, {Index: pageA, Rotate: 90}}); err != nil {
		t.Fatal(err)
	}
	if doc.NumPages() != 6 {
		t.Fatalf("%d pages", doc.NumPages())
	}
	out := save(t, doc)
	sameRendersFrom(t, out, []part{
		{base, Page{Index: pageA}}, {base, Page{Index: pageB}}, {base, Page{Index: pageC}}, {base, Page{Index: pageD}},
		{other, Page{Index: pageD}}, {other, Page{Index: pageA, Rotate: 90}},
	})
	cfg := open(t, out).Layers()
	if cfg == nil || len(cfg.Layers) != 2 {
		t.Fatalf("layers %+v", cfg)
	}
	for _, l := range cfg.Layers {
		if l.Visible {
			t.Errorf("layer %q shows", l.Name)
		}
	}
	// The outline and form of the opened file stay; the imported page
	// brings none.
	d := strict(t, out)
	cat, _ := d.Catalog()
	if !cat.Has("Outlines") || !cat.Has("AcroForm") {
		t.Errorf("catalogue %v", cat)
	}
	page, _ := d.Page(6)
	if page.Has("B") || page.Has("StructParents") {
		t.Errorf("imported page %v", page)
	}
}

// TestMerge puts pages of two sources into a new document and checks that
// what the pages of one source share is copied once.
func TestMerge(t *testing.T) {
	a, b := sampleFile(false), sampleFile(true)
	sa, sb := source(t, a), source(t, b)
	doc := New()
	for _, step := range []struct {
		s   *Source
		sel []Page
	}{
		{sa, pages(pageA)}, {sb, pages(pageD)}, {sa, pages(pageD, pageB)},
	} {
		if err := doc.ImportPages(step.s, step.sel); err != nil {
			t.Fatal(err)
		}
	}
	out := save(t, doc)
	sameRendersFrom(t, out, []part{
		{a, Page{Index: pageA}}, {b, Page{Index: pageD}}, {a, Page{Index: pageD}}, {a, Page{Index: pageB}},
	})
	d := strict(t, out)
	images := 0
	size, _ := d.Trailer().Get("Size").Int()
	for n := int32(1); n < int32(size); n++ {
		o, _ := d.Get(pdf.Ref{Num: n})
		if dict, _ := o.Dict(); dict.Has("Width") {
			images++
		}
	}
	if images != 2 {
		t.Errorf("%d images, want one per source", images)
	}
	info, _ := d.GetDict(d.Trailer(), "Info")
	if title, _ := info.Get("Title").Str(); string(title) != "Sample" {
		t.Errorf("/Info /Title %q", title)
	}
	cfg := open(t, out).Layers()
	if cfg == nil || len(cfg.Layers) != 2 || cfg.Layers[0].Visible || cfg.Layers[1].Visible {
		t.Fatalf("layers %+v", cfg)
	}
}

// TestSaveTwice checks that saving does not change the document and that
// the output is the same each time.
func TestSaveTwice(t *testing.T) {
	doc := New()
	if err := doc.ImportPages(source(t, sampleFile(false)), pages(pageA, pageC)); err != nil {
		t.Fatal(err)
	}
	first, second := save(t, doc), save(t, doc)
	if !bytes.Equal(first, second) {
		t.Fatal("two saves differ")
	}
}

// TestID checks the file identifier: made from the content for a new
// document, its first part kept for an opened one.
func TestID(t *testing.T) {
	id := func(b []byte) (string, string) {
		d := strict(t, b)
		a, _ := d.Resolve(d.Trailer().Get("ID")).Array()
		if len(a) != 2 {
			t.Fatalf("/ID %v", d.Trailer().Get("ID"))
		}
		x, _ := a[0].Str()
		y, _ := a[1].Str()
		return string(x), string(y)
	}
	src := encrypted("", -4) // has an /ID
	doc, err := Open(src)
	if err != nil {
		t.Fatal(err)
	}
	first, second := id(save(t, doc))
	sd, _ := pdf.Open(src)
	sa, _ := sd.Resolve(sd.Trailer().Get("ID")).Array()
	if s, _ := sa[0].Str(); first != string(s) {
		t.Errorf("first /ID part %x, want the source's %x", first, s)
	}
	if second == first || len(second) != 16 {
		t.Errorf("second /ID part %x", second)
	}

	n := New()
	if err := n.ImportPages(source(t, src), pages(0)); err != nil {
		t.Fatal(err)
	}
	if first, second := id(save(t, n)); first != second || len(first) != 16 {
		t.Errorf("/ID %x %x", first, second)
	}
}

func TestDocumentErrors(t *testing.T) {
	var out bytes.Buffer
	if err := New().Save(&out, SaveOptions{}); err == nil || out.Len() != 0 {
		t.Errorf("empty document: err %v, %d bytes", err, out.Len())
	}
	doc := New()
	s := source(t, sampleFile(false))
	if err := doc.ImportPages(s, []Page{{Index: 0}, {Index: 9}}); err == nil {
		t.Error("page out of range accepted")
	}
	if doc.NumPages() != 0 {
		t.Errorf("a failed import added %d pages", doc.NumPages())
	}
	if err := doc.ImportPages(s, pages(0)); err != nil {
		t.Fatal(err)
	}
	if err := doc.Save(&out, SaveOptions{Mode: 9}); err == nil || out.Len() != 0 {
		t.Errorf("unknown mode: err %v, %d bytes", err, out.Len())
	}
	locked := encrypted("", -4&^(1<<10))
	if _, err := Open(locked); err != nil {
		t.Fatal(err) // it opens; saving it is refused
	}
	ld, _ := Open(locked)
	if err := ld.Save(&out, SaveOptions{}); !errors.Is(err, ErrEncrypted) || out.Len() != 0 {
		t.Errorf("no assembly: err %v, %d bytes", err, out.Len())
	}
	if err := doc.ImportPages(source(t, locked), pages(0)); !errors.Is(err, ErrEncrypted) {
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

func TestWriteError(t *testing.T) {
	doc, err := Open(sampleFile(false))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 100, 1000} {
		if err := doc.Save(&failing{n: n}, SaveOptions{}); !errors.Is(err, errFull) {
			t.Errorf("after %d bytes: err %v", n, err)
		}
	}
}
