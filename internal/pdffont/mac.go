package pdffont

// MacRomanEncoding is the encoding of the Mac OS Roman character set, as
// PDF 32000-2 Annex D names its glyphs: code 39 is the straight quote
// (quotesingle) and 96 the grave, unlike StandardEncoding; above 127 it is
// the Mac's own order of accented letters and symbols. Code 202 is the
// no-break space and 219 the currency sign, as the specification has them.
var MacRomanEncoding = func() [256]string {
	var t [256]string
	copy(t[32:127], WinAnsiEncoding[32:127])
	copy(t[128:], []string{
		"Adieresis", "Aring", "Ccedilla", "Eacute", "Ntilde", "Odieresis", "Udieresis", "aacute",
		"agrave", "acircumflex", "adieresis", "atilde", "aring", "ccedilla", "eacute", "egrave",
		"ecircumflex", "edieresis", "iacute", "igrave", "icircumflex", "idieresis", "ntilde", "oacute",
		"ograve", "ocircumflex", "odieresis", "otilde", "uacute", "ugrave", "ucircumflex", "udieresis",
		"dagger", "degree", "cent", "sterling", "section", "bullet", "paragraph", "germandbls",
		"registered", "copyright", "trademark", "acute", "dieresis", "notequal", "AE", "Oslash",
		"infinity", "plusminus", "lessequal", "greaterequal", "yen", "mu", "partialdiff", "summation",
		"product", "pi", "integral", "ordfeminine", "ordmasculine", "Omega", "ae", "oslash",
		"questiondown", "exclamdown", "logicalnot", "radical", "florin", "approxequal", "Delta", "guillemotleft",
		"guillemotright", "ellipsis", "space", "Agrave", "Atilde", "Otilde", "OE", "oe",
		"endash", "emdash", "quotedblleft", "quotedblright", "quoteleft", "quoteright", "divide", "lozenge",
		"ydieresis", "Ydieresis", "fraction", "currency", "guilsinglleft", "guilsinglright", "fi", "fl",
		"daggerdbl", "periodcentered", "quotesinglbase", "quotedblbase", "perthousand", "Acircumflex", "Ecircumflex", "Aacute",
		"Edieresis", "Egrave", "Iacute", "Icircumflex", "Idieresis", "Igrave", "Oacute", "Ocircumflex",
		"apple", "Ograve", "Uacute", "Ucircumflex", "Ugrave", "dotlessi", "circumflex", "tilde",
		"macron", "breve", "dotaccent", "ring", "cedilla", "hungarumlaut", "ogonek", "caron",
	})
	return t
}()

// MacExpertEncoding is the encoding of Adobe's expert character sets — old
// style figures, small capitals, superiors and inferiors, fractions — as
// PDF 32000-2 Annex D gives it.
var MacExpertEncoding = func() [256]string {
	var t [256]string
	copy(t[32:], []string{
		"space", "exclamsmall", "Hungarumlautsmall", "centoldstyle", "dollaroldstyle", "dollarsuperior", "ampersandsmall", "Acutesmall",
		"parenleftsuperior", "parenrightsuperior", "twodotenleader", "onedotenleader", "comma", "hyphen", "period", "fraction",
		"zerooldstyle", "oneoldstyle", "twooldstyle", "threeoldstyle", "fouroldstyle", "fiveoldstyle", "sixoldstyle", "sevenoldstyle",
		"eightoldstyle", "nineoldstyle", "colon", "semicolon", "", "threequartersemdash", "", "questionsmall",
		"", "", "", "", "Ethsmall", "", "", "onequarter",
		"onehalf", "threequarters", "oneeighth", "threeeighths", "fiveeighths", "seveneighths", "onethird", "twothirds",
		"", "", "", "", "", "", "ff", "fi",
		"fl", "ffi", "ffl", "parenleftinferior", "", "parenrightinferior", "Circumflexsmall", "hypheninferior",
		"Gravesmall", "Asmall", "Bsmall", "Csmall", "Dsmall", "Esmall", "Fsmall", "Gsmall",
		"Hsmall", "Ismall", "Jsmall", "Ksmall", "Lsmall", "Msmall", "Nsmall", "Osmall",
		"Psmall", "Qsmall", "Rsmall", "Ssmall", "Tsmall", "Usmall", "Vsmall", "Wsmall",
		"Xsmall", "Ysmall", "Zsmall", "colonmonetary", "onefitted", "rupiah", "Tildesmall", "",
		"", "asuperior", "centsuperior", "", "", "", "", "Aacutesmall",
		"Agravesmall", "Acircumflexsmall", "Adieresissmall", "Atildesmall", "Aringsmall", "Ccedillasmall", "Eacutesmall", "Egravesmall",
		"Ecircumflexsmall", "Edieresissmall", "Iacutesmall", "Igravesmall", "Icircumflexsmall", "Idieresissmall", "Ntildesmall", "Oacutesmall",
		"Ogravesmall", "Ocircumflexsmall", "Odieresissmall", "Otildesmall", "Uacutesmall", "Ugravesmall", "Ucircumflexsmall", "Udieresissmall",
		"", "eightsuperior", "fourinferior", "threeinferior", "sixinferior", "eightinferior", "seveninferior", "Scaronsmall",
		"", "centinferior", "twoinferior", "", "Dieresissmall", "", "Caronsmall", "osuperior",
		"fiveinferior", "", "commainferior", "periodinferior", "Yacutesmall", "", "dollarinferior", "",
		"", "Thornsmall", "", "nineinferior", "zeroinferior", "Zcaronsmall", "AEsmall", "Oslashsmall",
		"questiondownsmall", "oneinferior", "Lslashsmall", "", "", "", "", "",
		"", "Cedillasmall", "", "", "", "", "", "OEsmall",
		"figuredash", "hyphensuperior", "", "", "", "", "exclamdownsmall", "",
		"Ydieresissmall", "", "onesuperior", "twosuperior", "threesuperior", "foursuperior", "fivesuperior", "sixsuperior",
		"sevensuperior", "ninesuperior", "zerosuperior", "", "esuperior", "rsuperior", "tsuperior", "",
		"", "isuperior", "ssuperior", "dsuperior", "", "", "", "",
		"", "lsuperior", "Ogoneksmall", "Brevesmall", "Macronsmall", "bsuperior", "nsuperior", "msuperior",
		"commasuperior", "periodsuperior", "Dotaccentsmall", "Ringsmall", "", "", "", "",
	})
	return t
}()
