package cera

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/timzifer/cera/internal/cms"
	"github.com/timzifer/cera/internal/pdf"
)

// Reading and checking signatures (PDF 2.0, 12.8). A signature covers the
// bytes of the file as it was when it was signed — all of it but the room
// for the signature itself (/ByteRange) — and what is appended later
// leaves it in place. Checking one is three questions:
//
//   - Integrity: are those bytes still what was signed, and is the
//     signature that of the key of the certificate it names?
//   - Changes: was anything appended after it, and is that what the
//     signatures allow — filling in the form, signing again, annotating —
//     or more?
//   - Trust: does the certificate lead to one the caller trusts? cera has
//     no trust list of its own.
//
// Revocation and long-term validation (/DSS, OCSP, CRLs) are not checked.

// ErrSignatureUnsupported is returned by [Signature.Verify] for a
// signature cera cannot check: a subfilter or an algorithm it does not
// know, or signature data it cannot read.
var ErrSignatureUnsupported = errors.New("cera: the signature cannot be checked")

// Signature is a signature of a document, in a signature field.
type Signature struct {
	// Field is the signature field.
	Field *Field
	// SubFilter says how the signature is encoded: "adbe.pkcs7.detached",
	// "ETSI.CAdES.detached", "adbe.pkcs7.sha1", "adbe.x509.rsa_sha1", or
	// "ETSI.RFC3161" for a document timestamp.
	SubFilter string
	// Signer is the name of the signer: the common name of the signing
	// certificate, else its subject, else the name the signature
	// dictionary gives (/Name). It is what the file says until Verify
	// has checked the signature.
	Signer string
	// Time is when the signature was made: the time of its timestamp
	// token when it has one that holds, else what the signer claims (/M,
	// or the signing time in the signature).
	Time time.Time
	// Reason and Location are what the signer states.
	Reason, Location string
	// CoversWholeFile reports that the signature covers the file to its
	// end: nothing was appended after it.
	CoversWholeFile bool
	// Certification is the DocMDP permissions of a certification
	// signature — 1: no changes, 2: filling in forms and signing, 3: also
	// annotating — and 0 for an approval signature.
	Certification int

	doc    *Document
	dict   pdf.Dict
	ranges [][2]int64 // offset and length of the stretches covered
	end    int64      // where the last one ends
	lock   *sigLock
}

// VerifyOptions say how to check a signature.
type VerifyOptions struct {
	// Roots are the certificates the caller trusts. Without them the
	// chain is not checked and SignatureStatus.Trusted is false.
	Roots *x509.CertPool
	// Intermediates are certificates between the signer's and the roots,
	// added to those the signature carries.
	Intermediates *x509.CertPool
	// At is the time the certificates must be valid at; zero for the time
	// of the signature (Signature.Time), or now when it has none.
	At time.Time
}

// Changes say what was appended to a file after a signature.
type Changes struct {
	// FormFilled: values of form fields changed. Fields names them.
	FormFilled bool
	Fields     []string
	// Signed: further signatures were added.
	Signed bool
	// Annotated: annotations were added, changed or removed.
	Annotated bool
	// Other: something else changed — pages, their content, fields added
	// or removed —, or cera could not tell what did.
	Other bool
}

// Any reports whether anything changed.
func (c Changes) Any() bool { return c.FormFilled || c.Signed || c.Annotated || c.Other }

// SignatureStatus is the result of checking a signature.
type SignatureStatus struct {
	// Intact reports that the bytes the signature covers are those that
	// were signed, and that the signature is that of the key of
	// Certificate. Nothing else of the status means anything without it.
	Intact bool
	// Changes is what was appended after the signature, and
	// ChangesAllowed whether the certification of the document and the
	// lock of the signature's field allow it: filling in fields that are
	// not locked and signing unless the certification says no changes,
	// annotating unless it says forms only. Other changes never are.
	Changes        Changes
	ChangesAllowed bool
	// Certificate is the signer's certificate and Certificates all the
	// signature carries.
	Certificate  *x509.Certificate
	Certificates []*x509.Certificate
	// Trusted reports that Certificate leads to one of the roots of the
	// options, valid at the time checked; TrustError says why not.
	Trusted    bool
	TrustError error
	// Timestamped reports that Signature.Time is the time of a timestamp
	// token, signed by Timestamper, rather than the signer's claim. The
	// timestamp authority's own chain is not checked.
	Timestamped bool
	Timestamper *x509.Certificate
}

