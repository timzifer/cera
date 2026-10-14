package cera

import (
	"context"
	"encoding/binary"
	"unicode/utf16"

	"github.com/go-opentype/opentype"
	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdffont"
)

// What a document says of its fonts and what cera draws them with: the
// fonts a page shows text in, and for each whether its program is in the
// file, and the stand-in that draws it where it is not, or cannot be read.
// A viewer's "document properties" lists them; they are what stands behind
// the font-missing keys of Stats.Unsupported.

// Subset reports that the font's name carries a subset tag ("ABCDEF+"):
// the program in the file has only some of its glyphs.
func (f *Font) Subset() bool { return f.subset }

// Type returns the font's type, the /Subtype of its dictionary: "Type1",
// "MMType1", "TrueType", "Type3", or for a composite font "Type0" with its
// descendant's after a slash, "Type0/CIDFontType0" or
// "Type0/CIDFontType2".
func (f *Font) Type() string { return f.kind }

// Embedded reports that the file holds the font's program (/FontFile,
// /FontFile2 or /FontFile3). A Type 3 font draws its glyphs from content
// streams of the file and counts as embedded.
func (f *Font) Embedded() bool { return f.embedded }

// DrawnWith returns the name of the font cera draws f with in place of a
// program the file does not hold or that cera cannot read: a font of the
// document's FontProvider, or one of cera's stand-ins ("TeX Gyre Heros",
// "Arimo", …). It is empty when the font is drawn with its own program,
// and for a composite font cera has nothing to draw with.
func (f *Font) DrawnWith() string {
	if !f.substituted || f.program == nil {
		return ""
	}
	return programName(f.program)
}

// Unreadable reports that the file holds a program for the font that cera
// cannot read: it draws the font as if the file did not hold one.
func (f *Font) Unreadable() bool { return f.bad }

// fontLister collects the fonts a page shows text in.
type fontLister struct {
	nullDevice
	fonts []*Font
	seen  map[*Font]bool
}

func (l *fontLister) ShowText(run *GlyphRun, _ TextMode) {
	if f := run.Font; f != nil && !l.seen[f] {
		l.seen[f] = true
		l.fonts = append(l.fonts, f)
	}
}

// Fonts returns the fonts the page shows text in, in the order it first
// does: in its content and the forms, patterns and Type 3 glyphs it draws,
// in the appearances of its annotations, and in every layer, shown or not.
// A font the resources name but no text uses is not among them.
func (p *Page) Fonts(ctx context.Context) ([]*Font, error) {
	l := &fontLister{seen: map[*Font]bool{}}
	if err := p.RunWith(ctx, l, RunOptions{Scale: 1, Layers: &Visibility{}}); err != nil {
		return nil, err
	}
	return l.fonts, nil
}

// DocumentFont is a font of a document and the pages that show text in
// it.
type DocumentFont struct {
	*Font
	// Pages are the 0-based indices of the pages, in order.
	Pages []int
}

// Fonts returns the fonts of the document: those its pages show text in
// (see Page.Fonts), in the order they are first shown, each with its
// pages. It runs every page.
func (d *Document) Fonts(ctx context.Context) ([]DocumentFont, error) {
	var out []DocumentFont
	at := map[*Font]int{}
	for i := range d.NumPages() {
		p, err := d.Page(i)
		if err != nil {
			return nil, err
		}
		fonts, err := p.Fonts(ctx)
		if err != nil {
			return nil, err
		}
		for _, f := range fonts {
			k, ok := at[f]
			if !ok {
				k = len(out)
				at[f] = k
				out = append(out, DocumentFont{Font: f})
			}
			out[k].Pages = append(out[k].Pages, i)
		}
	}
	return out, nil
}

// fontFacts sets what Type, Subset and Embedded report, from the font's
// dictionary.
func (f *Font) fontFacts(d *Document, dict pdf.Dict) {
	if n, ok := d.name(dict.Get("BaseFont")); ok {
		f.subset = isTagged(string(n))
	}
	sub, _ := d.name(dict.Get("Subtype"))
	f.kind = string(sub)
	if sub == "Type0" {
		if a, _ := d.resolve(dict.Get("DescendantFonts")).Array(); len(a) > 0 {
			if ds, ok := d.name(d.dict(a[0]).Get("Subtype")); ok {
				f.kind += "/" + string(ds)
			}
		}
	}
	if f.pdf.Kind() == pdffont.Type3 {
		f.embedded = true
		return
	}
	desc := f.pdf.Descriptor()
	for _, k := range []pdf.Name{"FontFile", "FontFile2", "FontFile3"} {
		if _, ok := d.resolve(desc.Get(k)).Stream(); ok {
			f.embedded = true
		}
	}
}

// programName returns the full name of a font program, from its name
// table (name 4, or 1 and 2), or its PostScript name (6).
func programName(p *opentype.Font) string {
	b, ok := p.Table("name")
	if !ok || len(b) < 6 {
		return ""
	}
	count, strOff := int(binary.BigEndian.Uint16(b[2:])), int(binary.BigEndian.Uint16(b[4:]))
	found := map[uint16]string{}
	for i := range count {
		r := 6 + 12*i
		if r+12 > len(b) {
			break
		}
		platform, enc := binary.BigEndian.Uint16(b[r:]), binary.BigEndian.Uint16(b[r+2:])
		id := binary.BigEndian.Uint16(b[r+6:])
		n, off := int(binary.BigEndian.Uint16(b[r+8:])), int(binary.BigEndian.Uint16(b[r+10:]))
		if strOff+off+n > len(b) || found[id] != "" {
			continue
		}
		s := b[strOff+off : strOff+off+n]
		switch {
		case platform == 3 && (enc == 1 || enc == 10), platform == 0:
			u := make([]uint16, len(s)/2)
			for k := range u {
				u[k] = binary.BigEndian.Uint16(s[2*k:])
			}
			found[id] = string(utf16.Decode(u))
		case platform == 1 && enc == 0:
			found[id] = string(s) // Mac Roman; the names are ASCII
		}
	}
	if s := found[4]; s != "" {
		return s
	}
	if s := found[1]; s != "" {
		if st := found[2]; st != "" && st != "Regular" {
			return s + " " + st
		}
		return s
	}
	return found[6]
}
