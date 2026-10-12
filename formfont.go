package cera

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"slices"
	"sync"
	"unicode/utf16"

	"github.com/go-opentype/fonts/arimo"
	"github.com/go-opentype/opentype"
	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// Fallback fonts of generated appearances. The font of a field, and
// Helvetica in its place, is a simple font with one alphabet. A character
// it lacks is shown with the first of these fonts that has it: Arimo,
// which is linked anyway and has Greek and Cyrillic, then the fonts the
// document's FontProvider has for the CJK character collections. They are
// TrueType programs addressed by glyph: composite fonts with the Identity-H
// encoding, whose codes are the glyph indices of the program.
//
// The renderer draws with the whole program. What the editor writes into a
// file is a subset of it with the glyphs used, their widths, a /CIDToGIDMap
// from the codes — still the indices of the whole program, so that both
// write the same content — to the glyphs of the subset, and a /ToUnicode
// map, so that the text can be searched and copied.

// fallbackFont is one fallback font of a document.
type fallbackFont struct {
	res  pdf.Name // its name in the resources of an appearance
	ref  pdf.Ref  // stands for it where the renderer generates
	name string   // /BaseFont
	data []byte

	once  sync.Once
	prog  *opentype.Font // nil: not a program that can be used
	perEm float64

	fontOnce sync.Once
	font     *Font // what the renderer draws it with
}

// program returns the parsed program, nil unless it is a TrueType program
// with a Unicode character map. A variable font whose default is lighter
// than regular (Noto Sans JP, SC and KR are Thin) is used at weight 400:
// the file holds a static font, which has the weight of the default.
func (fb *fallbackFont) program() *opentype.Font {
	fb.once.Do(func() {
		p, err := opentype.Parse(fb.data)
		if err != nil {
			return
		}
		for _, a := range p.Axes() {
			if a.Tag != "wght" || a.Default >= 400 || a.Max < 400 {
				continue
			}
			if data, err := p.InstanceBytes(map[string]float64{"wght": 400}); err == nil {
				if q, err := opentype.Parse(data); err == nil {
					p, fb.data = q, data
				}
			}
		}
		if !p.HasCharacterMap() || p.UnitsPerEm() <= 0 {
			return
		}
		if _, ok := p.Table("glyf"); !ok {
			return
		}
		fb.prog, fb.perEm = p, float64(p.UnitsPerEm())
	})
	return fb.prog
}

// glyph returns the glyph of r.
func (fb *fallbackFont) glyph(r rune) (opentype.GlyphIndex, bool) {
	p := fb.program()
	if p == nil {
		return 0, false
	}
	gid, ok := p.GlyphIndex(r)
	return gid, ok && gid != 0 && int(gid) < p.NumGlyphs() && int(gid) <= math.MaxUint16
}

// advance returns the width of glyph gid in em.
func (fb *fallbackFont) advance(gid opentype.GlyphIndex) float64 {
	return float64(fb.prog.GlyphAdvance(gid)) / fb.perEm
}

// The collections a FontProvider is asked for, in this order.
var fallbackOrderings = []string{"Japan1", "GB1", "CNS1", "Korea1"}

// fallbackFonts returns the fallback fonts of the document, in the order
// they are tried.
func (d *Document) fallbackFonts() []*fallbackFont {
	d.ff.fbOnce.Do(func() {
		add := func(name string, data []byte) {
			if len(data) == 0 {
				return
			}
			for _, fb := range d.ff.fb {
				if &fb.data[0] == &data[0] {
					return // a provider answers several requests with one font
				}
			}
			n := len(d.ff.fb)
			d.ff.fb = append(d.ff.fb, &fallbackFont{
				res: pdf.Name(fmt.Sprintf("CeraFb%d", n)), ref: pdf.Ref{Num: helvRef.Num - 1 - int32(n)}, name: name, data: data,
			})
		}
		add("Arimo", arimo.TTF)
		if d.fontProvider == nil {
			return
		}
		for _, o := range fallbackOrderings {
			if data, ok := d.fontProvider.Font(FontRequest{Registry: "Adobe", Ordering: o}); ok {
				add("Fallback-"+o, data)
			}
		}
	})
	return d.ff.fb
}

// fallbackGlyph returns the first fallback font that has r, and its glyph.
func (d *Document) fallbackGlyph(r rune) (*fallbackFont, opentype.GlyphIndex, bool) {
	for _, fb := range d.fallbackFonts() {
		if gid, ok := fb.glyph(r); ok {
			return fb, gid, true
		}
	}
	return nil, 0, false
}

