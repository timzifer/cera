// Ported from github.com/go-pdfkit/reader v0.6.0 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/reader authors); see LICENSE-go-pdfkit.
// Changed: the object key is derived once per object, strings are decrypted
// in place, streams when they are decoded, and RC4 runs without allocating.

package pdf

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fmt"
	"hash"
)

// ErrWrongPassword is returned when a file is encrypted and neither the
// password given nor the empty one opens it.
var ErrWrongPassword = errors.New("pdf: the password does not open this file")

// ErrUnsupportedEncryption is returned for a security handler other than the
// standard one.
var ErrUnsupportedEncryption = errors.New("pdf: unsupported security handler")

// pad is the 32-byte string every pre-2.0 password is padded with.
var pad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56,
	0xFF, 0xFA, 0x01, 0x08, 0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80,
	0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// cryptMethod is how one class of data — strings or streams — is protected.
type cryptMethod uint8

const (
	cryptNone cryptMethod = iota // /Identity: not encrypted at all
	cryptRC4
	cryptAESV2 // 128-bit AES, per-object key
	cryptAESV3 // 256-bit AES, the file key used directly
)

// Permissions say what a reader may do with a file it opened with the user
// password.
type Permissions uint32

// The permissions a standard security handler can express.
const (
	PermPrint         Permissions = 1 << 2
	PermModify        Permissions = 1 << 3
	PermCopy          Permissions = 1 << 4
	PermAnnotate      Permissions = 1 << 5
	PermFillForms     Permissions = 1 << 8
	PermExtract       Permissions = 1 << 9
	PermAssemble      Permissions = 1 << 10
	PermPrintFaithful Permissions = 1 << 11

	AllPermissions = PermPrint | PermModify | PermCopy | PermAnnotate |
		PermFillForms | PermExtract | PermAssemble | PermPrintFaithful
)

// A decryptor holds the file encryption key and how to apply it. It is
// immutable once made.
type decryptor struct {
	key       []byte
	strings   cryptMethod
	streams   cryptMethod
	revision  int
	perm      Permissions
	owner     bool  // the password given was the owner's
	skipObj   int32 // the /Encrypt dictionary's own object number
	skipKnown bool
	// plainMetadata: /EncryptMetadata false, so metadata streams are
	// stored as they are.
	plainMetadata bool
	v3        cipher.Block // AESV3: the file key's cipher, shared
}

// A Protection is what a file's security handler says about it.
type Protection struct {
	Method      string // "RC4-40", "RC4-128", "AES-128", "AES-256" or "none"
	Revision    int
	Permissions Permissions
	Owner       bool // opened with the owner password
}

func (dec *decryptor) methodName() string {
	switch dec.streams {
	case cryptAESV3:
		return "AES-256"
	case cryptAESV2:
		return "AES-128"
	case cryptRC4:
		return fmt.Sprintf("RC4-%d", len(dec.key)*8)
	}
	return "none"
}

// newDecryptor derives the file encryption key from a password. resolve
// follows indirect entries of the /Encrypt dictionary.
func newDecryptor(enc Dict, id []byte, password string, resolve func(Object) Object) (*decryptor, error) {
	get := func(d Dict, k Name) Object { return resolve(d.Get(k)) }
	if f, ok := get(enc, "Filter").Name(); ok && f != "Standard" {
		return nil, ErrUnsupportedEncryption
	}
	v := int(intOr(get(enc, "V"), 0))
	rev := int(intOr(get(enc, "R"), 0))
	if v == 0 || rev == 0 {
		return nil, fmt.Errorf("pdf: /Encrypt has no usable /V and /R")
	}
	length := int(intOr(get(enc, "Length"), 40))
	if length < 40 || length > 256 || length%8 != 0 {
		length = 40
	}
	dec := &decryptor{revision: rev}
	if err := dec.readMethods(enc, v, get); err != nil {
		return nil, err
	}
	owner, _ := get(enc, "O").Str()
	user, _ := get(enc, "U").Str()
	perm := int32(intOr(get(enc, "P"), 0))
	metadata := true
	if b, ok := get(enc, "EncryptMetadata").Bool(); ok {
		metadata = b
	}

	dec.perm = Permissions(uint32(perm)) & AllPermissions
	dec.plainMetadata = !metadata
	if dec.streams == cryptNone && dec.strings == cryptNone {
		// Nothing in the body is encrypted, so the password is not checked.
		return dec, nil
	}
	if rev >= 5 {
		key, asOwner, err := deriveKeyR5(enc, password, get)
		if err != nil {
			return nil, err
		}
		dec.key, dec.owner = key, asOwner
	} else {
		n := length / 8
		if rev == 2 {
			n = 5
		}
		key, asOwner, err := deriveKeyLegacy(password, owner, user, id, perm, n, rev, metadata)
		if err != nil {
			return nil, err
		}
		dec.key, dec.owner = key, asOwner
	}
	if dec.streams == cryptAESV3 || dec.strings == cryptAESV3 {
		dec.v3, _ = aes.NewCipher(dec.key)
	}
	return dec, nil
}

