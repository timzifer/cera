package pdfedit

import (
	"errors"
	"fmt"
	"io"

	"github.com/timzifer/cera/internal/pdf"
)

// Source is a PDF file pages are imported from. A source may be imported
// from many times, into one document or several: within one document,
// what its pages share (fonts, images) is copied once.
type Source struct {
	d *pdf.Document
}

// OpenSource reads a PDF file. An encrypted file must open with the empty
// password, or the error is [ErrEncrypted].
func OpenSource(data []byte) (*Source, error) {
	d, err := pdf.Open(data)
	if err != nil {
		if errors.Is(err, pdf.ErrWrongPassword) || errors.Is(err, pdf.ErrUnsupportedEncryption) {
			return nil, fmt.Errorf("%w: %v", ErrEncrypted, err)
		}
		return nil, fmt.Errorf("pdfedit: %w", err)
	}
	return &Source{d: d}, nil
}

// NumPages returns the number of pages of the source.
func (s *Source) NumPages() int { return s.d.PageCount() }

// mayAssemble returns [ErrEncrypted] when the file's permissions do not
// allow reassembling it.
func (s *Source) mayAssemble() error {
	if prot, ok := s.d.Protection(); ok && !prot.Owner && prot.Permissions&pdf.PermAssemble == 0 {
		return ErrEncrypted
	}
	return nil
}

// Document is a PDF file being put together. It records what goes into
// the file; [Document.Save] writes it and may be called more than once.
type Document struct {
	base  *Source // the file the document was opened from; nil for New
	pages []docPage
}

// docPage is one page of the document: page index of src, turned by
// rotate more degrees.
type docPage struct {
	src    *Source
	index  int
	rotate int
}

// New returns an empty document.
func New() *Document { return &Document{} }

// Open returns a document based on a PDF file: its pages, and everything
// around them the file has (see the package documentation).
func Open(data []byte) (*Document, error) {
	s, err := OpenSource(data)
	if err != nil {
		return nil, err
	}
	doc := &Document{base: s}
	for i := range s.NumPages() {
		doc.pages = append(doc.pages, docPage{src: s, index: i})
	}
	return doc, nil
}

// NumPages returns the number of pages of the document.
func (doc *Document) NumPages() int { return len(doc.pages) }

// ImportPages appends the given pages of src to the document, in that
// order (a page may repeat). Nothing is appended when it fails.
func (doc *Document) ImportPages(src *Source, pages []Page) error {
	if err := src.mayAssemble(); err != nil {
		return err
	}
	n := src.NumPages()
	for i, p := range pages {
		if p.Rotate%90 != 0 {
			return fmt.Errorf("pdfedit: page %d of the selection: rotation %d is not a multiple of 90", i, p.Rotate)
		}
		if p.Index < 0 || p.Index >= n {
			return fmt.Errorf("pdfedit: page %d of the selection: index %d is out of range [0, %d)", i, p.Index, n)
		}
	}
	for _, p := range pages {
		doc.pages = append(doc.pages, docPage{src: src, index: p.Index, rotate: p.Rotate})
	}
	return nil
}

// SaveMode tells how [Document.Save] writes a document.
type SaveMode uint8

const (
	// Rewrite writes the whole document as a new file.
	Rewrite SaveMode = iota
)

// SaveOptions are the options of [Document.Save].
type SaveOptions struct {
	Mode SaveMode
}

// Save writes the document to w. An encrypted file is written decrypted
// when its permissions allow reassembling it. Nothing is written when Save
// fails, but for an error of w itself.
func (doc *Document) Save(w io.Writer, opt SaveOptions) error {
	if opt.Mode != Rewrite {
		return fmt.Errorf("pdfedit: unknown save mode %d", opt.Mode)
	}
	if len(doc.pages) == 0 {
		return errors.New("pdfedit: the document has no pages")
	}
	if doc.base != nil {
		if err := doc.base.mayAssemble(); err != nil {
			return err
		}
	}
	b := newBuilder(doc)
	if err := b.build(); err != nil {
		return err
	}
	return b.write(w)
}
