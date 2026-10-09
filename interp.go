package cera

import (
	"bytes"
	"image"
	"image/color"
	"math"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/stilus"

	"github.com/timzifer/cera/internal/content"
)

// checkEvery is the number of operators between deadline checks.
const checkEvery = 256

// gstate is the part of the PDF graphics state the interpreter implements.
type gstate struct {
	ctm Matrix

	fillCS, strokeCS     *colorSpace
	fill, stroke         [maxComps]float64
	fillPat, strokePat   patternPaint // in a Pattern colour space
	fillAlpha, strokeAlp float64
	// blend and smask composite what is painted (see group.go); ais says
	// alpha and soft mask are shape rather than opacity (AIS), which only
	// a knockout group tells apart.
	blend BlendMode
	smask *softMask
	ais   bool
	// Overprint of fills and strokes, OPM 1, and the transfer function
	// (nil: identity); see extgstate.go.
	opFill, opStroke, opm1 bool
	transfer               *transfer

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
	// base is the CTM at the start of the content stream running: the
	// space of the patterns it sets.
	base Matrix

	path       Path
	cur, start stilus.Point
	hasCur     bool
	clip       int8 // pending W/W*: -1 none, else the fill rule

	// knockouts counts the knockout groups being drawn.
	knockouts int

	text textObject
	td   TextDevice // dev, if it wants the text
	tmp  Path       // glyph outlines filled with a pattern
	t3c  t3Clip     // Type 3 glyphs shown in a clipping mode

	// Tiling patterns: the device pixels drawn (empty: unknown), the
	// depth of pattern cells being drawn, the tiles made for this page
	// and their bytes.
	devBox    image.Rectangle
	patDepth  int
	tiles     map[tileKey]*Tile
	tileBytes int

	paint Paint

	scanners  []*content.Scanner // one per form depth
	depth     int                // of the content stream running
	objs      []objState         // saved while soft masks are drawn
	masks     int                // soft masks drawn
	maskDepth int                // soft masks being drawn
	dashes    []float64          // dash patterns of this page, append-only
	defs      defaultMemo        // device spaces of the resources last used
	dashBuf   []float64

	// Optional content. out is the device the page is drawn to; rec is
	// out when it is a display list, which records hidden content with
	// tags. Any other device sees hidden content through mute, which
	// passes on only the clips, against ocVis at zoom ocZoom.
	out    Device
	rec    *displayList
	mute   clipsOnly
	ocVis  *Visibility
	ocZoom float64
	ocCur  int32   // the list's tag; for other devices 1 while hidden
	mc     []int32 // ocCur before each open BMC or BDC
	mcBase int     // len(mc) when the running content stream started

	// mcd, when the device is a MarkedContentDevice, is told of every
	// sequence on mc; mcBuf is the MarkedContent passed to it.
	mcd   MarkedContentDevice
	mcBuf MarkedContent

	annotBuf []byte // generated appearance streams
	// formVals are the field values that differ from the saved ones, for
	// the appearances of widgets (RenderOptions.Form).
	formVals map[*Field]Value

	// overprint simulates overprint (RenderOptions.SimulateOverprint).
	overprint bool
}

func (in *interp) reset(doc *Document, dev Device, st *Stats, lim *limit) {
	*in = interp{
		doc: doc, dev: dev, st: st, lim: lim,
		stack: in.stack[:0], path: in.path, clip: -1, tmp: in.tmp,
		scanners: in.scanners, dashes: in.dashes[:0], dashBuf: in.dashBuf,
		text: in.text.keep(), t3c: t3Clip{clips: in.t3c.clips}, objs: in.objs[:0], mc: in.mc[:0], annotBuf: in.annotBuf[:0],
		out: dev, ocVis: &noLayers, ocZoom: 1,
	}
	in.path.Reset()
	in.td, _ = dev.(TextDevice)
	in.mcd, _ = dev.(MarkedContentDevice)
	in.rec, _ = dev.(*displayList)
	in.mute.d = dev
}

