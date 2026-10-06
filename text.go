package cera

import (
	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/stilus"

	"github.com/timzifer/cera/internal/content"
)

// textState is the part of the graphics state that text operators set
// (PDF 2.0, 9.3).
type textState struct {
	font      *Font
	size      float64
	charSpace float64
	wordSpace float64
	scale     float64 // Tz / 100
	leading   float64
	rise      float64
	mode      TextMode
	// noKnockout is TK false (see textKnockout).
	noKnockout bool
}

// textObject is what lives between BT and ET, outside the graphics state,
// and the buffers of the text operators.
type textObject struct {
	tm, tlm Matrix
	// clip collects the outlines of glyphs shown in a clipping render
	// mode, in device space; ET intersects the clip with it.
	clip     Path
	clipping bool

	run  GlyphRun
	user []Matrix // em → user space, per glyph of run
	path Path     // stroked text

	// one is a glyph of run drawn on its own, boxes the glyph boxes
	// compared for text knockout.
	one   GlyphRun
	boxes []Rect

	// t3 are the Type 3 fonts whose glyphs are running, innermost last.
	t3 []*Font
}

// keep returns an empty text object that reuses t's buffers and holds no
// references to a document.
func (t textObject) keep() textObject {
	clear(t.run.Glyphs[:cap(t.run.Glyphs)])
	clear(t.t3[:cap(t.t3)])
	t.clip.Reset()
	t.path.Reset()
	return textObject{
		clip: t.clip, path: t.path,
		run:  GlyphRun{Glyphs: t.run.Glyphs[:0]},
		user: t.user[:0], t3: t.t3[:0], boxes: t.boxes[:0],
	}
}

var identity = stilus.Identity

// textOp runs the text operators.
func (in *interp) textOp(sc *content.Scanner, op []byte, res pdf.Dict, depth int) {
	var v [6]float64
	ts := &in.gs.text
	tx := &in.text
	num := func(dst *float64) {
		if !sc.Nums(v[:1]) {
			in.st.Errors++
			return
		}
		*dst = v[0]
	}
	switch string(op) {
	case "BT":
		tx.tm, tx.tlm = identity, identity
		if len(tx.t3) == 0 {
			tx.clip.Reset()
			tx.clipping = false
		}
	case "ET":
		if tx.clipping && len(tx.t3) == 0 {
			in.dev.ClipPath(&tx.clip, identity, NonZero)
			in.gs.clips++
			in.st.Clips++
			tx.clip.Reset()
			tx.clipping = false
		}
	case "Tc":
		num(&ts.charSpace)
	case "Tw":
		num(&ts.wordSpace)
	case "TL":
		num(&ts.leading)
	case "Ts":
		num(&ts.rise)
	case "Tz":
		if sc.Nums(v[:1]) {
			ts.scale = v[0] / 100
		} else {
			in.st.Errors++
		}
	case "Tr":
		if sc.Nums(v[:1]) && v[0] >= 0 && v[0] <= 7 {
			ts.mode = TextMode(v[0])
		} else {
			in.st.Errors++
		}
	case "Tf":
		name := sc.FromEnd(1)
		if !sc.Nums(v[:1]) || name == nil || name.Kind != content.Name {
			in.st.Errors++
			return
		}
		ts.size = v[0]
		ts.font = nil
		if o := in.doc.dict(res.Get("Font")).Get(pdf.Name(sc.Text(name))); !o.IsNull() {
			ts.font = in.doc.font(o)
		}
		if ts.font == nil {
			in.st.Errors++
		}
	case "Td", "TD":
		if !sc.Nums(v[:2]) {
			in.st.Errors++
			return
		}
		if op[1] == 'D' {
			ts.leading = -v[1]
		}
		in.nextLine(v[0], v[1])
	case "Tm":
		if !sc.Nums(v[:6]) {
			in.st.Errors++
			return
		}
		tx.tm, tx.tlm = Matrix(v), Matrix(v)
	case "T*":
		in.nextLine(0, -ts.leading)
	case "Tj":
		in.showOperand(sc, sc.Last(), res, depth)
		in.flushText()
	case "'":
		in.nextLine(0, -ts.leading)
		in.showOperand(sc, sc.Last(), res, depth)
		in.flushText()
	case "\"":
		aw, ac := sc.FromEnd(2), sc.FromEnd(1)
		if aw == nil || aw.Kind != content.Number || ac.Kind != content.Number {
			in.st.Errors++
			return
		}
		ts.wordSpace, ts.charSpace = aw.Num, ac.Num
		in.nextLine(0, -ts.leading)
		in.showOperand(sc, sc.Last(), res, depth)
		in.flushText()
	case "TJ":
		arr := sc.Last()
		if arr == nil || arr.Kind != content.Array {
			in.st.Errors++
			return
		}
		sc.Elems(arr, func(e *content.Operand) bool {
			switch e.Kind {
			case content.Number:
				// Thousandths of the size, backwards (or up, vertically).
				d := -e.Num / 1000 * ts.size
				if f := ts.font; f != nil && f.vertical {
					tx.tm = stilus.Translate(0, d).Mul(tx.tm)
				} else {
					tx.tm = stilus.Translate(d*ts.scale, 0).Mul(tx.tm)
				}
			case content.String, content.HexString:
				in.show(sc.Text(e), res, depth)
			}
			return in.err == nil
		})
		in.flushText()
	case "d1":
		if len(tx.t3) > 0 {
			in.gs.uncolored = true
		}
	case "d0":
	}
}

