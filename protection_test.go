package cera

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
)

// TestOpenPassword tells a document that needs a password from a broken
// one: a missing and a wrong password are ErrPassword.
func TestOpenPassword(t *testing.T) {
	const all = int32(-4)
	for _, method := range []string{"rc4", "aes128", "aes256"} {
		src := testpdf.EncryptedWith(method, "secret", all)
		if _, err := Open(src); !errors.Is(err, ErrPassword) {
			t.Errorf("%s, no password: err %v", method, err)
		}
		_, err := OpenWithPassword(src, "wrong")
		if !errors.Is(err, ErrPassword) {
			t.Errorf("%s, wrong password: err %v", method, err)
		}
		if err != nil && !strings.HasPrefix(err.Error(), "cera: ") {
			t.Errorf("%s: the error reads %q", method, err)
		}
		if _, err := OpenReaderAt(bytes.NewReader(src), int64(len(src)), OpenOptions{}); !errors.Is(err, ErrPassword) {
			t.Errorf("%s, read through a ReaderAt: err %v", method, err)
		}
		d, err := OpenWithPassword(src, "secret")
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if !d.Encrypted() {
			t.Errorf("%s: not encrypted", method)
		}
	}
}

// TestOpenUnsupportedEncryption names what no password opens.
func TestOpenUnsupportedEncryption(t *testing.T) {
	for _, tt := range []struct{ name, encrypt, says string }{
		{"handler", "<</Filter /Adobe.PubSec /V 4 /R 4>>", "/Adobe.PubSec"},
		{"crypt filter", "<</Filter /Standard /V 5 /R 6 /Length 256 /P -4 /CF <</StdCF <</CFM /AESGCM>>>> /StmF /StdCF /StrF /StdCF>>", "/AESGCM"},
	} {
		src := testpdf.File{
			Objs: map[int]string{
				1: "<</Type /Catalog /Pages 2 0 R>>",
				2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 100]>>",
				3: "<</Type /Page /Parent 2 0 R>>",
				4: tt.encrypt,
			},
			Trailer: "/Root 1 0 R /Encrypt 4 0 R /ID [<00> <00>]",
		}.Bytes()
		_, err := Open(src)
		if !errors.Is(err, ErrUnsupportedEncryption) || errors.Is(err, ErrPassword) {
			t.Errorf("%s: err %v", tt.name, err)
			continue
		}
		if s := err.Error(); !strings.HasPrefix(s, "cera: ") || !strings.Contains(s, tt.says) || strings.Contains(s, "pdf:") {
			t.Errorf("%s: the error reads %q", tt.name, s)
		}
	}
}

// TestOpenError prefixes the errors of the reader.
func TestOpenError(t *testing.T) {
	_, err := Open([]byte("not a PDF file"))
	if err == nil || !strings.HasPrefix(err.Error(), "cera: ") || errors.Is(err, ErrPassword) {
		t.Errorf("err %v", err)
	}
}

// TestPermissions returns what the author granted to a reader with the
// user password, and everything to the owner and for a plain file.
func TestPermissions(t *testing.T) {
	const all = int32(-4)
	granted := PermPrint | PermCopy | PermExtract
	perm := all &^ int32(AllPermissions&^granted)
	for _, method := range []string{"rc4", "aes128", "aes256"} {
		d := editOpen(t, testpdf.EncryptedWith(method, "", perm))
		if got := d.Permissions(); got != granted {
			t.Errorf("%s: permissions %#x, want %#x", method, got, granted)
		}
		if method == "aes256" {
			continue // the fixture has no owner password
		}
		d, err := OpenWithPassword(testpdf.EncryptedWith(method, "user", perm), testpdf.OwnerPassword)
		if err != nil {
			t.Fatalf("%s, owner password: %v", method, err)
		}
		if got := d.Permissions(); got != AllPermissions || !d.Encrypted() {
			t.Errorf("%s, owner password: permissions %#x, encrypted %v", method, got, d.Encrypted())
		}
	}
	d := editOpen(t, fieldsFile(false))
	if d.Encrypted() || d.Permissions() != AllPermissions {
		t.Errorf("a plain file: encrypted %v, permissions %#x", d.Encrypted(), d.Permissions())
	}
}
