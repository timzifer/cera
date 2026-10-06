package cera

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/go-opentype/fonts/arimo"
	"github.com/go-opentype/opentype"
)

func TestMacGlyphNames(t *testing.T) {
	m := macGlyphIndex()
	if len(macGlyphNames) != 258 || len(m) != 258 {
		t.Fatalf("%d names, %d distinct", len(macGlyphNames), len(m))
	}
	for name, want := range map[string]int{"space": 3, "A": 36, "greater": 33, "a": 68, "quoteright": 183, "apple": 210, "dcroat": 257} {
		if m[name] != want {
			t.Errorf("%s at %d, want %d", name, m[name], want)
		}
	}
}

// withPost returns arimo with its 'cmap' table renamed away and its 'post'
// table replaced by post, appended at the end.
func withPost(t *testing.T, post []byte) []byte {
	t.Helper()
	data := bytes.Clone(arimo.TTF)
	dir, _, _ := sfntEntry(t, data, "cmap")
	copy(data[dir:], "xmap")
	for len(data)%4 != 0 {
		data = append(data, 0)
	}
	dir, _, _ = sfntEntry(t, data, "post")
	binary.BigEndian.PutUint32(data[dir+8:], uint32(len(data)))
	binary.BigEndian.PutUint32(data[dir+12:], uint32(len(post)))
	return append(data, post...)
}

// arimoGlyphs returns arimo's glyph count and the glyphs of A and B.
func arimoGlyphs(t *testing.T) (n int, a, b opentype.GlyphIndex) {
	t.Helper()
	p, err := opentype.Parse(arimo.TTF)
	if err != nil {
		t.Fatal(err)
	}
	a, _ = p.GlyphIndex('A')
	b, _ = p.GlyphIndex('B')
	return p.NumGlyphs(), a, b
}

// postV2 is a format 2 'post' table naming glyph a "Ayb" (its own string)
// and glyph b "quoteright" (Macintosh index 183); every other glyph is
// .notdef.
func postV2(n int, a, b opentype.GlyphIndex) []byte {
	post := make([]byte, 34+2*n)
	binary.BigEndian.PutUint32(post, 0x00020000)
	binary.BigEndian.PutUint16(post[32:], uint16(n))
	binary.BigEndian.PutUint16(post[34+2*int(a):], 258)
	binary.BigEndian.PutUint16(post[34+2*int(b):], 183)
	return append(post, 3, 'A', 'y', 'b')
}

func TestPostGlyphNames(t *testing.T) {
	n, a, b := arimoGlyphs(t)
	p, err := opentype.Parse(withPost(t, postV2(n, a, b)))
	if err != nil {
		t.Fatal(err)
	}
	if p.HasCharacterMap() {
		t.Fatal("the cmap is still there")
	}
	names := postGlyphNames(p)
	if names["Ayb"] != a || names["quoteright"] != b {
		t.Errorf("format 2: Ayb %d, quoteright %d; want %d, %d", names["Ayb"], names["quoteright"], a, b)
	}

	// Format 2.5: each glyph's name is the Macintosh name at its index
	// plus an offset.
	post := make([]byte, 34+n)
	binary.BigEndian.PutUint32(post, 0x00025000)
	binary.BigEndian.PutUint16(post[32:], uint16(n))
	for gid := range n {
		post[34+gid] = byte(-gid) // .notdef up to 128, beyond index 256 (ccaron)
	}
	post[34+int(a)] = 10
	if p, err = opentype.Parse(withPost(t, post)); err != nil {
		t.Fatal(err)
	}
	if name := macGlyphNames[int(a)+10]; postGlyphNames(p)[name] != a {
		t.Errorf("format 2.5: %s is %d, want %d", name, postGlyphNames(p)[name], a)
	}

	// A truncated table names nothing.
	if p, err = opentype.Parse(withPost(t, postV2(n, a, b)[:40])); err != nil {
		t.Fatal(err)
	}
	if names := postGlyphNames(p); len(names) != 0 {
		t.Errorf("truncated: %d names", len(names))
	}
}

// A TrueType program without a cmap is addressed through the glyph names
// of its 'post' table, also names outside the Macintosh order (pdf.js
// TrueType_without_cmap: WinAnsi quoteright of an Armenian subset).
func TestTextTrueTypePostNames(t *testing.T) {
	n, a, b := arimoGlyphs(t)
	render := func(prog []byte, enc string) []byte {
		font := "<< /Type /Font /Subtype /TrueType /BaseFont /ABCDEF+Arimo /FirstChar 65 /LastChar 66 /Widths [667 667] /Encoding " + enc + " /FontDescriptor 101 0 R >>"
		desc := "<< /Type /FontDescriptor /FontName /ABCDEF+Arimo /Flags 32 /FontFile2 102 0 R >>"
		stream := fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(prog), prog)
		img, _, err := renderPage(t, textPDF("BT /F1 60 Tf 20 20 Td (AB) Tj ET", font, desc, stream), 0, RenderOptions{Background: white})
		if err != nil {
			t.Fatal(err)
		}
		return img.Pix
	}
	want := render(arimo.TTF, "/WinAnsiEncoding")
	got := render(withPost(t, postV2(n, a, b)), "<< /Differences [65 /Ayb /quoteright] >>")
	if !bytes.Equal(got, want) {
		t.Error("AB drawn through the post names differs from AB drawn through the cmap")
	}
}
