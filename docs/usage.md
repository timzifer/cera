# Usage

Open a document, render a page into an image the caller owns, read its text:

```go
doc, err := cera.Open(data)
page, err := doc.Page(0)
dst := image.NewRGBA(page.Bounds(150.0 / 72)) // owned by the caller, reusable
var st cera.Stats
err = page.Render(ctx, dst, cera.RenderOptions{
	Scale:      150.0 / 72,
	Background: color.RGBA{255, 255, 255, 255},
	Deadline:   time.Now().Add(time.Second), // partial image + ErrDeadline
	Stats:      &st,                         // ops, fills, unsupported features
	Workers:    0,                           // goroutines per page; 0 = all cores
})
page.Release() // drop the cached display list when the page leaves the view

text, err := page.Text(ctx) // characters, lines and blocks in reading order
fmt.Println(text.String())
```

A Document is safe for concurrent use: render its pages from as many
goroutines as there are cores, each with `Workers: 1`, or one page with all
of them; objects, fonts and images are loaded once and shared.

A viewer that turns its view asks for the page turned:
`page.Rotated(90)` is the page as it is shown then — a `Page` with its
own bounds, display list and text, the same each time it is asked for.
Everything in points of the displayed page is in its coordinates:
`Bounds`, `Render`, `Text`, `Links`, `Annotations` and `EditAnnotation`.
Where an API takes a page number, the view is said beside it:
`page.Rotated(90).Destination(&target)` for a link's target,
`index.SetRotation(90)` for a `TextIndex`, and `View: 90` in a
`NewAnnotation` or `Highlight` whose geometry was taken from the turned
view. `Page.Rotate` is for reading.

A page for print has more boxes than the one a viewer shows.
`page.Boxes()` returns them — `Media`, `Crop`, `Bleed`, `Trim`, `Art` —
in default user space, with the defaults of the specification for those
a page does not have; `page.DisplayMatrix()` maps them to the points of
the displayed page, where a viewer of print files outlines them.
`page.InBox(cera.BoxMedia)` is the page showing its whole medium with
the marks around it, and `page.InBox(cera.BoxTrim)` the finished page, as
a thumbnail wants it: views like those of `Rotated`, which they combine
with, and said beside a page number in the same way, by
`index.SetBox(cera.BoxMedia)` and `Box: cera.BoxMedia` in a
`NewAnnotation` or `Highlight`. The command draws one with `-box`.

A file need not be in memory: `cera.OpenReaderAt(f, size, opts)` reads
through an `io.ReaderAt` (an `*os.File`, a browser `File` in `js/wasm`)
only what the pages drawn need.

A document keeps what it decodes for the next page that needs it: 256 MB
of images and 64 MB of streams (content, forms, Type 3 glyphs) at most,
least recently used dropped first. That suits one document in a viewer.
The bounds are options, and the caches can be emptied and shared:

```go
// A thumbnailer draws each page once: keep nothing.
doc, err := cera.OpenWith(data, cera.OpenOptions{ImageCacheBytes: -1, StreamCacheBytes: -1})

// js/wasm, where the heap is small.
doc, err = cera.OpenWith(data, cera.OpenOptions{ImageCacheBytes: 32 << 20, StreamCacheBytes: 8 << 20})
doc.Trim() // the tab went to the background: drop what is decoded

// A server with many documents open: one bound for all of them.
cache := cera.NewCache(512<<20, 128<<20)
doc, err = cera.OpenWith(data, cera.OpenOptions{Cache: cache})
doc.Trim() // done with the document; otherwise its entries leave as others come in

// What the process keeps between renders, whatever the document.
cera.SetPoolLimits(cera.PoolLimits{GlyphCaches: 4, LayerBytes: 8 << 20})
```

`Trim` leaves the document and its pages usable: what they show is decoded
again when it is drawn. It does not free the display list of a page —
`Page.Release` does — nor the fonts and colour spaces the document has
loaded, which go with the document. A bound of 0 is the default, a negative
one none. The budgets on what one input may cost
([budgets.md](budgets.md)) are not options.

An encrypted file opens with its password. `ErrPassword` says that it
needs one, or another one, and `ErrUnsupportedEncryption` that no password
helps:

```go
doc, err := cera.Open(data)
for errors.Is(err, cera.ErrPassword) {
	pw, ok := askPassword()
	if !ok {
		return
	}
	doc, err = cera.OpenWith(data, cera.OpenOptions{Password: pw})
}

if doc.Encrypted() && doc.Permissions()&cera.PermCopy == 0 {
	// the author does not want text copied
}
```

`Permissions` is what the author allows a reader with the user password,
and everything for the owner password and for a file that is not
encrypted. The editor refuses what they forbid; rendering and text
extraction do not ask, so whether to print or to copy is the
application's decision.

