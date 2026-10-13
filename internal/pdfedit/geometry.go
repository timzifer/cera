package pdfedit

import (
	"math"
	"slices"
	"strconv"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// Geometry changes the boxes of a page and moves what is on it.
type Geometry struct {
	// M takes the default user space the page had to the one it is
	// written with: its content, what is drawn on it and its annotations
	// move by it. Moves says that it is not the identity. Clip is the part
	// of the page, in the space it had, that is moved: what it showed.
	M     [6]float64
	Moves bool
	Clip  [4]float64
	// Boxes are written in place of the boxes the page has, by key.
	Boxes map[pdf.Name][4]float64
}

// boxKeys are the keys of the boxes of a page.
var boxKeys = [...]pdf.Name{"MediaBox", "CropBox", "BleedBox", "TrimBox", "ArtBox"}

// round4 returns v as it is written: to the ten-thousandth of a point.
func round4(v float64) pdf.Object { return pdf.Real(math.Round(v*1e4) / 1e4) }

func (g *Geometry) point(x, y float64) (float64, float64) {
	return g.M[0]*x + g.M[2]*y + g.M[4], g.M[1]*x + g.M[3]*y + g.M[5]
}

// numbers reads an array of numbers.
func numbers(o pdf.Object, resolve func(pdf.Object) pdf.Object) ([]float64, bool) {
	a, ok := resolve(o).Array()
	if !ok {
		return nil, false
	}
	out := make([]float64, len(a))
	for i, e := range a {
		v, ok := resolve(e).Float()
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, false
		}
		out[i] = v
	}
	return out, true
}

// points moves an array of points, x and y in turn.
func (g *Geometry) points(o pdf.Object, resolve func(pdf.Object) pdf.Object) (pdf.Object, bool) {
	v, ok := numbers(o, resolve)
	if !ok || len(v)%2 != 0 {
		return pdf.Null, false
	}
	out := make(pdf.Array, len(v))
	for i := 0; i < len(v); i += 2 {
		x, y := g.point(v[i], v[i+1])
		out[i], out[i+1] = round4(x), round4(y)
	}
	return out.Object(), true
}

// annot returns the annotation dictionary d moved with its page: its
// rectangle, and the points that say where it is drawn — of a markup, a
// line, a polygon, an ink annotation, a callout. Its appearance follows
// the rectangle. What is measured in the annotation's own terms stays: the
// width of a border, the size of the text of a field.
func (g *Geometry) annot(d pdf.Dict, resolve func(pdf.Object) pdf.Object) pdf.Dict {
	if g == nil || !g.Moves {
		return d
	}
	if v, ok := numbers(d.Get("Rect"), resolve); ok && len(v) == 4 {
		x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, c := range [4][2]float64{{v[0], v[1]}, {v[2], v[1]}, {v[0], v[3]}, {v[2], v[3]}} {
			x, y := g.point(c[0], c[1])
			x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x), max(y1, y)
		}
		d = d.With("Rect", pdf.Array{round4(x0), round4(y0), round4(x1), round4(y1)}.Object())
	}
	for _, k := range [...]pdf.Name{"QuadPoints", "Vertices", "L", "CL"} {
		if !d.Has(k) {
			continue
		}
		if v, ok := g.points(d.Get(k), resolve); ok {
			d = d.With(k, v)
		}
	}
	if list, ok := resolve(d.Get("InkList")).Array(); ok {
		out := make(pdf.Array, len(list))
		for i, path := range list {
			out[i] = path
			if v, ok := g.points(path, resolve); ok {
				out[i] = v
			}
		}
		d = d.With("InkList", out.Object())
	}
	// The differences between the rectangle and what is drawn in it:
	// left, top, right and bottom, or left, bottom, right and top — the
	// first and third are widths either way.
	if v, ok := numbers(d.Get("RD"), resolve); ok && len(v) == 4 {
		sx, sy := math.Hypot(g.M[0], g.M[1]), math.Hypot(g.M[2], g.M[3])
		d = d.With("RD", pdf.Array{round4(v[0] * sx), round4(v[1] * sy), round4(v[2] * sx), round4(v[3] * sy)}.Object())
	}
	return d
}

