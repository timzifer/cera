package cera

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// signedFile is a form with two text fields and an XFA form. With signed,
// its signature field holds a signature that locks "name"; with mdp > 0
// the signature certifies the document with those DocMDP permissions.
// With direct, the form is a direct object in the catalogue.
func signedFile(signed bool, mdp int, direct bool) []byte {
	form := "<</Fields [10 0 R 11 0 R 12 0 R] /SigFlags 3 /XFA 30 0 R /DA (/Helv 10 Tf 0 g) /DR <</Font <</Helv 20 0 R>>>>>>"
	cat := "<</Type /Catalog /Pages 2 0 R /AcroForm 5 0 R"
	if direct {
		cat = "<</Type /Catalog /Pages 2 0 R /AcroForm " + form
	}
	if mdp > 0 {
		cat += " /Perms <</DocMDP 21 0 R>>"
	}
	sig := "<</Type /Annot /Subtype /Widget /FT /Sig /T (sig) /Rect [10 10 90 30]"
	if signed || mdp > 0 {
		sig += " /V 21 0 R /Lock <</Type /SigFieldLock /Action /Include /Fields [(name)]>>"
	}
	p := mdp
	if p == 0 {
		p = 2
	}
	return testpdf.File{
		Objs: map[int]string{
			1:  cat + ">>",
			2:  "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 200 100]>>",
			3:  "<</Type /Page /Parent 2 0 R /Annots [10 0 R 11 0 R 12 0 R]>>",
			4:  "<</Type /Page /Parent 2 0 R>>",
			5:  form,
			10: "<</Type /Annot /Subtype /Widget /FT /Tx /T (name) /V (a) /Rect [10 60 90 80]>>",
			11: "<</Type /Annot /Subtype /Widget /FT /Tx /T (city) /V (b) /Rect [100 60 190 80]>>",
			12: sig + ">>",
			20: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
			21: fmt.Sprintf("<</Type /Sig /Filter /Adobe.PPKLite /SubFilter /adbe.pkcs7.detached /Contents <00> /ByteRange [0 0 0 0]"+
				" /Reference [<</Type /SigRef /TransformMethod /DocMDP /TransformParams <</Type /TransformParams /P %d /V /1.2>>>>]>>", p),
			30: testpdf.Stream("", []byte("<xdp:xdp/>")),
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

// TestXFARemoved writes a value into a form with XFA data: /XFA goes, from
// an indirect form or from one direct in the catalogue.
func TestXFARemoved(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprintf("direct=%v", direct), func(t *testing.T) {
			src := signedFile(false, 0, direct)
			doc := editOpen(t, src)
			if !doc.Form().XFA {
				t.Fatal("no XFA in the source")
			}
			e := doc.Edit()
			if err := e.SetFields(fill(t, doc, map[string]Value{"name": TextValue("x")})); err != nil {
				t.Fatal(err)
			}
			for mode, out := range map[string][]byte{"save": editSave(t, e), "update": editUpdate(t, e, src)} {
				f := editOpen(t, out).Form()
				if f.XFA {
					t.Errorf("%s: /XFA stays", mode)
				}
				if v := f.Field("name").Saved.Text(); v != "x" {
					t.Errorf("%s: name %q", mode, v)
				}
				if len(f.Fields) != 3 {
					t.Errorf("%s: %d fields", mode, len(f.Fields))
				}
			}
		})
	}
	// No value written, no change.
	src := signedFile(false, 0, false)
	var out bytes.Buffer
	if err := editOpen(t, src).Edit().Update(&out); err != nil || !bytes.Equal(out.Bytes(), src) {
		t.Errorf("an unchanged form is written: err %v", err)
	}
}

func TestSigned(t *testing.T) {
	src := signedFile(true, 0, false)
	doc := editOpen(t, src)
	e := doc.Edit()
	// The signature locks name.
	if err := e.SetFields(fill(t, doc, map[string]Value{"name": TextValue("x")})); !errors.Is(err, ErrLocked) {
		t.Errorf("a locked field: err %v", err)
	}
	if err := e.SetFields(fill(t, doc, map[string]Value{"city": TextValue("y")})); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := e.Save(&out); !errors.Is(err, ErrSigned) || out.Len() != 0 {
		t.Errorf("save of a signed document: err %v, %d bytes", err, out.Len())
	}
	upd := editUpdate(t, e, src)
	if v := editOpen(t, upd).Form().Field("city").Saved.Text(); v != "y" {
		t.Errorf("city %q", v)
	}
	// Pages may change in an update of a document that is signed but
	// not certified.
	if err := e.DeletePages(1); err != nil {
		t.Fatal(err)
	}
	editUpdate(t, e, src)
}

func TestCertified(t *testing.T) {
	for _, mdp := range []int{1, 2, 3} {
		t.Run(fmt.Sprint("P", mdp), func(t *testing.T) {
			src := signedFile(true, mdp, false)
			doc := editOpen(t, src)
			e := doc.Edit()
			err := e.SetFields(fill(t, doc, map[string]Value{"city": TextValue("y")}))
			if mdp == 1 {
				if !errors.Is(err, ErrCertified) {
					t.Errorf("a value under P 1: err %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				editUpdate(t, e, src)
			}
			if err := e.DeletePages(1); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := e.Update(&out); !errors.Is(err, ErrCertified) || out.Len() != 0 {
				t.Errorf("a page change: err %v, %d bytes", err, out.Len())
			}
		})
	}
}

func TestSigLock(t *testing.T) {
	for _, tt := range []struct {
		l    sigLock
		name string
		want bool
	}{
		{sigLock{"All", nil}, "a", true},
		{sigLock{"Include", []string{"a", "b.c"}}, "b.c", true},
		{sigLock{"Include", []string{"a"}}, "b", false},
		{sigLock{"Exclude", []string{"a"}}, "a", false},
		{sigLock{"Exclude", []string{"a"}}, "b", true},
		{sigLock{"", []string{"a"}}, "a", false},
	} {
		if got := tt.l.locks(tt.name); got != tt.want {
			t.Errorf("%+v locks %q: %v", tt.l, tt.name, got)
		}
	}
}

// TestSignedBytesKept checks that an update leaves the signed bytes as
// they are: the file is a prefix of the update.
func TestSignedBytesKept(t *testing.T) {
	src := signedFile(true, 2, false)
	doc := editOpen(t, src)
	e := doc.Edit()
	if err := e.SetFields(fill(t, doc, map[string]Value{"city": TextValue("y")})); err != nil {
		t.Fatal(err)
	}
	upd := editUpdate(t, e, src)
	d, _ := pdf.Open(upd)
	cat, _ := d.Catalog()
	if d.Resolve(cat.Get("Perms")).IsNull() {
		t.Error("/Perms is gone")
	}
}
