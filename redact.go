package cera

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"slices"

	"github.com/timzifer/cera/internal/content"
	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// Redaction (PDF 2.0, 12.5.6.23): taking content out of a page for good.
// A box drawn over text leaves the text in the file; a redaction rewrites
// the content of the page so that what was in its areas is not there to be
// found — by a viewer, a text extraction, or a look at the bytes.
//
// The interpreter runs the content of the page as it does to draw it, and
// a redactor decides for every operation whether its bytes are copied,
// left out or replaced:
//
//   - text: the glyphs whose boxes meet an area leave their strings, and
//     a movement of their width stands in for them, so that the rest of
//     the line stays where it was;
//   - paths: one that lies in an area is left out, one that reaches into
//     an area is clipped to what is outside;
//   - images: one that lies in an area is left out; of one that reaches
//     into an area the samples are decoded, emptied there, and encoded
//     again; an inline image that meets an area is left out;
//   - shadings painted by sh are clipped to what is outside;
//   - form XObjects are rewritten like the page, each use a copy of its
//     own, so that other pages and other uses keep theirs.
//
// It fails closed: what it cannot rewrite is left out whole, or the save
// fails. Nothing that meets an area stays as it was.

// redaction is one call of Editor.Redact: areas of a page in its default
// user space, and the colour of the boxes drawn over them, nil for none.
type redaction struct {
	areas []Quad
	fill  color.Color
}

// ErrRedaction is returned when a file with a redaction is written and
// the content of a page cannot be rewritten: nothing is written then.
var ErrRedaction = errors.New("cera: the page cannot be redacted")

// Redact removes what page i of the result, a page of the edited
// document, has in the areas, which are in points of the displayed page:
// the glyphs, paths and images drawn there, also by form XObjects and in
// layers that are hidden, and the annotations, links and form fields that
// reach into them. With a fill, boxes of that colour are drawn over the
// areas.
//
// The content is taken out when the file is written, and it is written
// anew: [Editor.Save] writes what is left and nothing of what was there,
// while [Editor.Update], which keeps the bytes of the file, returns
// [ErrNoUpdate]. Save returns [ErrRedaction] when the content of the page
// cannot be rewritten.
//
// What a redaction takes out besides: /ActualText, /Alt and /E of the
// marked content and of the structure elements the removed content
// belongs to, and the thumbnail of the page. What it leaves, for the
// caller to look at: the outline, whose titles may quote a heading; the
// metadata; attachments; other pages. A path that reaches into an area is
// clipped, not cut: its numbers stay in the file. Of a tiling pattern or a
// soft mask the cells are not rewritten; what they paint in an area is
// clipped away with the path they fill.
//
// Fields that reach into an area are removed as [Editor.RemoveField]
// removes them, and Redact returns what that returns: [ErrCertified],
// [ErrLocked]. Drawings on the same page ([Editor.Draw]) are made after
// the file is written.
func (e *Editor) Redact(i int, areas []Quad, fill color.Color) error {
	if err := e.check(i); err != nil {
		return err
	}
	ep := &e.pages[i]
	if e.base == nil || !ep.own || ep.blank {
		return errors.New("cera: a page of the edited document is redacted, not a new or an imported one")
	}
	if len(ep.draws) > 0 {
		return errors.New("cera: the page has drawings of this editor: write them first, then redact the result")
	}
	if !e.unguarded && e.base.signing().mdp != 0 {
		return ErrCertified
	}
	pg, err := e.base.Page(ep.index)
	if err != nil {
		return err
	}
	inv, ok := pg.Rotated(ep.rotate).DisplayMatrix().Invert()
	if !ok {
		return errors.New("cera: the page has no area")
	}
	var user []Quad
	for _, q := range areas {
		u := q.Transform(inv)
		for _, p := range u.P {
			if math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.IsInf(p[0], 0) || math.IsInf(p[1], 0) {
				return fmt.Errorf("cera: the area %v of a redaction is not finite", q)
			}
		}
		if u.area() > 0 {
			user = append(user, u)
		}
	}
	if len(user) == 0 {
		return nil
	}

	// The annotations that reach into the areas go, the fields of widgets
	// among them with all their widgets.
	var fields []string
	var annots []int
	form := e.base.Form()
	for _, a := range pg.Annotations() {
		if ep.annots != nil && ep.annots.gone[a.Index] {
			continue
		}
		if !slices.ContainsFunc(user, func(u Quad) bool { return quadsMeet(QuadOf(a.Rect), u) }) {
			continue
		}
		if w := form.widget(pg.index, a.Index); w != nil {
			if !e.delFields[w.Field.Name] && !slices.Contains(fields, w.Field.Name) {
				fields = append(fields, w.Field.Name)
			}
			continue
		}
		annots = append(annots, a.Index)
	}
	if !e.unguarded {
		sg := e.base.signing()
		for _, name := range fields {
			if sg.locked(name) {
				return ErrLocked
			}
		}
	}
	for _, name := range fields {
		if err := e.RemoveField(name); err != nil {
			return err
		}
	}
	for _, index := range annots {
		if err := e.RemoveAnnotation(i, index); err != nil {
			return err
		}
	}
	// Widgets added in this editor that reach into the areas.
	e.addFields = slices.DeleteFunc(e.addFields, func(a *addedField) bool {
		a.widgets = slices.DeleteFunc(a.widgets, func(w addedWidget) bool {
			return w.page == ep.index && slices.ContainsFunc(user, func(u Quad) bool { return quadsMeet(QuadOf(w.rect), u) })
		})
		return len(a.widgets) == 0
	})
	ep.redacts = append(ep.redacts, redaction{areas: user, fill: fill})
	return nil
}

