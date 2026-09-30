package cera

import "github.com/go-pdfkit/reader"

type csKind uint8

const (
	csGray csKind = iota
	csRGB
	csCMYK
	csLab
	csIndexed
	csTint // Separation and DeviceN, approximated until tint transforms (M7)
	csPattern
)

// colorSpace is a resolved PDF colour space.
type colorSpace struct {
	kind   csKind
	n      int         // components of a colour value
	base   *colorSpace // Indexed
	hival  int         // Indexed
	lookup []byte      // Indexed
}

var (
	spaceGray    = &colorSpace{kind: csGray, n: 1}
	spaceRGB     = &colorSpace{kind: csRGB, n: 3}
	spaceCMYK    = &colorSpace{kind: csCMYK, n: 4}
	spacePattern = &colorSpace{kind: csPattern, n: 0}
)

// maxComps bounds the components of a colour value (DeviceN allows 32).
const maxComps = 32

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
		k := 1 - clamp01(v[3])
		return (1 - clamp01(v[0])) * k, (1 - clamp01(v[1])) * k, (1 - clamp01(v[2])) * k
	case csLab:
		l := clamp01(v[0] / 100)
		return l, l, l
	case csIndexed:
		i := int(v[0] + 0.5)
		i = min(max(i, 0), cs.hival)
		var c [4]float64
		n := cs.base.n
		for j := range n {
			if k := i*n + j; k < len(cs.lookup) {
				c[j] = float64(cs.lookup[k]) / 255
			}
		}
		return cs.base.rgb(c[:n])
	case csTint:
		t := 0.0
		for _, x := range v[:cs.n] {
			t = max(t, clamp01(x))
		}
		return 1 - t, 1 - t, 1 - t
	}
	return 0, 0, 0
}

// colorSpace resolves a colour space operand or resource entry. approx names
// a feature that is only approximated, or is empty.
func (d *Document) colorSpace(o reader.Object, res reader.Dict, depth int) (cs *colorSpace, approx string) {
	if depth > 4 {
		return nil, ""
	}
	o = d.resolve(o)
	if n, ok := reader.ToName(o); ok {
		switch n {
		case "DeviceGray", "G", "CalGray":
			return spaceGray, ""
		case "DeviceRGB", "RGB", "CalRGB":
			return spaceRGB, ""
		case "DeviceCMYK", "CMYK":
			return spaceCMYK, ""
		case "Pattern":
			return spacePattern, ""
		}
		if o, ok := d.dict(res["ColorSpace"])[n]; ok {
			return d.colorSpace(o, res, depth+1)
		}
		return nil, ""
	}
	a, ok := reader.ToArray(o)
	if !ok || len(a) == 0 {
		return nil, ""
	}
	fam, _ := d.name(a[0])
	switch fam {
	case "DeviceGray", "CalGray", "G":
		return spaceGray, ""
	case "DeviceRGB", "CalRGB", "RGB":
		return spaceRGB, ""
	case "DeviceCMYK", "CMYK":
		return spaceCMYK, ""
	case "Lab":
		return &colorSpace{kind: csLab, n: 3}, "lab"
	case "ICCBased":
		if len(a) < 2 {
			return nil, ""
		}
		s, ok := reader.ToStream(d.resolve(a[1]))
		if !ok {
			return nil, ""
		}
		if n, ok := d.num(s.Dict["N"]); ok {
			switch int(n) {
			case 1:
				return spaceGray, ""
			case 3:
				return spaceRGB, ""
			case 4:
				return spaceCMYK, ""
			}
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
	case "Separation":
		return &colorSpace{kind: csTint, n: 1}, "tint-transform"
	case "DeviceN":
		if len(a) < 2 {
			return nil, ""
		}
		names, _ := reader.ToArray(d.resolve(a[1]))
		return &colorSpace{kind: csTint, n: min(max(len(names), 1), maxComps)}, "tint-transform"
	case "Pattern":
		return spacePattern, ""
	}
	return nil, ""
}
