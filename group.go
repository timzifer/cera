package cera

import (
	"image/color"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/stilus"
)

// The interpreter's side of transparency (PDF 2.0, 11). A form XObject with
// a transparency group is drawn between BeginGroup and EndGroup, composited
// with the blend mode, soft mask and fill alpha of the state that paints it
// and with those reset inside. An object painted in a state with a blend
// mode or a soft mask becomes a group of its own: fill, stroke, the glyphs
// of one text-showing operator, an image. A soft mask is drawn before each
// group it masks, from its form, in the state (and CTM) in which the
// ExtGState naming it was set.

// softMask is the soft mask of a graphics state.
type softMask struct {
	form *pdf.Stream
	res  pdf.Dict // resources where the ExtGState was set
	ctm  Matrix
	sm   SoftMask
}

var blendModes = map[pdf.Name]BlendMode{
	"Normal": BlendNormal, "Compatible": BlendNormal,
	"Multiply": BlendMultiply, "Screen": BlendScreen, "Overlay": BlendOverlay,
	"Darken": BlendDarken, "Lighten": BlendLighten,
	"ColorDodge": BlendColorDodge, "ColorBurn": BlendColorBurn,
	"HardLight": BlendHardLight, "SoftLight": BlendSoftLight,
	"Difference": BlendDifference, "Exclusion": BlendExclusion,
	"Hue": BlendHue, "Saturation": BlendSaturation, "Color": BlendColor, "Luminosity": BlendLuminosity,
}

// blendMode reads /BM: a name, or an array of names of which the first
// known one counts.
func (d *Document) blendMode(o pdf.Object) (BlendMode, bool) {
	o = d.resolve(o)
	if n, ok := o.Name(); ok {
		bm, ok := blendModes[n]
		return bm, ok
	}
	if a, ok := o.Array(); ok {
		for _, e := range a {
			if n, ok := d.name(e); ok {
				if bm, ok := blendModes[n]; ok {
					return bm, true
				}
			}
		}
	}
	return BlendNormal, false
}

// softMask reads the /SMask entry of an ExtGState set in the current
// state; nil for /None and for what cannot be read.
func (in *interp) softMask(o pdf.Object, res pdf.Dict) *softMask {
	doc := in.doc
	o = doc.resolve(o)
	if n, ok := o.Name(); ok {
		if n != "None" {
			in.st.Errors++
		}
		return nil
	}
	d, ok := o.Dict()
	if !ok {
		in.st.Errors++
		return nil
	}
	s := doc.stream(d.Get("G"))
	kind, _ := doc.name(d.Get("S"))
	if s == nil || (kind != "Luminosity" && kind != "Alpha") {
		in.st.Errors++
		return nil
	}
	m := &softMask{form: s, res: res, ctm: in.gs.ctm, sm: SoftMask{Luminosity: kind == "Luminosity"}}
	m.sm.Backdrop = color.RGBA{A: 255}
	if m.sm.Luminosity {
		if bc := doc.floats(d.Get("BC")); len(bc) > 0 {
			cs := spaceGray
			if g := doc.dict(s.Dict.Get("Group")); !g.IsZero() {
				// Depth 1: Default spaces do not remap a group's
				// blending space.
				if c, _ := doc.colorSpace(g.Get("CS"), res, 1); c != nil {
					cs = c
				}
			}
			if cs.kind != csPattern && len(bc) >= cs.n {
				r, g, b := cs.rgb(bc)
				m.sm.Backdrop = premul(r, g, b, 1)
			}
		}
	}
	if tr, ok := d.Lookup("TR"); ok {
		if n, ok := doc.name(tr); !ok || n != "Identity" {
			if m.sm.Transfer = lut256(doc.function(tr, 0)); m.sm.Transfer == nil {
				in.st.unsupported("smask-transfer")
			}
		}
	}
	return m
}

// objState is what drawing a soft mask must not disturb of the object it
// masks: the current path and the text object.
type objState struct {
	path       Path
	cur, start stilus.Point
	hasCur     bool
	clip       int8
	text       textObject
}

func (in *interp) pushObject() {
	n := len(in.objs)
	if n == cap(in.objs) {
		in.objs = append(in.objs, objState{})
	} else {
		in.objs = in.objs[:n+1]
	}
	o := &in.objs[n]
	o.path, in.path = in.path, o.path
	in.path.Reset()
	cur := in.text
	in.text = o.text.keep()
	o.text = cur
	o.cur, o.start, o.hasCur, o.clip = in.cur, in.start, in.hasCur, in.clip
	in.hasCur, in.clip = false, -1
}

func (in *interp) popObject() {
	n := len(in.objs) - 1
	o := &in.objs[n]
	in.path, o.path = o.path, in.path
	spare := in.text
	in.text = o.text
	o.text = spare.keep()
	in.cur, in.start, in.hasCur, in.clip = o.cur, o.start, o.hasCur, o.clip
	in.objs = in.objs[:n]
}

