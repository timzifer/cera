package cera

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/timzifer/cera/internal/cms"
	"github.com/timzifer/cera/internal/testpdf"
)

// testSigner is a key with a certificate of its own making.
type testSigner struct {
	key  crypto.Signer
	cert *x509.Certificate
}

func newSigner(t *testing.T, name string, key crypto.Signer) testSigner {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(7),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testSigner{key, cert}
}

var signedAt = time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)

// signedFileWith is a form with a text field "name" and a signature field
// "sig" on one page, signed by s. sigDict is added to the signature
// dictionary (a subfilter, a DocMDP reference), catalog to the catalogue
// and field to the signature field. With rsaSHA1 the signature is a bare
// PKCS #1 signature with the certificate beside it (adbe.x509.rsa_sha1);
// the subfilters adbe.pkcs7.sha1 and ETSI.RFC3161 in sigDict make signed
// data that holds the SHA-1 digest, and a timestamp token.
func signedFileWith(t *testing.T, s testSigner, sigDict, catalog, field string, rsaSHA1 bool) []byte {
	t.Helper()
	const room = 4096 // bytes of signature data
	if sigDict == "" {
		sigDict = "/SubFilter /adbe.pkcs7.detached"
	}
	if rsaSHA1 {
		sigDict += " /Cert <" + hex.EncodeToString(s.cert.Raw) + ">"
	}
	file := testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R /AcroForm <</Fields [10 0 R 11 0 R] /SigFlags 3 /DA (/Helv 10 Tf 0 g) /DR <</Font <</Helv 30 0 R>>>>>> " + catalog + ">>",
			2:  "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200]>>",
			3:  "<</Type /Page /Parent 2 0 R /Contents 4 0 R /Annots [10 0 R 11 0 R]>>",
			4:  testpdf.Stream("", []byte("0.9 g 0 0 300 200 re f")),
			10: "<</Type /Annot /Subtype /Widget /FT /Tx /T (name) /V (Ada) /Rect [10 160 140 180]>>",
			11: "<</Type /Annot /Subtype /Widget /FT /Sig /T (sig) /Rect [10 100 140 140] /V 20 0 R " + field + ">>",
			20: "<</Type /Sig /Filter /Adobe.PPKLite " + sigDict + " /Reason (I agree) /Location (Berlin) /Name (as the dictionary says)" +
				" /M (D:20260101000000Z) /ByteRange [0 0000000000 0000000000 0000000000] /Contents <" + strings.Repeat("0", 2*room) + ">>>",
			30: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
	// The byte range: the file around the string of the signature data.
	start := bytes.Index(file, []byte("/Contents <")) + len("/Contents ")
	end := start + 2*room + 2
	br := fmt.Sprintf("/ByteRange [0 %010d %010d %010d]", start, end, len(file)-end)
	at := bytes.Index(file, []byte("/ByteRange ["))
	copy(file[at:], br)
	covered := append(append([]byte{}, file[:start]...), file[end:]...)
	var data []byte
	var err error
	switch {
	case rsaSHA1:
		digest := sha1.Sum(covered)
		sig, serr := rsa.SignPKCS1v15(rand.Reader, s.key.(*rsa.PrivateKey), crypto.SHA1, digest[:])
		if serr != nil {
			t.Fatal(serr)
		}
		data, err = asn1.Marshal(sig)
	case strings.Contains(sigDict, "adbe.pkcs7.sha1"):
		digest := sha1.Sum(covered)
		data, err = cms.SignContent(digest[:], false, s.cert, s.key, signedAt)
	case strings.Contains(sigDict, "ETSI.RFC3161"):
		digest := sha256.Sum256(covered)
		info, ierr := cms.TimestampInfo(digest[:], signedAt)
		if ierr != nil {
			t.Fatal(ierr)
		}
		data, err = cms.SignContent(info, true, s.cert, s.key, signedAt)
	default:
		digest := sha256.Sum256(covered)
		data, err = cms.Sign(digest[:], s.cert, s.key, signedAt)
	}
	if err != nil || len(data) > room {
		t.Fatalf("signing: %v, %d bytes", err, len(data))
	}
	hex.Encode(file[start+1:], data)
	return file
}

