package pdf

import (
	"bytes"
	"crypto/rc4"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

func TestParseObject(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"42", "42"},
		{"-17", "-17"},
		{"+5", "5"},
		{"3.5", "3.5"},
		{".5", "0.5"},
		{"5.", "5"},
		{"--5", "-5"},
		{"3.4-5", "3.4"},
		{"true", "true"},
		{"null", "null"},
		{"/Name", "/Name"},
		{"/A#20B", "/A B"},
		{"/", "/"},
		{"(a\\(b\\)c)", `"a(b)c"`},
		{"(line\\\nfeed)", `"linefeed"`},
		{"(\\101\\60)", `"A0"`},
		{"(a\r\nb)", `"a\nb"`},
		{"(nested (paren))", `"nested (paren)"`},
		{"<48656C6C6F>", `"Hello"`},
		{"<4 8 6>", `"H` + "`" + `"`},
		{"12 0 R", "12 0 R"},
		{"12 0", "12"},
		{"[1 2 0 R 3]", "[1 2 0 R 3]"},
		{"<</A 1 /B [2 3] /C <</D (x)>>>>", `<</A 1 /B [2 3] /C <</D "x">>>>`},
		{"<</A 1 /A 2>>", "<</A 2>>"},
		{"[]", "[]"},
		{"<<>>", "<<>>"},
		{"9223372036854775807", "9223372036854775807"},
		{"9223372036854775808", "9.223372036854776e+18"},
	} {
		o, _, err := ParseObject([]byte(tc.in))
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got := o.String(); got != tc.want {
			t.Errorf("%q: got %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{
		"", ")", ">", "(unterminated", "<abc", "[1 2", "<</A 1", "<<1 2>>",
		"/A#2", "/A#zz", "[1 -2 0 R]", "-", ".", "{", "bogus", "<</A>>",
	} {
		if _, _, err := ParseObject([]byte(in)); err == nil {
			t.Errorf("%q parsed", in)
		}
	}
}

func TestNumberKinds(t *testing.T) {
	for in, kind := range map[string]Kind{
		"5": KindInteger, "-0": KindInteger, "5.0": KindReal, "--5": KindReal,
		"99999999999999999999": KindReal,
	} {
		o, _, _ := ParseObject([]byte(in))
		if o.Kind() != kind {
			t.Errorf("%s is a %s, want %s", in, o.Kind(), kind)
		}
	}
	// Reals are exactly what strconv makes of them.
	r := rand.New(rand.NewPCG(1, 2))
	for range 10000 {
		f := (r.Float64() - 0.5) * pow(10, r.IntN(30)-10)
		for _, s := range []string{fmt.Sprintf("%.6f", f), fmt.Sprint(f), fmt.Sprintf("%.17f", f)} {
			if strings.ContainsAny(s, "e") {
				continue
			}
			o, _, err := ParseObject([]byte(s))
			if err != nil {
				t.Fatal(s, err)
			}
			got, _ := o.Float()
			var want float64
			if _, err := fmt.Sscan(s, &want); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("%s: got %v, want %v", s, got, want)
			}
		}
	}
}

func pow(b float64, e int) float64 {
	v := 1.0
	for ; e > 0; e-- {
		v *= b
	}
	for ; e < 0; e++ {
		v /= b
	}
	return v
}

