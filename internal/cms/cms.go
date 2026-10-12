// Package cms reads the signed data of the Cryptographic Message Syntax
// (RFC 5652, PKCS #7) as far as PDF signatures use it, and checks it: the
// digest of what was signed against the signed attributes, the signature
// against the signer's certificate. It reads timestamp tokens (RFC 3161),
// which are signed data too. It uses the standard library alone.
package cms

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	_ "crypto/sha1" // the digests signatures name
	_ "crypto/sha256"
	_ "crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// Errors of reading and checking signed data.
var (
	// ErrSyntax is returned for data that is no signed data cms reads.
	ErrSyntax = errors.New("cms: malformed signed data")
	// ErrUnsupported is returned for an algorithm cms does not know.
	ErrUnsupported = errors.New("cms: unsupported algorithm")
	// ErrDigest is returned when what was signed is not what the
	// signature covers: the content changed.
	ErrDigest = errors.New("cms: the digest of the content is not the one signed")
	// ErrSignature is returned when the signature is not the signer's.
	ErrSignature = errors.New("cms: the signature does not verify")
	// ErrNoSigner is returned when the signer's certificate is not among
	// those the signed data carries.
	ErrNoSigner = errors.New("cms: the signer's certificate is missing")
)

var (
	oidSignedData     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidData           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidTSTInfo        = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 4}
	oidContentType    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest  = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSigningTime    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}
	oidTimeStampToken = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 2, 14}

	oidSHA1   = asn1.ObjectIdentifier{1, 3, 14, 3, 2, 26}
	oidSHA256 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidSHA384 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 2}
	oidSHA512 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}

	oidRSA       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidRSAPSS    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 10}
	oidRSASHA1   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 5}
	oidRSASHA256 = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}
	oidRSASHA384 = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 12}
	oidRSASHA512 = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 13}
	oidECKey     = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	oidECSHA1    = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 1}
	oidECSHA256  = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
	oidECSHA384  = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 3}
	oidECSHA512  = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 4}
)

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,tag:0"`
}

type signedData struct {
	Version          int
	DigestAlgorithms []pkix.AlgorithmIdentifier `asn1:"set"`
	Encap            encapContent
	Certificates     asn1.RawValue `asn1:"optional,tag:0"`
	CRLs             asn1.RawValue `asn1:"optional,tag:1"`
	SignerInfos      []signerInfo  `asn1:"set"`
}

type encapContent struct {
	Type    asn1.ObjectIdentifier
	Content asn1.RawValue `asn1:"optional,explicit,tag:0"`
}

type signerInfo struct {
	Version            int
	SID                asn1.RawValue
	DigestAlgorithm    pkix.AlgorithmIdentifier
	SignedAttrs        asn1.RawValue `asn1:"optional,tag:0"`
	SignatureAlgorithm pkix.AlgorithmIdentifier
	Signature          []byte
	UnsignedAttrs      asn1.RawValue `asn1:"optional,tag:1"`
}

type issuerAndSerial struct {
	Issuer asn1.RawValue
	Serial *big.Int
}

type attribute struct {
	Type   asn1.ObjectIdentifier
	Values asn1.RawValue `asn1:"set"`
}

// SignedData is signed data with one signer, as PDF signatures have it.
type SignedData struct {
	// Certificates are those the data carries: the signer's, and what its
	// writer added of the chain.
	Certificates []*x509.Certificate
	// Signer is the signer's certificate, nil if the data does not carry
	// it.
	Signer *x509.Certificate
	// SigningTime is the time the signer claims (the signing-time
	// attribute); zero if it claims none.
	SigningTime time.Time
	// Content is the content the data encapsulates; nil for a detached
	// signature, whose content is elsewhere.
	Content []byte
	// ContentType says what Content is: data, or the info of a timestamp
	// token.
	ContentType asn1.ObjectIdentifier
	// Timestamp is the timestamp token over the signature, if it has one
	// (the signature-time-stamp attribute); see Timestamp.
	TimestampToken []byte

	si     signerInfo
	digest crypto.Hash
}

