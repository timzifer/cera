package content

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/go-pdfkit/reader"
)

// render prints the operations of a stream in a canonical form.
func render(data string) string {
	var s Scanner
	s.Reset([]byte(data))
	var b strings.Builder
	for {
		op, ok := s.Next()
		if !ok {
			break
		}
		for i := range s.Len() {
			s.write(&b, s.Arg(i))
			b.WriteByte(' ')
		}
		b.Write(op)
		if d, img := s.Image(); d != nil {
			b.WriteByte(' ')
			s.write(&b, d)
			fmt.Fprintf(&b, " %q", img)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func (s *Scanner) write(b *strings.Builder, o *Operand) {
	switch o.Kind {
	case Number:
		fmt.Fprint(b, o.Num)
	case Name:
		fmt.Fprintf(b, "/%s", s.Text(o))
	case String, HexString:
		fmt.Fprintf(b, "%q", s.Text(o))
	case Bool:
		fmt.Fprint(b, o.Num != 0)
	case Null:
		b.WriteString("null")
	case Array, Dict:
		open, close := "[", "]"
		if o.Kind == Dict {
			open, close = "<<", ">>"
		}
		b.WriteString(open)
		first := true
		s.Elems(o, func(e *Operand) bool {
			if !first {
				b.WriteByte(' ')
			}
			first = false
			s.write(b, e)
			return true
		})
		b.WriteString(close)
	}
}

func TestScan(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"1 0 0 rg 10 10 20 20 re f", "1 0 0 rg\n10 10 20 20 re\nf\n"},
		{"q 1 0 0 1 .5 -.5 cm Q", "q\n1 0 0 1 0.5 -0.5 cm\nQ\n"},
		{"3.4-5 m", "3.4 -5 m\n"},
		{"--5 +-2 5. l", "-5 2 5 l\n"},
		{"0.0001 123456789012345678901234 x", "0.0001 1.2345678901234567e+23 x\n"},
		{"[3 1] 0 d", "[3 1] 0 d\n"},
		{"[(a) -120 <4142 3>] TJ", "[\"a\" -120 \"AB0\"] TJ\n"},
		{"(a\\(b\\)\\101\\n(c)) Tj", "\"a(b)A\\n(c)\" Tj\n"},
		{"/Span <</MCID 3 /Alt (x) /A [true null]>> BDC EMC", "/Span <</MCID 3 /Alt \"x\" /A [true null]>> BDC\nEMC\n"},
		{"/A#20B gs % comment\nn", "/A B gs\nn\n"},
		{"1 2 ) ] } 3 m", "1 2 3 m\n"},
		{"[1 2 f", "[1 2] f\n"},
		{"(unterminated", ""},
		{"<414", ""},
		{"- . 1 w", "1 w\n"},
		{"true false null x", "true false null x\n"},
	} {
		if got := render(c.in); got != c.want {
			t.Errorf("%q:\ngot  %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestInlineImage(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"BI /W 2 /H 2 /BPC 8 /CS /G ID abEIcd EI Q", "BI <</W 2 /H 2 /BPC 8 /CS /G>> \"abEIcd\"\nQ\n"},
		{"BI /W 4 /H 1 /F /AHx ID 01020304> EI Q", "BI <</W 4 /H 1 /F /AHx>> \"01020304>\"\nQ\n"},
		{"BI /W 1 /H 1 /L 3 /F /Fl ID \x00\xffE EI Q", "BI <</W 1 /H 1 /L 3 /F /Fl>> \"\\x00\\xffE\"\nQ\n"},
		{"BI /W 1 /H 1 /IM true ID \x80 EI", "BI <</W 1 /H 1 /IM true>> \"\\x80\"\n"},
		{"BI /W 1 Q", "BI <</W 1>> \"\"\nQ\n"},
		{"BI /W 1 ID xyz", "BI <</W 1>> \"xyz\"\n"},
	} {
		if got := render(c.in); got != c.want {
			t.Errorf("%q:\ngot  %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestNums(t *testing.T) {
	var s Scanner
	s.Reset([]byte("/X 1 2 3 cm 1 (a) m"))
	s.Next()
	var v [2]float64
	if !s.Nums(v[:]) || v != [2]float64{2, 3} {
		t.Errorf("Nums = %v", v)
	}
	var w [4]float64
	if s.Nums(w[:]) {
		t.Error("Nums accepted a name")
	}
	s.Next()
	if s.Nums(v[:]) {
		t.Error("Nums accepted a string")
	}
}

func TestDictGet(t *testing.T) {
	var s Scanner
	s.Reset([]byte("/OC <</Type /OCMD /OCGs [1 2] /P /AnyOn>> BDC"))
	s.Next()
	d := s.Last()
	if v := s.DictGet(d, "P"); !s.NameIs(v, "AnyOn") {
		t.Errorf("P = %v", v)
	}
	if v := s.DictGet(d, "OCGs"); v == nil || v.Kind != Array {
		t.Errorf("OCGs = %v", v)
	}
	if v := s.DictGet(d, "Missing"); v != nil {
		t.Errorf("Missing = %v", v)
	}
	if !s.NameIs(s.Arg(0), "OC") {
		t.Error("first operand")
	}
}

func TestManyOperandsKeepTheLast(t *testing.T) {
	in := strings.Repeat("1 ", maxOperands+100) + "2 3 m"
	var s Scanner
	s.Reset([]byte(in))
	s.Next()
	var v [2]float64
	if s.Len() != maxOperands || !s.Nums(v[:]) || v != [2]float64{2, 3} {
		t.Errorf("len %d, %v", s.Len(), v)
	}
}

func TestDeepNesting(t *testing.T) {
	in := strings.Repeat("[", 100) + strings.Repeat("]", 100) + " 5 w"
	var s Scanner
	s.Reset([]byte(in))
	op, ok := s.Next()
	if !ok || string(op) != "w" || s.Errors() == 0 {
		t.Errorf("op %q ok %v errors %d", op, ok, s.Errors())
	}
	var v [1]float64
	if !s.Nums(v[:]) || v[0] != 5 {
		t.Errorf("width %v", v)
	}
}

func TestNumberPrecision(t *testing.T) {
	for _, in := range []string{"0.1", "123.456", "-0.000001", "99999.99999", "1234567890.123", ".5", "7"} {
		var s Scanner
		s.Reset([]byte(in + " x"))
		s.Next()
		want := 0.0
		if _, err := fmt.Sscan(in, &want); err != nil {
			t.Fatal(err)
		}
		if got := s.Arg(0).Num; got != want {
			t.Errorf("%s: %v, want %v", in, got, want)
		}
	}
}

// TestAgreesWithReader compares operators and numeric operands with
// go-pdfkit/reader on well-formed content.
func TestAgreesWithReader(t *testing.T) {
	data := "q 0.5 0 0 -0.5 10.25 800 cm 0 0 1 RG 1.5 w [2 1] 0 d 10 10 m 20.5 30 l 1 2 3 4 5 6 c h S Q " +
		"BT /F1 12 Tf 1 0 0 1 72 700 Tm [(Hello) -250 (World)] TJ ET /GS0 gs /Im1 Do " +
		"/P <</MCID 0>> BDC 0 0 100 100 re W* n EMC 0.1 0.2 0.3 0.4 k"
	var s Scanner
	s.Reset([]byte(data))
	ops, err := reader.Operations([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		op, ok := s.Next()
		if !ok {
			if i != len(ops) {
				t.Fatalf("%d operations, reader %d", i, len(ops))
			}
			return
		}
		if i >= len(ops) || string(op) != ops[i].Operator || s.Len() != len(ops[i].Operands) {
			t.Fatalf("operation %d: %q with %d operands", i, op, s.Len())
		}
		for j, o := range ops[i].Operands {
			if f, ok := reader.ToFloat(o); ok && s.Arg(j).Num != f {
				t.Errorf("operation %d operand %d: %v, reader %v", i, j, s.Arg(j).Num, f)
			}
		}
	}
}

func TestNoAllocs(t *testing.T) {
	data := []byte(strings.Repeat("q 0.5 0 0 -0.5 10.25 800 cm [2 1] 0 d /F1 12 Tf (a\\)b) Tj 10 10 m 20.5 30 l S Q\n", 50))
	var s Scanner
	s.Reset(data)
	for {
		if _, ok := s.Next(); !ok {
			break
		}
	}
	allocs := testing.AllocsPerRun(10, func() {
		s.Reset(data)
		for {
			if _, ok := s.Next(); !ok {
				break
			}
		}
	})
	if allocs != 0 {
		t.Errorf("%v allocations per scan", allocs)
	}
}

func FuzzScan(f *testing.F) {
	for _, s := range []string{
		"1 0 0 rg 10 10 20 20 re f",
		"[(a) 1 <41>] TJ /X <</A [1 2 <<>>]>> BDC",
		"BI /W 2 /H 2 /BPC 8 /CS /RGB ID abcdefghijkl EI",
		"((((\\",
		"<<<<[[[[",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		var s Scanner
		s.Reset([]byte(in))
		for n := 0; ; n++ {
			if n > len(in)+1 {
				t.Fatal("scanner does not advance")
			}
			_, ok := s.Next()
			if !ok {
				break
			}
			for i := range s.Len() {
				o := s.Arg(i)
				s.Text(o)
				s.Elems(o, func(e *Operand) bool { s.Text(e); return true })
				if o.Kind == Number && math.IsNaN(o.Num) {
					t.Fatal("NaN")
				}
			}
			s.Image()
		}
	})
}

func BenchmarkScan(b *testing.B) {
	var c strings.Builder
	for i := range 20000 {
		fmt.Fprintf(&c, "%.2f %.2f m %.2f %.2f l S\n", float64(i%1000)*1.19, float64(i%800)*1.05, float64(i%900)*1.3, float64(i%700)*0.9)
	}
	data := []byte(c.String())
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	var s Scanner
	for b.Loop() {
		s.Reset(data)
		for {
			if _, ok := s.Next(); !ok {
				break
			}
		}
	}
}

func BenchmarkReaderScan(b *testing.B) {
	var c strings.Builder
	for i := range 20000 {
		fmt.Fprintf(&c, "%.2f %.2f m %.2f %.2f l S\n", float64(i%1000)*1.19, float64(i%800)*1.05, float64(i%900)*1.3, float64(i%700)*0.9)
	}
	data := []byte(c.String())
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		s := reader.NewContentScanner(data)
		for {
			if _, ok := s.Next(); !ok {
				break
			}
		}
	}
}
