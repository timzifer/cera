package cera

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-opentype/fonts/arimo"
	"github.com/go-opentype/fonts/cousine"
	"github.com/go-opentype/fonts/tinos"
	"github.com/go-opentype/opentype"
	"github.com/go-pdfkit/pdffont"
	"github.com/go-pdfkit/reader"
)

// Fonts split the work as the spec's layers do: go-pdfkit/pdffont reads what
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
	// substituted says the program is a stand-in for one the document
	// does not carry.
	substituted bool
	// vertical is a composite font written top to bottom (Identity-V).
	vertical bool
	// ascent and descent are em fractions, for text boxes.
	ascent, descent float64

	mu sync.Mutex
	// outlines are the glyphs read so far, by glyph index, in em units. A
	// nil entry is a glyph the program does not have (or an empty one).
	// Paths are never modified once stored, so devices may read them from
	// any goroutine.
	outlines map[opentype.GlyphIndex]*Path
	// gids caches the glyph index of each code of a simple font (-1 = not
	// yet looked up).
	gids [256]int32
	// procs are the decoded glyph procedures of a Type 3 font, by name.
	procs map[string][]byte
}

var fontIDs atomic.Uint64

// Name returns the font's BaseFont without a subset prefix ("ABCDEF+").
func (f *Font) Name() string { return f.name }

// Text returns the characters a code stands for: the font's ToUnicode map,
// or what its encoding calls the glyph. ok is false when the document does
// not say.
func (f *Font) Text(code int) (string, bool) { return f.pdf.Text(code) }

// Type3 reports a font whose glyphs are content streams (their Glyph has no
// Outline).
func (f *Font) Type3() bool { return f.pdf.Kind() == pdffont.Type3 }

// Metrics returns the ascent and descent (negative) as fractions of the em.
func (f *Font) Metrics() (ascent, descent float64) { return f.ascent, f.descent }

func (f *Font) composite() bool { return f.pdf.Kind() == pdffont.Composite }

