// Package system supplies the fonts installed on the machine for fonts a
// PDF names but does not embed, to cera through its FontProvider:
//
//	doc, err := cera.OpenWith(data, cera.OpenOptions{
//		Fonts: cera.FontProviders{system.New(), cjk.Provider{japan1.Collection}},
//	})
//
// A document that asks for "Segoe UI" or "MS Mincho" on a machine that has
// them gets them. A font is found by the name the document spells —
// "Arial,Bold", "ArialMT", "Arial-BoldMT" — or by its family with the
// weight and slope asked for, and never by a font of another family:
// where the machine does not have the family, cera's stand-ins and the
// providers after this one do better.
//
// The font directories of the platform are read on first use: the name
// tables of the TrueType and OpenType files in them, the faces of
// collections one by one. That takes some tenths of a second on a machine
// with a thousand fonts, once for a Provider. On js/wasm there are none.
//
// This module is separate from cera's because what it does depends on the
// machine: a page drawn with it is not the same everywhere.
package system

import (
	"cmp"
	"encoding/binary"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/timzifer/cera"
)

// Face is a font of the machine: a file, or one face of a collection.
type Face struct {
	// File is the font file, and Index the face in it when it is a
	// collection (.ttc), else 0.
	File  string
	Index int
	// PostScript is the PostScript name ("Arial-BoldMT"), Family and Style
	// the family and the style in it ("Arial", "Bold"), Full the full name
	// ("Arial Bold").
	PostScript, Family, Style, Full string
	// Weight is the weight class, 400 for regular and 700 for bold, and
	// Italic says that the face is slanted.
	Weight int
	Italic bool

	offset int64 // of the face's table directory in the file
}

// Provider is a cera.FontProvider of the fonts in some directories. The
// zero Provider has none; New returns that of the machine. It is safe for
// concurrent use.
type Provider struct {
	dirs []string

	once     sync.Once
	faces    []Face
	byName   map[string]int   // PostScript and full names, folded
	byFamily map[string][]int // families, folded
}

// New returns the provider of the fonts installed on the machine: in the
// font directories of the platform, the user's among them.
func New() *Provider { return &Provider{dirs: platformDirs()} }

// NewDirs returns a provider of the fonts in dirs and the directories
// below them.
func NewDirs(dirs ...string) *Provider { return &Provider{dirs: slices.Clone(dirs)} }

// Limits of what is read: font files, the depth of directories below a
// font directory, and the faces of one collection.
const (
	maxFiles = 1 << 14
	maxDepth = 6
	maxFaces = 256
)

// Faces returns the fonts the provider found, in the order of their
// files. It reads the directories when they were not read yet.
func (p *Provider) Faces() []Face {
	p.load()
	return slices.Clone(p.faces)
}

func (p *Provider) load() {
	p.once.Do(func() {
		p.byName, p.byFamily = map[string]int{}, map[string][]int{}
		files := 0
		for _, dir := range p.dirs {
			root := filepath.Clean(dir)
			_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil // what cannot be read is not there
				}
				if d.IsDir() {
					if rel, _ := filepath.Rel(root, path); strings.Count(rel, string(filepath.Separator)) >= maxDepth {
						return filepath.SkipDir
					}
					return nil
				}
				switch strings.ToLower(filepath.Ext(path)) {
				case ".ttf", ".otf", ".ttc", ".otc":
				default:
					return nil
				}
				if files++; files > maxFiles {
					return filepath.SkipAll
				}
				p.faces = append(p.faces, readFaces(path)...)
				return nil
			})
		}
		for i, f := range p.faces {
			// The first of two fonts of one name stays: the system's
			// directories come before the user's.
			for _, name := range []string{f.PostScript, f.Full, f.Family + " " + f.Style} {
				if k := fold(name); k != "" {
					if _, ok := p.byName[k]; !ok {
						p.byName[k] = i
					}
				}
			}
			if k := fold(f.Family); k != "" {
				p.byFamily[k] = append(p.byFamily[k], i)
			}
		}
	})
}

// fold returns a name as names are compared: without case, spaces,
// hyphens, commas and underscores.
func fold(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', ',', '_':
			return -1
		}
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

// styleWords are what the names of fonts say of their style, the longer
// first; each with the weight it means (0: none) and whether it slants.
var styleWords = []struct {
	word   string
	weight int
	italic bool
}{
	{"bolditalic", 700, true}, {"boldoblique", 700, true}, {"semibold", 600, false}, {"demibold", 600, false},
	{"extrabold", 800, false}, {"ultrabold", 800, false}, {"extralight", 200, false}, {"ultralight", 200, false},
	{"regular", 400, false}, {"italic", 0, true}, {"oblique", 0, true}, {"medium", 500, false}, {"roman", 400, false},
	{"normal", 400, false}, {"light", 300, false}, {"black", 900, false}, {"heavy", 900, false}, {"thin", 100, false},
	{"bold", 700, false}, {"demi", 600, false}, {"book", 400, false},
}

