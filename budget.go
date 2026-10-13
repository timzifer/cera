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
	maxFormDepth       = 12       // forms, Type 3 glyphs, pattern cells and soft masks nested
	maxStateDepth      = 1 << 12  // q without Q
	maxPatternDepth    = 4        // patterns painted inside pattern cells
	maxTileBytes       = 64 << 20 // tiles made for one page
	maxTileOffsets     = 64       // copies of a cell drawn into one tile
	maxTileSide        = 1024     // pixels of a tile side
	maxMasks           = 1 << 10  // soft masks drawn for one page
	maxMaskDepth       = 4        // soft masks nested in each other
	maxMeshTris        = 1 << 20  // triangles of one mesh shading
	maxPatches         = 1 << 16  // patches of one type 6 or 7 shading
	maxMeshRow         = 1 << 16  // vertices of a row of a type 5 shading
	maxAnnots          = 1 << 12  // annotations drawn for one page
	maxImageBytes      = 256 << 20
	maxAttachmentBytes = 256 << 20 // decoded bytes of an embedded file
	maxCodecPixels     = 64 << 20  // pixels a JPEG or JPEG 2000 codestream may declare
	maxKnockoutGlyphs  = 256       // glyphs of one run tested for overlap under TK
	maxCMYKProfiles    = 16        // CMYK ICC profiles tabulated for one document, ~1 MB each
	maxICCTables       = 16        // grey and RGB ICC profiles of lookup tables tabulated for one document, ~0.4 MB each
)

// Limits bound structures whose valid sizes the specification bounds or
// that no producer comes near; input past them is malformed, and what does
// not fit is ignored like other damage (Stats.Errors or the key of the
// feature that does not read).
const (
	maxComps          = 32 // components of a colour (DeviceN allows 32)
	maxFunctionDepth  = 8  // stitching functions naming functions
	maxPSStack        = 100
	maxLayerDepth     = 16       // /Order nesting and /VE expressions
	maxFieldDepth     = 32       // the AcroForm field tree
	maxInfoEntries    = 256      // entries of /Info beyond the standard ones
	maxInfoString     = 64 << 10 // bytes of one decoded /Info string
	maxXMPBytes       = 16 << 20 // bytes of the decoded XMP metadata stream
	maxOutlineDepth   = 32       // levels of the document outline
	maxOutlineItems   = 1 << 16  // entries of the document outline
	maxActions        = 16       // actions chained to that of a link, and targets of a /GoToE
	maxAttachments    = 1 << 12  // embedded files listed for a document
	maxSignatureBytes = 16 << 20 // bytes of the data of a signature
	maxCompareNodes   = 1 << 20  // objects compared to tell what changed after a signature
	maxLabelDepth     = 16       // levels of the number tree of page labels
	maxLabelNodes     = 1 << 16  // nodes of the number tree of page labels
	maxPrintRanges    = 1 << 10  // page ranges of /PrintPageRange
	maxStructNodes    = 1 << 21  // elements and content of the structure tree
	maxStructDepth    = 128      // levels of the structure tree
	maxFormDataBytes  = 64 << 20 // bytes of an FDF or XFDF file
	maxFormDataFields = 1 << 16  // fields of an FDF or XFDF file
)

