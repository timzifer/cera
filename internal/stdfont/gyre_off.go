//go:build cera_nogyre

package stdfont

// Gyre says the TeX Gyre stand-ins are built in; with the build tag
// cera_nogyre they are not, and Arimo, Tinos and Cousine draw Helvetica,
// Times and Courier.
const Gyre = false

var (
	HelveticaRegular, HelveticaBold, HelveticaItalic, HelveticaBoldItalic []byte
	TimesRegular, TimesBold, TimesItalic, TimesBoldItalic                 []byte
	CourierRegular, CourierBold, CourierItalic, CourierBoldItalic         []byte
)