func signedPDF(t *testing.T, s testSigner) []byte {
	return signedFileWith(t, s, "", "", "", false)
}

func ecSigner(t *testing.T, name string) testSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return newSigner(t, name, key)
}

func TestSignatures(t *testing.T) {
	ada := ecSigner(t, "Ada Lovelace")
	src := signedPDF(t, ada)
	doc := editOpen(t, src)
	sigs := doc.Signatures()
	if len(sigs) != 1 {
		t.Fatalf("%d signatures", len(sigs))
	}
	s := &sigs[0]
	// The time is what /M says: the signature has no timestamp.
	if s.Field.Name != "sig" || s.SubFilter != "adbe.pkcs7.detached" || s.Signer != "Ada Lovelace" || s.Reason != "I agree" ||
		s.Location != "Berlin" || !s.CoversWholeFile || s.Certification != 0 || !s.Time.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("signature %+v", s)
	}
	st, err := s.Verify(VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Intact || st.Changes.Any() || !st.ChangesAllowed || st.Trusted || st.TrustError != nil || st.Timestamped ||
		st.Certificate == nil || st.Certificate.Subject.CommonName != "Ada Lovelace" || len(st.Certificates) != 1 {
		t.Errorf("status %+v", st)
	}

	// Trust: with the signer's own certificate as a root, and with
	// another's.
	roots := x509.NewCertPool()
	roots.AddCert(ada.cert)
	if st, _ := s.Verify(VerifyOptions{Roots: roots}); !st.Trusted || st.TrustError != nil {
		t.Errorf("trusted root: %v, %v", st.Trusted, st.TrustError)
	}
	others := x509.NewCertPool()
	others.AddCert(ecSigner(t, "Eve").cert)
	if st, _ := s.Verify(VerifyOptions{Roots: others}); !st.Intact || st.Trusted || st.TrustError == nil {
		t.Errorf("another root: trusted %v, %v", st.Trusted, st.TrustError)
	}
	// Valid from 2026: not at a time before.
	if st, _ := s.Verify(VerifyOptions{Roots: roots, At: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}); st.Trusted {
		t.Error("trusted before the certificate was valid")
	}

	if got := editOpen(t, fieldsFile(false)).Signatures(); got != nil {
		t.Errorf("a document without signatures has %v", got)
	}
}

