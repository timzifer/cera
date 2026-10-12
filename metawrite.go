package cera

import (
	"bytes"
	"fmt"
	"html"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
)

// Writing metadata (ADR 0013). A document says what it is in two places:
// the document information dictionary (/Info) and the XMP metadata stream
// of the catalogue. PDF/A asks that they agree, and readers of PDF 2.0 take
// XMP first, so the editor writes both from one Metadata.
//
// The editor sets no metadata of its own: Save and Update leave /ModDate
// and /Producer as they are unless SetMetadata says otherwise, so that the
// same edit gives the same file.

// SetMetadata records the metadata of the result, written by Save and
// Update into /Info and into the XMP stream, so that both say the same:
// title, author, subject, keywords, creator, producer, the dates and
// /Trapped; empty fields and zero dates are left out, and removed where
// the document had them. Custom are the other entries of /Info, as
// strings.
//
// The XMP packet is m.XMP, or the one the edited document has when m.XMP
// is nil — as it is in what [Document.Metadata] returns for a document
// without one —, with the properties that mirror /Info changed and what
// else it holds kept; a new packet is made when there is none.
//
// It returns [ErrCertified] for a document certified for no changes.
func (e *Editor) SetMetadata(m Metadata) error {
	if e.base != nil && e.base.signing().mdp == 1 {
		return ErrCertified
	}
	switch m.Trapped {
	case "", "True", "False", "Unknown":
	default:
		return fmt.Errorf("cera: /Trapped is True, False or Unknown, not %q", m.Trapped)
	}
	for k := range m.Custom {
		if k == "" || slices.Contains(infoKeys, k) || strings.ContainsAny(k, " \t\r\n()<>[]{}/%#") {
			return fmt.Errorf("cera: %q is not a key for a custom entry of /Info", k)
		}
	}
	m.Custom = maps.Clone(m.Custom)
	m.XMP = bytes.Clone(m.XMP)
	e.meta = &m
	return nil
}

// infoKeys are the entries of /Info that Metadata has fields for.
var infoKeys = []string{"Title", "Author", "Subject", "Keywords", "Creator", "Producer", "CreationDate", "ModDate", "Trapped"}

// metaPatches adds the metadata SetMetadata recorded to doc.
func (e *Editor) metaPatches(doc *pdfedit.Doc) {
	m := e.meta
	var entries []pdf.Entry
	text := func(key, s string) {
		if s != "" {
			entries = append(entries, pdf.Entry{Key: pdf.Name(key), Val: pdf.String(encodeText(s))})
		}
	}
	text("Title", m.Title)
	text("Author", m.Author)
	text("Subject", m.Subject)
	text("Keywords", m.Keywords)
	text("Creator", m.Creator)
	text("Producer", m.Producer)
	if !m.Created.IsZero() {
		text("CreationDate", pdfDate(m.Created))
	}
	if !m.Modified.IsZero() {
		text("ModDate", pdfDate(m.Modified))
	}
	if m.Trapped != "" {
		entries = append(entries, pdf.Entry{Key: "Trapped", Val: pdf.Name(m.Trapped).Object()})
	}
	for _, k := range slices.Sorted(maps.Keys(m.Custom)) {
		text(k, m.Custom[k])
	}

	packet := m.XMP
	if packet == nil && e.base != nil {
		packet = e.base.Metadata().XMP
	}
	xmp := syncXMP(packet, m)

	d := e.base
	if d == nil {
		doc.Info, doc.XMP = pdf.NewDict(entries...), xmp
		return
	}
	// The information dictionary of the document is changed where it is
	// an object of its own; else the result gets a new one.
	if ref, ok := d.r.Trailer().Get("Info").Ref(); ok && !d.dict(ref.Object()).IsZero() {
		// What Metadata holds and does not set goes: the entries it has
		// fields for and the other strings. Other entries stay.
		var del []pdf.Name
		for k, v := range d.dict(ref.Object()).All() {
			_, isString := d.resolve(v).Str()
			if (isString || slices.Contains(infoKeys, string(k))) && !slices.ContainsFunc(entries, func(e pdf.Entry) bool { return e.Key == k }) {
				del = append(del, k)
			}
		}
		doc.Patches = append(doc.Patches, pdfedit.Patch{Ref: ref, Set: entries, Del: del})
	} else {
		doc.Info = pdf.NewDict(entries...)
	}
	// The metadata stream is a new object: plain, so that tools that do
	// not read PDF find the packet.
	if root, ok := d.r.Trailer().Get("Root").Ref(); ok {
		doc.Objects = append(doc.Objects, pdf.NewStream(pdf.NewDict(
			pdf.Entry{Key: "Type", Val: pdf.Name("Metadata").Object()},
			pdf.Entry{Key: "Subtype", Val: pdf.Name("XML").Object()},
		), xmp).Object())
		doc.Patches = append(doc.Patches, pdfedit.Patch{Ref: root, Set: []pdf.Entry{
			{Key: "Metadata", Val: pdfedit.NewRef(len(doc.Objects) - 1).Object()},
		}})
	}
}

