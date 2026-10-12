# 0014. Text interaction: selection, search, links and highlights

- Status: accepted
- Date: 2026-10-12
- Milestone: M9

## Context

Viewers built on cera need more from the text of a page than a string.
fyne-pdf lists "text selection, search, links, annotations" as not
supported, and fons (the document dock of its web surface) marks passages
in page coordinates and stores them anchored by work, text layer, byte
range and a hash of the quoted bytes, so that a better OCR run does not
move a mark to another sentence. Until #111, cera offered:

- `Page.Text` with `TextChar`s in content order, an axis-aligned `Box` per
  glyph and a `String` built by a simple line and space heuristic;
- `Annotation` and `LinkTarget` with a rectangle in user space, no
  QuadPoints;
- a private page matrix, and marked content read only for optional
  content: `/ActualText`, `/MCID` and artifacts were ignored.

A viewer needs a toolkit-neutral data API it can use without knowing PDF:
hit testing, selection by character, word, line and block, copying,
search with context, links with their targets, a hover cursor, and
writing a highlight back into the file.

## Decision

The text interaction API lives in the root package as plain data on
`*PageText` and `*Document`; cera draws nothing for it.

**One coordinate space.** Every position, quad and rectangle is in points
of the *displayed* page: y down, origin at the top-left corner, after
`/Rotate` and `/UserUnit`. `Page.DisplayMatrix` maps default user space
there, its inverse back. A viewer only multiplies by its zoom.

**Glyphs as quads.** `Quad` (`quad.go`) is four corners in PDF QuadPoints
order with `Bounds`, `Contains` and `Transform`. A `TextChar` carries the
exact em box as `Quad` (rotated and skewed text included), `Code`, `Dir`,
`Advance`, `MCID` and `Artifact`; `Box` stays as the bounds of the quad.

**Marked content.** The interpreter keeps a stack of marked-content
sequences and tells devices that implement `MarkedContentDevice`
(`BeginMarkedContent`, `EndMarkedContent`) about them, with tag, MCID,
ActualText, Alt and Lang resolved from the operand or `/Properties`.
Sequences still open at the end of a content stream are closed there.
An XObject's `/OC` is not a marked-content sequence and is not reported.

**Layout and reading order** (`textlayout.go`, `structtree.go`).
`Page.TextWith(ctx, TextOptions)` returns an immutable `PageText` safe for
concurrent reads:

- Fake-bold duplicates are dropped; invisible text (OCR layers) is kept.
- Ligatures (multi-rune text, and U+FB00 to U+FB06) are split into one
  `TextChar` per character with a share of the quad.
- The glyphs of an `/ActualText` sequence form one group: the first
  carries the text, the others none, and selection takes the group whole.
- Characters form `TextLine`s (same direction, baseline moving less than
  half an em, gaps up to 1.5 em, no step back over 1 em); lines on one
  baseline closer than 1 em and not overlapping are merged (text shown
  out of order); lines form `TextBlock`s (same direction and similar
  size, at most 1.7 em apart, overlapping along the text). The
  thresholds are tuned on arXiv papers, whose column gutters are about
  2.4 em.
- Reading order: in a tagged PDF the structure tree (`/StructTreeRoot`,
  `/K`, depth first) orders the blocks it covers by their first MCID, the
  rest follows by geometry; otherwise a recursive XY cut at the widest gap
  orders the blocks, which finds columns.
- `String` is the plain text: lines separated by `\n`, blocks by `\n\n`,
  a space where a gap exceeds 0.15 em without a space glyph.
  `TextLine.Hyphenated` marks a word broken across lines.

**Selection and offsets** (`textselect.go`). `TextPos` is a boundary
before `Chars[i]`, `TextRange` a half-open span. `CharAt`, `PosAt`,
`Select(a, b, Granularity)`, `WordAt`, `LineAt`, `All`, `Quads` (one quad
per line, for drawing a highlight), `TextOf(r, TextFormat)` (optionally
joining hyphenated words) and `InRect` (rectangle selection). `Offset`,
`PosAtOffset` and `RangeOfOffsets` map between positions and byte offsets
in `String()`, so a range can be stored as offsets into a text layer;
`TextRange` itself is not a storage format.

