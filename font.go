package cera

import (
	"sync"
	"sync/atomic"

	"github.com/go-opentype/opentype"
	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdffont"

	"github.com/timzifer/cera/internal/cmap"
	"github.com/timzifer/cera/internal/stdfont"
)

// Fonts split the work as the spec's layers do: internal/pdffont reads what
// the document says about a font (codes, widths, encodings, ToUnicode), and
// go-opentype/opentype reads the embedded program (TrueType, CFF, Type 1)
// that the outlines come from. cera keeps each outline once per font, as a
// Path in em units, and hands it to the device; the raster device caches
// what it draws of it (see glyphcache.go).

// Font is a font of a document, as the interpreter uses it. Devices get it
// with every GlyphRun: for the text a code stands for (Text) and for what
// the font is called (Name).
type Font struct {
	// id identifies the font in glyph caches that outlive the document.
	id   uint64
	pdf  *pdffont.Font
	name string // BaseFont without a subset tag

	// program is the embedded font program or a stand-in, face the handle
	// its outlines are read through (not safe for concurrent use; guarded
	// by mu). Outlines come in font units; perEm scales them to em.
	program *opentype.Font
	face    *opentype.Face
	perEm   float64
	// fdScales replaces perEm per glyph for a CID-keyed CFF program that
	// gives its FontMatrix in the FDArray (cffcid.go).
	fdScales *cffFDScales
	// shear is the linear part of the program's FontMatrix when it does
	// more than scale (fontmatrix.go); it replaces perEm.
	shear *[4]float64
	// substituted says the program is a stand-in for one the document
	// does not carry (from a FontProvider or built in); bad that the
	// document carries one cera cannot read.
	substituted bool
	bad         bool
	// stretch scales the outlines of a stand-in horizontally (narrow
	// faces); 0 is 1.
	stretch float64
	// bolden thickens the strokes of a stand-in by this much, in em
	// (fontsubst.go); 0 for none.
	bolden float64
	// std is the alphabet of a font named Symbol or ZapfDingbats (its
	// built-in encoding, AFM widths and characters), stdGlyphs that its
	// program is cera's stand-in for it.
	std       *stdfont.Font
	stdGlyphs bool

	// cmap reads the codes of a composite font (fontcid.go); cmapMissing
	// says the font names one cera does not know.
	cmap        *cmap.CMap
	cmapMissing bool
	// registry, ordering and supplement are a composite font's character
	// collection ("Adobe", "Japan1", 6).
	registry, ordering string
	supplement         int
	// byUnicode says the program is keyed by Unicode, not by CID (a
	// provider's stand-in for a composite font): CIDs find their glyphs
	// through the characters they stand for.
	byUnicode bool
	// vertical is a composite font written top to bottom (WMode 1), with
	// its default and per-CID vertical metrics (/DW2, /W2).
	vertical bool
	dw2      vmetric
	w2       map[int]vmetric
	// gsub finds vertical glyph forms ('vert'); vert caches them (f.mu).
	gsub *opentype.GSUB
	vert map[opentype.GlyphIndex]opentype.GlyphIndex
	// ascent and descent are em fractions, for text boxes.
	ascent, descent float64

	mu sync.Mutex
	// outlines are the glyphs read so far, by glyph index, in em units. A
	// nil entry is a glyph the program does not have (or an empty one).
	// Paths are never modified once stored, so devices may read them from
	// any goroutine.
	outlines map[opentype.GlyphIndex]*Path
	// builtinBase says the codes of a simple font are the program's own:
	// a symbolic Type 1 or CFF program and no /Encoding at all, so the
	// built-in encoding comes before StandardEncoding (PDF 2.0 9.6.5.2,
	// as PDFium and pdf.js read it).
	builtinBase bool
	// hasToUnicode says the font dictionary has a ToUnicode map.
	hasToUnicode bool
	// gids caches the glyph index of each code of a simple font (-1 = not
	// yet looked up).
	gids [256]int32
	// procs are the decoded glyph procedures of a Type 3 font, by name.
	procs map[string][]byte

	// enc maps characters to the codes of a simple font, for generated
	// appearances (formgen.go).
	encOnce sync.Once
	enc     map[rune]byte
}

var fontIDs atomic.Uint64

// Name returns the font's BaseFont without a subset prefix ("ABCDEF+").
func (f *Font) Name() string { return f.name }

