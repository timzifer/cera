package cera

import (
	"bytes"
	"image/color"
	"math"

	"github.com/go-pdfkit/reader"
	"github.com/timzifer/stilus"

	"github.com/timzifer/cera/internal/content"
)

// Interpreter limits (spec, "Robustheit und Sicherheit").
const (
	maxFormDepth  = 12
	maxStateDepth = 1 << 12
	checkEvery    = 256 // operators between deadline checks
)

// gstate is the part of the PDF graphics state the interpreter implements.
type gstate struct {
	ctm Matrix

	fillCS, strokeCS     *colorSpace
	fill, stroke         [maxComps]float64
	fillAlpha, strokeAlp float64

	style StrokeStyle

	text textState
	// uncolored is set inside a Type 3 glyph declared with d1: its colour
	// operators are ignored, it paints in the colour that showed it.
	uncolored bool

	// clips counts the device clips pushed while this state was current,
	// so that Q can pop them.
	clips int
}

// interp executes content streams against a Device.
type interp struct {
	doc *Document
	dev Device
	st  *Stats
	lim *limit
	err error

	gs    gstate
	stack []gstate

	path       Path
	cur, start stilus.Point
	hasCur     bool
	clip       int8 // pending W/W*: -1 none, else the fill rule

	text textObject
	td   TextDevice // dev, if it wants the text

	paint Paint

	scanners []*content.Scanner // one per form depth
	dashes   []float64          // dash patterns of this page, append-only
	dashBuf  []float64
}

func (in *interp) reset(doc *Document, dev Device, st *Stats, lim *limit) {
	*in = interp{
		doc: doc, dev: dev, st: st, lim: lim,
		stack: in.stack[:0], path: in.path, clip: -1,
		scanners: in.scanners, dashes: in.dashes[:0], dashBuf: in.dashBuf,
		text: in.text.keep(),
	}
	in.path.Reset()
	in.td, _ = dev.(TextDevice)
}

// release drops references to the document so pooled workers do not keep
// it alive.
func (in *interp) release() {
	clear(in.stack[:cap(in.stack)]) // popped states hold fonts too
	in.stack = in.stack[:0]
	in.doc, in.dev, in.st, in.lim, in.td = nil, nil, nil, nil, nil
	in.gs = gstate{}
	in.text = in.text.keep()
}

func (in *interp) initState(ctm Matrix) {
	in.gs = gstate{
		ctm:    ctm,
		fillCS: spaceGray, strokeCS: spaceGray,
		fillAlpha: 1, strokeAlp: 1,
		style: StrokeStyle{Width: 1, MiterLimit: 10},
		text:  textState{scale: 1},
	}
}

// run interprets a page's content stream under ctm and unwinds the state
// stack afterwards.
func (in *interp) run(data []byte, res reader.Dict, ctm Matrix, depth int) {
	in.initState(ctm)
	in.exec(data, res, depth)
	in.unwind(0)
	in.popClips(in.gs.clips)
	in.gs.clips = 0
}

// exec interprets one content stream in the current state.
func (in *interp) exec(data []byte, res reader.Dict, depth int) {
	for len(in.scanners) <= depth {
		in.scanners = append(in.scanners, new(content.Scanner))
	}
	sc := in.scanners[depth]
	sc.Reset(data)
	for in.err == nil {
		op, ok := sc.Next()
		if !ok {
			break
		}
		in.st.Ops++
		if in.st.Ops%checkEvery == 0 && in.expired() {
			break
		}
		in.do(sc, op, res, depth)
	}
	in.st.Errors += sc.Errors()
	sc.Reset(nil)
}

func (in *interp) expired() bool {
	if in.lim.expired() {
		in.err = ErrDeadline
		return true
	}
	return false
}

// lookup returns the resolved entry of the resource category cat named by
// the operand name.
func (in *interp) lookup(res reader.Dict, cat reader.Name, sc *content.Scanner, name *content.Operand) reader.Object {
	if name == nil || name.Kind != content.Name {
		return nil
	}
	sub := in.doc.dict(res[cat])
	if sub == nil {
		return nil
	}
	return in.doc.resolve(sub[reader.Name(sc.Text(name))])
}

// lookupRef is lookup without resolving the entry, so that the reference
// can serve as a cache key.
func (in *interp) lookupRef(res reader.Dict, cat reader.Name, sc *content.Scanner, name *content.Operand) reader.Object {
	if name == nil || name.Kind != content.Name {
		return nil
	}
	return in.doc.dict(res[cat])[reader.Name(sc.Text(name))]
}

