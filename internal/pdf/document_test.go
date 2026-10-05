package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestOpenSimple(t *testing.T) {
	d := mustOpen(t, file{objs: onePage("0 0 m 10 10 l S"), trailer: "/Root 1 0 R"}.bytes())
	if d.Repaired() {
		t.Error("a well-formed file was repaired")
	}
	if d.Version() != "1.7" {
		t.Errorf("version %q", d.Version())
	}
	if n := d.PageCount(); n != 1 {
		t.Fatalf("%d pages", n)
	}
	p, err := d.Page(1)
	if err != nil {
		t.Fatal(err)
	}
	// MediaBox is inherited from the page tree node.
	if mb, ok := p.Get("MediaBox").Array(); !ok || len(mb) != 4 {
		t.Errorf("MediaBox %v", p.Get("MediaBox"))
	}
	c, _ := d.Resolve(p.Get("Contents")).Stream()
	if got := string(d.Decode(c).Data); got != "0 0 m 10 10 l S" {
		t.Errorf("contents %q", got)
	}
}

func TestRepairWrongOffsets(t *testing.T) {
	b := file{objs: onePage("q Q"), trailer: "/Root 1 0 R", shift: 7}.bytes()
	d := mustOpen(t, b)
	if !d.Repaired() {
		t.Error("shifted offsets were not repaired")
	}
	if d.PageCount() != 1 {
		t.Fatalf("%d pages", d.PageCount())
	}
}

func TestRepairWithoutXref(t *testing.T) {
	d := mustOpen(t, file{objs: onePage("q Q"), trailer: "/Root 1 0 R", noXref: true}.bytes())
	if !d.Repaired() || d.PageCount() != 1 {
		t.Fatalf("repaired %v, %d pages", d.Repaired(), d.PageCount())
	}
}

func TestSynthesisedCatalogue(t *testing.T) {
	objs := onePage("q Q")
	delete(objs, 1)
	d := mustOpen(t, file{objs: objs, noXref: true}.bytes())
	if d.PageCount() != 1 {
		t.Fatalf("%d pages from loose page objects", d.PageCount())
	}
}

func TestStreamLengthIndirectAndWrong(t *testing.T) {
	objs := onePage("")
	objs[4] = "<</Length 5 0 R>>\nstream\nhello world\nendstream"
	objs[5] = "11"
	d := mustOpen(t, file{objs: objs, trailer: "/Root 1 0 R"}.bytes())
	s, _ := mustGet(t, d, 4).Stream()
	if got := string(d.Raw(s)); got != "hello world" {
		t.Errorf("indirect length: %q", got)
	}

	objs[5] = "3" // wrong: endstream is searched for
	d = mustOpen(t, file{objs: objs, trailer: "/Root 1 0 R"}.bytes())
	s, _ = mustGet(t, d, 4).Stream()
	if got := string(d.Raw(s)); got != "hello world" {
		t.Errorf("wrong length: %q", got)
	}
}

func TestSelfReferentialLength(t *testing.T) {
	objs := onePage("")
	objs[4] = "<</Length 4 0 R>>\nstream\nabc\nendstream"
	d := mustOpen(t, file{objs: objs, trailer: "/Root 1 0 R"}.bytes())
	s, ok := mustGet(t, d, 4).Stream()
	if !ok || string(d.Raw(s)) != "abc" {
		t.Errorf("stream whose length is itself: %v", mustGet(t, d, 4))
	}
}

