package cera

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/timzifer/cera/internal/pdf"
)

// Files embedded in a document (PDF 2.0, 7.11.4): the XML of an e-invoice
// (ZUGFeRD, Factur-X), the data behind a report, the documents of a
// portfolio. A file specification with an embedded file stream (/EF) is
// one; cera reads them from the /EmbeddedFiles name tree of the catalogue,
// from its associated files (/AF), and from FileAttachment annotations.
// File specifications that name a file outside the document are not
// attachments.

// ErrAttachmentTooLarge is returned by [Attachment.Open] for a file that
// decodes to more than cera reads into memory (256 Mi bytes).
var ErrAttachmentTooLarge = errors.New("cera: the attachment is too large")

// Attachment is a file embedded in a document.
type Attachment struct {
	// Name is the file name (/UF, else /F, else the name the document
	// lists it under), Description what the document says about it
	// (/Desc).
	Name        string
	Description string
	// MIME is the media type of the file (/Subtype of the embedded file
	// stream), such as "application/xml"; empty when the file gives none.
	MIME string
	// Size is the size of the file in bytes as the document states it
	// (/Params /Size), -1 if it says none. Open tells the size there is.
	Size int64
	// Created and Modified are zero when the document does not say.
	Created  time.Time
	Modified time.Time
	// Relationship is what the file is to the document or to the part of
	// it that names it (/AFRelationship): "Source", "Data", "Alternative",
	// "Supplement", "EncryptedPayload", "FormData", "Schema" or
	// "Unspecified"; empty when it says none. The XML of an e-invoice is
	// "Alternative" or "Data".
	Relationship string

	doc    *Document
	stream *pdf.Stream
}

// Open returns the content of the file, decoded and, in an encrypted
// document, decrypted. It is read into memory: [ErrAttachmentTooLarge]
// for more than 256 Mi bytes. A document may encrypt its attachments
// alone; it then opens without the password, and Open returns
// [ErrPassword] until the document is opened with it.
func (a *Attachment) Open() (io.ReadCloser, error) {
	if a == nil || a.stream == nil {
		return nil, errors.New("cera: the attachment has no content")
	}
	if a.doc.r.FilesLocked() {
		return nil, ErrPassword
	}
	data, _, err := a.doc.r.DecodeStrict(a.stream)
	if err != nil {
		return nil, fmt.Errorf("cera: %w", err)
	}
	if len(data) > maxAttachmentBytes {
		return nil, ErrAttachmentTooLarge
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// Attachments returns the files embedded in the document: those of the
// /EmbeddedFiles name tree, in its order, the order of their names, and
// then the associated files of the catalogue (/AF) that are not among
// them. The files of FileAttachment annotations are on their pages
// ([Annotation.File]). Read once and shared; nil for a document without.
func (d *Document) Attachments() []*Attachment {
	d.attachOnce.Do(func() {
		defer func() {
			if recover() != nil {
				d.attachments = nil
			}
		}()
		cat, err := d.r.Catalog()
		if err != nil {
			return
		}
		seen := map[pdf.Ref]bool{} // file specifications listed
		add := func(o pdf.Object, name string) {
			if len(d.attachments) >= maxAttachments {
				return
			}
			if ref, ok := o.Ref(); ok {
				if seen[ref] {
					return
				}
				seen[ref] = true
			}
			if a := d.attachment(o, name); a != nil {
				d.attachments = append(d.attachments, a)
			}
		}
		nodes := 0
		visited := map[pdf.Ref]bool{}
		var walk func(o pdf.Object, depth int)
		walk = func(o pdf.Object, depth int) {
			if ref, ok := o.Ref(); ok {
				if visited[ref] {
					return
				}
				visited[ref] = true
			}
			node := d.dict(o)
			if nodes++; node.IsZero() || depth > 32 || nodes > maxAttachments {
				return
			}
			names, _ := d.resolve(node.Get("Names")).Array()
			for i := 0; i+1 < len(names); i += 2 {
				add(names[i+1], textString(d.resolve(names[i])))
			}
			kids, _ := d.resolve(node.Get("Kids")).Array()
			for _, k := range kids {
				walk(k, depth+1)
			}
		}
		walk(d.dict(cat.Get("Names")).Get("EmbeddedFiles"), 0)
		af, _ := d.resolve(cat.Get("AF")).Array()
		for _, o := range af {
			add(o, "")
		}
	})
	return d.attachments
}

// attachment reads the file specification o, listed under name; nil if it
// embeds no file.
func (d *Document) attachment(o pdf.Object, name string) *Attachment {
	fs := d.dict(o)
	ef := d.dict(fs.Get("EF"))
	if ef.IsZero() {
		return nil
	}
	var s *pdf.Stream
	for _, key := range []pdf.Name{"UF", "F", "DOS", "Mac", "Unix"} {
		if s = d.stream(ef.Get(key)); s != nil {
			break
		}
	}
	if s == nil {
		return nil
	}
	a := &Attachment{Name: name, Size: -1, doc: d, stream: s}
	for _, key := range []pdf.Name{"UF", "F"} {
		if n := textString(d.resolve(fs.Get(key))); n != "" {
			a.Name = n
			break
		}
	}
	a.Description = textString(d.resolve(fs.Get("Desc")))
	if r, ok := d.name(fs.Get("AFRelationship")); ok {
		a.Relationship = string(r)
	}
	if m, ok := d.name(s.Dict.Get("Subtype")); ok {
		a.MIME = string(m)
	}
	params := d.dict(s.Dict.Get("Params"))
	if n, ok := d.integer(params.Get("Size")); ok && n >= 0 {
		a.Size = int64(n)
	}
	a.Created = parseDate(textString(d.resolve(params.Get("CreationDate"))))
	a.Modified = parseDate(textString(d.resolve(params.Get("ModDate"))))
	return a
}
