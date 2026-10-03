// Package stdfont holds the stand-ins for the standard fonts Symbol and
// ZapfDingbats, which have alphabets of their own: subsets of DejaVu Sans
// (Bitstream Vera licence, see LICENSE-DejaVu) with their character sets,
// and Adobe's metrics and built-in encodings for them. Run
//
//	go run ./internal/stdfont/gen -src dir
//
// to rebuild them (see gen for what dir holds).
package stdfont

import "sort"

// Glyph is one glyph of a standard font's character set.
type Glyph struct {
	// Name is the glyph name, as encodings and AFM files spell it.
	Name string
	// Rune is the character the glyph stands for, 0 if none.
	Rune rune
	// Width is the AFM advance width in thousandths of an em.
	Width int
	// GID is the glyph index in the stand-in program, 0 if it has none.
	GID uint16
}

// Font is a standard font with its own alphabet.
type Font struct {
	Name   string
	ttf    *[]byte
	glyphs []Glyph    // by name
	codes  [256]uint8 // built-in encoding: index into glyphs + 1
}

// Program returns the stand-in font program (TrueType).
func (f *Font) Program() []byte { return *f.ttf }

// Code returns the glyph the font's built-in encoding gives a code.
func (f *Font) Code(code byte) (*Glyph, bool) {
	i := f.codes[code]
	if i == 0 {
		return nil, false
	}
	return &f.glyphs[i-1], true
}

// Glyph returns a glyph by name.
func (f *Font) Glyph(name string) (*Glyph, bool) {
	i := sort.Search(len(f.glyphs), func(i int) bool { return f.glyphs[i].Name >= name })
	if i < len(f.glyphs) && f.glyphs[i].Name == name {
		return &f.glyphs[i], true
	}
	return nil, false
}
