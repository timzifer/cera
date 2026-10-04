package cera

import (
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/go-opentype/fonts/arimo"
	"github.com/go-opentype/fonts/cousine"
	"github.com/go-opentype/fonts/tinos"
	"github.com/go-opentype/opentype"
	"github.com/go-pdfkit/reader"

	"github.com/timzifer/cera/internal/stdfont"
)

// A PDF need not carry its fonts: the fourteen standard faces never are,
// and many files name any font and hope. Such text is drawn, in this
// order, with a program a FontProvider supplies, with the stand-ins for
// Symbol and ZapfDingbats (their own alphabets, from internal/stdfont), or
// with a stand-in chosen by name and descriptor from three families
// metric-compatible with Helvetica, Times and Courier. Widths the document
// gives always win, so lines keep their length whatever draws them.
// Composite fonts get a program only from a provider.

// FontProvider supplies programs for fonts a document names but does not
// embed: CJK fonts (see the module github.com/timzifer/cera/fonts/cjk) or
// the fonts of the system the document is shown on. Font is called once
// per font and document, possibly from several goroutines at a time.
type FontProvider interface {
	// Font returns a font program — TrueType, OpenType (CFF), a bare CFF
	// or a Type 1 font — for the font req describes, ok false for none.
	// For a composite font (req.Ordering set) it is either a CID-keyed
	// program of that character collection, whose glyphs are found by
	// CID, or any other program with a Unicode character map, whose
	// glyphs are found by the character each CID stands for.
	Font(req FontRequest) (program []byte, ok bool)
}

// FontRequest describes a font a document does not embed.
type FontRequest struct {
	// Name is the font's BaseFont without a subset prefix, as the document
	// spells it ("Arial,Bold", "MS-Mincho").
	Name string
	// Registry, Ordering and Supplement are the character collection of a
	// composite font ("Adobe", "Japan1", 6), empty for a simple font.
	Registry, Ordering string
	Supplement         int
	// Vertical is a composite font written top to bottom.
	Vertical bool
	// The style, from the name and the font descriptor.
	Serif, FixedPitch, Symbolic, Bold, Italic bool
	// Weight is the descriptor's /FontWeight (400 regular, 700 bold), 0 if
	// it gives none.
	Weight int
}

// OpenOptions are the options of OpenWith.
type OpenOptions struct {
	// Password opens an encrypted document.
	Password string
	// Fonts supplies programs for fonts the document does not embed; nil
	// for the built-in stand-ins only.
	Fonts FontProvider
	// NaiveCMYK converts DeviceCMYK, and ICC profiles of four components
	// cera does not read, with R = (1-C)(1-K) and its like instead of
	// through a press profile (CGATS TR 005, SWOP), for callers who want
	// the device values: fills, strokes, shadings and images.
	NaiveCMYK bool
}

// Font descriptor flags.
const (
	flagFixedPitch = 1 << 0
	flagSerif      = 1 << 1
	flagSymbolic   = 1 << 2
	flagItalic     = 1 << 6
	flagForceBold  = 1 << 18
)

// request describes the font for a FontProvider and the stand-ins.
func (f *Font) request(d *Document) FontRequest {
	req := FontRequest{
		Name: f.name, Registry: f.registry, Ordering: f.ordering,
		Supplement: f.supplement, Vertical: f.vertical,
	}
	desc := f.pdf.Descriptor()
	flags, _ := d.num(desc["Flags"])
	fl := int(flags)
	lower := strings.ToLower(f.name)
	req.Serif = fl&flagSerif != 0
	req.FixedPitch = fl&flagFixedPitch != 0
	req.Symbolic = fl&flagSymbolic != 0
	if w, ok := d.num(desc["FontWeight"]); ok && w >= 100 && w <= 1000 {
		req.Weight = int(w)
	}
	req.Bold = fl&flagForceBold != 0 || req.Weight >= 600 || hasAny(lower, boldWords)
	if !req.Bold && req.Weight == 0 && !hasAny(lower, lightWords) {
		if w, ok := d.num(desc["StemV"]); ok && w >= 120 {
			req.Bold = true
		}
	}
	req.Italic = fl&flagItalic != 0 || hasAny(lower, italicWords)
	if a, ok := d.num(desc["ItalicAngle"]); ok && math.Abs(a) >= 4 && math.Abs(a) < 45 {
		req.Italic = true
	}
	return req
}

var (
	boldWords   = []string{"bold", "black", "heavy", "demi", ",bd", "-bd"}
	lightWords  = []string{"light", "thin", "hairline"}
	italicWords = []string{"italic", "oblique", "slanted", "kursiv", ",it", "-it"}
)

func hasAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// substitute gives a font without a usable program one from the provider
// or a stand-in.
func (f *Font) substitute(d *Document) {
	req := f.request(d)
	if p := d.fontProvider; p != nil {
		if data, ok := p.Font(req); ok && len(data) > 0 {
			if prog := parseProgram(data); prog != nil {
				f.attach(prog)
				f.substituted = true
				f.byUnicode = f.composite() && !prog.IsCIDKeyed()
				return
			}
		}
	}
	if f.composite() {
		// Glyphs addressed by number: no stand-in draws them.
		return
	}
	if f.std != nil {
		if prog := stdProgram(f.std); prog != nil {
			f.attach(prog)
			f.substituted = true
			f.stdGlyphs = true
		}
		return
	}
	f.standIn(req, stretchOf(d, f.pdf.Descriptor()))
}

