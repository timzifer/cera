// Package pdfedit builds new PDF files from the pages of existing ones.
//
// A [Document] is what is written: an empty one ([New]) or one based on a
// file ([Open]), to which pages of other files ([Source]) are added with
// [Document.ImportPages]. [Document.Save] writes it. [Extract] is the
// short form for copying pages of one file into a new one.
//
// Files are read with cera's own parser, so whatever cera renders is
// accepted: classic cross-reference tables and cross-reference streams,
// object streams, damaged files that need a repair, and encrypted files
// that open with the empty password.
//
// Pages keep their content as it is: every object a page refers to is
// copied once per source, byte for byte, and streams keep their filters.
// What a page takes from its ancestors in the page tree (/Resources,
// /MediaBox, /CropBox, /Rotate) is written onto the page itself.
//
// A document opened from a file keeps its pages with everything around
// them: the outline, the structure tree, the names, the interactive form
// and the rest of the catalogue. Imported pages come alone, and what
// cannot survive without the rest of their document is left out:
//
//   - the outline, the structure tree, the /Dests and the rest of the
//     /Names of the catalogue, page labels, article threads (/B), page
//     thumbnails (/Thumb, viewers make their own) and the document's XMP
//     metadata, which may claim a conformance the new file no longer has;
//   - the interactive form (/AcroForm): widget annotations stay on their
//     pages as plain annotations and still show their appearance streams,
//     but they are no longer fields;
//   - link annotations whose destination is a page that is not imported;
//     links to pages that are imported from the same source are pointed
//     at the copy (the first one, for a page imported more than once),
//     and named destinations become explicit ones;
//   - /P and /StructParent of annotations and /StructParents of pages.
//
// The optional content properties (/OCProperties) of every source are
// kept, merged when there are several, so layers hidden in a source stay
// hidden. The document information dictionary (/Info) is the one of the
// opened file, or of the first source pages are imported from.
//
// The new file has a classic cross-reference table and is never
// encrypted; its version is that of the newest source, at least 1.7.
package pdfedit

import (
	"errors"
	"io"
)

// ErrEncrypted is returned for an encrypted file that does not open with
// the empty password, that uses a security handler cera cannot read, or
// whose permissions do not allow assembling the document (bit 11 of /P).
var ErrEncrypted = errors.New("pdfedit: the file is encrypted and may not be reassembled")

// Page selects a source page.
type Page struct {
	Index  int // 0-based page index in the source
	Rotate int // extra clockwise rotation in degrees, a multiple of 90 (added to the page's /Rotate)
}

// Extract writes a PDF to w that contains the given pages of src, in that
// order (a page may repeat). An encrypted src that may be reassembled is
// written decrypted. Nothing is written when Extract fails, but for an
// error of w itself.
func Extract(w io.Writer, src []byte, pages []Page) error {
	if len(pages) == 0 {
		return errors.New("pdfedit: no pages selected")
	}
	s, err := OpenSource(src)
	if err != nil {
		return err
	}
	doc := New()
	if err := doc.ImportPages(s, pages); err != nil {
		return err
	}
	return doc.Save(w, SaveOptions{})
}
