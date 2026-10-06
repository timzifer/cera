# Budgets

Every bound on the work or memory one input may cost is in
[`budget.go`](../budget.go) (ADR 0010). Budgets bound valid input that would
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
| `maxCMYKProfiles` | 16 | `icc-budget` | distinct CMYK ICC profiles one document converts through; spaces of further ones convert through DeviceCMYK's |
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
| `pdf.MaxStreamBytes` | 256 Mi | – | bytes one filter may decode a stream to; the stream is cut there (Stats.Errors) |
| `pdf.MaxFilters` | 8 | – | filters of one stream's chain; a longer chain does not decode |
| `pdf.MaxPredictorRow` | 16 Mi | – | bytes of one PNG or TIFF predictor row; a longer row does not decode |
| `pdf.MaxNesting` | 128 | – | arrays and dictionaries nested in one object of the file; a deeper object is null |
| `pdf.MaxRefChain` | 64 | – | objects whose loading needs another (references to references, indirect stream lengths); deeper ones are null |
| `pdf.MaxXrefSections` | 1 Ki | – | cross-reference sections of one /Prev chain; older ones are not read |
| `pdf.MaxObjects` | 8 Mi | – | cross-reference entries of one file; past it the file is rebuilt by scanning |
| `pdf.MaxPageTreeDepth` | 64 | – | page tree nodes nested in each other; deeper pages are not found |
| `pdf.MaxObjectWindow` | 64 Mi | – | bytes read to parse one object of a file opened with OpenReaderAt; a larger object is not read |
| `imageCacheBytes` | 256 Mi | – | decoded images a document keeps |
| `pdf.DefaultStreamCacheBytes` | 64 Mi | – | decoded streams (content, forms, Type 3 glyphs, functions) a document keeps |
| `pdf.MaxCachedStream` | 8 Mi | – | largest decoded stream kept; larger ones are decoded on every use |
| `maxFreeLayerBytes` | 64 Mi | – | transparency layer buffers a device keeps |
| `maxIdleGlyphCaches` | 64 | – | glyph mask caches kept between renders |
| `maxMeshShaders` | 8 | – | mesh shaders kept per mesh |
| `maxReplayCells` | 64 | – | pattern cells drawn as vector operations instead of a tile |
| `maxRunPoints` | 4 Ki | – | points of consecutive strokes or fills of one pen drawn as one union; past it, the next union starts |
