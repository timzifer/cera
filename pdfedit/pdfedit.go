// Package pdfedit builds new PDF files from the pages of existing ones.
//
// [Extract] copies pages of a PDF into a new file, in any order and with
// repeats, and can turn them by multiples of 90 degrees. It reads the file
// with cera's own parser, so it accepts what cera renders: classic
// cross-reference tables and cross-reference streams, object streams,
// damaged files that need a repair, and encrypted files that open with the
// empty password.
//
// The pages keep their content as it is: every object a selected page
// refers to is copied once, byte for byte, and streams keep their filters.
// What a page takes from its ancestors in the page tree (/Resources,
// /MediaBox, /CropBox, /Rotate) is written onto the page itself.
//
// What cannot survive without the rest of the document is left out:
//
//   - the outline, the structure tree, the /Dests and the rest of the
//     /Names of the catalogue, page labels, article threads (/B), page
//     thumbnails (/Thumb, viewers make their own) and the document's XMP
//     metadata, which may claim a conformance the new file no longer has;
//   - the interactive form (/AcroForm): widget annotations stay on their
//     pages as plain annotations and still show their appearance streams,
//     but they are no longer fields;
//   - link annotations whose destination is a page that is not copied;
//     links to pages that are copied are pointed at the copy (the first
//     one, for a page that is copied more than once), and named
//     destinations become explicit ones;
//   - /P and /StructParent of annotations and /StructParents of pages.
//
// The optional content properties (/OCProperties) are kept, so layers
// hidden in the source stay hidden. The document information dictionary
// (/Info) is kept as well.
//
// The new file is a PDF 1.7 file with a classic cross-reference table and
// is never encrypted.
package pdfedit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/timzifer/cera/internal/pdf"
)

// ErrEncrypted is returned for an encrypted file that does not open with
// the empty password, that uses a security handler cera cannot read, or
// whose permissions do not allow assembling the document (bit 11 of /P).
var ErrEncrypted = errors.New("pdfedit: the file is encrypted and may not be reassembled")

// Page selects a source page for the output.
type Page struct {
	Index  int // 0-based page index in the source
	Rotate int // extra clockwise rotation in degrees, a multiple of 90 (added to the page's /Rotate)
}

// Extract writes a PDF to w that contains the given pages of src, in that
// order (a page may repeat). An encrypted src that may be reassembled is
// written decrypted. Nothing is written when Extract fails.
func Extract(w io.Writer, src []byte, pages []Page) error {
	if len(pages) == 0 {
		return errors.New("pdfedit: no pages selected")
	}
	for i, p := range pages {
		if p.Rotate%90 != 0 {
			return fmt.Errorf("pdfedit: page %d of the selection: rotation %d is not a multiple of 90", i, p.Rotate)
		}
	}
	d, err := pdf.Open(src)
	if err != nil {
		if errors.Is(err, pdf.ErrWrongPassword) || errors.Is(err, pdf.ErrUnsupportedEncryption) {
			return fmt.Errorf("%w: %v", ErrEncrypted, err)
		}
		return fmt.Errorf("pdfedit: %w", err)
	}
	if prot, ok := d.Protection(); ok && !prot.Owner && prot.Permissions&pdf.PermAssemble == 0 {
		return ErrEncrypted
	}
	n := d.PageCount()
	for i, p := range pages {
		if p.Index < 0 || p.Index >= n {
			return fmt.Errorf("pdfedit: page %d of the selection: index %d is out of range [0, %d)", i, p.Index, n)
		}
	}
	x := newExtractor(d)
	if err := x.build(pages); err != nil {
		return err
	}
	_, err = w.Write(x.write())
	return err
}

// Output objects 1 and 2 are the catalogue and the page tree; the pages
// follow from 3 on.
const (
	catalogNum = 1
	pagesNum   = 2
)

// outRef is a reference to object num of the output. Source references are
// positive, so the objects Extract makes itself refer to each other with
// negative numbers, which the source never resolves.
func outRef(num int32) pdf.Object { return pdf.Ref{Num: -num}.Object() }

// An extractor copies pages of one document.
type extractor struct {
	d *pdf.Document

	// objs are the output objects by number less one: objects Extract made
	// (with source and output references) or source objects to copy.
	objs []pdf.Object
	// copied maps source object numbers to output numbers.
	copied map[int32]int32
	// isPage holds the object numbers of every source page, pageOut the
	// output number of the first copy of each selected one.
	isPage  map[int32]bool
	pageOut map[int32]int32
	// skip holds source objects never copied: the /Encrypt dictionary.
	skip map[int32]bool

	infoNum int32
	buf     bytes.Buffer
}

