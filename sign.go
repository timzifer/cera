package cera

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/timzifer/cera/internal/cms"
	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
)

// Signing (PDF 2.0, 12.8; ETSI EN 319 142-1). A signature is an update of
// the document: a signature dictionary in a signature field, with room
// for the signature, which covers every byte of the file but that room.
// The file is written with the room empty, the bytes around it are
// digested, and the signature over the digest is written into the room.

// SignOptions describe a signature [Editor.Sign] makes.
type SignOptions struct {
	// Key signs, and Certificate is its certificate. The key is an RSA or
	// an ECDSA key behind [crypto.Signer], so that it may be on a token or
	// with a signing service; it is asked once, for a signature over a
	// SHA-256 digest. Chain are the certificates between Certificate and
	// a root, which go into the signature for who checks it.
	Key         crypto.Signer
	Certificate *x509.Certificate
	Chain       []*x509.Certificate

	// Field is the fully qualified name of the signature field to sign,
	// one of the document that is not signed yet, or the name of a new
	// one. Empty, a new field is named "Signature1", or by the first
	// number that is free.
	Field string
	// Page and Rect place a new field: Rect is where it shows on Page, in
	// points of the displayed page. With the zero Rect the signature is
	// invisible. An existing field stays where it is.
	Page int
	Rect Rect
	// Appearance draws what the field shows, on a canvas of the size of
	// its rectangle; nil, or for an invisible signature, nothing is drawn.
	// A signed field shows the same to everyone: whether the signature
	// holds is for a viewer to say, beside the page.
	Appearance func(c *Canvas)

	// Name is the name of the signer, where the certificate should not
	// speak for it; Reason, Location and Contact are what the signer
	// states.
	Name, Reason, Location, Contact string
	// Time is when the signature is made, as the signer claims; the zero
	// Time is now.
	Time time.Time

	// Certify makes a certification signature, the author's, which says
	// what may be changed afterwards (DocMDP): 1 nothing, 2 filling in
	// forms and signing, 3 also annotating. 0 makes an approval signature.
	// A document is certified once, before it is signed.
	Certify int
	// Lock names the fields that may not change once the signature is
	// made; nil locks none.
	Lock *FieldLock
}

// FieldLock names the fields a signature locks (PDF 2.0, 12.7.5.5).
type FieldLock struct {
	// Action is "All" for every field, "Include" for those of Fields and
	// "Exclude" for all but those.
	Action string
	// Fields are fully qualified field names.
	Fields []string
}

// ErrSignatureRoom is returned by [Editor.Sign] when the signature is
// larger than the room cera left for it, as with a certificate chain a
// signing service adds to.
var ErrSignatureRoom = errors.New("cera: the signature does not fit the room left for it")

// signPlan is a signature being made.
type signPlan struct {
	opt   SignOptions
	field *Field // the field to sign; nil for a new one, named name
	name  string
	// The widget of a new field: on page, at rect in its default user
	// space, the zero Rect for an invisible one.
	page int
	rect Rect
	// canvas is the appearance drawn, nil for none, and m leads from it
	// to the user space of the page.
	canvas *Canvas
	m      Matrix
	room   int // bytes left for the signature
	sig    *pdfedit.Sig
}

// Sign writes the edited document's file to w with an update that holds
// the changes of the editor, as [Editor.Update] writes them, and a
// signature over all of it: an approval signature, or with
// [SignOptions.Certify] a certification. Signatures the document has stay
// valid. The signature is a PAdES baseline signature (B-B,
// ETSI.CAdES.detached) without a timestamp: its time is the signer's
// word.
//
// The pages cannot change with a signature: an editor that deleted,
// moved, turned, imported or drew on pages, or flattens the form, is
// written with Update first, and the result signed. Sign returns the
// errors of Update, [ErrCertified] for a document whose certification
// allows no changes, [ErrLocked] for a field a signature locks, and
// [ErrSignatureRoom]. The file is held in memory while it is signed, and
// nothing is written when Sign fails, but for an error of w itself.
//
// To go on after signing, open what was written: the editor still edits
// the document as it was.
func (e *Editor) Sign(w io.Writer, opt SignOptions) (err error) {
	defer recoverPanic(&err)
	if err := e.updatable(); err != nil {
		return err
	}
	sp, err := e.signCheck(opt)
	if err != nil {
		return err
	}
	e.sign = sp
	defer func() { e.sign = nil }()
	var buf bytes.Buffer
	err = pdfedit.Update(&buf, e.plan())
	switch {
	case errors.Is(err, pdfedit.ErrRepaired):
		return fmt.Errorf("%w: %v", ErrNoUpdate, err)
	case err != nil:
		return fmt.Errorf("cera: %w", err)
	}
	out := buf.Bytes()
	c, br := sp.sig.Contents, sp.sig.ByteRange
	if c[0] <= 0 || c[1] <= c[0] || br[1] <= br[0] || c[1] > int64(len(out)) || br[1] > int64(len(out)) {
		return errors.New("cera: the signature dictionary was not written")
	}
	// All but the room for the signature, brackets included.
	ranges := fmt.Sprintf("[0 %d %d %d", c[0], c[1], int64(len(out))-c[1])
	room := int(br[1] - br[0])
	if len(ranges)+1 > room {
		return errors.New("cera: the file is too large to sign")
	}
	copy(out[br[0]:br[1]], ranges+strings.Repeat(" ", room-len(ranges)-1)+"]")
	h := sha256.New()
	h.Write(out[:c[0]])
	h.Write(out[c[1]:])
	der, err := cms.SignCAdES(h.Sum(nil), opt.Certificate, opt.Key, opt.Chain...)
	if err != nil {
		return fmt.Errorf("cera: signing: %w", err)
	}
	if int64(2*len(der)) > c[1]-c[0]-2 {
		return ErrSignatureRoom
	}
	hex.Encode(out[c[0]+1:], der)
	_, err = w.Write(out)
	return err
}

