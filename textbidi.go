package cera

import (
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/bidi"
	"golang.org/x/text/unicode/norm"
)

// Right-to-left text. A page shows Arabic and Hebrew as it shows any text,
// glyph by glyph where they stand, and mostly from left to right: in
// visual order, the reverse of the order they are read in, and as the
// presentation forms the glyphs are — the shape a letter has at the start,
// in the middle or at the end of a word — rather than the letters. The
// layout
//   - decomposes presentation forms to their letters, and gives the
//     letters of a ligature their parts of its box from the right;
//   - puts the characters of a line into logical order: it orders them by
//     their place on the line, resolves their directions with the Unicode
//     bidirectional algorithm, taking the direction of most letters for
//     that of the line, and reverses what reads from the right (rule L2,
//     which undoes itself). The algorithm resolves numbers and
//     punctuation by what stands before them in reading order, so a line
//     that reads from the right is given to it from its right end.
//
// Brackets are not mirrored. A bracket that reads from the right is drawn
// with the glyph of its counterpart, but writers map that glyph back to
// the character typed, as they map the forms of a letter: in the files
// seen, the text of the glyph is the bracket meant (pdf.js issue11656.pdf
// reads "(4)" that way).
//
// A line without a right-to-left character is left as it is.

// decomposeRTL replaces the Arabic and Hebrew presentation forms in s
// (U+FB1D to U+FDFF, U+FE70 to U+FEFF) with the letters they stand for.
func decomposeRTL(s string) string {
	// They are encoded with a first byte of 0xEF; most text has none.
	if strings.IndexByte(s, 0xef) < 0 || !strings.ContainsFunc(s, presentationForm) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if presentationForm(r) {
			b.WriteString(norm.NFKC.String(string(r)))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func presentationForm(r rune) bool {
	return r >= 0xfb1d && r <= 0xfdff || r >= 0xfe70 && r <= 0xfefc
}

// strongRTL reports whether the first character of s reads from the right:
// a Hebrew or Arabic letter.
func strongRTL(s string) bool {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 || r < 0x590 {
		return false
	}
	p, _ := bidi.LookupRune(r)
	return p.Class() == bidi.R || p.Class() == bidi.AL
}

// reorderLine puts the characters of a line, the indices idx into chars,
// into logical order and marks those that read from the right, when the
// line has right-to-left characters. It reports whether the line as a
// whole reads from the right.
func reorderLine(chars []TextChar, idx []int) bool {
	// Right-to-left characters start at U+0590, a first byte of 0xD6:
	// most lines have nothing from there on.
	if !slices.ContainsFunc(idx, func(ci int) bool { s := chars[ci].Text; return s != "" && s[0] >= 0xd6 }) {
		return false
	}
	rtl, ltr := 0, 0
	for _, ci := range idx {
		switch r, _ := utf8.DecodeRuneInString(chars[ci].Text); {
		case strongRTL(chars[ci].Text):
			rtl++
		case r < 0x590 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= 0xc0):
			ltr++
		case r >= 0x590:
			if p, _ := bidi.LookupRune(r); p.Class() == bidi.L {
				ltr++
			}
		}
	}
	if rtl == 0 {
		return false
	}
	// Where they stand: by the middle of their boxes along the line.
	f := frame{chars[idx[0]].Dir}
	mid := func(ci int) float64 { return f.u(chars[ci].Origin) + chars[ci].Advance/2 }
	slices.SortStableFunc(idx, func(a, b int) int { return cmpFloat(mid(a), mid(b)) })
	// The word spaces, while the characters are in the order they stand.
	for k := 1; k < len(idx); k++ {
		a, b := &chars[idx[k-1]], &chars[idx[k]]
		if !isSpace(a.Text) && !isSpace(b.Text) && f.u(b.Origin)-f.u(a.Origin)-a.Advance > 0.15*max(a.Size, b.Size) {
			a.gapR, b.gapL = true, true
		}
	}

	// One character of text for each, after a mark that sets the
	// direction of the line: the algorithm would take that of the first
	// letter, which in visual order is the last or any other.
	base := rtl > ltr
	visual := make([]rune, 1, len(idx)+1)
	visual[0] = '‎'
	if base {
		visual[0] = '‏'
	}
	// at returns the place in idx of character k of the text.
	at := func(k int) int {
		if base {
			return len(idx) - k
		}
		return k - 1
	}
	for k := 1; k <= len(idx); k++ {
		r, n := utf8.DecodeRuneInString(chars[idx[at(k)]].Text)
		if n == 0 || r == utf8.RuneError {
			r = '￼' // an object: neutral
		}
		visual = append(visual, r)
	}
	var p bidi.Paragraph
	if _, err := p.SetString(string(visual)); err != nil {
		return base
	}
	order, err := p.Order()
	if err != nil {
		return base
	}

	// The level of each character: 0 for what reads from the left on a
	// line that does, 1 for what reads from the right, 2 for letters and
	// numbers that read from the left inside that.
	level := make([]uint8, len(idx))
	number := func(lo, hi int) bool {
		for _, r := range visual[lo : hi+1] {
			switch c, _ := bidi.LookupRune(r); c.Class() {
			case bidi.L, bidi.R, bidi.AL:
				return false
			}
		}
		return true
	}
	n := order.NumRuns()
	dirs := make([]bidi.Direction, n)
	spans := make([][2]int, n)
	for i := range n {
		run := order.Run(i)
		dirs[i] = run.Direction()
		spans[i][0], spans[i][1] = run.Pos()
	}
	for i := range n {
		lo, hi := spans[i][0], spans[i][1]
		lv := uint8(0)
		switch {
		case dirs[i] == bidi.RightToLeft:
			lv = 1
		case base:
			lv = 2
		case number(lo, hi) && (i > 0 && dirs[i-1] == bidi.RightToLeft || i+1 < n && dirs[i+1] == bidi.RightToLeft):
			lv = 2 // a number in a phrase that reads from the right
		}
		for k := max(lo, 1); k <= hi && k <= len(idx); k++ {
			level[at(k)] = lv
		}
	}

	// Rule L2: from the highest level down, reverse every stretch at that
	// level or above.
	for lv := uint8(2); lv >= 1; lv-- {
		for i := 0; i < len(idx); {
			if level[i] < lv {
				i++
				continue
			}
			j := i
			for j < len(idx) && level[j] >= lv {
				j++
			}
			slices.Reverse(idx[i:j])
			slices.Reverse(level[i:j])
			i = j
		}
	}
	for k, ci := range idx {
		chars[ci].rtl = level[k] == 1
	}
	return base
}
