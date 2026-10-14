# Features

What cera draws and does today, and what it counts as unsupported.

## Supported

### Text

- All text operators and render modes, including the clipping modes 4–7
- Embedded fonts: TrueType, CFF (bare, OpenType, CID-keyed), Type 1, Type 3
  (coloured `d0` and uncoloured `d1` glyphs)
- Composite fonts: predefined CMaps (PDF 2.0 Table 116) and embedded ones,
  codes of 1–4 bytes, `CIDToGIDMap`
- Vertical writing: `-V` CMaps, `/WMode`, `/W2` and `/DW2`, vertical glyph
  forms through the font's `vert` feature
- Text knockout (`TK`) for overlapping glyphs below opacity 1

Embedded programs PDF writers got wrong are read repaired, as PDFium and
pdf.js read them (`fontrepair.go`): TrueType tables that disagree (glyph
count, `loca`, `hmtx`/`vmtx`, index format, `unitsPerEm`), tables a few
bytes from where the directory says, directory entries of garbage, a
TrueType collection as `FontFile2`, CFF DICTs with an empty real or bytes
that start no operand, a CFF program as `FontFile`, a PFB cut off before
its trailer. A program whose glyphs themselves are damaged is left to a
stand-in (`font-bad`).

Fonts a file does not embed:

| font asked for | drawn with |
|---|---|
| Helvetica, Times, Courier and frequent families (Calibri, Verdana, Cambria …) | TeX Gyre Heros, Termes, Cursor (the URW Nimbus shapes PDFium and MuPDF draw too, metric-compatible), in the weight, slope and width asked for; Arimo, Tinos, Cousine for text TeX Gyre has no glyphs for (Cyrillic), and for all of it when built with the tag `cera_nogyre` (1.45 MB smaller, see [usage](usage.md)) |
| sans serif monospaces (Consolas, Menlo, Lucida Console …) | Cousine |
| Symbol, ZapfDingbats | DejaVu Sans subsets at AFM widths |
| Adobe CJK collections | Noto Sans through `fonts/cjk` (opt-in, see [usage](usage.md)), at the weight the document asks for; reads back as Unicode without ToUnicode |
| anything else | the fonts of the machine through `fonts/system` (opt-in), or a `FontProvider` of your own (`cera.OpenWith`); `cera.FontProviders` asks several in turn. A variable font a provider supplies is drawn at the weight the document asks for (400, 700 for bold, or `/FontWeight`) |

### Images

- Image XObjects and inline images at 1, 2, 4, 8 and 16 bits, in every
  supported colour space, with `/Decode`
- Masks, each at its own resolution: stencil (`/ImageMask`), soft (`/SMask`),
  `/Mask` stream, colour key (`/Mask` array)
- Codecs: JPEG (incl. Adobe CMYK), JPEG 2000 (incl. alpha), JBIG2 (with
  `JBIG2Globals`), CCITT fax, every stream filter of the reader

### Paths and clipping

- All construction and painting operators, nonzero and even-odd
- Strokes: width, caps, joins, miter limit, dashes; exact under anisotropic
  transforms
- Clipping: rectangles free, other shapes as masks
- Form XObjects with `Matrix` and `BBox`; `/Rotate`, `/UserUnit`, CropBox

### Colour

| space | notes |
|---|---|
| DeviceGray, DeviceRGB | |
| DeviceCMYK | through the CMYK profile of the document's output intent (PDF/X, PDF/A, PDF/E; a page's own intent, PDF 2.0, on that page; `OpenOptions.IgnoreOutputIntent` to skip them), else a SWOP press profile; or the caller's (`OpenOptions.CMYKProfile`); `OpenOptions.NaiveCMYK` for raw device values |
| CalGray, CalRGB, Lab | through CIE XYZ, D50 (Bradford) |
| ICCBased | grey and RGB profiles of curves and a matrix, or where they have none of lut8, lut16 or lutAtoB tables (scanner and camera profiles); CMYK profiles of such tables; Lab or XYZ; others by channel count |
| Separation, DeviceN | tint transforms; `/None` paints nothing |
| Indexed | on any of the above |
| DefaultGray, DefaultRGB, DefaultCMYK | remap device colours, images and shadings of their resources; not the device spaces inside other spaces |

