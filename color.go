package cera

import (
	"sync"

	"github.com/timzifer/cera/internal/pdf"

	"github.com/timzifer/cera/internal/cmyk"
)

type csKind uint8

const (
	csGray csKind = iota
	csRGB
	csCMYK
	csCIE // CalGray, CalRGB, Lab, ICCBased with a profile that is read
	csIndexed
	csTint // Separation and DeviceN
	csPattern
)

// colorSpace is a resolved PDF colour space. It is immutable once made,
// apart from lookup tables made on first use, and shared by the pages of
// a document.
type colorSpace struct {
	kind   csKind
	n      int         // components of a colour value
	base   *colorSpace // Indexed, Pattern (uncoloured), and the alternate of a tint space
	hival  int         // Indexed
	lookup []byte      // Indexed
	cie    *cieSpace
	// fn is the tint transform of a Separation or DeviceN space, nil if it
	// cannot be read (tints are then drawn as grey); none marks the
	// separation /None, which paints nothing.
	fn   function
	none bool
	// colorants are the names of the colorants of a tint space.
	colorants []string
	// cmyk is the profile a CMYK space converts through, nil for the naive
	// conversion; icc returns its bytes, nil for the bundled one.
	cmyk *cmyk.Table
	icc  func() []byte

	once sync.Once
	tint *[256][3]uint8 // one-component tint transform, tabulated
}

var (
	spaceGray = &colorSpace{kind: csGray, n: 1}
	spaceRGB  = &colorSpace{kind: csRGB, n: 3}
	// spaceCMYK is DeviceCMYK through the bundled profile.
	spaceCMYK = &colorSpace{kind: csCMYK, n: 4, cmyk: cmyk.Default()}
	// spaceCMYKNaive is DeviceCMYK without a profile (OpenOptions.NaiveCMYK).
	spaceCMYKNaive = &colorSpace{kind: csCMYK, n: 4}
	spacePattern   = &colorSpace{kind: csPattern, n: 0}
)

// initial returns the initial colour of cs (PDF 2.0, 8.6.5).
func (cs *colorSpace) initial(v []float64) {
	for i := range v {
		v[i] = 0
	}
	switch cs.kind {
	case csCMYK:
		v[3] = 1
	case csTint:
		for i := range cs.n {
			v[i] = 1
		}
	case csCIE:
		if cs.cie.lab {
			// L* = 0, and a*, b* clipped into their range.
			v[1] = clampTo(0, cs.cie.rng[0], cs.cie.rng[1])
			v[2] = clampTo(0, cs.cie.rng[2], cs.cie.rng[3])
		}
	}
}

// rgb converts a colour value of cs to straight sRGB components in [0, 1].
func (cs *colorSpace) rgb(v []float64) (r, g, b float64) {
	switch cs.kind {
	case csGray:
		return v[0], v[0], v[0]
	case csRGB:
		return v[0], v[1], v[2]
	case csCMYK:
		if cs.cmyk != nil {
			return cs.cmyk.RGB(v[0], v[1], v[2], v[3])
		}
		k := 1 - clamp01(v[3])
		return (1 - clamp01(v[0])) * k, (1 - clamp01(v[1])) * k, (1 - clamp01(v[2])) * k
	case csCIE:
		return cs.cie.rgb(v)
	case csIndexed:
		var c [maxComps]float64
		n := cs.indexed(v, c[:])
		return cs.base.rgb(c[:n])
	case csTint:
		return cs.tintRGB(v)
	}
	return 0, 0, 0
}

// indexed writes the base components of the palette entry of the index
// v[0] to c and returns how many there are.
func (cs *colorSpace) indexed(v, c []float64) int {
	i := int(v[0] + 0.5)
	i = min(max(i, 0), cs.hival)
	n := cs.base.n
	for j := range n {
		c[j] = 0
		if k := i*n + j; k < len(cs.lookup) {
			c[j] = float64(cs.lookup[k]) / 255
		}
	}
	if cs.base.kind == csCIE && cs.base.cie.lab {
		// Lab entries are bytes over the ranges of L*, a* and b*.
		c[0] *= 100
		rng := &cs.base.cie.rng
		c[1] = rng[0] + c[1]*(rng[1]-rng[0])
		c[2] = rng[2] + c[2]*(rng[3]-rng[2])
	}
	return n
}