// fallbackFont returns the font the renderer draws ref with, a reference
// standing for a fallback font.
func (d *Document) fallbackFont(ref pdf.Ref) *Font {
	for _, fb := range d.fallbackFonts() {
		if fb.ref != ref {
			continue
		}
		fb.fontOnce.Do(func() {
			if fb.program() == nil {
				return
			}
			file := pdf.NewStream(pdf.NewDict(pdf.Entry{Key: "Length1", Val: pdf.Integer(int64(len(fb.data)))}), fb.data)
			f := d.loadFont(fb.dict(fb.name, file.Object(), pdf.Name("Identity").Object(), nil))
			if f.program == nil {
				return
			}
			f.glyphWidths = true
			fb.font = f
		})
		return fb.font
	}
	return nil
}

// dict returns the font dictionary of the fallback font: a Type 0 font
// with the Identity-H encoding over the TrueType program file, named name,
// with the glyph of each code from c2g, the given entries of the CIDFont
// (/W) and of the font itself (/ToUnicode).
func (fb *fallbackFont) dict(name string, file, c2g pdf.Object, cid []pdf.Entry, font ...pdf.Entry) pdf.Dict {
	p := fb.prog
	u := func(v int) pdf.Object { return pdf.Real(math.Round(float64(v) * 1000 / fb.perEm)) }
	x0, y0, x1, y1 := p.FontBBox()
	base := pdf.Name(name).Object()
	desc := pdf.NewDict(
		pdf.Entry{Key: "Type", Val: pdf.Name("FontDescriptor").Object()},
		pdf.Entry{Key: "FontName", Val: base},
		pdf.Entry{Key: "Flags", Val: pdf.Integer(4)}, // symbolic: no standard alphabet
		pdf.Entry{Key: "FontBBox", Val: pdf.Array{u(x0), u(y0), u(x1), u(y1)}.Object()},
		pdf.Entry{Key: "ItalicAngle", Val: pdf.Real(p.ItalicAngle())},
		pdf.Entry{Key: "Ascent", Val: u(p.Ascent())},
		pdf.Entry{Key: "Descent", Val: u(p.Descent())},
		pdf.Entry{Key: "CapHeight", Val: u(p.CapHeight())},
		pdf.Entry{Key: "StemV", Val: pdf.Integer(int64(p.StemV()))},
		pdf.Entry{Key: "FontFile2", Val: file},
	)
	kid := append([]pdf.Entry{
		{Key: "Type", Val: pdf.Name("Font").Object()},
		{Key: "Subtype", Val: pdf.Name("CIDFontType2").Object()},
		{Key: "BaseFont", Val: base},
		{Key: "CIDSystemInfo", Val: pdf.NewDict(
			pdf.Entry{Key: "Registry", Val: pdf.String([]byte("Adobe"))},
			pdf.Entry{Key: "Ordering", Val: pdf.String([]byte("Identity"))},
			pdf.Entry{Key: "Supplement", Val: pdf.Integer(0)},
		).Object()},
		{Key: "FontDescriptor", Val: desc.Object()},
		{Key: "CIDToGIDMap", Val: c2g},
		{Key: "DW", Val: pdf.Integer(1000)},
	}, cid...)
	return pdf.NewDict(append([]pdf.Entry{
		{Key: "Type", Val: pdf.Name("Font").Object()},
		{Key: "Subtype", Val: pdf.Name("Type0").Object()},
		{Key: "BaseFont", Val: base},
		{Key: "Encoding", Val: pdf.Name("Identity-H").Object()},
		{Key: "DescendantFonts", Val: pdf.Array{pdf.NewDict(kid...).Object()}.Object()},
	}, font...)...)
}

// genFonts names the fonts of generated appearances in their resources,
// and hears what they show.
type genFonts interface {
	// helvetica returns Helvetica with WinAnsiEncoding.
	helvetica() pdf.Object
	// fallback returns the font fb.
	fallback(fb *fallbackFont) pdf.Object
	// used says that glyph gid of fb shows r.
	used(fb *fallbackFont, gid opentype.GlyphIndex, r rune)
	// missing says that no font has r: it is shown as '?'.
	missing(r rune)
}

// renderFonts are the fonts where the renderer generates an appearance:
// references no file defines, which Document.font recognises.
type renderFonts struct{}

func (renderFonts) helvetica() pdf.Object                         { return helvRef.Object() }
func (renderFonts) fallback(fb *fallbackFont) pdf.Object          { return fb.ref.Object() }
func (renderFonts) used(*fallbackFont, opentype.GlyphIndex, rune) {}
func (renderFonts) missing(rune)                                  {}

// editFonts are the fonts of the appearances an editor writes: new
// objects. Object 0 is Helvetica; a fallback font gets an object when an
// appearance names it, which finish fills in when all are written.
type editFonts struct {
	objs  *[]pdf.Object
	fonts []*editFont
	// lacking reports a character no font has.
	lacking bool
}

