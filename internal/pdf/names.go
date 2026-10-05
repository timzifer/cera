package pdf

// wellKnown maps the names files use most to one shared string each, so a
// parsed name costs nothing and compares with a string literal of the same
// text by pointer: the linker gives equal string literals one address.
var wellKnown = func() map[string]Name {
	m := make(map[string]Name, len(wellKnownNames))
	for _, n := range wellKnownNames {
		m[string(n)] = n
	}
	return m
}()

// intern returns the shared name for b, if it is a well-known one. The map
// lookup with string(b) does not allocate.
func intern(b []byte) (Name, bool) {
	n, ok := wellKnown[string(b)]
	return n, ok
}

var wellKnownNames = [...]Name{
	// Document structure.
	"Type", "Subtype", "Catalog", "Pages", "Page", "Kids", "Parent", "Count",
	"Root", "Info", "ID", "Size", "Prev", "XRefStm", "Encrypt", "XRef",
	"ObjStm", "N", "First", "Extends", "W", "Index", "Length", "Filter",
	"DecodeParms", "DL", "F", "FFilter", "FDecodeParms", "Version",
	"Metadata", "Names", "Dests", "Outlines", "OpenAction", "AcroForm",
	"OCProperties", "OCGs", "OCG", "OCMD", "OC", "D", "ON", "OFF", "Order",
	"BaseState", "AS", "Usage", "Intent", "View", "Design", "Print",
	"PageLabels", "StructTreeRoot", "MarkInfo", "Lang", "ViewerPreferences",
	"PageMode", "PageLayout", "Threads", "URI", "Collection", "Perms",
	"Legal", "Requirements", "AF", "DSS",
	// Pages.
	"Resources", "MediaBox", "CropBox", "BleedBox", "TrimBox", "ArtBox",
	"Rotate", "Contents", "Annots", "Group", "Thumb", "B", "Dur", "Trans",
	"UserUnit", "VP", "Tabs", "StructParents", "PieceInfo", "LastModified",
	// Resources.
	"ExtGState", "ColorSpace", "Pattern", "Shading", "XObject", "Font",
	"ProcSet", "Properties", "PDF", "Text", "ImageB", "ImageC", "ImageI",
	// Filters.
	"FlateDecode", "Fl", "LZWDecode", "LZW", "ASCIIHexDecode", "AHx",
	"ASCII85Decode", "A85", "RunLengthDecode", "RL", "CCITTFaxDecode", "CCF",
	"DCTDecode", "DCT", "JPXDecode", "JBIG2Decode", "Crypt", "Predictor",
	"Colors", "BitsPerComponent", "Columns", "EarlyChange", "K", "EncodedByteAlign",
	"Rows", "EndOfLine", "EndOfBlock", "BlackIs1", "DamagedRowsBeforeError",
	"JBIG2Globals", "ColorTransform", "Name", "Identity",
	// Images and XObjects.
	"Image", "Form", "PS", "Width", "Height", "ImageMask", "Mask", "SMask",
	"SMaskInData", "Decode", "Interpolate", "Alternates", "Matte", "BBox",
	"Matrix", "Ref", "OPI", "StructParent", "Measure", "PtData",
	// Colour spaces.
	"DeviceGray", "DeviceRGB", "DeviceCMYK", "CalGray", "CalRGB", "Lab",
	"ICCBased", "Indexed", "Separation", "DeviceN", "All", "None",
	"WhitePoint", "BlackPoint", "Gamma", "Range", "Alternate", "Process",
	"Components", "Colorants", "MixingHints", "Attributes", "NChannel",
	// Graphics state.
	"LW", "LC", "LJ", "ML", "RI", "OP", "op", "OPM", "BG", "BG2", "UCR",
	"UCR2", "TR", "TR2", "HT", "FL", "SM", "SA", "BM", "CA", "ca", "AIS",
	"TK", "Normal", "Multiply", "Screen", "Overlay", "Darken", "Lighten",
	"ColorDodge", "ColorBurn", "HardLight", "SoftLight", "Difference",
	"Exclusion", "Hue", "Saturation", "Color", "Luminosity", "Compatible",
	"Alpha", "G", "BC", "TR", "I", "CS",
	// Shadings, patterns and functions.
	"ShadingType", "PatternType", "PaintType", "TilingType", "XStep",
	"YStep", "Coords", "Domain", "Function", "Functions", "Extend",
	"Background", "AntiAlias", "FunctionType", "C0", "C1", "Bounds",
	"Encode", "Size", "BitsPerSample", "Order", "BitsPerCoordinate",
	"BitsPerFlag", "VerticesPerRow",
	// Fonts.
	"Type0", "Type1", "MMType1", "Type3", "TrueType", "CIDFontType0",
	"CIDFontType2", "BaseFont", "FirstChar", "LastChar", "Widths",
	"FontDescriptor", "Encoding", "ToUnicode", "DescendantFonts",
	"CIDSystemInfo", "CIDToGIDMap", "DW", "DW2", "W2", "Registry",
	"Ordering", "Supplement", "FontName", "FontFamily", "FontStretch",
	"FontWeight", "Flags", "FontBBox", "ItalicAngle", "Ascent", "Descent",
	"Leading", "CapHeight", "XHeight", "StemV", "StemH", "AvgWidth",
	"MaxWidth", "MissingWidth", "FontFile", "FontFile2", "FontFile3",
	"CharSet", "CIDSet", "Style", "FontMatrix", "CharProcs", "Differences",
	"BaseEncoding", "WinAnsiEncoding", "MacRomanEncoding",
	"MacExpertEncoding", "StandardEncoding", "Identity-H", "Identity-V",
	"Type1C", "CIDFontType0C", "OpenType", "UseCMap", "CMapName",
	"Length1", "Length2", "Length3", "FontFile",
	// Annotations and forms.
	"Annot", "Link", "Widget", "Popup", "FreeText", "Line", "Square",
	"Circle", "Polygon", "PolyLine", "Highlight", "Underline", "Squiggly",
	"StrikeOut", "Stamp", "Caret", "Ink", "FileAttachment", "Sound",
	"Movie", "Screen", "PrinterMark", "TrapNet", "Watermark", "3D",
	"Redact", "Rect", "AP", "R", "MK", "DA", "DR", "DV", "V", "T", "TU",
	"TM", "Ff", "FT", "Btn", "Tx", "Ch", "Sig", "Fields", "NeedAppearances",
	"XFA", "Q", "Opt", "TI", "MaxLen", "QuadPoints", "Border", "BS", "BE",
	"IC", "LE", "RD", "NM", "M", "P", "A", "S", "Dest", "GoTo", "Action",
	"Next", "Off", "Yes", "Rotate",
	// Encryption.
	"Standard", "V", "O", "U", "OE", "UE", "CF", "StmF", "StrF", "EFF",
	"CFM", "V2", "AESV2", "AESV3", "EncryptMetadata", "AuthEvent",
	"DocOpen", "Recipients",
	// Inline image abbreviations.
	"BPC", "DP", "H", "IM", "L", "RGB", "CMYK",
}
