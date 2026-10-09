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

A file need not be in memory: `cera.OpenReaderAt(f, size, opts)` reads
through an `io.ReaderAt` (an `*os.File`, a browser `File` in `js/wasm`)
only what the pages drawn need.

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

layer := cera.NewFormLayer(page, state, provider)
layer.Update(cera.View{Scale: scale, Viewport: visible})
err = page.Render(ctx, dst, cera.RenderOptions{Scale: scale, Form: state, SkipAnnotation: layer.Skip})
```

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
for byte, streams with their filters. The result is never encrypted: an
encrypted document is written decrypted when its permissions allow
reassembling it, else `Save` and `ImportPages` return `ErrNoAssembly`.
`Save` may be called again; documents an editor uses may be rendered
meanwhile.

`SetFields` records the values of a `FormState` of the edited document
that differ from what the file holds: a text as a text string (its rich
text value removed), a button state as a name with every widget's `/AS`,
a selection as export values with `/I` for list boxes. Every widget of a
changed text or choice field gets an appearance stream of its new value,
made as cera draws it (the form's `/DA` font, or Helvetica when that cannot
show the text), so every viewer shows the values without regenerating
them; check boxes and radio buttons switch between their appearances.

`Flatten` draws every widget the document shows into the content of its
page, as cera renders it (with values from `SetFields`), and removes the
form: the widgets leave the pages, `/AcroForm` the catalogue. Hidden
widgets are dropped, widgets in a layer stay in it. Flattened widgets lie
under the annotations the page keeps.

Signatures are not checked, but what they declare is kept: a signed
document is not saved as a new file (`ErrSigned`, use `Update`), a field a
signature locks (`/Lock`) does not change (`ErrLocked`), and a
certification's DocMDP permissions allow no change with P 1 and no change
of the pages with P 2 and 3 (`ErrCertified`). Writing values removes an
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
right to fill in forms, annotate or modify).

## Command line

```sh
go build -C cmd/cera -o "$PWD/cera" .
./cera -dpi 150 -v -o 'page-%d.png' input.pdf
./cera -cmyk-profile USWebCoatedSWOP.icc -page 1 print.pdf   # or -naive-cmyk
```

`cmd/cera` is a module of its own, so that cera itself depends on nothing
it does not draw with: it writes the PNGs with
[calamus](https://github.com/timzifer/calamus), which encodes them in bands
on all cores (encoding was most of the command's time with `image/png`).
