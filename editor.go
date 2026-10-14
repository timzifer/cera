package cera

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
)

// Editing documents (ADR 0013). An Editor records changes to a document,
// or puts a new one together, and writes the result as a new file.
//
// Everything at the level of the document — the outline, the names, the
// structure tree, the interactive form, the layers, page labels and
// metadata — belongs to the document being edited. Pages imported from
// another document bring only what is on the page: their content and
// resources, their annotations, and links to other pages imported from
// the same document; their widgets stay as plain annotations, no longer
// fields, unless the page is imported with them ([EditPage.Fields]), and
// the outline entries, named destinations and page labels of their
// document come only when asked for ([Editor.ImportPagesWith]).
// Importing a page of the edited document itself makes such a copy too.
// Layers of imported pages are added to the document's, so layers hidden
// in the source stay hidden.
//
// Deleting a page removes what hangs on it alone: its annotations, and
// form fields whose widgets were all on deleted pages. Outline entries,
// links and named destinations that pointed at it lose their target. Page
// labels stay with their pages: /PageLabels is written anew for the pages
// of the result, and a page from another document has no label.
//
// An editor makes content too (canvas.go): blank pages, and drawings over
// or under the content of any page.
//
// Pages keep their content as it is: every object a page refers to is
// copied once per source document, byte for byte, and streams keep their
// filters. What a page takes from its ancestors in the page tree
// (/Resources, /MediaBox, /CropBox, /Rotate) is written onto the page
// itself.

// ErrNoAssembly is returned for an encrypted document whose permissions
// do not allow reassembling it (bit 11 of /P), opened with the user
// password: it may not be saved as a new file, and its pages may not be
// imported.
var ErrNoAssembly = errors.New("cera: the document's permissions do not allow reassembling it")

// ErrNoUpdate is returned by [Editor.Update] for a document that cannot
// be updated incrementally: a new one, and one whose structure needed a
// repair.
var ErrNoUpdate = errors.New("cera: the document cannot be updated incrementally")

// ErrNotPermitted is returned by [Editor.SaveWith] for removing or
// changing the protection of a document opened with its user password,
// and by [Editor.Update] for a change the permissions of an encrypted
// document do not allow: changing the pages
// needs the permission to modify it or to assemble it, writing form values
// the permission to fill in forms, to annotate or to modify, adding
// annotations the permission to annotate or to modify, and flattening,
// which changes the content of pages, drawing on pages and setting metadata
// the permission to modify.
var ErrNotPermitted = errors.New("cera: the document's permissions do not allow the change")

// EditPage selects a page to import.
type EditPage struct {
	Index  int // 0-based page index in the source document
	Rotate int // extra clockwise rotation in degrees, a multiple of 90 (added to the page's /Rotate)
	// Fields brings the page's form fields with it: the part of its
	// document's field tree that leads to the page's widgets, a root
	// renamed name_2 and so on when the result has its name. They come
	// with the first copy of the page; signature fields stay plain
	// annotations. Without it, widgets stay as plain annotations.
	Fields bool
}

// ImportOptions say what imported pages bring from their document besides
// what is on them, so that merging documents does not make them worse.
type ImportOptions struct {
	// Outline brings the entries of the source's outline that lead to an
	// imported page, with the entries above them. They follow the entries
	// the result has, under an entry titled OutlineTitle, or at the top
	// level when that is empty.
	Outline      bool
	OutlineTitle string
	// Destinations brings the source's named destinations on imported
	// pages, so that links into the file by name still arrive; a name
	// the result has is renamed name_2, name_3 and so on, as field names
	// are. (Links between imported pages are kept either way.)
	Destinations bool
	// Labels keeps the page labels of the imported pages: the numbers
	// printed on them.
	Labels bool
	// Structure brings the elements of the source's structure tree whose
	// content is on imported pages, with the elements above them, so that
	// a tagged document stays tagged (PDF/UA). Where the result has a
	// tree, they follow its elements in a Part under its Document element
	// (or its root); a result without one takes the first tree brought as
	// its own. A page imported twice is tagged once, its first copy.
	Structure bool
}

