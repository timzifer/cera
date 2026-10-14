package cera

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// Validation data (PDF 2.0, 12.8.4.3; ETSI EN 319 142-1, PAdES B-LT): what
// checks a signature years later, when the certificates have expired and
// whoever answered for their revocation may be gone. It goes into the
// document security store, /DSS of the catalogue, in an update after the
// signatures; a document timestamp over it ([Editor.Timestamp]) makes them
// B-LTA.

// Validation is validation data of signatures, fetched by the caller: the
// certificates of the chains — the signer's, a timestamp authority's, an
// OCSP responder's —, OCSP responses (RFC 6960, the DER OCSPResponse) and
// certificate revocation lists (RFC 5280, the DER CertificateList).
// [Signature.Verify] says what certificates a signature carries.
type Validation struct {
	Certificates []*x509.Certificate
	OCSPs        [][]byte
	CRLs         [][]byte
}

// AddValidation records validation data, to be written into the document
// security store by [Editor.Update], [Editor.Sign] and [Editor.Timestamp]:
// for the signature sig of the edited document, whose entry in the store
// (/VRI) names it, or with sig nil for the document. What the store has
// stays, and what it has already is not stored twice. Validation data is
// no change by the rules of a certification (PDF 2.0, 12.8.2.2.2).
func (e *Editor) AddValidation(sig *Signature, v Validation) error {
	if e.base == nil {
		return errors.New("cera: a new document has no signatures to validate")
	}
	if sig != nil && sig.doc != e.base {
		return errors.New("cera: the signature is not of the edited document")
	}
	for _, b := range append(append([][]byte(nil), v.OCSPs...), v.CRLs...) {
		var raw asn1.RawValue
		if rest, err := asn1.Unmarshal(b, &raw); err != nil || len(rest) > 0 {
			return errors.New("cera: an OCSP response or a revocation list is not one DER value")
		}
	}
	for _, c := range v.Certificates {
		if c == nil || len(c.Raw) == 0 {
			return errors.New("cera: a certificate without its DER")
		}
	}
	e.validation = append(e.validation, validationFor{sig, v})
	return nil
}

// validationFor is validation data AddValidation recorded.
type validationFor struct {
	sig *Signature
	v   Validation
}