### Transparency and graphics state

- Groups: isolated, non-isolated, knockout
- All 16 blend modes
- Soft masks: luminosity (with backdrop colour) and alpha, transfer
  functions of all four types
- Constant alpha (`CA`, `ca`), `/Font` in `gs`, transfer functions
  (`TR`, `TR2`) on solid colours and images
- Overprint (`OP`, `op`, `OPM`) simulated as Multiply with
  `RenderOptions.SimulateOverprint` (off by default)
- Ignored on purpose: `BG`, `UCR`, `HT`, `FL`, `SM`, `SA`, `ri`

### Shadings and patterns

- All seven shading types: function-based, axial, radial, free-form and
  lattice triangle meshes, Coons and tensor-product patches
- `Function`, `Domain`, `Extend`, `BBox`, `Background`
- Painted by `sh` and as shading patterns of fills, strokes and text
- Tiling patterns, coloured and uncoloured

### Optional content (layers)

- Groups and membership dictionaries (`/P`, visibility expressions)
- `BDC /OC` and XObjects with `/OC`
- Default and alternate configurations, `/Order`, radio-button groups,
  locked layers, intents
- Automatic states for view, print and export (`/AS`, zoom ranges)

### Annotations

- Drawn from normal appearance streams (`/AS`, `NoZoom`, `NoRotate`, `/OC`)
- Appearance generated when missing: Square, Circle, Line (endings, leader
  lines), PolyLine, Polygon, Ink, Highlight, Underline, StrikeOut, Squiggly;
  FreeText (box, border, callout, wrapped text from `/DA`, `/DS`, `/Q`),
  the icons of Text and FileAttachment, Caret, the standard stamps, and
  with `RenderOptions.LinkBorders` the border of a link that has `/Border`
  or `/BS`; with `/CA`, which an appearance stream overrides
- Link targets: destinations (explicit, named — looked up by the bytes of
  the name), GoTo, GoToR, GoToE, Launch, URI (with the document's base and
  `/IsMap`) and Named actions, the first of chained actions that leads
  somewhere, and the type of any other action
- `Page.Annotations` with link targets and, for markup annotations,
  `Markup`: author, subject, dates, rich text, colours, opacity, border,
  geometry in displayed points (quads, ink strokes, vertices, line and its
  endings, callout), popup, reply thread and review state, icon
- Written by the editor (`AddAnnotation`, `SetAnnotation`,
  `RemoveAnnotation`): notes, free text, ink, shapes, lines, text markup,
  carets, standard stamps and links, with generated appearance streams,
  popups and reply threads

### Forms

- AcroForm fields with inheritance; values in a caller-owned `FormState`
- Appearances generated for text fields (single line, multiline, comb,
  password, auto-size), check boxes, radio buttons, combo and list boxes,
  push buttons; text beyond the field's font and Helvetica in fallback
  fonts (Greek, Cyrillic; CJK with `fonts/cjk`), embedded as subsets when
  the form is saved
- Native widgets through `FormWidgetProvider` and `FormLayer` (tab order,
  focus); fyne provider in [`form/fyneform`](../form/fyneform)
- `Widget.Actions`: what a push button does — `ResetForm` and `Hide`
  carried out by the `FormState` (`Do`, `ResetFields`, `SetHidden`),
  `GoTo`, `URI` and `Named` as link targets, `SubmitForm` with its
  address, format and the data to send (`Submission`; cera sends
  nothing), JavaScript reported
- `FormState.ExportFDF`, `ExportXFDF`, `ImportFDF`, `ImportXFDF`: the
  values of a form in its exchange formats, with hierarchical names, the
  states of buttons and several options of a list box
- Format, validation and calculation without JavaScript where a field's
  scripts are calls of Acrobat's form functions (`AFNumber_Format`,
  `AFPercent_Format`, `AFDate_FormatEx`, `AFTime_Format`,
  `AFSpecial_Format`, `AFRange_Validate`, `AFSimple_Calculate`):
  `Field.Computed`, `Field.Format`, recalculation in `FormState.SetValue`