// ApplyRedactions carries out the redaction annotations of the edited
// document's pages that the result still has (/Subtype /Redact): the areas
// they mark are redacted as [Editor.Redact] does, with boxes in their
// interior colour, and the annotations go with them. It returns the number
// of annotations applied. The text an annotation asks to be written over
// its area (/OverlayText) is not written.
func (e *Editor) ApplyRedactions() (int, error) {
	if e.base == nil {
		return 0, nil
	}
	n := 0
	for i := range e.pages {
		ep := &e.pages[i]
		if !ep.own || ep.blank {
			continue
		}
		pg, err := e.base.Page(ep.index)
		if err != nil {
			return n, err
		}
		view := pg.Rotated(ep.rotate).DisplayMatrix()
		for _, a := range pg.Annotations() {
			if a.Subtype != "Redact" || ep.annots != nil && ep.annots.gone[a.Index] {
				continue
			}
			d := e.base
			var areas []Quad
			if pts, ok := d.resolve(a.dict.Get("QuadPoints")).Array(); ok {
				for k := 0; k+8 <= len(pts) && len(areas) < maxAnnots; k += 8 {
					var v [8]float64
					valid := true
					for j := range v {
						var ok bool
						v[j], ok = d.num(pts[k+j])
						valid = valid && ok
					}
					if valid {
						// As viewers write them: the upper corners, then the lower.
						areas = append(areas, Quad{P: [4][2]float64{{v[0], v[1]}, {v[2], v[3]}, {v[4], v[5]}, {v[6], v[7]}}}.Transform(view))
					}
				}
			}
			if len(areas) == 0 {
				areas = []Quad{QuadOf(a.Rect).Transform(view)}
			}
			var fill color.Color
			if c, ok := d.annotColor(a.dict.Get("IC")); ok && len(c) > 0 {
				fill = *rgbaOf(c)
			}
			if err := e.Redact(i, areas, fill); err != nil {
				return n, err
			}
			// Its rectangle may lie beside its quads: it goes in any case.
			if ep.annots == nil || !ep.annots.gone[a.Index] {
				if err := e.RemoveAnnotation(i, a.Index); err != nil {
					return n, err
				}
			}
			n++
		}
	}
	return n, nil
}

// redacts reports whether the editor redacts pages.
func (e *Editor) redacts() bool {
	return slices.ContainsFunc(e.pages, func(p editPage) bool { return len(p.redacts) > 0 })
}

// quadsMeet reports whether two convex quads have area in common: no line
// along an edge of one has the other wholly on its far side. Quads that
// only touch do not meet.
func quadsMeet(a, b Quad) bool {
	const eps = 1e-9
	ring := func(q Quad) [4][2]float64 { return [4][2]float64{q.P[0], q.P[1], q.P[3], q.P[2]} }
	ra, rb := ring(a), ring(b)
	if a.area() <= 0 || b.area() <= 0 {
		return false
	}
	apart := func(p, q [4][2]float64) bool {
		for i, u := range p {
			v := p[(i+1)%4]
			// The normal of the edge, and where the corners of both
			// stand along it.
			nx, ny := v[1]-u[1], u[0]-v[0]
			lo, hi := math.Inf(1), math.Inf(-1)
			for _, c := range p {
				d := c[0]*nx + c[1]*ny
				lo, hi = min(lo, d), max(hi, d)
			}
			qlo, qhi := math.Inf(1), math.Inf(-1)
			for _, c := range q {
				d := c[0]*nx + c[1]*ny
				qlo, qhi = min(qlo, d), max(qhi, d)
			}
			scale := math.Max(1, math.Hypot(nx, ny))
			if qhi <= lo+eps*scale || qlo >= hi-eps*scale {
				return true
			}
		}
		return false
	}
	return !apart(ra, rb) && !apart(rb, ra)
}

// quadWithin reports whether a lies in b.
func quadWithin(a, b Quad) bool {
	for _, p := range a.P {
		if !b.Contains(p[0], p[1]) {
			return false
		}
	}
	return true
}

// redactor rewrites the content of one page.
type redactor struct {
	doc   *Document
	areas []Quad // in default user space
	objs  *[]pdf.Object
	// pending is the frame of the stream that runs next, cur that of the
	// stream that runs: nil for one that is not rewritten.
	pending, cur *redFrame
	// touched are the marked-content identifiers of the page of which
	// content went, and gone the XObjects of the document that were left
	// out or written anew: images emptied, forms rewritten.
	touched map[int]bool
	gone    map[pdf.Ref]bool
	names   int // of the resources made
	failed  string
}

// redFrame is a content stream being rewritten: the page's, or that of a
// form XObject one of its operations draws.
type redFrame struct {
	r      *redactor
	parent *redFrame
	res    pdf.Dict
	src    []byte
	out    []byte
	copied int // of src
	ran    bool
	// changed says that out is not src.
	changed bool
	// Of the operation that runs: where it is in src and in out, the
	// matrix it starts with, and what was found of it.
	start, end, opOut int
	ctm               Matrix
	hit               int  // of a path, an image, a shading: hitNone and on
	box               Rect // of a path that is painted, with its stroke
	child             *redFrame
	// The path under construction: where its operators are in out, the
	// operator that makes it a clip, and whether other operators stand
	// between.
	path   [][2]int
	clipOp string
	mixed  bool
	// The strings a text operator has shown, and their glyphs.
	strs   int
	glyphs []redGlyph
	// The marked-content sequences that are open.
	mc []redMC
	// The XObjects made for this stream, to add to its resources, and
	// the names of those and of the property lists it has that what is
	// written still uses: the others leave its resources, where they
	// would keep what the content no longer shows.
	xobjects  []pdf.Entry
	usedX     map[pdf.Name]bool
	usedProps map[pdf.Name]bool
	nesting   int // q without Q
}