// signCheck reports why the editor cannot sign as opt says, and else
// returns the signature to make.
func (e *Editor) signCheck(opt SignOptions) (*signPlan, error) {
	d := e.base
	if opt.Key == nil || opt.Certificate == nil {
		return nil, errors.New("cera: a signature needs a key and its certificate")
	}
	switch pub := opt.Key.Public().(type) {
	case *rsa.PublicKey:
		if !pub.Equal(opt.Certificate.PublicKey) {
			return nil, errors.New("cera: the certificate is not that of the key")
		}
	case *ecdsa.PublicKey:
		if !pub.Equal(opt.Certificate.PublicKey) {
			return nil, errors.New("cera: the certificate is not that of the key")
		}
	default:
		return nil, fmt.Errorf("cera: a %T cannot sign: want an RSA or an ECDSA key", pub)
	}
	if opt.Certify < 0 || opt.Certify > 3 {
		return nil, fmt.Errorf("cera: certification permissions %d: want 1, 2 or 3", opt.Certify)
	}
	if l := opt.Lock; l != nil && l.Action != "All" && l.Action != "Include" && l.Action != "Exclude" {
		return nil, fmt.Errorf("cera: field lock %q: want All, Include or Exclude", l.Action)
	}
	if e.pagesChanged() || e.drawsOn() || e.resized() || e.outlined() || e.labelsSet || e.flatten {
		return nil, errors.New("cera: a signature is not written with changes of the pages or a flattened form: update the document first, then sign the result")
	}
	if !e.unguarded {
		sg := d.signing()
		if sg.mdp == 1 {
			return nil, ErrCertified
		}
		if opt.Certify != 0 && sg.signed {
			return nil, errors.New("cera: a document is certified before it is signed, and once")
		}
		if opt.Field != "" && sg.locked(opt.Field) {
			return nil, ErrLocked
		}
	}
	if prot, ok := d.r.Protection(); ok && !prot.Owner && prot.Permissions&(pdf.PermFillForms|pdf.PermAnnotate|pdf.PermModify) == 0 {
		return nil, ErrNotPermitted
	}
	if _, ok := d.r.Trailer().Get("Root").Ref(); !ok {
		return nil, fmt.Errorf("%w: the catalogue is not an object of its own", ErrNoUpdate)
	}

	if e.formed() {
		return nil, errors.New("cera: a signature is not written with fields added or removed: update the document first, then sign the result")
	}
	sp := &signPlan{opt: opt, name: opt.Field, room: cms.Room(opt.Certificate, opt.Chain...)}
	if sp.opt.Time.IsZero() {
		sp.opt.Time = time.Now()
	}
	form := d.Form()
	var shown Rect // of the widget, in points of the displayed page
	var view *Page
	if f := form.Field(opt.Field); f != nil {
		switch {
		case f.Type != FieldSignature:
			return nil, fmt.Errorf("cera: the field %q is no signature field", opt.Field)
		case f.ref.Num <= 0:
			return nil, fmt.Errorf("cera: the field %q is not an object of its own", opt.Field)
		case !d.dict(d.dict(f.ref.Object()).Get("V")).IsZero():
			return nil, fmt.Errorf("cera: the field %q is signed", opt.Field)
		}
		sp.field = f
		// What it shows goes into its first widget that has an area.
		for _, w := range f.Widgets {
			if w.ref.Num > 0 && w.Rect.Dx() > 0 && w.Rect.Dy() > 0 {
				pg, err := d.Page(w.Page)
				if err != nil {
					break
				}
				view, sp.page, sp.rect = pg, w.Page, w.Rect
				shown = QuadOf(w.Rect).Transform(pg.DisplayMatrix()).Bounds()
				break
			}
		}
	} else {
		if sp.name == "" {
			for n := 1; ; n++ {
				if sp.name = fmt.Sprintf("Signature%d", n); form.Field(sp.name) == nil {
					break
				}
			}
		}
		if strings.Contains(sp.name, ".") {
			return nil, fmt.Errorf("cera: the name %q of a new field has a period, which parts the names of a field and those above it", sp.name)
		}
		if opt.Rect.Dx() > 0 && opt.Rect.Dy() > 0 {
			if err := e.check(opt.Page); err != nil {
				return nil, err
			}
			pg, err := d.Page(opt.Page)
			if err != nil {
				return nil, err
			}
			inv, ok := pg.DisplayMatrix().Invert()
			if !ok {
				return nil, errors.New("cera: the page has no area")
			}
			view, sp.page, shown = pg, opt.Page, opt.Rect
			sp.rect = QuadOf(opt.Rect).Transform(inv).Bounds()
		}
	}
	if view != nil && opt.Appearance != nil {
		inv, ok := view.DisplayMatrix().Invert()
		if !ok {
			return nil, errors.New("cera: the page has no area")
		}
		c := pageCanvas(shown.Dx(), shown.Dy())
		opt.Appearance(c)
		for len(c.stack) > 0 {
			c.Restore()
		}
		if len(c.cw) > 0 {
			// From the canvas to the page as it is displayed, and from
			// there to its user space.
			sp.canvas = c
			sp.m = Matrix{inv[0], inv[1], inv[2], inv[3],
				inv[0]*shown.X0 + inv[2]*shown.Y0 + inv[4], inv[1]*shown.X0 + inv[3]*shown.Y0 + inv[5]}
		}
	}
	return sp, nil
}