// Signatures returns the signatures of the document, in the order they
// were made. Read once and shared; nil for a document without.
func (d *Document) Signatures() []Signature {
	d.sigOnce.Do(func() {
		defer func() {
			if recover() != nil {
				d.sigs = nil
			}
		}()
		form := d.Form()
		if form == nil {
			return
		}
		size := d.r.SourceSize()
		cat, _ := d.r.Catalog()
		mdpRef, _ := d.dict(cat.Get("Perms")).Get("DocMDP").Ref()
		for _, f := range form.Fields {
			if f.Type != FieldSignature || f.ref.Num <= 0 {
				continue
			}
			node := d.dict(f.ref.Object())
			v := d.dict(node.Get("V"))
			if v.IsZero() {
				continue
			}
			s := Signature{Field: f, doc: d, dict: v}
			if n, ok := d.name(v.Get("SubFilter")); ok {
				s.SubFilter = string(n)
			}
			s.Reason = textString(d.resolve(v.Get("Reason")))
			s.Location = textString(d.resolve(v.Get("Location")))
			s.Signer = textString(d.resolve(v.Get("Name")))
			s.Time = parseDate(textString(d.resolve(v.Get("M"))))
			br, _ := d.resolve(v.Get("ByteRange")).Array()
			for i := 0; i+1 < len(br); i += 2 {
				off, ok1 := d.integer(br[i])
				n, ok2 := d.integer(br[i+1])
				if !ok1 || !ok2 || off < 0 || n < 0 || int64(off)+int64(n) > size || int64(off) < s.end {
					s.ranges = nil
					break
				}
				s.ranges = append(s.ranges, [2]int64{int64(off), int64(n)})
				s.end = int64(off) + int64(n)
			}
			s.CoversWholeFile = len(s.ranges) > 0 && s.ranges[0][0] == 0 && s.end == size
			// A certification: the signature /Perms /DocMDP names, or one
			// with a DocMDP transform.
			refs, _ := d.resolve(v.Get("Reference")).Array()
			for _, o := range refs {
				ref := d.dict(o)
				if m, _ := d.name(ref.Get("TransformMethod")); m != "DocMDP" {
					continue
				}
				s.Certification = 2
				if p, ok := d.integer(d.dict(ref.Get("TransformParams")).Get("P")); ok && p >= 1 && p <= 3 {
					s.Certification = p
				}
			}
			if vr, ok := node.Get("V").Ref(); ok && vr == mdpRef && s.Certification == 0 {
				s.Certification = 2
			}
			if lock := d.dict(node.Get("Lock")); !lock.IsZero() {
				l := &sigLock{}
				if n, ok := d.name(lock.Get("Action")); ok {
					l.action = string(n)
				}
				fields, _ := d.resolve(lock.Get("Fields")).Array()
				for _, o := range fields {
					l.fields = append(l.fields, textString(d.resolve(o)))
				}
				s.lock = l
			}
			// Who and when, as the signature itself says.
			if sd, _ := s.signed(); sd != nil {
				if c := sd.Signer; c != nil {
					s.Signer = c.Subject.CommonName
					if s.Signer == "" {
						s.Signer = c.Subject.String()
					}
				}
				if ts := sd.SignatureTimestamp(); ts != nil {
					s.Time = ts.Time
				} else if s.Time.IsZero() {
					s.Time = sd.SigningTime
				}
				if s.SubFilter == "ETSI.RFC3161" {
					if ts, err := cms.ParseTimestamp(s.contents()); err == nil {
						s.Time = ts.Time
					}
				}
			}
			d.sigs = append(d.sigs, s)
		}
		slices.SortStableFunc(d.sigs, func(a, b Signature) int {
			switch {
			case a.end < b.end:
				return -1
			case a.end > b.end:
				return 1
			}
			return 0
		})
	})
	return d.sigs
}

// contents returns the signature data: what the file holds in the gap of
// the byte range, a hexadecimal string. It is read from the file, not from
// the dictionary: in an encrypted file the reader would decrypt it, and it
// is not encrypted.
func (s *Signature) contents() []byte {
	if len(s.ranges) != 2 {
		// No gap to read: take the string of the dictionary.
		b, _ := s.doc.resolve(s.dict.Get("Contents")).Str()
		return b
	}
	from := s.ranges[0][0] + s.ranges[0][1]
	n := s.ranges[1][0] - from
	if n <= 0 || n > maxSignatureBytes {
		return nil
	}
	raw, err := io.ReadAll(s.doc.r.SourceRange(from, n))
	if err != nil {
		return nil
	}
	// The gap is the string with its brackets; some writers leave the
	// brackets among the bytes covered.
	raw = bytes.TrimSuffix(bytes.TrimPrefix(bytes.TrimSpace(raw), []byte("<")), []byte(">"))
	raw = bytes.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, raw)
	if len(raw)%2 == 1 {
		raw = append(raw, '0')
	}
	out := make([]byte, len(raw)/2)
	if _, err := hex.Decode(out, raw); err != nil {
		return nil
	}
	return out
}

