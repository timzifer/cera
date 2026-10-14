package cera

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/timzifer/cera/internal/cms"
	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// stampedAt is the time of the tokens of testAuthority.
var stampedAt = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

// testAuthority is a timestamp authority of s: it stamps the digest it is
// given at stampedAt, answering with the token, or with response a
// TimeStampResp that holds it.
func testAuthority(s testSigner, response bool) TimestampFunc {
	return func(ctx context.Context, digest []byte) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := cms.TimestampInfo(digest, stampedAt)
		if err != nil {
			return nil, err
		}
		tok, err := cms.SignContent(info, true, s.cert, s.key, stampedAt)
		if err != nil || !response {
			return tok, err
		}
		return asn1.Marshal(struct {
			Status struct{ Status int }
			Token  asn1.RawValue
		}{Token: asn1.RawValue{FullBytes: tok}})
	}
}

// TestSignTimestamp signs with a timestamp of an authority (B-T): the time
// of the signature is the token's.
func TestSignTimestamp(t *testing.T) {
	ada, tsa := ecSigner(t, "Ada Lovelace"), ecSigner(t, "Time Authority")
	src := viewFile()
	for _, response := range []bool{false, true} {
		opt := signOptions(ada)
		opt.Timestamp = testAuthority(tsa, response)
		signed := signWith(t, editOpen(t, src).Edit(), src, opt)
		s, st := intact(t, editOpen(t, signed), 0, ada)
		if !st.Timestamped || st.Timestamper == nil || !st.Timestamper.Equal(tsa.cert) || !s.Time.Equal(stampedAt) {
			t.Errorf("response %v: timestamped %v by %v at %v", response, st.Timestamped, st.Timestamper, s.Time)
		}
	}

	// An authority that refuses, one that stamps something else, and a
	// context that is done.
	refuse := func(context.Context, []byte) ([]byte, error) {
		return asn1.Marshal(struct{ Status struct{ Status int } }{struct{ Status int }{2}})
	}
	other := func(ctx context.Context, digest []byte) ([]byte, error) {
		return testAuthority(tsa, false)(ctx, bytes.Repeat([]byte{1}, len(digest)))
	}
	failing := errors.New("no network")
	down := func(context.Context, []byte) ([]byte, error) { return nil, failing }
	for name, ts := range map[string]TimestampFunc{"refused": refuse, "another digest": other, "down": down} {
		opt := signOptions(ada)
		opt.Timestamp = ts
		var out bytes.Buffer
		err := editOpen(t, src).Edit().Sign(&out, opt)
		if err == nil || out.Len() != 0 {
			t.Errorf("%s: %v, %d bytes written", name, err, out.Len())
		}
		if name == "down" && !errors.Is(err, failing) {
			t.Errorf("%s: %v", name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opt := signOptions(ada)
	opt.Timestamp = testAuthority(tsa, false)
	if err := editOpen(t, src).Edit().SignContext(ctx, &bytes.Buffer{}, opt); !errors.Is(err, context.Canceled) {
		t.Errorf("a context that is done: %v", err)
	}
}

// TestDocumentTimestamp stamps a signed document (ETSI.RFC3161), also one
// certified to allow no changes: the timestamp is no change.
func TestDocumentTimestamp(t *testing.T) {
	ada, tsa := ecSigner(t, "Ada Lovelace"), ecSigner(t, "Time Authority")
	src := viewFile()
	for _, certify := range []int{0, 1} {
		opt := signOptions(ada)
		opt.Certify = certify
		signed := signWith(t, editOpen(t, src).Edit(), src, opt)
		var out bytes.Buffer
		if err := editOpen(t, signed).Edit().Timestamp(context.Background(), &out, testAuthority(tsa, true)); err != nil {
			t.Fatalf("certify %d: %v", certify, err)
		}
		if !bytes.HasPrefix(out.Bytes(), signed) {
			t.Fatal("the stamped file does not start with the signed one")
		}
		d := editOpen(t, out.Bytes())
		_, st := intact(t, d, 0, ada)
		if st.Changes.Any() || !st.ChangesAllowed {
			t.Errorf("certify %d: the signature sees changes %+v, allowed %v", certify, st.Changes, st.ChangesAllowed)
		}
		sigs := d.Signatures()
		if len(sigs) != 2 {
			t.Fatalf("%d signatures", len(sigs))
		}
		s := sigs[1]
		roots := x509.NewCertPool()
		roots.AddCert(tsa.cert)
		ts, err := s.Verify(VerifyOptions{Roots: roots})
		if err != nil {
			t.Fatal(err)
		}
		if s.SubFilter != "ETSI.RFC3161" || !s.CoversWholeFile || !ts.Intact || !ts.Trusted || !ts.Timestamped || !s.Time.Equal(stampedAt) {
			t.Errorf("certify %d: the timestamp %+v, %+v", certify, s, ts)
		}
		if typ, _ := d.name(s.dict.Get("Type")); typ != "DocTimeStamp" {
			t.Errorf("the timestamp's dictionary is of type %q", typ)
		}
	}
	// One that does not stamp the document.
	other := func(ctx context.Context, digest []byte) ([]byte, error) {
		return testAuthority(tsa, false)(ctx, make([]byte, len(digest)))
	}
	if err := editOpen(t, src).Edit().Timestamp(context.Background(), &bytes.Buffer{}, other); err == nil {
		t.Error("a token of another digest was written")
	}
}

// fakeOCSP is a DER value standing for an OCSP response.
func fakeOCSP(t *testing.T, n int) []byte {
	b, err := asn1.Marshal(struct{ N int }{n})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAddValidation writes validation data into the document security
// store (B-LT), adds to a store that is there, and stamps it (B-LTA).
func TestAddValidation(t *testing.T) {
	ada, tsa := ecSigner(t, "Ada Lovelace"), ecSigner(t, "Time Authority")
	src := viewFile()
	opt := signOptions(ada)
	opt.Certify = 1
	opt.Timestamp = testAuthority(tsa, false)
	signed := signWith(t, editOpen(t, src).Edit(), src, opt)
	crl, err := x509.CreateRevocationList(nil, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: signedAt, NextUpdate: signedAt.AddDate(0, 1, 0)}, ada.cert, ada.key)
	if err != nil {
		t.Fatal(err)
	}

	d := editOpen(t, signed)
	sig := &d.Signatures()[0]
	e := d.Edit()
	if err := e.AddValidation(sig, Validation{Certificates: []*x509.Certificate{ada.cert, tsa.cert}, OCSPs: [][]byte{fakeOCSP(t, 1)}, CRLs: [][]byte{crl}}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddValidation(nil, Validation{Certificates: []*x509.Certificate{ada.cert}}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddValidation(sig, Validation{OCSPs: [][]byte{[]byte("not DER")}}); err == nil {
		t.Error("an OCSP response that is no DER was taken")
	}
	var out bytes.Buffer
	if err := e.Update(&out); err != nil {
		t.Fatal(err)
	}
	d2 := editOpen(t, out.Bytes())
	_, st := intact(t, d2, 0, ada)
	if st.Changes.Any() || !st.ChangesAllowed {
		t.Errorf("the certified signature sees changes %+v", st.Changes)
	}
	// The key of the signature: SHA-1 of its /Contents, as the file has it.
	raw := sig.contents()
	sum := sha1.Sum(raw)
	key := strings.ToUpper(hex.EncodeToString(sum[:]))
	want := "Certs 2 OCSPs 1 CRLs 1 VRI " + key + " Cert 2 OCSP 1 CRL 1"
	if got := dssString(t, d2); got != want {
		t.Errorf("store %q, want %q", got, want)
	}

	// More for the same signature, part of it stored already, with a
	// document timestamp over it.
	e = d2.Edit()
	if err := e.AddValidation(&d2.Signatures()[0], Validation{Certificates: []*x509.Certificate{tsa.cert}, OCSPs: [][]byte{fakeOCSP(t, 2)}}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := e.Timestamp(context.Background(), &out, testAuthority(tsa, false)); err != nil {
		t.Fatal(err)
	}
	d3 := editOpen(t, out.Bytes())
	want = "Certs 2 OCSPs 2 CRLs 1 VRI " + key + " Cert 2 OCSP 2 CRL 1"
	if got := dssString(t, d3); got != want {
		t.Errorf("store %q, want %q", got, want)
	}
	sigs := d3.Signatures()
	if len(sigs) != 2 || sigs[1].SubFilter != "ETSI.RFC3161" || !sigs[1].CoversWholeFile {
		t.Fatalf("signatures %+v", sigs)
	}
	if _, st := intact(t, d3, 0, ada); st.Changes.Any() || !st.ChangesAllowed {
		t.Errorf("the certified signature sees changes %+v", st.Changes)
	}

	// The store of the timestamp's own entry: by the token, without the
	// padding after it.
	tsKey := sigs[1].vriKey()
	tok, err := cms.Token(sigs[1].contents())
	if err != nil {
		t.Fatal(err)
	}
	sum = sha1.Sum(tok)
	if tsKey != strings.ToUpper(hex.EncodeToString(sum[:])) || len(tok) >= len(sigs[1].contents()) {
		t.Errorf("the key of a document timestamp %s is not that of its token", tsKey)
	}
}

// dssString writes what the document security store of d holds: how many
// certificates, responses and lists, and for each signature as many.
func dssString(t *testing.T, d *Document) string {
	t.Helper()
	cat, _ := d.r.Catalog()
	dss := d.dict(cat.Get("DSS"))
	if typ, _ := d.name(dss.Get("Type")); typ != "DSS" {
		t.Errorf("/DSS of type %q", typ)
	}
	var b strings.Builder
	count := func(dict pdf.Dict, keys ...pdf.Name) {
		for _, k := range keys {
			a, _ := d.resolve(dict.Get(k)).Array()
			for _, o := range a {
				if s, ok := d.resolve(o).Stream(); !ok || len(d.r.Decode(s).Data) == 0 {
					t.Errorf("%s: an entry that is no stream with data", k)
				}
			}
			b.WriteString(" " + string(k) + " " + string(rune('0'+len(a))))
		}
	}
	count(dss, "Certs", "OCSPs", "CRLs")
	b.WriteString(" VRI")
	for k, v := range d.dict(dss.Get("VRI")).All() {
		b.WriteString(" " + string(k))
		count(d.dict(v), "Cert", "OCSP", "CRL")
	}
	return strings.TrimSpace(b.String())
}

// TestSignSameNames signs a form with two fields of one name, with other
// values, twice: the second signature fills in nothing.
func TestSignSameNames(t *testing.T) {
	ada := ecSigner(t, "Ada Lovelace")
	src := testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R /AcroForm <</Fields [10 0 R 11 0 R]>>>>",
			2:  "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 100]>>",
			3:  "<</Type /Page /Parent 2 0 R /Annots [10 0 R 11 0 R]>>",
			10: "<</Type /Annot /Subtype /Widget /FT /Tx /T (same) /V (one) /Rect [10 10 90 30] /P 3 0 R>>",
			11: "<</Type /Annot /Subtype /Widget /FT /Tx /T (same) /V (two) /Rect [10 50 90 70] /P 3 0 R>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
	once := signWith(t, editOpen(t, src).Edit(), src, signOptions(ada))
	twice := signWith(t, editOpen(t, once).Edit(), once, signOptions(ada))
	if _, st := intact(t, editOpen(t, twice), 0, ada); st.Changes.FormFilled || st.Changes.Other || !st.Changes.Signed {
		t.Errorf("changes %+v", st.Changes)
	}
}
