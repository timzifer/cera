// Ported from github.com/go-pdfkit/pdffont v0.3.1 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/render authors); see LICENSE-go-pdfkit.

package pdffont

import (
	"strings"
	"unicode/utf16"
)

// readToUnicode reads the font's /ToUnicode map, the one thing in a PDF
// that says what its text actually says.
func (f *Font) readToUnicode() {
	s, ok := f.doc.Resolve(f.dict.Get("ToUnicode")).Stream()
	if !ok {
		return
	}
	if data, ok := f.decode(s); ok {
		f.toUni = ReadToUnicode(data)
	}
}

// ReadToUnicode decodes a ToUnicode CMap: a little PostScript program whose
// only meaningful parts are the bfchar and bfrange blocks saying which
// characters each code stands for.
func ReadToUnicode(data []byte) map[int]string {
	out := map[int]string{}
	toks := cmapTokens(data)
	for i := 0; i < len(toks); i++ {
		switch toks[i].word {
		case "beginbfchar":
			i = readBFChar(toks, i+1, out)
		case "beginbfrange":
			i = readBFRange(toks, i+1, out)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// maxToUnicodeEntries is how many codes one map may name.
//
// A bfrange names a run of codes in about twenty bytes, and only the width of
// a single run was bounded — not how many runs a map could hold. So the size
// of the answer had nothing to do with the size of the question: 585 bytes
// produced 655 360 entries in 2.3 seconds, and 10 655 bytes produced
// 13 107 200 entries and a gigabyte. Every font on every page is read this
// way, so that was a gigabyte per font.
//
// The bound is not a guess. Across 5 338 /ToUnicode maps taken out of real
// documents — arXiv figures, government forms, and mozilla's pdf.js corpus —
// the median names 13 codes, the 99th percentile names 538, and the largest
// names exactly 65 536: one whole two-byte code space, which is as many codes
// as a font of that shape can have. This allows four times that, so a
// document has to be malformed or hostile to reach it, and a map that does is
// cut off there rather than being allowed to ask for everything.
const maxToUnicodeEntries = 1 << 18

// A cmapToken is one piece of such a program: a hexadecimal string, an array
// bracket, or a bare word.
type cmapToken struct {
	word  string
	hex   []byte
	isHex bool
}

// cmapTokens cuts the program up. Only three shapes matter — a hexadecimal
// string, a bracket, and a word — and everything else can be a word.
func cmapTokens(b []byte) []cmapToken {
	var out []cmapToken
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == '%':
			for i < len(b) && b[i] != '\n' && b[i] != '\r' {
				i++
			}
		case c == '<':
			j := i + 1
			for j < len(b) && b[j] != '>' {
				j++
			}
			out = append(out, cmapToken{hex: unhexBytes(b[i+1 : min(j, len(b))]), isHex: true})
			i = min(j+1, len(b))
		case c == '[' || c == ']':
			out = append(out, cmapToken{word: string(c)})
			i++
		case c <= ' ':
			i++
		default:
			j := i
			for j < len(b) && b[j] > ' ' && b[j] != '<' && b[j] != '[' && b[j] != ']' && b[j] != '%' {
				j++
			}
			out = append(out, cmapToken{word: string(b[i:j])})
			i = j
		}
	}
	return out
}

// readBFChar reads pairs of "code, what it stands for" up to the block's end.
func readBFChar(toks []cmapToken, i int, out map[int]string) int {
	for i+1 < len(toks) {
		if toks[i].word == "endbfchar" {
			return i
		}
		if len(out) >= maxToUnicodeEntries {
			return i
		}
		if !toks[i].isHex || !toks[i+1].isHex {
			return i
		}
		out[codeOf(toks[i].hex)] = textOf(toks[i+1].hex)
		i += 2
	}
	return i
}

// readBFRange reads runs of codes: either a first character the run counts on
// from, or a list naming each one.
func readBFRange(toks []cmapToken, i int, out map[int]string) int {
	for i+2 < len(toks) {
		if toks[i].word == "endbfrange" {
			return i
		}
		if !toks[i].isHex || !toks[i+1].isHex {
			return i
		}
		lo, hi := codeOf(toks[i].hex), codeOf(toks[i+1].hex)
		if hi < lo || hi-lo > 1<<16 {
			return i
		}
		if len(out) >= maxToUnicodeEntries {
			return i
		}
		switch {
		case toks[i+2].isHex:
			base := utf16Runes(toks[i+2].hex)
			for c := lo; c <= hi && len(out) < maxToUnicodeEntries; c++ {
				out[c] = countOn(base, c-lo)
			}
			i += 3
		case toks[i+2].word == "[":
			j := i + 3
			for c := lo; c <= hi && j < len(toks) && toks[j].word != "]"; c++ {
				if toks[j].isHex && len(out) < maxToUnicodeEntries {
					out[c] = textOf(toks[j].hex)
				}
				j++
			}
			for j < len(toks) && toks[j].word != "]" {
				j++
			}
			i = j + 1
		default:
			return i
		}
	}
	return i
}

// codeOf reads a code, which is one or two bytes written as hexadecimal.
func codeOf(b []byte) int {
	v := 0
	for _, c := range b {
		v = v<<8 | int(c)
	}
	return v
}

// textOf reads what a code stands for: characters written as sixteen-bit
// units, the way the format carries text.
func textOf(b []byte) string {
	return string(utf16Runes(b))
}

// utf16Runes decodes the sixteen-bit units a ToUnicode value is written in.
func utf16Runes(b []byte) []rune {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return utf16.Decode(units)
}

// countOn is what the n'th code of a range stands for, counting on from the
// first: the last character moves and the rest stay, which is what the
// specification says a range means.
func countOn(base []rune, n int) string {
	if len(base) == 0 {
		return ""
	}
	out := append([]rune{}, base...)
	out[len(out)-1] += rune(n)
	return string(out)
}

// unhexBytes reads a hexadecimal string, ignoring whatever it was laid out
// with. An odd digit at the end is taken as the high half of a byte, which is
// what the format says.
func unhexBytes(b []byte) []byte {
	var digits []byte
	for _, c := range b {
		if isHexDigit(c) {
			digits = append(digits, hexVal(c))
		}
	}
	if len(digits)%2 == 1 {
		digits = append(digits, 0)
	}
	out := make([]byte, 0, len(digits)/2)
	for i := 0; i+1 < len(digits); i += 2 {
		out = append(out, digits[i]<<4|digits[i+1])
	}
	return out
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func hexVal(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	default:
		return c - 'a' + 10
	}
}

// TrimText tidies what a map gives back: the format allows a value to carry
// the replacement character or nothing at all, and neither is text.
func TrimText(s string) string {
	s = strings.TrimFunc(s, func(r rune) bool { return r == 0 || r == 0xFFFD })
	return s
}
