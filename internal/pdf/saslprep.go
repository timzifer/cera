package pdf

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// saslprep prepares a password for the revision 5 and 6 security handlers
// (PDF 32000-2 7.6.4.3.3): the SASLprep profile of stringprep (RFC 4013),
// then UTF-8, at most 127 bytes. Characters RFC 4013 maps to nothing are
// dropped, other spaces become U+0020, and the result is NFKC-normalised,
// so a password typed with a soft hyphen or a ª opens a file whose
// producer prepared it. Prohibited characters are kept: refusing the
// password would not open the file either.
func saslprep(password string) []byte {
	var b strings.Builder
	for _, r := range password {
		switch {
		case mapsToNothing(r):
		case nonASCIISpace(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := []byte(norm.NFKC.String(b.String()))
	if len(out) > 127 {
		out = out[:127]
	}
	return out
}

// mapsToNothing is RFC 3454 table B.1.
func mapsToNothing(r rune) bool {
	switch r {
	case 0x00AD, 0x034F, 0x1806, 0x180B, 0x180C, 0x180D, 0x200B, 0x200C,
		0x200D, 0x2060, 0xFEFF:
		return true
	}
	return r >= 0xFE00 && r <= 0xFE0F
}

// nonASCIISpace is RFC 3454 table C.1.2.
func nonASCIISpace(r rune) bool {
	switch r {
	case 0x00A0, 0x1680, 0x202F, 0x205F, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200B
}
