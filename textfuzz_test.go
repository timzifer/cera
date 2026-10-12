package cera

import (
	"math"
	"testing"
	"unicode/utf8"
)

// fuzzChars makes characters of bytes, eight each, as a text device might
// collect them from a hostile page: any text, positions and sizes that are
// far off, not finite or the same for all, directions in every quarter and
// between, sequences of /ActualText and marked content.
func fuzzChars(data []byte) []TextChar {
	texts := [...]string{"", "\ufb01", "\u00ad", "-", "e\u0301", "\ufffd", "ab", "\u200f", " ", "\n", "\t", "\u00a0", "http://a.bc/d", "\u0661", "(", "."}
	angles := [...]float64{0, 90, 180, 270, 45, 1, -1, math.NaN()}
	val := func(b byte) float64 {
		switch b {
		case 0xff:
			return math.NaN()
		case 0xfe:
			return math.Inf(1)
		case 0xfd:
			return math.Inf(-1)
		case 0xfc:
			return 1e300
		case 0xfb:
			return -1e300
		}
		return float64(b)
	}
	var chars []TextChar
	for len(data) >= 8 && len(chars) < 512 {
		b := data[:8]
		data = data[8:]
		c := TextChar{MCID: -1, Code: int(b[0])}
		switch {
		case b[0] < 0x80:
			c.Text = string(rune(b[0]))
		case b[0] < 0xa0:
			c.Text = string(rune(0x5d0 + int(b[0]&0x1f))) // Hebrew
		case b[0] < 0xc0:
			c.Text = string(rune(0x627 + int(b[0]&0x1f))) // Arabic
		case b[0] < 0xe0:
			c.Text = string(rune(0x4e00 + int(b[0]&0x1f))) // Han
		default:
			c.Text = texts[b[0]&15]
		}
		x, y := val(b[1])*3+float64(b[7]&7)/4, val(b[2])*3
		c.Size = val(b[3]) / 8
		c.Advance = val(b[4]) / 128 * c.Size
		sin, cos := math.Sincos(angles[b[5]&7] * math.Pi / 180)
		c.Dir = [2]float64{cos, sin}
		c.Origin = [2]float64{x, y}
		// The em box: the ascent on the side the text stands up to, with
		// y down.
		ux, uy := sin, -cos
		asc, desc := 0.8*c.Size, -0.2*c.Size
		c.Quad = Quad{[4][2]float64{
			{x + asc*ux, y + asc*uy},
			{x + asc*ux + c.Advance*cos, y + asc*uy + c.Advance*sin},
			{x + desc*ux, y + desc*uy},
			{x + desc*ux + c.Advance*cos, y + desc*uy + c.Advance*sin},
		}}
		c.Box = c.Quad.Bounds()
		c.Artifact = b[6]&1 != 0
		c.Mode = TextMode(b[6] >> 1 & 7)
		if b[6]&0x10 != 0 {
			c.MCID = int(b[5] >> 3)
		}
		c.group = int32(b[6] >> 5) // 0: no /ActualText sequence
		chars = append(chars, c)
	}
	return chars
}