// Editor records the pages of a document being edited or put together.
// It is not safe for concurrent use, but the documents it uses may be
// rendered while it is. Its methods change nothing when they fail.
type Editor struct {
	// lastID is the identity given to a page last; outline is the outline
	// SetOutline set, and outlinePrune what PruneOutline asked for; labels
	// are the page labels SetPageLabels set.
	lastID                   int
	outline                  []outlineNode
	outlineSet, outlinePrune bool
	labels                   []LabelRange
	labelsSet                bool

	base  *Document // the edited document; nil for NewEditor
	pages []editPage
	// imports is what the pages imported from a document bring with
	// them, by ImportPagesWith.
	imports map[*Document]ImportOptions
	fields  map[*Field]Value // values to write, by SetFields
	flatten bool             // the form is flattened, by Flatten
	// flattenAnnots: the annotations are flattened, by FlattenAnnotations.
	flattenAnnots bool
	// gen names the fonts of the appearances being written, while plan
	// puts the result together.
	gen *editFonts
	// noAppearance leaves the appearance streams of annotations out, for
	// tests that compare them with what the renderer draws without.
	noAppearance bool
	// unguarded writes what a certification or a signature's lock does
	// not allow, for tests of how such changes are found.
	unguarded bool
	// meta is the metadata of the result, by SetMetadata; nil to leave
	// what the edited document has.
	meta *Metadata
	// sign is the signature being made, while Sign writes.
	sign *signPlan
	// validation is the validation data AddValidation recorded.
	validation []validationFor
	// addFields are the fields added and the widgets added to fields of
	// the edited document, by AddField, and delFields the names of its
	// fields that are removed, by RemoveField.
	addFields []*addedField
	delFields map[string]bool
	// planErr is why plan could not describe the result: nothing is
	// written then.
	planErr error
}

// editPage is one page of the result: page index of src, turned by rotate
// more degrees. An own page is a page of the edited document.
type editPage struct {
	// id tells the page from the others of the result, wherever it is
	// moved: an outline entry leads to it by that.
	id     int
	src    *Document
	index  int
	rotate int
	own    bool
	fields bool
	labels bool // an imported page keeps its label
	// annots are the changes to the annotations of an own page.
	annots *annotEdits
	// A blank page, made by AddPage, has no src: it is w by h points.
	blank bool
	w, h  float64
	// draws is what was drawn on the page, by Draw.
	draws []*drawing
	// redacts are the redactions of an own page, by Redact.
	redacts []redaction
	// geo is what SetPageBox and ResizePage change of the page.
	geo *pageGeo
}

// Edit returns an editor of d with all its pages.
func (d *Document) Edit() *Editor {
	e := &Editor{base: d}
	for i := range d.NumPages() {
		e.pages = append(e.pages, editPage{id: e.newID(), src: d, index: i, own: true})
	}
	return e
}

// NewEditor returns an editor of a new, empty document.
func NewEditor() *Editor { return &Editor{} }

// NumPages returns the number of pages of the result.
func (e *Editor) NumPages() int { return len(e.pages) }

// ImportPages inserts the given pages of src before page at (NumPages to
// append), in that order; a page may repeat. They bring what is on them;
// [Editor.ImportPagesWith] brings more.
func (e *Editor) ImportPages(at int, src *Document, pages ...EditPage) error {
	return e.ImportPagesWith(at, src, ImportOptions{}, pages...)
}

// ImportPagesWith is ImportPages with options: the pages bring the outline
// entries and named destinations that lead to them, and their page labels.
// What a document brings is one thing for all its pages: the options of
// several imports from one document add up, and its outline entries are
// put under the title given last. An entry or a name whose page is not in
// the result when it is written is left out. Pages of the edited document
// imported into itself bring nothing more: it has all that already.
//
// Without [ImportOptions.Structure] imported pages are untagged.
func (e *Editor) ImportPagesWith(at int, src *Document, opt ImportOptions, pages ...EditPage) error {
	if at < 0 || at > len(e.pages) {
		return fmt.Errorf("cera: insert position %d is out of range [0, %d]", at, len(e.pages))
	}
	if !pdfedit.MayAssemble(src.r) {
		return ErrNoAssembly
	}
	n := src.NumPages()
	add := make([]editPage, len(pages))
	for i, p := range pages {
		if p.Rotate%90 != 0 {
			return fmt.Errorf("cera: page %d of the selection: rotation %d is not a multiple of 90", i, p.Rotate)
		}
		if p.Index < 0 || p.Index >= n {
			return fmt.Errorf("cera: page %d of the selection: index %d is out of range [0, %d)", i, p.Index, n)
		}
		add[i] = editPage{src: src, index: p.Index, rotate: p.Rotate, fields: p.Fields, labels: opt.Labels}
	}
	for i := range add {
		add[i].id = e.newID()
	}
	e.pages = append(e.pages[:at], append(add, e.pages[at:]...)...)
	if src != e.base && (opt.Outline || opt.Destinations || opt.Structure) {
		if e.imports == nil {
			e.imports = map[*Document]ImportOptions{}
		}
		had := e.imports[src]
		had.Outline, had.Destinations = had.Outline || opt.Outline, had.Destinations || opt.Destinations
		had.Structure = had.Structure || opt.Structure
		if opt.Outline {
			had.OutlineTitle = opt.OutlineTitle
		}
		e.imports[src] = had
	}
	return nil
}