func (in *interp) do(sc *content.Scanner, op []byte, res reader.Dict, depth int) {
	var v [6]float64
	bad := func() { in.st.Errors++ }
	switch string(op) {
	// Graphics state.
	case "q":
		if len(in.stack) >= maxStateDepth {
			bad()
			return
		}
		in.stack = append(in.stack, in.gs)
		in.gs.clips = 0
	case "Q":
		in.restore()
	case "cm":
		if !sc.Nums(v[:6]) {
			bad()
			return
		}
		in.gs.ctm = Matrix(v).Mul(in.gs.ctm)
	case "w":
		if !sc.Nums(v[:1]) {
			bad()
			return
		}
		in.gs.style.Width = math.Abs(v[0])
	case "J":
		if !sc.Nums(v[:1]) {
			bad()
			return
		}
		in.gs.style.Cap = stilus.Cap(min(max(int(v[0]), 0), 2))
	case "j":
		if !sc.Nums(v[:1]) {
			bad()
			return
		}
		in.gs.style.Join = stilus.Join(min(max(int(v[0]), 0), 2))
	case "M":
		if !sc.Nums(v[:1]) {
			bad()
			return
		}
		in.gs.style.MiterLimit = v[0]
	case "d":
		arr, phase := sc.FromEnd(1), sc.FromEnd(0)
		if arr == nil || arr.Kind != content.Array || phase.Kind != content.Number {
			bad()
			return
		}
		in.dashBuf = in.dashBuf[:0]
		ok := true
		sc.Elems(arr, func(e *content.Operand) bool {
			ok = e.Kind == content.Number
			in.dashBuf = append(in.dashBuf, e.Num)
			return ok
		})
		if !ok {
			in.dashBuf = in.dashBuf[:0]
		}
		in.setDash(in.dashBuf, phase.Num)
	case "gs":
		d, _ := reader.ToDict(in.lookup(res, "ExtGState", sc, sc.Last()))
		in.extGState(d)
	case "ri", "i":

	// Path construction.
	case "m":
		if !sc.Nums(v[:2]) {
			bad()
			return
		}
		in.moveTo(v[0], v[1])
	case "l":
		if !sc.Nums(v[:2]) {
			bad()
			return
		}
		in.lineTo(v[0], v[1])
	case "c":
		if !sc.Nums(v[:6]) {
			bad()
			return
		}
		in.curveTo(v[0], v[1], v[2], v[3], v[4], v[5])
	case "v":
		if !sc.Nums(v[:4]) {
			bad()
			return
		}
		in.curveTo(float64(in.cur.X), float64(in.cur.Y), v[0], v[1], v[2], v[3])
	case "y":
		if !sc.Nums(v[:4]) {
			bad()
			return
		}
		in.curveTo(v[0], v[1], v[2], v[3], v[2], v[3])
	case "h":
		in.closePath()
	case "re":
		if !sc.Nums(v[:4]) {
			bad()
			return
		}
		x, y, w, h := float32(v[0]), float32(v[1]), float32(v[2]), float32(v[3])
		in.path.MoveTo(x, y)
		in.path.LineTo(x+w, y)
		in.path.LineTo(x+w, y+h)
		in.path.LineTo(x, y+h)
		in.path.Close()
		in.cur, in.start, in.hasCur = stilus.Point{X: x, Y: y}, stilus.Point{X: x, Y: y}, true

	// Path painting.
	case "S":
		in.strokePath()
		in.endPath()
	case "s":
		in.closePath()
		in.strokePath()
		in.endPath()
	case "f", "F":
		in.fillPath(NonZero)
		in.endPath()
	case "f*":
		in.fillPath(EvenOdd)
		in.endPath()
	case "B":
		in.fillPath(NonZero)
		in.strokePath()
		in.endPath()
	case "B*":
		in.fillPath(EvenOdd)
		in.strokePath()
		in.endPath()
	case "b":
		in.closePath()
		in.fillPath(NonZero)
		in.strokePath()
		in.endPath()
	case "b*":
		in.closePath()
		in.fillPath(EvenOdd)
		in.strokePath()
		in.endPath()
	case "n":
		in.endPath()
	case "W":
		in.clip = int8(NonZero)
	case "W*":
		in.clip = int8(EvenOdd)

	// Colour.
	case "CS", "cs", "SC", "SCN", "sc", "scn", "G", "g", "RG", "rg", "K", "k":
		if !in.gs.uncolored {
			in.color(sc, op, res)
		}

	// External objects.
	case "Do":
		in.xobject(in.lookupRef(res, "XObject", sc, sc.Last()), res, depth)
	case "BI":
		in.inlineImage(sc, res)
	case "sh":
		in.st.unsupported("shading")

	// Text.
	case "BT", "ET", "Tc", "Tw", "Tz", "TL", "Tf", "Tr", "Ts", "Td", "TD", "Tm", "T*",
		"Tj", "TJ", "'", "\"", "d0", "d1":
		in.textOp(sc, op, res, depth)

	// Marked content and compatibility.
	case "BDC":
		if sc.Len() >= 1 && sc.NameIs(sc.Arg(0), "OC") {
			in.st.unsupported("optional-content")
		}
	case "BMC", "EMC", "MP", "DP", "BX", "EX":
	default:
		bad()
	}
}

