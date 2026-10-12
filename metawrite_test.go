package cera

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/timzifer/cera/internal/testpdf"
)

// metaFile is a document with an information dictionary and an XMP packet
// that says the same in the ways packets do: a title as an element, the
// producer as an attribute, dates under the old prefix xap, and a property
// of someone else's.
func metaFile() []byte {
	xmp := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
<rdf:Description rdf:about="" xmlns:pdf="http://ns.adobe.com/pdf/1.3/" pdf:Producer="Old Producer" pdf:Keywords='old, words'/>
<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:xap="http://ns.adobe.com/xap/1.0/" xmlns:acme="http://example.com/acme/">
<dc:title><rdf:Alt><rdf:li xml:lang="x-default">Old Title</rdf:li></rdf:Alt></dc:title>
<dc:creator><rdf:Seq><rdf:li>Old Author</rdf:li></rdf:Seq></dc:creator>
<dc:rights>All rights</dc:rights>
<xap:ModifyDate>2001-01-01T00:00:00Z</xap:ModifyDate>
<xap:CreatorTool>Old Tool</xap:CreatorTool>
<acme:Ticket>T-42</acme:Ticket>
</rdf:Description>
</rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`
	return testpdf.File{
		Objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R /Metadata 6 0 R>>",
			2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 100]>>",
			3: "<</Type /Page /Parent 2 0 R>>",
			5: "<</Title (Old Title) /Author (Old Author) /Producer (Old Producer) /Keywords (old, words) /Creator (Old Tool)" +
				" /ModDate (D:20010101000000Z) /Trapped /True /Department (Sales) /Number 7>>",
			6: testpdf.Stream("/Type /Metadata /Subtype /XML", []byte(xmp)),
		},
		Trailer: "/Root 1 0 R /Info 5 0 R",
	}.Bytes()
}

func TestSetMetadata(t *testing.T) {
	src := metaFile()
	doc := editOpen(t, src)
	created := time.Date(2026, 10, 12, 9, 30, 0, 0, time.FixedZone("", 2*3600))
	modified := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	m := doc.Metadata()
	if m.Title != "Old Title" || m.Custom["Department"] != "Sales" {
		t.Fatalf("metadata of the file %+v", m)
	}
	m.Title = "Größe & <Form>"
	m.Author = "Ada"
	m.Subject = "What it is about"
	m.Keywords = ""
	m.Producer = "cera"
	m.Created, m.Modified = created, modified
	m.Trapped = "False"
	m.Custom = map[string]string{"Project": "Übung"}

	for _, mode := range []string{"save", "update"} {
		e := doc.Edit()
		if err := e.SetMetadata(m); err != nil {
			t.Fatal(err)
		}
		// The caller's map is not kept.
		m.Custom["Later"] = "no"
		var out []byte
		if mode == "save" {
			out = editSave(t, e)
		} else {
			out = editUpdate(t, e, src)
		}
		delete(m.Custom, "Later")
		got := editOpen(t, out).Metadata()
		if got.Title != m.Title || got.Author != "Ada" || got.Subject != m.Subject || got.Keywords != "" || got.Producer != "cera" ||
			got.Creator != "Old Tool" || !got.Created.Equal(created) || !got.Modified.Equal(modified) || got.Trapped != "False" ||
			len(got.Custom) != 1 || got.Custom["Project"] != "Übung" {
			t.Errorf("%s: metadata %+v", mode, got)
		}
		xmp := string(got.XMP)
		for _, want := range []string{
			`<dc:title><rdf:Alt><rdf:li xml:lang="x-default">Größe &amp; &lt;Form&gt;</rdf:li></rdf:Alt></dc:title>`,
			"<dc:creator><rdf:Seq><rdf:li>Ada</rdf:li></rdf:Seq></dc:creator>",
			"<pdf:Producer>cera</pdf:Producer>",
			"<xmp:CreatorTool>Old Tool</xmp:CreatorTool>",
			"<xmp:CreateDate>2026-10-12T09:30:00+02:00</xmp:CreateDate>",
			"<xmp:ModifyDate>2026-10-13T08:00:00Z</xmp:ModifyDate>",
			"<xmp:MetadataDate>2026-10-13T08:00:00Z</xmp:MetadataDate>",
			// What is not /Info's stays.
			"<dc:rights>All rights</dc:rights>", "<acme:Ticket>T-42</acme:Ticket>", `<?xpacket end="w"?>`,
		} {
			if !strings.Contains(xmp, want) {
				t.Errorf("%s: the packet lacks %s:\n%s", mode, want, xmp)
			}
		}
		for _, old := range []string{"Old Title", "Old Author", "Old Producer", "old, words", "2001-01-01", "xap:CreatorTool", "pdf:Keywords"} {
			if strings.Contains(xmp, old) {
				t.Errorf("%s: the packet still has %q:\n%s", mode, old, xmp)
			}
		}
		if n := strings.Count(xmp, "</rdf:RDF>"); n != 1 {
			t.Errorf("%s: %d ends of the RDF", mode, n)
		}
	}

	// Without SetMetadata the editor changes none of it.
	if got := editOpen(t, editSave(t, doc.Edit())).Metadata(); got.Title != "Old Title" || !got.Modified.Equal(time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)) ||
		!bytes.Equal(got.XMP, doc.Metadata().XMP) {
		t.Errorf("metadata of an unchanged document %+v", got)
	}
	for _, bad := range []Metadata{{Trapped: "Yes"}, {Custom: map[string]string{"Title": "x"}}, {Custom: map[string]string{"a b": "x"}}, {Custom: map[string]string{"": "x"}}} {
		if err := doc.Edit().SetMetadata(bad); err == nil {
			t.Errorf("%+v was set", bad)
		}
	}
}

// TestSetMetadataNew gives a document put together from pages a title, an
// information dictionary and a packet of its own, and one without either
// both.
func TestSetMetadataNew(t *testing.T) {
	src := editOpen(t, fieldsFile(false)) // no /Info, no XMP
	when := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	m := Metadata{Title: "Assembled", Producer: "cera", Modified: when}
	check := func(name string, out []byte) {
		t.Helper()
		got := editOpen(t, out).Metadata()
		if got.Title != "Assembled" || got.Producer != "cera" || !got.Modified.Equal(when) || got.Author != "" {
			t.Errorf("%s: metadata %+v", name, got)
		}
		xmp := string(got.XMP)
		if !strings.HasPrefix(xmp, "<?xpacket begin=\"\ufeff\"") || !strings.Contains(xmp, `<rdf:li xml:lang="x-default">Assembled</rdf:li>`) ||
			!strings.Contains(xmp, "<xmp:ModifyDate>2026-10-12T00:00:00Z</xmp:ModifyDate>") || strings.Contains(xmp, "dc:creator") {
			t.Errorf("%s: packet:\n%s", name, xmp)
		}
	}
	e := NewEditor()
	if err := e.ImportPages(0, src, EditPage{Index: 0}); err != nil {
		t.Fatal(err)
	}
	if err := e.SetMetadata(m); err != nil {
		t.Fatal(err)
	}
	check("new", editSave(t, e))

	e = src.Edit()
	if err := e.SetMetadata(m); err != nil {
		t.Fatal(err)
	}
	check("save", editSave(t, e))
	check("update", editUpdate(t, e, fieldsFile(false)))
}

// TestSetMetadataGuarded refuses metadata where no change is allowed.
func TestSetMetadataGuarded(t *testing.T) {
	if err := editOpen(t, signedFile(true, 1, false)).Edit().SetMetadata(Metadata{Title: "x"}); !errors.Is(err, ErrCertified) {
		t.Errorf("certified for no changes: %v", err)
	}
	if err := editOpen(t, signedFile(true, 2, false)).Edit().SetMetadata(Metadata{Title: "x"}); err != nil {
		t.Errorf("certified for forms: %v", err)
	}
	const all = int32(-4)
	for perm, want := range map[int32]error{all: nil, all &^ (1 << 3): ErrNotPermitted} {
		e := editOpen(t, testpdf.EncryptedWith("aes128", "", perm)).Edit()
		if err := e.SetMetadata(Metadata{Title: "Secret no more"}); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := e.Update(&out); !errors.Is(err, want) {
			t.Errorf("permissions %d: %v, want %v", perm, err, want)
		}
		if want == nil {
			if got := editOpen(t, out.Bytes()).Metadata(); got.Title != "Secret no more" || !strings.Contains(string(got.XMP), "Secret no more") {
				t.Errorf("metadata of the encrypted update %+v", got)
			}
		}
	}
}