// TestSignatureTampered finds a change of the bytes a signature covers,
// wherever it is.
func TestSignatureTampered(t *testing.T) {
	ada := ecSigner(t, "Ada")
	src := signedPDF(t, ada)
	change := func(find string) []byte {
		out := bytes.Clone(src)
		at := bytes.Index(out, []byte(find))
		if at < 0 {
			t.Fatalf("no %q in the file", find)
		}
		out[at+len(find)-1] ^= 1
		return out
	}
	for name, find := range map[string]string{
		"the page":        "0.9 g",
		"the value":       "/V (Ada",
		"the reason":      "(I agree",
		"after the range": "/Root 1 0",
	} {
		doc, err := Open(change(find))
		if err != nil {
			continue // a change that broke the file
		}
		sigs := doc.Signatures()
		if len(sigs) != 1 {
			t.Errorf("%s: %d signatures", name, len(sigs))
			continue
		}
		st, err := sigs[0].Verify(VerifyOptions{})
		if err != nil || st.Intact {
			t.Errorf("%s changed: intact %v, %v", name, st.Intact, err)
		}
	}
	// The signature data itself changed: not that key's signature.
	// (Its last bytes are the signature value; before that come the
	// certificates, which the signature does not cover: trust does.)
	bad := bytes.Clone(src)
	from := bytes.Index(bad, []byte("/Contents <")) + len("/Contents <")
	data := bad[from : from+bytes.IndexByte(bad[from:], '>')]
	at := bytes.LastIndexFunc(data, func(r rune) bool { return r != '0' }) - 4
	if data[at] == '0' {
		data[at] = '1'
	} else {
		data[at] = '0'
	}
	if sigs := editOpen(t, bad).Signatures(); len(sigs) == 1 {
		if st, err := sigs[0].Verify(VerifyOptions{}); err == nil && st.Intact {
			t.Error("intact with the signature data changed")
		}
	}

	// A byte range that leaves out more than the signature data: what is
	// in the gap is signed by nobody.
	wide := bytes.Clone(src)
	i := bytes.Index(wide, []byte("/ByteRange [0 ")) + len("/ByteRange [0 ")
	start, err := strconv.Atoi(string(wide[i : i+10]))
	if err != nil {
		t.Fatal(err)
	}
	copy(wide[i:], fmt.Sprintf("%010d", start-20))
	if sigs := editOpen(t, wide).Signatures(); len(sigs) == 1 {
		if st, err := sigs[0].Verify(VerifyOptions{}); !errors.Is(err, ErrSignatureUnsupported) || st.Intact {
			t.Errorf("a wider gap: intact %v, %v", st.Intact, err)
		}
	}
	// A subfilter cera does not know.
	odd := editOpen(t, signedFileWith(t, ada, "/SubFilter /acme.signature", "", "", false))
	if _, err := odd.Signatures()[0].Verify(VerifyOptions{}); !errors.Is(err, ErrSignatureUnsupported) {
		t.Errorf("unknown subfilter: %v", err)
	}
}

// TestSignatureChanges tells what was appended after a signature, and
// whether the certification and the lock of its field allow it.
func TestSignatureChanges(t *testing.T) {
	ada := ecSigner(t, "Ada")
	docMDP := func(p int) (sigDict, catalog string) {
		return fmt.Sprintf("/SubFilter /adbe.pkcs7.detached /Reference [<</Type /SigRef /TransformMethod /DocMDP /TransformParams <</P %d>>>>]", p),
			"/Perms <</DocMDP 20 0 R>>"
	}
	for _, tt := range []struct {
		name    string
		mdp     int
		lock    string
		edit    func(t *testing.T, doc *Document, e *Editor)
		want    Changes
		allowed bool
	}{
		{"nothing", 0, "", nil, Changes{}, true},
		{"form filled", 0, "", fillName, Changes{FormFilled: true, Fields: []string{"name"}}, true},
		{"form filled, certified for no changes", 1, "", fillName, Changes{FormFilled: true, Fields: []string{"name"}}, false},
		{"form filled, certified for forms", 2, "", fillName, Changes{FormFilled: true, Fields: []string{"name"}}, true},
		{"form filled, field locked", 0, "/Lock <</Type /SigFieldLock /Action /Include /Fields [(name)]>>", fillName,
			Changes{FormFilled: true, Fields: []string{"name"}}, false},
		{"form filled, another field locked", 0, "/Lock <</Type /SigFieldLock /Action /Exclude /Fields [(name)]>>", fillName,
			Changes{FormFilled: true, Fields: []string{"name"}}, true},
		{"annotated", 0, "", addNote, Changes{Annotated: true}, true},
		{"annotated, certified for forms", 2, "", addNote, Changes{Annotated: true}, false},
		{"annotated, certified for annotations", 3, "", addNote, Changes{Annotated: true}, true},
		{"page turned", 0, "", func(t *testing.T, _ *Document, e *Editor) {
			if err := e.RotatePage(0, 90); err != nil {
				t.Fatal(err)
			}
		}, Changes{Other: true}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sigDict, catalog := "", ""
			if tt.mdp > 0 {
				sigDict, catalog = docMDP(tt.mdp)
			}
			src := signedFileWith(t, ada, sigDict, catalog, tt.lock, false)
			out := src
			if tt.edit != nil {
				doc := editOpen(t, src)
				e := doc.Edit()
				e.unguarded = true // also what the editor would refuse
				tt.edit(t, doc, e)
				var buf bytes.Buffer
				if err := e.Update(&buf); err != nil {
					t.Fatal(err)
				}
				out = buf.Bytes()
			}
			sigs := editOpen(t, out).Signatures()
			if len(sigs) != 1 {
				t.Fatalf("%d signatures", len(sigs))
			}
			s := &sigs[0]
			if s.Certification != tt.mdp || s.CoversWholeFile != (tt.edit == nil) {
				t.Errorf("certification %d, covers the whole file %v", s.Certification, s.CoversWholeFile)
			}
			st, err := s.Verify(VerifyOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !st.Intact {
				t.Fatal("the signature does not hold after an update")
			}
			if st.Changes.FormFilled != tt.want.FormFilled || st.Changes.Annotated != tt.want.Annotated ||
				st.Changes.Other != tt.want.Other || st.Changes.Signed != tt.want.Signed ||
				strings.Join(st.Changes.Fields, ",") != strings.Join(tt.want.Fields, ",") {
				t.Errorf("changes %+v, want %+v", st.Changes, tt.want)
			}
			if st.ChangesAllowed != tt.allowed {
				t.Errorf("changes allowed: %v, want %v", st.ChangesAllowed, tt.allowed)
			}
		})
	}
}

