package cera

import (
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// TextPos is a position in the text of a page: the boundary before
// Chars[p], from 0 to len(Chars).
type TextPos int

// TextRange is the characters Chars[Start:End] of a page.
type TextRange struct{ Start, End TextPos }

// Empty reports whether r holds no characters.
func (r TextRange) Empty() bool { return r.End <= r.Start }

// Granularity is the unit a selection grows by.
type Granularity uint8

// Granularities.
const (
	SelectChars  Granularity = iota // single characters: a drag
	SelectWords                     // whole words: a double click and drag
	SelectLines                     // whole lines: a triple click and drag
	SelectBlocks                    // whole blocks
)

// TextFormat controls TextOf.
type TextFormat struct {
	// JoinHyphenated drops the hyphen of a word broken across lines
	// (TextLine.Hyphenated) and the line break after it.
	JoinHyphenated bool
}

// All returns the range of all the text.
func (t *PageText) All() TextRange { return TextRange{0, TextPos(len(t.Chars))} }

// CharAt returns the index of the character whose quad contains (x, y),
// in points of the displayed page.
func (t *PageText) CharAt(x, y float64) (int, bool) {
	for li := range t.Lines {
		l := &t.Lines[li]
		if !l.Quad.Contains(x, y) {
			continue
		}
		for i := l.Start; i < l.End; i++ {
			if t.Chars[i].Quad.Contains(x, y) {
				return i, true
			}
		}
	}
	return 0, false
}

// PosAt returns the position nearest to (x, y), in points of the
// displayed page: on the nearest line, before the character under the
// point when the point is in its first half, else after it. A drag from
// PosAt of one point to PosAt of another selects the text between them,
// also when the points are beside or between lines.
func (t *PageText) PosAt(x, y float64) TextPos {
	best, bestDist := -1, math.Inf(1)
	p := [2]float64{x, y}
	for li := range t.Lines {
		l := &t.Lines[li]
		f := frame{l.Dir}
		u, v := f.u(p), f.v(p)
		u0, u1 := f.u(l.Quad.P[0]), f.u(l.Quad.P[1])
		v0, v1 := f.v(l.Quad.P[0]), f.v(l.Quad.P[2])
		// Beside a line counts half as far as above or below it: a point
		// right of a line's end is on that line.
		du := max(u0-u, 0, u-u1) / 2
		dv := max(v0-v, 0, v-v1)
		if d := math.Hypot(du, dv); d < bestDist {
			best, bestDist = li, d
		}
	}
	if best < 0 {
		return 0
	}
	l := &t.Lines[best]
	f := frame{l.Dir}
	u := f.u(p)
	for i := l.Start; i < l.End; i++ {
		c := &t.Chars[i]
		mid := f.u(c.Origin) + c.Advance/2
		if u < mid {
			return TextPos(i)
		}
	}
	return TextPos(l.End)
}

// Select returns the range between the positions a and b, in either
// order, grown to whole units of g and to whole /ActualText sequences.
func (t *PageText) Select(a, b TextPos, g Granularity) TextRange {
	n := TextPos(len(t.Chars))
	a, b = min(max(a, 0), n), min(max(b, 0), n)
	if a > b {
		a, b = b, a
	}
	r := TextRange{a, b}
	switch g {
	case SelectWords:
		r.Start = t.WordAt(a).Start
		if b > a {
			r.End = t.WordAt(b - 1).End
		} else {
			r.End = t.WordAt(a).End
		}
	case SelectLines:
		r.Start = t.LineAt(a).Start
		r.End = t.LineAt(max(b-1, a)).End
	case SelectBlocks:
		r.Start = t.blockAt(a).Start
		r.End = t.blockAt(max(b-1, a)).End
	}
	return t.wholeGroups(r)
}

// wholeGroups grows r to the /ActualText sequences it cuts.
func (t *PageText) wholeGroups(r TextRange) TextRange {
	n := TextPos(len(t.Chars))
	for r.Start > 0 && r.Start < n && t.Chars[r.Start].group != 0 && t.Chars[r.Start-1].group == t.Chars[r.Start].group {
		r.Start--
	}
	for r.End > 0 && r.End < n && t.Chars[r.End].group != 0 && t.Chars[r.End-1].group == t.Chars[r.End].group {
		r.End++
	}
	return r
}

// WordAt returns the word at p: the characters around Chars[p] on its line
// of the same kind (letters and digits, other signs, white space) with no
// word space between them. Each ideograph is a word of its own. At the end
// of a line it is the word before.
func (t *PageText) WordAt(p TextPos) TextRange {
	if len(t.Lines) == 0 {
		return TextRange{}
	}
	i := min(max(int(p), 0), len(t.Chars)-1)
	l := &t.Lines[t.lineOf(i)]
	if int(p) == l.End && p > 0 {
		i = int(p) - 1
		l = &t.Lines[t.lineOf(i)]
	}
	k := t.wordKind(i)
	start, end := i, i+1
	if k != kindIdeograph {
		for start > l.Start && t.wordKind(start-1) == k && !t.spaceBefore(start) {
			start--
		}
		for end < l.End && t.wordKind(end) == k && !t.spaceBefore(end) {
			end++
		}
	}
	return t.wholeGroups(TextRange{TextPos(start), TextPos(end)})
}

// Kinds of characters for WordAt.
const (
	kindSpace = iota
	kindWord
	kindSign
	kindIdeograph
)

// wordKind returns the kind of Chars[i]; the rest of an /ActualText
// sequence is of the kind of its text.
func (t *PageText) wordKind(i int) int {
	for i > 0 && t.Chars[i].Text == "" && t.Chars[i].group != 0 && t.Chars[i-1].group == t.Chars[i].group {
		i--
	}
	s := t.Chars[i].Text
	r, _ := utf8.DecodeRuneInString(s)
	switch {
	case s == "":
		return kindSign
	case isSpace(s):
		return kindSpace
	case unicode.Is(unicode.Ideographic, r) || unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
		return kindIdeograph
	case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_':
		return kindWord
	}
	return kindSign
}

// LineAt returns the line at p, or before it at the end of a line.
func (t *PageText) LineAt(p TextPos) TextRange {
	if len(t.Lines) == 0 {
		return TextRange{}
	}
	l := &t.Lines[t.lineOf(min(max(int(p), 0), len(t.Chars)-1))]
	return TextRange{TextPos(l.Start), TextPos(l.End)}
}

// blockAt returns the block at p.
func (t *PageText) blockAt(p TextPos) TextRange {
	if len(t.Lines) == 0 {
		return TextRange{}
	}
	b := &t.Blocks[t.Lines[t.lineOf(min(max(int(p), 0), len(t.Chars)-1))].Block]
	return TextRange{TextPos(t.Lines[b.Start].Start), TextPos(t.Lines[b.End-1].End)}
}

// lineOf returns the index of the line of Chars[i].
func (t *PageText) lineOf(i int) int {
	return sort.Search(len(t.Lines), func(li int) bool { return t.Lines[li].End > i })
}

// Quads returns the boxes that highlight r: one per line it touches, from
// the first to the last character of r on the line, as high as the line.
func (t *PageText) Quads(r TextRange) []Quad {
	if r.Empty() {
		return nil
	}
	start, end := max(int(r.Start), 0), min(int(r.End), len(t.Chars))
	var qs []Quad
	for li := t.lineOf(start); li < len(t.Lines) && t.Lines[li].Start < end; li++ {
		l := &t.Lines[li]
		f := frame{l.Dir}
		e := extent{u0: math.Inf(1), u1: math.Inf(-1), v0: f.v(l.Quad.P[0]), v1: f.v(l.Quad.P[2])}
		for i := max(start, l.Start); i < min(end, l.End); i++ {
			for _, p := range t.Chars[i].Quad.P {
				u := f.u(p)
				e.u0, e.u1 = min(e.u0, u), max(e.u1, u)
			}
		}
		if e.u0 <= e.u1 {
			qs = append(qs, e.quad(f))
		}
	}
	return qs
}

// TextOf returns the text of r, as String does for the whole page: a
// space for a word space, a line break between lines and an empty line
// between blocks.
func (t *PageText) TextOf(r TextRange, format TextFormat) string {
	s, _, _ := t.write(int(r.Start), int(r.End), format, false)
	return s
}

// write returns the text of Chars[start:end], and with offsets where the
// text of each of them starts and ends in it: starts[len] is the length of
// the text, and a character without text of its own starts and ends where
// the text before it ends.
func (t *PageText) write(start, end int, format TextFormat, offsets bool) (s string, starts, ends []int32) {
	start, end = max(start, 0), min(end, len(t.Chars))
	if start >= end {
		if offsets {
			return "", []int32{0}, nil
		}
		return "", nil, nil
	}
	if offsets {
		starts, ends = make([]int32, end-start+1), make([]int32, end-start)
	}
	var b strings.Builder
	at := func(i int) {
		if offsets {
			starts[i-start], ends[i-start] = int32(b.Len()), int32(b.Len())
		}
	}
	sep := "" // written before the next text
	block := -1
	for li := t.lineOf(start); li < len(t.Lines) && t.Lines[li].Start < end; li++ {
		l := &t.Lines[li]
		if l.Block != block {
			if b.Len() > 0 {
				sep = "\n\n"
			}
			block = l.Block
		}
		from, to := max(start, l.Start), min(end, l.End)
		join := false
		if format.JoinHyphenated && l.Hyphenated && end > l.End {
			// Up to the hyphen, with the white space after it.
			for to > from && isSpace(t.Chars[to-1].Text) {
				to--
			}
			if to > from {
				to--
				join = true
			}
		}
		wrote := false // on this line
		for i := from; i < to; i++ {
			c := &t.Chars[i]
			if c.Text == "" && c.group != 0 || !wrote && isSpace(c.Text) {
				at(i) // replaced by the /ActualText before, or indenting
				continue
			}
			wrote = true
			if sep != "" {
				b.WriteString(sep)
				sep = ""
			} else if i > from && t.spaceBefore(i) {
				b.WriteByte(' ')
			}
			at(i)
			if c.Text == "" {
				b.WriteRune('�')
			} else {
				b.WriteString(c.Text)
			}
			if offsets {
				ends[i-start] = int32(b.Len())
			}
		}
		for i := to; i < min(end, l.End); i++ {
			at(i) // the hyphen joined
		}
		if b.Len() > 0 && sep == "" && !join {
			sep = "\n"
		}
	}
	if offsets {
		starts[end-start] = int32(b.Len())
	}
	return b.String(), starts, ends
}

// plainText is String of a PageText and where the text of each character
// is in it, made on first use.
type plainText struct {
	once         sync.Once
	s            string
	starts, ends []int32
}

// plainText returns the text of the page with its offsets.
func (t *PageText) plainText() *plainText {
	p := t.plain
	if p == nil {
		p = new(plainText) // made by hand: not kept
	}
	p.once.Do(func() { p.s, p.starts, p.ends = t.write(0, len(t.Chars), TextFormat{}, true) })
	return p
}

// Offset returns the byte offset of p in String: where the text of
// Chars[p] starts, or the length of the text for the end.
func (t *PageText) Offset(p TextPos) int {
	pt := t.plainText()
	return int(pt.starts[min(max(int(p), 0), len(t.Chars))])
}

// PosAtOffset returns the position of the byte offset n of String: before
// the character whose text holds it, or after the text before n when n is
// in the space or line breaks between characters.
func (t *PageText) PosAtOffset(n int) TextPos {
	pt := t.plainText()
	return TextPos(sort.Search(len(pt.ends), func(i int) bool { return int(pt.ends[i]) > n }))
}

// RangeOfOffsets returns the characters whose text is in the bytes
// String()[lo:hi], with those it covers only in part: the way back from
// an anchor kept as a byte range of the text of a page.
func (t *PageText) RangeOfOffsets(lo, hi int) TextRange {
	pt := t.plainText()
	r := TextRange{Start: t.PosAtOffset(lo)}
	r.End = TextPos(sort.Search(len(pt.ends), func(i int) bool { return int(pt.starts[i]) >= hi }))
	r.End = max(r.End, r.Start)
	return t.wholeGroups(r)
}

// InRect returns the ranges of the characters whose centres are inside
// r, in points of the displayed page: a rectangular selection, such as a
// column of a table.
func (t *PageText) InRect(r Rect) []TextRange {
	var out []TextRange
	for i := range t.Chars {
		b := t.Chars[i].Box
		x, y := (b.X0+b.X1)/2, (b.Y0+b.Y1)/2
		if x < r.X0 || x > r.X1 || y < r.Y0 || y > r.Y1 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].End == TextPos(i) {
			out[n-1].End++
		} else {
			out = append(out, TextRange{TextPos(i), TextPos(i + 1)})
		}
	}
	for i := range out {
		out[i] = t.wholeGroups(out[i])
	}
	return out
}
