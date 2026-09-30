package cera

import (
	"context"
	"math"
	"time"

	"github.com/go-pdfkit/reader"
	"github.com/timzifer/stilus"
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

	// clips counts the device clips pushed while this state was current,
	// so that Q can pop them.
	clips int
}

// interp executes content streams against a Device.
type interp struct {
	doc      *Document
	dev      Device
	st       *Stats
	ctx      context.Context
	deadline time.Time
	err      error

	gs    gstate
	stack []gstate

	path       Path
	cur, start stilus.Point
	hasCur     bool
	clip       int8 // pending W/W*: -1 none, else the fill rule
	inText     bool

	paint Paint
}

func (in *interp) reset(doc *Document, dev Device, st *Stats, ctx context.Context, deadline time.Time) {
	if ctx == nil {
		ctx = context.Background()
	}
	*in = interp{
		doc: doc, dev: dev, st: st, ctx: ctx, deadline: deadline,
		stack: in.stack[:0], path: in.path, clip: -1,
	}
	in.path.Reset()
}

// release drops references to the document so pooled workers do not keep
// it alive.
func (in *interp) release() {
	for i := range in.stack {
		in.stack[i] = gstate{}
	}
	in.stack = in.stack[:0]
	in.doc, in.dev, in.st, in.ctx = nil, nil, nil, nil
	in.gs = gstate{}
}

func (in *interp) initState(ctm Matrix) {
	in.gs = gstate{
		ctm:    ctm,
		fillCS: spaceGray, strokeCS: spaceGray,
		fillAlpha: 1, strokeAlp: 1,
		style: StrokeStyle{Width: 1, MiterLimit: 10},
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
	sc := reader.NewContentScanner(data)
	for in.err == nil {
		op, ok := sc.Next()
		if !ok {
			break
		}
		in.st.Ops++
		if in.st.Ops%checkEvery == 0 && in.expired() {
			return
		}
		in.do(op, res, depth)
	}
	if sc.Err() != nil {
		in.st.Errors++
	}
}

func (in *interp) expired() bool {
	if in.ctx.Err() != nil || (!in.deadline.IsZero() && time.Now().After(in.deadline)) {
		in.err = ErrDeadline
		return true
	}
	return false
}

// nums reads n numeric operands into v; ok is false if the operator has
// the wrong operands.
func nums(ops []reader.Object, v []float64) bool {
	if len(ops) < len(v) {
		return false
	}
	ops = ops[len(ops)-len(v):]
	for i, o := range ops {
		f, ok := reader.ToFloat(o)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return false
		}
		v[i] = f
	}
	return true
}

