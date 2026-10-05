package cera

import (
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/go-opentype/opentype"

	"github.com/timzifer/cera/internal/stdfont"
)

// shortHmtx returns a copy of a TrueType program whose hmtx table holds
// only the pairs of its first metrics glyphs, as Windows' printer drivers
// embed it (borb's 0354.pdf).
func shortHmtx(t *testing.T, data []byte, metrics int) []byte {
	t.Helper()
	out := append([]byte(nil), data...)
	for i := range int(binary.BigEndian.Uint16(out[4:])) {
		d := 12 + 16*i
		switch string(out[d : d+4]) {
		case "hhea":
			binary.BigEndian.PutUint16(out[int(binary.BigEndian.Uint32(out[d+8:]))+34:], uint16(metrics))
		case "hmtx":
			binary.BigEndian.PutUint32(out[d+12:], uint32(4*metrics))
		}
	}
	return out
}

func TestReadProgramShortHmtx(t *testing.T) {
	orig := stdfont.Symbol.Program()
	want, err := opentype.Parse(orig)
	if err != nil {
		t.Fatal(err)
	}
	short := shortHmtx(t, orig, 100)
	if _, err := opentype.Parse(short); err == nil {
		t.Fatal("the short hmtx table parses without repair; the test no longer tests it")
	}
	got, err := readProgram("FontFile2", short)
	if err != nil {
		t.Fatalf("readProgram: %v", err)
	}
	wf, gf := want.NewFace(want.UnitsPerEm()), got.NewFace(got.UnitsPerEm())
	for _, gid := range []opentype.GlyphIndex{50, 99, 150} {
		ws, _ := wf.GlyphOutline(gid)
		gs, _ := gf.GlyphOutline(gid)
		if !reflect.DeepEqual(ws, gs) {
			t.Errorf("glyph %d: outline differs after the repair", gid)
		}
	}
	// The pairs keep their advances; the glyphs after them take the last.
	if w, g := wf.AdvanceIndex(50), gf.AdvanceIndex(50); w != g {
		t.Errorf("glyph 50 advances %v, want %v", g, w)
	}
	if w, g := wf.AdvanceIndex(99), gf.AdvanceIndex(150); w != g {
		t.Errorf("glyph 150 advances %v, want glyph 99's %v", g, w)
	}
}

func TestPadHmtxLeavesOthers(t *testing.T) {
	if _, ok := padHmtx(stdfont.Symbol.Program()); ok {
		t.Error("a complete hmtx table is repaired")
	}
	// Shorter than its pairs: not this defect, still rejected.
	short := shortHmtx(t, stdfont.Symbol.Program(), 100)
	for i := range int(binary.BigEndian.Uint16(short[4:])) {
		if d := 12 + 16*i; string(short[d:d+4]) == "hmtx" {
			binary.BigEndian.PutUint32(short[d+12:], 10)
		}
	}
	if _, ok := padHmtx(short); ok {
		t.Error("an hmtx table shorter than its pairs is repaired")
	}
	for _, data := range [][]byte{nil, []byte("true"), make([]byte, 12)} {
		if _, ok := padHmtx(data); ok {
			t.Errorf("%q is repaired", data)
		}
	}
}

// collection wraps a TrueType program as the only font of a collection.
func collection(data []byte) []byte {
	out := append([]byte("ttcf\x00\x01\x00\x00\x00\x00\x00\x01\x00\x00\x00\x10"), data...)
	for i := range int(binary.BigEndian.Uint16(out[16+4:])) {
		e := 16 + 12 + 16*i + 8
		binary.BigEndian.PutUint32(out[e:], binary.BigEndian.Uint32(out[e:])+16)
	}
	return out
}

func TestReadProgramCollection(t *testing.T) {
	orig := stdfont.Symbol.Program()
	want, err := opentype.Parse(orig)
	if err != nil {
		t.Fatal(err)
	}
	ttc := collection(orig)
	if _, err := opentype.Parse(ttc); err == nil {
		t.Fatal("a collection parses as it is; the test no longer tests it")
	}
	got, err := readProgram("FontFile2", ttc)
	if err != nil {
		t.Fatalf("readProgram: %v", err)
	}
	if got.NumGlyphs() != want.NumGlyphs() {
		t.Fatalf("%d glyphs, want %d", got.NumGlyphs(), want.NumGlyphs())
	}
	wf, gf := want.NewFace(want.UnitsPerEm()), got.NewFace(got.UnitsPerEm())
	ws, _ := wf.GlyphOutline(50)
	gs, _ := gf.GlyphOutline(50)
	if !reflect.DeepEqual(ws, gs) {
		t.Error("glyph 50: outline differs")
	}
	for _, data := range [][]byte{nil, []byte("ttcf"), ttc[:20], orig} {
		if _, ok := firstOfCollection(data); ok {
			t.Errorf("%.8q is taken for a collection", data)
		}
	}
}
