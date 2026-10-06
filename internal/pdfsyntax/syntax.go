// Package pdfsyntax holds the lexical rules of the PDF object syntax that
// both the file reader (internal/pdf) and the content stream scanner
// (internal/content) need: character classes, numbers and the escapes of
// names and strings. Nothing here allocates.
package pdfsyntax

import (
	"math"
	"strconv"
)

// IsSpace reports the six bytes the specification calls white-space, NUL
// included.
func IsSpace(c byte) bool { return class[c]&space != 0 }

// IsDelim reports the delimiter bytes.
func IsDelim(c byte) bool { return class[c]&delim != 0 }

// IsRegular reports a byte that may appear inside a name, a number or a
// keyword: anything that is neither white-space nor a delimiter.
func IsRegular(c byte) bool { return class[c] == 0 }

// IsDigit reports a decimal digit.
func IsDigit(c byte) bool { return c >= '0' && c <= '9' }

// IsNumStart reports a byte that can start a number.
func IsNumStart(c byte) bool { return c == '+' || c == '-' || c == '.' || IsDigit(c) }

const (
	space = 1 << iota
	delim
)

var class = func() (t [256]uint8) {
	for _, c := range []byte{0, 9, 10, 12, 13, 32} {
		t[c] = space
	}
	for _, c := range []byte("()<>[]{}/%") {
		t[c] = delim
	}
	return t
}()

// HexVal decodes one hexadecimal digit, or reports -1.
func HexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// SkipSpace returns the position of the first byte at or after p that is
// neither white-space nor part of a comment.
func SkipSpace(b []byte, p int) int {
	for p < len(b) {
		c := b[p]
		if IsSpace(c) {
			p++
			continue
		}
		if c == '%' {
			for p < len(b) && b[p] != '\n' && b[p] != '\r' {
				p++
			}
			continue
		}
		break
	}
	return p
}

// A Number is a parsed numeric token.
type Number struct {
	Int   int64   // the value, when IsInt
	Float float64 // the value, always
	IsInt bool    // written as an integer that fits an int64
}

// ParseNumber reads a number starting at b[p] and returns it with the
// position after it. A number too large for a float64 is not one. It accepts what producers write and readers tolerate:
// a doubled sign ("--5", only the first counts; the number is then a real),
// a leading or trailing point, and it stops at the first byte that cannot
// continue the number ("3.4-5" is two numbers). ok is false when the bytes
// hold no digit; end then steps over the signs and point that were there.
//
// The value is exactly what strconv.ParseInt or, failing that,
// strconv.ParseFloat makes of the text with its extra signs removed.
func ParseNumber(b []byte, p int) (n Number, end int, ok bool) {
	start := p
	neg := false
	signs := 0
	for p < len(b) && (b[p] == '+' || b[p] == '-') {
		if signs == 0 {
			neg = b[p] == '-'
		}
		signs++
		p++
	}
	var mant uint64
	digits, sig, exp := 0, 0, 0
	dot, exact := false, true
	for ; p < len(b); p++ {
		c := b[p]
		if c >= '0' && c <= '9' {
			digits++
			if mant == 0 && c == '0' {
				if dot {
					exp--
				}
				continue
			}
			if sig < 19 {
				mant = mant*10 + uint64(c-'0')
				sig++
				if dot {
					exp--
				}
			} else {
				exact = false
				if !dot {
					exp++
				}
			}
			continue
		}
		if c == '.' && !dot {
			dot = true
			continue
		}
		break
	}
	if digits == 0 {
		return Number{}, p, false
	}
	if !dot && signs <= 1 && exact && exp == 0 {
		// An integer, if it fits.
		if mant <= math.MaxInt64 || (neg && mant == 1<<63) {
			v := int64(mant)
			if neg {
				v = -v
			}
			return Number{Int: v, Float: float64(v), IsInt: true}, p, true
		}
	}
	var f float64
	if exact && mant < 1<<53 && exp >= -22 && exp <= 22 {
		// Clinger's fast path: both operands are exact, so one rounding.
		f = float64(mant)
		if exp < 0 {
			f /= pow10[-exp]
		} else {
			f *= pow10[exp]
		}
	} else {
		var err error
		if f, err = parseSlow(b[start:p], signs); err != nil {
			// Out of range: a syntax error, as strconv says.
			return Number{}, p, false
		}
	}
	if neg {
		f = -f
	}
	// A whole number written with a point, or too big for an int64, is a
	// real; its integer part is kept for callers that truncate.
	return Number{Int: int64(f), Float: f}, p, true
}

