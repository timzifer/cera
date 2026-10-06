# Architecture

cera has its own interpreter feeding a device interface and a display list:
content is parsed once per page, only the visible region is drawn, and all
cores are used. The rest comes from libraries:

| layer | source |
|---|---|
| file reader: xref, objects, filters, encryption, repair, decoded-stream cache | cera (`internal/pdf`), ported from [go-pdfkit/reader](https://github.com/go-pdfkit/reader) (BSD-3) onto its own object model ([ADR 0012](adr/0012-own-pdf-reader.md)); Brotli through [andybalholm/brotli](https://github.com/andybalholm/brotli) (MIT) |
| content scanner, interpreter, graphics state | cera |
| display list, bands, workers | cera |
| rasterizer, stroker, clip, compositing; shaders for images, glyph masks, layers, blend modes, gradients, meshes | [timzifer/stilus](https://github.com/timzifer/stilus) (MIT) |
| fonts: PDF side (encodings, widths, ToUnicode) | cera (`internal/pdffont`); glyph-name tables and ToUnicode reader from [go-pdfkit/pdffont](https://github.com/go-pdfkit/pdffont) (BSD-3) |
| fonts: programs (TrueType, CFF, Type 1) | [go-opentype/opentype](https://github.com/go-opentype/opentype); FDArray matrices of CID-keyed CFF in cera (`cffcid.go`) |
| fonts: stand-ins | [go-opentype/fonts](https://github.com/go-opentype/fonts): TeX Gyre Heros, Termes, Cursor (GUST Font License, `internal/stdfont`); Arimo, Tinos, Cousine, DejaVu Sans subsets, Noto Sans JP/SC/KR (`fonts/cjk`) |
| fonts: CMaps, vertical writing, font providers | cera; predefined CMaps from [Adobe's CMap resources](https://github.com/adobe-type-tools/cmap-resources) (BSD-3) |
| text: glyph selection, glyph cache, Type 3, render modes | cera |
| images: samples, masks, mip levels, image cache | cera; codecs [go-images/jpeg](https://github.com/go-images/jpeg) (BSD-3), [go-images/jpeg2000](https://github.com/go-images/jpeg2000), [gobig2](https://github.com/tannevaled/gobig2) (Apache-2.0) |
| transparency: groups, knockout, blend modes, soft masks | cera |
| shadings, colour spaces, PDF functions | cera, drawn by stilus; parts ported from [timzifer/render](https://github.com/timzifer/render) (BSD-3) |

## File reader

`internal/pdf` reads the file (ADR 0012). An object is a 24-byte value, not
an interface: numbers, booleans and references inline, strings, names,
arrays and dictionaries pointing at their data. An indirect object is laid
out in one slab per kind, names that files use often are shared strings,
other names and plain strings point into the file. Dictionaries are entry
lists, searched in order up to 12 keys and by halving above.

Each object is parsed once per cross-reference table and published with a
compare-and-swap: concurrent readers never wait on each other, and a
repair builds a new table instead of resetting the one others read. Decoded
streams (content, forms, Type 3 glyphs, functions) are kept in a bounded
cache that decodes each stream once however many pages ask for it; images
and font programs bypass it, their consumers keep what they make of them.
So pages of one document render concurrently, and looking up an object
already loaded allocates nothing.

`internal/pdffont` reads the PDF side of fonts on top of it. `readerdiff/`,
a module of its own, compares both with the go-pdfkit packages they
replace, object by object, stream by stream and font code by font code.

## Display list and bands

The first render of a page at a scale interprets its content into a display
list: every path with its transform, paint and device-space box (clipped by
the clips around it), and a band index of about 16 horizontal bands. Later
renders at that scale (other tiles, a scrolled viewport, another thread)
skip parsing and draw only the items that touch their region. One worker
draws the region in one pass; several take bands from a shared counter, each
with its own pooled stilus canvas, so steady-state rendering allocates
nothing per page but its deadline. Content under an empty clip and paths
outside the page are dropped while recording.

## Text

Text is recorded as runs of glyphs: each glyph's outline (read once per
font, in em units) and its device matrix. A raster worker rasterizes a
glyph once per size (to 1/64 pixel per em) and quarter-pixel position
into a coverage mask and composites it from then on, through the clip
stack like any fill; glyphs larger than 160 pixels per em are filled as
paths. Each worker keeps its own cache (4 MB), so drawing takes no locks.

## Images

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

## Transparency

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

## Shadings and patterns

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

## Colour

Colours are converted to sRGB when they are read: Separation and DeviceN
through their tint transforms into the alternate space (a one-ink
transform is tabulated at 256 tints, so a spot-colour image costs a
palette), Lab, CalGray, CalRGB and ICC profiles of the matrix/TRC kind
through CIE XYZ, adapted to D50 with the Bradford transform. An ICC
profile close to sRGB is taken as the device space; RGB images in another
profile convert through tables per channel. DeviceCMYK converts through a
SWOP press profile (colord's profile of CGATS TR 005, NPES, the
characterization data's source; `internal/cmyk`), as a press prints it,
or through the caller's (`OpenOptions.CMYKProfile`), or with
`OpenOptions.NaiveCMYK` naively, as device values. An ICCBased CMYK space
converts through its own profile. `internal/cmyk` reads a profile's
lut8, lut16 or lutAtoB tables, converts as Little CMS does (relative
colorimetric, black point compensation) and tabulates the result once
per profile as linear sRGB at 17⁴ nodes, shared by content between
documents while any holds it.