// tintRGB converts tints through the tint transform and the alternate
// space; a one-component transform is tabulated at 256 tints.
func (cs *colorSpace) tintRGB(v []float64) (r, g, b float64) {
	if cs.fn == nil {
		t := 0.0
		for _, x := range v[:cs.n] {
			t = max(t, clamp01(x))
		}
		return 1 - t, 1 - t, 1 - t
	}
	if cs.n == 1 {
		cs.once.Do(func() {
			var tab [256][3]uint8
			var in [1]float64
			for i := range tab {
				in[0] = float64(i) / 255
				r, g, b := cs.evalTint(in[:])
				tab[i] = [3]uint8{unit8(r), unit8(g), unit8(b)}
			}
			cs.tint = &tab
		})
		c := cs.tint[unit8(v[0])]
		return float64(c[0]) / 255, float64(c[1]) / 255, float64(c[2]) / 255
	}
	return cs.evalTint(v[:cs.n])
}

func (cs *colorSpace) evalTint(v []float64) (r, g, b float64) {
	out := cs.fn.eval(v)
	var c [maxComps]float64
	copy(c[:cs.base.n], out)
	return cs.base.rgb(c[:cs.base.n])
}

// csEntry is a colour space cached by reference, with the feature it only
// approximates.
type csEntry struct {
	cs     *colorSpace
	approx string
}

// colorSpace resolves a colour space operand or resource entry. approx names
// a feature that is only approximated, or is empty. Spaces named by
// reference are made once per document. A device space named at depth 0
// (an operand, an image's or a shading's /ColorSpace) is remapped by the
// Default space of res; the device spaces inside other spaces are not.
func (d *Document) colorSpace(o pdf.Object, res pdf.Dict, depth int) (cs *colorSpace, approx string) {
	if depth > 4 {
		return nil, ""
	}
	if n, ok := o.Name(); ok {
		if dev := deviceComps(n); dev > 0 && depth == 0 {
			return d.defaultSpace(res, dev)
		}
		switch n {
		case "DeviceGray", "G", "CalGray":
			return spaceGray, ""
		case "DeviceRGB", "RGB", "CalRGB":
			return spaceRGB, ""
		case "DeviceCMYK", "CMYK":
			return d.deviceCMYK()
		case "Pattern":
			return spacePattern, ""
		}
		if o, ok := d.dict(res.Get("ColorSpace")).Lookup(n); ok {
			return d.colorSpace(o, res, depth+1)
		}
		return nil, ""
	}
	ref, isRef := o.Ref()
	if isRef {
		d.spMu.Lock()
		e, ok := d.spaces[ref]
		d.spMu.Unlock()
		if ok {
			return e.cs, e.approx
		}
	}
	cs, approx = d.makeColorSpace(d.resolve(o), res, depth)
	if isRef {
		d.spMu.Lock()
		if d.spaces == nil {
			d.spaces = map[pdf.Ref]csEntry{}
		}
		d.spaces[ref] = csEntry{cs, approx}
		d.spMu.Unlock()
	}
	return cs, approx
}

// deviceComps returns the components of the device space a name (or its
// inline image abbreviation) names, 0 for any other name.
func deviceComps(n pdf.Name) int {
	switch n {
	case "DeviceGray", "G":
		return 1
	case "DeviceRGB", "RGB":
		return 3
	case "DeviceCMYK", "CMYK":
		return 4
	}
	return 0
}

// defaultNames are the Default spaces by the components of their device
// space.
var defaultNames = [5]pdf.Name{1: "DefaultGray", 3: "DefaultRGB", 4: "DefaultCMYK"}

// defaultSpace is the device space of n components as the resources res
// remap it (PDF 2.0, 8.6.5.6): through their DefaultGray, DefaultRGB or
// DefaultCMYK, if it is a space of as many components (not a pattern or
// Indexed space), else the device space itself. NaiveCMYK keeps
// DeviceCMYK naive.
func (d *Document) defaultSpace(res pdf.Dict, n int) (*colorSpace, string) {
	dev, devApprox := d.deviceSpace(n), ""
	if n == 4 {
		dev, devApprox = d.deviceCMYK()
		if d.naiveCMYK {
			return dev, ""
		}
	}
	o, ok := d.dict(res.Get("ColorSpace")).Lookup(defaultNames[n])
	if !ok {
		return dev, devApprox
	}
	// Depth 1: the Default space is not remapped in turn.
	cs, approx := d.colorSpace(o, pdf.Dict{}, 1)
	if cs == nil || cs.n != n || cs.kind == csPattern || cs.kind == csIndexed {
		return dev, devApprox
	}
	return cs, approx
}