const (
	hitNone = iota
	hitPart
	hitWhole
)

// redGlyph is a glyph a text operator showed.
type redGlyph struct {
	str, off, n int
	hit         bool
	adjust      float64 // the number of TJ that moves as far as the glyph
}

// redMC is an open marked-content sequence.
type redMC struct {
	out, outEnd int      // its operator in out
	strip       []byte   // what replaces the operator when content of the sequence went; nil to leave it
	prop        pdf.Name // the property list it names in the resources
	mcid        int
	touched     bool
}

// used notes a name of the resources that what is written uses.
func used(m *map[pdf.Name]bool, name pdf.Name) {
	if *m == nil {
		*m = map[pdf.Name]bool{}
	}
	(*m)[name] = true
}

// doName returns the name a Do operator draws.
func doName(sc *content.Scanner) pdf.Name {
	if o := sc.Last(); o != nil && o.Kind == content.Name {
		return pdf.Name(sc.Text(o))
	}
	return ""
}

// enter starts the frame of a stream that begins to run.
func (r *redactor) enter(data []byte, res pdf.Dict) (frame, outer *redFrame) {
	frame, outer = r.pending, r.cur
	r.pending, r.cur = nil, frame
	if frame != nil {
		frame.src, frame.res, frame.ran = data, res, true
	}
	return frame, outer
}

// leave ends it. A stream that did not run to its end is not known to be
// rewritten whole.
func (r *redactor) leave(frame, outer *redFrame, whole bool) {
	r.cur = outer
	if frame == nil {
		return
	}
	if !whole {
		r.fail("its content did not run to the end")
	}
	frame.out = append(frame.out, frame.src[frame.copied:]...)
	frame.copied = len(frame.src)
	for len(frame.mc) > 0 {
		frame.endMarked()
	}
}

func (r *redactor) fail(why string) {
	if r.failed == "" {
		r.failed = why
	}
}

// str counts a string shown.
func (r *redactor) str() {
	if f := r.cur; f != nil {
		f.strs++
	}
}

// glyph takes a glyph of the string shown last: the bytes off to off+n of
// it, of advance w in the em, with box mapping the em to default user
// space. step is how far the pen moves for it in the em, and spacing what
// is added to that in text space, at the font size.
func (r *redactor) glyph(off, n int, f *Font, w float64, box Matrix, step, spacing, size float64) {
	fr := r.cur
	if fr == nil {
		return
	}
	g := redGlyph{str: fr.strs - 1, off: off, n: n}
	if size != 0 {
		g.adjust = -(step*1000 + spacing*1000/size)
	}
	// The box of the glyph: its advance wide, from the descent to the
	// ascent of its font, and at least a hair wide, so that a glyph
	// without advance in an area goes too.
	asc, desc := f.ascent, f.descent
	if !(asc > desc) {
		asc, desc = 0.8, -0.2
	}
	x0, x1 := min(0, w), max(0, w)
	if x1-x0 < 0.05 {
		x0, x1 = x0-0.025, x1+0.025
	}
	q := Quad{P: [4][2]float64{{x0, asc}, {x1, asc}, {x0, desc}, {x1, desc}}}.Transform(box)
	for _, p := range q.P {
		if math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.IsInf(p[0], 0) || math.IsInf(p[1], 0) {
			g.hit = true // where it is cannot be told: it goes
		}
	}
	g.hit = g.hit || slices.ContainsFunc(r.areas, func(a Quad) bool { return quadsMeet(q, a) })
	fr.glyphs = append(fr.glyphs, g)
}

// meets says how the box b, under m, lies to the areas.
func (r *redactor) meets(b Rect, m Matrix) int {
	q := QuadOf(b).Transform(m)
	for _, p := range q.P {
		if math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.IsInf(p[0], 0) || math.IsInf(p[1], 0) {
			return hitWhole
		}
	}
	hit := hitNone
	for _, a := range r.areas {
		if quadWithin(q, a) {
			return hitWhole
		}
		if quadsMeet(q, a) {
			hit = hitPart
		}
	}
	return hit
}

// touch marks that content of the stream went: its marked content, and
// that of the streams it is drawn in.
func (fr *redFrame) touch() {
	for f := fr; f != nil; f = f.parent {
		f.changed = true
		for i := range f.mc {
			f.mc[i].touched = true
		}
	}
}

var (
	pathOps  = map[string]bool{"m": true, "l": true, "c": true, "v": true, "y": true, "h": true, "re": true}
	paintOps = map[string]bool{"S": true, "s": true, "f": true, "F": true, "f*": true, "B": true, "B*": true, "b": true, "b*": true}
	showOps  = map[string]bool{"Tj": true, "TJ": true, "'": true, "\"": true}
)