// signed returns the signed data of a signature that has some.
func (s *Signature) signed() (*cms.SignedData, error) {
	switch s.SubFilter {
	case "adbe.pkcs7.detached", "ETSI.CAdES.detached", "adbe.pkcs7.sha1", "ETSI.RFC3161":
		return cms.Parse(s.contents())
	}
	return nil, fmt.Errorf("%w: subfilter %q", ErrSignatureUnsupported, s.SubFilter)
}

// digest returns the digest of the bytes the signature covers.
func (s *Signature) digest(h crypto.Hash) ([]byte, error) {
	if len(s.ranges) == 0 {
		return nil, fmt.Errorf("%w: no byte range", ErrSignatureUnsupported)
	}
	hh := h.New()
	for _, r := range s.ranges {
		if _, err := io.Copy(hh, s.doc.r.SourceRange(r[0], r[1])); err != nil {
			return nil, fmt.Errorf("cera: %w", err)
		}
	}
	return hh.Sum(nil), nil
}

// Verify checks the signature: that the bytes it covers are those signed
// and the signature is its certificate's, what was appended to the file
// after it, and — with roots in opt — whether the certificate is trusted.
// It returns [ErrSignatureUnsupported] for a signature it cannot check; a
// signature that is checked and does not hold is a status with Intact
// false, not an error.
func (s *Signature) Verify(opt VerifyOptions) (SignatureStatus, error) {
	var st SignatureStatus
	unsupported := func(err error) (SignatureStatus, error) {
		return SignatureStatus{}, fmt.Errorf("%w: %v", ErrSignatureUnsupported, err)
	}
	failed := func(err error) bool { return errors.Is(err, cms.ErrDigest) || errors.Is(err, cms.ErrSignature) }

	// The bytes covered are the file from its start, around the
	// signature data and nothing else: a range that leaves out more would
	// leave room for what nobody signed.
	if len(s.ranges) != 2 || s.ranges[0][0] != 0 {
		return unsupported(errors.New("the byte range is not the file around the signature"))
	}
	data := s.contents()
	if len(data) == 0 {
		return unsupported(errors.New("the byte range does not leave out the signature data alone"))
	}
	if own, ok := s.doc.resolve(s.dict.Get("Contents")).Str(); ok && !s.doc.r.Encrypted() && !bytes.Equal(own, data) {
		return unsupported(errors.New("the byte range leaves out something else than the signature"))
	}

	var tsCert *x509.Certificate
	switch s.SubFilter {
	case "adbe.pkcs7.detached", "ETSI.CAdES.detached":
		sd, err := s.signed()
		if err != nil {
			return unsupported(err)
		}
		digest, err := s.digest(sd.Hash())
		if err != nil {
			return st, err
		}
		st.Certificate, st.Certificates = sd.Signer, sd.Certificates
		err = sd.Verify(digest, nil)
		if err != nil && !failed(err) {
			return unsupported(err)
		}
		st.Intact = err == nil
		if ts := sd.SignatureTimestamp(); ts != nil {
			st.Timestamped, tsCert = true, ts.Signed.Signer
		}
	case "adbe.pkcs7.sha1":
		// The signed data holds the SHA-1 digest of the bytes covered.
		sd, err := s.signed()
		if err != nil {
			return unsupported(err)
		}
		digest, err := s.digest(crypto.SHA1)
		if err != nil {
			return st, err
		}
		st.Certificate, st.Certificates = sd.Signer, sd.Certificates
		err = sd.VerifyContent(nil)
		if err != nil && !failed(err) {
			return unsupported(err)
		}
		st.Intact = err == nil && bytes.Equal(sd.Content, digest)
	case "adbe.x509.rsa_sha1":
		// A bare signature over the SHA-1 digest; the certificates are
		// in /Cert, a string or an array of them, the signer's first.
		var certs [][]byte
		switch c := s.doc.resolve(s.dict.Get("Cert")); c.Kind() {
		case pdf.KindString:
			b, _ := c.Str()
			certs = append(certs, b)
		case pdf.KindArray:
			arr, _ := c.Array()
			for _, o := range arr {
				if b, ok := s.doc.resolve(o).Str(); ok {
					certs = append(certs, b)
				}
			}
		}
		for _, der := range certs {
			if c, err := x509.ParseCertificate(der); err == nil {
				st.Certificates = append(st.Certificates, c)
			}
		}
		if len(st.Certificates) == 0 {
			return unsupported(cms.ErrNoSigner)
		}
		st.Certificate = st.Certificates[0]
		digest, err := s.digest(crypto.SHA1)
		if err != nil {
			return st, err
		}
		err = cms.VerifyRaw(st.Certificate, digest, s.contents())
		if err != nil && !failed(err) {
			return unsupported(err)
		}
		st.Intact = err == nil
	case "ETSI.RFC3161":
		// A document timestamp: a token over the bytes covered.
		ts, err := cms.ParseTimestamp(s.contents())
		if err != nil && !failed(err) {
			return unsupported(err)
		}
		if err == nil {
			digest, derr := s.digest(ts.Hash)
			if derr != nil {
				return st, derr
			}
			st.Certificate, st.Certificates = ts.Signed.Signer, ts.Signed.Certificates
			st.Intact = bytes.Equal(digest, ts.Digest)
			st.Timestamped, tsCert = true, ts.Signed.Signer
		}
	default:
		return unsupported(fmt.Errorf("subfilter %q", s.SubFilter))
	}
	st.Timestamper = tsCert
	if !st.Intact {
		return st, nil
	}

	st.Changes = s.changes()
	st.ChangesAllowed = s.allowed(st.Changes)

	if opt.Roots != nil && st.Certificate != nil {
		at := opt.At
		if at.IsZero() {
			at = s.Time
		}
		if at.IsZero() {
			at = time.Now()
		}
		inter := x509.NewCertPool()
		if opt.Intermediates != nil {
			inter = opt.Intermediates.Clone()
		}
		for _, c := range st.Certificates {
			if c != st.Certificate {
				inter.AddCert(c)
			}
		}
		_, err := st.Certificate.Verify(x509.VerifyOptions{
			Roots: opt.Roots, Intermediates: inter, CurrentTime: at, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		})
		st.Trusted, st.TrustError = err == nil, err
	}
	return st, nil
}

