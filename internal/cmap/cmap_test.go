package cmap

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func codes(c *CMap, s string) (out [][2]int) {
	b := []byte(s)
	for len(b) > 0 {
		code, n := c.Next(b)
		out = append(out, [2]int{int(code), c.CID(code, n)})
		b = b[n:]
	}
	return out
}

func TestPredefinedRKSJ(t *testing.T) {
	h := Predefined("90ms-RKSJ-H")
	if h == nil {
		t.Fatal("no 90ms-RKSJ-H")
	}
	if h.Ordering != "Japan1" || h.Registry != "Adobe" || h.WMode != 0 {
		t.Errorf("collection %s-%s, wmode %d", h.Registry, h.Ordering, h.WMode)
	}
	// One byte for ASCII, two for a lead byte in 81-9F.
	got := codes(h, "A\x81\x40\x82\xa0")
	want := [][2]int{{0x41, 231 + 0x41 - 0x20}, {0x8140, 633}, {0x82a0, 843}}
	if len(got) != len(want) {
		t.Fatalf("codes %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("code %d: %v, want %v", i, got[i], want[i])
		}
	}
	v := Predefined("90ms-RKSJ-V")
	if v == nil || v.WMode != 1 || v.Parent() != h {
		t.Fatalf("90ms-RKSJ-V: %+v", v)
	}
	// Overridden by V, and inherited from H.
	if cid := v.CID(0x8141, 2); cid != 7887 {
		t.Errorf("V <8141> = %d, want 7887", cid)
	}
	if cid := v.CID(0x8140, 2); cid != 633 {
		t.Errorf("V <8140> = %d, want 633", cid)
	}
	// Notdef range: control codes.
	if cid := h.CID(0x05, 1); cid != 231 {
		t.Errorf("notdef <05> = %d, want 231", cid)
	}
}

func TestPredefinedUnicodeRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		cmap, text string
	}{
		{"UniJIS-UCS2-H", "あいうアイウ日本語漢字"},
		{"UniGB-UCS2-H", "中文简体汉字"},
		{"UniCNS-UCS2-H", "中文繁體漢字"},
		{"UniKS-UCS2-H", "한국어가나다"},
		{"UniJIS-UTF16-V", "縦書き"},
	} {
		c := Predefined(tc.cmap)
		if c == nil {
			t.Errorf("no %s", tc.cmap)
			continue
		}
		for _, r := range tc.text {
			u := utf16.Encode([]rune{r})
			code, n := c.Next([]byte{byte(u[0] >> 8), byte(u[0])})
			cid := c.CID(code, n)
			if cid == 0 {
				t.Errorf("%s: %q unmapped", tc.cmap, r)
				continue
			}
			back, ok := ToUnicode(c.Ordering, cid)
			if !ok || back != r {
				t.Errorf("%s: %q -> CID %d -> %q, %v", tc.cmap, r, cid, back, ok)
			}
		}
	}
	// A vertical form reads as its horizontal character.
	v := Predefined("UniJIS-UCS2-V")
	cid := v.CID(0x3001, 2) // 、
	if cid == Predefined("UniJIS-UCS2-H").CID(0x3001, 2) {
		t.Fatalf("V does not select a vertical form for U+3001")
	}
	if r, ok := ToUnicode("Japan1", cid); !ok || r != 0x3001 {
		t.Errorf("vertical CID %d reads %q", cid, r)
	}
}

func TestAllPredefined(t *testing.T) {
	indexOnce.Do(readIndex)
	if len(index) < 60 {
		t.Fatalf("%d entries in data.bin", len(index))
	}
	for name := range index {
		if strings.HasPrefix(name, "ucs:") {
			continue
		}
		c := Predefined(name)
		if c == nil || len(c.codespace()) == 0 || c.Ordering == "" {
			t.Errorf("%s: %+v", name, c)
		}
	}
	if Predefined("No-Such-CMap") != nil {
		t.Error("unknown CMap found")
	}
}