// readMethods works out how strings and streams are protected.
func (dec *decryptor) readMethods(enc Dict, v int, get func(Dict, Name) Object) error {
	if v < 4 {
		dec.strings, dec.streams = cryptRC4, cryptRC4
		return nil
	}
	cf, _ := get(enc, "CF").Dict()
	pick := func(key Name) (cryptMethod, error) {
		name, ok := get(enc, key).Name()
		if !ok || name == "Identity" {
			return cryptNone, nil
		}
		f, ok := get(cf, name).Dict()
		if !ok {
			return cryptNone, nil
		}
		switch m, _ := get(f, "CFM").Name(); m {
		case "V2":
			return cryptRC4, nil
		case "AESV2":
			return cryptAESV2, nil
		case "AESV3":
			return cryptAESV3, nil
		case "None":
			return cryptNone, nil
		default:
			return cryptNone, fmt.Errorf("pdf: unsupported crypt filter method /%s", m)
		}
	}
	var err error
	if dec.streams, err = pick("StmF"); err != nil {
		return err
	}
	dec.strings, err = pick("StrF")
	return err
}

// deriveKeyLegacy is the pre-2.0 key derivation, trying the password as the
// user password and then as the owner password.
func deriveKeyLegacy(password string, owner, user, id []byte, perm int32, n, rev int, metadata bool) (key []byte, asOwner bool, err error) {
	for _, c := range legacyCandidates(password, owner, n, rev) {
		k := legacyFileKey(c.padded, owner, id, perm, n, rev, metadata)
		if legacyUserKeyMatches(k, user, id, rev) {
			return k, c.owner, nil
		}
	}
	return nil, false, ErrWrongPassword
}

type legacyCandidate struct {
	padded []byte
	owner  bool
}

func legacyCandidates(password string, owner []byte, n, rev int) []legacyCandidate {
	out := []legacyCandidate{{padded: padPassword([]byte(password))}}
	if password != "" {
		out = append(out, legacyCandidate{padded: padPassword(nil)})
	}
	if u := userFromOwner([]byte(password), owner, n, rev); u != nil {
		out = append(out, legacyCandidate{padded: u, owner: true})
	}
	return out
}

func padPassword(p []byte) []byte {
	out := make([]byte, 32)
	k := copy(out, p)
	copy(out[k:], pad)
	return out
}

// legacyFileKey is algorithm 2.
func legacyFileKey(padded, owner, id []byte, perm int32, n, rev int, metadata bool) []byte {
	h := md5.New()
	h.Write(padded)
	h.Write(owner)
	h.Write([]byte{byte(perm), byte(perm >> 8), byte(perm >> 16), byte(perm >> 24)})
	h.Write(id)
	if rev >= 4 && !metadata {
		h.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	}
	key := h.Sum(nil)
	if rev >= 3 {
		for range 50 {
			sum := md5.Sum(key[:n])
			key = sum[:]
		}
	}
	return key[:n]
}

// legacyUserKeyMatches is algorithm 6.
func legacyUserKeyMatches(key, user, id []byte, rev int) bool {
	if rev == 2 {
		return bytes.Equal(rc4Bytes(key, pad), user)
	}
	h := md5.New()
	h.Write(pad)
	h.Write(id)
	x := rc4Bytes(key, h.Sum(nil))
	for i := 1; i <= 19; i++ {
		x = rc4Bytes(xorKey(key, i), x)
	}
	return len(user) >= 16 && bytes.Equal(x, user[:16])
}