// allowed reports whether the changes after the signature are what the
// document's certification and the lock of the signature's field allow.
func (s *Signature) allowed(c Changes) bool {
	if c.Other {
		return false
	}
	mdp := s.doc.signing().mdp // of the document: it binds every later change
	switch {
	case mdp == 1 && c.Any():
		return false
	case mdp == 2 && c.Annotated:
		return false
	}
	if s.lock != nil {
		for _, name := range c.Fields {
			if s.lock.locks(name) {
				return false
			}
		}
	}
	return true
}

// changes compares the document as it was signed with the document as it
// is: the file up to the end of what the signature covers, opened on its
// own, against the whole file.
func (s *Signature) changes() Changes {
	d := s.doc
	if s.CoversWholeFile {
		return Changes{}
	}
	old, err := d.r.Revision(s.end)
	if err != nil {
		return Changes{Other: true}
	}
	od := newDocument(old)
	var c Changes
	cmp := &objectCompare{a: od, b: d, seen: map[[2]pdf.Ref]bool{}, left: maxCompareNodes}

	// The pages: as many, each with the same content and resources.
	if od.NumPages() != d.NumPages() {
		c.Other = true
		return c
	}
	for i := range d.NumPages() {
		pa, erra := od.r.Page(i + 1)
		pb, errb := d.r.Page(i + 1)
		if erra != nil || errb != nil || !cmp.dicts(pa, pb, "Annots", "Parent", "StructParents", "Tabs") {
			c.Other = true
			return c
		}
		// Its annotations: widgets belong to the form, below.
		pageA, _ := od.Page(i)
		pageB, _ := d.Page(i)
		if pageA == nil || pageB == nil {
			c.Other = true
			return c
		}
		notWidgets := func(p *Page) []*Annotation {
			var out []*Annotation
			annots := p.Annotations()
			for k := range annots {
				if annots[k].Subtype != "Widget" {
					out = append(out, &annots[k])
				}
			}
			return out
		}
		aa, ab := notWidgets(pageA), notWidgets(pageB)
		if len(aa) != len(ab) {
			c.Annotated = true
			continue
		}
		for k := range aa {
			if !cmp.dicts(aa[k].dict, ab[k].dict, "P", "Parent", "Popup", "IRT") {
				c.Annotated = true
				break
			}
		}
	}
	if cmp.left <= 0 {
		c.Other = true // too much to compare: not known to be the same
		return c
	}

	// The form: values changed, signatures added.
	fa, fb := od.Form(), d.Form()
	if fa == nil || fb == nil {
		c.Other = fa != fb
		return c
	}
	// Fields by their objects, which an update keeps: two fields of a
	// form may have one name.
	byRef := map[pdf.Ref]*Field{}
	for _, f := range fa.Fields {
		if f.ref.Num > 0 {
			byRef[f.ref] = f
		}
	}
	find := func(fs *Form, byRef map[pdf.Ref]*Field, f *Field) *Field {
		if f.ref.Num > 0 {
			if g, ok := byRef[f.ref]; ok {
				return g
			}
			return nil
		}
		return fs.Field(f.Name)
	}
	for _, f := range fb.Fields {
		before := find(fa, byRef, f)
		// A document timestamp is no change (PDF 2.0, 12.8.2.2.2).
		stamp := func() bool {
			t, _ := d.name(d.dict(d.dict(f.ref.Object()).Get("V")).Get("Type"))
			return t == "DocTimeStamp"
		}
		switch {
		case f.Type == FieldSignature && (before == nil || od.dict(od.dict(before.ref.Object()).Get("V")).IsZero()) && stamp():
		case before == nil && f.Type == FieldSignature:
			c.Signed = true
		case before == nil:
			c.Other = true // a field was added
		case f.Type != before.Type:
			c.Other = true
		case f.Type == FieldSignature:
			signedBefore := !od.dict(od.dict(before.ref.Object()).Get("V")).IsZero()
			signedNow := !d.dict(d.dict(f.ref.Object()).Get("V")).IsZero()
			if signedNow && !signedBefore {
				c.Signed = true
			} else if signedNow != signedBefore {
				c.Other = true
			}
		case !f.Saved.Equal(before.Saved):
			c.FormFilled = true
			c.Fields = append(c.Fields, f.Name)
		}
	}
	now := map[pdf.Ref]*Field{}
	for _, f := range fb.Fields {
		if f.ref.Num > 0 {
			now[f.ref] = f
		}
	}
	for _, f := range fa.Fields {
		if find(fb, now, f) == nil {
			c.Other = true // a field was removed
		}
	}
	return c
}

