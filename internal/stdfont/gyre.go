package stdfont

import _ "embed"

// The stand-ins for Helvetica, Times and Courier: TeX Gyre Heros, Termes
// and Cursor 2.004 (GUST Font License, see LICENSE-TeXGyre), which are
// URW's Nimbus Sans L, Nimbus Roman No9 L and Nimbus Mono L, the clones of
// the standard fonts Ghostscript and MuPDF draw, with their metrics. The
// files are unchanged.
var (
	//go:embed texgyreheros-regular.otf
	HelveticaRegular []byte
	//go:embed texgyreheros-bold.otf
	HelveticaBold []byte
	//go:embed texgyreheros-italic.otf
	HelveticaItalic []byte
	//go:embed texgyreheros-bolditalic.otf
	HelveticaBoldItalic []byte

	//go:embed texgyretermes-regular.otf
	TimesRegular []byte
	//go:embed texgyretermes-bold.otf
	TimesBold []byte
	//go:embed texgyretermes-italic.otf
	TimesItalic []byte
	//go:embed texgyretermes-bolditalic.otf
	TimesBoldItalic []byte

	//go:embed texgyrecursor-regular.otf
	CourierRegular []byte
	//go:embed texgyrecursor-bold.otf
	CourierBold []byte
	//go:embed texgyrecursor-italic.otf
	CourierItalic []byte
	//go:embed texgyrecursor-bolditalic.otf
	CourierBoldItalic []byte
)