// color runs the colour operators.
func (in *interp) color(sc *content.Scanner, op []byte, res reader.Dict) {
	var v [4]float64
	switch string(op) {
	case "CS", "cs":
		cs := in.colorSpaceOperand(sc, res)
		if cs == nil {
			in.st.Errors++
			return
		}
		if op[0] == 'C' {
			in.gs.strokeCS = cs
			cs.initial(in.gs.stroke[:])
		} else {
			in.gs.fillCS = cs
			cs.initial(in.gs.fill[:])
		}
	case "SC", "SCN":
		in.setColor(sc, in.gs.strokeCS, in.gs.stroke[:])
	case "sc", "scn":
		in.setColor(sc, in.gs.fillCS, in.gs.fill[:])
	default: // G g RG rg K k
		cs := spaceGray
		switch op[0] {
		case 'R', 'r':
			cs = spaceRGB
		case 'K', 'k':
			cs = spaceCMYK
		}
		if !sc.Nums(v[:cs.n]) {
			in.st.Errors++
			return
		}
		if op[0] >= 'a' {
			in.gs.fillCS = cs
			copy(in.gs.fill[:], v[:cs.n])
		} else {
			in.gs.strokeCS = cs
			copy(in.gs.stroke[:], v[:cs.n])
		}
	}
}

// colorSpaceOperand resolves the colour space named by the last operand.
// The device spaces are recognised without touching the resources.
func (in *interp) colorSpaceOperand(sc *content.Scanner, res reader.Dict) *colorSpace {
	o := sc.Last()
	if o == nil || o.Kind != content.Name {
		return nil
	}
	switch string(sc.Text(o)) {
	case "DeviceGray", "G", "CalGray":
		return spaceGray
	case "DeviceRGB", "RGB", "CalRGB":
		return spaceRGB
	case "DeviceCMYK", "CMYK":
		return spaceCMYK
	case "Pattern":
		return spacePattern
	}
	cs, approx := in.doc.colorSpace(reader.Name(sc.Text(o)), res, 0)
	if approx != "" {
		in.st.unsupported(approx)
	}
	return cs
}

func (in *interp) restore() {
	if len(in.stack) == 0 {
		in.st.Errors++
		return
	}
	in.popClips(in.gs.clips)
	in.gs = in.stack[len(in.stack)-1]
	in.stack = in.stack[:len(in.stack)-1]
}

// unwind restores states down to stack depth n.
func (in *interp) unwind(n int) {
	for len(in.stack) > n {
		in.restore()
	}
}

func (in *interp) popClips(n int) {
	for range n {
		in.dev.PopClip()
	}
}

func (in *interp) moveTo(x, y float64) {
	p := stilus.Point{X: float32(x), Y: float32(y)}
	in.path.MoveTo(p.X, p.Y)
	in.cur, in.start, in.hasCur = p, p, true
}

func (in *interp) lineTo(x, y float64) {
	if !in.hasCur {
		in.moveTo(x, y)
		return
	}
	in.cur = stilus.Point{X: float32(x), Y: float32(y)}
	in.path.LineTo(in.cur.X, in.cur.Y)
}

func (in *interp) curveTo(x1, y1, x2, y2, x3, y3 float64) {
	if !in.hasCur {
		in.moveTo(x1, y1)
	}
	in.cur = stilus.Point{X: float32(x3), Y: float32(y3)}
	in.path.CubicTo(float32(x1), float32(y1), float32(x2), float32(y2), in.cur.X, in.cur.Y)
}

func (in *interp) closePath() {
	if in.hasCur {
		in.path.Close()
		in.cur = in.start
	}
}

// endPath applies a pending clip and starts a new path.
func (in *interp) endPath() {
	if in.clip >= 0 {
		in.dev.ClipPath(&in.path, in.gs.ctm, FillRule(in.clip))
		in.gs.clips++
		in.st.Clips++
		in.clip = -1
	}
	in.path.Reset()
	in.hasCur = false
}