// Parse reads signed data from der, which may be followed by padding, as
// the /Contents of a PDF signature is.
func Parse(der []byte) (*SignedData, error) {
	var ci contentInfo
	if _, err := asn1.Unmarshal(der, &ci); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSyntax, err)
	}
	if !ci.ContentType.Equal(oidSignedData) {
		return nil, fmt.Errorf("%w: content type %v", ErrSyntax, ci.ContentType)
	}
	var sd signedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSyntax, err)
	}
	if len(sd.SignerInfos) != 1 {
		return nil, fmt.Errorf("%w: %d signers", ErrSyntax, len(sd.SignerInfos))
	}
	out := &SignedData{si: sd.SignerInfos[0], ContentType: sd.Encap.Type}
	var err error
	if out.digest, err = hashOf(out.si.DigestAlgorithm.Algorithm); err != nil {
		return nil, err
	}
	if len(sd.Encap.Content.Bytes) > 0 {
		// An OCTET STRING, of which only the definite form is read.
		var content []byte
		if _, err := asn1.Unmarshal(sd.Encap.Content.Bytes, &content); err != nil {
			return nil, fmt.Errorf("%w: content: %v", ErrSyntax, err)
		}
		out.Content = content
	}
	// The certificates: a SET of them, of which those that parse are kept.
	for rest := sd.Certificates.Bytes; len(rest) > 0; {
		var raw asn1.RawValue
		var err error
		if rest, err = asn1.Unmarshal(rest, &raw); err != nil {
			break
		}
		if c, err := x509.ParseCertificate(raw.FullBytes); err == nil {
			out.Certificates = append(out.Certificates, c)
		}
	}
	out.Signer = out.findSigner()
	for _, a := range attributes(out.si.SignedAttrs.Bytes) {
		if a.Type.Equal(oidSigningTime) {
			var t time.Time
			if _, err := asn1.Unmarshal(a.Values.Bytes, &t); err == nil {
				out.SigningTime = t
			}
		}
	}
	for _, a := range attributes(out.si.UnsignedAttrs.Bytes) {
		if a.Type.Equal(oidTimeStampToken) {
			out.TimestampToken = a.Values.Bytes
		}
	}
	return out, nil
}

// attributes reads a set of attributes, as far as they parse.
func attributes(b []byte) []attribute {
	var out []attribute
	for len(b) > 0 {
		var a attribute
		var err error
		if b, err = asn1.Unmarshal(b, &a); err != nil {
			break
		}
		out = append(out, a)
	}
	return out
}

// findSigner returns the certificate the signer info names: by issuer and
// serial number, or by subject key identifier.
func (s *SignedData) findSigner() *x509.Certificate {
	sid := s.si.SID
	if sid.Class == asn1.ClassContextSpecific && sid.Tag == 0 {
		for _, c := range s.Certificates {
			if bytes.Equal(c.SubjectKeyId, sid.Bytes) {
				return c
			}
		}
		return nil
	}
	var is issuerAndSerial
	if _, err := asn1.Unmarshal(sid.FullBytes, &is); err != nil || is.Serial == nil {
		return nil
	}
	for _, c := range s.Certificates {
		if c.SerialNumber != nil && c.SerialNumber.Cmp(is.Serial) == 0 && bytes.Equal(c.RawIssuer, is.Issuer.FullBytes) {
			return c
		}
	}
	return nil
}

// Hash returns the digest algorithm of the signer.
func (s *SignedData) Hash() crypto.Hash { return s.digest }

// Verify checks the signature over content digested: digest is the digest,
// by Hash, of the content — of what a detached signature covers, or of
// Content. It returns ErrDigest when the signed attributes name another
// digest, ErrSignature when the signature is not that of the signer's
// certificate, ErrNoSigner without that certificate, and ErrUnsupported
// for an algorithm it does not know. cert, when not nil, is the signer's
// certificate where the data does not carry it.
func (s *SignedData) Verify(digest []byte, cert *x509.Certificate) error {
	if cert == nil {
		cert = s.Signer
	}
	if cert == nil {
		return ErrNoSigner
	}
	signed := digest
	if attrs := s.si.SignedAttrs.Bytes; len(attrs) > 0 {
		var md []byte
		found := false
		for _, a := range attributes(attrs) {
			if a.Type.Equal(oidMessageDigest) {
				_, err := asn1.Unmarshal(a.Values.Bytes, &md)
				found = err == nil
			}
		}
		if !found || !bytes.Equal(md, digest) {
			return ErrDigest
		}
		// The signature is over the attributes as a SET OF, not with
		// the tag [0] they have in the signer info.
		der := append([]byte{}, s.si.SignedAttrs.FullBytes...)
		der[0] = 0x31
		h := s.digest.New()
		h.Write(der)
		signed = h.Sum(nil)
	}
	return verify(cert.PublicKey, s.si.SignatureAlgorithm, s.digest, signed, s.si.Signature)
}

// VerifyContent is Verify for signed data that carries its content.
func (s *SignedData) VerifyContent(cert *x509.Certificate) error {
	h := s.digest.New()
	h.Write(s.Content)
	return s.Verify(h.Sum(nil), cert)
}

// verify checks sig over the digest hashed with h, by the algorithm alg.
func verify(pub crypto.PublicKey, alg pkix.AlgorithmIdentifier, h crypto.Hash, hashed, sig []byte) error {
	oid := alg.Algorithm
	// A signature algorithm that names its digest says the same as the
	// digest algorithm; one that is the key's alone leaves it to that.
	switch k := pub.(type) {
	case *rsa.PublicKey:
		switch {
		case oid.Equal(oidRSAPSS):
			if rsa.VerifyPSS(k, h, hashed, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthAuto}) != nil {
				return ErrSignature
			}
			return nil
		case oid.Equal(oidRSA), oid.Equal(oidRSASHA1), oid.Equal(oidRSASHA256), oid.Equal(oidRSASHA384), oid.Equal(oidRSASHA512):
			if rsa.VerifyPKCS1v15(k, h, hashed, sig) != nil {
				return ErrSignature
			}
			return nil
		}
	case *ecdsa.PublicKey:
		if oid.Equal(oidECKey) || oid.Equal(oidECSHA1) || oid.Equal(oidECSHA256) || oid.Equal(oidECSHA384) || oid.Equal(oidECSHA512) {
			if !ecdsa.VerifyASN1(k, hashed, sig) {
				return ErrSignature
			}
			return nil
		}
	case ed25519.PublicKey:
		// Ed25519 signs the message, not a digest of it: only what is
		// signed without attributes cannot be checked from the digest.
		return fmt.Errorf("%w: Ed25519", ErrUnsupported)
	}
	return fmt.Errorf("%w: signature %v with a %T", ErrUnsupported, oid, pub)
}

