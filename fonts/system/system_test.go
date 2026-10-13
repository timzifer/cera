package system

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"unicode/utf16"

	"github.com/timzifer/cera"
)

// sfntTables are the tables of a font that is nothing but its names: what
// a provider reads of a font.
func sfntTables(family, style, ps string, weight int, italic bool) map[string][]byte {
	u16 := func(s string) []byte {
		var b []byte
		for _, c := range utf16.Encode([]rune(s)) {
			b = binary.BigEndian.AppendUint16(b, c)
		}
		return b
	}
	names := map[uint16]string{1: family, 2: style, 4: family + " " + style, 6: ps}
	ids := []uint16{1, 2, 4, 6}
	name := binary.BigEndian.AppendUint16(nil, 0)
	name = binary.BigEndian.AppendUint16(name, uint16(len(ids)))
	name = binary.BigEndian.AppendUint16(name, uint16(6+12*len(ids)))
	var strs []byte
	for _, id := range ids {
		s := u16(names[id])
		for _, v := range []uint16{3, 1, 0x409, id, uint16(len(s)), uint16(len(strs))} {
			name = binary.BigEndian.AppendUint16(name, v)
		}
		strs = append(strs, s...)
	}
	os2 := make([]byte, 96)
	binary.BigEndian.PutUint16(os2[4:], uint16(weight))
	if italic {
		binary.BigEndian.PutUint16(os2[62:], 1)
	}
	return map[string][]byte{"name": append(name, strs...), "OS/2": os2, "head": make([]byte, 54)}
}

// sfnt writes fonts as one file: a font, or with several a collection.
func sfnt(fonts ...map[string][]byte) []byte {
	var out []byte
	base := 0
	if len(fonts) > 1 {
		out = append([]byte("ttcf\x00\x01\x00\x00"), 0, 0, 0, byte(len(fonts)))
		out = append(out, make([]byte, 4*len(fonts))...)
	}
	type rec struct {
		at  int // of the record's offset field
		tab []byte
	}
	var recs []rec
	for i, tables := range fonts {
		if len(fonts) > 1 {
			binary.BigEndian.PutUint32(out[12+4*i:], uint32(len(out)))
		}
		base = len(out)
		tags := make([]string, 0, len(tables))
		for t := range tables {
			tags = append(tags, t)
		}
		sort.Strings(tags)
		out = append(out, 0, 1, 0, 0, 0, byte(len(tags)), 0, 0, 0, 0, 0, 0)
		for _, t := range tags {
			out = append(out, t...)
			out = append(out, make([]byte, 12)...)
			binary.BigEndian.PutUint32(out[len(out)-4:], uint32(len(tables[t])))
			recs = append(recs, rec{len(out) - 8, tables[t]})
		}
	}
	_ = base
	for _, r := range recs {
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
		binary.BigEndian.PutUint32(out[r.at:], uint32(len(out)))
		out = append(out, r.tab...)
	}
	return out
}

