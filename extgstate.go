package cera

import (
	"github.com/go-pdfkit/reader"
)

// The parts of the graphics state ADR 0007 adds: the font set through gs,
// overprint, text knockout and transfer functions. The device-dependent
// parameters (BG, BG2, UCR, UCR2, HT, FL, SM, SA) describe the output
// device, not the page, and are ignored on purpose, as is the rendering
// intent (ADR 0003).

// extGStateMore reads the entries of ExtGState d (the object ref, when it
// is indirect) that extGState leaves to this file.
func (in *interp) extGStateMore(d reader.Dict, ref reader.Ref) {
	doc := in.doc
	if a, ok := reader.ToArray(doc.resolve(d["Font"])); ok {
		// Like Tf: a font reference and a size.
		var f *Font
		size, sized := 0.0, false
		if len(a) == 2 {
			f = doc.font(a[0])
			size, sized = doc.num(a[1])
		}
		if f == nil || !sized {
			in.st.Errors++
		} else {
			in.gs.text.font, in.gs.text.size = f, size
		}
	}
	// OP sets both kinds of overprint unless op is there for fills.
	if o, ok := d["OP"]; ok {
		in.gs.opStroke = doc.boolean(o)
		if _, ok := d["op"]; !ok {
			in.gs.opFill = in.gs.opStroke
		}
	}
	if o, ok := d["op"]; ok {
		in.gs.opFill = doc.boolean(o)
	}
	if v, ok := doc.num(d["OPM"]); ok {
		in.gs.opm1 = v == 1
	}
	if o, ok := d["TK"]; ok {
		in.gs.text.noKnockout = !doc.boolean(o)
	}
	// TR2 takes precedence over TR.
	tr, ok := d["TR2"]
	if !ok {
		tr, ok = d["TR"]
	}
	if ok {
		t, read := doc.transfer(tr, ref)
		if !read {
			in.st.unsupported("transfer") // drawn without
		}
		in.gs.transfer = t
	}
}

// overprints reports whether an object painted in cs with overprint on is
// composited as a simulated overprint (RenderOptions.SimulateOverprint):
// only DeviceCMYK under OPM 1, which leaves the backdrop's components
// where the object's are zero, and Separation and DeviceN spaces, which
// leave the colourants they do not name, differ from painting over. Where
// it is not simulated, it is counted as "overprint".
func (in *interp) overprints(cs *colorSpace, on bool) bool {
	if !on || cs.none {
		return false
	}
	switch cs.kind {
	case csTint:
	case csCMYK:
		if !in.gs.opm1 {
			return false
		}
	default:
		return false
	}
	if !in.overprint {
		in.st.unsupported("overprint") // painted over
		return false
	}
	return true
}

// objectBlend returns the blend mode an object painted in cs (with
// overprint op of its kind) is composited with, and whether it needs a
// group of its own. cera composites in RGB, so a simulated overprint is
// approximated as Multiply with the backdrop.
func (in *interp) objectBlend(cs *colorSpace, op bool) (BlendMode, bool) {
	bm := in.gs.blend
	if bm == BlendNormal && in.overprints(cs, op) {
		bm = BlendMultiply
	}
	return bm, bm != BlendNormal || in.gs.smask != nil
}

// untransferred counts an object whose colours do not pass through the
// transfer function of the state: images, shadings and patterns.
func (in *interp) untransferred() {
	if in.gs.transfer != nil {
		in.st.unsupported("transfer")
	}
}

// transfer is a transfer function (TR, TR2) tabulated per RGB component.
// cera composites in RGB, so the functions apply after colour conversion.
type transfer [3][256]uint8

// apply maps the straight components r, g, b in [0, 1].
func (t *transfer) apply(r, g, b float64) (float64, float64, float64) {
	f := func(i int, v float64) float64 {
		return float64(t[i][int(clamp01(v)*255+0.5)]) / 255
	}
	return f(0, r), f(1, g), f(2, b)
}