func newExtractor(d *pdf.Document) *extractor {
	x := &extractor{
		d:       d,
		copied:  map[int32]int32{},
		isPage:  map[int32]bool{},
		pageOut: map[int32]int32{},
		skip:    map[int32]bool{},
	}
	for i := range d.PageCount() {
		if r, ok := d.PageRef(i + 1); ok {
			x.isPage[r.Num] = true
		}
	}
	if r, ok := d.Trailer().Get("Encrypt").Ref(); ok {
		x.skip[r.Num] = true
	}
	return x
}

// alloc adds an output object and returns its number.
func (x *extractor) alloc(o pdf.Object) int32 {
	x.objs = append(x.objs, o)
	return int32(len(x.objs))
}

// build makes the catalogue, the page tree and the pages.
func (x *extractor) build(pages []Page) error {
	cat, err := x.d.Catalog()
	if err != nil {
		return fmt.Errorf("pdfedit: %w", err)
	}
	x.alloc(pdf.Null) // the catalogue, filled in below
	x.alloc(pdf.Null) // the page tree
	first := int32(len(x.objs) + 1)
	kids := make(pdf.Array, len(pages))
	for i, p := range pages {
		num := x.alloc(pdf.Null)
		kids[i] = outRef(num)
		r, _ := x.d.PageRef(p.Index + 1)
		if _, ok := x.pageOut[r.Num]; !ok {
			x.pageOut[r.Num] = num
		}
	}
	// Pages are made once every selected page has its number, so links
	// between them can point at the copies.
	for i, p := range pages {
		dict, err := x.page(p)
		if err != nil {
			return err
		}
		x.objs[first+int32(i)-1] = dict.Object()
	}

	x.objs[pagesNum-1] = pdf.NewDict(
		pdf.Entry{Key: "Type", Val: pdf.Name("Pages").Object()},
		pdf.Entry{Key: "Kids", Val: kids.Object()},
		pdf.Entry{Key: "Count", Val: pdf.Integer(int64(len(pages)))},
	).Object()

	entries := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("Catalog").Object()},
		{Key: "Pages", Val: outRef(pagesNum)},
	}
	for _, k := range [...]pdf.Name{"OCProperties", "Lang"} {
		if v := cat.Get(k); !v.IsNull() {
			entries = append(entries, pdf.Entry{Key: k, Val: v})
		}
	}
	x.objs[catalogNum-1] = pdf.NewDict(entries...).Object()

	// The information dictionary is numbered now: the trailer is written
	// after the objects.
	info := x.d.Trailer().Get("Info")
	if r, ok := info.Ref(); ok {
		x.infoNum = x.ref(r)
	} else if dict, ok := info.Dict(); ok && info.Kind() == pdf.KindDict {
		x.infoNum = x.alloc(dict.Object())
	}
	return nil
}

// Page attributes that are not copied: the page tree is new, the rotation
// and the annotations are rewritten, and beads, thumbnails and the
// structure tree's key are dropped.
var pageDropped = map[pdf.Name]bool{
	"Type": true, "Parent": true, "Rotate": true, "Annots": true,
	"B": true, "Thumb": true, "StructParents": true,
}

// page makes the dictionary of one output page.
func (x *extractor) page(p Page) (pdf.Dict, error) {
	src, err := x.d.Page(p.Index + 1) // with the inherited attributes
	if err != nil {
		return pdf.Dict{}, fmt.Errorf("pdfedit: page %d: %w", p.Index, err)
	}
	entries := []pdf.Entry{
		{Key: "Type", Val: pdf.Name("Page").Object()},
		{Key: "Parent", Val: outRef(pagesNum)},
	}
	for k, v := range src.All() {
		if !pageDropped[k] {
			entries = append(entries, pdf.Entry{Key: k, Val: v})
		}
	}
	if rot := rotation(x.d.Resolve(src.Get("Rotate")), p.Rotate); rot != 0 {
		entries = append(entries, pdf.Entry{Key: "Rotate", Val: pdf.Integer(int64(rot))})
	}
	if annots := x.annots(src.Get("Annots")); len(annots) > 0 {
		entries = append(entries, pdf.Entry{Key: "Annots", Val: annots.Object()})
	}
	return pdf.NewDict(entries...), nil
}

// rotation adds extra degrees to a page's /Rotate, read the way cera reads
// it, and returns 0, 90, 180 or 270.
func rotation(o pdf.Object, extra int) int {
	q := 0
	if v, ok := o.Float(); ok && !math.IsNaN(v) && !math.IsInf(v, 0) {
		q = int(math.Mod(math.Round(v/90), 4))
	}
	q += extra / 90
	return (q%4 + 4) % 4 * 90
}