// editFont is a fallback font being written.
type editFont struct {
	fb     *fallbackFont
	obj    int // its place among the new objects
	glyphs map[opentype.GlyphIndex]rune
}

func (ef *editFonts) helvetica() pdf.Object { return pdfedit.NewRef(0).Object() }

func (ef *editFonts) font(fb *fallbackFont) *editFont {
	for _, f := range ef.fonts {
		if f.fb == fb {
			return f
		}
	}
	f := &editFont{fb: fb, obj: len(*ef.objs), glyphs: map[opentype.GlyphIndex]rune{}}
	*ef.objs = append(*ef.objs, pdf.Null)
	ef.fonts = append(ef.fonts, f)
	return f
}

func (ef *editFonts) fallback(fb *fallbackFont) pdf.Object {
	return pdfedit.NewRef(ef.font(fb).obj).Object()
}

func (ef *editFonts) used(fb *fallbackFont, gid opentype.GlyphIndex, r rune) {
	if f := ef.font(fb); f.glyphs[gid] == 0 {
		f.glyphs[gid] = r
	}
}

func (ef *editFonts) missing(rune) { ef.lacking = true }

// finish writes the fallback fonts the appearances named.
func (ef *editFonts) finish() {
	add := func(o pdf.Object) pdf.Object {
		*ef.objs = append(*ef.objs, o)
		return pdfedit.NewRef(len(*ef.objs) - 1).Object()
	}
	for _, f := range ef.fonts {
		fb := f.fb
		gids := make([]opentype.GlyphIndex, 0, len(f.glyphs))
		for g := range f.glyphs {
			gids = append(gids, g)
		}
		slices.Sort(gids)
		if len(gids) == 0 {
			continue
		}

		// The program: the glyphs used, renumbered, and where each code
		// finds its glyph. A program that cannot be subset is written
		// whole.
		program, c2g := fb.data, pdf.Name("Identity").Object()
		if sub, renumbered, err := fb.prog.SubsetTrueType(gids); err == nil {
			program = sub
			m := make([]byte, 2*(int(gids[len(gids)-1])+1))
			for _, g := range gids {
				binary.BigEndian.PutUint16(m[2*int(g):], uint16(renumbered[g]))
			}
			c2g = add(pdfwrite.Flate(pdf.NewDict(), m).Object())
		}
		file := add(pdfwrite.Flate(pdf.NewDict(pdf.Entry{Key: "Length1", Val: pdf.Integer(int64(len(program)))}), program).Object())

		// /W: runs of consecutive codes, each with its widths, not
		// rounded: a line is as long in the file as the renderer drew it.
		var widths pdf.Array
		for i := 0; i < len(gids); {
			j := i
			var run pdf.Array
			for ; j < len(gids) && int(gids[j])-int(gids[i]) == j-i; j++ {
				run = append(run, pdf.Real(fb.advance(gids[j])*1000))
			}
			widths = append(widths, pdf.Integer(int64(gids[i])), run.Object())
			i = j
		}

		// The name: a tag that stands for the subset.
		h := fnv.New32a()
		for _, g := range gids {
			h.Write([]byte{byte(g >> 8), byte(g)})
		}
		tag, sum := make([]byte, 6), h.Sum32()
		for i := range tag {
			tag[i] = 'A' + byte(sum%26)
			sum /= 26
		}
		(*ef.objs)[f.obj] = fb.dict(string(tag)+"+"+fb.name, file, c2g,
			[]pdf.Entry{{Key: "W", Val: widths.Object()}},
			pdf.Entry{Key: "ToUnicode", Val: add(pdfwrite.Flate(pdf.NewDict(), toUnicode(gids, f.glyphs)).Object())},
		).Object()
	}
}

// toUnicode returns the /ToUnicode CMap of two-byte codes showing the
// given characters.
func toUnicode(codes []opentype.GlyphIndex, chars map[opentype.GlyphIndex]rune) []byte {
	b := []byte("/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n" +
		"/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n" +
		"/CMapName /Adobe-Identity-UCS def\n/CMapType 2 def\n" +
		"1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n")
	for i := 0; i < len(codes); i += 100 {
		part := codes[i:min(i+100, len(codes))]
		b = fmt.Appendf(b, "%d beginbfchar\n", len(part))
		for _, c := range part {
			b = fmt.Appendf(b, "<%04X> <", c)
			for _, u := range utf16.Encode([]rune{chars[c]}) {
				b = fmt.Appendf(b, "%04X", u)
			}
			b = append(b, ">\n"...)
		}
		b = append(b, "endbfchar\n"...)
	}
	return append(b, "endcmap\nCMapName currentdict /CMap defineresource pop\nend\nend\n"...)
}