// Text returns the characters a code stands for: the font's ToUnicode map,
// what its encoding calls the glyph, or for fonts of the Adobe CJK
// character collections what the code's CID stands for. ok is false when
// none says.
func (f *Font) Text(code int) (string, bool) {
	if f.std != nil && !f.pdf.Chosen(code) && code >= 0 && code <= 255 {
		// Symbol and ZapfDingbats: what the document does not name, the
		// built-in encoding does (not StandardEncoding, as pdffont
		// assumes).
		if f.hasToUnicode {
			if s, ok := f.pdf.Text(code); ok {
				return s, true
			}
		}
		if g, ok := f.std.Code(byte(code)); ok && g.Rune != 0 {
			return string(g.Rune), true
		}
		return "", false
	}
	return f.pdf.Text(code)
}

// Vertical reports a font written top to bottom.
func (f *Font) Vertical() bool { return f.vertical }

// Type3 reports a font whose glyphs are content streams (their Glyph has no
// Outline).
func (f *Font) Type3() bool { return f.pdf.Kind() == pdffont.Type3 }

// Metrics returns the ascent and descent (negative) as fractions of the em.
func (f *Font) Metrics() (ascent, descent float64) { return f.ascent, f.descent }

func (f *Font) composite() bool { return f.pdf.Kind() == pdffont.Composite }

// font returns the font a font resource stands for. Fonts reached through
// an indirect reference are loaded once per document.
func (d *Document) font(o pdf.Object) *Font {
	ref, isRef := o.Ref()
	if isRef && ref == helvRef {
		return d.helvetica()
	}
	if isRef {
		d.fontMu.Lock()
		f, ok := d.fonts[ref]
		d.fontMu.Unlock()
		if ok {
			return f
		}
	}
	dict := d.dict(o)
	var f *Font
	if !dict.IsZero() {
		f = d.loadFont(dict)
	}
	if isRef {
		d.fontMu.Lock()
		if d.fonts == nil {
			d.fonts = map[pdf.Ref]*Font{}
		}
		if g, ok := d.fonts[ref]; ok {
			f = g // loaded concurrently
		} else {
			d.fonts[ref] = f
		}
		d.fontMu.Unlock()
	}
	return f
}

func (d *Document) loadFont(dict pdf.Dict) *Font {
	pf := pdffont.Read(d.r, dict)
	f := &Font{id: fontIDs.Add(1), pdf: pf, ascent: 0.8, descent: -0.2}
	for i := range f.gids {
		f.gids[i] = -1
	}
	if n, ok := d.name(dict.Get("BaseFont")); ok {
		f.name = string(n)
		if len(f.name) > 7 && f.name[6] == '+' {
			f.name = f.name[7:]
		}
	}
	f.hasToUnicode = !d.resolve(dict.Get("ToUnicode")).IsNull()
	if f.composite() {
		f.readCMap(d, dict)
	}
	if pf.Kind() == pdffont.Type3 {
		return f
	}
	if !f.composite() {
		f.std = stdAlphabet(f.name)
	}
	if key, data, ok := pf.Program(); ok {
		if p, err := readProgram(key, data); err == nil {
			f.attach(p)
			if key == "FontFile3" {
				f.fdScales = readCFFFDScales(data)
			}
			if m, ok := fontShear(string(key), data); ok {
				f.shear = &m
			}
		} else {
			f.bad = true
		}
	}
	if f.program == nil {
		f.substitute(d)
	}
	if f.program != nil && !f.substituted && !f.composite() && f.pdf.Symbolic() && !isTrueType(f.program) &&
		d.resolve(dict.Get("Encoding")).IsNull() {
		f.builtinBase = true
	}
	if f.vertical && f.program != nil {
		f.gsub = f.program.GSUB()
	}
	if f.program != nil && !f.composite() && f.pdf.Symbolic() {
		// A symbolic font's codes are its own; what the program calls
		// them is the best text there is for codes the document does not
		// name.
		pf.SetFallback(func(code int) (string, bool) {
			f.mu.Lock()
			gid := f.glyphIndex(code)
			f.mu.Unlock()
			if name, ok := f.program.GlyphName(gid); ok {
				return pdffont.TextOfGlyphName(name)
			}
			return "", false
		})
	}
	return f
}