func write(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProvider(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "TestSans-Regular.ttf", sfnt(sfntTables("Test Sans", "Regular", "TestSans-Regular", 400, false)))
	write(t, dir, "TestSans-Bold.ttf", sfnt(sfntTables("Test Sans", "Bold", "TestSans-Bold", 700, false)))
	write(t, dir, "TestSans-Italic.ttf", sfnt(sfntTables("Test Sans", "Italic", "TestSans-Italic", 400, true)))
	write(t, dir, "TestSans-BoldItalic.otf", sfnt(sfntTables("Test Sans", "Bold Italic", "TestSans-BoldItalic", 700, true)))
	write(t, dir, "more/TestSans-Light.ttf", sfnt(sfntTables("Test Sans", "Light", "TestSans-Light", 300, false)))
	write(t, dir, "Other.ttc", sfnt(
		sfntTables("Other Serif", "Regular", "OtherSerif", 400, false),
		sfntTables("Other Serif", "Bold", "OtherSerif-Bold", 700, false)))
	write(t, dir, "Times.ttf", sfnt(sfntTables("Times New Roman", "Bold", "TimesNewRomanPS-BoldMT", 700, false)))
	write(t, dir, "junk.ttf", []byte("this is no font at all"))
	write(t, dir, "readme.txt", []byte("nor is this"))

	p := NewDirs(dir)
	if faces := p.Faces(); len(faces) != 8 {
		t.Fatalf("%d faces: %+v", len(faces), faces)
	}
	for _, c := range []struct {
		req  cera.FontRequest
		want string // the PostScript name of the face, "" for none
	}{
		// As documents spell names.
		{cera.FontRequest{Name: "TestSans-Bold"}, "TestSans-Bold"},
		{cera.FontRequest{Name: "Test Sans Bold"}, "TestSans-Bold"},
		{cera.FontRequest{Name: "TestSans,BoldItalic"}, "TestSans-BoldItalic"},
		{cera.FontRequest{Name: "TestSans,Bold"}, "TestSans-Bold"},
		{cera.FontRequest{Name: "TestSansMT"}, "TestSans-Regular"},
		{cera.FontRequest{Name: "TestSans-BoldMT"}, "TestSans-Bold"},
		{cera.FontRequest{Name: "TestSans-Light"}, "TestSans-Light"},
		{cera.FontRequest{Name: "TimesNewRoman,Bold"}, "TimesNewRomanPS-BoldMT"},
		{cera.FontRequest{Name: "TimesNewRomanPSMT"}, "TimesNewRomanPS-BoldMT"}, // the family has no other face
		// The family, with the style the descriptor gives.
		{cera.FontRequest{Name: "TestSans"}, "TestSans-Regular"},
		{cera.FontRequest{Name: "TestSans", Bold: true}, "TestSans-Bold"},
		{cera.FontRequest{Name: "TestSans", Italic: true}, "TestSans-Italic"},
		{cera.FontRequest{Name: "TestSans", Bold: true, Italic: true}, "TestSans-BoldItalic"},
		{cera.FontRequest{Name: "TestSans", Weight: 600}, "TestSans-Bold"},
		{cera.FontRequest{Name: "TestSans", Weight: 320}, "TestSans-Light"},
		{cera.FontRequest{Name: "TestSans-Black"}, "TestSans-Bold"}, // the nearest it has
		// A face of a collection.
		{cera.FontRequest{Name: "OtherSerif-Bold"}, "OtherSerif-Bold"},
		{cera.FontRequest{Name: "Other-Serif", Ordering: "Japan1"}, "OtherSerif"},
		// Never a font of another family.
		{cera.FontRequest{Name: "Helvetica"}, ""},
		{cera.FontRequest{Name: "TestSerif", Bold: true}, ""},
		{cera.FontRequest{Name: "Test"}, ""},
		{cera.FontRequest{}, ""},
	} {
		f, ok := p.Match(c.req)
		if ok != (c.want != "") || f.PostScript != c.want {
			t.Errorf("%+v: %q, %v, want %q", c.req, f.PostScript, ok, c.want)
		}
	}

	// A font file is supplied as it is.
	prog, ok := p.Font(cera.FontRequest{Name: "TestSans-Italic"})
	file, _ := os.ReadFile(filepath.Join(dir, "TestSans-Italic.ttf"))
	if !ok || !bytes.Equal(prog, file) {
		t.Errorf("the font supplied is %d bytes, the file %d", len(prog), len(file))
	}
	// A face of a collection as a font of its own.
	prog, ok = p.Font(cera.FontRequest{Name: "OtherSerif-Bold"})
	if !ok || bytes.HasPrefix(prog, []byte("ttcf")) {
		t.Fatalf("the face of the collection is supplied as %q", prog[:min(len(prog), 4)])
	}
	write(t, dir, "again/face.ttf", prog)
	if faces := readFaces(filepath.Join(dir, "again", "face.ttf")); len(faces) != 1 || faces[0].PostScript != "OtherSerif-Bold" || faces[0].Weight != 700 || faces[0].Family != "Other Serif" {
		t.Errorf("read again, the face is %+v", faces)
	}
	if _, ok := p.Font(cera.FontRequest{Name: "Nothing"}); ok {
		t.Error("a font nobody has is supplied")
	}

	// No directories, and directories that are not there.
	if _, ok := (&Provider{}).Font(cera.FontRequest{Name: "TestSans"}); ok {
		t.Error("the zero provider has a font")
	}
	if faces := NewDirs(filepath.Join(dir, "none")).Faces(); len(faces) != 0 {
		t.Errorf("a directory that is not there has %d faces", len(faces))
	}
}

// TestMachine reads the fonts of the machine the test runs on: whatever
// they are, they have names, and one that is there is found by its name.
func TestMachine(t *testing.T) {
	p := New()
	faces := p.Faces()
	t.Logf("%d faces in %v", len(faces), p.dirs)
	families := map[string]bool{}
	for _, f := range faces {
		if f.Family == "" && f.PostScript == "" || f.Weight < 1 || f.Weight > 1000 {
			t.Errorf("%+v", f)
		}
		families[f.Family] = true
	}
	if len(faces) == 0 {
		return
	}
	f := faces[len(faces)/2]
	if f.PostScript == "" {
		return
	}
	got, ok := p.Match(cera.FontRequest{Name: f.PostScript})
	if !ok || fold(got.PostScript) != fold(f.PostScript) {
		t.Errorf("%q is found as %+v, %v", f.PostScript, got, ok)
	}
	prog, ok := p.Font(cera.FontRequest{Name: f.PostScript})
	if !ok || len(prog) < 12 || slices.Equal(prog[:4], []byte("ttcf")) {
		t.Errorf("%q is supplied as %d bytes", f.PostScript, len(prog))
	}
}

func TestSplit(t *testing.T) {
	for name, want := range map[string]string{
		"Arial": "arial 0 false", "Arial,Bold": "arial 700 false", "ArialMT": "arial 0 false", "Arial-BoldMT": "arial 700 false",
		"Arial-BoldItalicMT": "arial 700 true", "TimesNewRomanPS-ItalicMT": "timesnewroman 0 true", "Courier New,BoldItalic": "couriernew 700 true",
		"Segoe UI Semibold": "segoeui 600 false", "Helvetica-Oblique": "helvetica 0 true", "Bold": "bold 0 false",
	} {
		family, weight, italic := split(name)
		if got := family + " " + itoa(weight) + " " + map[bool]string{false: "false", true: "true"}[italic]; got != want {
			t.Errorf("%q: %s, want %s", name, got, want)
		}
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for ; v > 0; v /= 10 {
		b = append([]byte{byte('0' + v%10)}, b...)
	}
	return string(b)
}
