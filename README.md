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
| rasterizer, stroker, clip, compositing; shaders for images, glyph masks, layers and blend modes, gradients, meshes | [timzifer/stilus](https://github.com/timzifer/stilus) (MIT) | used as is; the generic shaders moved there from cera with M7 |
| fonts: PDF side (encodings, widths, ToUnicode) | [go-pdfkit/pdffont](https://github.com/go-pdfkit/pdffont) (BSD-3) | used as is |
| fonts: programs (TrueType, CFF, Type 1) and stand-ins | [go-opentype/opentype](https://github.com/go-opentype/opentype), [go-opentype/fonts](https://github.com/go-opentype/fonts) (Arimo, Tinos, Cousine; DejaVu Sans subsets for Symbol and ZapfDingbats; Noto Sans JP/SC/KR in `fonts/cjk`) | used as is; cera reads the FDArray font matrices of CID-keyed CFF itself (`cffcid.go`) |
| fonts: CMaps, vertical writing, font providers | cera; predefined CMaps compiled from [Adobe's CMap resources](https://github.com/adobe-type-tools/cmap-resources) (BSD-3) | M8 (ADR 0008) |
| text: glyph selection, glyph cache, Type 3, render modes, TextDevice | cera, glyph rules ported from the M2 spike | M4 |
| images: samples, masks, mip levels, image cache | cera, decoding rules ported from [timzifer/render](https://github.com/timzifer/render); codecs [go-images/jpeg](https://github.com/go-images/jpeg) (BSD-3), [go-images/jpeg2000](https://github.com/go-images/jpeg2000) and [gobig2](https://github.com/tannevaled/gobig2) (Apache-2.0) | M5 |
| transparency: groups, knockout, blend modes, soft masks | cera; PDF functions ported from [timzifer/render](https://github.com/timzifer/render) | M6 |
| shadings, colour spaces: tint transforms, CIE spaces, ICC matrix/TRC profiles | cera, mesh and patch reading after [timzifer/render](https://github.com/timzifer/render) (BSD-3); drawn by stilus | M7 |

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
[`form/fyneform`](form/fyneform) is such a provider for fyne, a module of
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
nearest pixel unless they ask for `/Interpolate`, as Ghostscript does.
`RenderOptions.ImageFilter = ImageSmooth` (`-image-filter smooth` in
`cmd/cera`) smooths every magnified image bilinearly instead, as PDFium,
MuPDF and Poppler do; switching it does not interpret the page again. An
opaque image drawn right over another on the same parallelogram replaces
it in the display list, so the lower image does not show through the
antialiased edges as a frame.

Transparency groups and soft masks are drawn into layers: an RGBA image
over the part of the band the group can touch, taken from a buffer pool
of the worker, composited back through the clips around the group with a
shader that reads the backdrop, so blend modes stay exact at antialiased
edges. Most groups in real files need no layer, and the display list
drops them while recording: a group that composites like its content
(Normal, opaque, unmasked, and non-isolated or without blend modes inside)
and a group of a single object, whose opacity goes to the object. A soft
mask is drawn, as the group its form describes, for each group or object
it masks, over that object's box only.

Shadings are read once per document into what a raster worker draws
without evaluating PDF functions: axial and radial shadings into a ramp
of 512 colours over their parameter, with knots at the bounds of
stitching functions so that colour breaks fall on the exact pixel, drawn
by the gradient shaders of stilus (exact two-circle geometry, extends, per
pixel centre), a
function-based shading into a 128 × 128 texture sampled bilinearly over
its domain, and the four mesh kinds into Gouraud-shaded triangles,
coloured at their vertices or carrying the shading's parameter into a
ramp; Coons and tensor patches are cut on a grid of 2 to 16 cells a side,
finer the fewer patches a mesh has. A mesh is one fill of its box with a
stilus `MeshShader`, binned once per matrix and shared by all workers,
without antialiasing inside so that no seams show and without a layer. A
path, a stroke or text painted with a pattern becomes the pattern painted
through a clip of its shape (`ClipPath`, or `ClipStroke` for strokes). A
tiling pattern is rasterized once per page and matrix into a tile of one
step at device resolution, which stilus's `ImageShader` repeats with exact
wrap-around (rotated and skewed hatching included, without seams); an
uncoloured pattern is an alpha tile painted in any colour. When one cell
covers the shape, or a few cells too large for a tile do, the cells are
replayed as vector operations instead.

Colours are converted to sRGB when they are read: Separation and DeviceN
through their tint transforms into the alternate space (a one-ink
transform is tabulated at 256 tints, so a spot-colour image costs a
palette), Lab, CalGray, CalRGB and ICC profiles of the matrix/TRC kind
through CIE XYZ, adapted to D50 with the Bradford transform. An ICC
profile close to sRGB is taken as the device space; RGB images in another
profile convert through tables per channel. DeviceCMYK converts through a
SWOP press profile (colord's profile of CGATS TR 005, NPES, the
characterization data's source; `internal/cmyk`), as a press prints it,
or with `OpenOptions.NaiveCMYK` naively, as device values.

`Page.Run` drives any `Device` directly, without a display list; a device
that also implements `TextDevice` receives every string shown, in every
render mode. `Page.Text` is built on it.

```sh
go run ./cmd/cera -dpi 150 -v -o 'page-%d.png' input.pdf
```

## What is drawn today

Text: all text operators and render modes (fill, stroke, invisible, and
the clipping modes 4–7), embedded TrueType, CFF (bare, OpenType and
CID-keyed) and Type 1 programs, composite fonts (the predefined CMaps of
PDF 2.0 Table 116 and embedded ones, codes of one to four bytes,
CIDToGIDMap), vertical writing (`-V` CMaps and `/WMode`, `/W2` and `/DW2`
metrics, vertical glyph forms through the program's `vert` feature),
Type 3 fonts (coloured `d0` and uncoloured `d1` glyphs), and stand-ins for
fonts a file does not embed: Arimo, Tinos and Cousine, metric-compatible
with Helvetica, Times and Courier, chosen by name (a table of frequent
families: Calibri, Verdana, Cambria, Consolas …) and descriptor, in the
weight, slope and width the font asks for; Symbol and ZapfDingbats from
subsets of DejaVu Sans drawn to their AFM widths. Fonts a file does not
embed can also come from a `FontProvider` (`cera.OpenWith`); the module
`github.com/timzifer/cera/fonts/cjk` provides Noto Sans for the Adobe CJK
collections, and text of those collections reads back as Unicode even
without a ToUnicode map. Images: image XObjects and inline images at 1, 2,
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
`/UserUnit`, CropBox. Transparency: groups (isolated and non-isolated,
knockout, with the fill alpha of the state that paints them), all 16
blend modes, soft masks (luminosity with backdrop colour, alpha, transfer
functions of all four function types) in the graphics state, and objects
painted with a blend mode or soft mask, composited as groups of their own;
glyphs of one text-showing operator that overlap at an opacity below 1
knock each other out (`TK`). The rest of the graphics state: the font of
`gs` (`/Font`), transfer functions (`TR`, `TR2`) on solid colours, and
overprint (`OP`, `op`, `OPM`) of DeviceCMYK, Separation and DeviceN
simulated as Multiply with `RenderOptions.SimulateOverprint` (off by
default); the device parameters (`BG`, `UCR`, `HT`, `FL`, `SM`, `SA`) and
`ri` are ignored on purpose. A page that blends is drawn transparent and composited onto the
background, which comes after the page group. Shadings: all seven types
(function-based, axial, radial, free-form and lattice triangle meshes,
Coons and tensor-product patches), with `Function`, `Domain`, `Extend`,
`BBox`, and `Background` in patterns, painted by `sh` and as shading
patterns of fills, strokes and text. Colour spaces: Separation and
DeviceN with their tint transforms (`/None` paints nothing), Lab,
CalGray, CalRGB, ICCBased with grey and RGB matrix/TRC profiles,
Indexed on any of them. Optional content (layers): groups and membership
dictionaries (`/P` and visibility expressions), marked content `BDC /OC`
and XObjects with `/OC`, the default and alternate configurations with
`/Order`, radio-button groups, locked layers, intents and automatic
states for view, print and export (`/AS`, including zoom ranges); see
below. Annotations: drawn from their normal appearance streams (with
`/AS` states, `/CA`, `NoZoom`, `NoRotate`, `/OC`), and generated for
Square, Circle, Line (with endings and leader lines), PolyLine, Polygon,
Ink, Highlight, Underline, StrikeOut and Squiggly without one;
`Page.Annotations` exposes them with link targets. Forms: AcroForm fields
with inheritance, values in a caller-owned `FormState`, appearances
generated for text fields (single line, multiline, comb, password,
auto-size), check boxes, radio buttons, combo and list boxes and push
buttons when the value changed, `/NeedAppearances` is set or `/AP` is
missing; native widgets through `FormWidgetProvider` and `FormLayer`
(tab order, focus), a fyne provider in `form/fyneform`; `cmd/cera -field
name=value` fills a form. JavaScript actions are not run, XFA forms are
counted (`xfa`) and drawn from their AcroForm fallback.

Not yet, and counted in `Stats.Unsupported` so the corpus report shows what
matters most: tiling patterns past their budgets (`pattern-budget`:
nested past 4 levels, more than 64 MB of tiles per page), tint
transforms that do not read (`tint-transform`, drawn as grey), shading
functions that do not read (`shading-function`); ICC profiles built from
lookup tables (CMYK press profiles among them) are drawn as the device
space of as many components, CMYK through cera's SWOP profile; fonts neither embedded nor
standing in (`font-missing`; non-embedded composite fonts no `FontProvider`
supplies as `font-missing-japan1`, `-gb1`, `-cns1`, `-korea1` or `-cid`),
CMaps that are neither predefined nor readable (`cmap-missing`, read as
Identity),
strokes, images and unbounded shadings of Type 3 glyphs in clipping modes
(`type3-clip-approx`, clipping to their boxes), annotations without
appearance that cera does not generate (`annot-no-ap`: FreeText, Text,
Stamp …), `/Matte` of soft masks over images in other spaces than grey or
RGB (`smask-matte`, undone in RGB), image filters the reader does not know
(`image-filter`), images larger than 256 MB decoded (`image-too-large`);
a non-isolated group with blend modes inside that is an object of a
knockout group (`non-isolated-blend`: drawn isolated; one that is itself
blended has its backdrop removed, PDF 2.0 11.4.8), `/AIS` (`alpha-is-shape`), transfer functions that do
not read (`smask-transfer`), soft masks past 4 levels of nesting or 1024
per page (`smask-budget`, drawn empty); overprint that is not simulated
(`overprint`, painted over), transfer functions on images, shadings and
patterns or that do not read (`transfer`, drawn without), overlapping
stroked or pattern-filled glyphs that `TK` would knock out
(`text-knockout`). Inputs past a budget (see Budgets) are drawn with less
and counted as well: `nesting-budget`, `mesh-budget`, `annot-budget`,
`pattern-budget`, `smask-budget`, `image-too-large`.

## Budgets

Every bound on the work or memory one input may cost is in
[`budget.go`](budget.go) (ADR 0010). Budgets bound valid input that would
cost too much: past one, the page is drawn with less, never failed, and the
key is counted in `Stats.Unsupported`, so the corpus reports show which
files hit it. Limits bound structures the specification bounds or no
producer comes near; past them input is malformed and treated like other
damage. Caches bound memory kept for reuse and do not change what is drawn.
A test keeps this table in step with the code.

| bound | value | key | what |
|---|---:|---|---|
| `maxFormDepth` | 12 | `nesting-budget` | forms, Type 3 glyphs, pattern cells and soft masks nested in each other; deeper ones are not drawn |
| `maxStateDepth` | 4 Ki | `nesting-budget` | graphics states saved by q; a q past it is ignored |
| `maxPatternDepth` | 4 | `pattern-budget` | tiling patterns painted inside pattern cells; deeper ones are not drawn |
| `maxTileBytes` | 64 Mi | `pattern-budget` | bytes of pattern tiles made for one page; further patterns are not drawn |
| `maxTileOffsets` | 64 | `pattern-budget` | copies of a cell drawn into one tile; only the first 8×8 are drawn |
| `maxTileSide` | 1 Ki | `pattern-budget` | pixels of a tile side; larger cells, if more than maxReplayCells, are drawn at this resolution |
| `maxMasks` | 1 Ki | `smask-budget` | soft masks drawn for one page; further masks are empty |
| `maxMaskDepth` | 4 | `smask-budget` | soft masks nested in each other; deeper masks are empty |
| `maxMeshTris` | 1 Mi | `mesh-budget` | triangles of one mesh shading; the rest of the mesh is not read |
| `maxPatches` | 64 Ki | `mesh-budget` | patches of one type 6 or 7 shading; the rest is not read |
| `maxMeshRow` | 64 Ki | `mesh-budget` | vertices per row of a type 5 shading; a longer row is not drawn |
| `maxAnnots` | 4 Ki | `annot-budget` | annotations drawn for one page; the rest are not drawn |
| `maxImageBytes` | 256 Mi | `image-too-large` | bytes of one decoded image plane; a larger image is not drawn |
| `maxCodecPixels` | 64 Mi | `image-too-large` | pixels a JPEG or JPEG 2000 codestream may declare; a larger image is not drawn |
| `maxKnockoutGlyphs` | 256 | `text-knockout` | glyphs of one run tested for overlap under text knockout; a longer run is taken to overlap |
| `maxComps` | 32 | – | components of a colour value; further ones are ignored |
| `maxFunctionDepth` | 8 | – | stitching functions naming functions; deeper ones do not read (shading-function, tint-transform) |
| `maxPSStack` | 100 | – | operand stack of a PostScript calculator function; further pushes are dropped |
| `maxLayerDepth` | 16 | – | /Order nesting and visibility expressions; deeper ones do not read (oc-bad) |
| `maxFieldDepth` | 32 | – | depth of the AcroForm field tree; deeper fields are not read |
| `content.MaxNesting` | 32 | – | arrays and dictionaries nested in one operand (Stats.Errors) |
| `content.MaxOperands` | 4 Ki | – | operands kept for one operator; the first are dropped |
| `cmap.MaxSpans` | 128 Ki | – | code ranges of one CMap; further ones are ignored |
| `cmap.MaxSpaces` | 64 | – | codespace ranges of one CMap |
| `cmap.MaxDepth` | 4 | – | usecmap nesting |
| `imageCacheBytes` | 256 Mi | – | decoded images a document keeps |
| `maxFreeLayerBytes` | 64 Mi | – | transparency layer buffers a device keeps |
| `maxIdleGlyphCaches` | 64 | – | glyph mask caches kept between renders |
| `maxMeshShaders` | 8 | – | mesh shaders kept per mesh |
| `maxReplayCells` | 64 | – | pattern cells drawn as vector operations instead of a tile |
| `maxRunPoints` | 4 Ki | – | points of consecutive strokes or fills of one pen drawn as one union; past it, the next union starts |

## Roadmap

Milestones follow the spec *PDF-Renderer für Go (Testballon)*. What is
still missing for a feature-complete renderer, and how it will be built, is
recorded as architecture decisions in [`docs/adr`](docs/adr/README.md).

| | milestone | content |
|---|---|---|
| ✓ | M0 harness and corpus | stilus/harness, pinned corpus, PDFium reference |
| ✓ | M1 rasterizer core | stilus |
| ✓ | M2 spike in a go-pdfkit fork | timzifer/render `codex/stilus-renderer` |
| ✓ | M3 display list, region, parallelism | own interpreter, streaming content scanner without per-operand allocations, display list with band index, parallel bands |
| ✓ | M4 text | fonts via pdffont and opentype, stand-ins, per-worker glyph mask cache, Type 3, all render modes incl. text clips, TextDevice, `Page.Run`, `Page.Text` |
| ✓ | M5 images | image XObjects and inline images, JPEG/JPEG 2000/JBIG2/CCITT, soft, stencil and colour-key masks at their own resolution, one-bit and palette planes, lazy mip levels with bilinear sampling, per-document image cache |
| ✓ | M6 transparency | groups (isolated, non-isolated, knockout) in pooled layers per band, all blend modes exact at antialiased edges, soft masks (luminosity, alpha, backdrop, transfer functions), trivial and single-object groups dropped from the display list |
| ◐ | M7 shadings and colour | shading types 1–7 and shading patterns, function LUTs (ramps, textures, tint tables), Separation/DeviceN, Lab, CalGray/CalRGB, simplified ICC (matrix/TRC); generic shaders moved to stilus; tiling patterns (ADR 0002) on stilus's wrapping textures, knotted ramps and the mesh shader of stilus v0.7 |
| ✓ | M7½ layers, annotations, forms | optional content (ADR 0004), annotations (ADR 0005), interactive forms and `FormWidgetProvider` (ADR 0006) |
| ✓ | M8 robustness | font fallbacks, CMaps and vertical writing (ADR 0008), transparency remainders (ADR 0009), accuracy against PDFium with pinned thresholds, fuzzing of every reader, budgets, large corpora in random batches (ADR 0010) |
| | M9 GPU backend | GGDevice on gogpu/gg, glyph atlas, lux |

## Corpus and CI

Every push and pull request runs `CI` (`.github/workflows/ci.yml`): lint
(golangci-lint), tests with race detector on Linux, macOS and Windows, a
cgo-free build for 14 targets including `js/wasm` and `wasip1/wasm`, 60 s of
fuzzing for each reader of untrusted structure, benchmarks, the pinned
corpus, and its accuracy against PDFium. Its single
aggregate job `ci-ok` is the required check for `main`.

Corpora come in two tiers:

| tier | what | when | gate |
|---|---|---|---|
| pinned | 33 files from the pdf.js suite and arXiv (SHA-256 in `internal/corpus/manifest.json`, shared with the stilus harness) + 8 synthetic A3 drawings | every push | no panic, hang, deadline or unopened file; report with ms/page, allocations and unsupported features in the job summary; no file differs from PDFium more than its pinned threshold |
| large | pdf.js `test/pdfs` (~980 files), [borb-pdf-corpus](https://github.com/borb-pdf/borb-pdf-corpus), [veraPDF corpus](https://github.com/veraPDF/veraPDF-corpus), [pdfCabinetOfHorrors](https://github.com/openpreserve/format-corpus/tree/master/pdfCabinetOfHorrors), [PDF 2.0 examples](https://github.com/pdf-association/pdf20examples), qpdf test files, all at pinned commits; one sampled zip of [CC-MAIN-2021-31-PDF-UNTRUNCATED](https://digitalcorpora.org/corpora/file-corpora/cc-main-2021-31-pdf-untruncated/) (7.9 M web PDFs, a different shard each week) | weekly and on demand (`Large corpora` workflow), a random batch per source (300 files, 40 of them compared with PDFium); in full locally | no panic, hang or deadline; files the reader rejects are listed, not failed; the 20 slowest pages, the 20 with the most allocations and the differences to PDFium are reported |
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
go run ./cmd/corpus run -dir testdata/borb -runs 0 -sample 50 -seed 7   # a batch, as CI draws it
```

Customer drawings stay local: pass their directory as another `-dir`
(comma-separated); they are reported as category `local`.

`-workers` sets the goroutines per page (default 1, the single-core figure
of the spec; 0 = all cores). Every page is timed twice per run: a first
render (interpret and draw) and a render again with its display list cached.

### Accuracy

`accuracy/` compares cera with PDFium (WebAssembly through go-pdfium, no
cgo; a module of its own, so cera does not depend on it) page by page
(ADR 0010): the share of pixels differing by more than 16 levels in any
channel and the 99th percentile of the difference, next to both renderers'
time and the page's `Stats.Unsupported` keys. Every file of the pinned
corpus has a threshold for its worst page in `accuracy/thresholds.json`; CI
fails when a file gets worse. Thresholds only go down, in the pull request
that improves a file: `-update` pins new files and lowers improved ones.
Cera, PDFium and difference images of the worst pages are in the job's
artifact.

```sh
cd accuracy
go run . -dir ../testdata/corpus -thresholds thresholds.json -refs ../testdata/pdfium -out ../report-accuracy
go run . -dir ../testdata/corpus -thresholds thresholds.json -refs ../testdata/pdfium -update   # after an improvement
go run . -dir ../testdata/borb -pages 3 -out ../report-borb                                     # a large corpus, report only
```

`-refs` keeps PDFium's renderings between runs; `-sample` and `-seed`
compare a random batch, as the `Large corpora` workflow does.

PDFium is a reference, not ground truth. For local runs,
`accuracy/reference` (a module of its own, with cgo) measures cera against
several references and against an exact rendering, and writes a report
(`report.html`, `pages.csv`, `summary.json`):

- **Consensus of engines.** PDFium (WebAssembly), MuPDF (linked through cgo
  with go-fitz, which ships its libraries; without cgo `mutool`), Poppler
  (`pdftoppm`) and Ghostscript (`gs`), whichever are available. Every
  engine, cera and each reference alike, is held against the median of all
  the others on the pixels where those agree; where the references disagree
  among themselves the pixel is *contested* and says nothing about who is
  right. Shares are of the inked area (pixels not paper white in some
  rendering). Each statistic comes twice: per pixel (*edges*: how lines and
  glyphs are antialiased, where renderers differ by taste) and in boxes of
  4×4 pixels (*content*: what is drawn).
- **Exact rendering** of the synthetic drawings (`accuracy/exact`): an
  independent interpreter for the operators they use computes the area of
  each pixel the geometry covers (exact along 64 sample rows per pixel,
  strokes as the union of segments, caps and joins, circles analytically).
  The report shows each engine's ink against it (1× exact, above heavier)
  and its error. `joins-caps` and `fills-subpixel` test every cap and join,
  the miter limit, skewed round caps, both winding rules, sub-pixel
  rectangles and an even-odd clip.

```sh
cd accuracy/reference
go run . -dir ../../testdata/corpus                         # → ../../report-reference/report.html
go run . -dir ../../testdata/borb -pages 3 -dpi 72 -sample 200
go run . -engines cera,pdfium,mupdf -match synthetic/       # a subset of engines and files
```

The charts are drawn with [figure](https://github.com/timzifer/figure)
(SVG, once with light and once with dark tokens, the page showing the one
its colour scheme asks for); the tables below them hold every value.

The references' renderings are cached in `testdata/reference-cache` by
engine version, file content, resolution and page; cera is always rendered.
MuPDF is AGPL and Ghostscript AGPL as well: they are linked into or run by
this tool only, never by cera.

## Repository setup

`main` is protected by the ruleset in `.github/rulesets/main.json`: no direct
pushes, force pushes or deletion; changes land through pull requests with a
green `ci-ok`. Apply or update it with `scripts/protect-main.sh` (GitHub CLI,
admin rights) or import the file under Settings → Rules → Rulesets.

## License

MIT