// annots copies the annotations of a page that still make sense alone.
// Each copy of a page gets copies of its annotations, since an annotation
// belongs to one page.
func (x *extractor) annots(o pdf.Object) pdf.Array {
	arr, _ := x.d.Resolve(o).Array()
	type kept struct {
		dict pdf.Dict
		num  int32 // output number; 0 for an annotation written in place
	}
	var keep []kept
	local := map[int32]int32{} // source annotation → its copy on this page
	for _, e := range arr {
		ad, ok := x.d.Resolve(e).Dict()
		if !ok || x.d.Resolve(e).Kind() == pdf.KindStream {
			continue
		}
		ad, ok = x.link(ad)
		if !ok {
			continue
		}
		k := kept{dict: ad}
		if r, ok := e.Ref(); ok {
			if _, dup := local[r.Num]; dup {
				continue
			}
			k.num = x.alloc(pdf.Null)
			local[r.Num] = k.num
		}
		keep = append(keep, k)
	}
	out := make(pdf.Array, 0, len(keep))
	for _, k := range keep {
		var entries []pdf.Entry
		for key, v := range k.dict.All() {
			switch key {
			case "P", "StructParent":
				continue
			case "Parent", "Popup", "IRT":
				// Only references between annotations of this page
				// survive.
				r, ok := v.Ref()
				if !ok {
					continue
				}
				n, ok := local[r.Num]
				if !ok {
					continue
				}
				v = outRef(n)
			}
			entries = append(entries, pdf.Entry{Key: key, Val: v})
		}
		dict := pdf.NewDict(entries...)
		if k.num == 0 {
			out = append(out, dict.Object())
			continue
		}
		x.objs[k.num-1] = dict.Object()
		out = append(out, outRef(k.num))
	}
	return out
}

// link checks the destination of a link annotation. A link into the
// document stays only when it goes to a copied page; its destination is
// then written explicitly. Other annotations pass unchanged.
func (x *extractor) link(ad pdf.Dict) (pdf.Dict, bool) {
	if sub, _ := x.d.Resolve(ad.Get("Subtype")).Name(); sub != "Link" {
		return ad, true
	}
	var dest pdf.Object
	if v, ok := ad.Lookup("Dest"); ok {
		dest = v
	} else {
		act, _ := x.d.Resolve(ad.Get("A")).Dict()
		if s, _ := x.d.Resolve(act.Get("S")).Name(); s != "GoTo" {
			return ad, true // a URI, a remote file, JavaScript, ...
		}
		dest = act.Get("D")
	}
	a := x.explicit(dest)
	if len(a) == 0 {
		return ad, false
	}
	r, ok := a[0].Ref()
	if !ok {
		return ad, false
	}
	if _, ok := x.pageOut[r.Num]; !ok {
		return ad, false
	}
	var entries []pdf.Entry
	for k, v := range ad.All() {
		if k != "A" && k != "Dest" {
			entries = append(entries, pdf.Entry{Key: k, Val: v})
		}
	}
	entries = append(entries, pdf.Entry{Key: "Dest", Val: a.Object()})
	return pdf.NewDict(entries...), true
}

// explicit resolves a destination to its array: an explicit destination,
// or a named one looked up in the catalogue's /Dests and in the /Dests
// name tree.
func (x *extractor) explicit(o pdf.Object) pdf.Array {
	o = x.d.Resolve(o)
	var key []byte
	switch o.Kind() {
	case pdf.KindArray:
		a, _ := o.Array()
		return a
	case pdf.KindName:
		n, _ := o.Name()
		key = []byte(n)
	case pdf.KindString:
		key, _ = o.Str()
	default:
		return nil
	}
	cat, err := x.d.Catalog()
	if err != nil {
		return nil
	}
	v := pdf.Null
	if dests, ok := x.d.GetDict(cat, "Dests"); ok {
		v = dests.Get(pdf.Name(key))
	}
	if v.IsNull() {
		names, _ := x.d.GetDict(cat, "Names")
		v = x.nameTree(names.Get("Dests"), key)
	}
	v = x.d.Resolve(v)
	if dd, ok := v.Dict(); ok {
		v = x.d.Resolve(dd.Get("D"))
	}
	a, _ := v.Array()
	return a
}

// maxNameTreeNodes bounds a name tree search.
const maxNameTreeNodes = 1 << 14

// nameTree finds key in the name tree rooted at root.
func (x *extractor) nameTree(root pdf.Object, key []byte) pdf.Object {
	stack := []pdf.Object{root}
	seen := map[int32]bool{}
	for n := 0; len(stack) > 0 && n < maxNameTreeNodes; n++ {
		o := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if r, ok := o.Ref(); ok {
			if seen[r.Num] {
				continue
			}
			seen[r.Num] = true
		}
		node, ok := x.d.Resolve(o).Dict()
		if !ok {
			continue
		}
		names, _ := x.d.Resolve(node.Get("Names")).Array()
		for i := 0; i+1 < len(names); i += 2 {
			if k, ok := x.d.Resolve(names[i]).Str(); ok && bytes.Equal(k, key) {
				return names[i+1]
			}
		}
		kids, _ := x.d.Resolve(node.Get("Kids")).Array()
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, kids[i])
		}
	}
	return pdf.Null
}