// The namespaces of the XMP properties that mirror /Info.
const (
	nsDC  = "http://purl.org/dc/elements/1.1/"
	nsPDF = "http://ns.adobe.com/pdf/1.3/"
	nsXMP = "http://ns.adobe.com/xap/1.0/"
)

// xmpDate formats t as XMP has dates (ISO 8601).
func xmpDate(t time.Time) string {
	if _, off := t.Zone(); off == 0 {
		return t.UTC().Format("2006-01-02T15:04:05Z")
	}
	return t.Format("2006-01-02T15:04:05-07:00")
}

// syncXMP returns packet with the properties that mirror /Info saying what
// m says: they are taken out wherever the packet has them, as elements or
// as attributes, under whatever prefix it gives their namespace, and put
// back in one description of their own. The rest of the packet stays. A
// packet without RDF in it, and none, gives a new one.
func syncXMP(packet []byte, m *Metadata) []byte {
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString(`<rdf:Description rdf:about="" xmlns:dc="` + nsDC + `" xmlns:pdf="` + nsPDF + `" xmlns:xmp="` + nsXMP + `">` + "\n")
	alt := func(name, s string) {
		if s != "" {
			b.WriteString("<" + name + `><rdf:Alt><rdf:li xml:lang="x-default">` + esc(s) + "</rdf:li></rdf:Alt></" + name + ">\n")
		}
	}
	simple := func(name, s string) {
		if s != "" {
			b.WriteString("<" + name + ">" + esc(s) + "</" + name + ">\n")
		}
	}
	alt("dc:title", m.Title)
	if m.Author != "" {
		b.WriteString("<dc:creator><rdf:Seq><rdf:li>" + esc(m.Author) + "</rdf:li></rdf:Seq></dc:creator>\n")
	}
	alt("dc:description", m.Subject)
	simple("pdf:Keywords", m.Keywords)
	simple("pdf:Producer", m.Producer)
	simple("pdf:Trapped", m.Trapped)
	simple("xmp:CreatorTool", m.Creator)
	if !m.Created.IsZero() {
		simple("xmp:CreateDate", xmpDate(m.Created))
	}
	if !m.Modified.IsZero() {
		simple("xmp:ModifyDate", xmpDate(m.Modified))
		simple("xmp:MetadataDate", xmpDate(m.Modified))
	}
	b.WriteString("</rdf:Description>\n")
	block := b.String()

	s := string(packet)
	end := strings.LastIndex(s, "</rdf:RDF>")
	if end < 0 {
		return []byte("<?xpacket begin=\"\ufeff\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>\n" +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/">` + "\n" +
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` + "\n" +
			block + "</rdf:RDF>\n</x:xmpmeta>\n<?xpacket end=\"w\"?>")
	}
	for ns, names := range map[string][]string{
		nsDC:  {"title", "creator", "description"},
		nsPDF: {"Keywords", "Producer", "Trapped"},
		nsXMP: {"CreatorTool", "CreateDate", "ModifyDate", "MetadataDate"},
	} {
		// The prefixes the packet gives the namespace.
		decl := regexp.MustCompile(`xmlns:([A-Za-z_][\w.-]*)\s*=\s*["']` + regexp.QuoteMeta(ns) + `["']`)
		for _, d := range decl.FindAllStringSubmatch(s, -1) {
			for _, name := range names {
				q := regexp.QuoteMeta(d[1] + ":" + name)
				s = regexp.MustCompile(`(?s)<`+q+`(\s[^>]*)?>.*?</`+q+`\s*>\s*`).ReplaceAllString(s, "")
				s = regexp.MustCompile(`<`+q+`(\s[^>]*)?/>\s*`).ReplaceAllString(s, "")
				s = regexp.MustCompile(`\s`+q+`\s*=\s*("[^"]*"|'[^']*')`).ReplaceAllString(s, "")
			}
		}
	}
	end = strings.LastIndex(s, "</rdf:RDF>")
	if end < 0 {
		return syncXMP(nil, m)
	}
	return []byte(s[:end] + block + s[end:])
}
