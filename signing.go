package cera

import (
	"errors"
	"slices"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
)

// What signatures allow an editor to change (PDF 2.0, 12.8). cera checks
// no signature; it reads what they declare:
//
//   - a certification signature's DocMDP permissions (12.8.2.2): with P 1
//     no change is allowed, with P 2 and 3 filling in the form is, but no
//     change to the pages;
//   - the field locks of signed signature fields (/Lock, 12.7.5.5): the
//     fields they name may not change;
//   - and that a document is signed at all: saving it as a new file
//     invalidates every signature, so only an incremental update may.

// Errors of editing a signed or certified document.
var (
	ErrCertified = errors.New("cera: the document's certification does not allow the change")
	ErrLocked    = errors.New("cera: the field is locked by a signature")
	ErrSigned    = errors.New("cera: the document is signed: a new file would invalidate its signatures, an update keeps them")
)

// signing is what the signatures of a document declare.
type signing struct {
	signed bool // a signature field holds a signature
	mdp    int  // the DocMDP permissions, 1 to 3; 0 when not certified
	locks  []sigLock
}

// sigLock is the field lock of a signed signature field.
type sigLock struct {
	action string   // All, Include or Exclude
	fields []string // fully qualified names
}

// locks reports whether the lock covers field name.
func (l sigLock) locks(name string) bool {
	in := slices.Contains(l.fields, name)
	switch l.action {
	case "All":
		return true
	case "Include":
		return in
	case "Exclude":
		return !in
	}
	return false
}

// signing reads what the signatures of d declare.
func (d *Document) signing() signing {
	var s signing
	if form := d.Form(); form != nil {
		for _, f := range form.Fields {
			if f.Type != FieldSignature || f.ref.Num <= 0 {
				continue
			}
			node := d.dict(f.ref.Object())
			if d.dict(node.Get("V")).IsZero() {
				continue // unsigned
			}
			s.signed = true
			lock := d.dict(node.Get("Lock"))
			if lock.IsZero() {
				continue
			}
			l := sigLock{}
			if n, ok := d.name(lock.Get("Action")); ok {
				l.action = string(n)
			}
			fields, _ := d.resolve(lock.Get("Fields")).Array()
			for _, o := range fields {
				l.fields = append(l.fields, textString(d.resolve(o)))
			}
			s.locks = append(s.locks, l)
		}
	}
	cat, err := d.r.Catalog()
	if err != nil {
		return s
	}
	sig := d.dict(d.dict(cat.Get("Perms")).Get("DocMDP"))
	if sig.IsZero() {
		return s
	}
	s.signed = true
	s.mdp = 2 // the default of /P
	refs, _ := d.resolve(sig.Get("Reference")).Array()
	for _, o := range refs {
		ref := d.dict(o)
		if m, _ := d.name(ref.Get("TransformMethod")); m != "DocMDP" {
			continue
		}
		if p, ok := d.integer(d.dict(ref.Get("TransformParams")).Get("P")); ok && p >= 1 && p <= 3 {
			s.mdp = p
		}
	}
	return s
}

// locked reports whether a signature locks field name.
func (s signing) locked(name string) bool {
	for _, l := range s.locks {
		if l.locks(name) {
			return true
		}
	}
	return false
}

// pagesChanged reports whether the editor changes the pages of the edited
// document: their set, order or rotation, or imports others.
func (e *Editor) pagesChanged() bool {
	if e.base == nil || len(e.pages) != e.base.NumPages() {
		return true
	}
	for i, p := range e.pages {
		if !p.own || p.index != i || p.rotate%360 != 0 {
			return true
		}
	}
	return false
}

// xfaPatch removes /XFA from the edited document's form, so that viewers
// show the fields written rather than the stale XFA data. A form direct in
// the catalogue is changed there.
func (e *Editor) xfaPatch() (pdfedit.Patch, bool) {
	d := e.base
	cat, err := d.r.Catalog()
	if err != nil {
		return pdfedit.Patch{}, false
	}
	af := cat.Get("AcroForm")
	if d.dict(af).Get("XFA").IsNull() {
		return pdfedit.Patch{}, false
	}
	if r, ok := af.Ref(); ok {
		return pdfedit.Patch{Ref: r, Del: []pdf.Name{"XFA"}}, true
	}
	root, ok := d.r.Trailer().Get("Root").Ref()
	if !ok {
		return pdfedit.Patch{}, false
	}
	var entries []pdf.Entry
	for k, v := range d.dict(af).All() {
		if k != "XFA" {
			entries = append(entries, pdf.Entry{Key: k, Val: v})
		}
	}
	return pdfedit.Patch{Ref: root, Set: []pdf.Entry{{Key: "AcroForm", Val: pdf.NewDict(entries...).Object()}}}, true
}