// DeletePages removes the pages at the given indices of the result.
func (e *Editor) DeletePages(indices ...int) error {
	del := map[int]bool{}
	for _, i := range indices {
		if err := e.check(i); err != nil {
			return err
		}
		del[i] = true
	}
	kept := e.pages[:0:0]
	for i, p := range e.pages {
		if !del[i] {
			kept = append(kept, p)
		}
	}
	e.pages = kept
	return nil
}

// MovePage moves the page at index from so that it is at index to.
func (e *Editor) MovePage(from, to int) error {
	if err := e.check(from); err != nil {
		return err
	}
	if err := e.check(to); err != nil {
		return err
	}
	p := e.pages[from]
	e.pages = append(e.pages[:from], e.pages[from+1:]...)
	e.pages = append(e.pages[:to], append([]editPage{p}, e.pages[to:]...)...)
	return nil
}

// RotatePage turns the page at index i clockwise by degrees, a multiple
// of 90.
func (e *Editor) RotatePage(i, degrees int) error {
	if err := e.check(i); err != nil {
		return err
	}
	if degrees%90 != 0 {
		return fmt.Errorf("cera: rotation %d is not a multiple of 90", degrees)
	}
	e.pages[i].rotate += degrees
	return nil
}

func (e *Editor) check(i int) error {
	if i < 0 || i >= len(e.pages) {
		return fmt.Errorf("cera: page index %d is out of range [0, %d)", i, len(e.pages))
	}
	return nil
}

// Update writes the edited document's file to w as it was read, followed
// by an incremental update that makes it the result (PDF 2.0, 7.5.6). The
// original bytes stay as they are, so signatures over them stay valid, and
// the update holds only what changed: the pages of the document keep
// their objects, deleted pages and what hung on them alone are freed, and
// imported pages are copied in. An unchanged document is written as it
// is. An encrypted document stays encrypted: what the update writes is
// encrypted with its key. It returns [ErrNoUpdate] for an editor made with
// [NewEditor] and for a document that needed a repair, [ErrNotPermitted]
// for a change an encrypted document's permissions do not allow, and
// [ErrCertified] for changes a certification signature does not allow:
// any with DocMDP permissions 1, changes of the pages, of their boxes and
// size, of the outline and the page labels, drawings on the pages and
// flattened annotations with 2 and 3, added annotations with 1 and 2. The
// editor may
// be updated again. Nothing is written when Update fails, but
// for an error of w itself.
func (e *Editor) Update(w io.Writer) (err error) {
	defer recoverPanic(&err)
	if err := e.updatable(); err != nil {
		return err
	}
	err = pdfedit.Update(w, e.plan())
	switch {
	case errors.Is(err, pdfedit.ErrRepaired):
		return fmt.Errorf("%w: %v", ErrNoUpdate, err)
	case err != nil:
		return fmt.Errorf("cera: %w", err)
	}
	return nil
}

// updatable reports why the changes of the editor cannot be written as an
// update of the edited document, nil if they can.
func (e *Editor) updatable() error {
	if e.base == nil {
		return fmt.Errorf("%w: %v", ErrNoUpdate, pdfedit.ErrNoBase)
	}
	if e.redacts() {
		return fmt.Errorf("%w: an update keeps the bytes a redaction is to remove: save the document as a new file", ErrNoUpdate)
	}
	if sg := e.base.signing(); !e.unguarded && (sg.mdp == 1 && (len(e.fields) > 0 || e.pagesChanged() || e.annotated() || e.meta != nil) ||
		sg.mdp != 0 && (e.outlined() || e.labelsSet) ||
		sg.mdp == 2 && (e.pagesChanged() || e.annotated()) || sg.mdp == 3 && e.pagesChanged() ||
		sg.mdp != 0 && (e.flattens() || e.drawsOn() || e.formed() || e.resized())) {
		return ErrCertified
	}
	if prot, ok := e.base.r.Protection(); ok && !prot.Owner {
		perm := prot.Permissions
		if e.pagesChanged() && perm&(pdf.PermModify|pdf.PermAssemble) == 0 ||
			len(e.fields) > 0 && perm&(pdf.PermFillForms|pdf.PermAnnotate|pdf.PermModify) == 0 ||
			e.annotated() && perm&(pdf.PermAnnotate|pdf.PermModify) == 0 ||
			(e.flattens() || e.drawsOn() || e.formed() || e.resized() || e.outlined() || e.labelsSet || e.meta != nil) && perm&pdf.PermModify == 0 {
			return ErrNotPermitted
		}
	}
	return nil
}

