package cera

import (
	"bytes"
	"fmt"
	"testing"
)

// type1Encrypt is the Type 1 encryption: eexec (r 55665) for the private
// part, 4330 for charstrings, after four leading bytes.
func type1Encrypt(plain []byte, r uint16) []byte {
	out := make([]byte, 0, 4+len(plain))
	for _, p := range append([]byte{0, 0, 0, 0}, plain...) {
		c := p ^ byte(r>>8)
		r = (uint16(c)+r)*52845 + 22719
		out = append(out, c)
	}
	return out
}

// type1Program builds a Type 1 program whose glyphs are named as scanning
// software names them (G<code>), in the order given, with a built-in
// encoding that maps each code to its glyph.
func type1Program(glyphs ...string) (prog []byte, len1, len2 int) {
	var clear bytes.Buffer
	clear.WriteString("%!PS-AdobeFont-1.0: Test 001\n11 dict begin\n/FontName /Test def\n/FontType 1 def\n" +
		"/FontMatrix [0.001 0 0 0.001 0 0] readonly def\n/FontBBox {0 0 500 700} readonly def\n/PaintType 0 def\n" +
		"/Encoding 256 array\n0 1 255 {1 index exch /.notdef put} for\n")
	for _, g := range glyphs {
		var code int
		if _, err := fmt.Sscanf(g, "G%d", &code); err == nil {
			fmt.Fprintf(&clear, "dup %d /%s put\n", code, g)
		}
	}
	clear.WriteString("readonly def\ncurrentdict end\ncurrentfile eexec\n")

	var priv bytes.Buffer
	priv.WriteString("dup /Private 8 dict dup begin\n/RD {string currentfile exch readstring pop} executeonly def\n" +
		"/ND {noaccess def} executeonly def\n/NP {noaccess put} executeonly def\n/lenIV 4 def\n" +
		"/BlueValues [] def\n/MinFeature {16 16} def\n/password 5839 def\n/Subrs 0 array\n")
	fmt.Fprintf(&priv, "2 index /CharStrings %d dict dup begin\n", len(glyphs)+1)
	// 0 500 hsbw endchar
	cs := type1Encrypt([]byte{139, 248, 136, 13, 14}, 4330)
	for _, g := range append([]string{".notdef"}, glyphs...) {
		fmt.Fprintf(&priv, "/%s %d RD %s ND\n", g, len(cs), cs)
	}
	priv.WriteString("end\nend\nreadonly put\nnoaccess put\ndup /FontName get exch definefont pop\nmark currentfile closefile\n")

	enc := type1Encrypt(priv.Bytes(), 55665)
	tail := bytes.Repeat([]byte("0"), 512)
	tail = append(tail, "\ncleartomark\n"...)
	prog = append(append(append([]byte{}, clear.Bytes()...), enc...), tail...)
	return prog, clear.Len(), len(enc)
}

// A symbolic Type 1 font whose /Differences leave code 32 to the base
// encoding: its glyph comes from the program's built-in encoding, not from
// the Macintosh glyph order of TrueType programs, which puts "space" at
// glyph 3 (borb 0449.pdf).
func TestType1BuiltinEncodingWithoutCmap(t *testing.T) {
	prog, len1, len2 := type1Program("G100", "G110", "G101", "G32", "G9")
	font := "<< /Type /Font /Subtype /Type1 /BaseFont /ABCDEF+Arial0116.625 /FirstChar 9 /LastChar 32 /FontDescriptor 101 0 R " +
		"/Encoding << /Type /Encoding /Differences [9 /G9] >> >>"
	desc := "<< /Type /FontDescriptor /FontName /ABCDEF+Arial0116.625 /Flags 4 /FontFile 102 0 R >>"
	stream := fmt.Sprintf("<< /Length %d /Length1 %d /Length2 %d /Length3 0 >>\nstream\n%s\nendstream", len(prog), len1, len2, prog)
	d, err := Open(textPDF("BT /F1 10 Tf ( \t) Tj ET", font, desc, stream))
	if err != nil {
		t.Fatal(err)
	}
	f := d.font(d.dict(d.dict(mustPage(t, d).dict["Resources"])["Font"])["F1"])
	if f.program == nil || f.substituted || f.bad {
		t.Fatalf("program not read: %+v", f)
	}
	if f.program.HasCharacterMap() {
		t.Fatal("a Type 1 program should have no cmap")
	}
	for code, want := range map[int]string{32: "G32", 9: "G9"} {
		f.mu.Lock()
		gid := f.glyphIndex(code)
		f.mu.Unlock()
		if got, _ := f.program.GlyphName(gid); got != want {
			t.Errorf("code %d: glyph %d (%q), want %s", code, gid, got, want)
		}
	}
}

func mustPage(t *testing.T, d *Document) *Page {
	t.Helper()
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