func (d *Document) makeColorSpace(o pdf.Object, res pdf.Dict, depth int) (cs *colorSpace, approx string) {
	if n, ok := o.Name(); ok {
		return d.colorSpace(n.Object(), res, depth+1)
	}
	a, ok := o.Array()
	if !ok || len(a) == 0 {
		return nil, ""
	}
	fam, _ := d.name(a[0])
	param := func() pdf.Dict {
		if len(a) < 2 {
			return pdf.Dict{}
		}
		return d.dict(a[1])
	}
	switch fam {
	case "DeviceGray", "G":
		return spaceGray, ""
	case "DeviceRGB", "RGB":
		return spaceRGB, ""
	case "DeviceCMYK", "CMYK":
		return d.deviceCMYK()
	case "CalGray", "CalRGB":
		n := 1
		dev := spaceGray
		if fam == "CalRGB" {
			n, dev = 3, spaceRGB
		}
		p := param()
		s := calSpace(n, d.floats(p.Get("WhitePoint")), d.gammas(p.Get("Gamma"), n), d.floats(p.Get("Matrix")))
		if s == nil {
			return dev, ""
		}
		return &colorSpace{kind: csCIE, n: n, cie: s}, ""
	case "Lab":
		p := param()
		s := labSpace(d.floats(p.Get("WhitePoint")), d.floats(p.Get("Range")))
		return &colorSpace{kind: csCIE, n: 3, cie: s}, ""
	case "ICCBased":
		if len(a) < 2 {
			return nil, ""
		}
		s, ok := d.resolve(a[1]).Stream()
		if !ok {
			return nil, ""
		}
		n, _ := d.integer(s.Dict.Get("N"))
		ref, _ := a[1].Ref()
		if n == 4 {
			return d.iccCMYK(ref, s)
		}
		if e, ok := d.iccTabled(ref); ok {
			return e.cs, e.approx
		}
		if prof, srgb := iccProfile(d.r.Decode(s).Data, n); prof != nil {
			if srgb {
				return d.deviceSpace(prof.n), ""
			}
			cs := &colorSpace{kind: csCIE, n: prof.n, cie: prof}
			if prof.table != nil {
				return d.keepTabled(ref, cs)
			}
			return cs, ""
		}
		// A profile cera does not read: the device space of as many
		// components, or the alternate the space names.
		switch n {
		case 1:
			return spaceGray, "icc-profile"
		case 3:
			return spaceRGB, "icc-profile"
		}
		if alt, ok := s.Dict.Lookup("Alternate"); ok {
			cs, _ := d.colorSpace(alt, res, depth+1)
			return cs, "icc-profile"
		}
		return nil, ""
	case "Indexed", "I":
		if len(a) < 4 {
			return nil, ""
		}
		base, approx := d.colorSpace(a[1], res, depth+1)
		hival, ok := d.num(a[2])
		if base == nil || base.kind == csIndexed || base.kind == csPattern || !ok {
			return nil, ""
		}
		var lookup []byte
		if v := d.resolve(a[3]); v.Kind() == pdf.KindString {
			lookup, _ = v.Str()
		} else if s, ok := v.Stream(); ok {
			lookup = d.r.Decode(s).Data
		}
		return &colorSpace{kind: csIndexed, n: 1, base: base, hival: min(max(int(hival), 0), 255), lookup: lookup}, approx
	case "Separation", "DeviceN":
		return d.tintSpace(fam, a, res, depth)
	case "Pattern":
		cs := &colorSpace{kind: csPattern}
		if len(a) >= 2 {
			if base, _ := d.colorSpace(a[1], res, depth+1); base != nil && base.kind != csPattern {
				cs.base, cs.n = base, base.n
			}
		}
		return cs, ""
	}
	return nil, ""
}

// iccTabled returns the space of the grey or RGB profile object ref that
// was tabulated before.
func (d *Document) iccTabled(ref pdf.Ref) (csEntry, bool) {
	if ref == (pdf.Ref{}) {
		return csEntry{}, false
	}
	d.csMu.Lock()
	defer d.csMu.Unlock()
	e, ok := d.iccSpaces[ref]
	return e, ok
}

