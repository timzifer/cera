package cera

import (
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// SearchOptions control Find.
type SearchOptions struct {
	// MatchCase tells upper and lower case apart.
	MatchCase bool
	// WholeWord finds the query only where it does not continue a word
	// before or after it.
	WholeWord bool
	// IgnoreDiacritics finds "resume" in "résumé"; it also reads
	// compatibility characters as what they stand for, such as "2" for
	// "²".
	IgnoreDiacritics bool
	// Context is how many characters of the text before and after a match
	// its Quote holds: 0 means 32, a negative number none.
	Context int
}

// Quote is a passage of text with some of the text before and after it,
// as a W3C TextQuoteSelector: an anchor that finds the passage again
// after the text was extracted anew, or in another text layer of the
// same document. White space in it is one space.
type Quote struct {
	Prefix, Exact, Suffix string
}

// TextMatch is a passage found in the text of a page.
type TextMatch struct {
	// Range is the characters of the passage; Quads draws them.
	Range TextRange
	// Quote is its text, with the text around it.
	Quote Quote
}

// Find returns the places of query in the text, in reading order and not
// overlapping. The text is searched as String has it, with runs of white
// space as one space, soft hyphens left out and words broken across lines
// (TextLine.Hyphenated) joined; so is the query. Places that share
// characters, as two in the text of one /ActualText do, are one place, the
// first.
func (t *PageText) Find(query string, opt SearchOptions) []TextMatch {
	c := t.searchCache()
	n := c.normText(t, opt)
	var out []TextMatch
	for _, m := range n.find(n.query(query), opt.WholeWord) {
		tm := c.match(t, n, m[0], m[1], opt.Context)
		if len(out) > 0 && tm.Range.Start < out[len(out)-1].Range.End {
			continue
		}
		out = append(out, tm)
	}
	return out
}

// FindQuote returns the place of q.Exact in the text whose text before
// and after it agrees most with q.Prefix and q.Suffix; the first such
// place on a tie. opt.WholeWord is ignored.
func (t *PageText) FindQuote(q Quote, opt SearchOptions) (TextMatch, bool) {
	c := t.searchCache()
	n := c.normText(t, opt)
	ms := n.find(n.query(q.Exact), false)
	if len(ms) == 0 {
		return TextMatch{}, false
	}
	prefix, suffix := n.query(q.Prefix), n.query(q.Suffix)
	best, bestScore := 0, -1
	for k, m := range ms {
		score := commonSuffix(strings.TrimRight(n.s[:m[0]], " "), prefix) + commonPrefix(strings.TrimLeft(n.s[m[1]:], " "), suffix)
		if score > bestScore {
			best, bestScore = k, score
		}
	}
	return c.match(t, n, ms[best][0], ms[best][1], opt.Context), true
}

// QuoteOf returns the text of r with context characters of the text before
// and after it (0 means 32): the anchor FindQuote finds r by again.
func (t *PageText) QuoteOf(r TextRange, context int) Quote {
	j := t.searchCache().joinedText(t)
	start, end := min(max(int(r.Start), 0), len(t.Chars)), min(max(int(r.End), 0), len(t.Chars))
	if start >= end {
		return Quote{}
	}
	return j.quote(int(j.starts[start]), int(j.ends[end-1]), context)
}

// commonSuffix returns the number of runes a and b end in alike.
func commonSuffix(a, b string) int {
	n := 0
	for a != "" && b != "" {
		ra, sa := utf8.DecodeLastRuneInString(a)
		rb, sb := utf8.DecodeLastRuneInString(b)
		if ra != rb {
			break
		}
		a, b, n = a[:len(a)-sa], b[:len(b)-sb], n+1
	}
	return n
}

// commonPrefix returns the number of runes a and b start with alike.
func commonPrefix(a, b string) int {
	n := 0
	for a != "" && b != "" {
		ra, sa := utf8.DecodeRuneInString(a)
		rb, sb := utf8.DecodeRuneInString(b)
		if ra != rb {
			break
		}
		a, b, n = a[sa:], b[sb:], n+1
	}
	return n
}

// match returns the match of the bytes n.s[i:j] of the text of t.
func (c *searchCache) match(t *PageText, n *normText, i, j, context int) TextMatch {
	jt := c.joinedText(t)
	lo, hi := int(n.lo[i]), int(n.hi[j-1])
	r := TextRange{Start: TextPos(sort.Search(len(jt.ends), func(k int) bool { return int(jt.ends[k]) > lo }))}
	r.End = max(r.Start, TextPos(sort.Search(len(jt.ends), func(k int) bool { return int(jt.starts[k]) >= hi })))
	return TextMatch{Range: t.wholeGroups(r), Quote: jt.quote(lo, hi, context)}
}

// searchCache holds the text of a page made for searching, on first use:
// the text with hyphenated words joined, and normalised from it for each
// of MatchCase and IgnoreDiacritics.
type searchCache struct {
	joinedOnce sync.Once
	joined     joinedText
	once       [4]sync.Once
	norm       [4]normText
	urlsOnce   sync.Once
	urls       []Link
}

// joinedText is the text of a page with the words broken across lines
// joined, and where the text of each character starts and ends in it.
type joinedText struct {
	s            string
	starts, ends []int32
}

// normText is text normalised for searching. The bytes s[i] come from the
// bytes [lo[i], hi[i]) of the joined text.
type normText struct {
	s      string
	lo, hi []int32
	fold   bool // case folded
	strip  bool // diacritics removed
}

// searchCache returns the search cache of t, or a new one for a PageText
// made by hand.
func (t *PageText) searchCache() *searchCache {
	if t.search != nil {
		return t.search
	}
	return new(searchCache)
}

// joinedText returns the text of t with words broken across lines joined.
func (c *searchCache) joinedText(t *PageText) *joinedText {
	c.joinedOnce.Do(func() {
		j := &c.joined
		j.s, j.starts, j.ends = t.write(0, len(t.Chars), TextFormat{JoinHyphenated: true}, true)
	})
	return &c.joined
}

// normText returns the text of t normalised for opt.
func (c *searchCache) normText(t *PageText, opt SearchOptions) *normText {
	k := 0
	if !opt.MatchCase {
		k |= 1
	}
	if opt.IgnoreDiacritics {
		k |= 2
	}
	c.once[k].Do(func() {
		c.norm[k] = normalize(c.joinedText(t).s, k&1 != 0, k&2 != 0)
	})
	return &c.norm[k]
}

// normalize returns s with runs of white space as one space and soft
// hyphens left out, case folded when fold is set and with diacritics
// removed when strip is.
func normalize(s string, fold, strip bool) normText {
	n := normText{fold: fold, strip: strip}
	var b strings.Builder
	b.Grow(len(s))
	lo, hi := make([]int32, 0, len(s)), make([]int32, 0, len(s))
	put := func(r rune, i, j int) {
		if fold {
			r = foldRune(r)
		}
		k := b.Len()
		b.WriteRune(r)
		for ; k < b.Len(); k++ {
			lo, hi = append(lo, int32(i)), append(hi, int32(j))
		}
	}
	space := false
	var buf [64]byte
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		j := i + w
		switch {
		case r == '\u00ad':
		case unicode.IsSpace(r):
			if !space {
				put(' ', i, j)
				space = true
			}
		case strip && r >= 0x80:
			space = false
			for _, d := range string(norm.NFKD.AppendString(buf[:0], s[i:j])) {
				if !unicode.Is(unicode.Mn, d) {
					put(d, i, j)
				}
			}
		default:
			space = false
			put(r, i, j)
		}
		i = j
	}
	n.s, n.lo, n.hi = b.String(), lo, hi
	return n
}