func (in *interp) fillPath(rule FillRule) {
	if in.path.Empty() || !in.setPaint(in.gs.fillCS, in.gs.fill[:], in.gs.fillAlpha) {
		return
	}
	in.st.Fills++
	in.dev.FillPath(&in.path, in.gs.ctm, rule, &in.paint)
}

func (in *interp) strokePath() {
	if in.path.Empty() || !in.setPaint(in.gs.strokeCS, in.gs.stroke[:], in.gs.strokeAlp) {
		return
	}
	in.st.Strokes++
	in.dev.StrokePath(&in.path, in.gs.ctm, &in.gs.style, &in.paint)
}

// setPaint prepares in.paint; false means nothing is painted.
func (in *interp) setPaint(cs *colorSpace, v []float64, alpha float64) bool {
	if cs.kind == csPattern {
		in.st.unsupported("pattern")
		return false
	}
	r, g, b := cs.rgb(v)
	in.paint = Paint{Color: premul(r, g, b, alpha)}
	return in.paint.Color.A != 0
}

func (in *interp) setColor(sc *content.Scanner, cs *colorSpace, dst []float64) {
	if cs.kind == csPattern {
		return // the pattern name is resolved when patterns land (M7)
	}
	var v [maxComps]float64
	if !sc.Nums(v[:cs.n]) {
		in.st.Errors++
		return
	}
	copy(dst, v[:cs.n])
}

// setDash sets the dash pattern from arr, which the caller may reuse.
func (in *interp) setDash(arr []float64, phase float64) {
	in.gs.style.Dash, in.gs.style.DashPhase = nil, 0
	var sum float64
	for _, f := range arr {
		if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
			return
		}
		sum += f
	}
	if !(sum > 0) || math.IsInf(sum, 0) {
		return
	}
	// Saved states share dash patterns, so the arena (reset per page) is
	// only ever appended to.
	n := len(in.dashes)
	in.dashes = append(in.dashes, arr...)
	in.gs.style.Dash = in.dashes[n:len(in.dashes):len(in.dashes)]
	if !math.IsNaN(phase) && !math.IsInf(phase, 0) {
		in.gs.style.DashPhase = phase
	}
}

func (in *interp) extGState(d reader.Dict) {
	if d == nil {
		in.st.Errors++
		return
	}
	doc := in.doc
	if v, ok := doc.num(d["LW"]); ok {
		in.gs.style.Width = math.Abs(v)
	}
	if v, ok := doc.num(d["LC"]); ok {
		in.gs.style.Cap = stilus.Cap(min(max(int(v), 0), 2))
	}
	if v, ok := doc.num(d["LJ"]); ok {
		in.gs.style.Join = stilus.Join(min(max(int(v), 0), 2))
	}
	if v, ok := doc.num(d["ML"]); ok {
		in.gs.style.MiterLimit = v
	}
	if a, ok := reader.ToArray(doc.resolve(d["D"])); ok && len(a) == 2 {
		arr, _ := reader.ToArray(doc.resolve(a[0]))
		phase, _ := doc.num(a[1])
		in.dashBuf = in.dashBuf[:0]
		for _, o := range arr {
			f, ok := doc.num(o)
			if !ok {
				in.dashBuf = in.dashBuf[:0]
				break
			}
			in.dashBuf = append(in.dashBuf, f)
		}
		in.setDash(in.dashBuf, phase)
	}
	if v, ok := doc.num(d["CA"]); ok {
		in.gs.strokeAlp = clamp01(v)
	}
	if v, ok := doc.num(d["ca"]); ok {
		in.gs.fillAlpha = clamp01(v)
	}
	if n, ok := doc.name(d["BM"]); ok && n != "Normal" && n != "Compatible" {
		in.st.unsupported("blend-mode")
	} else if a, ok := reader.ToArray(doc.resolve(d["BM"])); ok && len(a) > 0 {
		if n, _ := doc.name(a[0]); n != "Normal" && n != "Compatible" {
			in.st.unsupported("blend-mode")
		}
	}
	if sm, ok := d["SMask"]; ok {
		if n, ok := doc.name(sm); !ok || n != "None" {
			in.st.unsupported("soft-mask")
		}
	}
}

