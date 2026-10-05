package cera

import (
	"github.com/timzifer/cera/internal/cmap"
	"github.com/timzifer/cera/internal/content"
	"github.com/timzifer/cera/internal/pdf"
)

// Budgets bound the work and memory one input may cost (ADR 0010). Valid
// input past a budget is drawn with less – a mesh with fewer triangles, a
// pattern without its innermost cells, a soft mask empty – never failed,
// and every time a budget is reached its key is counted in
// Stats.Unsupported so the corpus reports show which inputs hit them.
const (
	maxFormDepth      = 12       // forms, Type 3 glyphs, pattern cells and soft masks nested
	maxStateDepth     = 1 << 12  // q without Q
	maxPatternDepth   = 4        // patterns painted inside pattern cells
	maxTileBytes      = 64 << 20 // tiles made for one page
	maxTileOffsets    = 64       // copies of a cell drawn into one tile
	maxTileSide       = 1024     // pixels of a tile side
	maxMasks          = 1 << 10  // soft masks drawn for one page
	maxMaskDepth      = 4        // soft masks nested in each other
	maxMeshTris       = 1 << 20  // triangles of one mesh shading
	maxPatches        = 1 << 16  // patches of one type 6 or 7 shading
	maxMeshRow        = 1 << 16  // vertices of a row of a type 5 shading
	maxAnnots         = 1 << 12  // annotations drawn for one page
	maxImageBytes     = 256 << 20
	maxCodecPixels    = 64 << 20 // pixels a JPEG or JPEG 2000 codestream may declare
	maxKnockoutGlyphs = 256      // glyphs of one run tested for overlap under TK
	maxCMYKProfiles   = 16       // CMYK ICC profiles tabulated for one document, ~1 MB each
)

// Limits bound structures whose valid sizes the specification bounds or
// that no producer comes near; input past them is malformed, and what does
// not fit is ignored like other damage (Stats.Errors or the key of the
// feature that does not read).
const (
	maxComps         = 32 // components of a colour (DeviceN allows 32)
	maxFunctionDepth = 8  // stitching functions naming functions
	maxPSStack       = 100
	maxLayerDepth    = 16 // /Order nesting and /VE expressions
	maxFieldDepth    = 32 // the AcroForm field tree
)

// Caches bound memory kept for reuse; past them entries are dropped and
// drawing does not change. maxReplayCells only chooses how pattern cells
// are drawn, maxRunPoints how many strokes or fills of one pen are drawn
// as one union (displaylist.go); past it, the next run starts.
const (
	imageCacheBytes    = 256 << 20 // decoded images a document keeps
	maxFreeLayerBytes  = 64 << 20  // layer buffers a device keeps
	maxIdleGlyphCaches = 64
	maxMeshShaders     = 8 // shaders kept per mesh; beyond, each draw sets up its own
	maxReplayCells     = 64
	maxRunPoints       = 1 << 12 // points of strokes or fills drawn as one union
)

// budget is a row of the table of budgets, limits and caches documented in
// docs/budgets.md.
type budget struct {
	name  string // the constant
	limit int
	key   string // counted in Stats.Unsupported; empty for limits and caches
	what  string
}

