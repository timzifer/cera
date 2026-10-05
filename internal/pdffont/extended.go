// Ported from github.com/go-pdfkit/pdffont v0.3.1 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/render authors); see LICENSE-go-pdfkit.

package pdffont

// The glyph names above cover the Latin every English document is set in and
// stop there. A document in Czech, Polish, Slovak, Hungarian, Turkish,
// Romanian, Latvian, Lithuanian or Maltese is set in the block that follows,
// and every one of its accented letters was being read as nothing at all —
// which is worse than it sounds, because a dropped character leaves no mark:
// "Příliš" comes back "Píliš" and looks like a word.
//
// Counted across the 118 833-file corpus: of 52.4 million glyph names, 483 382
// could not be turned into text, and the largest nameable share of those was
// this block — rcaron 650, zdotaccent 617, Ccaron 611, nacute 602, aogonek
// 602, scedilla 597, gbreve 597, and so on for a hundred more.
//
// These are the names the Adobe Glyph List gives, and the characters are not
// in doubt: unlike the subsetted fonts that call their glyphs g0 or dress a
// glyph number up as uni00000048, a name like "rcaron" says exactly one thing.
var latinExtendedRunes = map[string]rune{
	"Amacron": 'Ā', "amacron": 'ā', "Abreve": 'Ă', "abreve": 'ă',
	"Aogonek": 'Ą', "aogonek": 'ą', "Cacute": 'Ć', "cacute": 'ć',
	"Ccircumflex": 'Ĉ', "ccircumflex": 'ĉ', "Cdotaccent": 'Ċ', "cdotaccent": 'ċ',
	"Ccaron": 'Č', "ccaron": 'č', "Dcaron": 'Ď', "dcaron": 'ď',
	"Dcroat": 'Đ', "dcroat": 'đ', "Dslash": 'Đ', "dmacron": 'đ',
	"Emacron": 'Ē', "emacron": 'ē', "Ebreve": 'Ĕ', "ebreve": 'ĕ',
	"Edotaccent": 'Ė', "edotaccent": 'ė', "Eogonek": 'Ę', "eogonek": 'ę',
	"Ecaron": 'Ě', "ecaron": 'ě', "Gcircumflex": 'Ĝ', "gcircumflex": 'ĝ',
	"Gbreve": 'Ğ', "gbreve": 'ğ', "Gdotaccent": 'Ġ', "gdotaccent": 'ġ',
	"Gcommaaccent": 'Ģ', "gcommaaccent": 'ģ',
	"Hcircumflex": 'Ĥ', "hcircumflex": 'ĥ', "Hbar": 'Ħ', "hbar": 'ħ',
	"Itilde": 'Ĩ', "itilde": 'ĩ', "Imacron": 'Ī', "imacron": 'ī',
	"Ibreve": 'Ĭ', "ibreve": 'ĭ', "Iogonek": 'Į', "iogonek": 'į',
	"Idotaccent": 'İ', "dotlessi": 'ı', "IJ": 'Ĳ', "ij": 'ĳ',
	"Jcircumflex": 'Ĵ', "jcircumflex": 'ĵ',
	"Kcommaaccent": 'Ķ', "kcommaaccent": 'ķ', "kgreenlandic": 'ĸ',
	"Lacute": 'Ĺ', "lacute": 'ĺ', "Lcommaaccent": 'Ļ', "lcommaaccent": 'ļ',
	"Lcaron": 'Ľ', "lcaron": 'ľ', "Ldot": 'Ŀ', "ldot": 'ŀ',
	"Nacute": 'Ń', "nacute": 'ń', "Ncommaaccent": 'Ņ', "ncommaaccent": 'ņ',
	"Ncaron": 'Ň', "ncaron": 'ň', "napostrophe": 'ŉ', "Eng": 'Ŋ', "eng": 'ŋ',
	"Omacron": 'Ō', "omacron": 'ō', "Obreve": 'Ŏ', "obreve": 'ŏ',
	"Ohungarumlaut": 'Ő', "ohungarumlaut": 'ő',
	"Racute": 'Ŕ', "racute": 'ŕ', "Rcommaaccent": 'Ŗ', "rcommaaccent": 'ŗ',
	"Rcaron": 'Ř', "rcaron": 'ř', "Sacute": 'Ś', "sacute": 'ś',
	"Scircumflex": 'Ŝ', "scircumflex": 'ŝ', "Scedilla": 'Ş', "scedilla": 'ş',
	"Tcommaaccent": 'Ţ', "tcommaaccent": 'ţ', "Tcaron": 'Ť', "tcaron": 'ť',
	"Tbar": 'Ŧ', "tbar": 'ŧ', "Utilde": 'Ũ', "utilde": 'ũ',
	"Umacron": 'Ū', "umacron": 'ū', "Ubreve": 'Ŭ', "ubreve": 'ŭ',
	"Uring": 'Ů', "uring": 'ů', "Uhungarumlaut": 'Ű', "uhungarumlaut": 'ű',
	"Uogonek": 'Ų', "uogonek": 'ų', "Wcircumflex": 'Ŵ', "wcircumflex": 'ŵ',
	"Ycircumflex": 'Ŷ', "ycircumflex": 'ŷ',
	"Zacute": 'Ź', "zacute": 'ź', "Zdotaccent": 'Ż', "zdotaccent": 'ż',
	"longs": 'ſ',

	// A handful outside that block that the same documents use: the Romanian
	// letters written with a comma below rather than a cedilla, the Turkish
	// and Azeri schwa, and the two the Adobe list names for Welsh and Maltese.
	"Scommaaccent": 'Ș', "scommaaccent": 'ș',
	"Afii10017": 'А', "afii10017": 'А',
	"Wgrave": 'Ẁ', "wgrave": 'ẁ', "Wacute": 'Ẃ', "wacute": 'ẃ',
	"Wdieresis": 'Ẅ', "wdieresis": 'ẅ',
	"Ygrave": 'Ỳ', "ygrave": 'ỳ',
	"Hcaron": 'Ȟ', "hcaron": 'ȟ',
	// The rest of what the corpus asks for and nothing could answer, each of
	// them a name the Adobe list gives and none of them in doubt: the cedilla
	// spellings of the Romanian letters, the capital sharp s, the script ell
	// mathematicians write lengths with, and the angle brackets.
	"Tcedilla": 'Ţ', "tcedilla": 'ţ', "Germandbls": 'ẞ',
	"lscript": 'ℓ', "openbullet": '◦',
	"angbracketleft": '〈', "angbracketright": '〉',
	"Scommabelow": 'Ș', "scommabelow": 'ș',
	"Tcommabelow": 'Ț', "tcommabelow": 'ț',
}