func (f *Font) attach(p *opentype.Font) {
	f.program = p
	f.perEm = float64(p.UnitsPerEm())
	if !(f.perEm > 0) {
		f.perEm = 1000
	}
	f.face = p.NewFace(p.UnitsPerEm())
	f.lineMetrics(p)
}

// lineMetrics takes the ascent and descent of p, when they are sane.
func (f *Font) lineMetrics(p *opentype.Font) {
	em := float64(p.UnitsPerEm())
	if !(em > 0) {
		em = 1000
	}
	if a, dsc := float64(p.Ascent())/em, float64(p.Descent())/em; a > 0 && a < 2 && dsc <= 0 && dsc > -1 {
		f.ascent, f.descent = a, dsc
	}
}

// readProgram decodes an embedded font program. FontFile is a Type 1
// program (or, from some writers, a bare CFF one), FontFile2 TrueType,
// FontFile3 a bare CFF program or a whole OpenType font. Programs the
// parsers reject are read again repaired where a repair is known
// (fontrepair.go).
func readProgram(key pdf.Name, data []byte) (*opentype.Font, error) {
	switch key {
	case "FontFile":
		if isCFF(data) {
			return parseCFF(data)
		}
		f, err := opentype.ParseType1(data)
		if err != nil {
			if fixed, ok := repairPFB(data); ok {
				if g, err2 := opentype.ParseType1(fixed); err2 == nil {
					return g, nil
				}
			}
		}
		return f, err
	case "FontFile3":
		if f, err := parseSFNT(data); err == nil {
			return f, nil
		}
		return parseCFF(data)
	}
	return parseSFNT(data)
}

// parseCFF parses a bare CFF program, again with its DICTs repaired
// (repairCFF) when opentype.ParseCFF rejects it.
func parseCFF(data []byte) (*opentype.Font, error) {
	f, err := opentype.ParseCFF(data)
	if err != nil {
		if fixed, ok := repairCFF(data); ok {
			if g, err2 := opentype.ParseCFF(fixed); err2 == nil {
				return g, nil
			}
		}
	}
	return f, err
}

// parseSFNT parses a TrueType or OpenType program, the first font of a
// collection (firstOfCollection), and again repaired (repairSFNT) when
// opentype.Parse rejects it.
func parseSFNT(data []byte) (*opentype.Font, error) {
	if first, ok := firstOfCollection(data); ok {
		data = first
	}
	f, err := opentype.Parse(data)
	if err != nil {
		if fixed, ok := repairSFNT(data); ok {
			if g, err2 := opentype.Parse(fixed); err2 == nil {
				return g, nil
			}
		}
	}
	return f, err
}

