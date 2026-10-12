package cera

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"image/color"
	"io"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// signWith signs with e and checks that the result starts with the edited
// file and reads without a repair.
func signWith(t *testing.T, e *Editor, src []byte, opt SignOptions) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := e.Sign(&out, opt); err != nil {
		t.Fatal(err)
	}
	b := out.Bytes()
	if !bytes.HasPrefix(b, src) {
		t.Fatal("the signed file does not start with the file")
	}
	d, err := pdf.OpenWith(b, pdf.Options{Password: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Repaired() {
		t.Fatalf("the signed file needed a repair:\n%s", b[len(src):])
	}
	return b
}

// intact checks signature i of doc: it holds, and its certificate leads
// to that of s.
func intact(t *testing.T, doc *Document, i int, s testSigner) (*Signature, SignatureStatus) {
	t.Helper()
	sigs := doc.Signatures()
	if i >= len(sigs) {
		t.Fatalf("%d signatures, want one at %d", len(sigs), i)
	}
	roots := x509.NewCertPool()
	roots.AddCert(s.cert)
	st, err := sigs[i].Verify(VerifyOptions{Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Intact || !st.Trusted {
		t.Fatalf("signature %d: %+v", i, st)
	}
	return &sigs[i], st
}

func signOptions(s testSigner) SignOptions {
	return SignOptions{Key: s.key, Certificate: s.cert, Time: signedAt}
}

// TestSign signs a document without a form, invisibly, and signs the
// result again.
func TestSign(t *testing.T) {
	ada, bob := ecSigner(t, "Ada Lovelace"), ecSigner(t, "Bob")
	src := viewFile()
	doc := editOpen(t, src)
	opt := signOptions(ada)
	opt.Reason, opt.Location, opt.Contact = "I wrote it (really)", "Köln", "ada@example.com"
	signed := signWith(t, doc.Edit(), src, opt)

	sd := editOpen(t, signed)
	s, st := intact(t, sd, 0, ada)
	if s.Field.Name != "Signature1" || s.SubFilter != "ETSI.CAdES.detached" || s.Signer != "Ada Lovelace" || s.Reason != opt.Reason ||
		s.Location != "Köln" || !s.CoversWholeFile || s.Certification != 0 || !s.Time.Equal(signedAt) {
		t.Errorf("signature %+v", s)
	}
	if st.Changes.Any() || !st.ChangesAllowed || st.Timestamped {
		t.Errorf("status %+v", st)
	}
	// The field is a widget of the first page without an area, which is
	// none to show, and the form says that the document is signed.
	f := sd.Form().Field("Signature1")
	if f == nil || f.Type != FieldSignature || len(f.Widgets) != 0 {
		t.Fatalf("field %+v", f)
	}
	pd, _ := sd.r.Page(1)
	annots, _ := sd.resolve(pd.Get("Annots")).Array()
	if wd := sd.dict(annots[len(annots)-1]); len(annots) != 3 || annots[2] != f.ref.Object() || wd.Get("Rect").String() != "[0 0 0 0]" ||
		wd.Get("F").String() != "132" || wd.Get("P").String() != "3 0 R" {
		t.Errorf("the widget of the field: %v in %v", wd, annots)
	}
	cat, _ := sd.r.Catalog()
	if flags, _ := sd.integer(sd.dict(cat.Get("AcroForm")).Get("SigFlags")); flags != 3 {
		t.Errorf("/SigFlags %d", flags)
	}
	p0, _ := doc.Page(0)
	s0, _ := sd.Page(0)
	if n, _ := pixDiff(renderView(t, p0), renderView(t, s0)); n != 0 {
		t.Errorf("an invisible signature changed %d pixels", n)
	}
	// A new file would break it.
	if err := sd.Edit().Save(io.Discard); !errors.Is(err, ErrSigned) {
		t.Errorf("Save of the signed document: %v", err)
	}

	// Signed again, by another: both hold, and the first says that a
	// signature was added, which it allows.
	twice := signWith(t, sd.Edit(), signed, signOptions(bob))
	td := editOpen(t, twice)
	first, st := intact(t, td, 0, ada)
	if first.CoversWholeFile || !st.Changes.Signed || st.Changes.Other || st.Changes.FormFilled || !st.ChangesAllowed {
		t.Errorf("the first signature after the second: %+v", st)
	}
	second, st := intact(t, td, 1, bob)
	if second.Field.Name != "Signature2" || second.Signer != "Bob" || !second.CoversWholeFile || st.Changes.Any() {
		t.Errorf("the second signature %+v, %+v", second, st)
	}

	// A byte of the signed file changed: the signature does not hold.
	bad := bytes.Clone(signed)
	bad[bytes.Index(bad, []byte("Hello"))] = 'J'
	if st, err := editOpen(t, bad).Signatures()[0].Verify(VerifyOptions{}); err != nil || st.Intact {
		t.Errorf("a changed file: %+v, %v", st, err)
	}
}

// TestSignVisible signs with an appearance, in a new field of a turned
// page and in a field of the document.
func TestSignVisible(t *testing.T) {
	ada := ecSigner(t, "Ada")
	green := color.RGBA{0, 255, 0, 255}
	draw := func(c *Canvas) {
		w, h := c.Size()
		c.FillRect(Rect{0, 0, w, h}, green)
		c.FillRect(Rect{0, 0, 10, 10}, red) // the corner at the top left
	}
	for _, rotate := range []string{"", "/Rotate 90"} {
		src := testpdf.File{
			Objs: map[int]string{
				1: "<</Type /Catalog /Pages 2 0 R>>",
				2: "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 300 200]>>",
				3: "<</Type /Page /Parent 2 0 R>>",
				4: "<</Type /Page /Parent 2 0 R /Annots 5 0 R " + rotate + ">>",
				5: "[6 0 R]",
				6: "<</Type /Annot /Subtype /Square /Rect [0 0 5 5]>>",
			},
			Trailer: "/Root 1 0 R",
		}.Bytes()
		opt := signOptions(ada)
		opt.Field, opt.Page, opt.Rect, opt.Appearance = "approved", 1, Rect{50, 20, 110, 50}, draw
		signed := signWith(t, editOpen(t, src).Edit(), src, opt)
		sd := editOpen(t, signed)
		intact(t, sd, 0, ada)
		p, _ := sd.Page(1)
		img := renderView(t, p)
		for at, want := range map[[2]int]color.RGBA{{55, 25}: red, {80, 35}: green, {105, 45}: green, {55, 45}: green, {45, 35}: white, {115, 35}: white, {80, 55}: white} {
			if got := img.RGBAAt(at[0], at[1]); got != want {
				t.Errorf("%q: pixel %v is %v, want %v", rotate, at, got, want)
			}
		}
		f := sd.Form().Field("approved")
		if f == nil || len(f.Widgets) != 1 || f.Widgets[0].Page != 1 || f.Widgets[0].Annotation != 1 {
			t.Errorf("%q: field %+v", rotate, f)
		}
		// Where the page shows it.
		if got := QuadOf(f.Widgets[0].Rect).Transform(p.DisplayMatrix()).Bounds(); !sameRect(got, opt.Rect) {
			t.Errorf("%q: the widget is at %v, want %v", rotate, got, opt.Rect)
		}
	}

	// The signature field of a form: where it is.
	src := signedFile(false, 0, false)
	opt := signOptions(ada)
	opt.Field, opt.Appearance = "sig", draw
	opt.Page, opt.Rect = 1, Rect{0, 0, 50, 50} // of no account
	signed := signWith(t, editOpen(t, src).Edit(), src, opt)
	sd := editOpen(t, signed)
	s, _ := intact(t, sd, 0, ada)
	if s.Field.Name != "sig" || len(sd.Form().Fields) != 3 {
		t.Errorf("signed the field %q of %d", s.Field.Name, len(sd.Form().Fields))
	}
	p, _ := sd.Page(0)
	img := renderView(t, p)
	// The widget is at [10 10 90 30] of a page 100 high.
	if a, b, c := img.RGBAAt(12, 72), img.RGBAAt(50, 80), img.RGBAAt(50, 95); a != red || b != green || c == green {
		t.Errorf("the field shows %v, %v and below it %v", a, b, c)
	}
	// It is signed now.
	if err := sd.Edit().Sign(io.Discard, opt); err == nil || !strings.Contains(err.Error(), "is signed") {
		t.Errorf("signing a signed field: %v", err)
	}
}

// TestSignCertify certifies a document and locks fields.
func TestSignCertify(t *testing.T) {
	ada := ecSigner(t, "Ada")
	for _, direct := range []bool{false, true} {
		src := signedFile(false, 0, direct)
		doc := editOpen(t, src)
		e := doc.Edit()
		// Filled in and certified in one update.
		state := doc.Form().NewState()
		if err := state.SetValue(doc.Form().Field("city"), TextValue("Bonn")); err != nil {
			t.Fatal(err)
		}
		if err := e.SetFields(state); err != nil {
			t.Fatal(err)
		}
		opt := signOptions(ada)
		opt.Certify, opt.Lock = 2, &FieldLock{Action: "Include", Fields: []string{"name"}}
		signed := signWith(t, e, src, opt)
		sd := editOpen(t, signed)
		s, st := intact(t, sd, 0, ada)
		if s.Certification != 2 || s.Field.Name != "Signature1" || st.Changes.Any() {
			t.Errorf("direct %v: certification %d, %+v", direct, s.Certification, st)
		}
		form := sd.Form()
		if got := form.Field("city").Saved.Text(); got != "Bonn" || form.XFA || len(form.Fields) != 4 {
			t.Errorf("direct %v: city %q, XFA %v, %d fields", direct, got, form.XFA, len(form.Fields))
		}
		// What the certification allows, and what it and the lock do not.
		fill := func(field string) error {
			ne := sd.Edit()
			st := sd.Form().NewState()
			if err := st.SetValue(sd.Form().Field(field), TextValue("x")); err != nil {
				return err
			}
			if err := ne.SetFields(st); err != nil {
				return err
			}
			return ne.Update(io.Discard)
		}
		if err := fill("city"); err != nil {
			t.Errorf("direct %v: filling in a field: %v", direct, err)
		}
		if err := fill("name"); !errors.Is(err, ErrLocked) {
			t.Errorf("direct %v: filling in a locked field: %v", direct, err)
		}
		ne := sd.Edit()
		if err := ne.RotatePage(0, 90); err != nil {
			t.Fatal(err)
		}
		if err := ne.Update(io.Discard); !errors.Is(err, ErrCertified) {
			t.Errorf("direct %v: turning a page of a certified document: %v", direct, err)
		}
		// Signed by another: allowed; certified again: not.
		bob := ecSigner(t, "Bob")
		twice := signWith(t, sd.Edit(), signed, signOptions(bob))
		_, st = intact(t, editOpen(t, twice), 0, ada)
		if !st.Changes.Signed || st.Changes.Other || !st.ChangesAllowed {
			t.Errorf("direct %v: the certification after a signature: %+v", direct, st)
		}
		opt = signOptions(bob)
		opt.Certify = 1
		if err := sd.Edit().Sign(io.Discard, opt); err == nil {
			t.Errorf("direct %v: a certified document was certified again", direct)
		}
	}

	// No changes allowed: no signature either.
	if err := editOpen(t, signedFile(true, 1, false)).Edit().Sign(io.Discard, signOptions(ada)); !errors.Is(err, ErrCertified) {
		t.Errorf("signing a document certified with no changes: %v", err)
	}
	// Certified with no changes: nothing more.
	src := viewFile()
	opt := signOptions(ada)
	opt.Certify = 1
	sd := editOpen(t, signWith(t, editOpen(t, src).Edit(), src, opt))
	if s, _ := intact(t, sd, 0, ada); s.Certification != 1 {
		t.Errorf("certification %d", s.Certification)
	}
	if err := sd.Edit().Sign(io.Discard, signOptions(ada)); !errors.Is(err, ErrCertified) {
		t.Errorf("signing after a certification with no changes: %v", err)
	}
}

// bigSigner signs with more bytes than a signature has.
type bigSigner struct{ crypto.Signer }

func (b bigSigner) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return make([]byte, 1<<16), nil
}

// failSigner is a key that does not sign.
type failSigner struct{ crypto.Signer }

func (failSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("the token is not there")
}

func TestSignOdd(t *testing.T) {
	ada := ecSigner(t, "Ada")
	src := viewFile()
	doc := editOpen(t, src)

	// An RSA key, with a chain.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rs := newSigner(t, "Grace Hopper", key)
	opt := signOptions(rs)
	opt.Chain = []*x509.Certificate{ada.cert}
	opt.Name = "G. Hopper"
	sd := editOpen(t, signWith(t, doc.Edit(), src, opt))
	if s, st := intact(t, sd, 0, rs); s.Signer != "Grace Hopper" || len(st.Certificates) != 2 {
		t.Errorf("signer %q, %d certificates", s.Signer, len(st.Certificates))
	}

	// An encrypted document stays encrypted, the signature in the clear.
	enc := testpdf.Encrypted("user", -1)
	ed, err := OpenWithPassword(enc, "user")
	if err != nil {
		t.Fatal(err)
	}
	opt = signOptions(ada)
	opt.Reason = "secret reason"
	signed := signWith(t, ed.Edit(), enc, opt)
	if bytes.Contains(signed[len(enc):], []byte("secret reason")) {
		t.Error("the reason of a signature in an encrypted document is not encrypted")
	}
	esd, err := OpenWithPassword(signed, "user")
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := intact(t, esd, 0, ada); s.Reason != "secret reason" {
		t.Errorf("reason %q", s.Reason)
	}

	// What cannot be signed, or not so.
	bob := ecSigner(t, "Bob")
	turned := doc.Edit()
	if err := turned.RotatePage(0, 90); err != nil {
		t.Fatal(err)
	}
	drawn := doc.Edit()
	if err := drawn.Draw(0, true, func(c *Canvas) { c.FillRect(Rect{0, 0, 5, 5}, red) }); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		e   *Editor
		opt SignOptions
	}{
		"no key":             {doc.Edit(), SignOptions{Certificate: ada.cert}},
		"no certificate":     {doc.Edit(), SignOptions{Key: ada.key}},
		"another's key":      {doc.Edit(), SignOptions{Key: bob.key, Certificate: ada.cert}},
		"permissions":        {doc.Edit(), SignOptions{Key: ada.key, Certificate: ada.cert, Certify: 4}},
		"lock":               {doc.Edit(), SignOptions{Key: ada.key, Certificate: ada.cert, Lock: &FieldLock{Action: "Some"}}},
		"a page turned":      {turned, signOptions(ada)},
		"a page drawn on":    {drawn, signOptions(ada)},
		"a page that is not": {doc.Edit(), SignOptions{Key: ada.key, Certificate: ada.cert, Page: 7, Rect: Rect{0, 0, 10, 10}}},
		"a name with parts":  {doc.Edit(), SignOptions{Key: ada.key, Certificate: ada.cert, Field: "a.b"}},
		"a text field":       {editOpen(t, signedFile(false, 0, false)).Edit(), SignOptions{Key: ada.key, Certificate: ada.cert, Field: "name"}},
	} {
		var out bytes.Buffer
		if err := c.e.Sign(&out, c.opt); err == nil || out.Len() != 0 {
			t.Errorf("%s: %v, %d bytes written", name, err, out.Len())
		}
	}
	var out bytes.Buffer
	if err := NewEditor().Sign(&out, signOptions(ada)); !errors.Is(err, ErrNoUpdate) {
		t.Errorf("signing a new document: %v", err)
	}
	if err := doc.Edit().Sign(&out, SignOptions{Key: bigSigner{ada.key}, Certificate: ada.cert}); !errors.Is(err, ErrSignatureRoom) {
		t.Errorf("a signature too large: %v", err)
	}
	if err := doc.Edit().Sign(&out, SignOptions{Key: failSigner{ada.key}, Certificate: ada.cert}); err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("a key that does not sign: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("%d bytes written by signatures that failed", out.Len())
	}
	// The editor is as it was: it signs.
	e := doc.Edit()
	if err := e.Sign(&out, SignOptions{Key: failSigner{ada.key}, Certificate: ada.cert}); err == nil {
		t.Fatal("signed without a key")
	}
	intact(t, editOpen(t, signWith(t, e, src, signOptions(ada))), 0, ada)

	// Its text, read by a viewer: a field the page does not show.
	p, _ := editOpen(t, signWith(t, doc.Edit(), src, signOptions(ada))).Page(0)
	if text, err := p.Text(context.Background()); err != nil || text.String() != "Hello" {
		t.Errorf("text of the signed page: %v, %v", text, err)
	}
}
