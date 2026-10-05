# 0012. Own PDF reader and font layer

- Status: accepted
- Date: 2026-10-05
- Milestone: M8½

## Context

cera reads files through [go-pdfkit/reader](https://github.com/go-pdfkit/reader)
v0.6.0 and the PDF side of fonts through
[go-pdfkit/pdffont](https://github.com/go-pdfkit/pdffont) v0.3.1, both used
as is (BSD-3, not maintained by us). They are robust against damaged files,
but they stand in the way of cera's goals (≤ 1.5× MuPDF on one core, faster
than MuPDF on all cores, ≈ 0 allocations per page, 0 panics, js/wasm):

- **Not safe for concurrent use.** `Get`, the page tree, object streams and
  repair write plain maps, and a per-document `loading` map guards
  recursion. `Document` therefore asks callers to interpret pages of one
  document from one goroutine; pages cannot be rendered in parallel.
- **Allocations per object.** `Object` is an interface (numbers and refs are
  boxed), every dictionary is a map, every name a fresh string; reals go
  through a failing `strconv.ParseInt`; `N G R` lookahead lexes up to three
  tokens. A `/Widths` array costs one allocation per entry.
- **No cache of decoded streams.** Form XObjects and Type 3 glyph
  procedures are inflated again on every use; the object cache is unbounded.
  `Page` copies the inherited dictionary on every call, `Version` scans the
  whole file on every call.
- **Repair** calls `Get` on every object, and a lazy repair from `Get` resets
  the reader's state in the middle of a render.
- **Fatal inputs** `recoverPanic` cannot catch: unbounded parser nesting
  (stack overflow), predictor rows sized from unchecked parameters, LZW and
  RunLength without an output cap (LZW also allocates per code).
- **Hybrid files:** a free entry in the classic table hides the compressed
  entry of the `/XRefStm`.
- **Fonts:** pdffont maps `MacRomanEncoding` and `MacExpertEncoding` to
  `StandardEncoding` (`read.go`, pinned by its own test, also in v0.4.0).
  Code 0x27 becomes `quoteright` instead of `quotesingle` and everything above
  127 — umlauts in Mac PDFs — selects the wrong glyph, in rendering and in
  text extraction. `pdffont.Read` takes a concrete `*reader.Document`, so the
  font layer cannot move without the reader.

## Decision

cera gets its own reader, `internal/pdf`, and its own font layer,
`internal/pdffont`. The reader ports the robustness work of go-pdfkit/reader
v0.6.0 (repair, filters, encryption; BSD-3, attributed in every ported file
and in `internal/pdf/LICENSE-go-pdfkit`). The font layer is written anew
around compact tables, with the Mac encodings from PDF 32000-2 Annex D; the
glyph-name tables and the ToUnicode reader are taken over from pdffont
v0.3.1 (BSD-3, attributed likewise).

**Objects.** A 24-byte value `Object` (kind, length, a 64-bit payload and one
pointer) instead of an interface; `Array` is `[]Object`; `Dict` is a flat
slice of `{Key Name; Val Object}` entries, scanned linearly up to 12 keys and
sorted for binary search above. Each indirect object is parsed into one
contiguous slab. Well-known names are interned to static strings, other names
alias the input. Objects are immutable once published; slices handed out are
clipped. `unsafe` stays in `object.go` and `dict.go`.

**Concurrency.** The cross-reference table is immutable behind an
`atomic.Pointer`; each entry publishes its object with a compare-and-swap.
A goroutine that misses parses the object itself and never waits on another,
so loads cannot deadlock. Reference cycles are cut by a short pooled chain
(`MaxRefChain`). A repair — at open, or later when an offset turns out wrong —
builds a new table and swaps it in; readers of the old one are not
disturbed. Pages of one document may then be rendered from many goroutines.

**Streams.** A sharded, byte-bounded LRU of decoded streams with
single-flight (`streamCacheBytes`, `MaxCachedStream`). The filter chain is
resolved before a flight starts; inside it only pure code runs. Images and
font programs bypass the cache, their consumers already cache results.
Encrypted streams are decrypted lazily, as the first decode stage. The
`Decoded` contract of v0.6 (data, undecoded rest, image filter, recovered,
cause) is kept.

**Filters.** Flate with pooled decompressors and no Adler-32 check (as pdf.js
and MuPDF), LZW rewritten on prefix tables, every filter capped by
`MaxStreamBytes`, predictor rows bounded by `MaxPredictorRow`, parser nesting
by `MaxNesting`; hybrid sections merge table `n`, then `/XRefStm`, then table
`f`.

**Input.** `[]byte` without copies, later `io.ReaderAt` behind the same
`source` interface.

**Verification.** A separate module `readerdiff/` (like `accuracy/`) keeps
go-pdfkit out of cera's `go.mod` and compares both readers on every corpus
file: every object (L1), every decoded stream (L2), trailer, version and
page tree (L3), every font (L4) and finally the raster of every page (L5).
Differences are allowed only with a reason code (`hybrid-xref`,
`adler-ignored`, `stream-cap`, `nesting-limit`, `predictor-limit`,
`tiff-subbyte`, `macroman-encoding`, `macexpert-encoding`). Paired benchmarks
against v0.6 show the gain.

## Outcome

On the pinned corpus, the pdf.js suite and borb (1649 files) the readers
agree on every object, decoded stream, page and content stream except where
the allowlist gives a reason; the font layers agree on every font code of the
pinned corpus except the Mac encodings. All 105 pinned pages render
pixel-identical. Pinned corpus, v0.6 → internal/pdf: Open 2.5 → 0.89 ms
(4319 → 1026 allocations), resolving every object 41 → 21 ms (388k → 37k
allocations); median allocations per rendered page 33 → 3.

## Consequences

- Pages of a document can be rendered in parallel; the comment on
  `Document` and the README change accordingly.
- `Document.Reader()` is removed (no callers, no tagged release).
- cera maintains the reader. The fork points are reader v0.6.0 and pdffont
  v0.3.1; their release notes are reviewed once per milestone and fixes are
  taken over with attribution.
- Loaded objects are not evicted; memory grows with the objects touched,
  bounded by the file size times a constant.
- Cached decoded data and object slices are shared: callers must not write
  to them.
- Mac Roman and Mac Expert fonts render and extract correctly; the accuracy
  thresholds of affected files go down.

## Alternatives considered

- **Keep go-pdfkit/reader behind a document-wide lock.** Serialises every
  object access; parallel pages gain little and allocations stay.
- **Fork go-pdfkit/reader and patch it.** Thread safety, the object model and
  the cache touch nearly every file; the result would be a rewrite carrying
  an API built for the interface model.
- **Port pdffont unchanged.** Carries the Mac Roman error, which its test
  pins, and the map-based width and ToUnicode tables.
