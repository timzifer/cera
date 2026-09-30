# cera

[![CI](https://github.com/timzifer/cera/actions/workflows/ci.yml/badge.svg)](https://github.com/timzifer/cera/actions/workflows/ci.yml)
[![Large corpora](https://github.com/timzifer/cera/actions/workflows/corpus-large.yml/badge.svg)](https://github.com/timzifer/cera/actions/workflows/corpus-large.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/timzifer/cera.svg)](https://pkg.go.dev/github.com/timzifer/cera)

A PDF renderer in pure Go, aiming to be the fastest and leanest in the Go
ecosystem: MuPDF-class speed on technical drawings (≤ 1.5× MuPDF on one core,
faster than MuPDF on all cores), ≈ 0 allocations per page in steady state,
0 panics, no cgo, every GOOS/GOARCH including `js/wasm`.

cera is where the pieces come together:

| layer | source | status |
|---|---|---|
| file reader: xref, objects, filters, encryption, repair | [go-pdfkit/reader](https://github.com/go-pdfkit/reader) (BSD-3) | used as is |
| content scanner, interpreter, graphics state | cera | zero-allocation scanner; paths, clips, colour spaces, ExtGState, form XObjects |
| display list, bands, workers | cera | display list per page and scale, band index, parallel bands (M3) |
| rasterizer, stroker, clip, compositing | [timzifer/stilus](https://github.com/timzifer/stilus) (MIT) | used as is |
| fonts: PDF side (encodings, widths, ToUnicode) | [go-pdfkit/pdffont](https://github.com/go-pdfkit/pdffont) (BSD-3) | used as is |
| fonts: programs (TrueType, CFF, Type 1) and stand-ins | [go-opentype/opentype](https://github.com/go-opentype/opentype), [go-opentype/fonts](https://github.com/go-opentype/fonts) (Arimo, Tinos, Cousine) | used as is |
| text: glyph selection, glyph cache, Type 3, render modes, TextDevice | cera, glyph rules ported from the M2 spike | M4 |
| images: samples, masks, mip levels, image cache | cera, decoding rules ported from [timzifer/render](https://github.com/timzifer/render); codecs [go-images/jpeg](https://github.com/go-images/jpeg) (BSD-3), [go-images/jpeg2000](https://github.com/go-images/jpeg2000) and [gobig2](https://github.com/tannevaled/gobig2) (Apache-2.0) | M5 |
| shadings, transparency | [timzifer/render](https://github.com/timzifer/render) fork of go-pdfkit/render (BSD-3) | to be ported (M6–M7) |

The [stilus integration in the go-pdfkit/render fork](https://github.com/timzifer/render/tree/codex/stilus-renderer)
was the M2 spike of the plan: same interpreter, stilus instead of go-gfx,
9.9× less time summed over the corpus and 0 errors. cera builds the part that
spike could not change: its own interpreter feeding a device interface and a
display list, so that parsing happens once per page, only the visible region
is drawn, and all cores are used.

## Use

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

text, err := page.Text(ctx) // characters with boxes, including invisible text
fmt.Println(text.String())
```

`dst` may be any sub-rectangle of the page (a tile or viewport): only that
region is drawn. `RenderOptions.Region` narrows it further. Errors in the
content never stop a page; a non-nil error means a partial image
(`ErrDeadline`, a rasterizer budget, or a recovered `*PanicError`).

The first render of a page at a scale interprets its content into a display
list: every path with its transform, paint and device-space box (clipped by
the clips around it), and a band index of about 16 horizontal bands. Later
renders at that scale (other tiles, a scrolled viewport, another thread)
skip parsing and draw only the items that touch their region. One worker
draws the region in one pass; several take bands from a shared counter, each
with its own pooled stilus canvas, so steady-state rendering allocates
nothing per page but its deadline. Content under an empty clip and paths
outside the page are dropped while recording.

Text is recorded as runs of glyphs: each glyph's outline (read once per
font, in em units) and its device matrix. A raster worker rasterizes a
glyph once per size (to 1/64 pixel per em) and quarter-pixel position
into a coverage mask and composites it from then on, through the clip
stack like any fill; glyphs larger than 160 pixels per em are filled as
paths. Each worker keeps its own cache (4 MB), so drawing takes no locks.

Images are decoded once per document and kept in an image cache
(256 MB, least recently used out), in the form their samples come in: one
byte a pixel and a palette for grey, indexed and other one-component
images, one bit a pixel for faxes, JBIG2 and stencil masks, four bytes only
for colour. A raster worker draws an image as a fill of its parallelogram
with a shader that maps every device pixel back into the image, so images
are clipped and antialiased like paths and a band samples only its own
rows. An image drawn smaller than its samples is read from a mip level
(the image averaged over 2^k × 2^k blocks, made on first use) at most twice
as fine as the device, bilinearly; magnified images are sampled at the
nearest pixel unless they ask for `/Interpolate`.

`Page.Run` drives any `Device` directly, without a display list; a device
that also implements `TextDevice` receives every string shown, in every
render mode. `Page.Text` is built on it.

```sh
go run ./cmd/cera -dpi 150 -v -o 'page-%d.png' input.pdf
```

## What is drawn today

Text: all text operators and render modes (fill, stroke, invisible, and
the clipping modes 4–7), embedded TrueType, CFF (bare, OpenType and
CID-keyed) and Type 1 programs, composite fonts (2-byte codes,
CIDToGIDMap), Type 3 fonts (coloured `d0` and uncoloured `d1` glyphs),
and stand-ins for fonts a file does not embed: Arimo, Tinos and Cousine,
metric-compatible with Helvetica, Times and Courier, in the weight and
slope the font asks for. Images: image XObjects and inline images at 1, 2,
4, 8 and 16 bits in every colour space below, with `/Decode`, stencil
masks (`/ImageMask`) in the fill colour, soft masks (`/SMask`), stencil
masks (`/Mask` stream) and colour keys (`/Mask` array), each at its own
resolution; JPEG (`DCTDecode`, including Adobe CMYK), JPEG 2000
(`JPXDecode`, with the alpha a codestream may carry), JBIG2 (with
`JBIG2Globals`), CCITT fax and every stream filter of the reader. Paths (all construction and painting operators, nonzero and even-odd),
strokes (width, caps, joins, miter limit, dashes, exact under anisotropic
transforms), clipping (rectangles free, other shapes as masks), DeviceGray,
DeviceRGB, DeviceCMYK, CalGray/CalRGB, ICCBased (by channel count), Indexed,
constant alpha (`CA`, `ca`), form XObjects with `Matrix` and `BBox`, `/Rotate`,
`/UserUnit`, CropBox.

Not yet, and counted in `Stats.Unsupported` so the corpus report shows what
matters most: shadings, patterns, soft masks in the graphics state,
blend modes, transparency groups, optional content, tint transforms
(Separation/DeviceN are drawn as grey), Lab; fonts neither embedded nor
standing in (`font-missing`: Symbol, ZapfDingbats, non-embedded composite
fonts), vertical writing (`vertical-text`, drawn with default metrics),
Type 3 glyphs in clipping modes (`type3-clip`), `/Matte` of soft masks
(`smask-matte`, drawn without), image filters the reader does not know
(`image-filter`), images larger than 256 MB decoded (`image-too-large`).

## Roadmap

Milestones follow the spec *PDF-Renderer für Go (Testballon)*.

| | milestone | content |
|---|---|---|
| ✓ | M0 harness and corpus | stilus/harness, pinned corpus, PDFium reference |
| ✓ | M1 rasterizer core | stilus |
| ✓ | M2 spike in a go-pdfkit fork | timzifer/render `codex/stilus-renderer` |
| ✓ | M3 display list, region, parallelism | own interpreter, streaming content scanner without per-operand allocations, display list with band index, parallel bands |
| ✓ | M4 text | fonts via pdffont and opentype, stand-ins, per-worker glyph mask cache, Type 3, all render modes incl. text clips, TextDevice, `Page.Run`, `Page.Text` |
| ✓ | M5 images | image XObjects and inline images, JPEG/JPEG 2000/JBIG2/CCITT, soft, stencil and colour-key masks at their own resolution, one-bit and palette planes, lazy mip levels with bilinear sampling, per-document image cache |
| | M6 transparency | groups, knockout, blend modes, soft masks |
| | M7 shadings and colour | types 1–7, function LUTs, Separation/DeviceN, simplified ICC |
| | M8 robustness | fuzzing, large corpora, budgets (started: CI below) |
| | M9 GPU backend | GGDevice on gogpu/gg, glyph atlas, lux |

## Corpus and CI

Every push and pull request runs `CI` (`.github/workflows/ci.yml`): lint
(golangci-lint), tests with race detector on Linux, macOS and Windows, a
cgo-free build for 14 targets including `js/wasm` and `wasip1/wasm`, 60 s of
fuzzing of the interpreter, benchmarks, and the pinned corpus. Its single
aggregate job `ci-ok` is the required check for `main`.

Corpora come in two tiers:

| tier | what | when | gate |
|---|---|---|---|
| pinned | 33 files from the pdf.js suite and arXiv (SHA-256 in `internal/corpus/manifest.json`, shared with the stilus harness) + 6 synthetic A3 drawings | every push | no panic, hang, deadline or unopened file; report with ms/page, allocations and unsupported features in the job summary |
| large | pdf.js `test/pdfs` (~980 files), [borb-pdf-corpus](https://github.com/borb-pdf/borb-pdf-corpus), [veraPDF corpus](https://github.com/veraPDF/veraPDF-corpus), [pdfCabinetOfHorrors](https://github.com/openpreserve/format-corpus/tree/master/pdfCabinetOfHorrors), [PDF 2.0 examples](https://github.com/pdf-association/pdf20examples), qpdf test files, all at pinned commits; one sampled zip of [CC-MAIN-2021-31-PDF-UNTRUNCATED](https://digitalcorpora.org/corpora/file-corpora/cc-main-2021-31-pdf-untruncated/) (7.9 M web PDFs, a different shard each week) | weekly and on demand (`Large corpora` workflow) | no panic, hang or deadline; files the reader rejects are listed, not failed |
| unsafe | [CC-MAIN-2021-31-UNSAFE](https://digitalcorpora.org/corpora/file-corpora/unsafe-docs-cc-main-2021-31-unsafe/) `corpora-pdf` (fuzzer output, deliberately malformed files) | on demand only: run the workflow with `sources` = `["unsafe"]` | same |

Locally:

```sh
go run ./cmd/corpus fetch  -dir testdata/corpus   # pinned files, SHA-256 verified
go run ./cmd/corpus scenes -dir testdata/corpus   # synthetic drawings
go run ./cmd/corpus run    -dir testdata/corpus -dpi 150 -out report

go run ./cmd/corpus sources                       # the large corpora
go run ./cmd/corpus get -source borb              # → testdata/borb
go run ./cmd/corpus get -source ccmain -sample 500 -seed 42
go run ./cmd/corpus run -dir testdata/borb -runs 0 -pages 3 -fail-open=false
```

Customer drawings stay local: pass their directory as another `-dir`
(comma-separated); they are reported as category `local`.

`-workers` sets the goroutines per page (default 1, the single-core figure
of the spec; 0 = all cores). Every page is timed twice per run: a first
render (interpret and draw) and a render again with its display list cached.

Accuracy against PDFium (WebAssembly, no cgo) is measured by the
[stilus harness](https://github.com/timzifer/stilus/tree/main/harness); a cera
engine for it is still to be added there.

## Repository setup

`main` is protected by the ruleset in `.github/rulesets/main.json`: no direct
pushes, force pushes or deletion; changes land through pull requests with a
green `ci-ok`. Apply or update it with `scripts/protect-main.sh` (GitHub CLI,
admin rights) or import the file under Settings → Rules → Rulesets.

## License

MIT