Fonts a document names but does not embed can come from a provider; the
CJK module links Noto Sans only for the collections it is asked for:

```go
import (
	"github.com/timzifer/cera/fonts/cjk"
	"github.com/timzifer/cera/fonts/cjk/japan1"
)

doc, err := cera.OpenWith(data, cera.OpenOptions{
	Fonts: cjk.Provider{japan1.Collection}, // or a cera.FontProvider of your own
})
```

Helvetica, Times and Courier that a document does not embed are drawn with
TeX Gyre Heros, Termes and Cursor, the shapes PDFium and MuPDF draw too.
They add 1.45 MB to the binary; the build tag `cera_nogyre` leaves them
out, and Arimo, Tinos and Cousine (same metrics, Arial's, Times New
Roman's and Courier New's shapes) draw that text instead. Line lengths and
layout are the same either way:

```sh
go build -tags cera_nogyre ./...
GOOS=js GOARCH=wasm go build -C cmd/cera -tags cera_nogyre -o "$PWD/cera.wasm" .
```

CMYK (DeviceCMYK, and ICCBased spaces of four components whose profile
cera cannot read) converts through a bundled SWOP press profile (CGATS
TR 005). A caller who prints or proofs against a known condition gives
cera that profile instead; documents opened with the same profile share
its tables. An ICCBased space whose own CMYK profile cera reads (lut8,
lut16 or lutAtoB tables) converts through it, as PDFium and MuPDF do:

```go
icc, err := os.ReadFile("CoatedFOGRA39.icc")
doc, err := cera.OpenWith(data, cera.OpenOptions{
	CMYKProfile: icc,   // an error from OpenWith if cera cannot read it
	NaiveCMYK:   false, // true: device values, R = (1-C)(1-K), over all profiles
})
```

PDFium and MuPDF convert DeviceCMYK close to Adobe's U.S. Web Coated
(SWOP) v2, which may not be redistributed modified; with that profile as
`CMYKProfile`, cera's CMYK colours come within a few levels of MuPDF's
(ADR 0003).

A PDF/X, PDF/A or PDF/E file names the printing condition its device
colours are meant for, usually with the profile (`/OutputIntents`). Where
the caller gives no profile, cera converts DeviceCMYK through that one, as
Acrobat and MuPDF do, so a file made for uncoated paper or newsprint is
shown with those colours. `OpenOptions.IgnoreOutputIntent` keeps the
bundled profile, as PDFium does.

Layers (optional content) are switched per render, not on the document, so
several views can show different layers; switching draws the cached display
list again without interpreting the page:

```go
cfg := doc.Layers() // nil without /OCProperties
for _, l := range cfg.Layers {
	fmt.Println(l.Name, l.Visible)
}
vis := cfg.Visibility().With(cfg.Layers[0], false)
err = page.Render(ctx, dst, cera.RenderOptions{Scale: 150.0 / 72, Layers: &vis})
```

Annotations are drawn over the page content, as on screen by default;
`RenderOptions.Annotations` selects `AnnotsPrint` or `AnnotsNone`, and
`SkipAnnotation` leaves single ones out (a viewer drawing form fields
itself). Both only redraw the cached display list:

```go
for _, a := range page.Annotations() {
	if a.Link != nil {
		fmt.Println(a.Rect, a.Link.URI, a.Link.Page)
	}
}
```

Interactive forms (ADR 0006): `Document.Form` reads the fields of
`/AcroForm` with their widgets, and a `FormState` the caller owns holds
the values a user enters. Rendering with `RenderOptions.Form` shows them
through generated appearances, recorded once per widget and value; the
page itself is not interpreted again. A `FormLayer` places native widgets
of a `FormWidgetProvider` over the page image and leaves them out of it;
[`form/fyneform`](../form/fyneform) is such a provider for fyne, a module of
its own so that cera stays free of toolkits and cgo:

```go
form := doc.Form() // nil without /AcroForm
state := form.NewState()
err = state.SetValue(form.Field("address.street"), cera.TextValue("Main St 1"))
// A field with Computed set is formatted, validated and calculated as its
// scripts say: SetValue refuses a number out of range (ErrInvalidValue)
// and recalculates the totals; f.Format(state.Value(f)) is what it shows.

layer := cera.NewFormLayer(page, state, provider)
layer.Update(cera.View{Scale: scale, Viewport: visible})
err = page.Render(ctx, dst, cera.RenderOptions{Scale: scale, Form: state, SkipAnnotation: layer.Skip})
```

A push button does what its widget's `Actions` say, in their order. The
state carries out those of the form — `ResetForm` sets the fields named to
their defaults, `Hide` shows or hides widgets, which renders with the
state and a `FormLayer` then obey — and the rest is the viewer's:

```go
for _, a := range widget.Actions {
	switch {
	case state.Do(a): // ResetForm, Hide
	case a.Target != nil:
		follow(a.Target) // GoTo, URI, Named: as for a link
	case a.Kind == "SubmitForm":
		body, contentType, err := state.Submission(a) // FDF, XFDF or an HTML form
		// ask the user, then send body to a.URL — or do not: cera sends nothing
	case a.Kind == "JavaScript":
		// a.Script: reported, not run
	}
}
```

`state.SetHidden` and `state.ResetFields` do the same by hand. What a
state hides is how the document is shown, not part of it: `SetFields`
does not write it.

The values of a state travel as FDF or XFDF, the exchange formats of form
data: `state.ExportFDF(w)` and `state.ExportXFDF(w)` write every field
that has a value, by its name — but for passwords —, and
`state.ImportFDF(r)` and `state.ImportXFDF(r)` set the fields the data
names, as `SetValue` does, so that the form checks them and calculated
fields follow:

```go
err = state.ImportXFDF(f)
var fail *cera.FormDataError
if errors.As(err, &fail) {
	// the rest is set; fail.Unknown: names the form does not have,
	// fail.Refused: values it does not take (ErrInvalidValue, ErrReadOnly)
}
err = e.SetFields(state) // and written into the document
```

Rich text values are dropped for the plain ones beside them, and what
else the formats carry — annotations, pages, scripts — is not read.

`dst` may be any sub-rectangle of the page (a tile or viewport): only that
region is drawn. `RenderOptions.Region` narrows it further. Errors in the
content never stop a page; a non-nil error means a partial image
(`ErrDeadline`, a rasterizer budget, or a recovered `*PanicError`).

`Page.Run` drives any `Device` directly, without a display list; a device
that also implements `TextDevice` receives every string shown, in every
render mode, and one that implements `MarkedContentDevice` the
marked-content sequences (tag, MCID, `/ActualText`, `/Alt`, `/Lang`).
`Page.Text` is built on both: each `TextChar` has the `Quad` of its em
box, its baseline direction and advance, its MCID and whether it is an
artifact. Its coordinates are points of the displayed page, y down, after
`/Rotate` and `/UserUnit`; `Page.DisplayMatrix` maps user space to them.

`Page.Text` lays the characters out for reading: `PageText.Chars` are in
reading order, `Lines` and `Blocks` index into them. It drops the copies
fake bold draws on top of a character, splits ligatures (also U+FB00 to
U+FB06) into one character each, puts `/ActualText` on the first glyph it
replaces, joins text shown out of order on one baseline, and orders blocks
by the structure tree of a tagged PDF, the rest by an XY cut: columns,
rows, text in other directions last. `TextLine.Hyphenated` marks a word
broken across lines. `String` separates lines by a line break and blocks
by an empty line. `Page.TextWith` selects layers, annotations and form
values, and can ignore the structure tree.

Arabic and Hebrew come out as they are read and typed: a page shows them
in visual order and as presentation forms, and the layout puts the
characters of such a line into logical order (the Unicode bidirectional
algorithm, numbers and Latin words inside kept as they read) and gives
the letters for the forms, so the text can be searched and copied.
`TextLine.RTL` marks a line that reads from the right — its first
character is at its right end, and `PosAt` and `Select` follow the
pointer there; `Quads` of a range across both directions gives a box for
each stretch. The columns of a page that reads from the right, by its
lines or by `/ViewerPreferences /Direction /R2L`, are read from the
right.

Selection works on the `PageText` alone, in points of the displayed page;
a viewer divides its pointer positions by its zoom:

```go
a := text.PosAt(x0, y0)                       // where the drag started
b := text.PosAt(x1, y1)                       // where the pointer is
sel := text.Select(a, b, cera.SelectWords)    // SelectChars, SelectWords, SelectLines, SelectBlocks
for _, q := range text.Quads(sel) { /* highlight q, scaled by the zoom */ }
copied := text.TextOf(sel, cera.TextFormat{JoinHyphenated: true})
```

`WordAt` and `LineAt` give the word and line for a double and triple
click, `CharAt` the character under the pointer, and `InRect` the
characters in a rectangle (a column of a table). A selection is a
`TextRange` of indices into `Chars`: keep as many as there are marks.
To store a mark, keep bytes of the text instead, which outlive changes to
the layout: `Offset` maps a position to its byte offset in `String`, and
`RangeOfOffsets` maps a byte range back.

Search runs on the text as `String` has it, with runs of white space as
one space, soft hyphens left out and hyphenated words joined, so a query
finds a word broken across lines. A `TextIndex` keeps the laid-out text
of recently used pages and searches the document from a position on,
wrapping, forward or back:

