// Package pdfedit builds new PDF files from the pages of existing ones.
//
// Deprecated: Editing lives in the cera package: [cera.Document.Edit] for
// a document and [cera.NewEditor] for a new one. [Extract] is a short
// form for NewEditor, ImportPages and Save.
package pdfedit

import (
	"errors"
	"fmt"
	"io"

	"github.com/timzifer/cera"
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
// order (a page may repeat), as [cera.Editor] imports pages into a new
// document. An encrypted src that may be reassembled is written decrypted.
// Nothing is written when Extract fails.
//
// Deprecated: Use [cera.NewEditor], [cera.Editor.ImportPages] and
// [cera.Editor.Save].
func Extract(w io.Writer, src []byte, pages []Page) error {
	if len(pages) == 0 {
		return errors.New("pdfedit: no pages selected")
	}
	d, err := cera.Open(src)
	if err != nil {
		if errors.Is(err, pdf.ErrWrongPassword) || errors.Is(err, pdf.ErrUnsupportedEncryption) {
			return fmt.Errorf("%w: %v", ErrEncrypted, err)
		}
		return fmt.Errorf("pdfedit: %w", err)
	}
	sel := make([]cera.EditPage, len(pages))
	for i, p := range pages {
		sel[i] = cera.EditPage{Index: p.Index, Rotate: p.Rotate}
	}
	e := cera.NewEditor()
	if err := e.ImportPages(0, d, sel...); err != nil {
		if errors.Is(err, cera.ErrNoAssembly) {
			return ErrEncrypted
		}
		return fmt.Errorf("pdfedit: %w", err)
	}
	return e.Save(w)
}