- Not run: JavaScript. XFA: drawn from the AcroForm fallback, counted as `xfa`

### Text extraction and interaction

What a viewer needs of the text of a page (ADR 0014), in points of the
displayed page:

- `Page.Text`: the characters with what they stand for and their boxes,
  in reading order, in lines and blocks; a ligature as its characters,
  `/ActualText` as the text of its glyphs, words broken across lines
  marked
- Text extraction puts right-to-left text (Arabic, Hebrew) into logical
  order and gives the letters for presentation forms; lines, selection
  and columns follow the direction
- Selection by characters, words, lines and blocks (`Select`, `WordAt`,
  `LineAt`, `InRect`), the quads to draw it (`Quads`), its text with
  hyphenated words joined on request (`TextOf`)
- Search (`Find`): case, whole words, diacritics; a passage with the text
  around it as an anchor that finds it again after the text was laid out
  anew (`Quote`, `QuoteOf`, `FindQuote`, a W3C TextQuoteSelector)
- `TextIndex`: the text of the pages of a document, kept for the pages
  used last; search across pages in both directions, the text between
  two places on different pages
- `Page.Links` and the addresses written out in the text
  (`PageText.URLs`); `Document.Destination` resolves a target to a page
  and a view; `TextIndex.Hit` says what lies under a point: a link, a
  form widget, text
- `Document.Structure`: the structure tree of a tagged document — types
  and their roles (`/RoleMap`, namespaces), titles, `/Alt`, `/ActualText`,
  `/Lang`, marked content and annotations in reading order, the
  attributes of tables and lists
  (`/OpenAction`, `/PageMode`, `/PageLayout`, `/ViewerPreferences`)

### The document

- `Open`, `OpenWith`, and `OpenReaderAt` for a file read through an
  `io.ReaderAt` as it is needed: a large file, or one in a browser read
  through the File API
- Files with object streams and cross-reference streams, linearised
  files, files with incremental updates; damaged cross-reference tables
  are rebuilt
- What a document keeps decoded is bounded and can be shared and trimmed
  (`NewCache`, `Document.Trim`, `SetPoolLimits`; see [budgets](budgets.md))
- `Document.Outline`: the bookmarks as a tree, with titles, style, open
  state and targets (destinations, GoTo, URI, GoToR and Launch actions,
  structure elements)
- `Document.PageLabel` and `PageByLabel`: page labels (roman, decimal,
  letters, prefixes), read leniently; the editor keeps them with their
  pages
- `Document.ViewSettings`: how a document asks to be opened and printed
- `Document.Attachments` and `Annotation.File`: embedded files with
  their metadata and `/AFRelationship`, decoded and decrypted (also where
  the files alone are encrypted, `/EFF`)
- `Page.Rotated`: the page as a viewer shows it turned, with its own
  display list; text, links, annotations and editing follow the view
- `Page.Boxes` and `Page.InBox`: MediaBox, CropBox, BleedBox, TrimBox and
  ArtBox with their defaults, and the page showing another of them than
  the CropBox, a view as `Rotated` makes one
- `Page.RenderGray` draws into an `*image.Gray`, band by band, without
  the page in colour: for monochrome printers, fax, text recognition

### Metadata

- `Document.Metadata`: the document information dictionary decoded (title,
  author and the other standard entries, dates as `time.Time`, other string
  entries in `Custom`) and the XMP stream, raw
- Text strings in UTF-16BE, UTF-8 and PDFDocEncoding (all of it, accents at
  0x18–0x1F included), language escapes dropped; lenient dates
- With `/EncryptMetadata false` the XMP stream is read as stored

### Encryption

- Read: the standard security handler with RC4 (40 to 128 bits), AES-128
  and AES-256, opened with the user or the owner password
  (`OpenOptions.Password`); crypt filters, metadata left plain
  (`/EncryptMetadata false`), documents whose embedded files alone are
  encrypted
- `Document.Encrypted` and `Permissions`: what the author allows a reader
  with the user password. Drawing and text extraction do not ask; the
  editor refuses what they forbid (`ErrNotPermitted`, `ErrNoAssembly`)