**Search and the document index** (step 4). `TextIndex` caches the
`PageText` of a document's pages (LRU, safe for concurrent use) and spans
pages with `DocPos{Page, Pos}`. `PageText.Find` and `TextIndex.Find`
search the normalised plain text (runs of white space as one space, soft
hyphens and hyphenated line breaks removed, case folded) with
`SearchOptions{MatchCase, WholeWord, IgnoreDiacritics}`; the document
search is an `iter.Seq2[DocMatch, error]` that starts at a position,
wraps, can run backwards and stops when the context ends. Every match
carries its context as a `Quote{Prefix, Exact, Suffix}`. The same quote
form finds a passage again (`QuoteOf`, `FindQuote`), also when the quote
was taken from a foreign text layer such as GROBID's, where byte offsets
do not apply: of the places of `Exact`, the one whose surroundings agree
most with prefix and suffix wins.

**Links and hit testing** (step 5, `links.go`). `Page.Links` returns the
visible link annotations with their `/QuadPoints` as quads in displayed
points, or the rectangle when there are none or they reach outside it;
`PageText.URLs` finds `http(s)://`, `www.` and `mailto:` in the text.
`Document.Destination` resolves a target to a page and a view in
displayed points of that page: the point for the top-left corner, the
zoom, or the rectangle to fit, with `FitH` and `FitV` swapped on a page
turned by 90°. `/GoToR` and `/Launch` are read as `LinkTarget.External`
with the `File` they name and never followed. `TextIndex.Hit` at a point
answers link annotation, then address in the text, then widget, then
line of text, then nothing, for the hover cursor; widgets are only
reported, editing them stays with the form layer (ADR 0006).

**Highlights** (step 6, `annotwrite.go`). `Editor.AddHighlight` (ADR
0013) adds a `/Highlight`, `/Underline`, `/StrikeOut` or `/Squiggly`
annotation to a page of the edited document from quads in displayed
points (`PageText.Quads`), written as `/QuadPoints` in user space with an
appearance stream drawn as the renderer draws markup without one (a
highlight with blend mode Multiply), plus an optional note (`/Contents`,
closed popup). The highlight belongs to its page in the editor and moves
and goes with it. Saved by `Save`, or by `Update`, which keeps signatures
valid; it needs the permission to annotate or modify, and a certification
with DocMDP permissions 1 or 2 refuses it.

### Steps

| step | content | PR |
|---|---|---|
| 1 | Quads, marked content, `Page.DisplayMatrix` | #111 |
| 2 | Lines, blocks, reading order, structure tree | #112 |
| 3 | Selection, quads, offsets | #113 |
| 4 | `TextIndex`, `Find` with context, quote anchors | #116 |
| 5 | `Links`, `Destination`, `Hit` | #117 |
| 6 | `Editor.AddHighlight` | #118 |

Each step is one PR against `main`.

## Consequences

- A viewer builds selection, copy, search, links and hover from data
  alone; fyne-pdf and fons share one implementation.
- `String` changed from content order to reading order (v0.x).
- Reading order of untagged pages stays a heuristic: tables, sidebars and
  text wrapped around figures may read in a surprising order. Tagged PDFs
  are only as good as their structure tree.
- Text extraction runs the content stream like rendering does. Layout
  must stay well below render time per page; the cache in `TextIndex` is
  the caller's lever for repeated access.
- `IgnoreDiacritics` uses Unicode decomposition
  (`golang.org/x/text/unicode/norm`), already a dependency for SASLprep.

## Alternatives considered

- **A package of its own (`cera/text`).** Needs the interpreter's marked
  content, the page matrix, the font layer and, for highlights, the
  editor; all of them live in the root package.
- **Character boxes instead of quads.** Axis-aligned boxes are wrong for
  rotated and skewed text and cannot be written as QuadPoints.
- **Content order instead of a layout model.** Breaks selection and copy
  across columns and in streams written out of order.
- **Ranges as the stored anchor.** A `TextRange` indexes characters of one
  extraction and moves when extraction improves; byte offsets into a named
  text layer plus a quote with context survive that.