// foldRune returns the rune that stands for r and the runes of other case
// equal to it: the smallest of its case orbit.
func foldRune(r rune) rune {
	m := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		m = min(m, f)
	}
	return m
}

// query returns q normalised as n is, without white space at its ends.
func (n *normText) query(q string) string {
	return strings.Trim(normalize(q, n.fold, n.strip).s, " ")
}

// find returns the places of q in n.s as byte ranges, not overlapping.
func (n *normText) find(q string, wholeWord bool) [][2]int {
	if q == "" {
		return nil
	}
	var out [][2]int
	for at := 0; at < len(n.s); {
		i := strings.Index(n.s[at:], q)
		if i < 0 {
			break
		}
		i += at
		j := i + len(q)
		if wholeWord && !n.wordBounds(i, j) {
			_, w := utf8.DecodeRuneInString(n.s[i:])
			at = i + w
			continue
		}
		out = append(out, [2]int{i, j})
		at = j
	}
	return out
}

// wordBounds reports whether n.s[i:j] continues no word before or after.
func (n *normText) wordBounds(i, j int) bool {
	first, _ := utf8.DecodeRuneInString(n.s[i:])
	last, _ := utf8.DecodeLastRuneInString(n.s[:j])
	before, _ := utf8.DecodeLastRuneInString(n.s[:i])
	after, _ := utf8.DecodeRuneInString(n.s[j:])
	return (i == 0 || !isWordRune(before) || !isWordRune(first)) && (j == len(n.s) || !isWordRune(after) || !isWordRune(last))
}

// isWordRune reports whether r is part of a word.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_'
}

// quote returns the bytes s[lo:hi] with context runes before and after.
func (j *joinedText) quote(lo, hi, context int) Quote {
	if context == 0 {
		context = 32
	}
	q := Quote{Exact: collapseSpace(j.s[lo:hi])}
	if context < 0 {
		return q
	}
	pre := collapseSpace(j.s[runeStart(j.s, max(lo-8*context, 0)):lo])
	if k := utf8.RuneCountInString(pre) - context; k > 0 {
		for ; k > 0; k-- {
			_, w := utf8.DecodeRuneInString(pre)
			pre = pre[w:]
		}
	}
	post := collapseSpace(j.s[hi:runeStart(j.s, min(hi+8*context, len(j.s)))])
	for i, k := 0, 0; i < len(post); k++ {
		if k == context {
			post = post[:i]
			break
		}
		_, w := utf8.DecodeRuneInString(post[i:])
		i += w
	}
	q.Prefix, q.Suffix = pre, post
	return q
}

// runeStart returns i, or the start of the rune of s it is inside.
func runeStart(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// collapseSpace returns s with each run of white space as one space.
func collapseSpace(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}