// hashOf returns the hash an algorithm identifier names.
func hashOf(oid asn1.ObjectIdentifier) (crypto.Hash, error) {
	switch {
	case oid.Equal(oidSHA1):
		return crypto.SHA1, nil
	case oid.Equal(oidSHA256):
		return crypto.SHA256, nil
	case oid.Equal(oidSHA384):
		return crypto.SHA384, nil
	case oid.Equal(oidSHA512):
		return crypto.SHA512, nil
	}
	return 0, fmt.Errorf("%w: digest %v", ErrUnsupported, oid)
}

// VerifyRaw checks a bare PKCS #1 signature over a SHA-1 digest, as the
// PDF subfilter adbe.x509.rsa_sha1 has it: sig is the signature, or the
// DER OCTET STRING that holds it.
func VerifyRaw(cert *x509.Certificate, sha1Digest, sig []byte) error {
	var inner []byte
	if rest, err := asn1.Unmarshal(sig, &inner); err == nil && len(inner) > 0 && len(bytes.TrimRight(rest, "\x00")) == 0 {
		sig = inner
	}
	k, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("%w: a %T for adbe.x509.rsa_sha1", ErrUnsupported, cert.PublicKey)
	}
	if rsa.VerifyPKCS1v15(k, crypto.SHA1, sha1Digest, sig) != nil {
		return ErrSignature
	}
	return nil
}

// messageImprint is the digest a timestamp token stamps.
type messageImprint struct {
	Algorithm pkix.AlgorithmIdentifier
	Digest    []byte
}

// Timestamp is what a timestamp token says and whom it is from.
type Timestamp struct {
	// Time is when the token was made.
	Time time.Time
	// Hash and Digest are the digest of what the token stamps.
	Hash   crypto.Hash
	Digest []byte
	// Signed is the signed data of the token, signed by the timestamp
	// authority.
	Signed *SignedData
}

// ParseTimestamp reads a timestamp token and checks that its authority
// signed what it says. It does not check that the token stamps anything in
// particular: compare Digest with the digest, by Hash, of what it should.
func ParseTimestamp(token []byte) (*Timestamp, error) {
	sd, err := Parse(token)
	if err != nil {
		return nil, err
	}
	if !sd.ContentType.Equal(oidTSTInfo) || len(sd.Content) == 0 {
		return nil, fmt.Errorf("%w: no timestamp token", ErrSyntax)
	}
	// The info of the token (RFC 3161, TSTInfo): version, policy,
	// message imprint, serial number and time, then fields that vary.
	var (
		seq     asn1.RawValue
		version int
		policy  asn1.ObjectIdentifier
		imprint messageImprint
		serial  *big.Int
		genTime time.Time
	)
	if _, err := asn1.Unmarshal(sd.Content, &seq); err != nil {
		return nil, fmt.Errorf("%w: timestamp info: %v", ErrSyntax, err)
	}
	rest := seq.Bytes
	for _, field := range []any{&version, &policy, &imprint, &serial} {
		if rest, err = asn1.Unmarshal(rest, field); err != nil {
			return nil, fmt.Errorf("%w: timestamp info: %v", ErrSyntax, err)
		}
	}
	if _, err = asn1.UnmarshalWithParams(rest, &genTime, "generalized"); err != nil {
		return nil, fmt.Errorf("%w: timestamp info: %v", ErrSyntax, err)
	}
	h, err := hashOf(imprint.Algorithm.Algorithm)
	if err != nil {
		return nil, err
	}
	if err := sd.VerifyContent(nil); err != nil {
		return nil, err
	}
	return &Timestamp{Time: genTime, Hash: h, Digest: imprint.Digest, Signed: sd}, nil
}

// SignatureTimestamp returns the timestamp of the signature, when it has
// a token that stamps the signature value and its authority signed it;
// nil otherwise.
func (s *SignedData) SignatureTimestamp() *Timestamp {
	if len(s.TimestampToken) == 0 {
		return nil
	}
	ts, err := ParseTimestamp(s.TimestampToken)
	if err != nil {
		return nil
	}
	h := ts.Hash.New()
	h.Write(s.si.Signature)
	if !bytes.Equal(h.Sum(nil), ts.Digest) {
		return nil
	}
	return ts
}