func mustGet(t *testing.T, d *Document, num int32) Object {
	t.Helper()
	o, err := d.Get(Ref{Num: num})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// objStmFile builds a file whose catalogue and page tree live in an object
// stream, with a cross-reference stream.
func objStmFile(t *testing.T, hybridFree bool) []byte {
	t.Helper()
	members := []string{
		"<</Type /Catalog /Pages 2 0 R>>",
		"<</Type /Pages /Kids [3 0 R] /Count 1>>",
		"<</Type /Page /Parent 2 0 R /MediaBox [0 0 10 10]>>",
	}
	var hdr, body bytes.Buffer
	for i, m := range members {
		fmt.Fprintf(&hdr, "%d %d ", i+1, body.Len())
		body.WriteString(m + "\n")
	}
	first := hdr.Len()
	data := append(hdr.Bytes(), body.Bytes()...)

	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	off4 := b.Len()
	fmt.Fprintf(&b, "4 0 obj\n%s\nendobj\n", stream(fmt.Sprintf("/Type /ObjStm /N 3 /First %d", first), data))

	// Cross-reference stream: object 0 free, 1-3 in stream 4, 4 at off4,
	// 5 the xref stream itself.
	off5 := b.Len()
	var rows []byte
	row := func(t, f2, f3 int) { rows = append(rows, byte(t), byte(f2>>8), byte(f2), byte(f3)) }
	row(0, 0, 255)
	for i := range 3 {
		row(2, 4, i)
	}
	row(1, off4, 0)
	row(1, off5, 0)
	xs := stream("/Type /XRef /Size 6 /W [1 2 1] /Root 1 0 R", rows)
	if !hybridFree {
		fmt.Fprintf(&b, "5 0 obj\n%s\nendobj\nstartxref\n%d\n%%%%EOF\n", xs, off5)
		return b.Bytes()
	}
	// Hybrid: a classic table that lists 1-3 as free, pointing at the
	// stream through /XRefStm.
	fmt.Fprintf(&b, "5 0 obj\n%s\nendobj\n", xs)
	tab := b.Len()
	fmt.Fprintf(&b, "xref\n0 6\n0000000000 65535 f \n0000000000 65535 f \n0000000000 65535 f \n0000000000 65535 f \n%010d 00000 n \n%010d 00000 n \n", off4, off5)
	fmt.Fprintf(&b, "trailer\n<</Size 6 /Root 1 0 R /XRefStm %d>>\nstartxref\n%d\n%%%%EOF\n", off5, tab)
	return b.Bytes()
}

func TestObjectStream(t *testing.T) {
	d := mustOpen(t, objStmFile(t, false))
	if d.Repaired() {
		t.Error("repaired")
	}
	if d.PageCount() != 1 {
		t.Fatalf("%d pages", d.PageCount())
	}
	p, _ := d.Page(1)
	if mb, _ := p.Get("MediaBox").Array(); len(mb) != 4 {
		t.Errorf("page %v", p)
	}
}

func TestHybridFreeEntryDoesNotHideStream(t *testing.T) {
	d := mustOpen(t, objStmFile(t, true))
	if d.Repaired() {
		t.Error("a hybrid file was repaired: its /XRefStm entries were hidden")
	}
	if d.PageCount() != 1 {
		t.Fatalf("%d pages", d.PageCount())
	}
}

func TestConcurrentGet(t *testing.T) {
	objs := onePage("q Q")
	for i := 5; i < 200; i++ {
		objs[i] = fmt.Sprintf("<</N %d /A [%d 0 R 1 2.5 (s%d)] /Next %d 0 R>>", i, i-1, i, i+1)
	}
	b := file{objs: objs, trailer: "/Root 1 0 R"}.bytes()
	d := mustOpen(t, b)
	var wg sync.WaitGroup
	results := make([][]string, 8)
	for g := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 199; i >= 1; i-- {
				o, err := d.Get(Ref{Num: int32((i*(g+1))%199 + 1)})
				if err != nil {
					t.Error(err)
					return
				}
				results[g] = append(results[g], o.String())
			}
		}()
	}
	wg.Wait()
	// Every goroutine sees the same object for the same number.
	want := map[int32]Object{}
	for i := int32(1); i < 200; i++ {
		want[i] = mustGet(t, d, i)
	}
	for i := int32(1); i < 200; i++ {
		if a, b := mustGet(t, d, i), want[i]; a != b {
			t.Fatalf("object %d loaded twice: %v and %v", i, a, b)
		}
	}
}

func TestConcurrentRepair(t *testing.T) {
	b := file{objs: onePage("q Q"), trailer: "/Root 1 0 R", shift: 3}.bytes()
	for range 20 {
		d, err := Open(b)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for g := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := int32(1); i <= 4; i++ {
					d.Get(Ref{Num: (i+int32(g))%4 + 1})
				}
				d.PageCount()
			}()
		}
		wg.Wait()
		if d.PageCount() != 1 {
			t.Fatal("pages lost under concurrent repair")
		}
	}
}

func TestGetSteadyStateAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector empties sync.Pool")
	}
	d := mustOpen(t, file{objs: onePage("q Q"), trailer: "/Root 1 0 R"}.bytes())
	for i := int32(1); i <= 4; i++ {
		mustGet(t, d, i)
	}
	n := testing.AllocsPerRun(100, func() {
		for i := int32(1); i <= 4; i++ {
			o, _ := d.Get(Ref{Num: i})
			dict, _ := o.Dict()
			dict.Get("Type")
			d.Resolve(dict.Get("Pages"))
		}
	})
	if n != 0 {
		t.Errorf("%v allocations per lookup of loaded objects", n)
	}
}

func TestDeepNesting(t *testing.T) {
	objs := onePage("")
	objs[5] = strings.Repeat("[", 100000) + strings.Repeat("]", 100000)
	d := mustOpen(t, file{objs: objs, trailer: "/Root 1 0 R"}.bytes())
	// Too deep to parse: null, not a stack overflow.
	if o := mustGet(t, d, 5); !o.IsNull() {
		t.Errorf("got %v", o.Kind())
	}
}

func TestHugeObjectNumber(t *testing.T) {
	b := []byte("%PDF-1.4\n1 0 obj <</Type /Catalog /Pages 2147483647 0 R>> endobj\n" +
		"2147483647 0 obj <</Type /Pages /Kids [] /Count 0>> endobj\ntrailer <</Root 1 0 R>>\n")
	d := mustOpen(t, b)
	if c, err := d.Catalog(); err != nil {
		t.Fatal(err)
	} else if _, ok := d.GetDict(c, "Pages"); !ok {
		t.Error("object 2147483647 not found")
	}
}
