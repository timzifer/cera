package cera

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// TestUpdateEncrypted updates encrypted files: what the update writes is
// encrypted with the file's key, the file opens with its password as
// before, and shows the changes.
func TestUpdateEncrypted(t *testing.T) {
	for _, method := range []string{"rc4", "aes128", "aes256"} {
		for _, password := range []string{"", "secret"} {
			t.Run(fmt.Sprintf("%s/%q", method, password), func(t *testing.T) {
				src := testpdf.EncryptedWith(method, password, -4)
				doc, err := OpenWithPassword(src, password)
				if err != nil {
					t.Fatal(err)
				}
				e := doc.Edit()
				state := doc.Form().NewState()
				if err := state.SetValue(doc.Form().Field("secret"), TextValue("plaintext value")); err != nil {
					t.Fatal(err)
				}
				for i, err := range []error{
					e.SetFields(state),
					e.RotatePage(1, 90),
					e.ImportPages(2, editOpen(t, testpdf.SampleFile(false)), EditPage{Index: testpdf.PageA}),
				} {
					if err != nil {
						t.Fatalf("step %d: %v", i, err)
					}
				}
				var out bytes.Buffer
				if err := e.Update(&out); err != nil {
					t.Fatal(err)
				}
				upd := out.Bytes()
				if !bytes.HasPrefix(upd, src) {
					t.Fatal("the update does not start with the file")
				}
				tail := upd[len(src):]
				for _, plain := range []string{"plaintext value", "Hello"} {
					if bytes.Contains(tail, []byte(plain)) {
						t.Errorf("%q is written in the clear", plain)
					}
				}
				d, err := pdf.OpenWith(upd, pdf.Options{Password: password})
				if err != nil {
					t.Fatal(err)
				}
				if d.Repaired() || !d.Encrypted() {
					t.Fatalf("repaired %v, encrypted %v", d.Repaired(), d.Encrypted())
				}
				if password != "" {
					if _, err := pdf.Open(upd); !errors.Is(err, pdf.ErrWrongPassword) {
						t.Errorf("opens without the password: %v", err)
					}
				}
				od, err := OpenWithPassword(upd, password)
				if err != nil {
					t.Fatal(err)
				}
				if v := od.Form().Field("secret").Saved.Text(); v != "plaintext value" {
					t.Errorf("secret %q", v)
				}
				if title := od.Metadata().Title; title != "Secret (title)" {
					t.Errorf("title %q", title)
				}
				sameAsSaveWith(t, e, upd, password)
			})
		}
	}
}

// sameAsSaveWith is sameAsSave for an update that opens with password.
func sameAsSaveWith(t *testing.T, e *Editor, upd []byte, password string) {
	t.Helper()
	saved := editSave(t, e)
	ud, err := OpenWithPassword(upd, password)
	if err != nil {
		t.Fatal(err)
	}
	sd := editOpen(t, saved)
	if ud.NumPages() != sd.NumPages() {
		t.Fatalf("%d pages, saved %d", ud.NumPages(), sd.NumPages())
	}
	for i := range ud.NumPages() {
		got, up := editRender(t, ud, i)
		want, sp := editRender(t, sd, i)
		if up.Rotate != sp.Rotate || !bytes.Equal(got.Pix, want.Pix) {
			t.Errorf("page %d renders unlike the saved file", i)
		}
	}
}

// TestUpdateEncryptedPermissions refuses changes the permissions of an
// encrypted file do not allow.
func TestUpdateEncryptedPermissions(t *testing.T) {
	const all = int32(-4)
	noFill := all &^ (1<<8 | 1<<5 | 1<<3)
	noPages := all &^ (1<<10 | 1<<3)
	for _, tt := range []struct {
		name   string
		perm   int32
		fields bool
		pages  bool
		want   error
	}{
		{"fill allowed", noPages, true, false, nil},
		{"fill refused", noFill, true, false, ErrNotPermitted},
		{"pages allowed", noFill, false, true, nil},
		{"pages refused", noPages, false, true, ErrNotPermitted},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := testpdf.EncryptedWith("aes128", "", tt.perm)
			doc := editOpen(t, src)
			e := doc.Edit()
			if tt.fields {
				state := doc.Form().NewState()
				if err := state.SetValue(doc.Form().Field("secret"), TextValue("new")); err != nil {
					t.Fatal(err)
				}
				if err := e.SetFields(state); err != nil {
					t.Fatal(err)
				}
			}
			if tt.pages {
				if err := e.DeletePages(1); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			err := e.Update(&out)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err %v, want %v", err, tt.want)
			}
			if tt.want == nil {
				if _, err := pdf.Open(out.Bytes()); err != nil {
					t.Fatal(err)
				}
			} else if out.Len() != 0 {
				t.Errorf("%d bytes written", out.Len())
			}
		})
	}
}
