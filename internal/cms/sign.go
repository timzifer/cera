package cms

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"math/big"
	"time"
)

type attributeOut struct {
	Type   asn1.ObjectIdentifier
	Values []asn1.RawValue `asn1:"set"`
}

// Sign makes signed data with one signer over content that is elsewhere (a
// detached signature): digest is its SHA-256 digest. The signer's
// certificate cert and the certificates of chain go into the data, and the
// time at as the signing time the signer claims. The key is an RSA key
// (PKCS #1 v1.5) or an ECDSA key.
func Sign(digest []byte, cert *x509.Certificate, key crypto.Signer, at time.Time, chain ...*x509.Certificate) ([]byte, error) {
	return sign(oidData, nil, digest, cert, key, &at, chain)
}

// SignCAdES is Sign as PAdES asks for it (ETSI EN 319 142-1, the baseline
// signature B-B): the signed attributes name the signer's certificate by
// its digest (signing-certificate-v2, RFC 5035), so that no other
// certificate of the same key stands for it, and claim no signing time,
// which the signature dictionary of the document does.
func SignCAdES(digest []byte, cert *x509.Certificate, key crypto.Signer, chain ...*x509.Certificate) ([]byte, error) {
	return sign(oidData, nil, digest, cert, key, nil, chain)
}

// Room returns how many bytes signed data of Sign or SignCAdES takes at
// most, for the room a document leaves for it.
func Room(cert *x509.Certificate, chain ...*x509.Certificate) int {
	n := 1024 // the structure, the attributes, the names
	for _, c := range append([]*x509.Certificate{cert}, chain...) {
		n += len(c.Raw)
	}
	// Twice the issuer, once in the signer info.
	n += len(cert.RawIssuer)
	if pub, ok := cert.PublicKey.(*rsa.PublicKey); ok {
		return n + pub.Size()
	}
	return n + 160 // an ECDSA signature of P-521
}

// SignContent makes signed data that carries its content: data, or with
// timestamp the info of a timestamp token (RFC 3161, TSTInfo).
func SignContent(content []byte, timestamp bool, cert *x509.Certificate, key crypto.Signer, at time.Time, chain ...*x509.Certificate) ([]byte, error) {
	digest := sha256.Sum256(content)
	typ := oidData
	if timestamp {
		typ = oidTSTInfo
	}
	return sign(typ, content, digest[:], cert, key, &at, chain)
}

// TimestampInfo returns the info of a timestamp token that stamps the
// SHA-256 digest at the time at, for SignContent.
func TimestampInfo(digest []byte, at time.Time) ([]byte, error) {
	return asn1.Marshal(struct {
		Version int
		Policy  asn1.ObjectIdentifier
		Imprint messageImprint
		Serial  *big.Int
		GenTime time.Time `asn1:"generalized"`
	}{1, asn1.ObjectIdentifier{1, 2, 3, 4}, messageImprint{pkix.AlgorithmIdentifier{Algorithm: oidSHA256}, digest}, big.NewInt(1), at.UTC()})
}

// sign makes the signed data. With a time at, the signer claims it; without
// one, the attributes name the signer's certificate instead.
func sign(contentType asn1.ObjectIdentifier, content, digest []byte, cert *x509.Certificate, key crypto.Signer, at *time.Time, chain []*x509.Certificate) ([]byte, error) {
	const h = crypto.SHA256
	var sigAlg asn1.ObjectIdentifier
	switch key.Public().(type) {
	case *rsa.PublicKey:
		sigAlg = oidRSASHA256
	case *ecdsa.PublicKey:
		sigAlg = oidECSHA256
	default:
		return nil, fmt.Errorf("%w: a %T", ErrUnsupported, key.Public())
	}
	value := func(v any) asn1.RawValue {
		b, err := asn1.Marshal(v)
		if err != nil {
			panic(err) // values of fixed types
		}
		return asn1.RawValue{FullBytes: b}
	}
	attrs := []attributeOut{
		{Type: oidContentType, Values: []asn1.RawValue{value(contentType)}},
		{Type: oidMessageDigest, Values: []asn1.RawValue{value(digest)}},
	}
	if at != nil {
		attrs = append(attrs, attributeOut{Type: oidSigningTime, Values: []asn1.RawValue{value(at.UTC())}})
	} else {
		// SigningCertificateV2: the certificates, each by its digest, whose
		// algorithm is SHA-256 unless said.
		type certID struct{ Hash []byte }
		sum := sha256.Sum256(cert.Raw)
		attrs = append(attrs, attributeOut{Type: oidSigningCertV2, Values: []asn1.RawValue{
			value(struct{ Certs []certID }{[]certID{{sum[:]}}})}})
	}
	set, err := asn1.MarshalWithParams(attrs, "set")
	if err != nil {
		return nil, err
	}
	hh := h.New()
	hh.Write(set)
	sig, err := key.Sign(rand.Reader, hh.Sum(nil), h)
	if err != nil {
		return nil, err
	}
	signedAttrs := append([]byte{}, set...)
	signedAttrs[0] = 0xa0 // [0] IMPLICIT in the signer info

	sid, err := asn1.Marshal(issuerAndSerial{Issuer: asn1.RawValue{FullBytes: cert.RawIssuer}, Serial: cert.SerialNumber})
	if err != nil {
		return nil, err
	}
	var certs []byte
	for _, c := range append([]*x509.Certificate{cert}, chain...) {
		certs = append(certs, c.Raw...)
	}
	encap := encapContent{Type: contentType}
	if content != nil {
		octets, err := asn1.Marshal(content)
		if err != nil {
			return nil, err
		}
		encap.Content = asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: octets}
	}
	sd := signedData{
		Version:          1,
		DigestAlgorithms: []pkix.AlgorithmIdentifier{{Algorithm: oidSHA256}},
		Encap:            encap,
		Certificates:     asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: certs},
		SignerInfos: []signerInfo{{
			Version:            1,
			SID:                asn1.RawValue{FullBytes: sid},
			DigestAlgorithm:    pkix.AlgorithmIdentifier{Algorithm: oidSHA256},
			SignedAttrs:        asn1.RawValue{FullBytes: signedAttrs},
			SignatureAlgorithm: pkix.AlgorithmIdentifier{Algorithm: sigAlg},
			Signature:          sig,
		}},
	}
	inner, err := asn1.Marshal(sd)
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(contentInfo{
		ContentType: oidSignedData,
		Content:     asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: inner},
	})
}