// advance returns how far the pen moves for code (a CID for a composite
// font), in em. A stand-in for a font the document gives no widths for
// moves by the AFM widths of Symbol and ZapfDingbats, or by its own
// advances (all stand-ins are metric-compatible with Helvetica, Times or
// Courier).
func (f *Font) advance(code int) float64 {
	if f.pdf.HasWidth(code) || !f.substituted || f.composite() {
		return f.pdf.Width(code)
	}
	if f.stdGlyphs {
		if g, ok := f.stdGlyph(code); ok {
			return float64(g.Width) / 1000
		}
		return f.pdf.Width(code)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w := float64(f.face.AdvanceIndex(f.glyphIndex(code))) / f.perEm
	if f.stretch > 0 {
		w *= f.stretch
	}
	return w
}

// glyph returns the glyph index and the outline (in em, nil if none) of a
// code (a CID for a composite font).
func (f *Font) glyph(code int) (gid opentype.GlyphIndex, outline *Path) {
	if f.program == nil {
		return 0, nil
	}
	sx := 1.0
	if f.stretch > 0 {
		sx = f.stretch
	}
	var target float64 // the width a standard alphabet's glyph is drawn to
	if f.stdGlyphs {
		target = f.advance(code)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	gid = f.glyphIndex(code)
	if p, ok := f.outlines[gid]; ok {
		return gid, p
	}
	if target > 0 {
		// Drawn to the AFM width (or the document's), as the other
		// stand-ins are metric-compatible by design.
		if a := float64(f.face.AdvanceIndex(gid)) / f.perEm; a > 0 {
			sx = min(max(target/a, 0.5), 2)
		}
	}
	ux, uy := 1/f.perEm, 1/f.perEm
	if f.fdScales != nil {
		ux, uy = f.fdScales.scale(int(gid))
	}
	m := [4]float64{sx * ux, 0, 0, uy}
	if f.shear != nil {
		m = *f.shear
		m[0], m[2] = sx*m[0], sx*m[2]
	}
	segs, ok := f.face.GlyphOutline(gid)
	if ok && len(segs) > 0 {
		if f.bolden > 0 {
			segs = embolden(segs, f.bolden*f.perEm)
		}
		outline = segmentsPath(segs, m)
	}
	if f.outlines == nil {
		f.outlines = map[opentype.GlyphIndex]*Path{}
	}
	f.outlines[gid] = outline
	return gid, outline
}

// segmentsPath converts an outline in font units to a Path transformed by
// m: x' = m[0]x + m[2]y, y' = m[1]x + m[3]y.
func segmentsPath(segs []opentype.Segment, m [4]float64) *Path {
	p := new(Path)
	pt := func(q opentype.Point) (float32, float32) {
		return float32(m[0]*q.X + m[2]*q.Y), float32(m[1]*q.X + m[3]*q.Y)
	}
	for _, g := range segs {
		switch g.Op {
		case opentype.SegMoveTo:
			p.MoveTo(pt(g.P[0]))
		case opentype.SegLineTo:
			p.LineTo(pt(g.P[0]))
		case opentype.SegQuadTo:
			cx, cy := pt(g.P[0])
			x, y := pt(g.P[1])
			p.QuadTo(cx, cy, x, y)
		case opentype.SegClose:
			p.Close()
		}
	}
	if p.Empty() {
		return nil
	}
	return p
}

// glyphIndex works out which glyph of the program a code stands for (the
// rules of the go-pdfkit/render M2 spike, measured on its corpus). f.mu is
// held.
func (f *Font) glyphIndex(code int) opentype.GlyphIndex {
	if f.composite() {
		return f.cidGlyph(code)
	}
	if code < 0 || code > 255 {
		return 0
	}
	if g := f.gids[code]; g >= 0 {
		return opentype.GlyphIndex(g)
	}
	gid := f.simpleGlyphIndex(code)
	f.gids[code] = int32(gid)
	return gid
}

func (f *Font) simpleGlyphIndex(code int) opentype.GlyphIndex {
	p := f.program
	if f.stdGlyphs {
		if g, ok := f.stdGlyph(code); ok {
			return opentype.GlyphIndex(g.GID)
		}
		return 0
	}
	if f.builtinBase {
		if gid, ok := p.GlyphIndexByCode(byte(code)); ok && gid != 0 {
			return gid
		}
	}
	if f.pdf.Symbolic() {
		if gid, ok := f.byChar(code); ok {
			return gid
		}
	}
	if name, ok := f.pdf.GlyphName(code); ok {
		if gid, ok := p.GlyphIndexByName(name); ok && gid != 0 {
			return gid
		}
		if r, ok := pdffont.RuneOfGlyphName(name); ok {
			if gid, ok := p.GlyphIndex(r); ok && gid != 0 {
				return gid
			}
		}
		if !p.HasCharacterMap() && isTrueType(p) {
			if i, ok := macGlyphIndex()[name]; ok && i < p.NumGlyphs() {
				return opentype.GlyphIndex(i)
			}
		}
	}
	if gid, ok := f.byChar(code); ok {
		return gid
	}
	if gid, ok := p.GlyphIndexByCode(byte(code)); ok && gid != 0 {
		return gid
	}
	return opentype.GlyphIndex(code)
}

// isTrueType reports a program with TrueType outlines. Only those may be
// read in the Macintosh glyph order: a Type 1 or CFF program without a
// cmap keeps its glyphs in its own order and has its own encoding.
func isTrueType(p *opentype.Font) bool {
	_, ok := p.Table("glyf")
	return ok
}

// byChar looks a code up as a character, then in the range reserved for a
// symbolic font's own glyphs.
func (f *Font) byChar(code int) (opentype.GlyphIndex, bool) {
	for _, r := range [2]rune{rune(code), rune(0xF000 + code)} {
		if gid, ok := f.program.GlyphIndex(r); ok && gid != 0 {
			return gid, true
		}
	}
	return 0, false
}
