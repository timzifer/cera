package pdfwrite

import (
	"bufio"
	"bytes"
	"fmt"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
)

// source builds a PDF from numbered object bodies from 2 on; object 1 is
// the catalogue, with an empty page tree after the given objects.
func source(t *testing.T, objs ...string) *pdf.Document {
	t.Helper()
	objs = append([]string{fmt.Sprintf("<</Type/Catalog /Pages %d 0 R>>", len(objs)+2)}, objs...)
	objs = append(objs, "<</Type/Pages /Kids [] /Count 0>>")
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offs := make([]int, len(objs))
	for i, body := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<</Size %d /Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	d, err := pdf.Open(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// open reads an output file strictly.
func open(t *testing.T, b []byte) *pdf.Document {
	t.Helper()
	d, err := pdf.Open(b)
	if err != nil {
		t.Fatalf("the output does not open: %v\n%s", err, b)
	}
	if d.Repaired() {
		t.Fatalf("the output needed a repair:\n%s", b)
	}
	return d
}

func emptyCatalog(w *Writer) Ref {
	pages := w.Add(pdf.NewDict(
		pdf.Entry{Key: "Type", Val: pdf.Name("Pages").Object()},
		pdf.Entry{Key: "Kids", Val: pdf.Array{}.Object()},
		pdf.Entry{Key: "Count", Val: pdf.Integer(0)},
	).Object())
	return w.Add(pdf.NewDict(
		pdf.Entry{Key: "Type", Val: pdf.Name("Catalog").Object()},
		pdf.Entry{Key: "Pages", Val: pages.Object()},
	).Object())
}

// TestCopy copies a graph with a cycle and a stream: every object once,
// the stream with its bytes and a new /Length, references translated.
func TestCopy(t *testing.T) {
	src := source(t,
		"<</Self 2 0 R /Data 3 0 R /Again 3 0 R /Gone 9 0 R>>",
		"<</Length 99 /Filter/FlateDecode /K 2 0 R>>stream\nabc\nendstream",
	)
	w := New()
	im := w.Import(src)
	cat := emptyCatalog(w)
	w.Set(cat, mustDict(t, w, cat).With("X", im.Map(pdf.Ref{Num: 2}.Object())).Object())
	out := open(t, bytesOf(t, w, Trailer{Root: cat}))

	c, _ := out.Catalog()
	x, ok := c.Get("X").Ref()
	if !ok {
		t.Fatalf("/X = %v", c.Get("X"))
	}
	xd := dictOf(t, out, x)
	if self, _ := xd.Get("Self").Ref(); self != x {
		t.Errorf("/Self = %v, want %v", xd.Get("Self"), x)
	}
	d1, _ := xd.Get("Data").Ref()
	d2, _ := xd.Get("Again").Ref()
	if d1 != d2 || d1.Num == 0 {
		t.Errorf("/Data %v and /Again %v differ", d1, d2)
	}
	if !xd.Get("Gone").IsNull() {
		t.Errorf("/Gone = %v, want null", xd.Get("Gone"))
	}
	so, _ := out.Get(d1)
	s, ok := so.Stream()
	if !ok {
		t.Fatalf("/Data is %v", so.Kind())
	}
	if raw := out.Raw(s); string(raw) != "abc" {
		t.Errorf("stream bytes %q", raw)
	}
	if n, _ := s.Dict.Get("Length").Int(); n != 3 {
		t.Errorf("/Length %d", n)
	}
	if k, _ := s.Dict.Get("K").Ref(); k != x {
		t.Errorf("stream /K = %v, want %v", s.Dict.Get("K"), x)
	}
	if f, _ := s.Dict.Get("Filter").Name(); f != "FlateDecode" {
		t.Errorf("/Filter = %v", s.Dict.Get("Filter"))
	}
}

// TestMapRef checks the mapping hook and dropped keys.
func TestMapRef(t *testing.T) {
	src := source(t,
		"<</Parent 1 0 R /Keep 3 0 R /Skip 4 0 R>>",
		"(kept)",
		"(skipped)",
	)
	w := New()
	im := w.Import(src)
	im.Drop = map[pdf.Name]bool{"Parent": true}
	im.MapRef = func(r pdf.Ref) Ref {
		if r.Num == 4 {
			return 0
		}
		return im.Copy(r)
	}
	cat := emptyCatalog(w)
	w.Set(cat, mustDict(t, w, cat).With("X", im.Map(pdf.Ref{Num: 2}.Object())).Object())
	out := open(t, bytesOf(t, w, Trailer{Root: cat}))

	c, _ := out.Catalog()
	x, _ := c.Get("X").Ref()
	xd := dictOf(t, out, x)
	if xd.Has("Parent") {
		t.Error("/Parent was not dropped")
	}
	if s, _ := out.Resolve(xd.Get("Keep")).Str(); string(s) != "kept" {
		t.Errorf("/Keep = %v", xd.Get("Keep"))
	}
	if !xd.Get("Skip").IsNull() {
		t.Errorf("/Skip = %v, want null", xd.Get("Skip"))
	}
}

// TestOutputObjects checks what the writer does with output values it
// cannot write: references to no output object, nested streams.
func TestOutputObjects(t *testing.T) {
	w := New()
	cat := emptyCatalog(w)
	stm := pdf.NewStream(pdf.NewDict(pdf.Entry{Key: "Length", Val: pdf.Integer(1000)}), []byte("xy"))
	s := w.Add(stm.Object())
	w.Set(cat, mustDict(t, w, cat).
		With("S", s.Object()).
		With("Far", Ref(1000).Object()).
		With("Neg", pdf.Ref{Num: -1}.Object()).
		With("Nested", pdf.Array{stm.Object()}.Object()).Object())
	b := bytesOf(t, w, Trailer{Root: cat})
	out := open(t, b)
	c, _ := out.Catalog()
	for _, k := range []pdf.Name{"Far", "Neg"} {
		if !c.Get(k).IsNull() {
			t.Errorf("/%s = %v, want null", k, c.Get(k))
		}
	}
	if a, _ := c.Get("Nested").Array(); len(a) != 1 || !a[0].IsNull() {
		t.Errorf("/Nested = %v", c.Get("Nested"))
	}
	so, _ := out.Resolve(c.Get("S")).Stream()
	if so == nil || string(out.Raw(so)) != "xy" {
		t.Fatalf("/S = %v", c.Get("S"))
	}
	if n, _ := so.Dict.Get("Length").Int(); n != 2 {
		t.Errorf("/Length %d", n)
	}
}

func TestDropCrypt(t *testing.T) {
	name := func(n pdf.Name) pdf.Object { return n.Object() }
	arr := func(o ...pdf.Object) pdf.Object { return pdf.Array(o).Object() }
	parms := pdf.NewDict(pdf.Entry{Key: "Name", Val: name("Identity")}).Object()
	tests := []struct {
		filter, parms pdf.Object
		f, p          string
		ok            bool
	}{
		{name("Crypt"), parms, "null", "null", true},
		{name("FlateDecode"), pdf.Null, "/FlateDecode", "null", false},
		{arr(name("Crypt")), arr(parms), "null", "null", true},
		{arr(name("Crypt"), name("FlateDecode")), arr(parms, pdf.Null), "[/FlateDecode]", "[null]", true},
		{arr(name("Crypt"), name("FlateDecode")), pdf.Null, "[/FlateDecode]", "null", true},
		{arr(name("FlateDecode"), name("Crypt")), pdf.Null, "[/FlateDecode /Crypt]", "null", false},
	}
	for _, tt := range tests {
		f, p, ok := dropCrypt(tt.filter, tt.parms)
		if f.String() != tt.f || p.String() != tt.p || ok != tt.ok {
			t.Errorf("dropCrypt(%v, %v) = %v, %v, %v", tt.filter, tt.parms, f, p, ok)
		}
	}
}

func TestEscapes(t *testing.T) {
	var out bytes.Buffer
	b := bufio.NewWriter(&out)
	writeName(b, "A B#(c)/é")
	b.WriteByte(' ')
	writeString(b, []byte("a(b)\\c\r\nd"))
	b.Flush()
	const want = `/A#20B#23#28c#29#2F#C3#A9 (a\(b\)\\c\r\nd)`
	if got := out.String(); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	o, _, err := pdf.ParseObject([]byte("[" + want + "]"))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := o.Array()
	n, _ := a[0].Name()
	s, _ := a[1].Str()
	if n != "A B#(c)/é" || string(s) != "a(b)\\c\r\nd" {
		t.Fatalf("read back %q %q", n, s)
	}
}

func bytesOf(t *testing.T, w *Writer, tr Trailer) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := w.Write(&b, tr); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func mustDict(t *testing.T, w *Writer, r Ref) pdf.Dict {
	t.Helper()
	d, ok := w.objs[r-1].o.Dict()
	if !ok {
		t.Fatalf("object %d is no dictionary", r)
	}
	return d
}

func dictOf(t *testing.T, d *pdf.Document, r pdf.Ref) pdf.Dict {
	t.Helper()
	o, err := d.Get(r)
	if err != nil {
		t.Fatal(err)
	}
	dict, ok := o.Dict()
	if !ok {
		t.Fatalf("%v is %v", r, o.Kind())
	}
	return dict
}

func TestFlate(t *testing.T) {
	w := New()
	cat := emptyCatalog(w)
	data := bytes.Repeat([]byte("0 0 m 10 10 l S\n"), 100)
	s := w.Add(Flate(pdf.NewDict(), data).Object())
	w.Set(cat, mustDict(t, w, cat).With("S", s.Object()).Object())
	out := open(t, bytesOf(t, w, Trailer{Root: cat}))
	c, _ := out.Catalog()
	so, _ := out.Resolve(c.Get("S")).Stream()
	if so == nil {
		t.Fatalf("/S = %v", c.Get("S"))
	}
	if f, _ := so.Dict.Get("Filter").Name(); f != "FlateDecode" {
		t.Errorf("/Filter %v", so.Dict.Get("Filter"))
	}
	if raw := out.Raw(so); len(raw) >= len(data) {
		t.Errorf("%d bytes stored for %d", len(raw), len(data))
	}
	if got := out.Decode(so).Data; !bytes.Equal(got, data) {
		t.Errorf("decoded %d bytes, want %d", len(got), len(data))
	}
}