func TestLargeDictionaryLookup(t *testing.T) {
	var b strings.Builder
	b.WriteString("<<")
	for i := range 100 {
		fmt.Fprintf(&b, "/K%d %d ", 99-i, 99-i)
	}
	b.WriteString("/K5 500>>")
	o, _, err := ParseObject([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	d, _ := o.Dict()
	if d.Len() != 100 {
		t.Errorf("%d entries", d.Len())
	}
	for i := range 100 {
		want := int64(i)
		if i == 5 {
			want = 500 // the later value of a repeated key wins
		}
		if v, _ := d.Get(Name(fmt.Sprintf("K%d", i))).Int(); v != want {
			t.Errorf("K%d = %d", i, v)
		}
	}
	if !d.Get("missing").IsNull() {
		t.Error("absent key not null")
	}
	if !d.GetBytes([]byte("K7")).Kind().isNumber() {
		t.Error("GetBytes")
	}
}

func (k Kind) isNumber() bool { return k == KindInteger || k == KindReal }

func TestObjectAccessors(t *testing.T) {
	if !Null.IsNull() || Null.Kind() != KindNull {
		t.Error("zero Object is not null")
	}
	if v, ok := Real(3).Int(); !ok || v != 3 {
		t.Error("integral real is no int")
	}
	if _, ok := Real(3.5).Int(); ok {
		t.Error("3.5 is an int")
	}
	if r, ok := (Ref{7, 2}).Object().Ref(); !ok || r != (Ref{7, 2}) {
		t.Error("ref round trip")
	}
	if n, ok := Name("Hi").Object().Name(); !ok || n != "Hi" {
		t.Error("name round trip")
	}
	a := Array{Integer(1), Integer(2)}
	if b, ok := a.Object().Array(); !ok || len(b) != 2 || cap(b) != 2 {
		t.Error("array round trip, or capacity not clipped")
	}
	if d := (Dict{}); !d.Object().IsNull() || !d.IsZero() {
		t.Error("zero Dict")
	}
	if d := NewDict(); d.IsZero() || d.Object().Kind() != KindDict {
		t.Error("empty Dict is no dictionary")
	}
}

func TestFilters(t *testing.T) {
	plain := []byte(strings.Repeat("The quick brown fox jumps over the lazy dog. ", 50))
	none := func(o Object) Object { return o }
	for _, tc := range []struct {
		name string
		in   []byte
		want []byte
	}{
		{"FlateDecode", deflate(plain), plain},
		{"AHx", []byte("48 65 6C6C6F7>"), []byte("Hello\x70")},
		{"A85", []byte("<~87cURD]i,\"Ebo80~>"), []byte("Hello World!")},
		{"RL", []byte{2, 'a', 'b', 'c', 254, 'x', 128}, []byte("abcxxx")},
	} {
		out, err := applyFilter(Name(tc.name), tc.in, Dict{}, none)
		if err != nil || !bytes.Equal(out, tc.want) {
			t.Errorf("%s: %q, %v", tc.name, out, err)
		}
	}
	// A wrong checksum after good zlib data is ignored.
	z := deflate(plain)
	z[len(z)-1] ^= 0xFF
	if out, err := flateDecode(z, 0); err != nil || !bytes.Equal(out, plain) {
		t.Errorf("bad Adler-32: %v", err)
	}
	// Truncated Flate keeps its prefix and says so.
	z = deflate(plain)
	if out, err := flateDecode(z[:len(z)/2], 0); err == nil || len(out) == 0 {
		t.Errorf("truncated Flate: %d bytes, %v", len(out), err)
	}
}

func TestStreamCap(t *testing.T) {
	saved := MaxStreamBytes
	MaxStreamBytes = 1000
	defer func() { MaxStreamBytes = saved }()
	big := deflate(make([]byte, 5000))
	if _, err := flateDecode(big, 0); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Flate: %v", err)
	}
	rl := bytes.Repeat([]byte{129, 0}, 100)
	if _, err := runLengthDecode(rl); !errors.Is(err, ErrTooLarge) {
		t.Errorf("RunLength: %v", err)
	}
}

func TestLZW(t *testing.T) {
	// "-----A---B" with EarlyChange, from the specification's example.
	in := []byte{0x80, 0x0B, 0x60, 0x50, 0x22, 0x0C, 0x0C, 0x85, 0x01}
	out, err := lzwDecode(in, true)
	if err != nil || string(out) != "-----A---B" {
		t.Errorf("%q, %v", out, err)
	}
}

func TestPredictors(t *testing.T) {
	none := func(o Object) Object { return o }
	parm := func(pred, colors, bpc, cols int) Dict {
		return NewDict(Entry{"Predictor", Integer(int64(pred))}, Entry{"Colors", Integer(int64(colors))},
			Entry{"BitsPerComponent", Integer(int64(bpc))}, Entry{"Columns", Integer(int64(cols))})
	}
	// PNG Up and Sub rows.
	in := []byte{1, 1, 1, 1, 2, 1, 1, 1}
	out, err := applyPredictor(in, parm(12, 1, 8, 3), none)
	if err != nil || !bytes.Equal(out, []byte{1, 2, 3, 2, 3, 4}) {
		t.Errorf("PNG: %v, %v", out, err)
	}
	// TIFF at 4 bits: samples 1,1,1,1 differenced to 1,0,0,0.
	out, err = applyPredictor([]byte{0x10, 0x00}, parm(2, 1, 4, 4), none)
	if err != nil || !bytes.Equal(out, []byte{0x11, 0x11}) {
		t.Errorf("TIFF 4-bit: %x, %v", out, err)
	}
	// Rows too long to allocate are refused.
	if _, err := applyPredictor(in, parm(12, 1<<15, 16, 1<<30), none); err == nil {
		t.Error("huge predictor row accepted")
	}
}

func TestRC4MatchesStandardLibrary(t *testing.T) {
	key := []byte("a key of some length")
	data := bytes.Repeat([]byte("data "), 99)
	want := make([]byte, len(data))
	c, _ := rc4.NewCipher(key)
	c.XORKeyStream(want, data)
	got := bytes.Clone(data)
	rc4InPlace(key, got)
	if !bytes.Equal(got, want) {
		t.Error("RC4 differs from crypto/rc4")
	}
}