```go
x := cera.NewTextIndex(doc, cera.TextOptions{}, 0) // 0 keeps 64 pages
opt := cera.SearchOptions{WholeWord: true}        // also MatchCase, IgnoreDiacritics, Context
for m, err := range x.Find(ctx, "gamma", opt, cera.DocPos{Page: cur}, false) {
	if err != nil { /* a page that failed, or ctx ended */ continue }
	text, _ := x.Page(ctx, m.Page)
	show(m.Page, text.Quads(m.Range), m.Quote.Prefix, m.Quote.Exact, m.Quote.Suffix)
}
```

Each match carries a `Quote`: its text and up to `Context` characters
before and after it. A quote is an anchor that survives a new extraction
and works for text layers that are not cera's (such as GROBID's), where
byte offsets mean nothing: `QuoteOf` makes one from a range, and
`PageText.FindQuote` or `TextIndex.FindQuote` (from a page outward) find
the place whose surroundings agree most. `TextIndex.TextOf` copies a
selection across pages, from one `DocPos` to another.

For the pointer, `TextIndex.Hit` says what lies at a point of the
displayed page, and a click on a link resolves its target to a view:

```go
h, _ := x.Hit(ctx, page, px/zoom, py/zoom)
switch h.Kind {
case cera.HitLink: // hand; on click:
	if t := h.Link.Target; t.URI != "" {
		open(t.URI)
	} else if d, ok := doc.Destination(&t); ok {
		show(d.Page, d.Fit, d.X, d.Y, d.Zoom, d.Rect) // displayed points of page d.Page
	} else if n, ok := t.NamedPage(current, doc.NumPages()); ok {
		showPage(n) // /Named: NextPage, PrevPage, FirstPage, LastPage
	} else if t.External {
		ask(t.File) // /GoToR, /GoToE, /Launch: cera follows none of them
	} else {
		say(t.Action) // "JavaScript", "SubmitForm", ...: nothing cera runs
	}
case cera.HitWidget: // the form layer's; h.Widget
case cera.HitText:   // I-beam; h.Pos starts a selection
}
```

`Page.Links` returns the link annotations with their quads (from
`/QuadPoints`, else the rectangle), `PageText.URLs` the addresses written
in the text. Every link with a destination or an action is one:
`LinkTarget.Action` names the action, and of chained actions (`/Next`)
the target is the first that leads somewhere — a script before a `GoTo`
is skipped. A relative URI is resolved against the document's base
(`/URI /Base`).

`Page.Annotations` lists the annotations, and for the markup among them
(notes, highlights, ink, shapes, free text, stamps) `Markup` says what a
comments panel shows: author and dates, colour, the exact geometry in
points of the displayed page, the popup, and which annotation one replies
to:

```go
for _, a := range page.Annotations() {
	m := a.Markup
	if m == nil || m.Reply != nil {
		continue // no markup, or a reply: shown under its annotation
	}
	addComment(a.Index, a.Subtype, m.Author, m.Modified, a.Contents, m.Bounds)
}
```

A highlight written with `Editor.AddHighlight` reads back with the quads,
colour, opacity, author, date and note it was made from.

`Document.PageLabel(i)` is the number printed on page i — "iv", "12",
"A-3" — for the page box of a viewer, and `Document.PageByLabel` finds
the page for what a user types into it; both say nothing in a document
without `/PageLabels`.

`Document.Outline` returns the bookmarks for a side panel, a tree of
entries with a title, its style and a target that resolves like a link's:

```go
var show func(items []cera.OutlineItem, depth int)
show = func(items []cera.OutlineItem, depth int) {
	for _, it := range items {
		row := addRow(depth, it.Title, it.Bold, it.Italic, it.Color, it.Open)
		if d, ok := doc.Destination(it.Target); ok {
			row.onClick(func() { goTo(d) })
		}
		show(it.Children, depth+1)
	}
}
show(doc.Outline(), 0)
```

`Document.ViewSettings` is how the document asks to be opened: where to
start, which panel to show, how to lay out the pages, and what its print
dialogue starts with. They are wishes, with the defaults of the
specification where the document has none:

```go
vs := doc.ViewSettings()
if d, ok := doc.Destination(vs.Open); ok {
	goTo(d) // the page and zoom of /OpenAction
}
switch vs.PageMode {
case "UseOutlines":
	showOutline()
case "UseThumbs":
	showThumbnails()
case "UseAttachments":
	showAttachments()
}
setLayout(vs.PageLayout, vs.RightToLeft) // "TwoPageRight": spreads with a cover page
if vs.DisplayDocTitle {
	setTitle(doc.Metadata().Title)
}
printDialog(vs.Print.Ranges, vs.Print.Copies, vs.Print.Duplex, vs.Print.Scaling == "None")
```