// nextLine moves to the start of the next line, offset by (x, y).
func (in *interp) nextLine(x, y float64) {
	in.text.tlm = stilus.Translate(x, y).Mul(in.text.tlm)
	in.text.tm = in.text.tlm
}

func (in *interp) showOperand(sc *content.Scanner, o *content.Operand, res pdf.Dict, depth int) {
	if o == nil || (o.Kind != content.String && o.Kind != content.HexString) {
		in.st.Errors++
		return
	}
	in.show(sc.Text(o), res, depth)
}

// show adds the glyphs of one string to the run and moves the pen past
// them. Type 3 glyphs run at once.
func (in *interp) show(s []byte, res pdf.Dict, depth int) {
	ts := &in.gs.text
	tx := &in.text
	f := ts.font
	if f == nil {
		in.st.Errors++
		return
	}
	if f.Type3() {
		in.showType3(f, s, res, depth)
		return
	}
	if f.bad {
		in.st.unsupported("font-bad")
	}
	if f.program == nil {
		in.st.unsupported(f.missingKey())
	}
	if f.cmapMissing {
		in.st.unsupported("cmap-missing")
	}
	// Glyphs are needed unless nothing is drawn, clipped or extracted.
	need := ts.mode != TextInvisible || in.textDev() != nil
	em := Matrix{ts.size * ts.scale, 0, 0, ts.size, 0, ts.rise}
	if f.vertical {
		em[0] = ts.size
	}
	composite := f.composite()
	for i := 0; i < len(s); {
		// The code, its length, and the CID that selects widths and
		// glyphs (the code itself for a simple font).
		code, n, cid := int(s[i]), 1, int(s[i])
		if composite {
			if f.cmap.IsIdentity() && len(s)-i < 2 {
				break // an odd byte left over
			}
			c, k := f.cmap.Next(s[i:])
			code, n, cid = int(c), k, f.cmap.CID(c, k)
		}
		i += n
		w := f.advance(cid)
		var w1, vx, vy float64
		if f.vertical {
			w1, vx, vy = f.verticalMetrics(cid, w)
		}
		if need {
			gid, outline := f.glyph(cid)
			mu := em.Mul(tx.tm)
			g := Glyph{Code: code, GID: int(gid), Outline: outline, Advance: w}
			if f.vertical {
				// The glyph's horizontal origin is the position vector
				// away from the pen (PDF 2.0 9.7.4.3).
				mu = stilus.Translate(-vx, -vy).Mul(mu)
				g.Advance, g.Origin = -w1, [2]float64{vx, vy}
			}
			g.M = mu.Mul(in.gs.ctm)
			tx.run.Glyphs = append(tx.run.Glyphs, g)
			tx.user = append(tx.user, mu)
		}
		spacing := ts.charSpace
		if n == 1 && code == ' ' {
			spacing += ts.wordSpace
		}
		if f.vertical {
			tx.tm = stilus.Translate(0, w1*ts.size+spacing).Mul(tx.tm)
		} else {
			tx.tm = stilus.Translate((w*ts.size+spacing)*ts.scale, 0).Mul(tx.tm)
		}
	}
}