// userFromOwner is algorithm 7.
func userFromOwner(password, owner []byte, n, rev int) []byte {
	if len(owner) < 32 {
		return nil
	}
	sum := md5.Sum(padPassword(password))
	key := sum[:]
	if rev >= 3 {
		for range 50 {
			s := md5.Sum(key)
			key = s[:]
		}
	}
	key = key[:n]
	if rev == 2 {
		return rc4Bytes(key, owner[:32])
	}
	x := owner[:32]
	for i := 19; i >= 0; i-- {
		x = rc4Bytes(xorKey(key, i), x)
	}
	return x
}

func xorKey(key []byte, v int) []byte {
	out := make([]byte, len(key))
	for i := range key {
		out[i] = key[i] ^ byte(v)
	}
	return out
}

// deriveKeyR5 is the PDF 2.0 derivation, /R 5 and /R 6.
func deriveKeyR5(enc Dict, password string, get func(Dict, Name) Object) (key []byte, asOwner bool, err error) {
	user, _ := get(enc, "U").Str()
	userE, _ := get(enc, "UE").Str()
	owner, _ := get(enc, "O").Str()
	ownerE, _ := get(enc, "OE").Str()
	rev := int(intOr(get(enc, "R"), 6))
	if len(user) < 48 {
		return nil, false, fmt.Errorf("pdf: /U is %d bytes, not the 48 this revision needs", len(user))
	}
	// The password as SASLprep prepares it, then as given (a producer
	// that skipped the preparation), then the empty one.
	candidates := [][]byte{saslprep(password)}
	if raw := []byte(password); string(raw) != string(candidates[0]) {
		candidates = append(candidates, raw)
	}
	if password != "" {
		candidates = append(candidates, nil)
	}
	for _, candidate := range candidates {
		if key := unlockR5(candidate, user, userE, nil, rev); key != nil {
			return key, false, nil
		}
		if len(owner) >= 48 {
			if key := unlockR5(candidate, owner, ownerE, user[:48], rev); key != nil {
				return key, true, nil
			}
		}
	}
	return nil, false, ErrWrongPassword
}

func unlockR5(password, entry, wrapped, udata []byte, rev int) []byte {
	valSalt, keySalt := entry[32:40], entry[40:48]
	if !bytes.Equal(hash2B(password, valSalt, udata, rev), entry[:32]) {
		return nil
	}
	if len(wrapped) < 32 {
		return nil
	}
	inter := hash2B(password, keySalt, udata, rev)
	block, _ := aes.NewCipher(inter)
	out := make([]byte, 32)
	cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(out, wrapped[:32])
	return out
}

// hash2B is algorithm 2.B.
func hash2B(password, salt, udata []byte, rev int) []byte {
	h := sha256.New()
	h.Write(password)
	h.Write(salt)
	h.Write(udata)
	k := h.Sum(nil)
	if rev < 6 {
		return k
	}
	for round := 0; ; round++ {
		var k1 []byte
		one := append(append(append([]byte{}, password...), k...), udata...)
		for range 64 {
			k1 = append(k1, one...)
		}
		block, _ := aes.NewCipher(k[:16])
		e := make([]byte, len(k1)-len(k1)%aes.BlockSize)
		cipher.NewCBCEncrypter(block, k[16:32]).CryptBlocks(e, k1[:len(e)])
		sum := 0
		for _, b := range e[:16] {
			sum += int(b)
		}
		var next hash.Hash
		switch sum % 3 {
		case 0:
			next = sha256.New()
		case 1:
			next = sha512.New384()
		default:
			next = sha512.New()
		}
		next.Write(e)
		k = next.Sum(nil)
		if round >= 63 && int(e[len(e)-1]) <= round-31 {
			break
		}
	}
	return k[:32]
}

// objectKey is the per-object key the pre-2.0 methods use; AESV3 uses the
// file key unchanged. It is written to buf, which must hold 16 bytes.
func (dec *decryptor) objectKey(buf []byte, num, gen int32, method cryptMethod) []byte {
	if method == cryptAESV3 {
		return dec.key
	}
	var in [64]byte
	b := append(in[:0], dec.key...)
	b = append(b, byte(num), byte(num>>8), byte(num>>16), byte(gen), byte(gen>>8))
	if method == cryptAESV2 {
		b = append(b, 0x73, 0x41, 0x6C, 0x54) // "sAlT"
	}
	sum := md5.Sum(b)
	k := append(buf[:0], sum[:]...)
	if n := len(dec.key) + 5; n < 16 {
		return k[:n]
	}
	return k
}

// skips reports whether object num is the /Encrypt dictionary, which is
// never encrypted.
func (dec *decryptor) skips(num int32) bool { return dec.skipKnown && num == dec.skipObj }

