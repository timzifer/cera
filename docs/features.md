# Features

What cera draws today, and what it counts as unsupported.

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

Fonts a file does not embed:

| font asked for | drawn with |
|---|---|
| Helvetica, Times, Courier and frequent families (Calibri, Verdana, Cambria …) | TeX Gyre Heros, Termes, Cursor (the URW Nimbus shapes PDFium and MuPDF draw too, metric-compatible), in the weight, slope and width asked for; Arimo, Tinos, Cousine for text TeX Gyre has no glyphs for (Cyrillic), and for all of it when built with the tag `cera_nogyre` (1.45 MB smaller, see [usage](usage.md)) |
| sans serif monospaces (Consolas, Menlo, Lucida Console …) | Cousine |
| Symbol, ZapfDingbats | DejaVu Sans subsets at AFM widths |
| Adobe CJK collections | Noto Sans through `fonts/cjk` (opt-in, see [usage](usage.md)); reads back as Unicode without ToUnicode |
| anything else | a `FontProvider` of your own (`cera.OpenWith`) |

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
| DeviceCMYK | through a SWOP press profile, or the caller's (`OpenOptions.CMYKProfile`); `OpenOptions.NaiveCMYK` for raw device values |
| CalGray, CalRGB, Lab | through CIE XYZ, D50 (Bradford) |
| ICCBased | grey and RGB matrix/TRC profiles; CMYK profiles of lut8, lut16 or lutAtoB tables (Lab or XYZ); others by channel count |
| Separation, DeviceN | tint transforms; `/None` paints nothing |
| Indexed | on any of the above |

### Transparency and graphics state

- Groups: isolated, non-isolated, knockout
- All 16 blend modes
- Soft masks: luminosity (with backdrop colour) and alpha, transfer
  functions of all four types
- Constant alpha (`CA`, `ca`), `/Font` in `gs`, transfer functions
  (`TR`, `TR2`) on solid colours
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

- Drawn from normal appearance streams (`/AS`, `/CA`, `NoZoom`, `NoRotate`, `/OC`)
- Appearance generated when missing: Square, Circle, Line (endings, leader
  lines), PolyLine, Polygon, Ink, Highlight, Underline, StrikeOut, Squiggly
- `Page.Annotations` with link targets

### Forms

- AcroForm fields with inheritance; values in a caller-owned `FormState`
- Appearances generated for text fields (single line, multiline, comb,
  password, auto-size), check boxes, radio buttons, combo and list boxes,
  push buttons
- Native widgets through `FormWidgetProvider` and `FormLayer` (tab order,
  focus); fyne provider in [`form/fyneform`](../form/fyneform)
- `cmd/cera -field name=value` fills a form
- Not run: JavaScript. XFA: drawn from the AcroForm fallback, counted as `xfa`

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
| `icc-lut` | CMYK ICC profile cera cannot read | through DeviceCMYK's profile |
| – | grey and RGB ICC profiles built from lookup tables | as device space of as many components |

### Transparency and graphics state

| key | case | drawn |
|---|---|---|
| `blend-mode` | unknown blend mode | as Normal |
| `non-isolated-blend` | non-isolated group with blend modes inside, object of a knockout group | isolated |
| `alpha-is-shape` | `/AIS` | – |
| `smask-transfer` | soft-mask transfer function that does not read | – |
| `overprint` | overprint not simulated | painted over |
| `transfer` | transfer functions on images, shadings, patterns, or that do not read | without |

### Content and annotations

| key | case | drawn |
|---|---|---|
| `pattern` | pattern colour on an operation that cannot paint patterns | not drawn |
| `oc-bad` | optional-content membership that does not read | visible |
| `annot-no-ap` | annotation without appearance cera does not generate (FreeText, Text, Stamp …) | not drawn |
| `xfa` | XFA form | from AcroForm fallback |

### Budgets

Inputs past a [budget](budgets.md) are drawn with less and counted:
`nesting-budget`, `pattern-budget`, `smask-budget`, `mesh-budget`,
`annot-budget`, `image-too-large`, `icc-budget`.