// before looks at an operation that is about to run.
func (fr *redFrame) before(in *interp, sc *content.Scanner, op []byte) {
	r := fr.r
	fr.start, fr.end = sc.Span()
	fr.out = append(fr.out, fr.src[fr.copied:fr.start]...)
	fr.copied = fr.start
	fr.opOut = len(fr.out)
	fr.ctm = in.gs.ctm
	fr.hit, fr.child = hitNone, nil
	name := string(op)
	switch {
	case pathOps[name], name == "W", name == "W*":
		if name == "W" || name == "W*" {
			fr.clipOp = name
		}
	case paintOps[name]:
		b := in.path.Bounds()
		box := Rect{float64(b.X0), float64(b.Y0), float64(b.X1), float64(b.Y1)}
		// A stroke reaches beyond its path: by half its width, and
		// further at a mitre. A hair is added, so that a line has a box
		// with an area.
		pad := 0.01
		if name != "f" && name != "F" && name != "f*" {
			pad += in.gs.style.Width / 2 * max(1, in.gs.style.MiterLimit)
		}
		fr.box = Rect{box.X0 - pad, box.Y0 - pad, box.X1 + pad, box.Y1 + pad}
		if len(in.path.Points) > 0 {
			fr.hit = r.meets(fr.box, fr.ctm)
		}
	case showOps[name]:
		fr.strs, fr.glyphs = 0, fr.glyphs[:0]
	case name == "Do":
		o := in.lookupRef(fr.res, "XObject", sc, sc.Last())
		s, ok := r.doc.resolve(o).Stream()
		if !ok {
			break
		}
		switch sub, _ := r.doc.name(s.Dict.Get("Subtype")); sub {
		case "Form":
			fr.child = &redFrame{r: r, parent: fr}
			r.pending = fr.child
		case "Image":
			fr.hit = r.meets(Rect{0, 0, 1, 1}, fr.ctm)
		}
	case name == "BI":
		fr.hit = r.meets(Rect{0, 0, 1, 1}, fr.ctm)
	case name == "sh":
		fr.hit = hitPart // it paints all the clip lets through
	case name == "BDC", name == "BMC":
		fr.mc = append(fr.mc, fr.marked(in, sc, name))
	case name == "q":
		fr.nesting++
	case name == "Q":
		fr.nesting = max(fr.nesting-1, 0)
	}
	if len(fr.path) > 0 && !pathOps[name] && !paintOps[name] && name != "W" && name != "W*" && name != "n" {
		fr.mixed = true
	}
}

// after writes an operation that has run, or what stands for it.
func (fr *redFrame) after(in *interp, sc *content.Scanner, op []byte) {
	r := fr.r
	name := string(op)
	keep := func() { fr.out = append(fr.out, fr.src[fr.start:fr.end]...) }
	fr.copied = fr.end
	switch {
	case pathOps[name], name == "W", name == "W*":
		keep()
		fr.path = append(fr.path, [2]int{fr.opOut, len(fr.out)})
	case paintOps[name]:
		switch {
		case fr.hit == hitNone:
			keep()
		case fr.hit == hitWhole || fr.mixed || !fr.clipPath(in):
			fr.dropPath()
		}
		fr.path, fr.clipOp, fr.mixed = fr.path[:0], "", false
	case name == "n":
		keep()
		fr.path, fr.clipOp, fr.mixed = fr.path[:0], "", false
	case showOps[name]:
		if !slices.ContainsFunc(fr.glyphs, func(g redGlyph) bool { return g.hit }) {
			keep()
			break
		}
		fr.out = append(fr.out, fr.text(sc, name)...)
		fr.touch()
	case name == "Do" && fr.child != nil:
		child := fr.child
		r.pending = nil
		switch {
		case !child.ran:
			// Not run, so not looked at: it does not stay.
			r.lose(in, sc, fr)
			fr.touch()
		case child.changed:
			r.lose(in, sc, fr)
			fr.out = append(fr.out, fr.use(r.form(in, sc, fr, child))...)
			fr.changed = true
		default:
			keep()
			used(&fr.usedX, doName(sc))
		}
	case name == "Do" && fr.hit == hitPart:
		r.lose(in, sc, fr)
		if o, ok := r.image(in, sc, fr); ok {
			fr.out = append(fr.out, fr.use(o)...)
		}
		fr.touch()
	case (name == "Do" || name == "BI") && fr.hit == hitWhole, name == "BI" && fr.hit == hitPart:
		if name == "Do" {
			r.lose(in, sc, fr)
		}
		fr.touch()
	case name == "sh":
		if clip, ok := fr.clipOut(in, Rect{}, false); ok {
			fr.out = append(append(append(append(fr.out, "q\n"...), clip...), fr.src[fr.start:fr.end]...), "\nQ\n"...)
			fr.changed = true
		} else {
			fr.touch()
		}
	case name == "BDC", name == "BMC":
		keep()
		fr.mc[len(fr.mc)-1].outEnd = len(fr.out)
	case name == "EMC":
		keep()
		if len(fr.mc) > 0 {
			fr.endMarked()
		}
	case name == "Do":
		keep()
		used(&fr.usedX, doName(sc))
	case name == "DP":
		keep()
		if o := sc.Last(); o != nil && o.Kind == content.Name {
			used(&fr.usedProps, pdf.Name(sc.Text(o)))
		}
	default:
		keep()
	}
}

// dropPath takes the path under construction out of what is written.
// Where it was a clip too, what is painted until the clip ends was to
// show through it, in the area: an empty clip stands for it, so that this
// is not painted elsewhere.
func (fr *redFrame) dropPath() {
	for k := len(fr.path) - 1; k >= 0; k-- {
		fr.out = slices.Delete(fr.out, fr.path[k][0], fr.path[k][1])
	}
	if fr.clipOp != "" {
		fr.out = append(fr.out, "0 0 0 0 re W n\n"...)
	}
	fr.touch()
}