// FuzzText lays out arbitrary characters and searches, selects and
// measures the result (#138): the layout of text is fed by the content of
// a page, and what it makes of it is indexed by everything a viewer does
// with text.
func FuzzText(f *testing.F) {
	line := func(s string, x, y, size byte) []byte {
		var b []byte
		for i := range len(s) {
			b = append(b, s[i], x+byte(i)*size/16, y, size, 64, 0, 0, 0)
		}
		return b
	}
	f.Add(append(line("Hello world", 10, 20, 80), line("second line", 10, 25, 80)...), "world", false, uint16(2), uint16(9))
	f.Add(line("\x85\x86\x87 abc \xa1\xa2", 40, 9, 96), "abc", true, uint16(0), uint16(4))
	f.Add([]byte{'a', 0xff, 0xfe, 0xfd, 0xfc, 7, 0xff, 3, 'b', 1, 1, 0, 0, 4, 0x33, 0, 0xec, 9, 9, 99, 99, 1, 0x20, 0}, "a", false, uint16(1), uint16(1))
	f.Fuzz(func(t *testing.T, data []byte, query string, r2l bool, a, b uint16) {
		pt := layoutText(fuzzChars(data), nil, r2l)
		n := len(pt.Chars)

		// Lines hold the characters, blocks the lines, each once.
		at := 0
		for i, l := range pt.Lines {
			if l.Start != at || l.End < l.Start || l.End > n || l.Block < 0 || l.Block >= len(pt.Blocks) {
				t.Fatalf("line %d: %+v after character %d of %d", i, l, at, n)
			}
			at = l.End
		}
		if len(pt.Lines) > 0 && at != n {
			t.Fatalf("the lines end at character %d of %d", at, n)
		}
		at = 0
		for i, bl := range pt.Blocks {
			if bl.Start != at || bl.End <= bl.Start || bl.End > len(pt.Lines) {
				t.Fatalf("block %d: %+v after line %d of %d", i, bl, at, len(pt.Lines))
			}
			at = bl.End
		}
		if at != len(pt.Lines) {
			t.Fatalf("the blocks end at line %d of %d", at, len(pt.Lines))
		}

		// The text, and where each character is in it.
		s := pt.String()
		if !utf8.ValidString(s) {
			t.Fatalf("the text is not UTF-8: %q", s)
		}
		prev := 0
		for k := 0; k <= n; k++ {
			o := pt.Offset(TextPos(k))
			if o < prev || o > len(s) {
				t.Fatalf("offset %d of position %d after %d, in %d bytes", o, k, prev, len(s))
			}
			prev = o
			if p := pt.PosAtOffset(o); p < 0 || int(p) > n {
				t.Fatalf("offset %d is at position %d of %d", o, p, n)
			}
		}
		if all := pt.TextOf(pt.All(), TextFormat{}); all != s {
			t.Fatalf("the text of all characters is %q, String %q", all, s)
		}

		// Selections.
		valid := func(what string, r TextRange) {
			if r.Start < 0 || r.End < r.Start || int(r.End) > n {
				t.Fatalf("%s: %v of %d characters", what, r, n)
			}
		}
		pa, pb := TextPos(int(a)%(n+1)), TextPos(int(b)%(n+1))
		lo, hi := min(pa, pb), max(pa, pb)
		inner := TextRange{lo, hi}
		for g := SelectChars; g <= SelectBlocks; g++ {
			r := pt.Select(pa, pb, g)
			valid("selection", r)
			if r.Start > inner.Start || r.End < inner.End {
				t.Fatalf("selecting %v by unit %d gives %v", inner, g, r)
			}
			inner = r
			pt.Quads(r)
			if !utf8.ValidString(pt.TextOf(r, TextFormat{JoinHyphenated: true})) {
				t.Fatal("the text of a selection is not UTF-8")
			}
			pt.QuoteOf(r, int(b%40)-4)
		}
		valid("word", pt.WordAt(pa))
		valid("line", pt.LineAt(pa))
		valid("offsets", pt.RangeOfOffsets(int(a)%(len(s)+1), int(b)%(len(s)+1)))
		for _, r := range pt.InRect(Rect{float64(a % 512), float64(b % 512), float64(a%512) + 200, float64(b%512) + 100}) {
			valid("rectangle", r)
		}
		if n > 0 {
			c := pt.Chars[int(a)%n]
			if p := pt.PosAt(c.Origin[0], c.Origin[1]); p < 0 || int(p) > n {
				t.Fatalf("position %d of %d", p, n)
			}
			if k, ok := pt.CharAt(c.Origin[0], c.Origin[1]); ok && (k < 0 || k >= n) {
				t.Fatalf("character %d of %d", k, n)
			}
		}
		pt.PosAt(math.NaN(), float64(a))
		pt.CharAt(float64(a), math.Inf(1))

		// Search.
		for _, opt := range []SearchOptions{{}, {MatchCase: true, WholeWord: true}, {IgnoreDiacritics: true, Context: -1}} {
			end := TextPos(0)
			for i, m := range pt.Find(query, opt) {
				valid("match", m.Range)
				if m.Range.Start < end {
					t.Fatalf("match %d of %q at %v, the one before ends at %d", i, query, m.Range, end)
				}
				end = m.Range.End
				if i < 4 {
					if again, ok := pt.FindQuote(m.Quote, opt); ok {
						valid("quote", again.Range)
					}
				}
			}
		}
		for _, l := range pt.URLs() {
			if len(l.Quads) == 0 || l.Target.URI == "" {
				t.Fatalf("an address without a place or without an address: %+v", l)
			}
		}
	})
}