// keepTabled keeps cs, the space of a grey or RGB profile built from
// lookup tables (object ref, if it is one), for whatever names the
// profile again: tabulating it is work, and the table is memory. One
// table too many for a document is not kept; its space is the device
// space of as many components, counted as "icc-budget".
func (d *Document) keepTabled(ref pdf.Ref, cs *colorSpace) (*colorSpace, string) {
	d.csMu.Lock()
	defer d.csMu.Unlock()
	e := csEntry{cs, ""}
	if d.iccTables >= maxICCTables {
		e = csEntry{d.deviceSpace(cs.n), "icc-budget"}
	} else {
		d.iccTables++
	}
	if ref != (pdf.Ref{}) {
		if d.iccSpaces == nil {
			d.iccSpaces = map[pdf.Ref]csEntry{}
		}
		d.iccSpaces[ref] = e
	}
	return e.cs, e.approx
}

// iccCMYK is an ICCBased space of four components, its profile in s
// (object ref, if it is one): through the profile, as the other renderers
// convert it, or where cera cannot read it through DeviceCMYK's, counted
// as "icc-lut". NaiveCMYK takes precedence. Spaces of one profile object
// are made once.
func (d *Document) iccCMYK(ref pdf.Ref, s *pdf.Stream) (*colorSpace, string) {
	if d.naiveCMYK {
		return spaceCMYKNaive, ""
	}
	cs, approx := d.profileCMYK(ref, func() []byte { return d.r.Decode(s).Data })
	if cs == nil {
		cs = d.cmykSpace()
	}
	return cs, approx
}

// profileCMYK returns the conversion through the CMYK profile data returns
// (of object ref, if it is one), or nil and what that is counted as:
// "icc-lut" for a profile cera cannot read, "icc-budget" for one profile
// too many. A profile object is loaded once, whatever names it.
func (d *Document) profileCMYK(ref pdf.Ref, data func() []byte) (*colorSpace, string) {
	if ref != (pdf.Ref{}) {
		d.csMu.Lock()
		e, ok := d.iccSpaces[ref]
		d.csMu.Unlock()
		if ok {
			return e.cs, e.approx
		}
	}
	e := csEntry{nil, "icc-lut"}
	if t, err := cmyk.Load(data()); err == nil {
		e = csEntry{&colorSpace{kind: csCMYK, n: 4, cmyk: t, icc: data}, ""}
	}
	d.csMu.Lock()
	defer d.csMu.Unlock()
	if e.cs != nil && !d.cmykTables[e.cs.cmyk] {
		t := e.cs.cmyk
		if len(d.cmykTables) >= maxCMYKProfiles {
			e = csEntry{nil, "icc-budget"}
		} else {
			if d.cmykTables == nil {
				d.cmykTables = map[*cmyk.Table]bool{}
			}
			d.cmykTables[t] = true
		}
	}
	if ref != (pdf.Ref{}) {
		if d.iccSpaces == nil {
			d.iccSpaces = map[pdf.Ref]csEntry{}
		}
		d.iccSpaces[ref] = e
	}
	return e.cs, e.approx
}

// maxIntents is how many output intents of a document are looked at.
const maxIntents = 32

// deviceCMYK is DeviceCMYK as the document converts it, and what that is
// counted as when it is not what the document asks for. In this order:
// naively (OpenOptions.NaiveCMYK); through the caller's profile
// (OpenOptions.CMYKProfile); through the profile of the document's output
// intent; through the bundled SWOP profile of internal/cmyk.
//
// The output intent (PDF 2.0, 14.11.5) names the printing condition the
// device colours of a PDF/X, PDF/A or PDF/E file are meant for. The first
// intent of one of these kinds with a CMYK profile decides; one whose
// profile is for RGB or grey says nothing about DeviceCMYK. It is read
// when DeviceCMYK is first used. A profile cera cannot read is counted as
// "icc-lut", one past maxCMYKProfiles as "icc-budget", as those of
// ICCBased spaces are, and DeviceCMYK then goes through the bundled
// profile.
func (d *Document) deviceCMYK() (*colorSpace, string) {
	switch {
	case d.devCMYK != nil:
		return d.devCMYK, d.devApprox
	case d.ignoreIntent || d.r == nil:
		return spaceCMYK, ""
	}
	d.intentOnce.Do(func() {
		defer func() { _ = recover() }()
		cat, err := d.r.Catalog()
		if err != nil {
			return
		}
		d.intent, d.intentApprox, _ = d.intentCMYK(cat.Get("OutputIntents"))
	})
	if d.intent != nil {
		return d.intent, ""
	}
	return spaceCMYK, d.intentApprox
}