// drawSoftMask draws the soft mask of the current state for an object or
// group bounded by r under m.
func (in *interp) drawSoftMask(r Rect, m Matrix) {
	sm := in.gs.smask
	in.st.Groups++
	in.dev.BeginMask(r, m, &sm.sm)
	depth := in.depth + 1
	in.masks++
	switch {
	case depth >= maxFormDepth || len(in.stack) >= maxStateDepth:
		in.st.unsupported("nesting-budget")
	case in.maskDepth >= maxMaskDepth || in.masks > maxMasks:
		// Masks whose forms draw masked objects multiply.
		in.st.unsupported("smask-budget")
	default:
		in.maskDepth++
		in.pushObject()
		td, mcd := in.td, in.mcd
		in.td, in.mcd = nil, nil // mask content is not text of the page
		in.stack = append(in.stack, in.gs)
		base := len(in.stack)
		in.initState(sm.ctm)
		in.runForm(sm.form, sm.res, depth)
		in.unwind(base)
		in.restore()
		in.td, in.mcd = td, mcd
		in.popObject()
		in.maskDepth--
	}
	in.dev.EndMask()
}

// transparent reports whether objects painted now need a group.
func (in *interp) transparent() bool {
	return in.gs.blend != BlendNormal || in.gs.smask != nil
}

// shapeAlpha counts alpha that is shape (AIS true) inside a knockout
// group, where cera draws it as opacity: outside knockout groups shape and
// opacity multiply, and the two read the same (PDF 2.0 11.3.7), so AIS
// changes nothing there (ADR 0009).
func (in *interp) shapeAlpha() {
	if in.gs.ais && in.knockouts > 0 {
		in.st.unsupported("alpha-is-shape")
	}
}

// beginObject starts the group of an object bounded by r under m,
// composited with blend mode bm; a knockout group holds the glyphs of a
// run (see textKnockout). It keeps in.paint.
func (in *interp) beginObject(r Rect, m Matrix, bm BlendMode, knockout bool) {
	paint := in.paint
	g := Group{Isolated: true, Knockout: knockout, Blend: bm, Alpha: 255}
	if in.gs.smask != nil {
		in.drawSoftMask(r, m)
		g.Masked = true
	}
	in.st.Groups++
	in.dev.BeginGroup(r, m, &g)
	in.paint = paint
}

// pathBox returns the user-space box of the current path, grown by pad.
func (in *interp) pathBox(pad float64) Rect {
	b := in.path.Bounds()
	return Rect{b.X0 - pad, b.Y0 - pad, b.X1 + pad, b.Y1 + pad}
}

// strokePad is how far a stroke in the current state reaches beyond its
// path in user space; thin strokes are widened by the device box.
func (in *interp) strokePad() float64 {
	st := &in.gs.style
	return st.Width / 2 * max(st.MiterLimit, 1.5)
}

// runForm runs the content of form s in the current state: its matrix,
// its BBox as a clip, a new path. The caller saves and restores the state.
func (in *interp) runForm(s *pdf.Stream, parent pdf.Dict, depth int) {
	in.formSpace(s)
	in.formContent(s, parent, depth)
}

// formContent runs the content of form s with a new path.
func (in *interp) formContent(s *pdf.Stream, parent pdf.Dict, depth int) {
	doc := in.doc
	res := doc.dict(s.Dict.Get("Resources"))
	if res.IsZero() {
		res = parent
	}
	dec := doc.r.Decode(s)
	if dec.Recovered {
		in.st.Errors++
	}
	in.content(dec.Data, res, depth)
}

// content runs a content stream with a new path, in the current state,
// which is the space of the patterns it names.
func (in *interp) content(data []byte, res pdf.Dict, depth int) {
	in.path.Reset()
	in.hasCur, in.clip = false, -1
	base := in.base
	in.base = in.gs.ctm
	in.exec(data, res, depth)
	in.base = base
	in.path.Reset()
	in.hasCur, in.clip = false, -1
}

// formSpace applies the /Matrix of form s to the CTM and clips to its
// /BBox, returning the box (with ok false if it has none).
func (in *interp) formSpace(s *pdf.Stream) (bbox Rect, ok bool) {
	doc := in.doc
	if a, isArr := doc.resolve(s.Dict.Get("Matrix")).Array(); isArr && len(a) == 6 {
		var m Matrix
		valid := true
		for i, o := range a {
			var ok bool
			m[i], ok = doc.num(o)
			valid = valid && ok
		}
		if valid {
			in.gs.ctm = m.Mul(in.gs.ctm)
		}
	}
	if bbox, ok = doc.rect(s.Dict.Get("BBox")); ok {
		in.dev.ClipRect(bbox, in.gs.ctm)
		in.gs.clips++
	}
	return bbox, ok
}
