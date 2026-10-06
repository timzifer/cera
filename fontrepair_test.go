package cera

import (
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/go-opentype/opentype"

	"github.com/timzifer/cera/internal/pdf"
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

func TestRepairSFNTLeavesOthers(t *testing.T) {
	if _, ok := repairSFNT(stdfont.Symbol.Program()); ok {
		t.Error("a complete hmtx table is repaired")
	}
	// Shorter than its pairs: not this defect, still rejected.
	short := shortHmtx(t, stdfont.Symbol.Program(), 100)
	for i := range int(binary.BigEndian.Uint16(short[4:])) {
		if d := 12 + 16*i; string(short[d:d+4]) == "hmtx" {
			binary.BigEndian.PutUint32(short[d+12:], 10)
		}
	}
	if _, ok := repairSFNT(short); ok {
		t.Error("an hmtx table shorter than its pairs is repaired")
	}
	for _, data := range [][]byte{nil, []byte("true"), make([]byte, 12)} {
		if _, ok := repairSFNT(data); ok {
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

// sfntEntry returns where the directory entry of tag is, and the offset
// and length it gives.
func sfntEntry(t *testing.T, data []byte, tag string) (dir, off, n int) {
	t.Helper()
	for i := range int(binary.BigEndian.Uint16(data[4:])) {
		d := 12 + 16*i
		if string(data[d:d+4]) == tag {
			return d, int(binary.BigEndian.Uint32(data[d+8:])), int(binary.BigEndian.Uint32(data[d+12:]))
		}
	}
	t.Fatalf("no %s table", tag)
	return
}

// TestReadProgramRepairsSFNT breaks a TrueType program the ways writers of
// PDF subsets do (pdf.js bug868745, issue2537r, issue3405r, issue8480,
// issue14618, issue11768_reduced, bug1050040): opentype.Parse rejects
// each, readProgram reads it repaired with the glyphs of the original.
func TestReadProgramRepairsSFNT(t *testing.T) {
	orig := stdfont.Symbol.Program()
	want, err := opentype.Parse(orig)
	if err != nil {
		t.Fatal(err)
	}
	put16 := func(b []byte, at, v int) { binary.BigEndian.PutUint16(b[at:], uint16(v)) }
	put32 := func(b []byte, at, v int) { binary.BigEndian.PutUint32(b[at:], uint32(v)) }
	numGlyphs := want.NumGlyphs()
	for _, tc := range []struct {
		name   string
		break_ func(b []byte) []byte
		glyphs int // glyphs after the repair; 0 for as many as before
	}{
		{"an entry of garbage after the directory", func(b []byte) []byte {
			num := int(binary.BigEndian.Uint16(b[4:]))
			end := 12 + 16*num
			for i := range num {
				put32(b, 12+16*i+8, int(binary.BigEndian.Uint32(b[12+16*i+8:]))+16)
			}
			garbage := []byte("\x00\x01\x00\x00\x5f\x0f\x3c\xf5\x68\xf4\xe2\xef\x5f\x0f\x3c\xf5")
			b = append(b[:end:end], append(garbage, b[end:]...)...)
			put16(b, 4, num+1)
			return b
		}, 0},
		{"a table past the end", func(b []byte) []byte {
			last, at := "", 0
			for i := range int(binary.BigEndian.Uint16(b[4:])) {
				if off := int(binary.BigEndian.Uint32(b[12+16*i+8:])); off > at {
					last, at = string(b[12+16*i:12+16*i+4]), off
				}
			}
			d, off, _ := sfntEntry(t, b, last)
			put32(b, d+12, len(b)-off+7)
			return b
		}, 0},
		{"head a byte after where the directory says", func(b []byte) []byte {
			d, off, _ := sfntEntry(t, b, "head")
			put32(b, d+8, off+1)
			return b
		}, 0},
		{"maxp a byte before where the directory says", func(b []byte) []byte {
			d, off, _ := sfntEntry(t, b, "maxp")
			put32(b, d+8, off-1)
			return b
		}, 0},
		{"an indexToLocFormat of 256", func(b []byte) []byte {
			_, off, _ := sfntEntry(t, b, "head")
			put16(b, off+50, 256+int(binary.BigEndian.Uint16(b[off+50:])))
			return b
		}, 0},
		{"a unitsPerEm of zero", func(b []byte) []byte {
			_, off, _ := sfntEntry(t, b, "head")
			put16(b, off+18, 0)
			return b
		}, 0},
		{"more glyphs than loca and hmtx describe", func(b []byte) []byte {
			_, off, _ := sfntEntry(t, b, "maxp")
			put16(b, off+4, numGlyphs+10)
			return b
		}, numGlyphs},
		{"loca cut short", func(b []byte) []byte {
			_, hoff, _ := sfntEntry(t, b, "head")
			size := 2 << binary.BigEndian.Uint16(b[hoff+50:])
			d, _, n := sfntEntry(t, b, "loca")
			put32(b, d+12, n-3*size)
			return b
		}, 0},
		{"more metric pairs than glyphs", func(b []byte) []byte {
			_, off, _ := sfntEntry(t, b, "hhea")
			put16(b, off+34, numGlyphs+10)
			return b
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := tc.break_(append([]byte(nil), orig...))
			if _, err := opentype.Parse(broken); err == nil {
				t.Fatal("opentype.Parse reads it as it is; the test no longer tests a repair")
			}
			got, err := readProgram("FontFile2", broken)
			if err != nil {
				t.Fatalf("readProgram: %v", err)
			}
			wantGlyphs := tc.glyphs
			if wantGlyphs == 0 {
				wantGlyphs = numGlyphs
			}
			if got.NumGlyphs() != wantGlyphs {
				t.Errorf("%d glyphs, want %d", got.NumGlyphs(), wantGlyphs)
			}
			wf, gf := want.NewFace(want.UnitsPerEm()), got.NewFace(got.UnitsPerEm())
			for _, gid := range []opentype.GlyphIndex{20, 50, 90} {
				ws, _ := wf.GlyphOutline(gid)
				gs, _ := gf.GlyphOutline(gid)
				if !reflect.DeepEqual(ws, gs) {
					t.Errorf("glyph %d: outline differs after the repair", gid)
				}
			}
		})
	}
}

// A program that lost bytes inside glyf is not repaired, however well its
// tables can be made to agree: its glyphs are garbage, and a stand-in
// draws the text (pdf.js bug1050040, as PDFium and pdf.js do).
func TestRepairSFNTRefusesBrokenGlyphs(t *testing.T) {
	b := append([]byte(nil), stdfont.Symbol.Program()...)
	_, hoff, _ := sfntEntry(t, b, "head")
	binary.BigEndian.PutUint16(b[hoff+50:], 256+binary.BigEndian.Uint16(b[hoff+50:]))
	d, goff, _ := sfntEntry(t, b, "glyf")
	binary.BigEndian.PutUint32(b[d+8:], uint32(goff+1))
	if _, ok := repairSFNT(b); ok {
		t.Error("a program whose glyphs start a byte off is repaired")
	}
}

// A format 6 subtable longer than its cmap table is cut to the entries
// there are (pdf.js issue7544).
func TestRepairSFNTCmap6(t *testing.T) {
	cmap := []byte{
		0, 0, 0, 1, // version, one subtable
		0, 1, 0, 0, 0, 0, 0, 12, // Macintosh Roman at 12
		0, 6, 0, 16, 0, 0, // format 6, length, language
		0, 65, 1, 0, // firstCode 65, 256 entries
		0, 3, 0, 4, 0, 5, // three of them
	}
	s := &sfnt{data: cmap, tables: []sfntTable{{"cmap", 0, len(cmap)}}}
	s.fixCmap()
	if got := binary.BigEndian.Uint16(cmap[20:]); !s.changed || got != 3 {
		t.Errorf("entryCount %d, want 3", got)
	}
}

// A CFF program whose Private DICT holds an empty real and bytes that start
// no operand (pdf.js bug1068432, bug1308536) is read with them as zero and
// skipped, as pdf.js reads it, also when it is embedded as FontFile
// (issue5751).
func TestReadProgramRepairsCFF(t *testing.T) {
	m := [6]float64{0.001, 0, 0, 0.001, 0, 0}
	private := []byte{
		30, 0xff, 12, 9, // BlueScale: an empty real
		25, 255, 31, // reserved
		139, 21, // nominalWidthX 0
	}
	want, err := opentype.ParseCFF(plainCFF(m))
	if err != nil {
		t.Fatal(err)
	}
	broken := plainCFF(m, private...)
	if _, err := opentype.ParseCFF(broken); err == nil {
		t.Fatal("opentype.ParseCFF reads it as it is; the test no longer tests a repair")
	}
	for _, key := range []pdf.Name{"FontFile3", "FontFile"} {
		got, err := readProgram(key, broken)
		if err != nil {
			t.Fatalf("%s: readProgram: %v", key, err)
		}
		ws, _ := want.NewFace(1000).GlyphOutline(1)
		gs, _ := got.NewFace(1000).GlyphOutline(1)
		if len(gs) == 0 || !reflect.DeepEqual(ws, gs) {
			t.Errorf("%s: the square is %v, want %v", key, gs, ws)
		}
	}
}

// A Type 1 program in PFB segments whose last segment, the trailer, is cut
// off is read without it (pdf.js issue14462_reduced).
func TestReadProgramRepairsPFB(t *testing.T) {
	prog, len1, len2 := type1Program("G32", "G65")
	seg := func(kind byte, n int, data []byte) []byte {
		return append([]byte{0x80, kind, byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}, data...)
	}
	pfb := seg(1, len1, prog[:len1])
	pfb = append(pfb, seg(2, len2, prog[len1:len1+len2])...)
	pfb = append(pfb, seg(1, len(prog)-len1-len2, nil)...) // the trailer, cut off
	if _, err := opentype.ParseType1(pfb); err == nil {
		t.Fatal("opentype.ParseType1 reads it as it is; the test no longer tests a repair")
	}
	got, err := readProgram("FontFile", pfb)
	if err != nil {
		t.Fatalf("readProgram: %v", err)
	}
	if gid, ok := got.GlyphIndexByName("G65"); !ok || gid == 0 {
		t.Error("glyph G65 not found")
	}
	for _, data := range [][]byte{nil, prog, pfb[:3]} {
		if _, ok := repairPFB(data); ok {
			t.Errorf("%.8q is repaired", data)
		}
	}
}

// FuzzRepair feeds the repairs damaged programs: they must not panic, and
// what they return must not crash the parsers either.
func FuzzRepair(f *testing.F) {
	f.Add(stdfont.Symbol.Program())
	f.Add(plainCFF([6]float64{0.001, 0, 0, 0.001, 0, 0}, 30, 0xff, 12, 9, 25, 255, 31, 139, 21))
	prog, len1, _ := type1Program("G32")
	f.Add(append([]byte{0x80, 1, byte(len1), byte(len1 >> 8), 0, 0}, prog[:len1]...))
	f.Fuzz(func(t *testing.T, data []byte) {
		if fixed, ok := repairSFNT(data); ok {
			_, _ = opentype.Parse(fixed)
		}
		if fixed, ok := repairCFF(data); ok {
			_, _ = opentype.ParseCFF(fixed)
		}
		if fixed, ok := repairPFB(data); ok {
			_, _ = opentype.ParseType1(fixed)
		}
	})
}