// plan describes the result for the internal writer.
func (e *Editor) plan() pdfedit.Doc {
	doc := pdfedit.Doc{Pages: make([]pdfedit.Page, len(e.pages))}
	if e.base != nil {
		doc.Base = e.base.r
	}
	for i, p := range e.pages {
		if p.blank {
			doc.Pages[i] = pdfedit.Page{Rotate: p.rotate, Width: p.w, Height: p.h}
		} else {
			doc.Pages[i] = pdfedit.Page{Src: p.src.r, Index: p.index, Rotate: p.rotate, Own: p.own, Fields: p.fields}
		}
		if p.geo != nil {
			doc.Pages[i].Geo = p.geo.plan()
		}
	}
	// Object 0 is Helvetica, written only when an appearance uses it.
	doc.Objects = []pdf.Object{pdf.NewDict(
		pdf.Entry{Key: "Type", Val: pdf.Name("Font").Object()},
		pdf.Entry{Key: "Subtype", Val: pdf.Name("Type1").Object()},
		pdf.Entry{Key: "BaseFont", Val: pdf.Name("Helvetica").Object()},
		pdf.Entry{Key: "Encoding", Val: pdf.Name("WinAnsiEncoding").Object()},
	).Object()}
	e.gen = &editFonts{objs: &doc.Objects}
	defer func() { e.gen = nil }()
	doc.Patches = e.fieldPatches(&doc.Objects)
	if e.base != nil {
		doc.Patches = e.annotPatches(doc.Patches, &doc.Objects)
	}
	if labels, ok := e.pageLabels(); ok {
		if e.base == nil {
			doc.PageLabels = labels
		} else if root, ok := e.base.r.Trailer().Get("Root").Ref(); ok {
			p := pdfedit.Patch{Ref: root, Del: []pdf.Name{"PageLabels"}}
			if !labels.IsZero() {
				p = pdfedit.Patch{Ref: root, Set: []pdf.Entry{{Key: "PageLabels", Val: labels.Object()}}}
			}
			doc.Patches = append(doc.Patches, p)
		}
	}
	e.outlinePlan(&doc)
	for src, opt := range e.imports {
		if doc.Imports == nil {
			doc.Imports = map[*pdf.Document]pdfedit.Import{}
		}
		doc.Imports[src.r] = pdfedit.Import{
			Outline: opt.Outline, OutlineTitle: opt.OutlineTitle, Destinations: opt.Destinations, Structure: opt.Structure,
		}
	}
	if e.meta != nil {
		e.metaPatches(&doc)
	}
	if e.base != nil && e.formed() {
		e.fieldPlan(&doc)
	}
	e.planErr = nil
	if e.base != nil && e.redacts() {
		e.planErr = e.redactPlan(&doc)
	}
	if e.base != nil {
		e.dssPlan(&doc)
	}
	cf := e.canvasForms(&doc)
	e.drawPlan(&doc, cf)
	if e.sign != nil {
		e.signPlan(&doc, cf)
	}
	e.gen.finish()
	return doc
}

// Encryption protects a file the editor writes: with AES-256 (PDF 2.0),
// under a file key of its own.
type Encryption struct {
	// UserPassword opens the file with the permissions below; it may be
	// empty, for a file anyone opens and no one may change.
	UserPassword string
	// OwnerPassword opens it with all permissions, and is needed to
	// remove or change the protection. When empty the file gets a random
	// one that nobody knows.
	OwnerPassword string
	// Permissions are what a reader with the user password may do.
	Permissions Permissions
}