`Open` is nil for an `/OpenAction` that does not lead to a place in the
document — a script, an address, another file.

## Structure

A tagged document says what its content is: headings, paragraphs, lists,
tables with their cells, figures with a description. `Document.Structure`
returns that tree, nil for a document without one — what a bridge to a
screen reader reads by, and what an export to HTML keeps headings and
tables with:

```go
tree := doc.Structure()
if tree == nil {
	return // untagged: the text of the pages in the order of their layout
}
var walk func(e *cera.StructElem, depth int)
walk = func(e *cera.StructElem, depth int) {
	fmt.Printf("%s%s %q\n", strings.Repeat("  ", depth), e.Role, e.Alt)
	for _, k := range e.Kids {
		switch {
		case k.Elem != nil:
			walk(k.Elem, depth+1)
		case k.MCID >= 0:
			// the characters of page k.Page whose TextChar.MCID is k.MCID
		case k.Annot >= 0:
			// the annotation of page k.Page with that Index: the link of a Link
		}
	}
}
for _, e := range tree.Elems {
	walk(e, 0)
}
```

`Type` is the name the document gives an element and `Role` the standard
type its role map makes of it (`"Heading 1"` is an `"H1"`). `Kids` are in
reading order: elements, the marked content of a page by its identifier,
and annotations. `Title`, `Alt`, `ActualText`, `Expansion` and `Lang` are
those of the element, the language inherited from above; `Attr()` has the
attributes of tables and lists (`RowSpan`, `ColSpan`, `Headers`, `Scope`,
`Summary`, `ListNumbering`) and the `BBox` of a figure, and
`tree.ByID(id)` finds the header cell a cell names. `tree.Suspects` says
that the producer guessed the tree from a layout. Other attributes, and
content marked inside form XObjects, are not read.

## Metadata

```go
m := doc.Metadata()
fmt.Println(m.Title, m.Author, m.Created.Format(time.DateOnly))
if m.XMP != nil {
	// parse with encoding/xml if dc:title and the like are needed
}
```

`Metadata` never fails: a missing or damaged `/Info` leaves fields empty.
Dates are read leniently (a missing `D:`, missing fields, `Z00'00'`); a
date without a time zone is taken as UTC. PDF 2.0 deprecates `/Info`, but
for its dates, in favour of XMP, which is returned raw.

## Signatures

`Document.Signatures` lists the signatures in the order they were made,
and `Verify` checks one:

```go
for _, s := range doc.Signatures() {
	st, err := s.Verify(cera.VerifyOptions{Roots: roots}) // roots: *x509.CertPool, or nil
	switch {
	case err != nil:           // cera.ErrSignatureUnsupported: cannot be checked
	case !st.Intact:           // the signed bytes changed, or the signature is not the certificate's
	case !st.ChangesAllowed:   // something was appended that the signatures do not allow
	case st.Changes.Any():     // signed, then filled in, signed again or annotated: st.Changes
	}
	// s.Signer, s.Time (st.Timestamped: from a timestamp token), s.Reason,
	// s.Certification; st.Trusted and st.TrustError for the chain
}
```

`Intact` is the cryptography: the digest of the bytes the signature covers
and the signature of the certificate's key (signed data of
`adbe.pkcs7.detached`, `ETSI.CAdES.detached` and `adbe.pkcs7.sha1`, bare
`adbe.x509.rsa_sha1`, document timestamps `ETSI.RFC3161`; RSA and ECDSA).
`Changes` compares the document as signed with the document as it is.
Trust is against the roots the caller gives: cera has no trust list, and
does not check revocation.

`Editor.Sign` signs: it writes the document with an update that holds
the changes of the editor and a signature over all of it, so that the
signatures the document has stay valid.

```go
e := doc.Edit()
err := e.Sign(out, cera.SignOptions{
	Key:         key,  // a crypto.Signer: a key in memory, on a token, with a service
	Certificate: cert, // its certificate, and above it
	Chain:       []*x509.Certificate{intermediate},
	Reason:      "Approved",
	Location:    "Berlin",

	Field: "approval",                 // a signature field of the form, or a new one
	Page:  0,                          // where a new field shows, in points of the
	Rect:  cera.Rect{360, 700, 560, 760}, // displayed page; the zero Rect: invisible
	Appearance: func(c *cera.Canvas) { // what it shows, as Editor.Draw draws
		w, h := c.Size()
		c.Stroke(rect(0, 0, w, h), cera.StrokeStyle{Width: 1}, color.Black)
		c.DrawText("Signed by Ada Lovelace", 6, 20, font, 10, color.Black)
	},
})
```