// flushText draws the run collected by a text-showing operator in the
// current render mode.
func (in *interp) flushText() {
	tx := &in.text
	run := &tx.run
	if len(run.Glyphs) == 0 {
		return
	}
	defer func() {
		clear(run.Glyphs)
		run.Glyphs = run.Glyphs[:0]
		tx.user = tx.user[:0]
	}()
	ts := &in.gs.text
	run.Font = ts.font
	run.Vertical = ts.font.vertical
	if td := in.textDev(); td != nil {
		td.ShowText(run, ts.mode)
	}
	mode := ts.mode
	cs, op := in.gs.fillCS, in.gs.opFill
	if mode == TextStroke || mode == TextStrokeClip {
		cs, op = in.gs.strokeCS, in.gs.opStroke
	}
	knockout := in.textKnockout(mode)
	if mode != TextInvisible && mode != TextClip {
		if bm, grouped := in.objectBlend(cs, op); grouped || knockout {
			in.beginObject(in.runBox(), identity, bm, knockout)
			defer in.dev.EndGroup()
		}
	}
	if mode == TextFill || mode == TextFillStroke || mode == TextFillClip || mode == TextFillStrokeClip {
		if in.gs.fillCS.kind == csPattern {
			// The glyphs as a clip, in device space.
			if in.patternReady(&in.gs.fillPat, in.gs.fillAlpha) {
				in.st.Glyphs += len(run.Glyphs)
				in.tmp.Reset()
				for i := range run.Glyphs {
					if o := run.Glyphs[i].Outline; o != nil {
						appendTransformed(&in.tmp, o, run.Glyphs[i].M)
					}
				}
				in.dev.ClipPath(&in.tmp, identity, NonZero)
				in.paintPattern(false, deviceBox(&in.tmp, identity, 1))
				in.dev.PopClip()
			}
		} else if in.setPaint(in.gs.fillCS, in.gs.fill[:], in.gs.fillAlpha) {
			in.st.Glyphs += len(run.Glyphs)
			if knockout {
				// Each glyph an object of the knockout group.
				one := &tx.one
				one.Font, one.Vertical = run.Font, run.Vertical
				for i := range run.Glyphs {
					one.Glyphs = run.Glyphs[i : i+1 : i+1]
					in.dev.FillGlyphs(one, &in.paint)
				}
				*one = GlyphRun{}
			} else {
				in.dev.FillGlyphs(run, &in.paint)
			}
		}
	}
	if mode == TextStroke || mode == TextFillStroke || mode == TextStrokeClip || mode == TextFillStrokeClip {
		tx.path.Reset()
		for i := range run.Glyphs {
			if o := run.Glyphs[i].Outline; o != nil {
				appendTransformed(&tx.path, o, tx.user[i])
			}
		}
		switch {
		case tx.path.Empty():
		case in.gs.strokeCS.kind == csPattern:
			if in.patternReady(&in.gs.strokePat, in.gs.strokeAlp) {
				in.st.Strokes++
				in.dev.ClipStroke(&tx.path, in.gs.ctm, &in.gs.style)
				in.paintPattern(true, strokeBox(&tx.path, in.gs.ctm, &in.gs.style))
				in.dev.PopClip()
			}
		case in.setPaint(in.gs.strokeCS, in.gs.stroke[:], in.gs.strokeAlp):
			in.st.Strokes++
			in.dev.StrokePath(&tx.path, in.gs.ctm, &in.gs.style, &in.paint)
		}
	}
	if mode >= TextFillClip && len(tx.t3) == 0 {
		tx.clipping = true
		for i := range run.Glyphs {
			if o := run.Glyphs[i].Outline; o != nil {
				appendTransformed(&tx.clip, o, run.Glyphs[i].M)
			}
		}
	}
	run.Font = nil
}

// runBox returns the device box of the glyphs of the run, stroked or not.
func (in *interp) runBox() Rect {
	tx := &in.text
	pad := 1.0
	if m := in.gs.text.mode; m == TextStroke || m == TextFillStroke || m == TextStrokeClip || m == TextFillStrokeClip {
		pad += in.strokePad() * sigmaMax(in.gs.ctm)
	}
	var b Rect
	first := true
	for i := range tx.run.Glyphs {
		g := &tx.run.Glyphs[i]
		if g.Outline == nil {
			continue
		}
		r := deviceBox(g.Outline, g.M, pad)
		gb := Rect{float64(r.Min.X), float64(r.Min.Y), float64(r.Max.X), float64(r.Max.Y)}
		if first {
			b, first = gb, false
			continue
		}
		b = Rect{min(b.X0, gb.X0), min(b.Y0, gb.Y0), max(b.X1, gb.X1), max(b.Y1, gb.Y1)}
	}
	return b
}