// clipPath writes the path under construction clipped to what is outside
// the areas. It reports false where it cannot.
func (fr *redFrame) clipPath(in *interp) bool {
	if len(fr.path) == 0 {
		return false
	}
	b := fr.box
	clip, ok := fr.clipOut(in, Rect{b.X0 - 1, b.Y0 - 1, b.X1 + 1, b.Y1 + 1}, true)
	if !ok {
		return false
	}
	var again []byte
	if fr.clipOp != "" {
		// The clip the path makes lasts beyond the Q: made once more.
		for _, s := range fr.path {
			again = append(append(again, fr.out[s[0]:s[1]]...), ' ')
		}
		again = append(again, "n\n"...)
	}
	fr.out = append(fr.out, fr.src[fr.start:fr.end]...)
	fr.out = append(fr.out, "\nQ\n"...)
	fr.out = append(fr.out, again...)
	fr.out = slices.Insert(fr.out, fr.path[0][0], append([]byte("q\n"), clip...)...)
	fr.touch()
	return true
}

// clipOut returns operators that clip to what is outside the areas, in
// the user space of the operation that runs: for each area a path of the
// box around what is painted and the area, of which the even-odd rule
// leaves the box without the area. Without a box, as for a shading, the
// box is all the areas can reach. It reports false for a space that does
// not lead back to the page.
func (fr *redFrame) clipOut(in *interp, box Rect, bounded bool) ([]byte, bool) {
	inv, ok := fr.ctm.Invert()
	if !ok {
		return nil, false
	}
	var w csw
	for _, a := range fr.r.areas {
		q := a.Transform(inv)
		for _, p := range q.P {
			if math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.Abs(p[0]) > 1e9 || math.Abs(p[1]) > 1e9 {
				return nil, false
			}
		}
		outer := box
		if !bounded {
			qb := q.Bounds()
			outer = Rect{qb.X0 - 1e6, qb.Y0 - 1e6, qb.X1 + 1e6, qb.Y1 + 1e6}
		}
		w.num(outer.X0)
		w.num(outer.Y0)
		w.num(outer.Dx())
		w.num(outer.Dy())
		w.op("re")
		w.moveTo(q.P[0][0], q.P[0][1])
		w.lineTo(q.P[1][0], q.P[1][1])
		w.lineTo(q.P[3][0], q.P[3][1])
		w.lineTo(q.P[2][0], q.P[2][1])
		w.op("h W* n")
	}
	return w, true
}

// text returns the text operator that ran without the glyphs that were
// hit: an array of the strings left and of numbers that move the pen as
// far as the glyphs did.
func (fr *redFrame) text(sc *content.Scanner, name string) []byte {
	var w csw
	var strs [][]byte // the strings of the operator, in order
	var nums [][]byte // before string i: the numbers of TJ, as written
	switch name {
	case "TJ":
		nums = append(nums, nil)
		if arr := sc.Last(); arr != nil && arr.Kind == content.Array {
			sc.Elems(arr, func(e *content.Operand) bool {
				switch e.Kind {
				case content.String, content.HexString:
					strs = append(strs, slices.Clone(sc.Text(e)))
					nums = append(nums, nil)
				case content.Number:
					var n csw
					n.num(e.Num)
					nums[len(nums)-1] = append(nums[len(nums)-1], n...)
				}
				return true
			})
		}
	default:
		if o := sc.Last(); o != nil && (o.Kind == content.String || o.Kind == content.HexString) {
			strs = append(strs, slices.Clone(sc.Text(o)))
		}
		nums = [][]byte{nil, nil}
	}
	// What ' and " do before they show.
	switch name {
	case "'":
		w.op("T*")
	case "\"":
		if sc.Len() >= 3 {
			w.num(sc.Arg(0).Num)
			w.op("Tw")
			w.num(sc.Arg(1).Num)
			w.op("Tc")
		}
		w.op("T*")
	}
	w = append(w, '[')
	g := 0
	for i, s := range strs {
		w = append(w, nums[i]...)
		kept := func(from, to int) {
			if to > from {
				w = fmt.Appendf(w, "<%X>", s[from:to])
			}
		}
		at := 0
		for ; g < len(fr.glyphs) && fr.glyphs[g].str == i; g++ {
			gl := fr.glyphs[g]
			if !gl.hit || gl.off < at || gl.off+gl.n > len(s) {
				continue
			}
			kept(at, gl.off)
			w.num(gl.adjust)
			at = gl.off + gl.n
		}
		kept(at, len(s))
	}
	if len(nums) > len(strs) {
		w = append(w, nums[len(strs)]...)
	}
	w = append(w, "] TJ\n"...)
	return w
}

// marked returns the sequence a BDC or BMC opens: with what is to be
// written in its place if content of it goes — the operator without the
// text that stands for the content.
func (fr *redFrame) marked(in *interp, sc *content.Scanner, name string) redMC {
	mc := redMC{out: fr.opOut, mcid: -1}
	if name != "BDC" || sc.Len() < 2 {
		return mc
	}
	d := fr.r.doc
	props := sc.Arg(1)
	var has func(key string) bool
	switch props.Kind {
	case content.Dict:
		has = func(key string) bool { return sc.DictGet(props, key) != nil }
		if v := sc.DictGet(props, "MCID"); v != nil && v.Kind == content.Number {
			mc.mcid = mcid(v.Num)
		}
	case content.Name:
		mc.prop = pdf.Name(sc.Text(props))
		pd := d.dict(in.lookupRef(fr.res, "Properties", sc, props))
		has = func(key string) bool { return pd.Has(pdf.Name(key)) }
		if v, ok := d.num(pd.Get("MCID")); ok {
			mc.mcid = mcid(v)
		}
	default:
		return mc
	}
	if !has("ActualText") && !has("Alt") && !has("E") {
		return mc
	}
	// The tag, with the identifier alone.
	var w csw
	tag := sc.Arg(0)
	if tag.Kind != content.Name {
		return mc
	}
	w = append(w, '/')
	for _, c := range sc.Text(tag) {
		if c < 0x21 || c > 0x7e || c == '#' || c == '/' || c == '%' || c == '(' || c == ')' || c == '<' || c == '>' || c == '[' || c == ']' || c == '{' || c == '}' {
			w = fmt.Appendf(w, "#%02X", c)
			continue
		}
		w = append(w, c)
	}
	if mc.mcid >= 0 {
		w = fmt.Appendf(w, " <</MCID %d>> BDC\n", mc.mcid)
	} else {
		w = append(w, " BMC\n"...)
	}
	mc.strip = w
	return mc
}