// budgets lists every bound of the renderer. A test checks that docs/budgets.md
// documents each of them with its value and key.
var budgets = []budget{
	{"maxFormDepth", maxFormDepth, "nesting-budget", "forms, Type 3 glyphs, pattern cells and soft masks nested in each other; deeper ones are not drawn"},
	{"maxStateDepth", maxStateDepth, "nesting-budget", "graphics states saved by q; a q past it is ignored"},
	{"maxPatternDepth", maxPatternDepth, "pattern-budget", "tiling patterns painted inside pattern cells; deeper ones are not drawn"},
	{"maxTileBytes", maxTileBytes, "pattern-budget", "bytes of pattern tiles made for one page; further patterns are not drawn"},
	{"maxTileOffsets", maxTileOffsets, "pattern-budget", "copies of a cell drawn into one tile; only the first 8×8 are drawn"},
	{"maxTileSide", maxTileSide, "pattern-budget", "pixels of a tile side; larger cells, if more than maxReplayCells, are drawn at this resolution"},
	{"maxMasks", maxMasks, "smask-budget", "soft masks drawn for one page; further masks are empty"},
	{"maxMaskDepth", maxMaskDepth, "smask-budget", "soft masks nested in each other; deeper masks are empty"},
	{"maxMeshTris", maxMeshTris, "mesh-budget", "triangles of one mesh shading; the rest of the mesh is not read"},
	{"maxPatches", maxPatches, "mesh-budget", "patches of one type 6 or 7 shading; the rest is not read"},
	{"maxMeshRow", maxMeshRow, "mesh-budget", "vertices per row of a type 5 shading; a longer row is not drawn"},
	{"maxAnnots", maxAnnots, "annot-budget", "annotations drawn for one page; the rest are not drawn"},
	{"maxImageBytes", maxImageBytes, "image-too-large", "bytes of one decoded image plane; a larger image is not drawn"},
	{"maxCodecPixels", maxCodecPixels, "image-too-large", "pixels a JPEG or JPEG 2000 codestream may declare; a larger image is not drawn"},
	{"maxKnockoutGlyphs", maxKnockoutGlyphs, "text-knockout", "glyphs of one run tested for overlap under text knockout; a longer run is taken to overlap"},
	{"maxCMYKProfiles", maxCMYKProfiles, "icc-budget", "distinct CMYK ICC profiles one document converts through; spaces of further ones convert through DeviceCMYK's"},

	{"maxComps", maxComps, "", "components of a colour value; further ones are ignored"},
	{"maxFunctionDepth", maxFunctionDepth, "", "stitching functions naming functions; deeper ones do not read (shading-function, tint-transform)"},
	{"maxPSStack", maxPSStack, "", "operand stack of a PostScript calculator function; further pushes are dropped"},
	{"maxLayerDepth", maxLayerDepth, "", "/Order nesting and visibility expressions; deeper ones do not read (oc-bad)"},
	{"maxFieldDepth", maxFieldDepth, "", "depth of the AcroForm field tree; deeper fields are not read"},
	{"content.MaxNesting", content.MaxNesting, "", "arrays and dictionaries nested in one operand (Stats.Errors)"},
	{"content.MaxOperands", content.MaxOperands, "", "operands kept for one operator; the first are dropped"},
	{"cmap.MaxSpans", cmap.MaxSpans, "", "code ranges of one CMap; further ones are ignored"},
	{"cmap.MaxSpaces", cmap.MaxSpaces, "", "codespace ranges of one CMap"},
	{"cmap.MaxDepth", cmap.MaxDepth, "", "usecmap nesting"},
	{"pdf.MaxStreamBytes", pdf.MaxStreamBytes, "", "bytes one filter may decode a stream to; the stream is cut there (Stats.Errors)"},
	{"pdf.MaxFilters", pdf.MaxFilters, "", "filters of one stream's chain; a longer chain does not decode"},
	{"pdf.MaxPredictorRow", pdf.MaxPredictorRow, "", "bytes of one PNG or TIFF predictor row; a longer row does not decode"},
	{"pdf.MaxNesting", pdf.MaxNesting, "", "arrays and dictionaries nested in one object of the file; a deeper object is null"},
	{"pdf.MaxRefChain", pdf.MaxRefChain, "", "objects whose loading needs another (references to references, indirect stream lengths); deeper ones are null"},
	{"pdf.MaxXrefSections", pdf.MaxXrefSections, "", "cross-reference sections of one /Prev chain; older ones are not read"},
	{"pdf.MaxObjects", pdf.MaxObjects, "", "cross-reference entries of one file; past it the file is rebuilt by scanning"},
	{"pdf.MaxPageTreeDepth", pdf.MaxPageTreeDepth, "", "page tree nodes nested in each other; deeper pages are not found"},

	{"imageCacheBytes", imageCacheBytes, "", "decoded images a document keeps"},
	{"pdf.DefaultStreamCacheBytes", pdf.DefaultStreamCacheBytes, "", "decoded streams (content, forms, Type 3 glyphs, functions) a document keeps"},
	{"pdf.MaxCachedStream", pdf.MaxCachedStream, "", "largest decoded stream kept; larger ones are decoded on every use"},
	{"maxFreeLayerBytes", maxFreeLayerBytes, "", "transparency layer buffers a device keeps"},
	{"maxIdleGlyphCaches", maxIdleGlyphCaches, "", "glyph mask caches kept between renders"},
	{"maxMeshShaders", maxMeshShaders, "", "mesh shaders kept per mesh"},
	{"maxReplayCells", maxReplayCells, "", "pattern cells drawn as vector operations instead of a tile"},
	{"maxRunPoints", maxRunPoints, "", "points of consecutive strokes or fills of one pen drawn as one union; past it, the next union starts"},
}