// Caches bound memory kept for reuse; past them entries are dropped and
// drawing does not change. The first three are defaults: of
// OpenOptions.ImageCacheBytes and of SetPoolLimits (cache.go), as
// pdf.DefaultStreamCacheBytes is of OpenOptions.StreamCacheBytes.
// maxReplayCells only chooses how pattern cells
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
	{"maxICCTables", maxICCTables, "icc-budget", "grey and RGB ICC profiles built from lookup tables that one document converts through; spaces of further ones are the device space"},

	{"maxComps", maxComps, "", "components of a colour value; further ones are ignored"},
	{"maxFunctionDepth", maxFunctionDepth, "", "stitching functions naming functions; deeper ones do not read (shading-function, tint-transform)"},
	{"maxPSStack", maxPSStack, "", "operand stack of a PostScript calculator function; further pushes are dropped"},
	{"maxLayerDepth", maxLayerDepth, "", "/Order nesting and visibility expressions; deeper ones do not read (oc-bad)"},
	{"maxFieldDepth", maxFieldDepth, "", "depth of the AcroForm field tree; deeper fields are not read"},
	{"maxInfoEntries", maxInfoEntries, "", "entries of /Info beyond the standard ones kept in Metadata.Custom; further ones are left out"},
	{"maxInfoString", maxInfoString, "", "bytes of one decoded /Info string in Metadata; a longer one is cut"},
	{"maxXMPBytes", maxXMPBytes, "", "bytes of the decoded XMP metadata stream; a larger one is left out of Metadata"},
	{"maxOutlineDepth", maxOutlineDepth, "", "levels of the document outline; deeper entries are not read"},
	{"maxOutlineItems", maxOutlineItems, "", "entries of the document outline; further ones are not read"},
	{"maxActions", maxActions, "", "actions chained to that of a link or outline entry (/Next), and targets of a /GoToE; further ones are not read"},
	{"maxAttachments", maxAttachments, "", "embedded files listed by Document.Attachments, and nodes of their name tree; further ones are left out"},
	{"maxAttachmentBytes", maxAttachmentBytes, "", "decoded bytes of an embedded file; Attachment.Open returns ErrAttachmentTooLarge for more"},
	{"maxSignatureBytes", maxSignatureBytes, "", "bytes of the data of a signature (/Contents); a larger one is not checked"},
	{"maxCompareNodes", maxCompareNodes, "", "objects compared to tell what was appended after a signature; past it the changes count as Other"},
	{"maxLabelDepth", maxLabelDepth, "", "levels of the number tree of page labels; deeper nodes are not read"},
	{"maxLabelNodes", maxLabelNodes, "", "nodes of the number tree of page labels; further ones are not read"},
	{"maxPrintRanges", maxPrintRanges, "", "page ranges of /PrintPageRange in ViewSettings; further ones are left out"},
	{"maxStructNodes", maxStructNodes, "", "elements, content items and attribute objects of the structure tree, read for Document.Structure and for the reading order of text; further ones are not read (StructTree.Truncated)"},
	{"maxStructDepth", maxStructDepth, "", "levels of the structure tree in Document.Structure, and types a role map leads through; deeper elements are not read (StructTree.Truncated)"},
	{"maxFormDataBytes", maxFormDataBytes, "", "bytes of the FDF or XFDF data FormState.ImportFDF and ImportXFDF read; more is an error"},
	{"maxFormDataFields", maxFormDataFields, "", "fields with a value read from FDF or XFDF data; further ones are left out"},
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
	{"pdf.MaxObjectWindow", pdf.MaxObjectWindow, "", "bytes read to parse one object of a file opened with OpenReaderAt; a larger object is not read"},

	{"imageCacheBytes", imageCacheBytes, "", "decoded images a document keeps, unless OpenOptions.ImageCacheBytes or a Cache says otherwise"},
	{"pdf.DefaultStreamCacheBytes", pdf.DefaultStreamCacheBytes, "", "decoded streams (content, forms, Type 3 glyphs, functions) a document keeps, unless OpenOptions.StreamCacheBytes or a Cache says otherwise"},
	{"pdf.MaxCachedStream", pdf.MaxCachedStream, "", "largest decoded stream kept; larger ones are decoded on every use"},
	{"maxFreeLayerBytes", maxFreeLayerBytes, "", "transparency layer buffers a device keeps, unless SetPoolLimits says otherwise"},
	{"maxIdleGlyphCaches", maxIdleGlyphCaches, "", "glyph mask caches kept between renders, unless SetPoolLimits says otherwise"},
	{"maxMeshShaders", maxMeshShaders, "", "mesh shaders kept per mesh"},
	{"maxReplayCells", maxReplayCells, "", "pattern cells drawn as vector operations instead of a tile"},
	{"maxRunPoints", maxRunPoints, "", "points of consecutive strokes or fills of one pen drawn as one union; past it, the next union starts"},
}
