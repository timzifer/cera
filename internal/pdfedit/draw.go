package pdfedit

import (
	"fmt"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// What an editor makes itself: blank pages, and drawings on pages. A
// drawing is a form XObject with resources of its own, so that a page it
// is painted on gets one name more in its resources and nothing else, and
// its own content is neither read nor rewritten: that goes between q and Q,
// the drawings before it or after it.

// Draw is one drawing on a page of the output: a form XObject among
// Doc.Objects, whose /Matrix takes what it draws to the default user space
// of the page, painted under what the page has or over it.
type Draw struct {
	Form  pdf.Ref
	Under bool
}

// drawn returns the page dictionary page with the drawings painted on it.
// The dictionary is in the space resolve reads; the drawings are new
// objects.
func (b *builder) drawn(page pdf.Dict, resolve func(pdf.Object) pdf.Object, draws []Draw) pdf.Dict {
	res, _ := resolve(page.Get("Resources")).Dict()
	xo, _ := resolve(res.Get("XObject")).Dict()
	var under, over []byte
	var entries []pdf.Entry
	for k, v := range xo.All() {
		entries = append(entries, pdf.Entry{Key: k, Val: v})
	}
	n := 0
	for _, d := range draws {
		var name pdf.Name
		for {
			name = pdf.Name(fmt.Sprintf("CeraDraw%d", n))
			n++
			if !xo.Has(name) {
				break
			}
		}
		entries = append(entries, pdf.Entry{Key: name, Val: d.Form.Object()})
		op := "q /" + string(name) + " Do Q\n"
		if d.Under {
			under = append(under, op...)
		} else {
			over = append(over, op...)
		}
	}

	var own pdf.Array
	switch c := resolve(page.Get("Contents")); c.Kind() {
	case pdf.KindArray:
		own, _ = c.Array()
	case pdf.KindStream:
		own = pdf.Array{page.Get("Contents")}
	}
	var contents pdf.Array
	if len(own) == 0 {
		contents = pdf.Array{b.addStream(append(under, over...))}
	} else {
		// The page's own content goes between q and Q, so that what it
		// leaves in the graphics state does not move what is drawn over
		// it.
		contents = append(contents, b.addStream(append(under, "q\n"...)))
		contents = append(contents, own...)
		contents = append(contents, b.addStream(append([]byte("Q\n"), over...)))
	}

	resEntries := []pdf.Entry{{Key: "XObject", Val: pdf.NewDict(entries...).Object()}}
	for k, v := range res.All() {
		if k != "XObject" {
			resEntries = append(resEntries, pdf.Entry{Key: k, Val: v})
		}
	}
	return page.With("Contents", contents.Object()).With("Resources", pdf.NewDict(resEntries...).Object())
}

// addStream adds a content stream of data to the new objects and returns
// the reference to it.
func (b *builder) addStream(data []byte) pdf.Object {
	b.doc.Objects = append(b.doc.Objects, pdf.NewStream(pdf.NewDict(), data).Object())
	return NewRef(len(b.doc.Objects) - 1).Object()
}

// blank returns a page made by the editor: of the size asked for, with
// what is drawn on it.
func (b *builder) blank(p Page) pdf.Object {
	page := pdf.NewDict(
		pdf.Entry{Key: "MediaBox", Val: pdf.Array{pdf.Integer(0), pdf.Integer(0), pdf.Real(p.Width), pdf.Real(p.Height)}.Object()},
		pdf.Entry{Key: "Resources", Val: pdf.NewDict().Object()},
	)
	if len(p.Draws) > 0 {
		page = b.drawn(page, func(o pdf.Object) pdf.Object { return o }, p.Draws)
	}
	if p.Geo != nil {
		page = b.geometry(page, nil, p.Geo)
	}
	mapped, _ := b.mapOwn(page.Object()).Dict()
	entries := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("Page").Object()},
		{Key: "Parent", Val: b.pages.Object()},
	}
	for k, v := range mapped.All() {
		entries = append(entries, pdf.Entry{Key: k, Val: v})
	}
	if rot := Rotation(pdf.Null, p.Rotate); rot != 0 {
		entries = append(entries, pdf.Entry{Key: "Rotate", Val: pdf.Integer(int64(rot))})
	}
	return pdf.NewDict(entries...).Object()
}

// newObject returns the output number of new object i, numbering it when
// it is first referred to: new objects nothing refers to are not written.
func (b *builder) newObject(i int32) pdfwrite.Ref {
	objs := b.doc.Objects
	if i < 0 || int(i) >= len(objs) {
		return 0
	}
	if n, ok := b.newOut[i]; ok {
		return n
	}
	n := b.w.Alloc()
	b.newOut[i] = n
	if d := b.doc.ObjectSrc[int(i)]; d != nil {
		b.w.Set(n, b.bySrc[srcKey{d, false}].im.Map(objs[i]))
	} else {
		b.w.Set(n, b.mapOwn(objs[i]))
	}
	return n
}

// mapOwn returns a value of the edited document's space, where the new
// objects are, in the output's.
func (b *builder) mapOwn(o pdf.Object) pdf.Object {
	if x := b.own(); x != nil {
		return x.im.Map(o)
	}
	return b.mapFree(o, 0)
}

// mapFree is mapOwn for a new document: there is no document whose
// objects a value could name, only the new ones.
func (b *builder) mapFree(o pdf.Object, depth int) pdf.Object {
	if depth > maxFreeDepth {
		return pdf.Null
	}
	switch o.Kind() {
	case pdf.KindRef:
		if r, _ := o.Ref(); r.Gen == newGen {
			if n := b.newObject(r.Num); n > 0 {
				return n.Object()
			}
		}
		return pdf.Null
	case pdf.KindArray:
		a, _ := o.Array()
		out := make(pdf.Array, len(a))
		for i, e := range a {
			out[i] = b.mapFree(e, depth+1)
		}
		return out.Object()
	case pdf.KindDict:
		d, _ := o.Dict()
		return b.mapFreeDict(d, depth).Object()
	case pdf.KindStream:
		s, _ := o.Stream()
		data, _ := s.Unencrypted()
		return pdf.NewStream(b.mapFreeDict(s.Dict, depth), data).Object()
	}
	return o
}

func (b *builder) mapFreeDict(d pdf.Dict, depth int) pdf.Dict {
	entries := make([]pdf.Entry, 0, d.Len())
	for k, v := range d.All() {
		entries = append(entries, pdf.Entry{Key: k, Val: b.mapFree(v, depth+1)})
	}
	return pdf.NewDict(entries...)
}

// maxFreeDepth bounds how deeply new objects nest.
const maxFreeDepth = 64

// own returns the edited document, nil when a new one is made.
func (b *builder) own() *source {
	if b.doc.Base == nil {
		return nil
	}
	return b.bySrc[srcKey{b.doc.Base, true}]
}
