package cera

import "testing"

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
