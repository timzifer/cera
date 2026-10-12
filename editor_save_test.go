package cera

import (
	"bytes"
	"errors"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

func saveWith(t *testing.T, e *Editor, opt SaveOptions) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := e.SaveWith(&out, opt); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// TestSaveEncrypted protects a file the editor writes: it opens with the
// user password and the permissions given, with the owner password and
// all of them, and not without.
func TestSaveEncrypted(t *testing.T) {
	src := fieldsFile(false)
	doc := editOpen(t, src)
	want := renderState(t, doc, nil)
	granted := PermPrint | PermCopy
	for _, pw := range []struct{ user, owner string }{{"user", "owner"}, {"pässwörd Ω", "o"}, {"", "owner"}, {"only", ""}} {
		out := saveWith(t, doc.Edit(), SaveOptions{Encrypt: &Encryption{UserPassword: pw.user, OwnerPassword: pw.owner, Permissions: granted}})
		if bytes.Contains(out, []byte("0.9 g 0 0 300 200 re f")) || bytes.Contains(out, []byte("(old)")) {
			t.Errorf("%q: the file holds content or values in the clear", pw.user)
		}
		r, err := pdf.OpenWithPassword(out, pw.user)
		if err != nil {
			t.Fatalf("%q: %v", pw.user, err)
		}
		if prot, ok := r.Protection(); !ok || prot.Method != "AES-256" || prot.Revision != 6 || r.Repaired() {
			t.Errorf("%q: protection %+v, repaired %v", pw.user, prot, r.Repaired())
		}
		_, err = Open(out)
		if pw.user == "" {
			if err != nil {
				t.Errorf("an empty user password: %v", err)
			}
		} else if !errors.Is(err, ErrPassword) {
			t.Errorf("%q: opens without the password: %v", pw.user, err)
		}
		if _, err := OpenWithPassword(out, "wrong"); pw.user != "" && !errors.Is(err, ErrPassword) {
			t.Errorf("%q: opens with a wrong password: %v", pw.user, err)
		}
		d, err := OpenWithPassword(out, pw.user)
		if err != nil {
			t.Fatal(err)
		}
		if !d.Encrypted() || d.Permissions() != granted {
			t.Errorf("%q: encrypted %v, permissions %#x, want %#x", pw.user, d.Encrypted(), d.Permissions(), granted)
		}
		if v := d.Form().Field("name").Saved.Text(); v != "old" {
			t.Errorf("%q: field value %q", pw.user, v)
		}
		if n, _ := pixDiff(want, renderState(t, d, nil)); n > 0 {
			t.Errorf("%q: the encrypted file renders unlike the document", pw.user)
		}
		if pw.owner != "" {
			o, err := OpenWithPassword(out, pw.owner)
			if err != nil || o.Permissions() != AllPermissions {
				t.Errorf("%q: with the owner password: %v", pw.user, err)
			}
		}
		// The protection binds the editor: no filling in with these
		// permissions.
		e := d.Edit()
		if err := e.SetFields(fill(t, d, map[string]Value{"name": TextValue("new")})); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := e.Update(&buf); !errors.Is(err, ErrNotPermitted) {
			t.Errorf("%q: an update against the permissions: %v", pw.user, err)
		}
	}
	// A document put together from pages.
	e := NewEditor()
	if err := e.ImportPages(0, doc, EditPage{Index: 0}); err != nil {
		t.Fatal(err)
	}
	out := saveWith(t, e, SaveOptions{Encrypt: &Encryption{UserPassword: "u", Permissions: AllPermissions}})
	if d, err := OpenWithPassword(out, "u"); err != nil || d.NumPages() != 1 || d.Permissions() != AllPermissions {
		t.Errorf("a new document: %v", err)
	}
	var buf bytes.Buffer
	if err := doc.Edit().SaveWith(&buf, SaveOptions{Encrypt: &Encryption{}, Decrypt: true}); err == nil || buf.Len() > 0 {
		t.Errorf("encrypted and decrypted at once: %v", err)
	}
}

// TestSaveKeepsEncryption saves an encrypted document as it is protected,
// and changes or removes the protection for the owner alone.
func TestSaveKeepsEncryption(t *testing.T) {
	const all = int32(-4)
	granted := AllPermissions &^ PermCopy
	perm := all &^ int32(PermCopy)
	for _, method := range []string{"rc4", "aes128", "aes256"} {
		src := testpdf.EncryptedWith(method, "user", perm)
		doc, err := OpenWithPassword(src, "user")
		if err != nil {
			t.Fatal(err)
		}
		e := doc.Edit()
		if err := e.RotatePage(1, 90); err != nil {
			t.Fatal(err)
		}
		// Save: protected as it was.
		var buf bytes.Buffer
		if err := e.Save(&buf); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		out := buf.Bytes()
		if _, err := Open(out); !errors.Is(err, ErrPassword) {
			t.Errorf("%s: the saved file opens without the password: %v", method, err)
		}
		d, err := OpenWithPassword(out, "user")
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if d.Permissions() != granted || d.Metadata().Title != "Secret (title)" || d.Form().Field("secret").Saved.Text() != "old" {
			t.Errorf("%s: permissions %#x, title %q", method, d.Permissions(), d.Metadata().Title)
		}
		if p, _ := d.Page(1); p.Rotate != 90 {
			t.Errorf("%s: the page is not turned", method)
		}
		if got, want := renderState(t, d, nil), renderState(t, doc, nil); !bytes.Equal(got.Pix, want.Pix) {
			t.Errorf("%s: the saved file renders unlike the document", method)
		}

		// Removing or changing the protection: not with the user password.
		for name, opt := range map[string]SaveOptions{"decrypt": {Decrypt: true}, "encrypt anew": {Encrypt: &Encryption{UserPassword: "new"}}} {
			buf.Reset()
			if err := doc.Edit().SaveWith(&buf, opt); !errors.Is(err, ErrNotPermitted) || buf.Len() > 0 {
				t.Errorf("%s, %s with the user password: %v", method, name, err)
			}
		}
		if method == "aes256" {
			continue // the fixture has no owner password
		}
		owner, err := OpenWithPassword(src, testpdf.OwnerPassword)
		if err != nil {
			t.Fatal(err)
		}
		plain := saveWith(t, owner.Edit(), SaveOptions{Decrypt: true})
		if pd, err := Open(plain); err != nil || pd.Encrypted() || pd.Metadata().Title != "Secret (title)" {
			t.Errorf("%s: decrypted: %v", method, err)
		}
		changed := saveWith(t, owner.Edit(), SaveOptions{Encrypt: &Encryption{UserPassword: "new", OwnerPassword: "boss", Permissions: PermPrint}})
		if _, err := OpenWithPassword(changed, "user"); !errors.Is(err, ErrPassword) {
			t.Errorf("%s: the old password still opens: %v", method, err)
		}
		if cd, err := OpenWithPassword(changed, "new"); err != nil || cd.Permissions() != PermPrint || cd.Metadata().Title != "Secret (title)" {
			t.Errorf("%s: with the new password: %v", method, err)
		}
	}
}