// endMarked closes the innermost sequence.
func (fr *redFrame) endMarked() {
	mc := fr.mc[len(fr.mc)-1]
	fr.mc = fr.mc[:len(fr.mc)-1]
	if mc.prop != "" && (!mc.touched || mc.strip == nil) {
		used(&fr.usedProps, mc.prop)
	}
	if !mc.touched {
		return
	}
	if fr.parent == nil && mc.mcid >= 0 {
		fr.r.touched[mc.mcid] = true
	}
	if mc.strip != nil && mc.outEnd >= mc.out {
		fr.out = slices.Replace(fr.out, mc.out, mc.outEnd, mc.strip...)
	}
}

// use returns the operator that draws the new XObject o in the stream.
func (fr *redFrame) use(o pdf.Object) []byte {
	xo := fr.r.doc.dict(fr.res.Get("XObject"))
	var name pdf.Name
	for {
		name = pdf.Name(fmt.Sprintf("CeraRedact%d", fr.r.names))
		fr.r.names++
		if !xo.Has(name) {
			break
		}
	}
	fr.xobjects = append(fr.xobjects, pdf.Entry{Key: name, Val: o})
	return []byte("/" + string(name) + " Do\n")
}

// lose notes the XObject a Do operator draws as one that went.
func (r *redactor) lose(in *interp, sc *content.Scanner, fr *redFrame) {
	if ref, ok := in.lookupRef(fr.res, "XObject", sc, sc.Last()).Ref(); ok {
		r.gone[ref] = true
	}
}

// add adds a new object and returns the reference to it.
func (r *redactor) add(o pdf.Object) pdf.Object {
	*r.objs = append(*r.objs, o)
	return pdfedit.NewRef(len(*r.objs) - 1).Object()
}

// resources returns the resources of a rewritten stream: those it had,
// with the XObjects made for it, and without the XObjects and property
// lists that what is written no longer names — an image that was emptied,
// a form that was rewritten, text that stood for content that went.
func (fr *redFrame) resources() pdf.Dict {
	d := fr.r.doc
	entries := append([]pdf.Entry(nil), fr.xobjects...)
	for k, v := range d.dict(fr.res.Get("XObject")).All() {
		if fr.usedX[k] {
			entries = append(entries, pdf.Entry{Key: k, Val: v})
		}
	}
	var props []pdf.Entry
	for k, v := range d.dict(fr.res.Get("Properties")).All() {
		if fr.usedProps[k] {
			props = append(props, pdf.Entry{Key: k, Val: v})
		}
	}
	out := []pdf.Entry{{Key: "XObject", Val: pdf.NewDict(entries...).Object()}}
	if len(props) > 0 {
		out = append(out, pdf.Entry{Key: "Properties", Val: pdf.NewDict(props...).Object()})
	}
	for k, v := range fr.res.All() {
		if k != "XObject" && k != "Properties" {
			out = append(out, pdf.Entry{Key: k, Val: v})
		}
	}
	return pdf.NewDict(out...)
}

// form returns the form XObject the operation drew, with the content
// child was rewritten to.
func (r *redactor) form(in *interp, sc *content.Scanner, fr, child *redFrame) pdf.Object {
	s, _ := r.doc.resolve(in.lookupRef(fr.res, "XObject", sc, sc.Last())).Stream()
	var entries []pdf.Entry
	for k, v := range s.Dict.All() {
		switch k {
		case "Filter", "DecodeParms", "Length", "Resources", "F", "FFilter", "FDecodeParms", "DL":
			continue
		case "PieceInfo", "Metadata", "Ref", "OPI", "LastModified":
			continue // what may hold or name the form as it was
		}
		entries = append(entries, pdf.Entry{Key: k, Val: v})
	}
	entries = append(entries, pdf.Entry{Key: "Resources", Val: child.resources().Object()})
	return r.add(pdfwrite.Flate(pdf.NewDict(entries...), child.out).Object())
}

