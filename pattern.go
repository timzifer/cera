package cera

import (
	"image/color"

	"github.com/go-pdfkit/reader"

	"github.com/timzifer/cera/internal/content"
)

// Patterns (PDF 2.0, 8.7). A shading pattern paints its shading through
// the shape it fills: a path, a stroke or the glyphs of a text operator
// arrive at the device as a clip of that shape, the shading filled over
// it, and the clip's pop. Tiling patterns are not drawn yet (counted as
// "pattern").

// patternEntry is a pattern as read, once per document when it is named by
// reference.
type patternEntry struct {
	shading *shadingEntry // nil for a tiling pattern
	matrix  Matrix
	tiling  bool
}

// patternPaint is the pattern of a fill or stroke colour: the pattern and
// its space mapped to device space.
type patternPaint struct {
	e *patternEntry
	m Matrix
}

func (d *Document) pattern(o reader.Object, res reader.Dict) *patternEntry {
	ref, isRef := o.(reader.Ref)
	if isRef {
		d.shMu.Lock()
		e := d.patterns[ref]
		d.shMu.Unlock()
		if e != nil {
			return e
		}
	}
	e := d.readPattern(d.resolve(o), res)
	if e != nil && isRef {
		d.shMu.Lock()
		if d.patterns == nil {
			d.patterns = map[reader.Ref]*patternEntry{}
		}
		d.patterns[ref] = e
		d.shMu.Unlock()
	}
	return e
}

func (d *Document) readPattern(o reader.Object, res reader.Dict) *patternEntry {
	dict, ok := reader.ToDict(o)
	if s, isStream := reader.ToStream(o); isStream {
		dict, ok = s.Dict, true
	}
	if !ok {
		return nil
	}
	e := &patternEntry{matrix: identity}
	if m := d.floats(dict["Matrix"]); len(m) == 6 {
		e.matrix = Matrix(m)
	}
	switch t, _ := d.integer(dict["PatternType"]); t {
	case 1:
		e.tiling = true
	case 2:
		sub := d.dict(dict["Resources"])
		if sub == nil {
			sub = res
		}
		sh, ok := dict["Shading"]
		if !ok {
			return nil
		}
		e.shading = d.shading(sh, sub)
	default:
		return nil
	}
	return e
}

// setPattern sets the pattern named by the last operand as the colour of
// dst, in the space of the content stream running.
func (in *interp) setPattern(sc *content.Scanner, res reader.Dict, dst *patternPaint) {
	*dst = patternPaint{}
	o := in.lookupRef(res, "Pattern", sc, sc.Last())
	if o == nil {
		in.st.Errors++
		return
	}
	e := in.doc.pattern(o, res)
	if e == nil {
		in.st.Errors++
		return
	}
	*dst = patternPaint{e: e, m: e.matrix.Mul(in.base)}
}

// patternShading prepares in.paint with alpha for painting pattern p and
// returns its shading, nil if nothing is painted.
func (in *interp) patternShading(p *patternPaint, alpha float64) *Shading {
	e := p.e
	if e == nil {
		return nil
	}
	if e.tiling {
		in.st.unsupported("pattern")
		return nil
	}
	if e.shading.feature != "" {
		in.st.unsupported(e.shading.feature)
	}
	sh := e.shading.full
	if sh == nil {
		if e.shading.feature == "" {
			in.st.Errors++
		}
		return nil
	}
	in.paint = Paint{Color: color.RGBA{A: unit8(alpha)}}
	if in.paint.Color.A == 0 {
		return nil
	}
	in.st.Shadings++
	return sh
}

// shadingOp runs sh: the named shading painted over the clip.
func (in *interp) shadingOp(sc *content.Scanner, res reader.Dict) {
	o := in.lookupRef(res, "Shading", sc, sc.Last())
	if o == nil {
		in.st.Errors++
		return
	}
	e := in.doc.shading(o, res)
	if e.feature != "" {
		in.st.unsupported(e.feature)
	}
	if e.plain == nil {
		if e.feature == "" {
			in.st.Errors++
		}
		return
	}
	a := unit8(in.gs.fillAlpha)
	if a == 0 {
		return
	}
	in.paint = Paint{Color: color.RGBA{A: a}}
	in.st.Shadings++
	if in.transparent() {
		r, m := Rect{-1 << 20, -1 << 20, 1 << 20, 1 << 20}, identity
		if e.plain.HasBBox {
			r, m = e.plain.BBox, in.gs.ctm
		}
		in.beginObject(r, m)
		defer in.dev.EndGroup()
	}
	in.dev.FillShading(e.plain, in.gs.ctm, &in.paint)
}