func (in *interp) xobject(o reader.Object, res reader.Dict, depth int) {
	doc := in.doc
	ref, _ := o.(reader.Ref)
	s, ok := reader.ToStream(doc.resolve(o))
	if !ok {
		in.st.Errors++
		return
	}
	sub, _ := doc.name(s.Dict["Subtype"])
	switch sub {
	case "Image":
		r := doc.image(ref, s, res)
		in.drawImage(&r)
	case "Form":
		in.form(s, res, depth)
	case "PS":
	default:
		in.st.Errors++
	}
}

// form runs a form XObject (PDF 2.0, 8.10) inside q … Q.
func (in *interp) form(s *reader.Stream, parent reader.Dict, depth int) {
	if depth >= maxFormDepth || len(in.stack) >= maxStateDepth {
		in.st.Errors++
		return
	}
	doc := in.doc
	if g := doc.dict(s.Dict["Group"]); g != nil {
		if n, _ := doc.name(g["S"]); n == "Transparency" {
			in.st.unsupported("transparency-group")
		}
	}
	res := doc.dict(s.Dict["Resources"])
	if res == nil {
		res = parent
	}
	dec := doc.r.DecodeStreamRecovering(s)
	if dec.Recovered {
		in.st.Errors++
	}

	in.stack = append(in.stack, in.gs)
	in.gs.clips = 0
	base := len(in.stack)
	if a, ok := reader.ToArray(doc.resolve(s.Dict["Matrix"])); ok && len(a) == 6 {
		var m Matrix
		valid := true
		for i, o := range a {
			m[i], ok = doc.num(o)
			valid = valid && ok
		}
		if valid {
			in.gs.ctm = m.Mul(in.gs.ctm)
		}
	}
	if bbox, ok := doc.rect(s.Dict["BBox"]); ok {
		in.dev.ClipRect(bbox, in.gs.ctm)
		in.gs.clips++
	}
	// A form starts with a new path and ends with its own states unwound.
	in.path.Reset()
	in.hasCur, in.clip = false, -1
	in.exec(dec.Data, res, depth+1)
	in.unwind(base)
	in.restore()
	in.path.Reset()
	in.hasCur, in.clip = false, -1
}

// drawImage paints a decoded image in the unit square of user space: a
// stencil in the fill colour, other images with the fill alpha.
func (in *interp) drawImage(r *imageResult) {
	if r.approx != "" {
		in.st.unsupported(r.approx)
	}
	if r.recovered {
		in.st.Errors++
	}
	img := r.img
	if img == nil {
		if r.unsupported != "" {
			in.st.unsupported(r.unsupported)
		} else {
			in.st.Errors++
		}
		return
	}
	if img.Stencil {
		if !in.setPaint(in.gs.fillCS, in.gs.fill[:], in.gs.fillAlpha) {
			return
		}
	} else {
		a := unit8(in.gs.fillAlpha)
		if a == 0 {
			return
		}
		in.paint = Paint{Color: color.RGBA{A: a}}
	}
	in.st.Images++
	in.dev.DrawImage(img, in.gs.ctm, &in.paint)
}

// inlineImage draws the inline image of a BI operation. Inline images
// are small and not cached.
func (in *interp) inlineImage(sc *content.Scanner, res reader.Dict) {
	op, data := sc.Image()
	if op == nil {
		in.st.Errors++
		return
	}
	d, _ := operandObject(sc, op).(reader.Dict)
	d = (&reader.InlineImage{Dict: d}).Expanded()
	r := in.doc.decodeImage(d, data, res)
	in.drawImage(&r)
}

// operandObject converts an operand to a reader object (for inline image
// dictionaries; it allocates).
func operandObject(sc *content.Scanner, o *content.Operand) reader.Object {
	switch o.Kind {
	case content.Number:
		if o.Int && math.Abs(o.Num) < 1<<53 {
			return reader.Integer(int64(o.Num))
		}
		return reader.Real(o.Num)
	case content.Name:
		return reader.Name(sc.Text(o))
	case content.String, content.HexString:
		return reader.String(bytes.Clone(sc.Text(o)))
	case content.Bool:
		return reader.Bool(o.Num != 0)
	case content.Array:
		var a reader.Array
		sc.Elems(o, func(e *content.Operand) bool {
			a = append(a, operandObject(sc, e))
			return true
		})
		return a
	case content.Dict:
		d := reader.Dict{}
		var key *content.Operand
		sc.Elems(o, func(e *content.Operand) bool {
			if key == nil {
				key = e
				return true
			}
			if key.Kind == content.Name {
				k := reader.Name(sc.Text(key)) // before Text is called again
				d[k] = operandObject(sc, e)
			}
			key = nil
			return true
		})
		return d
	}
	return reader.Null{}
}