// SaveOptions say how [Editor.SaveWith] writes the file.
type SaveOptions struct {
	// Encrypt, when set, encrypts the file anew.
	Encrypt *Encryption
	// Decrypt writes the file of an encrypted document without its
	// protection.
	Decrypt bool
	// ObjectStreams says whether the file is written in the compact form
	// of PDF 1.5: the objects that are not streams packed into object
	// streams, compressed, with a cross-reference stream. A form with
	// thousands of small objects is a tenth smaller for it. The zero
	// value, ObjectStreamsAsSource, writes the edited document as it was
	// read, and a new document in the classic form.
	ObjectStreams ObjectStreamMode
	// Linearize lays the file out for a reader that gets it piece by
	// piece ("fast web view"; PDF 2.0, Annex F): what the first page needs
	// comes first, and a table of hints says where the other pages are, so
	// that a viewer shows a page before the rest of the file has arrived.
	// The whole file is laid out in memory before any of it is written. An
	// update appended later (Editor.Update) makes it an ordinary file
	// again.
	Linearize bool

	// The rest makes the file smaller without changing what it shows.
	// Save copies what the pages reach as it is; each of these looks at it
	// first, which takes the time of reading the document through.

	// PruneResources writes with every page the resources its content
	// names and no others, and likewise with the form XObjects, tiling
	// patterns and Type 3 fonts that content draws. A resource dictionary
	// that all pages share keeps every font and image of the document
	// with every page: after DeletePages, or in a file made of some pages
	// of another, what only the other pages drew is then left out. A page
	// whose content cannot be read to the end keeps what it has.
	PruneResources bool
	// JoinDuplicates writes streams that are the same — an image or a
	// font program embedded once for every page it is on — and the fonts
	// of the same program once, also those of different documents.
	JoinDuplicates bool
	// Compress stores the streams that the document has plain, or under a
	// filter that packs worse (LZW, run length, ASCII85, ASCIIHex), with
	// the Flate filter where that is shorter. Metadata streams stay plain.
	Compress bool
	// Drop names parts of the document that are left out.
	Drop Drop

	// SubsetFonts writes the font programs embedded whole with the
	// glyphs the pages show only, and tags their names as subsets:
	// TrueType programs and CFF programs keyed by name. The fonts of the
	// form, which a viewer writes typed text with, stay whole.
	SubsetFonts bool

	// Images, when set, makes the images smaller, at a loss: resampled
	// where they are drawn finer than needed, photographs stored as JPEG,
	// bilevel images as CCITT Group 4.
	Images *ImageOptions
}

// Drop names parts of a document a viewer does not need to show it, for
// [SaveOptions.Drop] to leave out.
type Drop uint

const (
	// DropThumbnails leaves out the thumbnail images of the pages:
	// viewers make their own.
	DropThumbnails Drop = 1 << iota
	// DropPieceInfo leaves out the private data that the programs which
	// made the document keep in it, with the document, its pages and its
	// form XObjects (/PieceInfo): what an illustration program needs to
	// edit its file again, often larger than the page it belongs to.
	DropPieceInfo
	// DropObjectMetadata leaves out the metadata streams of the pages,
	// images, fonts and other objects. The metadata of the document
	// itself stays.
	DropObjectMetadata
)

// ObjectStreamMode says whether a file is written with object streams;
// see [SaveOptions.ObjectStreams].
type ObjectStreamMode int

const (
	// ObjectStreamsAsSource packs the objects of a document that was
	// packed, and of no other.
	ObjectStreamsAsSource ObjectStreamMode = iota
	// ObjectStreamsAlways packs them: the smallest file, which readers
	// from before PDF 1.5 (2003) do not read.
	ObjectStreamsAlways
	// ObjectStreamsNever writes every object on its own, with a
	// cross-reference table.
	ObjectStreamsNever
)

// Save writes the result to w as a new file. An encrypted document stays
// encrypted, with its passwords and permissions: use [Editor.SaveWith] to
// change that. A signed document is refused with [ErrSigned]: a new file
// invalidates its signatures, and [Editor.Update] keeps them. A document
// whose embedded files alone are encrypted is refused with [ErrPassword]
// unless it was opened with the password. The editor may be saved again.
// Nothing is written when Save fails, but for an error of w itself.
func (e *Editor) Save(w io.Writer) error { return e.SaveWith(w, SaveOptions{}) }

