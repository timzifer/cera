package pdf

import (
	"crypto/md5"
	"fmt"
	"strings"
	"testing"
)

// encryptedFile builds a file encrypted with RC4 (V 4, R 4, 128 bits) and
// the empty user password, its Info strings and content encrypted, its
// metadata stream stored as /EncryptMetadata says.
func encryptedFile(t *testing.T, encryptMetadata bool) []byte {
	t.Helper()
	id := []byte("0123456789abcdef")
	owner := []byte(strings.Repeat("O", 32))
	const perm = int32(-4)
	key := legacyFileKey(padPassword(nil), owner, id, perm, 16, 4, encryptMetadata)
	// U (algorithm 5): the padding and the ID hashed, encrypted 20 times.
	h := md5.Sum(append(padPassword(nil), id...))
	u := rc4Bytes(key, h[:])
	for i := 1; i <= 19; i++ {
		u = rc4Bytes(xorKey(key, i), u)
	}
	u = append(u, make([]byte, 16)...)
	dec := &decryptor{key: key, revision: 4, strings: cryptRC4, streams: cryptRC4}
	enc := func(num int32, b []byte) []byte {
		return rc4Bytes(dec.objectKey(nil, num, 0, cryptRC4), b)
	}
	hex := func(b []byte) string { return fmt.Sprintf("<%x>", b) }
	xmp := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><dc:title>Plain</dc:title></x:xmpmeta>`)
	metaData := xmp
	if encryptMetadata {
		metaData = enc(5, xmp)
	}
	content := enc(4, []byte("0 0 m 10 10 l S"))
	em := "false"
	if encryptMetadata {
		em = "true"
	}
	f := file{
		objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R /Metadata 5 0 R>>",
			2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 100]>>",
			3: "<</Type /Page /Parent 2 0 R /Contents 4 0 R /Resources <<>>>>",
			4: stream("", content),
			5: stream("/Type /Metadata /Subtype /XML", metaData),
			6: "<</Title " + hex(enc(6, []byte("Secret title"))) + ">>",
			7: fmt.Sprintf("<</Filter /Standard /V 4 /R 4 /Length 128 /P %d /O %s /U %s "+
				"/CF <</StdCF <</CFM /V2 /Length 16>>>> /StmF /StdCF /StrF /StdCF /EncryptMetadata %s>>",
				perm, hex(owner), hex(u), em),
		},
		trailer: "/Root 1 0 R /Info 6 0 R /Encrypt 7 0 R /ID [" + hex(id) + hex(id) + "]",
	}
	return f.bytes()
}

// TestMetadataStreamAsEncryptMetadataSays checks that with /EncryptMetadata
// false the metadata stream is read as stored, while strings and other
// streams are decrypted; and that with true it is decrypted like the rest.
func TestMetadataStreamAsEncryptMetadataSays(t *testing.T) {
	for _, em := range []bool{false, true} {
		t.Run(fmt.Sprint(em), func(t *testing.T) {
			d := mustOpen(t, encryptedFile(t, em))
			info, _ := d.Resolve(d.Trailer().Get("Info")).Dict()
			if title, _ := d.Resolve(info.Get("Title")).Str(); string(title) != "Secret title" {
				t.Fatalf("Info title %q", title)
			}
			cat, err := d.Catalog()
			if err != nil {
				t.Fatal(err)
			}
			ms, ok := d.Resolve(cat.Get("Metadata")).Stream()
			if !ok {
				t.Fatal("no metadata stream")
			}
			if got := string(d.Decode(ms).Data); !strings.Contains(got, "<dc:title>Plain</dc:title>") {
				t.Fatalf("metadata stream read as %q", got)
			}
			page, err := d.Page(1) // pages count from 1 here
			if err != nil {
				t.Fatal(err)
			}
			cs, ok := d.Resolve(page.Get("Contents")).Stream()
			if !ok {
				t.Fatal("no content stream")
			}
			if got := string(d.Decode(cs).Data); got != "0 0 m 10 10 l S" {
				t.Fatalf("content read as %q", got)
			}
		})
	}
}