var pow10 = [...]float64{1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10,
	1e11, 1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22}

// parseSlow is strconv.ParseFloat on the unsigned digits, copied to the
// stack when they fit so the conversion to string does not escape.
func parseSlow(s []byte, signs int) (float64, error) {
	s = s[signs:]
	var buf [64]byte
	var f float64
	var err error
	if len(s) <= len(buf) {
		n := copy(buf[:], s)
		f, err = strconv.ParseFloat(string(buf[:n]), 64)
	} else {
		f, err = strconv.ParseFloat(string(s), 64)
	}
	return math.Abs(f), err
}

// DecodeName appends the bytes of a name with its #xx escapes resolved to
// dst. An escape that is not two hex digits stands for itself.
func DecodeName(dst, raw []byte) []byte {
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '#' && i+2 < len(raw) {
			if h1, h2 := HexVal(raw[i+1]), HexVal(raw[i+2]); h1 >= 0 && h2 >= 0 {
				dst = append(dst, byte(h1<<4|h2))
				i += 2
				continue
			}
		}
		dst = append(dst, c)
	}
	return dst
}

// DecodeHex appends the bytes of a hex string's digits to dst. Bytes that
// are not hex digits are skipped and a final odd digit is padded with zero.
func DecodeHex(dst, raw []byte) []byte {
	hi := -1
	for _, c := range raw {
		v := HexVal(c)
		if v < 0 {
			continue
		}
		if hi < 0 {
			hi = v
			continue
		}
		dst = append(dst, byte(hi<<4|v))
		hi = -1
	}
	if hi >= 0 {
		dst = append(dst, byte(hi<<4))
	}
	return dst
}

// DecodeLiteral appends the bytes a literal string's raw text (without the
// outer parentheses) denotes to dst, its escapes resolved. An end-of-line
// written into the string stays as it is: the specification asks for a
// line feed, but strings carry binary data — encryption hashes, IDs — and
// pdf.js, MuPDF and PDFium keep the bytes.
func DecodeLiteral(dst, raw []byte) []byte {
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c != '\\' {
			dst = append(dst, c)
			continue
		}
		i++
		if i >= len(raw) {
			break
		}
		e := raw[i]
		switch {
		case e == 'n':
			dst = append(dst, '\n')
		case e == 'r':
			dst = append(dst, '\r')
		case e == 't':
			dst = append(dst, '\t')
		case e == 'b':
			dst = append(dst, '\b')
		case e == 'f':
			dst = append(dst, '\f')
		case e == '\r':
			if i+1 < len(raw) && raw[i+1] == '\n' {
				i++
			}
		case e == '\n':
		case e >= '0' && e <= '7':
			v := int(e - '0')
			for k := 0; k < 2 && i+1 < len(raw) && raw[i+1] >= '0' && raw[i+1] <= '7'; k++ {
				i++
				v = v*8 + int(raw[i]-'0')
			}
			dst = append(dst, byte(v))
		default:
			dst = append(dst, e)
		}
	}
	return dst
}

// LiteralEnd returns the position after the ')' that closes a literal
// string whose '(' is at b[p], and whether the string needs DecodeLiteral
// (it holds an escape). end is -1 for an unterminated string.
func LiteralEnd(b []byte, p int) (end int, escaped bool) {
	depth := 0
	for ; p < len(b); p++ {
		switch b[p] {
		case '\\':
			escaped = true
			p++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return p + 1, escaped
			}
		}
	}
	return -1, escaped
}