// SaveWith is Save with options: to protect the file with passwords and
// permissions, or to write an encrypted document plain. Either changes the
// protection of an encrypted document, which needs the owner password:
// [ErrNotPermitted] for a document opened with the user password. Pages
// imported from other documents are encrypted, or not, as the result is.
func (e *Editor) SaveWith(w io.Writer, opt SaveOptions) (err error) {
	defer recoverPanic(&err)
	doc := e.plan()
	if e.planErr != nil {
		return e.planErr
	}
	if doc.Base != nil && !pdfedit.MayAssemble(doc.Base) {
		return ErrNoAssembly
	}
	switch opt.ObjectStreams {
	case ObjectStreamsAsSource:
		doc.Packed = e.base != nil && e.base.r.Packed()
	case ObjectStreamsAlways:
		doc.Packed = true
	case ObjectStreamsNever:
	default:
		return fmt.Errorf("cera: object stream mode %d", opt.ObjectStreams)
	}
	if opt.Drop&^(DropThumbnails|DropPieceInfo|DropObjectMetadata) != 0 {
		return fmt.Errorf("cera: parts to drop %#x", uint(opt.Drop))
	}
	doc.Prune, doc.Join, doc.Compress = opt.PruneResources, opt.JoinDuplicates, opt.Compress
	if opt.Images != nil {
		if err := e.imagePlan(&doc, opt.Images); err != nil {
			return err
		}
	}
	if opt.SubsetFonts {
		if err := e.fontPlan(&doc); err != nil {
			return err
		}
	}
	doc.Linear = opt.Linearize
	if opt.Drop&DropThumbnails != 0 {
		doc.Drop |= pdfedit.DropThumbnails
	}
	if opt.Drop&DropPieceInfo != 0 {
		doc.Drop |= pdfedit.DropPieceInfo
	}
	if opt.Drop&DropObjectMetadata != 0 {
		doc.Drop |= pdfedit.DropMetadata
	}
	if e.base != nil && e.base.signing().signed {
		return ErrSigned
	}
	// Embedded files that the password did not open would be written as
	// nothing.
	for _, p := range e.pages {
		if !p.blank && p.src.r.FilesLocked() {
			return ErrPassword
		}
	}
	owner, encrypted := true, false
	if e.base != nil {
		if prot, ok := e.base.r.Protection(); ok {
			owner, encrypted = prot.Owner, true
		}
	}
	switch {
	case opt.Encrypt != nil && opt.Decrypt:
		return errors.New("cera: a file is encrypted or decrypted, not both")
	case (opt.Encrypt != nil || opt.Decrypt) && encrypted && !owner && !e.unguarded:
		return ErrNotPermitted
	case opt.Encrypt != nil:
		enc := opt.Encrypt
		ownerPassword := enc.OwnerPassword
		if ownerPassword == "" {
			random := make([]byte, 24)
			if _, err := rand.Read(random); err != nil {
				return fmt.Errorf("cera: %w", err)
			}
			ownerPassword = hex.EncodeToString(random)
		}
		crypt, dict, err := pdf.NewEncryptor(enc.UserPassword, ownerPassword, pdf.Permissions(enc.Permissions), false)
		if err != nil {
			return fmt.Errorf("cera: %w", err)
		}
		doc.Crypt, doc.Encrypt = crypt, dict
	case encrypted && !opt.Decrypt:
		// As it was: the document's key and its encryption dictionary.
		d := e.base
		if dict, ok := d.inline(d.r.Trailer().Get("Encrypt"), 0).Dict(); ok {
			doc.Crypt, doc.Encrypt = d.r.Encryptor(), dict
		}
	}
	if err := pdfedit.Write(w, doc); err != nil {
		return fmt.Errorf("cera: %w", err)
	}
	return nil
}

// inline returns o with the references in it followed: a value of direct
// objects, as an encryption dictionary is written.
func (d *Document) inline(o pdf.Object, depth int) pdf.Object {
	o = d.resolve(o)
	if depth > 8 {
		return pdf.Null
	}
	switch o.Kind() {
	case pdf.KindDict:
		dict, _ := o.Dict()
		var entries []pdf.Entry
		for k, v := range dict.All() {
			entries = append(entries, pdf.Entry{Key: k, Val: d.inline(v, depth+1)})
		}
		return pdf.NewDict(entries...).Object()
	case pdf.KindArray:
		arr, _ := o.Array()
		out := make(pdf.Array, len(arr))
		for i, v := range arr {
			out[i] = d.inline(v, depth+1)
		}
		return out.Object()
	case pdf.KindStream:
		return pdf.Null
	}
	return o
}
