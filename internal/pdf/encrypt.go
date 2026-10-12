package pdf

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
)

// An Encryptor encrypts the strings and streams of objects written into an
// encrypted file, with the file's key and methods (PDF 2.0, 7.6.3): what
// an incremental update of the file appends.
type Encryptor struct {
	dec *decryptor
}

// Encryptor returns the encryptor of an encrypted file whose key is
// known, and nil for a file that is not encrypted.
func (d *Document) Encryptor() *Encryptor {
	dec := d.tab.Load().dec
	if dec == nil {
		return nil
	}
	return &Encryptor{dec}
}

// String returns s encrypted as a string of object num, generation gen.
func (e *Encryptor) String(num, gen int32, s []byte) []byte {
	if e == nil || e.dec.strings == cryptNone {
		return s
	}
	return e.encrypt(e.dec.strings, num, gen, s)
}

// Stream returns the bytes of a stream of object num, generation gen,
// with dictionary dict, encrypted. Metadata streams of a file that keeps
// metadata plain, and streams with a crypt filter of their own, are
// returned as they are.
func (e *Encryptor) Stream(num, gen int32, dict Dict, data []byte) []byte {
	if e == nil {
		return data
	}
	method := e.dec.streams
	if hasType(dict.e, "EmbeddedFile") {
		method = e.dec.embedded // /EFF
	}
	if method == cryptNone || streamIsPlain(dict) ||
		e.dec.plainMetadata && hasType(dict.e, "Metadata") {
		return data
	}
	return e.encrypt(method, num, gen, data)
}

// encrypt applies method m with the key of object num.
func (e *Encryptor) encrypt(m cryptMethod, num, gen int32, data []byte) []byte {
	var kb [16]byte
	key := e.dec.objectKey(kb[:0], num, gen, m)
	switch m {
	case cryptRC4:
		return rc4Bytes(key, data)
	case cryptAESV2:
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil
		}
		return aesEncrypt(block, data)
	case cryptAESV3:
		return aesEncrypt(e.dec.v3, data)
	}
	return data
}

// aesEncrypt encrypts data with AES in CBC mode, padded as PKCS#5 says,
// after a random initialisation vector.
func aesEncrypt(block cipher.Block, data []byte) []byte {
	n := aes.BlockSize - len(data)%aes.BlockSize
	out := make([]byte, aes.BlockSize+len(data)+n)
	if _, err := rand.Read(out[:aes.BlockSize]); err != nil {
		panic(err) // crypto/rand does not fail
	}
	body := out[aes.BlockSize:]
	copy(body, data)
	for i := len(data); i < len(body); i++ {
		body[i] = byte(n)
	}
	cipher.NewCBCEncrypter(block, out[:aes.BlockSize]).CryptBlocks(body, body)
	return out
}

// NewEncryptor makes the encryption of a new file: AES-256 (/V 5, /R 6)
// with a random file key, opened by the user password with the permissions
// perm and by the owner password with all of them. It returns the
// encryptor of what is written and the /Encrypt dictionary, whose own
// strings are written as they are. Metadata streams are encrypted too
// unless plainMetadata is set. Passwords are prepared with SASLprep and
// cut at 127 bytes, as readers prepare what is typed.
func NewEncryptor(user, owner string, perm Permissions, plainMetadata bool) (*Encryptor, Dict, error) {
	prep := func(s string) []byte {
		b := saslprep(s)
		if len(b) > 127 {
			b = b[:127]
		}
		return b
	}
	random := func(n int) ([]byte, error) {
		b := make([]byte, n)
		_, err := rand.Read(b)
		return b, err
	}
	key, err := random(32)
	if err != nil {
		return nil, Dict{}, err
	}
	// Algorithms 8 and 9: the validation hash of a password with two
	// salts, and the file key encrypted under a key made of the password.
	entry := func(password, udata []byte) (hash, wrapped []byte, err error) {
		salts, err := random(16)
		if err != nil {
			return nil, nil, err
		}
		hash = append(hash2B(password, salts[:8], udata, 6), salts...)
		block, err := aes.NewCipher(hash2B(password, salts[8:], udata, 6))
		if err != nil {
			return nil, nil, err
		}
		wrapped = make([]byte, 32)
		cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(wrapped, key)
		return hash, wrapped, nil
	}
	u, ue, err := entry(prep(user), nil)
	if err != nil {
		return nil, Dict{}, err
	}
	o, oe, err := entry(prep(owner), u)
	if err != nil {
		return nil, Dict{}, err
	}
	// Algorithm 10: the permissions, encrypted with the file key, so that
	// a reader can tell whether /P was changed.
	p := int32(uint32(perm&AllPermissions) | 0xFFFFF0C0)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, Dict{}, err
	}
	tail, err := random(4)
	if err != nil {
		return nil, Dict{}, err
	}
	perms := []byte{byte(p), byte(p >> 8), byte(p >> 16), byte(p >> 24), 0xff, 0xff, 0xff, 0xff, 'T', 'a', 'd', 'b'}
	if plainMetadata {
		perms[8] = 'F'
	}
	perms = append(perms, tail...)
	block.Encrypt(perms, perms)

	dec := &decryptor{
		key: key, strings: cryptAESV3, streams: cryptAESV3, embedded: cryptAESV3,
		revision: 6, perm: perm & AllPermissions, plainMetadata: plainMetadata, v3: block,
	}
	dict := NewDict(
		Entry{Key: "Filter", Val: Name("Standard").Object()},
		Entry{Key: "V", Val: Integer(5)},
		Entry{Key: "R", Val: Integer(6)},
		Entry{Key: "Length", Val: Integer(256)},
		Entry{Key: "CF", Val: NewDict(Entry{Key: "StdCF", Val: NewDict(
			Entry{Key: "CFM", Val: Name("AESV3").Object()},
			Entry{Key: "AuthEvent", Val: Name("DocOpen").Object()},
			Entry{Key: "Length", Val: Integer(32)},
		).Object()}).Object()},
		Entry{Key: "StmF", Val: Name("StdCF").Object()},
		Entry{Key: "StrF", Val: Name("StdCF").Object()},
		Entry{Key: "U", Val: String(u)},
		Entry{Key: "UE", Val: String(ue)},
		Entry{Key: "O", Val: String(o)},
		Entry{Key: "OE", Val: String(oe)},
		Entry{Key: "Perms", Val: String(perms)},
		Entry{Key: "P", Val: Integer(int64(p))},
		Entry{Key: "EncryptMetadata", Val: Boolean(!plainMetadata)},
	)
	return &Encryptor{dec}, dict, nil
}