The signature is a PAdES baseline signature (B-B, `ETSI.CAdES.detached`)
with SHA-256, by an RSA or an ECDSA key, which is asked once. It has no
timestamp and no validation data: its time is the signer's word.
`Certify: 2` makes the author's certification instead, which says what
may change afterwards — 1 nothing, 2 filling in forms and signing, 3 also
annotating — and `Lock` names the fields that may not change once it is
made. Form values, annotations and metadata of the same editor are
written with the signature; changes of the pages are not: update the
document first, open the result and sign that. Likewise, to go on after
a signature, open what `Sign` wrote. The file is held in memory while it
is signed.

## Attachments

`Document.Attachments` lists the files a document embeds — the XML of an
e-invoice, the data behind a report — with name, description, media type,
dates and `Relationship` (`/AFRelationship`), and `Open` reads one:

```go
for _, a := range doc.Attachments() {
	if a.Relationship == "Alternative" && a.MIME == "text/xml" { // Factur-X, ZUGFeRD
		r, err := a.Open()
		// ...
	}
}
```

The file of a FileAttachment annotation is `Annotation.File` on its page.
A document may encrypt its attachments alone: it opens without the
password, and `Open` returns `ErrPassword` until it is given.

`Editor.SetMetadata` writes one back, into `/Info` and into the XMP packet
so that both agree — the properties that mirror `/Info` are changed in
the packet the document has, the rest of it stays, and a document without
a packet gets one:

```go
m := doc.Metadata()
m.Title, m.Modified = "Annual report", time.Now()
err = e.SetMetadata(m)
```

`Save` and `Update` set no date or producer on their own: the same edit
gives the same file.

## Editing

An `Editor` (ADR 0013) records changes to a document, or puts a new one
together, and writes the result as a new file:

```go
e := doc.Edit()          // all pages of doc, and everything around them
e := cera.NewEditor()    // or an empty document

err = e.ImportPages(at, other, cera.EditPage{Index: 2, Rotate: 90})
err = e.DeletePages(3, 4)
err = e.MovePage(from, to)
err = e.RotatePage(i, 90)
err = e.SetFields(state) // values of doc.Form().NewState() the user changed
err = e.Flatten()        // draw the form into the pages and drop it
err = e.FlattenAnnotations() // and the other annotations
err = e.AddHighlight(i, cera.Highlight{Quads: text.Quads(sel), Note: "check"})
k, err := e.AddAnnotation(i, cera.NewAnnotation{Subtype: "Text", Contents: "A note",
	Markup: cera.Markup{Bounds: cera.Rect{X0: 40, Y0: 40, X1: 60, Y1: 60}, Author: "Ada"}})
err = e.Save(w)          // a new file
err = e.Update(w)        // the file as it was, and an incremental update
```

Everything at the level of the document (outline, names, structure tree,
form, layers, page labels, metadata) belongs to the edited document. Pages
imported from another document bring only what is on the page: content,
resources, annotations and links to other pages imported with them; their
widgets stay as plain annotations — unless the page is imported with
`EditPage{Index: i, Fields: true}`: then the fields reaching its widgets
come too, with their values, a root renamed `name_2` where the result has
the name (signature fields stay behind). Layers of imported pages are added, so
hidden layers stay hidden. Deleting a page removes its annotations and the
form fields whose widgets were all on deleted pages; outline entries and
links to it lose their target.

Pages are copied as they are: every object once per source document, byte
for byte, streams with their filters. An encrypted document may be
reassembled when its permissions allow it, else `Save` and `ImportPages`
return `ErrNoAssembly`. `Save` may be called again; documents an editor
uses may be rendered meanwhile.

`Save` keeps the protection of an encrypted document: the saved file opens
with the same passwords and grants the same permissions. `SaveWith`
changes that — it protects a file, or writes a protected one plain — and
for an encrypted document either needs the owner password
(`ErrNotPermitted` otherwise):

```go
err = e.SaveWith(w, cera.SaveOptions{Encrypt: &cera.Encryption{
	UserPassword:  "to open",
	OwnerPassword: "to change",
	Permissions:   cera.PermPrint | cera.PermPrintFaithful,
}})
err = e.SaveWith(w, cera.SaveOptions{Decrypt: true})
```

New protection is AES-256 (PDF 2.0, revision 6) with a random file key.
Pages imported from other documents are encrypted, or not, as the result
is.

`ImportPages` brings what is on the pages. `ImportPagesWith` brings what
their document says about them too, so that a merged file is no worse than
its parts:

```go
err = e.ImportPagesWith(e.NumPages(), other, cera.ImportOptions{
	Outline:      true,       // the bookmarks that lead to the imported pages
	OutlineTitle: "Appendix", // under one entry; "" puts them at the top level
	Destinations: true,       // named destinations, renamed name_2 where taken
	Labels:       true,       // the page numbers printed on them
}, pages...)
```

Bookmarks and names whose page is not in the result are left out. The
structure tree does not come: imported pages are untagged.

`AddPage` makes a blank page, and `Draw` draws on any page of the result —
over its content or under it — with a `Canvas` in points of the displayed
page, the origin at the top left, as everywhere in the editor: a watermark,
page numbers, a header, a cover sheet, pages of scanned images, several
pages on a sheet.

```go
font, err := cera.StandardFont("Helvetica") // or cera.NewCanvasFont(ttf)
logo := cera.NewCanvasImage(img)             // or cera.NewCanvasJPEG(data)

err = e.AddPage(0, 595, 842) // a cover sheet, A4
for i := range e.NumPages() {
	err = e.Draw(i, true, func(c *cera.Canvas) {
		w, h := c.Size()
		label := fmt.Sprintf("%d / %d", i+1, e.NumPages())
		c.DrawText(label, (w-font.Width(label, 9))/2, h-24, font, 9, color.Gray{90})
		c.DrawImage(logo, cera.Rect{X0: w - 96, Y0: 24, X1: w - 24, Y1: 48})

		c.Save()
		c.SetOpacity(0.15)
		c.Transform(cera.Matrix{0.7, -0.7, 0.7, 0.7, w / 2, h / 2}) // turned about the middle
		c.DrawText("DRAFT", -120, 30, font, 80, color.RGBA{200, 0, 0, 255})
		c.Restore()
	})
}
```

A canvas fills, strokes and clips paths (`cera.Path`), with an opacity and
a blend mode; draws images, a JPEG as it is and anything else compressed
without loss, with its alpha; draws text in the standard 14 fonts or in a
TrueType or OpenType font, of which the file gets the glyphs drawn; and
draws another page (`DrawPage`), for sheets of several pages and for a
stamp that is a PDF. Text is one line, character by character: there is no
shaping and no line breaking. A font or an image made once is one object
in the file however many pages show it.

The content of a page drawn on is kept as it is: a drawing is a form of
its own before or after it. It belongs to its page — it moves and turns
with it. In an encrypted document, drawing needs the permission to modify;
a certification forbids it.

`AddField` makes a form, or adds to one: a text field, a check box, the
buttons of a radio group, a combo or list box, a push button, or a
signature field for `Sign` to sign later. `RemoveField` takes one out,
with its widgets.

```go
boxed := cera.Appearance{Border: &black, Background: &white}
err = e.AddField(0, cera.Rect{60, 100, 260, 120}, cera.NewField{
	Name: "address.street", // below a node "address", made if there is none
	Type: cera.FieldText, Value: cera.TextValue("Main St 1"), Look: boxed,
})
// A radio group: a widget each, under one name.
err = e.AddField(0, cera.Rect{60, 130, 74, 144}, cera.NewField{Name: "plan", Type: cera.FieldRadio, OnState: "basic", Value: cera.StateValue("basic"), Look: boxed})
err = e.AddField(0, cera.Rect{90, 130, 104, 144}, cera.NewField{Name: "plan", OnState: "pro", Look: boxed})
err = e.AddField(0, cera.Rect{60, 160, 260, 220}, cera.NewField{Name: "approval", Type: cera.FieldSignature})
err = e.RemoveField("obsolete")
```

The rectangle is in points of the displayed page; a name the form has
adds a widget to that field. A new field is written with a default
appearance in Helvetica — the form's `/Helv`, added to its resources if
it has none — and an appearance stream for its value, as cera draws
fields that have none. Pages are those of the edited document; a
certified document takes no fields and gives none up (`ErrCertified`), a
field a signature locks stays (`ErrLocked`), and `Sign` wants the fields
written first.

`SetFields` records the values of a `FormState` of the edited document
that differ from what the file holds: a text as a text string (its rich
text value removed), a button state as a name with every widget's `/AS`,
a selection as export values with `/I` for list boxes. Every widget of a
changed text or choice field gets an appearance stream of its new value,
made as cera draws it (the form's `/DA` font, or Helvetica when that cannot
show the text), so every viewer shows the values without regenerating
them; check boxes and radio buttons switch between their appearances.

What Helvetica lacks is drawn with a fallback font — Arimo for Greek and
Cyrillic, and for Chinese, Japanese and Korean the fonts of the document's
`FontProvider` (`fonts/cjk`) — on screen and in the file, which gets a
subset of the font with the glyphs used and a `/ToUnicode` map. A character
no font has is drawn as `?`: `e.MissingGlyphs()` returns the fields with
such a value, and the written form gets `/NeedAppearances`, which asks a
viewer that has a font to draw the fields itself.