// appendTransformed appends p transformed by m to dst.
func appendTransformed(dst, p *Path, m Matrix) {
	pts := p.Points
	k := 0
	pt := func() (float32, float32) {
		x, y := m.Apply(float64(pts[k].X), float64(pts[k].Y))
		k++
		return float32(x), float32(y)
	}
	for _, vb := range p.Verbs {
		switch vb {
		case stilus.MoveTo:
			dst.MoveTo(pt())
		case stilus.LineTo:
			dst.LineTo(pt())
		case stilus.QuadTo:
			cx, cy := pt()
			x, y := pt()
			dst.QuadTo(cx, cy, x, y)
		case stilus.CubicTo:
			c1x, c1y := pt()
			c2x, c2y := pt()
			x, y := pt()
			dst.CubicTo(c1x, c1y, c2x, c2y, x, y)
		case stilus.Close:
			dst.Close()
		}
	}
}

// showType3 runs the glyph procedures of a Type 3 font for s.
func (in *interp) showType3(f *Font, s []byte, res pdf.Dict, depth int) {
	ts := &in.gs.text
	tx := &in.text
	fm := Matrix(f.pdf.FontMatrix())
	em := Matrix{ts.size * ts.scale, 0, 0, ts.size, 0, ts.rise}
	draw := ts.mode != TextInvisible && ts.mode != TextClip
	// Glyphs of a Type 3 glyph inherit its mode but clip only at the top.
	clip := ts.mode >= TextFillClip && len(tx.t3) == 0
	if clip {
		tx.clipping = true
	}
	for _, b := range s {
		code := int(b)
		mt := em.Mul(tx.tm)
		if td := in.textDev(); td != nil {
			tx.run.Font = f
			tx.run.Glyphs = append(tx.run.Glyphs[:0], Glyph{Code: code, GID: code, M: mt.Mul(in.gs.ctm), Advance: f.pdf.Width(code)})
			td.ShowText(&tx.run, ts.mode)
			tx.run.Glyphs = tx.run.Glyphs[:0]
			tx.run.Font = nil
		}
		if draw {
			in.type3Glyph(f, code, fm.Mul(mt), res, depth, false)
			if in.err != nil {
				return
			}
		}
		if clip {
			in.type3Clip(f, code, fm.Mul(mt), res, depth)
			if in.err != nil {
				return
			}
		}
		spacing := ts.charSpace
		if code == ' ' {
			spacing += ts.wordSpace
		}
		tx.tm = stilus.Translate((f.pdf.Width(code)*ts.size+spacing)*ts.scale, 0).Mul(tx.tm)
	}
}

// type3Glyph runs the glyph procedure of code with glyph space mapped to
// user space by mu, like a form XObject. For a clip (see type3Clip) the
// text it shows is filled, whatever the mode that showed the glyph.
func (in *interp) type3Glyph(f *Font, code int, mu Matrix, parent pdf.Dict, depth int, clip bool) {
	tx := &in.text
	if depth >= maxFormDepth || len(in.stack) >= maxStateDepth {
		in.st.unsupported("nesting-budget")
		return
	}
	for _, g := range tx.t3 {
		if g == f {
			in.st.Errors++ // a glyph that shows its own font
			return
		}
	}
	data := in.charProc(f, code)
	if data == nil {
		return
	}
	res := f.pdf.Type3Resources()
	if res.IsZero() {
		res = parent
	}
	in.st.Glyphs++
	in.stack = append(in.stack, in.gs)
	in.gs.clips = 0
	base := len(in.stack)
	in.gs.ctm = mu.Mul(in.gs.ctm)
	if clip {
		in.gs.text.mode = TextFill
	}
	tm, tlm := tx.tm, tx.tlm
	tx.t3 = append(tx.t3, f)
	in.path.Reset()
	in.hasCur, in.clip = false, -1
	in.exec(data, res, depth+1)
	in.unwind(base)
	in.restore()
	tx.t3[len(tx.t3)-1] = nil
	tx.t3 = tx.t3[:len(tx.t3)-1]
	tx.tm, tx.tlm = tm, tlm
	in.path.Reset()
	in.hasCur, in.clip = false, -1
}

// charProc returns the decoded glyph procedure of code, nil if there is
// none. Procedures are decoded once per font.
func (in *interp) charProc(f *Font, code int) []byte {
	name, ok := f.pdf.GlyphName(code)
	if !ok {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if data, ok := f.procs[name]; ok {
		return data
	}
	var data []byte
	if s, ok := (in.doc.resolve(f.pdf.CharProcs().Get(pdf.Name(name)))).Stream(); ok {
		dec := in.doc.r.Decode(s)
		if dec.Recovered {
			in.st.Errors++
		}
		data = dec.Data
		if data == nil {
			data = []byte{}
		}
	}
	if f.procs == nil {
		f.procs = map[string][]byte{}
	}
	f.procs[name] = data
	return data
}