func (in *interp) do(op reader.Operation, res reader.Dict, depth int) {
	var v [6]float64
	a := op.Operands
	bad := func() { in.st.Errors++ }
	switch op.Operator {
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
		if !nums(a, v[:6]) {
			bad()
			return
		}
		in.gs.ctm = Matrix(v).Mul(in.gs.ctm)
	case "w":
		if !nums(a, v[:1]) {
			bad()
			return
		}
		in.gs.style.Width = math.Abs(v[0])
	case "J":
		if !nums(a, v[:1]) {
			bad()
			return
		}
		in.gs.style.Cap = stilus.Cap(min(max(int(v[0]), 0), 2))
	case "j":
		if !nums(a, v[:1]) {
			bad()
			return
		}
		in.gs.style.Join = stilus.Join(min(max(int(v[0]), 0), 2))
	case "M":
		if !nums(a, v[:1]) {
			bad()
			return
		}
		in.gs.style.MiterLimit = v[0]
	case "d":
		if len(a) < 2 {
			bad()
			return
		}
		arr, _ := reader.ToArray(a[0])
		phase, _ := reader.ToFloat(a[1])
		in.setDash(arr, phase)
	case "gs":
		if len(a) < 1 {
			bad()
			return
		}
		n, _ := reader.ToName(a[0])
		in.extGState(in.doc.dict(in.doc.dict(res["ExtGState"])[n]))
	case "ri", "i":

	// Path construction.
	case "m":
		if !nums(a, v[:2]) {
			bad()
			return
		}
		in.moveTo(v[0], v[1])
	case "l":
		if !nums(a, v[:2]) {
			bad()
			return
		}
		in.lineTo(v[0], v[1])
	case "c":
		if !nums(a, v[:6]) {
			bad()
			return
		}
		in.curveTo(v[0], v[1], v[2], v[3], v[4], v[5])
	case "v":
		if !nums(a, v[:4]) {
			bad()
			return
		}
		in.curveTo(float64(in.cur.X), float64(in.cur.Y), v[0], v[1], v[2], v[3])
	case "y":
		if !nums(a, v[:4]) {
			bad()
			return
		}
		in.curveTo(v[0], v[1], v[2], v[3], v[2], v[3])
	case "h":
		if in.hasCur {
			in.path.Close()
			in.cur = in.start
		}
	case "re":
		if !nums(a, v[:4]) {
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
	case "CS", "cs":
		if len(a) < 1 {
			bad()
			return
		}
		cs, approx := in.doc.colorSpace(a[0], res, 0)
		if cs == nil {
			bad()
			return
		}
		if approx != "" {
			in.st.unsupported(approx)
		}
		if op.Operator == "CS" {
			in.gs.strokeCS = cs
			cs.initial(in.gs.stroke[:])
		} else {
			in.gs.fillCS = cs
			cs.initial(in.gs.fill[:])
		}
	case "SC", "SCN":
		in.setColor(in.gs.strokeCS, in.gs.stroke[:], a)
	case "sc", "scn":
		in.setColor(in.gs.fillCS, in.gs.fill[:], a)
	case "G", "g", "RG", "rg", "K", "k":
		cs := spaceGray
		switch op.Operator[0] {
		case 'R', 'r':
			cs = spaceRGB
		case 'K', 'k':
			cs = spaceCMYK
		}
		if !nums(a, v[:cs.n]) {
			bad()
			return
		}
		if op.Operator[0] >= 'a' {
			in.gs.fillCS = cs
			copy(in.gs.fill[:], v[:cs.n])
		} else {
			in.gs.strokeCS = cs
			copy(in.gs.stroke[:], v[:cs.n])
		}

	// External objects.
	case "Do":
		if len(a) < 1 {
			bad()
			return
		}
		n, _ := reader.ToName(a[0])
		in.xobject(n, res, depth)
	case "BI":
		in.st.unsupported("inline-image")
	case "sh":
		in.st.unsupported("shading")

	// Text (M4).
	case "BT":
		in.inText = true
	case "ET":
		in.inText = false
	case "Tj", "TJ", "'", "\"":
		in.st.unsupported("text")
	case "Tc", "Tw", "Tz", "TL", "Tf", "Tr", "Ts", "Td", "TD", "Tm", "T*", "d0", "d1":

	// Marked content and compatibility.
	case "BDC":
		if len(a) >= 1 {
			if n, _ := reader.ToName(a[0]); n == "OC" {
				in.st.unsupported("optional-content")
			}
		}
	case "BMC", "EMC", "MP", "DP", "BX", "EX":
	default:
		bad()
	}
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

func (in *interp) setColor(cs *colorSpace, dst []float64, a []reader.Object) {
	if cs.kind == csPattern {
		return // the pattern name is resolved when patterns land (M7)
	}
	var v [maxComps]float64
	if !nums(a, v[:cs.n]) {
		in.st.Errors++
		return
	}
	copy(dst, v[:cs.n])
}

func (in *interp) setDash(arr reader.Array, phase float64) {
	in.gs.style.Dash, in.gs.style.DashPhase = nil, 0
	if len(arr) == 0 {
		return
	}
	dash := make([]float64, 0, len(arr))
	var sum float64
	for _, o := range arr {
		f, ok := reader.ToFloat(o)
		if !ok || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
			return
		}
		dash = append(dash, f)
		sum += f
	}
	if sum == 0 {
		return
	}
	in.gs.style.Dash = dash
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
		in.setDash(arr, phase)
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

func (in *interp) xobject(name reader.Name, res reader.Dict, depth int) {
	doc := in.doc
	s, ok := reader.ToStream(doc.resolve(doc.dict(res["XObject"])[name]))
	if !ok {
		in.st.Errors++
		return
	}
	sub, _ := doc.name(s.Dict["Subtype"])
	switch sub {
	case "Image":
		in.st.unsupported("image")
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
