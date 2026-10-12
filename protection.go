package cera

import (
	"errors"
	"fmt"
	"strings"

	"github.com/timzifer/cera/internal/pdf"
)

// ErrPassword is returned by the Open functions for an encrypted document
// that the password of [OpenOptions] does not open: a missing one (the
// empty password was tried) and a wrong one alike. A viewer asks for a
// password and opens the file again.
var ErrPassword = errors.New("cera: the document needs a password, or the password is wrong")

// ErrUnsupportedEncryption is returned by the Open functions for a
// document encrypted in a way cera cannot read: a security handler other
// than the standard one, or a crypt filter it does not know. No password
// opens it.
var ErrUnsupportedEncryption = errors.New("cera: unsupported security handler")

// Permissions are what the author of an encrypted document allows a reader
// that opened it with the user password (PDF 2.0, table 22).
type Permissions uint32

// The permissions of an encrypted document.
const (
	// PermPrint allows printing; without PermPrintFaithful only in a
	// quality that does not serve as a copy of the document.
	PermPrint Permissions = 1 << 2
	// PermModify allows changing the content of the document.
	PermModify Permissions = 1 << 3
	// PermCopy allows copying text and graphics out of the document.
	PermCopy Permissions = 1 << 4
	// PermAnnotate allows adding and changing annotations, and filling
	// in form fields.
	PermAnnotate Permissions = 1 << 5
	// PermFillForms allows filling in form fields, also without
	// PermAnnotate.
	PermFillForms Permissions = 1 << 8
	// PermExtract allowed extracting text and graphics for accessibility;
	// PDF 2.0 deprecates it and always allows that.
	PermExtract Permissions = 1 << 9
	// PermAssemble allows inserting, deleting and rotating pages, also
	// without PermModify.
	PermAssemble Permissions = 1 << 10
	// PermPrintFaithful allows printing in full quality.
	PermPrintFaithful Permissions = 1 << 11

	// AllPermissions is every permission.
	AllPermissions = PermPrint | PermModify | PermCopy | PermAnnotate |
		PermFillForms | PermExtract | PermAssemble | PermPrintFaithful
)

// Encrypted reports whether the file of d is encrypted.
func (d *Document) Encrypted() bool { return d.r.Encrypted() }

// Permissions returns what the document allows: [AllPermissions] for a
// document that is not encrypted and for one opened with the owner
// password, else what its author granted.
//
// The editor refuses what the permissions do not allow ([ErrNoAssembly],
// [ErrNotPermitted]). Reading does not: [Page.Render] and [Page.Text] work
// whatever PermPrint and PermCopy say, and whether to print or to copy is
// the decision of the application.
func (d *Document) Permissions() Permissions {
	prot, ok := d.r.Protection()
	if !ok || prot.Owner {
		return AllPermissions
	}
	return Permissions(prot.Permissions) & AllPermissions
}

// openError returns the error of the reader as the Open functions report
// it.
func openError(err error) error {
	switch {
	case errors.Is(err, pdf.ErrWrongPassword):
		return ErrPassword
	case errors.Is(err, pdf.ErrUnsupportedEncryption):
		// What is not supported follows the reader's own words for it.
		return fmt.Errorf("%w%s", ErrUnsupportedEncryption, strings.TrimPrefix(err.Error(), pdf.ErrUnsupportedEncryption.Error()))
	}
	return fmt.Errorf("cera: %w", err)
}
