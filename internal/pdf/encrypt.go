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
	if e == nil || e.dec.streams == cryptNone || streamIsPlain(dict) ||
		e.dec.plainMetadata && hasType(dict.e, "Metadata") {
		return data
	}
	return e.encrypt(e.dec.streams, num, gen, data)
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
