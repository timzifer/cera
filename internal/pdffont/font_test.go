package pdffont

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
)

// doc builds a document whose object 1 is a font dictionary, with any
// further objects given.
func doc(t *testing.T, objs ...string) (*pdf.Document, pdf.Dict) {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offs := []int{0}
	for i, o := range objs {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs))
	for _, o := range offs[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	// A catalogue is not needed to read objects, but Open wants one.
	fmt.Fprintf(&b, "trailer\n<</Size %d /Root << /Type /Catalog /Pages << /Type /Pages /Kids [] /Count 0 >> >>>>\nstartxref\n%d\n%%%%EOF\n", len(offs), xref)
	d, err := pdf.Open(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	o, _ := d.Get(pdf.Ref{Num: 1})
	dict, _ := o.Dict()
	return d, dict
}

func TestMacRoman(t *testing.T) {
	d, dict := doc(t, "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /MacRomanEncoding>>")
	f := Read(d, dict)
	for code, want := range map[int]string{
		0x27: "quotesingle", 0x60: "grave", 0x8A: "adieresis", 0x9F: "udieresis",
		0xA7: "germandbls", 0x80: "Adieresis", 0xCA: "space", 0xDB: "currency",
	} {
		if got, _ := f.GlyphName(code); got != want {
			t.Errorf("code %#x: %s, want %s", code, got, want)
		}
	}
	for code, want := range map[int]string{0x27: "'", 0x8A: "ä", 0x9A: "ö", 0x9F: "ü", 0xA7: "ß", 0x85: "Ö"} {
		if got, _ := f.Text(code); got != want {
			t.Errorf("text of %#x: %q, want %q", code, got, want)
		}
	}
}

func TestMacExpert(t *testing.T) {
	d, dict := doc(t, "<</Type /Font /Subtype /Type1 /BaseFont /X /Encoding <</BaseEncoding /MacExpertEncoding /Differences [65 /A]>>>>")
	f := Read(d, dict)
	if n, _ := f.GlyphName(0x30); n != "zerooldstyle" {
		t.Errorf("0x30: %s", n)
	}
	if n, _ := f.GlyphName(0x61); n != "Asmall" {
		t.Errorf("0x61: %s", n)
	}
	if n, _ := f.GlyphName(65); n != "A" || !f.Chosen(65) || f.Chosen(0x61) {
		t.Error("differences over a base encoding")
	}
}

func TestSimpleWidths(t *testing.T) {
	d, dict := doc(t, "<</Type /Font /Subtype /TrueType /FirstChar 65 /Widths [500 2 0 R] /FontDescriptor 3 0 R>>",
		"600", "<</Type /FontDescriptor /Flags 32 /MissingWidth 250>>")
	f := Read(d, dict)
	if f.Width(65) != 0.5 || f.Width(66) != 0.6 || !f.HasWidth(66) {
		t.Errorf("widths %v %v", f.Width(65), f.Width(66))
	}
	if f.Width(67) != 0.25 || f.HasWidth(67) {
		t.Errorf("missing width %v", f.Width(67))
	}
	if f.Symbolic() {
		t.Error("nonsymbolic font is symbolic")
	}
}

func TestCompositeWidths(t *testing.T) {
	d, dict := doc(t, "<</Type /Font /Subtype /Type0 /DescendantFonts [2 0 R]>>",
		"<</Type /Font /Subtype /CIDFontType2 /DW 1000 /W [1 [100 200] 10 20 300 15 [999]]>>")
	f := Read(d, dict)
	for cid, want := range map[int]float64{0: 1, 1: 0.1, 2: 0.2, 3: 1, 10: 0.3, 14: 0.3, 15: 0.999, 16: 0.3, 20: 0.3, 21: 1} {
		if got := f.Width(cid); got != want {
			t.Errorf("cid %d: %v, want %v", cid, got, want)
		}
	}
	if g, ok := f.CIDToGID(77); !ok || g != 77 {
		t.Error("identity CIDToGID")
	}
}

func TestType3(t *testing.T) {
	d, dict := doc(t, "<</Type /Font /Subtype /Type3 /FontMatrix [0.01 0 0 0.01 0 0] /FirstChar 97 /Widths [50] /CharProcs <</a 2 0 R>> /Encoding <</Differences [97 /a]>>>>",
		"<</Length 0>>\nstream\n\nendstream")
	f := Read(d, dict)
	if f.Kind() != Type3 || f.Width(97) != 0.5 || f.Width(98) != 0 {
		t.Errorf("kind %v width %v", f.Kind(), f.Width(97))
	}
	if f.CharProcs().Get("a").IsNull() {
		t.Error("no CharProcs")
	}
}

func TestToUnicodeIsLazyAndWins(t *testing.T) {
	cmap := "/CIDInit /ProcSet findresource begin begincmap 1 beginbfchar <41> <0058> endbfchar 1 beginbfrange <61> <63> <0078> endbfrange endcmap"
	d, dict := doc(t, "<</Type /Font /Subtype /Type1 /ToUnicode 2 0 R>>",
		fmt.Sprintf("<</Length %d>>\nstream\n%s\nendstream", len(cmap), cmap))
	f := Read(d, dict)
	if f.toUni != nil {
		t.Error("ToUnicode read before text was asked for")
	}
	for code, want := range map[int]string{0x41: "X", 0x61: "x", 0x63: "z", 0x42: "B"} {
		if got, _ := f.Text(code); got != want {
			t.Errorf("text of %#x: %q, want %q", code, got, want)
		}
	}
}

func TestSymbolicNamesAreGuesses(t *testing.T) {
	d, dict := doc(t, "<</Type /Font /Subtype /TrueType /FontDescriptor 2 0 R /Encoding <</Differences [66 /beta]>>>>",
		"<</Type /FontDescriptor /Flags 4>>")
	f := Read(d, dict)
	if !f.Symbolic() {
		t.Fatal("not symbolic")
	}
	if _, ok := f.Text(65); ok {
		t.Error("a symbolic font's base encoding was believed")
	}
	if s, _ := f.Text(66); s != "β" {
		t.Errorf("a name the document chose: %q", s)
	}
	f.SetFallback(func(code int) (string, bool) { return "fb", code == 65 })
	if s, _ := f.Text(65); s != "fb" {
		t.Error("fallback not used")
	}
}

func TestGlyphNames(t *testing.T) {
	for name, want := range map[string]string{
		"a": "a", "a.sc": "a", "f_i": "fi", "uni0041": "A", "u1F600": "😀", "uni00410042": "AB",
		"uni00000048": "", "g17": "", "Scaron": "Š", "alpha": "α",
	} {
		got, ok := TextOfGlyphName(name)
		if got != want || ok != (want != "") {
			t.Errorf("%s: %q %v", name, got, ok)
		}
	}
}