// font returns the font a font resource stands for. Fonts reached through
// an indirect reference are loaded once per document.
func (d *Document) font(o reader.Object) *Font {
	ref, isRef := o.(reader.Ref)
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
	if dict != nil {
		f = d.loadFont(dict)
	}
	if isRef {
		d.fontMu.Lock()
		if d.fonts == nil {
			d.fonts = map[reader.Ref]*Font{}
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

func (d *Document) loadFont(dict reader.Dict) *Font {
	pf := pdffont.Read(d.r, dict)
	f := &Font{id: fontIDs.Add(1), pdf: pf, ascent: 0.8, descent: -0.2}
	for i := range f.gids {
		f.gids[i] = -1
	}
	if n, ok := d.name(dict["BaseFont"]); ok {
		f.name = string(n)
		if len(f.name) > 7 && f.name[6] == '+' {
			f.name = f.name[7:]
		}
	}
	if f.composite() {
		if n, ok := d.name(dict["Encoding"]); ok && strings.HasSuffix(string(n), "-V") {
			f.vertical = true
		}
	}
	if pf.Kind() == pdffont.Type3 {
		return f
	}
	if key, data, ok := pf.Program(); ok {
		if p, err := readProgram(key, data); err == nil {
			f.attach(p)
		}
	}
	if f.program == nil {
		f.standIn(d)
	}
	if f.program != nil && f.pdf.Symbolic() {
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
	if a, dsc := float64(p.Ascent())/f.perEm, float64(p.Descent())/f.perEm; a > 0 && a < 2 && dsc <= 0 && dsc > -1 {
		f.ascent, f.descent = a, dsc
	}
}

// readProgram decodes an embedded font program. FontFile2 is TrueType,
// FontFile a Type 1 program, FontFile3 a bare CFF program or a whole
// OpenType font.
func readProgram(key reader.Name, data []byte) (*opentype.Font, error) {
	switch key {
	case "FontFile":
		return opentype.ParseType1(data)
	case "FontFile3":
		if f, err := opentype.Parse(data); err == nil {
			return f, nil
		}
		return opentype.ParseCFF(data)
	}
	return opentype.Parse(data)
}

// advance returns how far the pen moves for code, in em. A stand-in for a
// font the document gives no widths for moves by its own advances (Arimo,
// Tinos and Cousine are metric-compatible with Helvetica, Times and
// Courier).
func (f *Font) advance(code int) float64 {
	if f.pdf.HasWidth(code) || !f.substituted {
		return f.pdf.Width(code)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return float64(f.face.AdvanceIndex(f.glyphIndex(code))) / f.perEm
}

// glyph returns the glyph index and the outline (in em, nil if none) of a
// code.
func (f *Font) glyph(code int) (gid opentype.GlyphIndex, outline *Path) {
	if f.program == nil {
		return 0, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	gid = f.glyphIndex(code)
	if p, ok := f.outlines[gid]; ok {
		return gid, p
	}
	segs, ok := f.face.GlyphOutline(gid)
	if ok && len(segs) > 0 {
		outline = segmentsPath(segs, 1/f.perEm)
	}
	if f.outlines == nil {
		f.outlines = map[opentype.GlyphIndex]*Path{}
	}
	f.outlines[gid] = outline
	return gid, outline
}

// segmentsPath converts an outline in font units to a Path scaled by s.
func segmentsPath(segs []opentype.Segment, s float64) *Path {
	p := new(Path)
	pt := func(q opentype.Point) (float32, float32) { return float32(q.X * s), float32(q.Y * s) }
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
		// A CID-keyed CFF program maps identifiers through its own
		// charset; CIDToGIDMap is defined only for TrueType-based ones.
		if f.program.IsCIDKeyed() {
			gid, _ := f.program.GlyphIndexByCID(code)
			return gid
		}
		gid, _ := f.pdf.CIDToGID(code)
		return opentype.GlyphIndex(gid)
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
		if !p.HasCharacterMap() {
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

// A PDF need not carry its fonts: the fourteen standard faces never are,
// and many files name any font and hope. Such text is drawn with a stand-in
// chosen by name and descriptor flags from three families metric-compatible
// with Helvetica, Times and Courier.

type standIn struct {
	once sync.Once
	ttf  []byte
	font *opentype.Font
}

func (s *standIn) get() *opentype.Font {
	s.once.Do(func() { s.font, _ = opentype.Parse(s.ttf) })
	return s.font
}

// family holds regular, bold, italic and bold italic.
type family [4]standIn

var (
	sansFamily  = family{{ttf: arimo.TTF}, {ttf: arimo.Bold}, {ttf: arimo.Italic}, {ttf: arimo.BoldItalic}}
	serifFamily = family{{ttf: tinos.TTF}, {ttf: tinos.Bold}, {ttf: tinos.Italic}, {ttf: tinos.BoldItalic}}
	monoFamily  = family{{ttf: cousine.TTF}, {ttf: cousine.Bold}, {ttf: cousine.Italic}, {ttf: cousine.BoldItalic}}
)

// Font descriptor flags.
const (
	flagFixedPitch = 1 << 0
	flagSerif      = 1 << 1
	flagItalic     = 1 << 6
	flagForceBold  = 1 << 18
)

// standIn gives a font without a usable program a stand-in. Composite
// fonts address glyphs by number and Symbol and ZapfDingbats have their own
// alphabets: no stand-in draws them correctly, so they get none.
func (f *Font) standIn(d *Document) {
	lower := strings.ToLower(f.name)
	if f.composite() || lower == "symbol" || strings.Contains(lower, "dingbats") {
		return
	}
	desc := f.pdf.Descriptor()
	flags, _ := d.num(desc["Flags"])
	fl := int(flags)
	fam := &sansFamily
	switch {
	case strings.Contains(lower, "courier") || strings.Contains(lower, "mono"):
		fam = &monoFamily
	case strings.Contains(lower, "times") || strings.Contains(lower, "roman") ||
		strings.Contains(lower, "serif") || strings.Contains(lower, "georgia") ||
		strings.Contains(lower, "garamond") || strings.Contains(lower, "book"):
		fam = &serifFamily
	case strings.Contains(lower, "helvetica") || strings.Contains(lower, "arial") ||
		strings.Contains(lower, "sans"):
	case fl&flagFixedPitch != 0:
		fam = &monoFamily
	case fl&flagSerif != 0:
		fam = &serifFamily
	}
	bold := strings.Contains(lower, "bold") || strings.Contains(lower, "black") ||
		strings.Contains(lower, "heavy") || fl&flagForceBold != 0
	if !bold {
		if w, ok := d.num(desc["StemV"]); ok && w >= 120 {
			bold = true
		}
	}
	italic := strings.Contains(lower, "italic") || strings.Contains(lower, "oblique") || fl&flagItalic != 0
	i := 0
	if bold {
		i |= 1
	}
	if italic {
		i |= 2
	}
	if p := fam[i].get(); p != nil {
		f.attach(p)
		f.substituted = true
	}
}