// split takes the name of a font as a document spells it apart: the
// family, folded, and what the name says of the style.
func split(name string) (family string, weight int, italic bool) {
	s := fold(name)
	// What vendors put after their names: Arial*MT*, TimesNewRoman*PS*MT.
	s = strings.TrimSuffix(s, "mt")
	for changed := true; changed; {
		changed = false
		for _, w := range styleWords {
			if strings.HasSuffix(s, w.word) && len(s) > len(w.word) {
				s = s[:len(s)-len(w.word)]
				weight, italic, changed = max(weight, w.weight), italic || w.italic, true
			}
		}
	}
	return strings.TrimSuffix(strings.TrimSuffix(s, "mt"), "ps"), weight, italic
}

// Font implements cera.FontProvider: the font of the machine req names,
// or the face of its family that is nearest in weight and has its slope.
func (p *Provider) Font(req cera.FontRequest) ([]byte, bool) {
	f, ok := p.Match(req)
	if !ok {
		return nil, false
	}
	prog, err := f.Program()
	return prog, err == nil
}

// Match returns the face Font supplies for req.
func (p *Provider) Match(req cera.FontRequest) (Face, bool) {
	p.load()
	if i, ok := p.byName[fold(req.Name)]; ok {
		return p.faces[i], true
	}
	family, weight, italic := split(req.Name)
	italic = italic || req.Italic
	switch {
	case weight != 0:
	case req.Weight != 0:
		weight = req.Weight
	case req.Bold:
		weight = 700
	default:
		weight = 400
	}
	faces := p.byFamily[family]
	if len(faces) == 0 {
		// "TimesNewRomanPS" is the family "Times New Roman".
		faces = p.byFamily[strings.TrimSuffix(family, "ps")]
	}
	if len(faces) == 0 {
		return Face{}, false
	}
	best := slices.MinFunc(faces, func(a, b int) int {
		fa, fb := p.faces[a], p.faces[b]
		// The slope first, then the weight; of two as near, the lighter
		// for a light request and the bolder for a bold one.
		return cmp.Or(
			cmp.Compare(btoi(fa.Italic != italic), btoi(fb.Italic != italic)),
			cmp.Compare(abs(fa.Weight-weight), abs(fb.Weight-weight)),
			cmp.Compare(abs(fa.Weight-400), abs(fb.Weight-400)),
		)
	})
	return p.faces[best], true
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func abs(v int) int { return max(v, -v) }

// Program returns the font program of the face: its file, or for a face
// of a collection a font file made of its tables.
func (f Face) Program() ([]byte, error) {
	data, err := os.ReadFile(f.File)
	if err != nil {
		return nil, err
	}
	if f.offset == 0 {
		return data, nil
	}
	return extract(data, f.offset)
}

// An sfnt file: a directory of tables, each a tag, a checksum, an offset
// and a length; a collection is several directories over shared tables.

func tag(b []byte) string { return string(b[:4]) }

// directory reads the table directory at off: where each table is.
func directory(r io.ReaderAt, off int64) (map[string][2]uint32, bool) {
	var head [12]byte
	if _, err := r.ReadAt(head[:], off); err != nil {
		return nil, false
	}
	switch tag(head[:]) {
	case "\x00\x01\x00\x00", "OTTO", "true":
	default:
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(head[4:]))
	recs := make([]byte, 16*n)
	if _, err := r.ReadAt(recs, off+12); err != nil {
		return nil, false
	}
	tables := make(map[string][2]uint32, n)
	for i := range n {
		rec := recs[16*i:]
		tables[tag(rec)] = [2]uint32{binary.BigEndian.Uint32(rec[8:]), binary.BigEndian.Uint32(rec[12:])}
	}
	return tables, true
}

// maxTable is the most of a name, OS/2 or head table that is read.
const maxTable = 1 << 20

func table(r io.ReaderAt, tables map[string][2]uint32, name string) []byte {
	t, ok := tables[name]
	if !ok || t[1] > maxTable {
		return nil
	}
	b := make([]byte, t[1])
	if _, err := r.ReadAt(b, int64(t[0])); err != nil {
		return nil
	}
	return b
}

// readFaces reads the faces of a font file: one, or those of a
// collection. A file that is no font has none.
func readFaces(path string) []Face {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var head [12]byte
	if _, err := f.ReadAt(head[:], 0); err != nil {
		return nil
	}
	offsets := []int64{0}
	if tag(head[:]) == "ttcf" {
		n := min(int(binary.BigEndian.Uint32(head[8:])), maxFaces)
		b := make([]byte, 4*n)
		if _, err := f.ReadAt(b, 12); err != nil {
			return nil
		}
		offsets = offsets[:0]
		for i := range n {
			offsets = append(offsets, int64(binary.BigEndian.Uint32(b[4*i:])))
		}
	}
	var faces []Face
	for i, off := range offsets {
		tables, ok := directory(f, off)
		if !ok {
			continue
		}
		face := Face{File: path, Index: i, Weight: 400}
		if len(offsets) > 1 || off != 0 {
			face.offset = off
		}
		names := nameTable(table(f, tables, "name"))
		face.PostScript, face.Full = names[6], names[4]
		// The family and style a designer means (16, 17) before those cut
		// down to four styles a family (1, 2).
		face.Family, face.Style = cmp.Or(names[16], names[1]), cmp.Or(names[17], names[2])
		if os2 := table(f, tables, "OS/2"); len(os2) >= 64 {
			if w := int(binary.BigEndian.Uint16(os2[4:])); w >= 1 && w <= 1000 {
				face.Weight = w
			}
			face.Italic = binary.BigEndian.Uint16(os2[62:])&0x201 != 0 // italic or oblique
		} else if hd := table(f, tables, "head"); len(hd) >= 46 {
			style := binary.BigEndian.Uint16(hd[44:])
			face.Italic = style&2 != 0
			if style&1 != 0 {
				face.Weight = 700
			}
		}
		if face.Family == "" && face.PostScript == "" {
			continue
		}
		faces = append(faces, face)
	}
	return faces
}

// nameTable returns the names of a name table by their number: of the
// Windows platform in US English where there are, else the first of
// Windows, else of the Macintosh in Roman.
func nameTable(b []byte) map[int]string {
	names := map[int]string{}
	rank := map[int]int{}
	if len(b) < 6 {
		return names
	}
	n, strs := int(binary.BigEndian.Uint16(b[2:])), int(binary.BigEndian.Uint16(b[4:]))
	for i := 0; i < n && 6+12*i+12 <= len(b); i++ {
		rec := b[6+12*i:]
		platform, encoding, lang, id := binary.BigEndian.Uint16(rec), binary.BigEndian.Uint16(rec[2:]), binary.BigEndian.Uint16(rec[4:]), int(binary.BigEndian.Uint16(rec[6:]))
		length, off := int(binary.BigEndian.Uint16(rec[8:])), strs+int(binary.BigEndian.Uint16(rec[10:]))
		if off < 0 || off+length > len(b) || length > 1024 {
			continue
		}
		raw := b[off : off+length]
		var s string
		r := 0
		switch {
		case platform == 3 && (encoding == 1 || encoding == 10) || platform == 0:
			u := make([]uint16, len(raw)/2)
			for k := range u {
				u[k] = binary.BigEndian.Uint16(raw[2*k:])
			}
			s, r = string(utf16.Decode(u)), 2
			if platform == 3 && lang == 0x409 {
				r = 3
			}
		case platform == 1 && encoding == 0:
			// Mac Roman is ASCII where names are ASCII.
			s, r = strings.Map(func(c rune) rune {
				if c < 0x20 || c > 0x7e {
					return -1
				}
				return c
			}, string(raw)), 1
		default:
			continue
		}
		if s = strings.TrimSpace(s); s != "" && r > rank[id] {
			names[id], rank[id] = s, r
		}
	}
	return names
}

// extract makes a font file of the face of a collection whose table
// directory is at off: the directory with the tables it names, one after
// another.
func extract(data []byte, off int64) ([]byte, error) {
	if off < 0 || off+12 > int64(len(data)) {
		return nil, io.ErrUnexpectedEOF
	}
	n := int(binary.BigEndian.Uint16(data[off+4:]))
	if off+12+16*int64(n) > int64(len(data)) {
		return nil, io.ErrUnexpectedEOF
	}
	out := slices.Clone(data[off : off+12+16*int64(n)])
	for i := range n {
		rec := 12 + 16*i
		at, length := int64(binary.BigEndian.Uint32(out[rec+8:])), int64(binary.BigEndian.Uint32(out[rec+12:]))
		if at < 0 || at+length > int64(len(data)) {
			return nil, io.ErrUnexpectedEOF
		}
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
		binary.BigEndian.PutUint32(out[rec+8:], uint32(len(out)))
		out = append(out, data[at:at+length]...)
	}
	return out, nil
}