// image returns the image the operation drew with its samples in the areas
// emptied: decoded as cera draws it and encoded anew, in RGB with its
// alpha. It reports false for an image it cannot write so, which is then
// left out: a stencil, which takes its colour from where it is drawn, and
// one that does not decode.
func (r *redactor) image(in *interp, sc *content.Scanner, fr *redFrame) (pdf.Object, bool) {
	o := in.lookupRef(fr.res, "XObject", sc, sc.Last())
	ref, _ := o.Ref()
	s, ok := r.doc.resolve(o).Stream()
	if !ok {
		return pdf.Null, false
	}
	res := r.doc.image(ref, s, fr.res)
	img := res.img
	if img == nil || img.Stencil || img.W <= 0 || img.H <= 0 {
		return pdf.Null, false
	}
	inv, ok := fr.ctm.Invert()
	if !ok {
		return pdf.Null, false
	}
	src := img.RGBA()
	dst := image.NewNRGBA(src.Rect)
	for i := 0; i < len(src.Pix); i += 4 {
		// Not premultiplied, as a file has it.
		a := uint32(src.Pix[i+3])
		if a == 0 {
			continue
		}
		dst.Pix[i] = uint8(min(uint32(src.Pix[i])*255/a, 255))
		dst.Pix[i+1] = uint8(min(uint32(src.Pix[i+1])*255/a, 255))
		dst.Pix[i+2] = uint8(min(uint32(src.Pix[i+2])*255/a, 255))
		dst.Pix[i+3] = uint8(a)
	}
	// The areas in samples: the unit square of the image has its first
	// row at the top.
	w, h := float64(img.W), float64(img.H)
	toSamples := Matrix{w, 0, 0, -h, 0, h}
	for _, a := range r.areas {
		q := a.Transform(inv).Transform(toSamples)
		b := q.Bounds()
		// A sample goes when the area reaches into it.
		x0, y0 := max(int(math.Floor(b.X0))-1, 0), max(int(math.Floor(b.Y0))-1, 0)
		x1, y1 := min(int(math.Ceil(b.X1))+1, img.W), min(int(math.Ceil(b.Y1))+1, img.H)
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				px := QuadOf(Rect{float64(x), float64(y), float64(x + 1), float64(y + 1)})
				if quadsMeet(px, q) {
					copy(dst.Pix[dst.PixOffset(x, y):][:4], []byte{0, 0, 0, 0})
				}
			}
		}
	}
	ci := NewCanvasImage(dst)
	if ci == nil {
		return pdf.Null, false
	}
	st := ci.stream
	if ci.mask != nil {
		data, _ := st.Unencrypted()
		st = pdf.NewStream(st.Dict.With("SMask", r.add(ci.mask.Object())), data)
	}
	return r.add(st.Object()), true
}

// nullDevice draws nothing: the interpreter runs for the redactor alone.
type nullDevice struct{}

func (nullDevice) FillPath(*Path, Matrix, FillRule, *Paint)       {}
func (nullDevice) StrokePath(*Path, Matrix, *StrokeStyle, *Paint) {}
func (nullDevice) ClipPath(*Path, Matrix, FillRule)               {}
func (nullDevice) ClipRect(Rect, Matrix)                          {}
func (nullDevice) ClipStroke(*Path, Matrix, *StrokeStyle)         {}
func (nullDevice) PopClip()                                       {}
func (nullDevice) FillShading(*Shading, Matrix, *Paint)           {}
func (nullDevice) FillTile(*Tile, Matrix, *Paint)                 {}
func (nullDevice) FillGlyphs(*GlyphRun, *Paint)                   {}
func (nullDevice) DrawImage(*Image, Matrix, *Paint)               {}
func (nullDevice) BeginGroup(Rect, Matrix, *Group)                {}
func (nullDevice) EndGroup()                                      {}
func (nullDevice) BeginMask(Rect, Matrix, *SoftMask)              {}
func (nullDevice) EndMask()                                       {}

// redactPage rewrites the content of page index of the edited document
// without what it has in the areas of rs, and returns the patch of the
// page with the marked-content identifiers of which content went.
func (e *Editor) redactPage(objs *[]pdf.Object, index int, rs []redaction, gone map[pdf.Ref]bool) (patch pdfedit.Patch, touched map[int]bool, err error) {
	d := e.base
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("%w: page %d: %v", ErrRedaction, index+1, v)
		}
	}()
	pg, err := d.Page(index)
	if err != nil {
		return patch, nil, err
	}
	ref, ok := d.r.PageRef(index + 1)
	if !ok {
		return patch, nil, fmt.Errorf("%w: page %d is not an object of its own", ErrRedaction, index+1)
	}
	r := &redactor{doc: d, objs: objs, touched: map[int]bool{}, gone: gone}
	for _, x := range rs {
		r.areas = append(r.areas, x.areas...)
	}
	root := &redFrame{r: r}
	r.pending = root

	res := d.dict(pg.dict.Get("Resources"))
	dec, derr := d.r.PageContents(index + 1)
	if derr != nil {
		return patch, nil, fmt.Errorf("%w: page %d: %v", ErrRedaction, index+1, derr)
	}
	var st Stats
	in := recorders.Get().(*interp)
	in.reset(d, nullDevice{}, &st, &limit{ctx: context.Background()})
	in.red = r
	// Every layer runs, shown or not: what is hidden is in the file too.
	in.run(dec.Data, res, identity, 0)
	failed := in.err
	in.release()
	recorders.Put(in)
	switch {
	case failed != nil:
		return patch, nil, fmt.Errorf("%w: page %d: %v", ErrRedaction, index+1, failed)
	case r.failed != "":
		return patch, nil, fmt.Errorf("%w: page %d: %s", ErrRedaction, index+1, r.failed)
	case !root.ran:
		return patch, nil, fmt.Errorf("%w: page %d: its content did not run", ErrRedaction, index+1)
	}

	// The boxes, in default user space: after as many Q as the content
	// left open.
	out := csw(root.out)
	out = append(out, '\n')
	for range root.nesting {
		out.op("Q")
	}
	for _, x := range rs {
		if x.fill == nil {
			continue
		}
		c := color.NRGBAModel.Convert(x.fill).(color.NRGBA)
		out.op("q")
		out.color([]float64{float64(c.R) / 255, float64(c.G) / 255, float64(c.B) / 255}, true)
		for _, a := range x.areas {
			out.moveTo(a.P[0][0], a.P[0][1])
			out.lineTo(a.P[1][0], a.P[1][1])
			out.lineTo(a.P[3][0], a.P[3][1])
			out.lineTo(a.P[2][0], a.P[2][1])
			out.op("h f")
		}
		out.op("Q")
	}
	contents := r.add(pdfwrite.Flate(pdf.NewDict(), out).Object())
	patch = pdfedit.Patch{Ref: ref, Set: []pdf.Entry{
		{Key: "Contents", Val: contents},
		{Key: "Resources", Val: root.resources().Object()},
		// What may hold a picture of the page as it was.
		{Key: "Thumb", Val: pdf.Null},
		{Key: "PieceInfo", Val: pdf.Null},
	}}
	return patch, r.touched, nil
}