// signPlan adds the signature being made to doc: its dictionary, the
// field that holds it, and what the form and the catalogue say of it.
func (e *Editor) signPlan(doc *pdfedit.Doc, cf *canvasForms) {
	sp, d := e.sign, e.base
	o := &sp.opt
	name := func(s string) pdf.Object { return pdf.Name(s).Object() }
	text := func(s string) pdf.Object { return pdf.String(encodeText(s)) }

	sig := []pdf.Entry{
		{Key: "Type", Val: name("Sig")},
		{Key: "Filter", Val: name("Adobe.PPKLite")},
		{Key: "SubFilter", Val: name("ETSI.CAdES.detached")},
		// Both are written with room, and filled in by Sign.
		{Key: "ByteRange", Val: pdf.Array{pdf.Integer(0), pdf.Integer(0), pdf.Integer(0), pdf.Integer(0)}.Object()},
		{Key: "Contents", Val: pdf.String(make([]byte, sp.room))},
		{Key: "M", Val: pdf.String([]byte(pdfDate(o.Time)))},
	}
	for _, t := range []struct {
		key pdf.Name
		s   string
	}{{"Name", o.Name}, {"Reason", o.Reason}, {"Location", o.Location}, {"ContactInfo", o.Contact}} {
		if t.s != "" {
			sig = append(sig, pdf.Entry{Key: t.key, Val: text(t.s)})
		}
	}
	// What the signature says may change after it (12.8.2).
	transform := func(method string, params ...pdf.Entry) pdf.Object {
		params = append([]pdf.Entry{{Key: "Type", Val: name("TransformParams")}}, params...)
		params = append(params, pdf.Entry{Key: "V", Val: name("1.2")})
		return pdf.NewDict(
			pdf.Entry{Key: "Type", Val: name("SigRef")},
			pdf.Entry{Key: "TransformMethod", Val: name(method)},
			pdf.Entry{Key: "TransformParams", Val: pdf.NewDict(params...).Object()},
		).Object()
	}
	var lock []pdf.Entry
	var refs pdf.Array
	if o.Certify != 0 {
		refs = append(refs, transform("DocMDP", pdf.Entry{Key: "P", Val: pdf.Integer(int64(o.Certify))}))
	}
	if l := o.Lock; l != nil {
		lock = []pdf.Entry{{Key: "Action", Val: name(l.Action)}}
		if l.Action != "All" {
			fields := make(pdf.Array, len(l.Fields))
			for i, f := range l.Fields {
				fields[i] = text(f)
			}
			lock = append(lock, pdf.Entry{Key: "Fields", Val: fields.Object()})
		}
		refs = append(refs, transform("FieldMDP", lock...))
	}
	if len(refs) > 0 {
		sig = append(sig, pdf.Entry{Key: "Reference", Val: refs.Object()})
	}
	sigRef := cf.add(pdf.NewDict(sig...).Object())
	sp.sig = &pdfedit.Sig{Object: len(doc.Objects) - 1}
	doc.Sig = sp.sig

	held := []pdf.Entry{{Key: "V", Val: sigRef}}
	if lock != nil {
		held = append(held, pdf.Entry{Key: "Lock", Val: pdf.NewDict(
			append([]pdf.Entry{{Key: "Type", Val: name("SigFieldLock")}}, lock...)...).Object()})
	}
	var look []pdf.Entry
	if sp.canvas != nil {
		look = []pdf.Entry{{Key: "AP", Val: pdf.NewDict(pdf.Entry{Key: "N", Val: cf.form(sp.canvas, sp.m).Object()}).Object()}}
	}
	root, _ := d.r.Trailer().Get("Root").Ref()
	var added pdf.Object // the new field, for the form
	if f := sp.field; f != nil {
		doc.Patches = append(doc.Patches, pdfedit.Patch{Ref: f.ref, Set: held})
		if look != nil {
			doc.Patches = append(doc.Patches, pdfedit.Patch{Ref: f.Widgets[sp.widget()].ref, Set: look})
		}
	} else {
		// A field that is its widget, on the page it shows on; an
		// invisible one on the first.
		page, pageRef := sp.pageOf(d)
		field := append([]pdf.Entry{
			{Key: "Type", Val: name("Annot")},
			{Key: "Subtype", Val: name("Widget")},
			{Key: "FT", Val: name("Sig")},
			{Key: "T", Val: text(sp.name)},
			{Key: "F", Val: pdf.Integer(int64(AnnotPrint | AnnotLocked))},
			{Key: "Rect", Val: rectArray(sp.rect)},
		}, held...)
		field = append(field, look...)
		if page != nil {
			field = append(field, pdf.Entry{Key: "P", Val: pageRef.Object()})
		}
		added = cf.add(pdf.NewDict(field...).Object())
		if page != nil {
			annots := append(e.annots(doc.Patches, pageRef, page), added)
			doc.Patches = append(doc.Patches, pdfedit.Patch{Ref: pageRef, Set: []pdf.Entry{{Key: "Annots", Val: annots.Object()}}})
		}
	}

	// The form: its fields, and that it has signatures, which a file is
	// then to be updated for and not written anew (SigFlags 1 and 2).
	set := []pdf.Entry{{Key: "SigFlags", Val: pdf.Integer(3)}}
	if !added.IsNull() {
		list, _ := d.resolve(e.formEntry(doc.Patches, "Fields")).Array()
		set = append(set, pdf.Entry{Key: "Fields", Val: append(append(pdf.Array(nil), list...), added).Object()})
	}
	doc.Patches = e.formSet(doc.Patches, set...)

	if o.Certify != 0 {
		// The catalogue names the signature that certifies the document.
		cat, _ := d.r.Catalog()
		perms := cat.Get("Perms")
		mdp := pdf.Entry{Key: "DocMDP", Val: sigRef}
		if r, ok := perms.Ref(); ok && !d.dict(perms).IsZero() {
			doc.Patches = append(doc.Patches, pdfedit.Patch{Ref: r, Set: []pdf.Entry{mdp}})
		} else {
			var entries []pdf.Entry
			for k, v := range d.dict(perms).All() {
				if k != "DocMDP" {
					entries = append(entries, pdf.Entry{Key: k, Val: v})
				}
			}
			doc.Patches = append(doc.Patches, pdfedit.Patch{Ref: root, Set: []pdf.Entry{{Key: "Perms", Val: pdf.NewDict(append(entries, mdp)...).Object()}}})
		}
	}
}

// widget returns the index of the widget of the signed field that shows
// its appearance.
func (sp *signPlan) widget() int {
	for i, w := range sp.field.Widgets {
		if w.Page == sp.page && w.Rect == sp.rect && w.ref.Num > 0 {
			return i
		}
	}
	return 0
}

// pageOf returns the page the widget of a new field goes on, nil when the
// document has none to name.
func (sp *signPlan) pageOf(d *Document) (*Page, pdf.Ref) {
	pg, err := d.Page(sp.page)
	if err != nil {
		return nil, pdf.Ref{}
	}
	ref, ok := d.r.PageRef(sp.page + 1)
	if !ok {
		return nil, pdf.Ref{}
	}
	return pg, ref
}

// lastSet returns what the patches set the entry key of object ref to.
func lastSet(patches []pdfedit.Patch, ref pdf.Ref, key pdf.Name) (pdf.Object, bool) {
	for k := len(patches) - 1; k >= 0; k-- {
		if patches[k].Ref != ref {
			continue
		}
		for _, s := range patches[k].Set {
			if s.Key == key {
				return s.Val, true
			}
		}
	}
	return pdf.Null, false
}
