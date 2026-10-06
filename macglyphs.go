package cera

import (
	"encoding/binary"
	"strings"
	"sync"

	"github.com/go-opentype/opentype"
)

// macGlyphNames is the standard Macintosh glyph order of TrueType 'post'
// tables. A TrueType program without a cmap and without glyph names
// (post format 3) is most often a subset that kept this order, so a
// simple font's glyph names are looked up in it as a last resort, as
// pdf.js does.
var macGlyphNames = strings.Fields(`
.notdef .null nonmarkingreturn space exclam quotedbl numbersign dollar
percent ampersand quotesingle parenleft parenright asterisk plus comma
hyphen period slash zero one two three four five six seven eight nine colon
semicolon less equal greater question at A B C D E F G H I J K L M N O P Q R
S T U V W X Y Z bracketleft backslash bracketright asciicircum underscore
grave a b c d e f g h i j k l m n o p q r s t u v w x y z braceleft bar
braceright asciitilde Adieresis Aring Ccedilla Eacute Ntilde Odieresis
Udieresis aacute agrave acircumflex adieresis atilde aring ccedilla eacute
egrave ecircumflex edieresis iacute igrave icircumflex idieresis ntilde
oacute ograve ocircumflex odieresis otilde uacute ugrave ucircumflex
udieresis dagger degree cent sterling section bullet paragraph germandbls
registered copyright trademark acute dieresis notequal AE Oslash infinity
plusminus lessequal greaterequal yen mu partialdiff summation product pi
integral ordfeminine ordmasculine Omega ae oslash questiondown exclamdown
logicalnot radical florin approxequal Delta guillemotleft guillemotright
ellipsis nonbreakingspace Agrave Atilde Otilde OE oe endash emdash
quotedblleft quotedblright quoteleft quoteright divide lozenge ydieresis
Ydieresis fraction currency guilsinglleft guilsinglright fi fl daggerdbl
periodcentered quotesinglbase quotedblbase perthousand Acircumflex
Ecircumflex Aacute Edieresis Egrave Iacute Icircumflex Idieresis Igrave
Oacute Ocircumflex apple Ograve Uacute Ucircumflex Ugrave dotlessi
circumflex tilde macron breve dotaccent ring cedilla hungarumlaut ogonek
caron Lslash lslash Scaron scaron Zcaron zcaron brokenbar Eth eth Yacute
yacute Thorn thorn minus multiply onesuperior twosuperior threesuperior
onehalf onequarter threequarters franc Gbreve gbreve Idotaccent Scedilla
scedilla Cacute cacute Ccaron ccaron dcroat`)

var macGlyphIndex = sync.OnceValue(func() map[string]int {
	m := make(map[string]int, len(macGlyphNames))
	for i, n := range macGlyphNames {
		m[n] = i
	}
	return m
})

// postGlyphNames reads the glyph names of a TrueType program's 'post'
// table in format 2 (names by index into the Macintosh order or the
// table's own Pascal strings) or 2.5 (offsets into the Macintosh order),
// which the opentype package does not read. A program without a cmap is
// addressed by these names, as PDFium, MuPDF and pdf.js do. It returns an
// empty map for other formats and for a malformed table.
func postGlyphNames(p *opentype.Font) map[string]opentype.GlyphIndex {
	names := map[string]opentype.GlyphIndex{}
	t, ok := p.Table("post")
	if !ok || len(t) < 34 {
		return names
	}
	n := int(binary.BigEndian.Uint16(t[32:]))
	n = min(n, p.NumGlyphs())
	add := func(name string, gid int) {
		if _, seen := names[name]; !seen && name != "" {
			names[name] = opentype.GlyphIndex(gid)
		}
	}
	switch binary.BigEndian.Uint32(t) {
	case 0x00020000:
		if len(t) < 34+2*n {
			return names
		}
		var own []string
		for s := t[34+2*int(binary.BigEndian.Uint16(t[32:])):]; len(s) > 0 && len(s) > int(s[0]); s = s[1+int(s[0]):] {
			own = append(own, string(s[1:1+int(s[0])]))
		}
		for gid := range n {
			i := int(binary.BigEndian.Uint16(t[34+2*gid:]))
			switch {
			case i < len(macGlyphNames):
				add(macGlyphNames[i], gid)
			case i-len(macGlyphNames) < len(own):
				add(own[i-len(macGlyphNames)], gid)
			}
		}
	case 0x00025000:
		if len(t) < 34+n {
			return names
		}
		for gid := range n {
			if i := gid + int(int8(t[34+gid])); i >= 0 && i < len(macGlyphNames) {
				add(macGlyphNames[i], gid)
			}
		}
	}
	return names
}