func fillName(t *testing.T, doc *Document, e *Editor) {
	t.Helper()
	if err := e.SetFields(fill(t, doc, map[string]Value{"name": TextValue("Grace")})); err != nil {
		t.Fatal(err)
	}
}

func addNote(t *testing.T, _ *Document, e *Editor) {
	t.Helper()
	if _, err := e.AddAnnotation(0, NewAnnotation{Subtype: "Text", Contents: "seen", Markup: Markup{Bounds: Rect{200, 20, 220, 40}}}); err != nil {
		t.Fatal(err)
	}
}

// TestSignatureRSASHA1 checks the old bare signatures (adbe.x509.rsa_sha1),
// and a signed-data signature made with an RSA key.
func TestSignatureRSASHA1(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	bob := newSigner(t, "Bob", key)
	for name, src := range map[string][]byte{
		"adbe.x509.rsa_sha1":  signedFileWith(t, bob, "/SubFilter /adbe.x509.rsa_sha1", "", "", true),
		"adbe.pkcs7.detached": signedPDF(t, bob),
		"adbe.pkcs7.sha1":     signedFileWith(t, bob, "/SubFilter /adbe.pkcs7.sha1", "", "", false),
		"ETSI.RFC3161":        signedFileWith(t, bob, "/Type /DocTimeStamp /SubFilter /ETSI.RFC3161", "", "", false),
	} {
		sigs := editOpen(t, src).Signatures()
		if len(sigs) != 1 || sigs[0].SubFilter != name {
			t.Fatalf("%s: %+v", name, sigs)
		}
		st, err := sigs[0].Verify(VerifyOptions{})
		if err != nil || !st.Intact || st.Certificate == nil || st.Certificate.Subject.CommonName != "Bob" {
			t.Errorf("%s: %+v, %v", name, st, err)
		}
		// A document timestamp tells the time, and who stamped it.
		if stamped := name == "ETSI.RFC3161"; st.Timestamped != stamped || stamped && (!sigs[0].Time.Equal(signedAt) || st.Timestamper == nil) {
			t.Errorf("%s: timestamped %v, time %v", name, st.Timestamped, sigs[0].Time)
		}
		bad := bytes.Replace(src, []byte("0.9 g"), []byte("0.8 g"), 1)
		if st, err := editOpen(t, bad).Signatures()[0].Verify(VerifyOptions{}); err != nil || st.Intact {
			t.Errorf("%s, changed: intact %v, %v", name, st.Intact, err)
		}
	}
}