func TestIdentity(t *testing.T) {
	c := Predefined("Identity-V")
	if c.WMode != 1 || !c.IsIdentity() {
		t.Fatal("Identity-V")
	}
	code, n := c.Next([]byte{0x12, 0x34, 0x56})
	if code != 0x1234 || n != 2 || c.CID(code, n) != 0x1234 {
		t.Errorf("code %x n %d", code, n)
	}
	if _, n := c.Next([]byte{1}); n != 1 {
		t.Error("odd byte")
	}
}

func TestParseEmbedded(t *testing.T) {
	prog := `%!PS
/CIDInit /ProcSet findresource begin
12 dict begin begincmap
/CIDSystemInfo << /Registry (Adobe) /Ordering (Japan1) /Supplement 6 >> def
/CMapName /Test-V def
/WMode 1 def
2 begincodespacerange <00> <7f> <8000> <ffff> endcodespacerange
1 beginnotdefrange <00> <1f> 1 endnotdefrange
3 begincidrange
<20> <7e> 100
<8000> <80ff> 1000
<8010> <8011> 5000
endcidrange
1 begincidchar <8020> 7 endcidchar
endcmap CMapName currentdict /CMap defineresource pop end end`
	c, err := Parse([]byte(prog), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Test-V" || c.WMode != 1 || c.Ordering != "Japan1" || c.Supplement != 6 {
		t.Errorf("header: %+v", c)
	}
	for _, tc := range []struct {
		code uint32
		n    int
		cid  int
	}{
		{0x20, 1, 100}, {0x41, 1, 133}, {0x05, 1, 1},
		{0x8000, 2, 1000}, {0x800f, 2, 1015},
		{0x8010, 2, 5000}, {0x8011, 2, 5001}, // later definition wins
		{0x8012, 2, 1018}, {0x8020, 2, 7}, {0x80ff, 2, 1255},
		{0x9000, 2, 0},
	} {
		if cid := c.CID(tc.code, tc.n); cid != tc.cid {
			t.Errorf("<%x>/%d = %d, want %d", tc.code, tc.n, cid, tc.cid)
		}
	}
	// A byte outside every codespace with a lead byte of a two-byte one.
	if code, n := c.Next([]byte{0x90}); n != 1 || code != 0x90 {
		t.Errorf("short code %x/%d", code, n)
	}
	// usecmap of a predefined CMap.
	sub, err := Parse([]byte("/90ms-RKSJ-H usecmap 1 begincidchar <41> 9 endcidchar"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if sub.CID(0x41, 1) != 9 || sub.CID(0x8140, 2) != 633 || sub.Ordering != "Japan1" {
		t.Error("usecmap")
	}
	if _, err := Parse([]byte("garbage"), nil); err == nil {
		t.Error("garbage parsed")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	c := Predefined("90ms-RKSJ-H")
	d, parent := unmarshal(Marshal(c))
	if d == nil || parent != "" {
		t.Fatal("unmarshal")
	}
	c.Each(func(code uint32, n int, cid int) {
		if got := d.CID(code, n); got != cid {
			t.Fatalf("<%x> = %d, want %d", code, got, cid)
		}
	})
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("1 begincodespacerange <00> <ff> endcodespacerange 1 begincidrange <00> <ff> 1 endcidrange"))
	f.Add([]byte("/UniJIS-UCS2-H usecmap /WMode 1 def 2 begincidchar <3001> 2 <3002> 3 endcidchar"))
	f.Add([]byte("<<</Registry (A(B)C) >> 3 begincidrange <0000> <00ff> 0 <0080> <0180> 9 <01> <00> 1 endcidrange"))
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := Parse(data, nil)
		if err != nil {
			return
		}
		for s := data; len(s) > 0; {
			code, n := c.Next(s)
			if n <= 0 || n > len(s) {
				t.Fatalf("Next consumed %d of %d", n, len(s))
			}
			c.CID(code, n)
			s = s[n:]
		}
		if d, _ := unmarshal(Marshal(c)); d == nil {
			t.Fatal("round trip failed")
		}
	})
}
