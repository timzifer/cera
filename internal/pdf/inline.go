// Ported from github.com/go-pdfkit/reader v0.6.0 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/reader authors); see LICENSE-go-pdfkit.

package pdf

// inlineKeys maps the abbreviations an inline image dictionary uses to the
// names their long form would have.
var inlineKeys = map[Name]Name{
	"BPC": "BitsPerComponent",
	"CS":  "ColorSpace",
	"D":   "Decode",
	"DP":  "DecodeParms",
	"F":   "Filter",
	"H":   "Height",
	"IM":  "ImageMask",
	"I":   "Interpolate",
	"W":   "Width",
	"L":   "Length",
}

// inlineColourSpaces maps the abbreviated colour space names.
var inlineColourSpaces = map[Name]Name{
	"G":    "DeviceGray",
	"RGB":  "DeviceRGB",
	"CMYK": "DeviceCMYK",
	"I":    "Indexed",
}

// ExpandInline returns an inline image's dictionary with the abbreviated
// keys and colour space names written out, so it reads like an image
// XObject's. Where a dictionary gives a key both ways, the abbreviation
// wins: it is the spelling the specification gives for inline images.
func ExpandInline(d Dict) Dict {
	out := make([]Entry, 0, d.Len())
	for _, e := range d.e {
		if _, abbreviated := inlineKeys[e.Key]; !abbreviated {
			out = append(out, Entry{e.Key, expandColourSpace(e.Key, e.Val)})
		}
	}
	// Appended after the written-out names, the abbreviations win where
	// NewDict meets a key twice.
	for _, e := range d.e {
		if long, abbreviated := inlineKeys[e.Key]; abbreviated {
			out = append(out, Entry{long, expandColourSpace(long, e.Val)})
		}
	}
	return NewDict(out...)
}

func expandColourSpace(k Name, v Object) Object {
	if k != "ColorSpace" {
		return v
	}
	if n, ok := v.Name(); ok {
		if long, ok := inlineColourSpaces[n]; ok {
			return long.Object()
		}
	}
	return v
}