// geometry writes page, a page dictionary of source x (nil for a page
// made here), with the boxes of g and its content moved.
//
// Moved content becomes a form XObject, drawn through the matrix: a
// matrix alone in front of the content would move everything but its
// patterns, which lie in the space of the content stream they are used in
// (PDF 2.0, 8.7.3.1) — that of the page, which no operator changes, or of
// the form, which moves with it. The form is clipped to what the page
// showed, so nothing that was outside it comes into view on the new page.
// The content streams are decoded and joined for it, not read.
//
// A tagged page is the exception. The structure tree names its marked
// content by number in the content of the page, and would not find it in
// a form; so the content of a page with /StructParents stays what it is,
// between a matrix with a clip and a Q — unless it has patterns to paint
// with, which have to be where they belong before anything else.
func (b *builder) geometry(page pdf.Dict, x *source, g *Geometry) pdf.Dict {
	resolve := func(o pdf.Object) pdf.Object { return o }
	if x != nil {
		resolve = x.resolve
	}
	for _, k := range boxKeys {
		if r, ok := g.Boxes[k]; ok {
			page = page.With(k, pdf.Array{round4(r[0]), round4(r[1]), round4(r[2]), round4(r[3])}.Object())
		}
	}
	if !g.Moves {
		return page
	}
	var own pdf.Array
	switch c := resolve(page.Get("Contents")); c.Kind() {
	case pdf.KindArray:
		own, _ = c.Array()
	case pdf.KindStream:
		own = pdf.Array{page.Get("Contents")}
	default:
		// Written by drawn: new objects, which a source need not see.
		if a, ok := page.Get("Contents").Array(); ok {
			own = a
		}
	}
	if len(own) == 0 {
		return page
	}
	cm := []byte("q")
	for _, v := range g.M {
		cm = append(cm, ' ')
		cm = strconv.AppendFloat(cm, math.Round(v*1e6)/1e6, 'f', -1, 64)
	}
	cm = append(cm, " cm"...)
	res, _ := resolve(page.Get("Resources")).Dict()
	if patterns, _ := resolve(res.Get("Pattern")).Dict(); page.Has("StructParents") && patterns.Len() == 0 {
		cm = append(cm, '\n')
		for _, v := range [...]float64{g.Clip[0], g.Clip[1], g.Clip[2] - g.Clip[0], g.Clip[3] - g.Clip[1]} {
			cm = strconv.AppendFloat(cm, math.Round(v*1e4)/1e4, 'f', -1, 64)
			cm = append(cm, ' ')
		}
		contents := pdf.Array{b.addStream(append(cm, "re W n\n"...))}
		contents = append(contents, own...)
		contents = append(contents, b.addStream([]byte("\nQ\n")))
		return page.With("Contents", contents.Object())
	}
	var data []byte
	for _, e := range own {
		data = append(append(data, b.contentData(x, e)...), '\n')
	}
	entries := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("XObject").Object()},
		{Key: "Subtype", Val: pdf.Name("Form").Object()},
		{Key: "BBox", Val: pdf.Array{round4(g.Clip[0]), round4(g.Clip[1]), round4(g.Clip[2]), round4(g.Clip[3])}.Object()},
		{Key: "Resources", Val: pdf.NewDict().Object()},
	}
	if r := page.Get("Resources"); !r.IsNull() {
		entries[3].Val = r
	}
	// A page that is a transparency group is one as a form too.
	if grp := page.Get("Group"); !grp.IsNull() {
		entries = append(entries, pdf.Entry{Key: "Group", Val: grp})
	}
	form := pdfwrite.Flate(pdf.NewDict(entries...), data).Object()
	var ref pdf.Object
	if x != nil {
		ref = x.newObj(form)
	} else {
		b.doc.Objects = append(b.doc.Objects, form)
		ref = NewRef(len(b.doc.Objects) - 1).Object()
	}

	// The page keeps its resources, for what is drawn on it without any
	// of its own, and gets one name more.
	xo, _ := resolve(res.Get("XObject")).Dict()
	name := pdf.Name("CeraPage")
	for n := 0; xo.Has(name); n++ {
		name = pdf.Name("CeraPage" + strconv.Itoa(n))
	}
	xos := append(slices.Clone(xo.Entries()), pdf.Entry{Key: name, Val: ref})
	resEntries := []pdf.Entry{{Key: "XObject", Val: pdf.NewDict(xos...).Object()}}
	for k, v := range res.All() {
		if k != "XObject" {
			resEntries = append(resEntries, pdf.Entry{Key: k, Val: v})
		}
	}
	cm = append(cm, " /"+string(name)+" Do Q\n"...)
	return page.With("Contents", b.addStream(cm)).With("Resources", pdf.NewDict(resEntries...).Object())
}