// transfer reads the transfer function o of the ExtGState ref (zero for a
// direct one): nil for the identity, which is also what an unreadable one
// is drawn as (read false). A function is applied to every component; an
// array of four applies its first three to red, green and blue. Tables
// are made once per ExtGState object.
func (d *Document) transfer(o reader.Object, ref reader.Ref) (t *transfer, read bool) {
	if n, ok := d.name(o); ok {
		return nil, n == "Identity" || n == "Default"
	}
	if ref != (reader.Ref{}) {
		d.trMu.Lock()
		e, ok := d.transfers[ref]
		d.trMu.Unlock()
		if ok {
			return e.t, e.read
		}
	}
	t, read = d.readTransfer(o)
	if ref != (reader.Ref{}) {
		d.trMu.Lock()
		if d.transfers == nil {
			d.transfers = map[reader.Ref]trEntry{}
		}
		d.transfers[ref] = trEntry{t, read}
		d.trMu.Unlock()
	}
	return t, read
}

type trEntry struct {
	t    *transfer
	read bool
}

func (d *Document) readTransfer(o reader.Object) (*transfer, bool) {
	var fs [3]function
	if a, ok := reader.ToArray(d.resolve(o)); ok {
		if len(a) != 4 {
			return nil, false
		}
		identity := true
		for i := range fs {
			if n, ok := d.name(a[i]); ok && n == "Identity" {
				continue
			}
			if fs[i] = d.oneFunction(a[i], 0); fs[i] == nil {
				return nil, false
			}
			identity = false
		}
		if identity {
			return nil, true
		}
	} else {
		f := d.oneFunction(o, 0)
		if f == nil {
			return nil, false
		}
		fs = [3]function{f, f, f}
	}
	t := new(transfer)
	for i, f := range fs {
		if f == nil {
			for j := range t[i] {
				t[i][j] = uint8(j)
			}
			continue
		}
		lut := lut256(f)
		if lut == nil {
			return nil, false
		}
		t[i] = *lut
	}
	return t, true
}

// Text knockout (TK) is applied per text-showing operator, not per text
// object: glyphs of one Tj or TJ that overlap knock each other out.

// textKnockout reports whether the run of the current text-showing
// operator, in render mode mode, is drawn as a knockout group of its
// glyphs: TK is on, the glyphs are filled with an opacity below 1, and
// two of them overlap. Elsewhere TK makes no difference, since cera
// composites the glyphs of a run as separate objects. Stroked runs and
// runs filled with a pattern that it would change are counted as
// "text-knockout" and drawn without.
func (in *interp) textKnockout(mode TextMode) bool {
	ts := &in.gs.text
	if ts.noKnockout || mode == TextInvisible || mode == TextClip {
		return false
	}
	fill := mode == TextFill || mode == TextFillClip
	alpha := in.gs.fillAlpha
	if !fill {
		alpha = min(alpha, in.gs.strokeAlp)
	}
	if alpha >= 1 || unit8(alpha) == 0 || !in.glyphsOverlap() {
		return false
	}
	if !fill || in.gs.fillCS.kind == csPattern {
		in.st.unsupported("text-knockout")
		return false
	}
	return true
}

// glyphsOverlap reports whether the boxes of two glyphs of the run
// overlap.
func (in *interp) glyphsOverlap() bool {
	gs := in.text.run.Glyphs
	if len(gs) > maxKnockoutGlyphs {
		return true
	}
	boxes := in.text.boxes[:0]
	defer func() { in.text.boxes = boxes[:0] }()
	for i := range gs {
		o := gs[i].Outline
		if o == nil || len(o.Points) == 0 {
			continue
		}
		pb := o.Bounds()
		b := rectUnder(Rect{float64(pb.X0), float64(pb.Y0), float64(pb.X1), float64(pb.Y1)}, gs[i].M)
		for _, c := range boxes {
			if b.X0 < c.X1 && c.X0 < b.X1 && b.Y0 < c.Y1 && c.Y0 < b.Y1 {
				return true
			}
		}
		boxes = append(boxes, b)
	}
	return false
}