// release drops references to the document so pooled workers do not keep
// it alive.
func (in *interp) release() {
	clear(in.stack[:cap(in.stack)]) // popped states hold fonts too
	in.stack = in.stack[:0]
	in.doc, in.dev, in.st, in.lim, in.td, in.mcd = nil, nil, nil, nil, nil, nil
	in.out, in.rec, in.mute.d, in.ocVis = nil, nil, nil, nil
	in.formVals = nil
	in.tiles = nil
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
func (in *interp) run(data []byte, res pdf.Dict, ctm Matrix, depth int) {
	in.initState(ctm)
	in.base = ctm
	in.exec(data, res, depth)
	in.unwind(0)
	in.popClips(in.gs.clips)
	in.gs.clips = 0
}

// exec interprets one content stream in the current state.
func (in *interp) exec(data []byte, res pdf.Dict, depth int) {
	for len(in.scanners) <= depth {
		in.scanners = append(in.scanners, new(content.Scanner))
	}
	sc := in.scanners[depth]
	sc.Reset(data)
	outer, mcBase := in.depth, in.mcBase
	in.depth, in.mcBase = depth, len(in.mc)
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
	in.endMarked(in.mcBase) // marked content does not outlive its stream
	in.depth, in.mcBase = outer, mcBase
}

func (in *interp) expired() bool {
	if in.lim.expired() {
		in.err = ErrDeadline
		return true
	}
	return false
}

// lookupRef returns the entry of the resource category cat named by the
// operand name, unresolved, so that a reference can serve as a cache key.
func (in *interp) lookupRef(res pdf.Dict, cat pdf.Name, sc *content.Scanner, name *content.Operand) pdf.Object {
	if name == nil || name.Kind != content.Name {
		return pdf.Null
	}
	return in.doc.dict(res.Get(cat)).Get(pdf.Name(sc.Text(name)))
}

func (in *interp) do(sc *content.Scanner, op []byte, res pdf.Dict, depth int) {
	var v [6]float64
	bad := func() { in.st.Errors++ }
	switch string(op) {
	// Graphics state.
	case "q":
		if len(in.stack) >= maxStateDepth {
			in.st.unsupported("nesting-budget")
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
		o := in.lookupRef(res, "ExtGState", sc, sc.Last())
		ref, _ := o.Ref()
		d, _ := in.doc.resolve(o).Dict()
		in.extGState(d, ref, res)
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
		in.shadingOp(sc, res)

	// Text.
	case "BT", "ET", "Tc", "Tw", "Tz", "TL", "Tf", "Tr", "Ts", "Td", "TD", "Tm", "T*",
		"Tj", "TJ", "'", "\"", "d0", "d1":
		in.textOp(sc, op, res, depth)

	// Marked content and compatibility.
	case "BDC":
		in.mc = append(in.mc, in.ocCur)
		if sc.Len() >= 2 && sc.NameIs(sc.Arg(0), "OC") {
			o := in.lookupRef(res, "Properties", sc, sc.Arg(1))
			in.enterOC(o)
		}
		if in.mcd != nil {
			in.beginMarked(sc, res)
		}
	case "BMC":
		in.mc = append(in.mc, in.ocCur)
		if in.mcd != nil {
			in.beginMarked(sc, res)
		}
	case "EMC":
		if len(in.mc) > in.mcBase {
			in.endMarked(len(in.mc) - 1)
		}
	case "MP", "DP", "BX", "EX":
	default:
		bad()
	}
}

// color runs the colour operators.
func (in *interp) color(sc *content.Scanner, op []byte, res pdf.Dict) {
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
			in.gs.strokePat = patternPaint{}
		} else {
			in.gs.fillCS = cs
			cs.initial(in.gs.fill[:])
			in.gs.fillPat = patternPaint{}
		}
	case "SC", "SCN":
		in.setColor(sc, in.gs.strokeCS, in.gs.stroke[:])
		if in.gs.strokeCS.kind == csPattern {
			in.setPattern(sc, res, &in.gs.strokePat)
		}
	case "sc", "scn":
		in.setColor(sc, in.gs.fillCS, in.gs.fill[:])
		if in.gs.fillCS.kind == csPattern {
			in.setPattern(sc, res, &in.gs.fillPat)
		}
	default: // G g RG rg K k
		n := 1
		switch op[0] {
		case 'R', 'r':
			n = 3
		case 'K', 'k':
			n = 4
		}
		cs := in.deviceSpace(res, n)
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
func (in *interp) colorSpaceOperand(sc *content.Scanner, res pdf.Dict) *colorSpace {
	o := sc.Last()
	if o == nil || o.Kind != content.Name {
		return nil
	}
	switch string(sc.Text(o)) {
	case "DeviceGray", "G":
		return in.deviceSpace(res, 1)
	case "DeviceRGB", "RGB":
		return in.deviceSpace(res, 3)
	case "DeviceCMYK", "CMYK":
		return in.deviceSpace(res, 4)
	case "CalGray":
		return spaceGray
	case "CalRGB":
		return spaceRGB
	case "Pattern":
		return spacePattern
	}
	cs, approx := in.doc.colorSpace(pdf.Name(sc.Text(o)).Object(), res, 0)
	if approx != "" {
		in.st.unsupported(approx)
	}
	return cs
}

// deviceSpace is the device space of n components as res remaps it
// (Document.defaultSpace), resolved once per resource dictionary.
func (in *interp) deviceSpace(res pdf.Dict, n int) *colorSpace {
	if !res.Same(in.defs.res) {
		in.defs = defaultMemo{res: res}
	}
	if in.defs.cs[n] == nil {
		in.defs.cs[n], in.defs.approx[n] = in.doc.defaultSpace(res, n)
	}
	if a := in.defs.approx[n]; a != "" {
		in.st.unsupported(a)
	}
	return in.defs.cs[n]
}

// defaultMemo holds the device spaces of one resource dictionary.
type defaultMemo struct {
	res    pdf.Dict       // compared by identity, and kept so that it stays unique
	cs     [5]*colorSpace // by components
	approx [5]string
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
	if in.path.Empty() {
		return
	}
	if in.gs.fillCS.kind == csPattern {
		if !in.patternReady(&in.gs.fillPat, in.gs.fillAlpha) {
			return
		}
		in.st.Fills++
		if in.transparent() {
			in.beginObject(in.pathBox(0), in.gs.ctm, in.gs.blend, false)
			defer in.dev.EndGroup()
		}
		in.dev.ClipPath(&in.path, in.gs.ctm, rule)
		in.paintPattern(false, deviceBox(&in.path, in.gs.ctm, 1))
		in.dev.PopClip()
		return
	}
	if !in.setPaint(in.gs.fillCS, in.gs.fill[:], in.gs.fillAlpha) {
		return
	}
	in.st.Fills++
	if bm, grouped := in.objectBlend(in.gs.fillCS, in.gs.opFill); grouped {
		in.beginObject(in.pathBox(0), in.gs.ctm, bm, false)
		defer in.dev.EndGroup()
	}
	in.dev.FillPath(&in.path, in.gs.ctm, rule, &in.paint)
}

func (in *interp) strokePath() {
	if in.path.Empty() {
		return
	}
	if in.gs.strokeCS.kind == csPattern {
		if !in.patternReady(&in.gs.strokePat, in.gs.strokeAlp) {
			return
		}
		in.st.Strokes++
		if in.transparent() {
			in.beginObject(in.pathBox(in.strokePad()), in.gs.ctm, in.gs.blend, false)
			defer in.dev.EndGroup()
		}
		in.dev.ClipStroke(&in.path, in.gs.ctm, &in.gs.style)
		in.paintPattern(true, strokeBox(&in.path, in.gs.ctm, &in.gs.style))
		in.dev.PopClip()
		return
	}
	if !in.setPaint(in.gs.strokeCS, in.gs.stroke[:], in.gs.strokeAlp) {
		return
	}
	in.st.Strokes++
	if bm, grouped := in.objectBlend(in.gs.strokeCS, in.gs.opStroke); grouped {
		in.beginObject(in.pathBox(in.strokePad()), in.gs.ctm, bm, false)
		defer in.dev.EndGroup()
	}
	in.dev.StrokePath(&in.path, in.gs.ctm, &in.gs.style, &in.paint)
}

// setPaint prepares in.paint; false means nothing is painted. Patterns
// are painted by the callers that can (fills, strokes, text).
func (in *interp) setPaint(cs *colorSpace, v []float64, alpha float64) bool {
	if cs.kind == csPattern {
		in.st.unsupported("pattern")
		return false
	}
	if cs.none {
		return false
	}
	r, g, b := cs.rgb(v)
	if t := in.gs.transfer; t != nil {
		r, g, b = t.apply(r, g, b)
	}
	in.paint = Paint{Color: premul(r, g, b, alpha)}
	return in.paint.Color.A != 0
}

func (in *interp) setColor(sc *content.Scanner, cs *colorSpace, dst []float64) {
	if cs.kind == csPattern {
		// The components of an uncoloured pattern's colour come before
		// the name (see setPattern).
		for i := range cs.n {
			o := sc.FromEnd(cs.n - i)
			if o == nil || o.Kind != content.Number {
				break
			}
			dst[i] = o.Num
		}
		return
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

func (in *interp) extGState(d pdf.Dict, ref pdf.Ref, res pdf.Dict) {
	if d.IsZero() {
		in.st.Errors++
		return
	}
	doc := in.doc
	if v, ok := doc.num(d.Get("LW")); ok {
		in.gs.style.Width = math.Abs(v)
	}
	if v, ok := doc.num(d.Get("LC")); ok {
		in.gs.style.Cap = stilus.Cap(min(max(int(v), 0), 2))
	}
	if v, ok := doc.num(d.Get("LJ")); ok {
		in.gs.style.Join = stilus.Join(min(max(int(v), 0), 2))
	}
	if v, ok := doc.num(d.Get("ML")); ok {
		in.gs.style.MiterLimit = v
	}
	if a, ok := doc.resolve(d.Get("D")).Array(); ok && len(a) == 2 {
		arr, _ := doc.resolve(a[0]).Array()
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
	if v, ok := doc.num(d.Get("CA")); ok {
		in.gs.strokeAlp = clamp01(v)
	}
	if v, ok := doc.num(d.Get("ca")); ok {
		in.gs.fillAlpha = clamp01(v)
	}
	if o, ok := d.Lookup("BM"); ok {
		bm, known := doc.blendMode(o)
		if !known {
			in.st.unsupported("blend-mode")
		}
		in.gs.blend = bm
	}
	if o, ok := d.Lookup("SMask"); ok {
		in.gs.smask = in.softMask(o, res)
	}
	if o, ok := d.Lookup("AIS"); ok {
		in.gs.ais = doc.boolean(o)
		in.shapeAlpha()
	}
	in.extGStateMore(d, ref)
}

// enterOC applies the membership o of content that begins; the caller
// has saved ocCur on mc.
func (in *interp) enterOC(o pdf.Object) {
	e, bad := in.doc.membership(o)
	if bad {
		in.st.unsupported("oc-bad") // drawn visible
	}
	switch {
	case e == nil:
	case in.rec != nil:
		in.setOC(in.rec.ocTag(in.ocCur, e))
	case in.ocCur == 0 && !e.eval(in.ocVis, in.ocZoom):
		in.setOC(1)
	}
}

// endMarked closes the marked content mc[n:].
func (in *interp) endMarked(n int) {
	if n < len(in.mc) {
		if in.mcd != nil {
			for range len(in.mc) - n {
				in.mcd.EndMarkedContent()
			}
		}
		in.setOC(in.mc[n])
		in.mc = in.mc[:n]
	}
}

func (in *interp) setOC(c int32) {
	in.ocCur = c
	switch {
	case in.rec != nil:
		in.rec.tag = c
	case c != 0:
		in.dev = &in.mute
	default:
		in.dev = in.out
	}
}

// textDev returns the device that wants the text shown now, if any.
func (in *interp) textDev() TextDevice {
	if in.ocCur != 0 {
		return nil // hidden text is not text of the page
	}
	return in.td
}

// clipsOnly passes the clips on to d and drops everything else: what a
// device other than the display list sees of hidden content.
type clipsOnly struct{ d Device }

func (c *clipsOnly) FillPath(*Path, Matrix, FillRule, *Paint)       {}
func (c *clipsOnly) StrokePath(*Path, Matrix, *StrokeStyle, *Paint) {}
func (c *clipsOnly) ClipPath(p *Path, m Matrix, rule FillRule)      { c.d.ClipPath(p, m, rule) }
func (c *clipsOnly) ClipRect(r Rect, m Matrix)                      { c.d.ClipRect(r, m) }
func (c *clipsOnly) ClipStroke(p *Path, m Matrix, st *StrokeStyle)  { c.d.ClipStroke(p, m, st) }
func (c *clipsOnly) FillShading(*Shading, Matrix, *Paint)           {}
func (c *clipsOnly) FillTile(*Tile, Matrix, *Paint)                 {}
func (c *clipsOnly) PopClip()                                       { c.d.PopClip() }
func (c *clipsOnly) FillGlyphs(*GlyphRun, *Paint)                   {}
func (c *clipsOnly) DrawImage(*Image, Matrix, *Paint)               {}
func (c *clipsOnly) BeginGroup(Rect, Matrix, *Group)                {}
func (c *clipsOnly) EndGroup()                                      {}
func (c *clipsOnly) BeginMask(Rect, Matrix, *SoftMask)              {}
func (c *clipsOnly) EndMask()                                       {}

// xobject draws the XObject o. One that is optional content of its own
// (/OC) is tagged like marked content.
func (in *interp) xobject(o pdf.Object, res pdf.Dict, depth int) {
	if in.rec == nil && in.ocCur != 0 {
		return // hidden: an XObject changes no state outside itself
	}
	doc := in.doc
	ref, _ := o.Ref()
	s, ok := doc.resolve(o).Stream()
	if !ok {
		in.st.Errors++
		return
	}
	if oc, ok := s.Dict.Lookup("OC"); ok {
		n := len(in.mc)
		in.mc = append(in.mc, in.ocCur)
		in.enterOC(oc)
		if in.rec != nil || in.ocCur == 0 {
			in.drawXObject(ref, s, res, depth)
		}
		// Not a marked-content sequence: the device was not told of it.
		in.setOC(in.mc[n])
		in.mc = in.mc[:n]
		return
	}
	in.drawXObject(ref, s, res, depth)
}

func (in *interp) drawXObject(ref pdf.Ref, s *pdf.Stream, res pdf.Dict, depth int) {
	doc := in.doc
	sub, _ := doc.name(s.Dict.Get("Subtype"))
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

// form runs a form XObject (PDF 2.0, 8.10) inside q … Q. A transparency
// group, or any form painted with a blend mode or soft mask, is drawn as a
// group.
func (in *interp) form(s *pdf.Stream, parent pdf.Dict, depth int) {
	if depth >= maxFormDepth || len(in.stack)+1 >= maxStateDepth {
		in.st.unsupported("nesting-budget")
		return
	}
	doc := in.doc
	var g Group
	isGroup := false
	if gd := doc.dict(s.Dict.Get("Group")); !gd.IsZero() {
		if n, _ := doc.name(gd.Get("S")); n == "Transparency" {
			isGroup = true
			g.Isolated, g.Knockout = doc.boolean(gd.Get("I")), doc.boolean(gd.Get("K"))
		}
	}
	if g.Knockout {
		in.knockouts++
		defer func() { in.knockouts-- }()
		in.shapeAlpha()
	}
	grouped := isGroup || in.transparent()

	// A form starts with a new path and ends with its own states unwound.
	in.stack = append(in.stack, in.gs)
	in.gs.clips = 0
	base := len(in.stack)
	if !grouped {
		in.runForm(s, parent, depth+1)
		in.unwind(base)
		in.restore()
		return
	}
	g.Blend, g.Alpha = in.gs.blend, 255
	if isGroup {
		g.Alpha = unit8(in.gs.fillAlpha)
	}
	// The group is bounded by the BBox, which formSpace clips to; its
	// content runs in a state of its own, so that its clips are popped
	// before the group ends.
	bbox, ok := in.formSpace(s)
	if !ok {
		bbox = Rect{-1 << 20, -1 << 20, 1 << 20, 1 << 20}
	}
	if in.gs.smask != nil {
		in.drawSoftMask(bbox, in.gs.ctm)
		g.Masked = true
	}
	in.st.Groups++
	in.dev.BeginGroup(bbox, in.gs.ctm, &g)
	in.stack = append(in.stack, in.gs)
	in.gs.clips = 0
	in.gs.blend, in.gs.smask = BlendNormal, nil
	if isGroup {
		in.gs.fillAlpha, in.gs.strokeAlp = 1, 1
	}
	in.formContent(s, parent, depth+1)
	in.unwind(base + 1)
	in.restore()
	in.dev.EndGroup()
	in.unwind(base)
	in.restore()
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
	if img.softMask && in.gs.smask != nil {
		// An image's /SMask overrides the soft mask of the graphics
		// state; blend mode and alpha still apply (PDF 2.0, Table 87).
		sm := in.gs.smask
		in.gs.smask = nil
		defer func() { in.gs.smask = sm }()
	}
	if img.Stencil && in.gs.fillCS.kind == csPattern {
		in.stencilPattern(img)
		return
	}
	bm, grouped := in.gs.blend, in.transparent()
	if img.Stencil {
		if !in.setPaint(in.gs.fillCS, in.gs.fill[:], in.gs.fillAlpha) {
			return
		}
		bm, grouped = in.objectBlend(in.gs.fillCS, in.gs.opFill)
	} else {
		a := unit8(in.gs.fillAlpha)
		if a == 0 {
			return
		}
		in.paint = Paint{Color: color.RGBA{A: a}}
		in.untransferred()
	}
	in.st.Images++
	if grouped {
		in.beginObject(Rect{0, 0, 1, 1}, in.gs.ctm, bm, false)
		defer in.dev.EndGroup()
	}
	in.dev.DrawImage(img, in.gs.ctm, &in.paint)
}

// stencilPattern paints the fill pattern through the stencil mask img:
// the stencil, drawn opaque, is the alpha soft mask of a group the
// pattern fills.
func (in *interp) stencilPattern(img *Image) {
	if !in.patternReady(&in.gs.fillPat, in.gs.fillAlpha) {
		return
	}
	in.st.Images++
	unit, m := Rect{0, 0, 1, 1}, in.gs.ctm
	if in.transparent() {
		in.beginObject(unit, m, in.gs.blend, false)
		defer in.dev.EndGroup()
	}
	in.st.Groups++
	in.dev.BeginMask(unit, m, &SoftMask{})
	in.dev.DrawImage(img, m, &Paint{Color: color.RGBA{255, 255, 255, 255}})
	in.dev.EndMask()
	in.st.Groups++
	in.dev.BeginGroup(unit, m, &Group{Isolated: true, Blend: BlendNormal, Alpha: 255, Masked: true})
	in.dev.ClipRect(unit, m)
	in.paintPattern(false, deviceBoxPoints([]stilus.Point{{X: 0, Y: 0}, {X: 1, Y: 1}}, m, 1))
	in.dev.PopClip()
	in.dev.EndGroup()
}

// inlineImage draws the inline image of a BI operation. Inline images
// are small and not cached.
func (in *interp) inlineImage(sc *content.Scanner, res pdf.Dict) {
	op, data := sc.Image()
	if op == nil {
		in.st.Errors++
		return
	}
	d, _ := operandObject(sc, op).Dict()
	d = pdf.ExpandInline(d)
	r := in.doc.decodeImage(d, data, res)
	in.drawImage(&r)
}

// operandObject converts an operand to a reader object (for inline image
// dictionaries; it allocates).
func operandObject(sc *content.Scanner, o *content.Operand) pdf.Object {
	switch o.Kind {
	case content.Number:
		if o.Int && math.Abs(o.Num) < 1<<53 {
			return pdf.Integer(int64(o.Num))
		}
		return pdf.Real(o.Num)
	case content.Name:
		return pdf.Name(sc.Text(o)).Object()
	case content.String, content.HexString:
		return pdf.String(bytes.Clone(sc.Text(o)))
	case content.Bool:
		return pdf.Boolean(o.Num != 0)
	case content.Array:
		var a pdf.Array
		sc.Elems(o, func(e *content.Operand) bool {
			a = append(a, operandObject(sc, e))
			return true
		})
		return a.Object()
	case content.Dict:
		var entries []pdf.Entry
		var key *content.Operand
		sc.Elems(o, func(e *content.Operand) bool {
			if key == nil {
				key = e
				return true
			}
			if key.Kind == content.Name {
				k := pdf.Name(sc.Text(key)) // before Text is called again
				entries = append(entries, pdf.Entry{Key: k, Val: operandObject(sc, e)})
			}
			key = nil
			return true
		})
		return pdf.NewDict(entries...).Object()
	}
	return pdf.Null
}
