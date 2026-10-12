package cms

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"
)

// selfSigned makes a certificate for name, signed by itself.
func selfSigned(t *testing.T, name string, key crypto.Signer) *x509.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: name, Organization: []string{"cera"}},
		NotBefore:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestSignAndVerify(t *testing.T) {
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rs, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)
	for name, key := range map[string]crypto.Signer{"ecdsa": ec, "rsa": rs} {
		cert := selfSigned(t, "Ada "+name, key)
		digest := sha256.Sum256([]byte("what is signed"))
		der, err := Sign(digest[:], cert, key, at)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// Padded, as in a PDF.
		sd, err := Parse(append(der, make([]byte, 100)...))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sd.Signer == nil || sd.Signer.Subject.CommonName != "Ada "+name || len(sd.Certificates) != 1 ||
			!sd.SigningTime.Equal(at) || sd.Hash() != crypto.SHA256 || sd.Content != nil {
			t.Errorf("%s: signer %v, time %v", name, sd.Signer, sd.SigningTime)
		}
		if err := sd.Verify(digest[:], nil); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		other := sha256.Sum256([]byte("what is not signed"))
		if err := sd.Verify(other[:], nil); !errors.Is(err, ErrDigest) {
			t.Errorf("%s, other content: %v", name, err)
		}
		// Another key's certificate.
		ec2, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err := sd.Verify(digest[:], selfSigned(t, "Eve", ec2)); !errors.Is(err, ErrSignature) && !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s, another certificate: %v", name, err)
		}
		// A changed signature, and a changed signed attribute (the
		// digest in it, and the time).
		for _, at := range []int{len(der) - 5, bytes.Index(der, digest[:]) + 3, bytes.Index(der, []byte("261012093000Z")) + 2} {
			bad := append([]byte{}, der...)
			bad[at] ^= 1
			if sd, err := Parse(bad); err == nil {
				if err := sd.Verify(digest[:], nil); err == nil {
					t.Errorf("%s: verifies with byte %d changed", name, at)
				}
			}
		}
		if sd.SignatureTimestamp() != nil {
			t.Errorf("%s: a timestamp", name)
		}
	}
	for _, junk := range [][]byte{nil, {0x30}, {0x30, 0x03, 0x02, 0x01, 0x01}, []byte("not asn.1")} {
		if _, err := Parse(junk); !errors.Is(err, ErrSyntax) {
			t.Errorf("%x: %v", junk, err)
		}
	}
}

// TestSignCAdES makes signed data as PAdES asks for it: it names the
// signer's certificate, claims no time, and fits the room told for it.
func TestSignCAdES(t *testing.T) {
	ec, _ := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	rs, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]crypto.Signer{"ecdsa": ec, "rsa": rs} {
		cert := selfSigned(t, "Ada "+name, key)
		ca := selfSigned(t, "An authority", key)
		digest := sha256.Sum256([]byte("what is signed"))
		der, err := SignCAdES(digest[:], cert, key, ca)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if room := Room(cert, ca); len(der) > room || room > len(der)+1200 {
			t.Errorf("%s: %d bytes in a room of %d", name, len(der), room)
		}
		sd, err := Parse(der)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sd.Signer == nil || sd.Signer.Subject.CommonName != "Ada "+name || len(sd.Certificates) != 2 || !sd.SigningTime.IsZero() {
			t.Errorf("%s: signer %v, time %v, %d certificates", name, sd.Signer, sd.SigningTime, len(sd.Certificates))
		}
		if err := sd.Verify(digest[:], nil); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		// The signed attributes hold the digest of the certificate.
		sum := sha256.Sum256(cert.Raw)
		at := bytes.Index(der, sum[:])
		if at < 0 {
			t.Fatalf("%s: the signature does not name its certificate", name)
		}
		bad := append([]byte{}, der...)
		bad[at] ^= 1
		if sd, err := Parse(bad); err == nil {
			if err := sd.Verify(digest[:], nil); err == nil {
				t.Errorf("%s: verifies with the named certificate changed", name)
			}
		}
	}
}