// objectCompare compares objects of two documents by what they hold,
// following references in each.
type objectCompare struct {
	a, b *Document
	seen map[[2]pdf.Ref]bool // pairs being compared, or found the same
	left int                 // objects it may still look at
}

// dicts reports whether a and b have the same entries but for those named.
func (c *objectCompare) dicts(a, b pdf.Dict, but ...pdf.Name) bool {
	n := 0
	for k, v := range a.All() {
		if slices.Contains(but, k) {
			continue
		}
		n++
		if !c.same(v, b.Get(k)) {
			return false
		}
	}
	for k := range b.All() {
		if !slices.Contains(but, k) {
			n--
		}
	}
	return n == 0
}

// same reports whether x of a and y of b are the same object.
func (c *objectCompare) same(x, y pdf.Object) bool {
	if c.left--; c.left <= 0 {
		return false
	}
	rx, okx := x.Ref()
	ry, oky := y.Ref()
	if okx && oky {
		// A page is named by its place: what is on it is compared
		// page by page, and its /Annots change with every signature.
		if px, py := c.a.pageIndex(rx), c.b.pageIndex(ry); px >= 0 || py >= 0 {
			return px == py
		}
		pair := [2]pdf.Ref{rx, ry}
		if c.seen[pair] {
			return true // already compared, or being compared further up
		}
		c.seen[pair] = true
	}
	x, y = c.a.resolve(x), c.b.resolve(y)
	if x.Kind() != y.Kind() {
		return false
	}
	switch x.Kind() {
	case pdf.KindDict:
		dx, _ := x.Dict()
		dy, _ := y.Dict()
		return c.dicts(dx, dy)
	case pdf.KindArray:
		ax, _ := x.Array()
		ay, _ := y.Array()
		if len(ax) != len(ay) {
			return false
		}
		for i := range ax {
			if !c.same(ax[i], ay[i]) {
				return false
			}
		}
		return true
	case pdf.KindStream:
		sx, _ := x.Stream()
		sy, _ := y.Stream()
		return c.dicts(sx.Dict, sy.Dict) && bytes.Equal(c.a.r.Raw(sx), c.b.r.Raw(sy))
	case pdf.KindString:
		bx, _ := x.Str()
		by, _ := y.Str()
		return bytes.Equal(bx, by)
	}
	return x.String() == y.String() // names, numbers, booleans, null
}