// stringCrypt returns a function that decrypts one of object num's strings
// in place and returns what is left of it, or nil when strings are not
// encrypted.
func (dec *decryptor) stringCrypt(num, gen int32) func([]byte) []byte {
	if dec == nil || dec.strings == cryptNone || dec.skips(num) {
		return nil
	}
	m := dec.strings
	key := dec.objectKey(make([]byte, 0, 16), num, gen, m)
	var block cipher.Block
	switch m {
	case cryptAESV2:
		block, _ = aes.NewCipher(key)
	case cryptAESV3:
		block = dec.v3
	}
	return func(b []byte) []byte {
		if m == cryptRC4 {
			rc4InPlace(key, b)
			return b
		}
		return aesInPlace(block, b)
	}
}

// decryptStream returns a stream's raw bytes decrypted, in a new slice.
func (dec *decryptor) decryptStream(num, gen int32, raw []byte) []byte {
	switch dec.streams {
	case cryptNone:
		return raw
	case cryptRC4:
		var kb [16]byte
		out := make([]byte, len(raw))
		copy(out, raw)
		rc4InPlace(dec.objectKey(kb[:0], num, gen, cryptRC4), out)
		return out
	case cryptAESV2:
		var kb [16]byte
		block, err := aes.NewCipher(dec.objectKey(kb[:0], num, gen, cryptAESV2))
		if err != nil {
			return nil
		}
		return aesInPlace(block, bytes.Clone(raw))
	default:
		return aesInPlace(dec.v3, bytes.Clone(raw))
	}
}

// aesInPlace undoes AES in CBC mode over b, the initialisation vector being
// its first block, and returns the plain text, a part of b. Data that is not
// whole blocks decrypts to nothing.
func aesInPlace(block cipher.Block, b []byte) []byte {
	if block == nil || len(b) < 2*aes.BlockSize || len(b)%aes.BlockSize != 0 {
		return nil
	}
	data := b[aes.BlockSize:]
	cipher.NewCBCDecrypter(block, b[:aes.BlockSize]).CryptBlocks(data, data)
	n := int(data[len(data)-1])
	if n < 1 || n > aes.BlockSize || n > len(data) {
		return data
	}
	return data[:len(data)-n]
}

// rc4Bytes applies RC4 to a copy of data.
func rc4Bytes(key, data []byte) []byte {
	out := make([]byte, len(data))
	copy(out, data)
	rc4InPlace(key, out)
	return out
}

// rc4InPlace applies RC4, which is its own inverse. The state lives on the
// stack: crypto/rc4 allocates a cipher per call.
func rc4InPlace(key, b []byte) {
	if len(key) == 0 {
		return
	}
	var s [256]uint8
	for i := range s {
		s[i] = uint8(i)
	}
	var j uint8
	for i := range 256 {
		j += s[i] + key[i%len(key)]
		s[i], s[j] = s[j], s[i]
	}
	var x, y uint8
	for k := range b {
		x++
		y += s[x]
		s[x], s[y] = s[y], s[x]
		b[k] ^= s[s[x]+s[y]]
	}
}

// streamIsPlain reports whether a stream's own filter chain says its bytes
// were left unencrypted: a leading /Crypt filter naming /Identity.
func streamIsPlain(d Dict) bool {
	if first, ok := firstFilter(d); !ok || first != "Crypt" {
		return false
	}
	name, ok := firstDecodeParms(d).Get("Name").Name()
	return !ok || name == "Identity"
}

// firstFilter names the first filter of a stream's chain, read directly.
func firstFilter(d Dict) (Name, bool) {
	o := d.Get("Filter")
	if n, ok := o.Name(); ok {
		return n, true
	}
	if a, ok := o.Array(); ok && len(a) > 0 {
		return a[0].Name()
	}
	return "", false
}

// firstDecodeParms is the parameter dictionary of the first filter, read
// directly.
func firstDecodeParms(d Dict) Dict {
	o := d.Get("DecodeParms")
	if pd, ok := o.Dict(); ok && o.Kind() == KindDict {
		return pd
	}
	if a, ok := o.Array(); ok && len(a) > 0 {
		if pd, ok := a[0].Dict(); ok {
			return pd
		}
	}
	return Dict{}
}

// intOr reads an integer with a default.
func intOr(o Object, def int64) int64 {
	if n, ok := o.Int(); ok {
		return n
	}
	return def
}