- Written: `Editor.Save` keeps the encryption of a document,
  `Editor.SaveWith` encrypts anew (AES-256, user and owner password,
  permissions) or decrypts, and `Editor.Update` appends in the encryption
  the file has

### Signatures

- `Document.Signatures` and `Signature.Verify`: integrity (CMS signed
  data, RSA and ECDSA, timestamp tokens), what was appended after a
  signature and whether certification and field locks allow it, and the
  certificate chain against roots the caller gives
- `Editor.Sign`: approval and certification signatures (PAdES B-B,
  `ETSI.CAdES.detached`) by an RSA or ECDSA key behind `crypto.Signer`,
  in a field of the form or a new one, invisible or with an appearance
  drawn on a canvas, with field locks; with a timestamp authority's
  token (`SignOptions.Timestamp`, B-T)
- `Editor.Timestamp`: document timestamps (`ETSI.RFC3161`);
  `Editor.AddValidation`: certificates, OCSP responses and CRLs the
  caller fetched, in the document security store (`/DSS`, `/VRI`; B-LT,
  B-LTA with a document timestamp after it). cera does no network I/O:
  the caller reaches the authority and the responders

### Editing

An `Editor` (ADR 0013) puts a result together of the pages of documents
and writes it: as a new file (`Save`), or as an incremental update that
leaves the file as it is and appends what changed, so that signatures
over it stay valid (`Update`).

- Pages: imported from any document (`ImportPages`, with their fields on
  request), deleted, moved, turned
- `Editor.ImportPagesWith`: imported pages bring their outline entries,
  named destinations, page labels and structure elements, so that a
  tagged document stays tagged
- `Editor.AddPage` and `Editor.Draw`: blank pages, and drawings over or
  under the content of any page — paths, images (JPEG as it is, others
  Flate with a soft mask), text in the standard 14 fonts and in embedded
  subsets of TrueType and OpenType fonts, other pages as forms; opacity
  and blend modes
- `Editor.SetPageBox`, `ResizePage` (a page on another format: content as
  a form through a matrix, annotations and boxes with it) and `AddSheets`
  (n-up, imposition)
- Form values written with their appearances (`SetFields`), the form or
  all annotations made part of the pages (`Flatten`,
  `FlattenAnnotations`), highlights (`AddHighlight`) and the other
  annotations (above)
- `Editor.AddField` and `RemoveField`: fields of every type added to
  pages with generated appearances, widgets added to fields, names that
  make the nodes above them; fields removed with their widgets
- `Editor.SetOutline`, `Outline` and `PruneOutline` (bookmarks added,
  renamed, reordered, built for a merged file; targets that stay with
  their pages; entries of deleted pages dropped) and `SetPageLabels`;
  `Document.PageLabels`
- `Editor.SetMetadata` writes `/Info` and the XMP packet in agreement,
  keeping what else the packet holds
- `Editor.Redact` and `ApplyRedactions`: content taken out of pages for
  good — glyphs out of their strings, paths left out or cut out of the
  areas, images re-encoded without the samples, forms rewritten per use,
  annotations, fields and the text that stood for the content in marked
  content and the structure tree — failing closed, written as a new file;
  the overlay text of redaction annotations written over their areas
- `Save` writes object streams and a cross-reference stream where the
  document had them, or on request (`SaveOptions.ObjectStreams`); also
  encrypted