// vriKey returns the key of the signature's entry in the document
// security store: the SHA-1 digest of its /Contents, in upper-case
// hexadecimal; for a document timestamp the digest of the token, without
// the padding after it (ETSI EN 319 142-1, 5.4.2.2).
func (s *Signature) vriKey() string {
	b := s.contents()
	if s.SubFilter == "ETSI.RFC3161" {
		var raw asn1.RawValue
		if _, err := asn1.Unmarshal(b, &raw); err == nil {
			b = raw.FullBytes
		}
	}
	sum := sha1.Sum(b)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// dssPlan writes the validation data recorded into the document security
// store of the edited document: its /DSS with the new data added.
func (e *Editor) dssPlan(doc *pdfedit.Doc) {
	d := e.base
	root, ok := d.r.Trailer().Get("Root").Ref()
	if !ok || len(e.validation) == 0 {
		return
	}
	cat, _ := d.r.Catalog()
	dssObj := cat.Get("DSS")
	dss := d.dict(dssObj)

	// What the store has, by kind: its streams, and the sums of their
	// data, which are not stored again.
	type kind struct {
		key, vriKey pdf.Name
		list        pdf.Array
		sums        map[[32]byte]pdf.Object
	}
	kinds := []*kind{{key: "Certs", vriKey: "Cert"}, {key: "OCSPs", vriKey: "OCSP"}, {key: "CRLs", vriKey: "CRL"}}
	for _, k := range kinds {
		k.sums = map[[32]byte]pdf.Object{}
		list, _ := d.resolve(dss.Get(k.key)).Array()
		k.list = append(pdf.Array(nil), list...)
		for _, o := range list {
			if s, ok := d.resolve(o).Stream(); ok {
				k.sums[sha256.Sum256(d.r.Decode(s).Data)] = o
			}
		}
	}
	// add returns the stream of data in the store, added where it is not.
	add := func(k *kind, data []byte) pdf.Object {
		sum := sha256.Sum256(data)
		if o, ok := k.sums[sum]; ok {
			return o
		}
		doc.Objects = append(doc.Objects, pdfwrite.Flate(pdf.NewDict(), data).Object())
		o := pdfedit.NewRef(len(doc.Objects) - 1).Object()
		k.sums[sum] = o
		k.list = append(k.list, o)
		return o
	}

	// The entries of signatures: the store's, and those of the data
	// recorded, merged by signature.
	vri := map[string][3]pdf.Array{}
	var keys []string
	old := d.dict(dss.Get("VRI"))
	for k, v := range old.All() {
		var lists [3]pdf.Array
		for i, kd := range kinds {
			a, _ := d.resolve(d.dict(v).Get(kd.vriKey)).Array()
			lists[i] = append(pdf.Array(nil), a...)
		}
		vri[string(k)] = lists
		keys = append(keys, string(k))
	}
	for _, rec := range e.validation {
		datas := [3][][]byte{nil, rec.v.OCSPs, rec.v.CRLs}
		for _, c := range rec.v.Certificates {
			datas[0] = append(datas[0], c.Raw)
		}
		var refs [3]pdf.Array
		for i, kd := range kinds {
			for _, b := range datas[i] {
				refs[i] = append(refs[i], add(kd, b))
			}
		}
		if rec.sig == nil {
			continue
		}
		key := rec.sig.vriKey()
		lists, had := vri[key]
		if !had {
			keys = append(keys, key)
		}
		for i := range lists {
			for _, r := range refs[i] {
				if !containsObj(lists[i], r) {
					lists[i] = append(lists[i], r)
				}
			}
		}
		vri[key] = lists
	}

	entries := []pdf.Entry{{Key: "Type", Val: pdf.Name("DSS").Object()}}
	for k, v := range dss.All() {
		switch k {
		case "Type", "Certs", "OCSPs", "CRLs", "VRI":
		default:
			entries = append(entries, pdf.Entry{Key: k, Val: v})
		}
	}
	for _, k := range kinds {
		if len(k.list) > 0 {
			entries = append(entries, pdf.Entry{Key: k.key, Val: k.list.Object()})
		}
	}
	if len(keys) > 0 {
		var ve []pdf.Entry
		for _, key := range keys {
			lists := vri[key]
			// What else an old entry says (TU, TS) stays.
			e := []pdf.Entry{{Key: "Type", Val: pdf.Name("VRI").Object()}}
			for k, v := range d.dict(old.Get(pdf.Name(key))).All() {
				switch k {
				case "Type", "Cert", "OCSP", "CRL":
				default:
					e = append(e, pdf.Entry{Key: k, Val: v})
				}
			}
			for i, kd := range kinds {
				if len(lists[i]) > 0 {
					e = append(e, pdf.Entry{Key: kd.vriKey, Val: lists[i].Object()})
				}
			}
			ve = append(ve, pdf.Entry{Key: pdf.Name(key), Val: pdf.NewDict(e...).Object()})
		}
		entries = append(entries, pdf.Entry{Key: "VRI", Val: pdf.NewDict(ve...).Object()})
	}
	set := []pdf.Entry{{Key: "DSS", Val: pdf.NewDict(entries...).Object()}}
	if r, ok := dssObj.Ref(); ok && !dss.IsZero() {
		// The store is an object of its own: changed in place.
		doc.Patches = append(doc.Patches, pdfedit.Patch{Ref: r, Set: entries})
		return
	}
	doc.Patches = append(doc.Patches, pdfedit.Patch{Ref: root, Set: set})
}

// containsObj reports whether a holds o.
func containsObj(a pdf.Array, o pdf.Object) bool {
	for _, x := range a {
		if x == o {
			return true
		}
	}
	return false
}