// intentCMYK returns the conversion through the profile of the first
// output intent of a list that decides DeviceCMYK (see deviceCMYK), with
// ok true; nil and what that is counted as when cera cannot use the
// profile. ok is false when no intent of the list decides.
func (d *Document) intentCMYK(list pdf.Object) (cs *colorSpace, approx string, ok bool) {
	intents, _ := d.resolve(list).Array()
	for _, o := range intents[:min(len(intents), maxIntents)] {
		it := d.dict(o)
		switch s, _ := d.name(it.Get("S")); s {
		case "GTS_PDFX", "GTS_PDFA1", "ISO_PDFE1":
		default:
			continue
		}
		prof := it.Get("DestOutputProfile")
		s, ok := d.resolve(prof).Stream()
		if !ok {
			continue
		}
		data := d.r.Decode(s).Data
		// The colour space of the profile: of its header, or where that
		// does not read, by the number of its components.
		space := ""
		if len(data) >= 20 {
			space = string(data[16:20])
		}
		if n, _ := d.integer(s.Dict.Get("N")); space != "CMYK" && (space == "RGB " || space == "GRAY" || n != 4) {
			continue
		}
		ref, _ := prof.Ref()
		// Decoded again when asked for (Page.CMYKProfile), not kept.
		cs, approx = d.profileCMYK(ref, func() []byte { return d.r.Decode(s).Data })
		return cs, approx, true
	}
	return nil, "", false
}

// cmykDoc returns the document page p is drawn with: its own, or for a page
// with an output intent of its own (PDF 2.0, 14.11.5), which takes
// precedence over the document's on it, a view of the document that
// converts DeviceCMYK through the profile of that intent. Pages with one
// profile share a view. A profile cera cannot use is counted as the
// document's is, and DeviceCMYK then converted as the document converts
// it. The options that decide DeviceCMYK for the document (NaiveCMYK,
// CMYKProfile, IgnoreOutputIntent) decide it for its pages.
func (p *Page) cmykDoc() *Document {
	d := p.doc
	if d.devCMYK != nil || d.ignoreIntent || !p.dict.Has("OutputIntents") {
		return d
	}
	ref, _ := d.r.PageRef(p.index + 1)
	d.viewMu.Lock()
	v, ok := d.pageViews[ref]
	d.viewMu.Unlock()
	if ok {
		return v
	}
	v = d
	if cs, approx, ok := d.intentCMYK(p.dict.Get("OutputIntents")); ok {
		base, _ := d.deviceCMYK()
		if cs == nil {
			cs = base
		}
		if cs != base || approx != "" {
			v = d.withCMYK(cs, approx)
		}
	}
	d.viewMu.Lock()
	defer d.viewMu.Unlock()
	if d.pageViews == nil {
		d.pageViews = map[pdf.Ref]*Document{}
	}
	d.pageViews[ref] = v
	return v
}

// withCMYK returns the view of d that converts DeviceCMYK through cs,
// counted as approx.
func (d *Document) withCMYK(cs *colorSpace, approx string) *Document {
	return d.view(cs, approx, projRGB)
}

// gammas reads /Gamma: a number for CalGray, three for CalRGB.
func (d *Document) gammas(o pdf.Object, n int) []float64 {
	if n == 1 {
		if g, ok := d.num(o); ok {
			return []float64{g}
		}
		return nil
	}
	return d.floats(o)
}

// tintSpace reads a Separation or DeviceN space: its tints go through the
// tint transform into the alternate space.
func (d *Document) tintSpace(fam pdf.Name, a pdf.Array, res pdf.Dict, depth int) (*colorSpace, string) {
	if len(a) < 2 {
		return nil, ""
	}
	cs := &colorSpace{kind: csTint, n: 1}
	if fam == "DeviceN" {
		names, _ := d.resolve(a[1]).Array()
		cs.n = min(max(len(names), 1), maxComps)
		none := len(names) > 0
		for _, o := range names[:min(len(names), maxComps)] {
			n, _ := d.name(o)
			none = none && n == "None"
			cs.colorants = append(cs.colorants, string(n))
		}
		cs.none = none
	} else if n, _ := d.name(a[1]); n == "None" {
		cs.none = true
	} else {
		cs.colorants = []string{string(n)}
	}
	if len(a) >= 4 {
		alt, approx := d.colorSpace(a[2], res, depth+1)
		if alt != nil && alt.kind != csPattern && alt.kind != csIndexed {
			if fn := d.function(a[3], 0); fn != nil && fn.outputs() == alt.n {
				cs.base, cs.fn = alt, fn
				return cs, approx
			}
		}
	}
	return cs, "tint-transform"
}
