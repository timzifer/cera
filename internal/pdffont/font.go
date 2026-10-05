// Package pdffont is the PDF side of a font: not its outlines, which are the
// font program's business, but what the document says about reading a string
// of bytes with it — which codes the string holds, how wide each is, what each
// glyph is called and what text it stands for (ADR 0012).
//
// The encoding tables follow PDF 32000-2 Annex D. Behaviour follows
// go-pdfkit/pdffont v0.3.1, which cera used before, except that
// MacRomanEncoding and MacExpertEncoding are their own tables rather than
// StandardEncoding.
package pdffont

import (
	"sync"

	"github.com/timzifer/cera/internal/pdf"
)

// A Kind says how a font's codes are read.
type Kind int

const (
	// Simple fonts take one byte a code and are addressed by glyph name.
	Simple Kind = iota
	// Composite fonts take a character identifier, usually two bytes wide.
	Composite
	// Type3 fonts draw each glyph by running a content stream.
	Type3
)

// A Font is a font as the document describes it. Its methods are safe for
// concurrent use once SetFallback has been called, if it is.
type Font struct {
	kind       Kind
	dict       pdf.Dict
	descriptor pdf.Dict
	doc        *pdf.Document

	// Simple and Type 3 fonts: what each code is called, whether the
	// document named it itself (in /Differences or by naming an encoding),
	// and how wide it is.
	names    [256]string
	named    bitset
	chosen   bitset
	widths   [256]float64
	hasWidth bitset

	// Composite fonts: widths by character identifier.
	cidWidths cidWidths

	defaultW float64
	symbolic bool

	// fallback is a last resort for codes the document says nothing about:
	// what the embedded program calls them.
	fallback func(code int) (string, bool)

	cidToGID []byte

	fontMatrix [6]float64
	charProcs  pdf.Dict
	t3Res      pdf.Dict

	// The ToUnicode map is read the first time text is asked for:
	// rendering does not need it.
	uniOnce sync.Once
	toUni   map[int]string
}

// A bitset is a set of byte codes.
type bitset [4]uint64

func (b *bitset) set(c int)      { b[c>>6] |= 1 << (c & 63) }
func (b *bitset) has(c int) bool { return c >= 0 && c < 256 && b[c>>6]&(1<<(c&63)) != 0 }

// Read takes what a font dictionary says. It never fails: a dictionary that
// says nothing useful gives a font that reads its codes one byte at a time
// and advances by half an em.
func Read(d *pdf.Document, dict pdf.Dict) *Font {
	f := &Font{
		doc: d, dict: dict, defaultW: 0.5,
		fontMatrix: [6]float64{0.001, 0, 0, 0.001, 0, 0},
	}
	switch sub, _ := d.Resolve(dict.Get("Subtype")).Name(); sub {
	case "Type0":
		f.kind = Composite
		f.readComposite()
	case "Type3":
		f.kind = Type3
		f.readType3()
	default:
		f.kind = Simple
		f.readSimple()
	}
	return f
}

// Kind says how this font's codes are read.
func (f *Font) Kind() Kind { return f.kind }

// Dict is the font dictionary itself.
func (f *Font) Dict() pdf.Dict { return f.dict }

// Descriptor is the font descriptor, where an embedded program lives.
func (f *Font) Descriptor() pdf.Dict { return f.descriptor }

// Symbolic reports a font that says it is addressed through its own
// character map rather than through an encoding of names.
func (f *Font) Symbolic() bool { return f.symbolic }

// FontMatrix is how a Type 3 font's glyph space relates to text space; for
// every other kind it is a thousandth.
func (f *Font) FontMatrix() [6]float64 { return f.fontMatrix }

// CharProcs are a Type 3 font's glyph drawings, by name.
func (f *Font) CharProcs() pdf.Dict { return f.charProcs }

// Type3Resources are what a Type 3 font's glyph drawings draw with.
func (f *Font) Type3Resources() pdf.Dict { return f.t3Res }

// Width is how far the pen moves for one code, in text space: a multiple of
// the point size.
func (f *Font) Width(code int) float64 {
	if f.kind == Composite {
		if w, ok := f.cidWidths.get(code); ok {
			return w
		}
		return f.defaultW
	}
	if f.hasWidth.has(code) {
		return f.widths[code]
	}
	return f.defaultW
}

// HasWidth reports whether the document said how wide this code is.
func (f *Font) HasWidth(code int) bool {
	if f.kind == Composite {
		_, ok := f.cidWidths.get(code)
		return ok
	}
	return f.hasWidth.has(code)
}

// GlyphName is what the document's encoding calls a code. ok is false for a
// composite font, which names nothing, and for a code the encoding passes
// over.
func (f *Font) GlyphName(code int) (string, bool) {
	if f.kind == Composite || !f.named.has(code) {
		return "", false
	}
	return f.names[code], true
}

// Chosen reports whether the document named this code itself rather than
// inheriting the name from a base encoding.
func (f *Font) Chosen(code int) bool { return f.chosen.has(code) }

// Text is what a code stands for as characters: what the ToUnicode map says,
// or failing that what its glyph name says, or what the fallback says. ok is
// false when none of them does.
func (f *Font) Text(code int) (string, bool) {
	f.uniOnce.Do(f.readToUnicode)
	if s, ok := f.toUni[code]; ok && s != "" {
		return s, true
	}
	if name, ok := f.GlyphName(code); ok && f.namedByTheDocument(code) {
		if text, ok := TextOfGlyphName(name); ok {
			return text, true
		}
	}
	if f.fallback != nil {
		if s, ok := f.fallback(code); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

// SetFallback installs a last resort for codes the document says nothing
// about: what the embedded font program calls them. It must be called
// before the font is shared.
func (f *Font) SetFallback(fn func(code int) (string, bool)) { f.fallback = fn }

// namedByTheDocument reports whether a code's name can be believed as a
// statement about which character it is: a symbolic font's base encoding is
// a guess, a name it chose itself is not.
func (f *Font) namedByTheDocument(code int) bool {
	return !f.symbolic || f.chosen.has(code)
}

// CIDToGID maps a character identifier to a glyph number for a composite
// font. ok is false when the font's map does not reach that far.
func (f *Font) CIDToGID(cid int) (int, bool) {
	if f.cidToGID == nil {
		return cid, true
	}
	i := cid * 2
	if i < 0 || i+1 >= len(f.cidToGID) {
		return 0, false
	}
	return int(f.cidToGID[i])<<8 | int(f.cidToGID[i+1]), true
}

// Program is the embedded font program: which key it arrived under, and its
// bytes. ok is false for a font whose program is not embedded.
func (f *Font) Program() (pdf.Name, []byte, bool) {
	for _, key := range [...]pdf.Name{"FontFile2", "FontFile3", "FontFile"} {
		s, ok := f.doc.Resolve(f.descriptor.Get(key)).Stream()
		if !ok {
			continue
		}
		if data, ok := f.decode(s); ok {
			return key, data, true
		}
	}
	return "", nil, false
}

// decode decodes a stream the font keeps what it makes of, strictly: a
// stream that does not decode cleanly, or stops at an image filter, gives
// nothing.
func (f *Font) decode(s *pdf.Stream) ([]byte, bool) {
	dec := f.doc.DecodeUncached(s)
	if dec.Recovered || dec.Image != "" {
		return nil, false
	}
	return dec.Data, true
}
