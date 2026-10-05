package cera

import (
	"sync"

	"github.com/go-pdfkit/reader"

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
	// cmyk is the profile a CMYK space converts through, nil for the naive
	// conversion.
	cmyk *cmyk.Table

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
		i := int(v[0] + 0.5)
		i = min(max(i, 0), cs.hival)
		var c [maxComps]float64
		n := cs.base.n
		for j := range n {
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
		return cs.base.rgb(c[:n])
	case csTint:
		return cs.tintRGB(v)
	}
	return 0, 0, 0
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
// reference are made once per document.
func (d *Document) colorSpace(o reader.Object, res reader.Dict, depth int) (cs *colorSpace, approx string) {
	if depth > 4 {
		return nil, ""
	}
	if n, ok := o.(reader.Name); ok {
		switch n {
		case "DeviceGray", "G", "CalGray":
			return spaceGray, ""
		case "DeviceRGB", "RGB", "CalRGB":
			return spaceRGB, ""
		case "DeviceCMYK", "CMYK":
			return d.cmykSpace(), ""
		case "Pattern":
			return spacePattern, ""
		}
		if o, ok := d.dict(res["ColorSpace"])[n]; ok {
			return d.colorSpace(o, res, depth+1)
		}
		return nil, ""
	}
	ref, isRef := o.(reader.Ref)
	if isRef {
		d.csMu.Lock()
		e, ok := d.spaces[ref]
		d.csMu.Unlock()
		if ok {
			return e.cs, e.approx
		}
	}
	cs, approx = d.makeColorSpace(d.resolve(o), res, depth)
	if isRef {
		d.csMu.Lock()
		if d.spaces == nil {
			d.spaces = map[reader.Ref]csEntry{}
		}
		d.spaces[ref] = csEntry{cs, approx}
		d.csMu.Unlock()
	}
	return cs, approx
}

func (d *Document) makeColorSpace(o reader.Object, res reader.Dict, depth int) (cs *colorSpace, approx string) {
	if n, ok := reader.ToName(o); ok {
		return d.colorSpace(n, res, depth+1)
	}
	a, ok := reader.ToArray(o)
	if !ok || len(a) == 0 {
		return nil, ""
	}
	fam, _ := d.name(a[0])
	param := func() reader.Dict {
		if len(a) < 2 {
			return nil
		}
		return d.dict(a[1])
	}
	switch fam {
	case "DeviceGray", "G":
		return spaceGray, ""
	case "DeviceRGB", "RGB":
		return spaceRGB, ""
	case "DeviceCMYK", "CMYK":
		return d.cmykSpace(), ""
	case "CalGray", "CalRGB":
		n := 1
		dev := spaceGray
		if fam == "CalRGB" {
			n, dev = 3, spaceRGB
		}
		p := param()
		s := calSpace(n, d.floats(p["WhitePoint"]), d.gammas(p["Gamma"], n), d.floats(p["Matrix"]))
		if s == nil {
			return dev, ""
		}
		return &colorSpace{kind: csCIE, n: n, cie: s}, ""
	case "Lab":
		p := param()
		s := labSpace(d.floats(p["WhitePoint"]), d.floats(p["Range"]))
		return &colorSpace{kind: csCIE, n: 3, cie: s}, ""
	case "ICCBased":
		if len(a) < 2 {
			return nil, ""
		}
		s, ok := reader.ToStream(d.resolve(a[1]))
		if !ok {
			return nil, ""
		}
		n, _ := d.integer(s.Dict["N"])
		if n == 4 {
			ref, _ := a[1].(reader.Ref)
			return d.iccCMYK(ref, s)
		}
		if prof, srgb := iccProfile(d.r.DecodeStreamRecovering(s).Data, n); prof != nil {
			if srgb {
				return d.deviceSpace(prof.n), ""
			}
			return &colorSpace{kind: csCIE, n: prof.n, cie: prof}, ""
		}
		switch n {
		case 1:
			return spaceGray, ""
		case 3:
			return spaceRGB, ""
		}
		if alt, ok := s.Dict["Alternate"]; ok {
			return d.colorSpace(alt, res, depth+1)
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
		switch v := d.resolve(a[3]).(type) {
		case reader.String:
			lookup = v
		case *reader.Stream:
			lookup = d.r.DecodeStreamRecovering(v).Data
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

// iccCMYK is an ICCBased space of four components, its profile in s
// (object ref, if it is one): through the profile, as the other renderers
// convert it, or where cera cannot read it through DeviceCMYK's, counted
// as "icc-lut". NaiveCMYK takes precedence. Spaces of one profile object
// are made once.
func (d *Document) iccCMYK(ref reader.Ref, s *reader.Stream) (*colorSpace, string) {
	if d.naiveCMYK {
		return spaceCMYKNaive, ""
	}
	if ref != (reader.Ref{}) {
		d.csMu.Lock()
		e, ok := d.iccSpaces[ref]
		d.csMu.Unlock()
		if ok {
			return e.cs, e.approx
		}
	}
	e := csEntry{d.cmykSpace(), "icc-lut"}
	if t, err := cmyk.Load(d.r.DecodeStreamRecovering(s).Data); err == nil {
		e = csEntry{&colorSpace{kind: csCMYK, n: 4, cmyk: t}, ""}
	}
	d.csMu.Lock()
	defer d.csMu.Unlock()
	if t := e.cs.cmyk; e.approx == "" && !d.cmykTables[t] {
		if len(d.cmykTables) >= maxCMYKProfiles {
			e = csEntry{d.cmykSpace(), "icc-budget"}
		} else {
			if d.cmykTables == nil {
				d.cmykTables = map[*cmyk.Table]bool{}
			}
			d.cmykTables[t] = true
		}
	}
	if ref != (reader.Ref{}) {
		if d.iccSpaces == nil {
			d.iccSpaces = map[reader.Ref]csEntry{}
		}
		d.iccSpaces[ref] = e
	}
	return e.cs, e.approx
}

// gammas reads /Gamma: a number for CalGray, three for CalRGB.
func (d *Document) gammas(o reader.Object, n int) []float64 {
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
func (d *Document) tintSpace(fam reader.Name, a reader.Array, res reader.Dict, depth int) (*colorSpace, string) {
	if len(a) < 2 {
		return nil, ""
	}
	cs := &colorSpace{kind: csTint, n: 1}
	if fam == "DeviceN" {
		names, _ := reader.ToArray(d.resolve(a[1]))
		cs.n = min(max(len(names), 1), maxComps)
		none := len(names) > 0
		for _, o := range names {
			n, _ := d.name(o)
			none = none && n == "None"
		}
		cs.none = none
	} else if n, _ := d.name(a[1]); n == "None" {
		cs.none = true
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
