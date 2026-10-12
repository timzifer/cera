package cera

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/timzifer/cera/internal/testpdf"
)

// attachmentsFile is a document with embedded files: three in a name tree
// of two kids — one of them twice, and one entry that names a file outside
// the document —, an associated file of the catalogue that the tree lists
// too and one it does not, and a FileAttachment annotation.
func attachmentsFile() []byte {
	file := func(data string, dict string) string {
		return testpdf.Stream("/Type /EmbeddedFile "+dict, []byte(data))
	}
	return testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R /Names <</EmbeddedFiles 10 0 R>> /AF [20 0 R 24 0 R]>>",
			2:  "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 100]>>",
			3:  "<</Type /Page /Parent 2 0 R /Annots [40 0 R 41 0 R]>>",
			10: "<</Kids [11 0 R 12 0 R 10 0 R]>>",
			11: "<</Limits [(a) (b)] /Names [(a) 20 0 R (b) 21 0 R]>>",
			12: "<</Limits [(c) (e)] /Names [(c) 22 0 R (d) 20 0 R (e) 23 0 R]>>",
			20: "<</Type /Filespec /F (invoice.xml) /UF <FEFF0052006500630068006E0075006E0067002E0078006D006C> /Desc (The invoice)" +
				" /AFRelationship /Alternative /EF <</F 30 0 R /UF 30 0 R>>>>",
			21: "<</Type /Filespec /F (plain.txt) /EF <</F 31 0 R>>>>",
			22: "<</Type /Filespec /F (elsewhere.pdf)>>", // not embedded
			23: "<</Type /Filespec /EF <</F 32 0 R>>>>",  // named by the tree alone
			24: "<</Type /Filespec /UF (source.csv) /AFRelationship /Source /EF <</UF 33 0 R>>>>",
			30: file("<Invoice/>", "/Subtype /text#2Fxml /Params <</Size 10 /CreationDate (D:20260102030405Z) /ModDate (D:20260203040506+01'00')>>"),
			31: testpdf.Stream("/Type /EmbeddedFile /Filter /FlateDecode", testpdf.Deflate([]byte(strings.Repeat("plain text ", 100)))),
			32: file("e", ""),
			33: file("a;b\n1;2\n", "/Subtype /text#2Fcsv"),
			40: "<</Type /Annot /Subtype /FileAttachment /Rect [10 10 30 30] /Name /Paperclip /Contents (See the notes) /FS 25 0 R>>",
			41: "<</Type /Annot /Subtype /FileAttachment /Rect [40 10 60 30] /FS (outside.txt)>>",
			25: "<</Type /Filespec /F (notes.txt) /EF <</F 34 0 R>>>>",
			34: file("notes", "/Subtype /text#2Fplain"),
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

func readAttachment(t *testing.T, a *Attachment) string {
	t.Helper()
	r, err := a.Open()
	if err != nil {
		t.Fatalf("%s: %v", a.Name, err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAttachments(t *testing.T) {
	doc := editOpen(t, attachmentsFile())
	as := doc.Attachments()
	var names []string
	for _, a := range as {
		names = append(names, a.Name)
	}
	// The tree in its order, each file once, then the associated file
	// the tree does not list.
	if got, want := strings.Join(names, " "), "Rechnung.xml plain.txt e source.csv"; got != want {
		t.Fatalf("attachments %q, want %q", got, want)
	}
	inv := as[0]
	if inv.Description != "The invoice" || inv.MIME != "text/xml" || inv.Size != 10 || inv.Relationship != "Alternative" ||
		!inv.Created.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) ||
		!inv.Modified.Equal(time.Date(2026, 2, 3, 4, 5, 6, 0, time.FixedZone("", 3600))) {
		t.Errorf("invoice %+v", inv)
	}
	if got := readAttachment(t, inv); got != "<Invoice/>" {
		t.Errorf("content %q", got)
	}
	plain := as[1]
	if plain.Size != -1 || plain.MIME != "" || plain.Relationship != "" || !plain.Created.IsZero() {
		t.Errorf("plain %+v", plain)
	}
	if got := readAttachment(t, plain); got != strings.Repeat("plain text ", 100) {
		t.Errorf("decoded content has %d bytes", len(got))
	}
	if as[3].Relationship != "Source" || readAttachment(t, as[3]) != "a;b\n1;2\n" {
		t.Errorf("associated file %+v", as[3])
	}
	if &doc.Attachments()[0] != &as[0] {
		t.Error("the attachments are read twice")
	}

	// The files of annotations are on their page.
	p, _ := doc.Page(0)
	annots := p.Annotations()
	if f := annots[0].File; f == nil || f.Name != "notes.txt" || f.MIME != "text/plain" || readAttachment(t, f) != "notes" {
		t.Errorf("file of the annotation %+v", f)
	}
	if annots[0].Markup == nil || annots[0].Markup.Icon != "Paperclip" || annots[0].Contents != "See the notes" {
		t.Errorf("annotation %+v", annots[0])
	}
	if annots[1].File != nil {
		t.Errorf("a file outside the document is an attachment: %+v", annots[1].File)
	}
	if got := editOpen(t, fieldsFile(false)).Attachments(); got != nil {
		t.Errorf("a document without attachments has %v", got)
	}
	if _, err := (&Attachment{}).Open(); err == nil {
		t.Error("an attachment without content opened")
	}
}

// TestAttachmentsEncrypted decrypts embedded files, also where they alone
// are encrypted (/EFF).
func TestAttachmentsEncrypted(t *testing.T) {
	const all = int32(-4)
	for _, method := range []string{"rc4", "aes128", "aes256", "aes256-files"} {
		doc, err := OpenWithPassword(testpdf.EncryptedWith(method, "secret", all), "secret")
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		as := doc.Attachments()
		if len(as) != 1 {
			t.Fatalf("%s: %d attachments", method, len(as))
		}
		a := as[0]
		if a.Name != "data.xml" || a.Description != "The data" || a.MIME != "application/xml" || a.Size != 15 || a.Relationship != "Data" {
			t.Errorf("%s: %+v", method, a)
		}
		if got := readAttachment(t, a); got != testpdf.AttachmentData {
			t.Errorf("%s: content %q", method, got)
		}
		// The page still draws: what is not an embedded file is read as
		// the file says, encrypted or not.
		if c := renderState(t, doc, nil).RGBAAt(40, 120); c != red {
			t.Errorf("%s: the page shows %v", method, c)
		}
		// Without the password the document does not open — unless its
		// files alone are encrypted: then it does, and they do not.
		locked, err := Open(testpdf.EncryptedWith(method, "secret", all))
		if method != "aes256-files" {
			if !errors.Is(err, ErrPassword) {
				t.Errorf("%s: without the password: %v", method, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: without the password: %v", method, err)
		}
		if c := renderState(t, locked, nil).RGBAAt(40, 120); c != red {
			t.Errorf("%s: without the password the page shows %v", method, c)
		}
		las := locked.Attachments()
		if len(las) != 1 || las[0].Name != "data.xml" {
			t.Fatalf("%s: without the password: attachments %v", method, las)
		}
		if _, err := las[0].Open(); !errors.Is(err, ErrPassword) {
			t.Errorf("%s: an attachment opened without the password: %v", method, err)
		}
		// Saved as a new file its attachments would be lost.
		var out bytes.Buffer
		if err := locked.Edit().Save(&out); !errors.Is(err, ErrPassword) || out.Len() > 0 {
			t.Errorf("%s: saved without the password: %v, %d bytes", method, err, out.Len())
		}
		if err := doc.Edit().Save(&out); err != nil {
			t.Errorf("%s: saving with the password: %v", method, err)
		} else if as := editOpen(t, out.Bytes()).Attachments(); len(as) != 1 || readAttachment(t, as[0]) != testpdf.AttachmentData {
			t.Errorf("%s: attachments of the saved file: %v", method, as)
		}
	}
}

// TestAttachmentsUpdated keeps an embedded file readable after an
// incremental update of an encrypted document.
func TestAttachmentsUpdated(t *testing.T) {
	const all = int32(-4)
	for _, method := range []string{"aes256", "aes256-files"} {
		src := testpdf.EncryptedWith(method, "", all)
		doc := editOpen(t, src)
		e := doc.Edit()
		if err := e.RotatePage(0, 90); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := e.Update(&out); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		as := editOpen(t, out.Bytes()).Attachments()
		if len(as) != 1 || readAttachment(t, as[0]) != testpdf.AttachmentData {
			t.Errorf("%s: attachments after the update: %v", method, as)
		}
	}
}

// TestAttachmentsCorpus reads the attachments of the pdf.js files, where
// the corpus has them.
func TestAttachmentsCorpus(t *testing.T) {
	for name, want := range map[string]int{"attachment.pdf": 1, "encrypted-attachment.pdf": 1} {
		b, err := os.ReadFile("testdata/pdfjs/test/pdfs/" + name)
		if err != nil {
			t.Skip("no pdf.js files")
		}
		doc, err := Open(b)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		as := doc.Attachments()
		if len(as) != want {
			t.Errorf("%s: %d attachments, want %d", name, len(as), want)
			continue
		}
		if s := readAttachment(t, as[0]); len(s) == 0 {
			t.Errorf("%s: %s is empty", name, as[0].Name)
		}
	}
}