// redactPlan rewrites the pages that are redacted, and takes from the
// structure tree the text that stands for content that went. It returns
// the first error: the file is not written then.
func (e *Editor) redactPlan(doc *pdfedit.Doc) error {
	d := e.base
	touched := map[int]map[int]bool{}
	gone := map[pdf.Ref]bool{}
	redacted := map[int]bool{}
	for _, ep := range e.pages {
		if !ep.own || len(ep.redacts) == 0 {
			continue
		}
		if len(ep.draws) > 0 {
			return fmt.Errorf("%w: page %d has drawings of this editor: write them first", ErrRedaction, ep.index+1)
		}
		patch, ids, err := e.redactPage(&doc.Objects, ep.index, ep.redacts, gone)
		if err != nil {
			return err
		}
		doc.Patches = append(doc.Patches, patch)
		redacted[ep.index] = true
		if len(ids) > 0 {
			touched[ep.index] = ids
		}
	}

	// An image that was emptied, a form that was rewritten: the pages that
	// were redacted do not name them any more. Other pages may, in
	// resources they share with them, without drawing them: there they go
	// too, or the file would keep them as they were. A page that draws
	// one keeps it; it shows it.
	if len(gone) > 0 {
		for i := range d.NumPages() {
			if redacted[i] {
				continue
			}
			pd, err := d.r.Page(i + 1)
			if err != nil {
				continue
			}
			res := d.dict(pd.Get("Resources"))
			xo := d.dict(res.Get("XObject"))
			var lost []pdf.Name
			for k, v := range xo.All() {
				if ref, ok := v.Ref(); ok && gone[ref] {
					lost = append(lost, k)
				}
			}
			if len(lost) == 0 {
				continue
			}
			ref, ok := d.r.PageRef(i + 1)
			dec, derr := d.r.PageContents(i + 1)
			if !ok || derr != nil {
				continue
			}
			var sc content.Scanner
			sc.Reset(dec.Data)
			for {
				op, ok := sc.Next()
				if !ok {
					break
				}
				if string(op) == "Do" {
					lost = slices.DeleteFunc(lost, func(n pdf.Name) bool { return n == doName(&sc) })
				}
			}
			if len(lost) == 0 {
				continue
			}
			var entries, out []pdf.Entry
			for k, v := range xo.All() {
				if !slices.Contains(lost, k) {
					entries = append(entries, pdf.Entry{Key: k, Val: v})
				}
			}
			for k, v := range res.All() {
				if k != "XObject" {
					out = append(out, pdf.Entry{Key: k, Val: v})
				}
			}
			out = append(out, pdf.Entry{Key: "XObject", Val: pdf.NewDict(entries...).Object()})
			doc.Patches = append(doc.Patches, pdfedit.Patch{Ref: ref, Set: []pdf.Entry{{Key: "Resources", Val: pdf.NewDict(out...).Object()}}})
		}
	}
	if len(touched) == 0 {
		return nil
	}

	// The structure elements the content belonged to, and those above
	// them: what they say in its place goes.
	cat, err := d.r.Catalog()
	if err != nil {
		return nil
	}
	root := d.dict(cat.Get("StructTreeRoot"))
	if root.IsZero() {
		return nil
	}
	strip := map[pdf.Ref]bool{}
	seen := map[pdf.Ref]bool{}
	nodes := 0
	var walk func(o pdf.Object, page int, above []pdf.Ref, depth int) bool
	walk = func(o pdf.Object, page int, above []pdf.Ref, depth int) bool {
		if nodes++; nodes > maxStructNodes || depth > maxStructDepth {
			return false
		}
		hit := func(page, id int) {
			if touched[page][id] {
				for _, r := range above {
					strip[r] = true
				}
			}
		}
		ref, isRef := o.Ref()
		if isRef {
			if seen[ref] {
				return true
			}
			seen[ref] = true
		}
		o = d.resolve(o)
		if v, ok := o.Int(); ok {
			hit(page, mcid(float64(v)))
			return true
		}
		if a, ok := o.Array(); ok {
			for _, k := range a {
				if !walk(k, page, above, depth) {
					return false
				}
			}
			return true
		}
		el, ok := o.Dict()
		if !ok {
			return true
		}
		if r, ok := el.Get("Pg").Ref(); ok {
			page = d.pageIndex(r)
		}
		switch t, _ := d.name(el.Get("Type")); t {
		case "MCR":
			if v, ok := d.num(el.Get("MCID")); ok {
				hit(page, mcid(v))
			}
		case "OBJR":
		default:
			if isRef {
				above = append(slices.Clone(above), ref)
			}
			return walk(el.Get("K"), page, above, depth+1)
		}
		return true
	}
	if !walk(root.Get("K"), -1, nil, 0) {
		return fmt.Errorf("%w: the structure tree is larger than is read", ErrRedaction)
	}
	refs := make([]pdf.Ref, 0, len(strip))
	for r := range strip {
		refs = append(refs, r)
	}
	slices.SortFunc(refs, func(a, b pdf.Ref) int { return int(a.Num - b.Num) })
	for _, r := range refs {
		el := d.dict(r.Object())
		if el.Has("ActualText") || el.Has("Alt") || el.Has("E") {
			doc.Patches = append(doc.Patches, pdfedit.Patch{Ref: r, Del: []pdf.Name{"ActualText", "Alt", "E"}})
		}
	}
	return nil
}