`Flatten` draws every widget the document shows into the content of its
page, as cera renders it (with values from `SetFields`), and removes the
form: the widgets leave the pages, `/AcroForm` the catalogue. Hidden
widgets are dropped, widgets in a layer stay in it. Page content lies
under the annotations a page keeps, so an annotation that lay under a
widget and overlaps it is flattened with the widget, and so on down the
stack: the page shows what it showed. Annotations that touch no flattened
one stay as they are.

`FlattenAnnotations` does the same for the other annotations, for print
or the archive: every annotation shown on screen is drawn into its page —
with its appearance stream or the one cera generates, its opacity, and in
its layer — and leaves the page with its popup. Links stay, as do
annotations that show nothing, and the widgets of the form unless
`Flatten` is called too; an annotation that lies on a widget then stays
with it. Annotations added by the same editor are not flattened.

Flattening changes the content of pages: `Update` refuses it with
`ErrNotPermitted` for an encrypted document without the permission to
modify, and `FlattenAnnotations` with `ErrCertified` for a certified one.

`AddHighlight` marks text on a page of the edited document: a
`/Highlight` (or `Underline`, `StrikeOut`, `Squiggly`) annotation from
quads in points of the displayed page, as `PageText.Quads` returns them
for a selection or a search match. It is written with `/QuadPoints` in
user space and an appearance stream (a highlight multiplies with the page
below it), so every viewer shows it alike; a `Note` goes into `/Contents`
with a closed popup. The highlight stays with its page when the page
moves, and goes when it is deleted.

`AddAnnotation` does the same for the other annotations a user comments
with: notes (`Text`), `FreeText`, `Ink`, `Square`, `Circle`, `Line`,
`Polygon`, `PolyLine`, `Caret`, the standard stamps and links. A
`NewAnnotation` is a type, a text and a `Markup`, the struct
`Annotation.Markup` reads: geometry in points of the displayed page,
colours, border, author and dates, the popup, what it replies to. The
appearance stream is what cera draws for the annotation without one, so
every viewer shows it alike; on a turned page text, stamps and icons stay
upright. It returns the index of the annotation, for a reply to it or to
change it again.

`SetAnnotation` changes an annotation of the document and
`RemoveAnnotation` removes one with its popup and its replies:

```go
a, _ := page.EditAnnotation(index) // as it is
a.Contents, a.Color = "Done", []float64{0, 0.6, 0}
err = e.SetAnnotation(i, index, a)
err = e.RemoveAnnotation(i, other)
```

What `SetAnnotation` does not describe stays in the annotation; its
appearance is made anew. Changes to annotations need the permission to
annotate or to modify, and a certification with DocMDP permissions 1 or 2
refuses them (`ErrCertified`); a signature's lock covers fields, not
annotations. A flatten in the same editor draws the document's annotations
as they were and leaves added ones alone: write the changes first.

Signatures are not checked, but what they declare is kept: a signed
document is not saved as a new file (`ErrSigned`, use `Update`), a field a
signature locks (`/Lock`) does not change (`ErrLocked`), and a
certification's DocMDP permissions allow no change with P 1, no change
of the pages with P 2 and 3 and no added annotations with P 2
(`ErrCertified`). Writing values removes an
XFA form, which would show the old ones. Calculated fields are not
computed again: cera runs no JavaScript.

`Update` writes the edited file unchanged and appends only what changed
(PDF 2.0, 7.5.6), so signatures over the original bytes stay valid: the
document's pages keep their objects, deleted pages and what hung on them
alone are freed, imported pages are copied in. An unchanged document is
written as it is. It returns `ErrNoUpdate` for an editor made with
`NewEditor` and for a file that needed a repair. An encrypted file stays
encrypted: what the update appends is encrypted with its key (RC4, AES-128
or AES-256), and `ErrNotPermitted` refuses what its permissions do not
allow (page changes need the right to modify or assemble, form values the
right to fill in forms, annotate or modify, highlights the right to
annotate or modify).

## Command line

```sh
go build -C cmd/cera -o "$PWD/cera" .
./cera -dpi 150 -v -o 'page-%d.png' input.pdf
./cera -cmyk-profile USWebCoatedSWOP.icc -page 1 print.pdf   # or -naive-cmyk, -ignore-output-intent
```

`cmd/cera` is a module of its own, so that cera itself depends on nothing
it does not draw with: it writes the PNGs with
[calamus](https://github.com/timzifer/calamus), which encodes them in bands
on all cores (encoding was most of the command's time with `image/png`).
