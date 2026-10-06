// Ported from github.com/go-pdfkit/pdffont v0.3.1 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/render authors); see LICENSE-go-pdfkit.

package pdffont

import "strings"

// RuneOfGlyphName turns a glyph name into the character it stands for, through
// the table above or through the two conventions the specification defines for
// naming a character directly. ok is false for a name that says nothing about
// which character it is, which a subsetted font's own names often do not.
func RuneOfGlyphName(name string) (rune, bool) {
	s, ok := TextOfGlyphName(name)
	if !ok {
		return 0, false
	}
	r := []rune(s)
	if len(r) != 1 {
		return 0, false
	}
	return r[0], true
}

// TextOfGlyphName turns a glyph name into the text it stands for, which is not
// always one character: a name may say it is a ligature of several.
//
// Three of the rules here are the ones the Adobe Glyph List lays down for
// reading a name nobody has listed, and each was costing real text. A name may
// carry a variant after a full stop — "a.sc" is a small-capital A and is still
// an A, 7 272 of them in the corpus. A name may be several component names
// with underscores between them — "f_i" is the fi ligature, and without this
// "Definition" comes back "Denition", which is not a missing character so much
// as a misspelt word. And a name may simply be one nobody thought to list.
func TextOfGlyphName(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	if r, ok := oneGlyphRune(name); ok {
		return string(r), true
	}
	if seq, ok := unicodeSequence(name); ok {
		return seq, true
	}
	// A variant of a character is still that character: the part before the
	// first full stop is the name, and what follows says which cut of it.
	if base, _, found := strings.Cut(name, "."); found {
		if base == "" {
			return "", false
		}
		return TextOfGlyphName(base)
	}
	// A name made of parts with underscores between them is the parts, in
	// order — which is how a ligature is named when it has no name of its own.
	if strings.Contains(name, "_") {
		// Every part that can be read contributes at least one character, and
		// a part that cannot gives up on the whole name, so what comes out of
		// this is never empty.
		var out strings.Builder
		for _, part := range strings.Split(name, "_") {
			piece, ok := TextOfGlyphName(part)
			if !ok {
				return "", false
			}
			out.WriteString(piece)
		}
		return out.String(), true
	}
	return "", false
}

// oneGlyphRune is a name that stands for exactly one character, by any of the
// ways a name can.
func oneGlyphRune(name string) (rune, bool) {
	if r, ok := glyphRunes[name]; ok {
		return r, true
	}
	if r, ok := latinExtendedRunes[name]; ok {
		return r, true
	}
	if r, ok := greekAndMathRunes[name]; ok {
		return r, true
	}
	if len(name) == 1 {
		return rune(name[0]), true
	}
	if r, ok := parseHexName(name, "uni", 4); ok {
		return r, true
	}
	if r, ok := parseHexName(name, "u", 4); ok {
		return r, true
	}
	return 0, false
}

// unicodeSequence reads the uniXXXXYYYY form, which names several characters
// at once: a letter and the accent that goes over it, or the three pieces a
// Hebrew cluster is written in.
//
// It has to be told apart from something that looks exactly like it. Producers
// write a glyph's own number in the same shape — uni00000048 is glyph 72, not
// U+0000 followed by U+0048 — and 49 740 names in the corpus are that. The two
// are separable by one observation: a real sequence never begins with U+0000,
// because nothing is written after a character that does not exist. Of the
// 3 875 genuine sequences found across 14 823 embedded fonts, not one starts
// with a zero group; of the disguised glyph numbers, all of them do.
func unicodeSequence(name string) (string, bool) {
	digits, ok := strings.CutPrefix(name, "uni")
	if !ok || len(digits) < 8 || len(digits)%4 != 0 {
		return "", false
	}
	var out strings.Builder
	for i := 0; i < len(digits); i += 4 {
		r, ok := parseHexName("uni"+digits[i:i+4], "uni", 4)
		if !ok {
			return "", false
		}
		if r == 0 {
			// A glyph number wearing a character's clothes.
			return "", false
		}
		out.WriteRune(r)
	}
	return out.String(), true
}

// parseHexName reads the uniXXXX and uXXXX conventions.
func parseHexName(name, prefix string, minDigits int) (rune, bool) {
	if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
		return 0, false
	}
	digits := name[len(prefix):]
	if len(digits) < minDigits || len(digits) > 6 {
		return 0, false
	}
	var v rune
	for i := 0; i < len(digits); i++ {
		d := hexDigit(digits[i])
		if d < 0 {
			return 0, false
		}
		v = v<<4 | rune(d)
	}
	return v, true
}

// hexDigit reads one hexadecimal digit, or reports -1.
func hexDigit(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	}
	return -1
}