// parseProgram reads a program a provider supplied, whatever its format.
func parseProgram(data []byte) *opentype.Font {
	if p, err := opentype.Parse(data); err == nil {
		return p
	}
	if p, err := opentype.ParseCFF(data); err == nil {
		return p
	}
	if p, err := opentype.ParseType1(data); err == nil {
		return p
	}
	return nil
}

// baseKey reduces a font name to what identifies its family: the part
// before a style suffix (",Bold", "-Italic"), in lower case, letters and
// digits only, without the foundry's suffixes ("PSMT", "MT", "Std").
func baseKey(name string) string {
	if i := strings.IndexAny(name, ",-"); i > 0 {
		name = name[:i]
	}
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	k := b.String()
	for _, suf := range []string{"psmt", "mt", "ps", "std", "pro", "lt"} {
		if len(k) > len(suf)+3 && strings.HasSuffix(k, suf) {
			k = k[:len(k)-len(suf)]
		}
	}
	return k
}

// stdAlphabet returns the standard font with its own alphabet a name
// stands for: Symbol or ZapfDingbats.
func stdAlphabet(name string) *stdfont.Font {
	switch k := baseKey(name); {
	case k == "symbol":
		return stdfont.Symbol
	case strings.Contains(k, "dingbats"):
		return stdfont.ZapfDingbats
	}
	return nil
}

var stdPrograms sync.Map // *stdfont.Font → *opentype.Font

func stdProgram(s *stdfont.Font) *opentype.Font {
	if p, ok := stdPrograms.Load(s); ok {
		return p.(*opentype.Font)
	}
	p, err := opentype.Parse(s.Program())
	if err != nil {
		return nil
	}
	got, _ := stdPrograms.LoadOrStore(s, p)
	return got.(*opentype.Font)
}

// stdGlyph is the glyph of the standard alphabet a code shows: the one the
// document names, or the one of the built-in encoding.
func (f *Font) stdGlyph(code int) (*stdfont.Glyph, bool) {
	if code < 0 || code > 255 {
		return nil, false
	}
	if f.pdf.Chosen(code) {
		if name, ok := f.pdf.GlyphName(code); ok {
			if g, ok := f.std.Glyph(name); ok {
				return g, true
			}
		}
	}
	return f.std.Code(byte(code))
}

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

// fontClass is how a stand-in draws a family: which of the three, and
// how much narrower or wider.
type fontClass struct {
	fam     *family
	stretch float64 // 0: 1
}

var (
	sans  = fontClass{fam: &sansFamily}
	serif = fontClass{fam: &serifFamily}
	mono  = fontClass{fam: &monoFamily}
)

func narrow(c fontClass, s float64) fontClass { c.stretch = s; return c }

// knownFonts maps the families most often named without being embedded to
// a class, by baseKey. A key also matches names it is a prefix of
// ("helveticaneue" matches "HelveticaNeueLTStd-Roman"); the longest wins.
var knownFonts = map[string]fontClass{
	// Sans serif.
	"helvetica": sans, "arial": sans, "arialnarrow": narrow(sans, 0.82),
	"helveticanarrow": narrow(sans, 0.82), "helveticaneue": sans,
	"helveticacondensed": narrow(sans, 0.82), "arialunicode": sans,
	"calibri": narrow(sans, 0.9), "verdana": sans, "tahoma": sans,
	"segoeui": sans, "trebuchet": sans, "centurygothic": sans,
	"franklingothic": sans, "futura": sans, "gillsans": sans, "lucidasans": sans,
	"lucidagrande": sans, "myriad": sans, "frutiger": sans, "univers": sans,
	"candara": sans, "corbel": sans, "liberationsans": sans, "dejavusans": sans,
	"opensans": sans, "roboto": sans, "lato": sans, "sourcesans": sans,
	"notosans": sans, "microsoftsansserif": sans,
	"msreferencesansserif": sans, "agaramond": serif, "avenir": sans,
	"bahnschrift": narrow(sans, 0.9), "aptos": sans, "ebrima": sans,
	"meta": sans, "dinpro": sans, "din": sans, "officinasans": sans,
	"rotis": sans, "syntax": sans, "thesans": sans, "stonesans": sans,
	"impact": narrow(sans, 0.75), "haettenschweiler": narrow(sans, 0.7),
	"agencyfb": narrow(sans, 0.7), "arialblack": sans,
	// Serif.
	"times": serif, "timesnewroman": serif, "timesroman": serif,
	"cambria": serif, "georgia": serif, "garamond": serif, "bookantiqua": serif,
	"palatino": serif, "palatinolinotype": serif, "bookman": serif,
	"bookmanoldstyle": serif, "centuryschoolbook": serif, "century": serif,
	"constantia": serif, "minion": serif, "baskerville": serif, "didot": serif,
	"bodoni": serif, "caslon": serif, "charter": serif, "utopia": serif,
	"liberationserif": serif, "dejavuserif": serif, "sylfaen": serif,
	"bembo": serif, "sabon": serif, "janson": serif, "plantin": serif,
	"stempelgaramond": serif, "adobegaramond": serif, "goudy": serif,
	"rockwell": serif, "lucidabright": serif, "notoserif": serif,
	"sourceserif": serif, "newcenturyschlbk": serif, "newcenturyschoolbook": serif,
	"perpetua": serif, "calisto": serif, "cochin": serif, "hoefler": serif,
	"timesnewromancondensed": narrow(serif, 0.82),
	// Monospaced.
	"courier": mono, "couriernew": mono, "consolas": mono, "lucidaconsole": mono,
	"lucidasanstypewriter": mono, "monaco": mono, "menlo": mono,
	"andalemono": mono, "liberationmono": mono, "dejavusansmono": mono,
	"sourcecodepro": mono, "sourcecode": mono, "inconsolata": mono,
	"lettergothic": mono, "ocrb": mono, "ocra": mono, "prestige": mono,
	"cascadia": mono, "cascadiacode": mono, "cascadiamono": mono,
	"firacode": mono, "firamono": mono, "jetbrainsmono": mono, "msgothic": mono,
	"orator": mono, "notomono": mono, "robotomono": mono, "ubuntumono": mono,
}