- `SaveWith` writes linearised files (`SaveOptions.Linearize`, "fast web
  view"): first-page section, page offset, shared object and outline
  hint tables, with a cross-reference table or with object streams, also
  encrypted; laid out by the rules qpdf checks linearised files by
- `SaveWith` makes a file smaller on request, losing nothing of what it
  shows: resources no content names left out (`PruneResources`), streams
  and fonts that are the same written once, across merged documents too
  (`JoinDuplicates`), plain, LZW, run length and ASCII streams under Flate
  (`Compress`), thumbnails, `/PieceInfo` and the metadata of objects
  dropped (`Drop`). Images are not resampled and fonts not subset

Not done, each with its issue: what a redaction leaves (#198),
destinations on resized pages (#205), images and fonts when a file is
made smaller (#201, #202).

### Command line

- `cmd/cera -field name=value` fills a form for drawing; `cera fill`
  writes it
- `cmd/cera`: `text`, `search`, `info`, `merge`, `split`, `rotate`,
  `delete`, `fill`; pages as PNG, JPEG, TIFF and WebP, in colour or grey

## Unsupported

Counted per page in `Stats.Unsupported`, so the corpus reports show what
matters most. A page is never failed for these; it is drawn as the last
column says.

### Fonts

| key | case | drawn |
|---|---|---|
| `font-missing` | font neither embedded nor standing in | – |
| `font-missing-japan1`, `-gb1`, `-cns1`, `-korea1`, `-cid` | non-embedded composite font no `FontProvider` supplies | – |
| `font-bad` | embedded font program that does not read | with a stand-in |
| `cmap-missing` | CMap neither predefined nor readable | as Identity |
| `type3-clip-approx` | strokes, images, unbounded shadings of Type 3 glyphs in clipping modes | clipped to their boxes |
| `text-knockout` | overlapping stroked or pattern-filled glyphs under `TK` | without knockout |

### Images

| key | case | drawn |
|---|---|---|
| `image-filter` | filter the reader does not know | not drawn |
| `smask-matte` | `/Matte` over images not in grey or RGB | matte undone in RGB |

### Colour

| key | case | drawn |
|---|---|---|
| `tint-transform` | tint transform that does not read | as grey |
| `shading-function` | shading function that does not read | – |
| `icc-lut` | CMYK ICC profile cera cannot read, of an ICCBased space or of the output intent | through DeviceCMYK's profile, the bundled one for the intent |
| `icc-profile` | grey or RGB ICC profile cera cannot read (neither curves and matrix nor a lut8, lut16 or lutAtoB table), a Lab profile, an ICCBased space of another number of components | as device space of as many components, or the alternate the space names |

### Transparency and graphics state

| key | case | drawn |
|---|---|---|
| `blend-mode` | unknown blend mode | as Normal |
| `non-isolated-blend` | non-isolated group with blend modes inside, object of a knockout group | isolated |
| `alpha-is-shape` | `/AIS true` inside a knockout group (elsewhere shape and opacity read the same) | alpha as opacity |
| `smask-transfer` | soft-mask transfer function that does not read | – |
| `overprint` | overprint not simulated | painted over |
| `transfer` | transfer functions on shadings and patterns, or that do not read (fills, strokes, text and images go through them) | without |

### Content and annotations

| key | case | drawn |
|---|---|---|
| `pattern` | pattern colour on an operation that cannot paint patterns | not drawn |
| `oc-bad` | optional-content membership that does not read | visible |
| `annot-no-ap` | annotation without appearance cera does not generate (a stamp of no standard name, Redact, Watermark, an unknown type) | not drawn |
| `xfa` | XFA form | from AcroForm fallback |

### Budgets

Inputs past a [budget](budgets.md) are drawn with less and counted:
`nesting-budget`, `pattern-budget`, `smask-budget`, `mesh-budget`,
`annot-budget`, `image-too-large`, `icc-budget`.

### Beyond drawing

Not counted, because no page is drawn differently for it:

| area | not done | instead |
|---|---|---|
| scripts | JavaScript is never run: of form fields (but for Acrobat's form functions, see Forms), of links, bookmarks and buttons | the action is reported by its type (`LinkTarget.Action`, `WidgetAction`) |
| forms | XFA | the AcroForm the file has beside it (`xfa`) |
| text | text whose font says neither what its codes stand for nor has a known encoding | the characters with their boxes and codes, `Text` empty |
| signatures | revocation when verifying (OCSP, CRLs, also those of `/DSS`); EdDSA and RSASSA-PSS keys when signing | integrity, what was appended, and the chain against the caller's roots |
| encryption | public-key security handlers, AES-GCM (#161) | the file does not open |
| output | pages in the CMYK of the document (#209), in another RGB than sRGB (#210); overprint per colorant (#155) | sRGB or grey; overprint simulated on request |
| tagged PDF | structure for what the editor adds (drawings, new pages, fields, annotations): it comes untagged | the structure tree of a document is read and kept; imported pages bring theirs on request |