// contentData returns the decoded data of one content stream of a page
// of source x (nil for a page made here): an object of the source, or a
// new one. What does not decode to its end gives what it has.
func (b *builder) contentData(x *source, e pdf.Object) []byte {
	if r, ok := e.Ref(); ok && r.Gen == newGen {
		if r.Num < 0 || int(r.Num) >= len(b.doc.Objects) {
			return nil
		}
		s, ok := b.doc.Objects[r.Num].Stream()
		if !ok {
			return nil
		}
		raw, _ := s.Unencrypted()
		space := b.doc.ObjectSrc[int(r.Num)]
		if space == nil {
			space = b.doc.Base
		}
		switch {
		case s.Dict.Get("Filter").IsNull():
			return raw
		case space != nil:
			return space.DecodeBytes(s.Dict, raw).Data
		}
		return pdf.DecodeRecovering(s.Dict, raw).Data
	}
	if x == nil {
		return nil
	}
	s, ok := x.resolve(e).Stream()
	if !ok {
		return nil
	}
	if s.Ref.Num > 0 {
		return x.d.Decode(s).Data
	}
	return x.d.DecodeBytes(s.Dict, x.d.Raw(s)).Data
}

// movePages moves the annotations of the pages of the edited document
// whose geometry moves them: each once, changed where it is an object.
// Imported pages get their annotations anew, moved as they are made.
func (b *builder) movePages() {
	moved := map[pdf.Ref]bool{}
	for i, p := range b.doc.Pages {
		if p.Geo == nil || !p.Geo.Moves || p.Src == nil || !p.Own {
			continue
		}
		x := b.bySrc[srcKey{p.Src, true}]
		page, err := x.pageDict(p)
		if err != nil {
			continue
		}
		arr, _ := x.resolve(page.Get("Annots")).Array()
		if len(arr) == 0 {
			continue
		}
		out := make(pdf.Array, len(arr))
		for k, e := range arr {
			out[k] = e
			r, isRef := e.Ref()
			switch {
			case !isRef:
				if d, ok := e.Dict(); ok && e.Kind() == pdf.KindDict {
					out[k] = p.Geo.annot(d, x.resolve).Object()
				}
				continue
			case moved[r]:
				continue
			case r.Gen == newGen:
				if r.Num >= 0 && int(r.Num) < len(b.doc.Objects) && b.doc.ObjectSrc[int(r.Num)] == nil {
					if o := b.doc.Objects[r.Num]; o.Kind() == pdf.KindDict {
						d, _ := o.Dict()
						b.doc.Objects[r.Num] = p.Geo.annot(d, x.resolve).Object()
					}
				}
			default:
				if o := x.get(r.Num); o.Kind() == pdf.KindDict {
					d, _ := o.Dict()
					b.patched[r.Num] = p.Geo.annot(d, x.resolve).Object()
				}
			}
			moved[r] = true
		}
		if b.pageAnnots == nil {
			b.pageAnnots = make([]pdf.Object, len(b.doc.Pages))
		}
		b.pageAnnots[i] = out.Object()
	}
}