// knownKeys are the keys of knownFonts, longest first, for prefix matches.
var knownKeys = func() []string {
	keys := make([]string, 0, len(knownFonts))
	for k := range knownFonts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	return keys
}()

// classOf finds the class of a font: by its name in knownFonts, by words
// in its name, then by its descriptor.
func classOf(req FontRequest) fontClass {
	lower := strings.ToLower(req.Name)
	c := knownClass(baseKey(req.Name))
	if c.fam == nil {
		switch {
		case strings.Contains(lower, "courier") || strings.Contains(lower, "mono") ||
			strings.Contains(lower, "typewriter") || strings.Contains(lower, "consol"):
			c = mono
		case strings.Contains(lower, "sans") || strings.Contains(lower, "gothic") ||
			strings.Contains(lower, "grotesk") || strings.Contains(lower, "grotesque"):
			c = sans
		case strings.Contains(lower, "times") || strings.Contains(lower, "roman") ||
			strings.Contains(lower, "serif") || strings.Contains(lower, "book") ||
			strings.Contains(lower, "antiqua") || strings.Contains(lower, "mincho"):
			c = serif
		case req.FixedPitch:
			c = mono
		case req.Serif:
			c = serif
		default:
			c = sans
		}
	}
	if c.stretch == 0 && hasAny(lower, []string{"narrow", "condensed", "compressed"}) && !strings.Contains(lower, "semicond") {
		c.stretch = 0.82
	}
	return c
}

// knownClass looks a baseKey up in knownFonts, exactly or by its longest
// known prefix; the zero fontClass if neither.
func knownClass(k string) fontClass {
	if c, ok := knownFonts[k]; ok {
		return c
	}
	for _, key := range knownKeys {
		if len(key) >= 4 && strings.HasPrefix(k, key) {
			return knownFonts[key]
		}
	}
	return fontClass{}
}

// stretchOf reads /FontStretch.
func stretchOf(d *Document, desc reader.Dict) float64 {
	n, _ := d.name(desc["FontStretch"])
	switch n {
	case "UltraCondensed":
		return 0.6
	case "ExtraCondensed":
		return 0.7
	case "Condensed":
		return 0.82
	case "SemiCondensed":
		return 0.9
	case "SemiExpanded":
		return 1.1
	case "Expanded":
		return 1.2
	case "ExtraExpanded":
		return 1.3
	case "UltraExpanded":
		return 1.4
	}
	return 0
}

// standIn gives a simple font without a usable program one of the three
// families; stretch is the descriptor's /FontStretch (0: none).
func (f *Font) standIn(req FontRequest, stretch float64) {
	c := classOf(req)
	i := 0
	if req.Bold {
		i |= 1
	}
	if req.Italic {
		i |= 2
	}
	p := c.fam[i].get()
	if p == nil {
		return
	}
	f.attach(p)
	f.substituted = true
	switch {
	case c.stretch > 0:
		f.stretch = c.stretch
	case stretch > 0:
		f.stretch = stretch
	}
}

// missingKey is the Stats.Unsupported key of text in a font nothing could
// be found for: font-missing, or for a composite font font-missing- and
// its character collection ("font-missing-japan1"), so the report splits
// it by script.
func (f *Font) missingKey() string {
	if !f.composite() {
		return "font-missing"
	}
	switch strings.ToLower(f.ordering) {
	case "japan1":
		return "font-missing-japan1"
	case "gb1":
		return "font-missing-gb1"
	case "cns1":
		return "font-missing-cns1"
	case "korea1", "kr":
		return "font-missing-korea1"
	}
	return "font-missing-cid"
}
