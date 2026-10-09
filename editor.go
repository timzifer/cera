package cera

import (
	"errors"
	"fmt"
	"io"

	"github.com/timzifer/cera/internal/pdfedit"
)

// Editing documents (ADR 0013). An Editor records changes to a document,
// or puts a new one together, and writes the result as a new file.
//
// Everything at the level of the document — the outline, the names, the
// structure tree, the interactive form, the layers, page labels and
// metadata — belongs to the document being edited. Pages imported from
// another document bring only what is on the page: their content and
// resources, their annotations, and links to other pages imported from
// the same document; their widgets stay as plain annotations, no longer
// fields. Importing a page of the edited document itself makes such a
// copy too. Layers of imported pages are added to the document's, so
// layers hidden in the source stay hidden.
//
// Deleting a page removes what hangs on it alone: its annotations, and
// form fields whose widgets were all on deleted pages. Outline entries,
// links and named destinations that pointed at it lose their target.
//
// Pages keep their content as it is: every object a page refers to is
// copied once per source document, byte for byte, and streams keep their
// filters. What a page takes from its ancestors in the page tree
// (/Resources, /MediaBox, /CropBox, /Rotate) is written onto the page
// itself.

// ErrNoAssembly is returned for an encrypted document whose permissions
// do not allow reassembling it (bit 11 of /P): it may not be saved as a
// new file, and its pages may not be imported.
var ErrNoAssembly = errors.New("cera: the document's permissions do not allow reassembling it")

// EditPage selects a page to import.
type EditPage struct {
	Index  int // 0-based page index in the source document
	Rotate int // extra clockwise rotation in degrees, a multiple of 90 (added to the page's /Rotate)
}

// Editor records the pages of a document being edited or put together.
// It is not safe for concurrent use, but the documents it uses may be
// rendered while it is. Its methods change nothing when they fail.
type Editor struct {
	base  *Document // the edited document; nil for NewEditor
	pages []editPage
}

// editPage is one page of the result: page index of src, turned by rotate
// more degrees. An own page is a page of the edited document.
type editPage struct {
	src    *Document
	index  int
	rotate int
	own    bool
}

// Edit returns an editor of d with all its pages.
func (d *Document) Edit() *Editor {
	e := &Editor{base: d}
	for i := range d.NumPages() {
		e.pages = append(e.pages, editPage{src: d, index: i, own: true})
	}
	return e
}

// NewEditor returns an editor of a new, empty document.
func NewEditor() *Editor { return &Editor{} }

// NumPages returns the number of pages of the result.
func (e *Editor) NumPages() int { return len(e.pages) }

// ImportPages inserts the given pages of src before page at (NumPages to
// append), in that order; a page may repeat.
func (e *Editor) ImportPages(at int, src *Document, pages ...EditPage) error {
	if at < 0 || at > len(e.pages) {
		return fmt.Errorf("cera: insert position %d is out of range [0, %d]", at, len(e.pages))
	}
	if !pdfedit.MayAssemble(src.r) {
		return ErrNoAssembly
	}
	n := src.NumPages()
	add := make([]editPage, len(pages))
	for i, p := range pages {
		if p.Rotate%90 != 0 {
			return fmt.Errorf("cera: page %d of the selection: rotation %d is not a multiple of 90", i, p.Rotate)
		}
		if p.Index < 0 || p.Index >= n {
			return fmt.Errorf("cera: page %d of the selection: index %d is out of range [0, %d)", i, p.Index, n)
		}
		add[i] = editPage{src: src, index: p.Index, rotate: p.Rotate}
	}
	e.pages = append(e.pages[:at], append(add, e.pages[at:]...)...)
	return nil
}

// DeletePages removes the pages at the given indices of the result.
func (e *Editor) DeletePages(indices ...int) error {
	del := map[int]bool{}
	for _, i := range indices {
		if err := e.check(i); err != nil {
			return err
		}
		del[i] = true
	}
	kept := e.pages[:0:0]
	for i, p := range e.pages {
		if !del[i] {
			kept = append(kept, p)
		}
	}
	e.pages = kept
	return nil
}

// MovePage moves the page at index from so that it is at index to.
func (e *Editor) MovePage(from, to int) error {
	if err := e.check(from); err != nil {
		return err
	}
	if err := e.check(to); err != nil {
		return err
	}
	p := e.pages[from]
	e.pages = append(e.pages[:from], e.pages[from+1:]...)
	e.pages = append(e.pages[:to], append([]editPage{p}, e.pages[to:]...)...)
	return nil
}

// RotatePage turns the page at index i clockwise by degrees, a multiple
// of 90.
func (e *Editor) RotatePage(i, degrees int) error {
	if err := e.check(i); err != nil {
		return err
	}
	if degrees%90 != 0 {
		return fmt.Errorf("cera: rotation %d is not a multiple of 90", degrees)
	}
	e.pages[i].rotate += degrees
	return nil
}

func (e *Editor) check(i int) error {
	if i < 0 || i >= len(e.pages) {
		return fmt.Errorf("cera: page index %d is out of range [0, %d)", i, len(e.pages))
	}
	return nil
}

// Save writes the result to w as a new file, which is never encrypted: an
// encrypted document is written decrypted when its permissions allow
// reassembling it. The editor may be saved again. Nothing is written when
// Save fails, but for an error of w itself.
func (e *Editor) Save(w io.Writer) (err error) {
	defer recoverPanic(&err)
	doc := pdfedit.Doc{Pages: make([]pdfedit.Page, len(e.pages))}
	if e.base != nil {
		doc.Base = e.base.r
		if !pdfedit.MayAssemble(doc.Base) {
			return ErrNoAssembly
		}
	}
	for i, p := range e.pages {
		doc.Pages[i] = pdfedit.Page{Src: p.src.r, Index: p.index, Rotate: p.rotate, Own: p.own}
	}
	if err := pdfedit.Write(w, doc); err != nil {
		return fmt.Errorf("cera: %w", err)
	}
	return nil
}
