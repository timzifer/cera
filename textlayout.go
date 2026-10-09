package cera

import (
	"math"
	"slices"
	"unicode"
	"unicode/utf8"
)

// Layout of the text of a page. The characters arrive in content order;
// layoutText
//   - drops the copies fake bold and fill-then-stroke draw on top of a
//     character,
//   - splits glyphs standing for several characters (ligatures) into one
//     character each, also the Latin ligatures U+FB00 to U+FB06,
//   - gathers characters into lines: consecutive characters on one
//     baseline, then lines on the same baseline, close but not
//     overlapping (text shown out of order),
//   - gathers lines into blocks: lines of the same direction and size,
//     one below the other, overlapping along the text,
//   - orders the blocks by the structure tree of a tagged PDF, the rest
//     by a recursive XY cut (columns before rows), text in other
//     directions after the main one.
//
// Distances are in em of the characters compared.
const (
	dupEm        = 0.1  // a copy is closer than this to the character
	lineAcrossEm = 0.5  // a line's baseline moves less than this
	lineBackEm   = 1    // the pen moves back at most this on a line
	lineGapEm    = 1.5  // a gap on a line is at most this; columns are further apart
	lineMergeEm  = 0.25 // lines on the same baseline differ by less
	mergeGapEm   = 1    // lines on the same baseline this close are one
	blockGapEm   = 1.7  // lines of a block are at most this far apart
	blockSize    = 1.5  // sizes of the lines of a block differ by less
)

// frame measures positions along a unit direction d, and across it
// towards the next line.
type frame struct{ d [2]float64 }

func (f frame) u(p [2]float64) float64 { return p[0]*f.d[0] + p[1]*f.d[1] }
func (f frame) v(p [2]float64) float64 { return -p[0]*f.d[1] + p[1]*f.d[0] }

// point returns the position at u along and v across.
func (f frame) point(u, v float64) [2]float64 {
	return [2]float64{u*f.d[0] - v*f.d[1], u*f.d[1] + v*f.d[0]}
}

// extent is a box in a frame.
type extent struct{ u0, v0, u1, v1 float64 }

func emptyExtent() extent {
	return extent{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
}

func (e *extent) addQuad(f frame, q *Quad) {
	for _, p := range q.P {
		u, v := f.u(p), f.v(p)
		e.u0, e.u1 = min(e.u0, u), max(e.u1, u)
		e.v0, e.v1 = min(e.v0, v), max(e.v1, v)
	}
}

func (e extent) quad(f frame) Quad {
	return Quad{[4][2]float64{f.point(e.u0, e.v0), f.point(e.u1, e.v0), f.point(e.u0, e.v1), f.point(e.u1, e.v1)}}
}

// sameDir reports whether two directions differ by less than about 8°.
func sameDir(a, b [2]float64) bool { return a[0]*b[0]+a[1]*b[1] > 0.99 }

// dirKey is a direction rounded to whole degrees, for grouping.
func dirKey(d [2]float64) int {
	k := int(math.Round(math.Atan2(d[1], d[0]) * 180 / math.Pi))
	if k <= -180 {
		k += 360
	}
	return k
}

// layoutLine is a line being laid out.
type layoutLine struct {
	chars []int // indices into the characters
	f     frame
	base  float64 // v of the baseline
	u0    float64 // along the text: first origin and furthest advance
	u1    float64
	size  float64 // largest em
	ext   extent  // of the character quads
	first int     // the line's first character in content order
	block int
}

// layoutBlock is a block being laid out.
type layoutBlock struct {
	lines   []int
	f       frame
	u0, u1  float64
	lo, hi  int     // the lines with the smallest and largest baseline
	spacing float64 // smallest distance of baselines, 0 for one line
	first   int
}

// layoutText lays out chars, in content order, and orders them for
// reading. ranks, when non-nil, is the place of marked-content identifiers
// in the structure tree. layoutText reuses chars.
func layoutText(chars []TextChar, ranks map[int]int) *PageText {
	chars = splitClusters(dropCopies(chars))
	lines := buildLines(chars)
	blocks := buildBlocks(lines)
	order := orderBlocks(chars, lines, blocks, ranks)

	t := &PageText{
		Chars:  make([]TextChar, 0, len(chars)),
		Lines:  make([]TextLine, 0, len(lines)),
		Blocks: make([]TextBlock, 0, len(blocks)),
	}
	for _, bi := range order {
		b := &blocks[bi]
		tb := TextBlock{Start: len(t.Lines), Bounds: Rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}}
		for _, li := range b.lines {
			l := &lines[li]
			tl := TextLine{Start: len(t.Chars), Quad: l.ext.quad(l.f), Dir: l.f.d, Block: len(t.Blocks)}
			for _, ci := range l.chars {
				t.Chars = append(t.Chars, chars[ci])
			}
			tl.End = len(t.Chars)
			r := tl.Quad.Bounds()
			tb.Bounds = Rect{min(tb.Bounds.X0, r.X0), min(tb.Bounds.Y0, r.Y0), max(tb.Bounds.X1, r.X1), max(tb.Bounds.Y1, r.Y1)}
			t.Lines = append(t.Lines, tl)
		}
		tb.End = len(t.Lines)
		for li := tb.Start; li+1 < tb.End; li++ {
			t.Lines[li].Hyphenated = t.hyphenated(li)
		}
		t.Blocks = append(t.Blocks, tb)
	}
	return t
}

// dropCopies removes from chars the characters drawn again on top of an
// earlier one: the same text in the same size and direction, less than
// dupEm away. It reuses chars.
func dropCopies(chars []TextChar) []TextChar {
	type cell struct{ x, y int }
	const size = 4.0 // points; copies further apart are not found
	at := func(p [2]float64) cell { return cell{int(math.Floor(p[0] / size)), int(math.Floor(p[1] / size))} }
	grid := map[cell][]int{}
	out := chars[:0]
	for i := range chars {
		c := &chars[i]
		if c.Text == "" || c.group != 0 || isSpace(c.Text) {
			out = append(out, *c)
			continue
		}
		tol := dupEm * c.Size
		k := at(c.Origin)
		copied := false
	search:
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				for _, j := range grid[cell{k.x + dx, k.y + dy}] {
					d := &out[j]
					if d.Text == c.Text && sameDir(d.Dir, c.Dir) && math.Abs(d.Size-c.Size) <= 0.05*c.Size &&
						math.Hypot(d.Origin[0]-c.Origin[0], d.Origin[1]-c.Origin[1]) < tol {
						copied = true
						break search
					}
				}
			}
		}
		if !copied {
			grid[k] = append(grid[k], len(out))
			out = append(out, *c)
		}
	}
	return out
}

// latinLigatures are the characters of U+FB00 to U+FB06.
var latinLigatures = [...]string{"ff", "fi", "fl", "ffi", "ffl", "st", "st"}

// cluster returns the length of the first character of s with the
// combining marks after it.
func cluster(s string) int {
	_, n := utf8.DecodeRuneInString(s)
	for n < len(s) {
		r, k := utf8.DecodeRuneInString(s[n:])
		if !unicode.Is(unicode.M, r) {
			break
		}
		n += k
	}
	return n
}

// splitClusters splits a character whose text is several characters into
// one each, a base with its combining marks, sharing its box evenly. It
// returns chars when nothing is split.
func splitClusters(chars []TextChar) []TextChar {
	extra := 0
	for i := range chars {
		c := &chars[i]
		if c.group != 0 {
			continue
		}
		if r, n := utf8.DecodeRuneInString(c.Text); n == len(c.Text) && r >= 0xfb00 && r <= 0xfb06 {
			c.Text = latinLigatures[r-0xfb00]
		}
		for s := c.Text; s != ""; extra++ {
			s = s[cluster(s):]
		}
		if c.Text != "" {
			extra--
		}
	}
	if extra == 0 {
		return chars
	}
	lerp := func(a, b [2]float64, t float64) [2]float64 {
		return [2]float64{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t}
	}
	out := make([]TextChar, 0, len(chars)+extra)
	for i := range chars {
		c := &chars[i]
		k := 0
		for s := c.Text; s != "" && c.group == 0; k++ {
			s = s[cluster(s):]
		}
		if k < 2 {
			out = append(out, *c)
			continue
		}
		q := c.Quad.P
		s := c.Text
		for j := range k {
			t0, t1 := float64(j)/float64(k), float64(j+1)/float64(k)
			p := *c
			n := cluster(s)
			p.Text, s = s[:n], s[n:]
			p.Quad = Quad{[4][2]float64{lerp(q[0], q[1], t0), lerp(q[0], q[1], t1), lerp(q[2], q[3], t0), lerp(q[2], q[3], t1)}}
			p.Box = p.Quad.Bounds()
			p.Origin[0] += c.Dir[0] * c.Advance * t0
			p.Origin[1] += c.Dir[1] * c.Advance * t0
			p.Advance = c.Advance / float64(k)
			out = append(out, p)
		}
	}
	return out
}

// continues reports whether c, shown after prev, is on its line.
func continues(prev, c *TextChar) bool {
	if !sameDir(prev.Dir, c.Dir) {
		return false
	}
	f := frame{prev.Dir}
	dp := [2]float64{c.Origin[0] - prev.Origin[0], c.Origin[1] - prev.Origin[1]}
	along, across := f.u(dp), f.v(dp)
	size := max(prev.Size, c.Size)
	return math.Abs(across) <= lineAcrossEm*size && along >= -lineBackEm*size && along-prev.Advance <= lineGapEm*size
}

// buildLines gathers the characters into lines, in content order of their
// first characters. Lines of white space alone are dropped.
func buildLines(chars []TextChar) []layoutLine {
	var lines []layoutLine
	for i := range chars {
		if n := len(lines); n > 0 {
			l := &lines[n-1]
			if continues(&chars[l.chars[len(l.chars)-1]], &chars[i]) {
				l.chars = append(l.chars, i)
				continue
			}
		}
		lines = append(lines, layoutLine{chars: []int{i}, first: i})
	}
	for i := range lines {
		lines[i].measure(chars)
	}
	lines = mergeLines(chars, lines)
	return slices.DeleteFunc(lines, func(l layoutLine) bool {
		for _, ci := range l.chars {
			if !isSpace(chars[ci].Text) {
				return false
			}
		}
		return true
	})
}

// measure sets the frame and extents of l from its characters.
func (l *layoutLine) measure(chars []TextChar) {
	c0 := &chars[l.chars[0]]
	l.f = frame{c0.Dir}
	l.base = l.f.v(c0.Origin)
	l.u0, l.u1, l.size = math.Inf(1), math.Inf(-1), 0
	l.ext = emptyExtent()
	for _, ci := range l.chars {
		c := &chars[ci]
		u := l.f.u(c.Origin)
		l.u0, l.u1 = min(l.u0, u), max(l.u1, u+c.Advance)
		l.size = max(l.size, c.Size)
		l.ext.addQuad(l.f, &c.Quad)
	}
}

// mergeLines joins lines on the same baseline that do not overlap and are
// no further apart than mergeGapEm: text shown out of order.
func mergeLines(chars []TextChar, lines []layoutLine) []layoutLine {
	idx := make([]int, len(lines))
	keys := make([]int, len(lines))
	for i := range lines {
		idx[i] = i
		keys[i] = dirKey(lines[i].f.d)
	}
	slices.SortFunc(idx, func(a, b int) int {
		if keys[a] != keys[b] {
			return keys[a] - keys[b]
		}
		return cmpFloat(lines[a].base, lines[b].base)
	})
	parent := make([]int, len(lines))
	for i := range parent {
		parent[i] = i
	}
	root := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	merged := false
	for x, a := range idx {
		la := &lines[a]
		for _, b := range idx[x+1:] {
			lb := &lines[b]
			size := max(la.size, lb.size)
			if keys[b] != keys[a] || lb.base-la.base > lineMergeEm*size {
				break
			}
			if !sameDir(la.f.d, lb.f.d) {
				continue
			}
			// lb in la's frame; the two must not overlap.
			u0, u1 := la.f.u(lb.f.point(lb.u0, lb.base)), la.f.u(lb.f.point(lb.u1, lb.base))
			gap := max(u0-la.u1, la.u0-u1)
			if gap >= -0.1*size && gap <= mergeGapEm*size {
				if ra, rb := root(a), root(b); ra != rb {
					parent[max(ra, rb)] = min(ra, rb)
					merged = true
				}
			}
		}
	}
	if !merged {
		return lines
	}
	out := lines[:0:0]
	at := map[int]int{} // root line → index in out
	for i := range lines {
		r := root(i)
		j, ok := at[r]
		if !ok {
			at[r] = len(out)
			out = append(out, layoutLine{first: lines[r].first})
			j = len(out) - 1
		}
		out[j].chars = append(out[j].chars, lines[i].chars...)
		out[j].first = min(out[j].first, lines[i].first)
	}
	for i := range out {
		l := &out[i]
		if len(l.chars) > 1 {
			f := frame{chars[l.chars[0]].Dir}
			slices.SortStableFunc(l.chars, func(a, b int) int {
				return cmpFloat(f.u(chars[a].Origin), f.u(chars[b].Origin))
			})
		}
		l.measure(chars)
	}
	slices.SortFunc(out, func(a, b layoutLine) int { return a.first - b.first })
	return out
}

// buildBlocks gathers the lines into blocks, the lines of each ordered by
// their baselines.
func buildBlocks(lines []layoutLine) []layoutBlock {
	var blocks []layoutBlock
	for li := range lines {
		l := &lines[li]
		best, bestDv := -1, math.Inf(1)
		for bi := range blocks {
			b := &blocks[bi]
			if !sameDir(b.f.d, l.f.d) {
				continue
			}
			base := b.f.v(l.f.point(l.u0, l.base))
			u0, u1 := b.f.u(l.f.point(l.u0, l.base)), b.f.u(l.f.point(l.u1, l.base))
			if u1 <= b.u0 || u0 >= b.u1 {
				continue // beside the block
			}
			// Below its last line or above its first.
			for _, end := range [2]struct {
				line int
				sign float64
			}{{b.hi, 1}, {b.lo, -1}} {
				o := &lines[end.line]
				size := max(o.size, l.size)
				if min(o.size, l.size)*blockSize < size {
					continue
				}
				dv := (base - o.base) * end.sign
				limit := blockGapEm * size
				if b.spacing > 0 {
					limit = min(limit, 1.3*b.spacing+0.1*size)
				}
				if dv > lineAcrossEm*size && dv <= limit && dv < bestDv {
					best, bestDv = bi, dv
				}
			}
		}
		if best < 0 {
			blocks = append(blocks, layoutBlock{lines: []int{li}, f: l.f, u0: l.u0, u1: l.u1, lo: li, hi: li, first: l.first})
			continue
		}
		b := &blocks[best]
		u0, u1 := b.f.u(l.f.point(l.u0, l.base)), b.f.u(l.f.point(l.u1, l.base))
		b.u0, b.u1 = min(b.u0, u0), max(b.u1, u1)
		b.lines = append(b.lines, li)
		if b.spacing == 0 || bestDv < b.spacing {
			b.spacing = bestDv
		}
		base := b.f.v(l.f.point(l.u0, l.base))
		if base > b.f.v(lines[b.hi].f.point(lines[b.hi].u0, lines[b.hi].base)) {
			b.hi = li
		} else {
			b.lo = li
		}
	}
	for bi := range blocks {
		b := &blocks[bi]
		f := b.f
		slices.SortStableFunc(b.lines, func(x, y int) int {
			lx, ly := &lines[x], &lines[y]
			px, py := lx.f.point(lx.u0, lx.base), ly.f.point(ly.u0, ly.base)
			if c := cmpFloat(f.v(px), f.v(py)); c != 0 {
				return c
			}
			return cmpFloat(f.u(px), f.u(py))
		})
		for _, li := range b.lines {
			lines[li].block = bi
		}
	}
	return blocks
}

// orderBlocks returns the blocks in reading order: those the structure
// tree places by the first place of their characters, then the others by
// their layout.
func orderBlocks(chars []TextChar, lines []layoutLine, blocks []layoutBlock, ranks map[int]int) []int {
	var rest []int
	var placed []int
	rank := make([]int, len(blocks))
	for bi := range blocks {
		rank[bi] = -1
		if ranks == nil {
			rest = append(rest, bi)
			continue
		}
		for _, li := range blocks[bi].lines {
			for _, ci := range lines[li].chars {
				c := &chars[ci]
				if r, ok := ranks[c.MCID]; ok && c.MCID >= 0 && !c.Artifact && (rank[bi] < 0 || r < rank[bi]) {
					rank[bi] = r
				}
			}
		}
		if rank[bi] >= 0 {
			placed = append(placed, bi)
		} else {
			rest = append(rest, bi)
		}
	}
	slices.SortStableFunc(placed, func(a, b int) int { return rank[a] - rank[b] })
	return append(placed, layoutOrder(chars, lines, blocks, rest)...)
}

// layoutOrder orders blocks by their position: the blocks of the direction
// with the most characters first, each direction by an XY cut in its own
// frame.
func layoutOrder(chars []TextChar, lines []layoutLine, blocks []layoutBlock, ids []int) []int {
	type group struct {
		key, n int
		ids    []int
	}
	var groups []*group
	byKey := map[int]*group{}
	for _, bi := range ids {
		k := dirKey(blocks[bi].f.d)
		g := byKey[k]
		if g == nil {
			g = &group{key: k}
			byKey[k] = g
			groups = append(groups, g)
		}
		g.ids = append(g.ids, bi)
		for _, li := range blocks[bi].lines {
			g.n += len(lines[li].chars)
		}
	}
	slices.SortStableFunc(groups, func(a, b *group) int { return b.n - a.n })
	var out []int
	for _, g := range groups {
		f := blocks[g.ids[0]].f
		ext := make(map[int]extent, len(g.ids))
		for _, bi := range g.ids {
			e := emptyExtent()
			for _, li := range blocks[bi].lines {
				q := lines[li].ext.quad(lines[li].f)
				e.addQuad(f, &q)
			}
			ext[bi] = e
		}
		out = xyCut(g.ids, ext, out)
	}
	return out
}

// xyCut appends ids to out in reading order: split in two at the widest
// gap that no block crosses, between columns or between rows (columns
// first when as wide), and so on in each part; without a gap, top to
// bottom, start to end. Cutting at the widest gap first keeps a page
// number in the gutter below the columns from making a column of its own.
func xyCut(ids []int, ext map[int]extent, out []int) []int {
	if len(ids) <= 1 {
		return append(out, ids...)
	}
	cols, colGap := widestGap(ids, ext, func(e extent) (float64, float64) { return e.u0, e.u1 })
	rows, rowGap := widestGap(ids, ext, func(e extent) (float64, float64) { return e.v0, e.v1 })
	switch {
	case cols[0] != nil && colGap >= rowGap:
		return xyCut(cols[1], ext, xyCut(cols[0], ext, out))
	case rows[0] != nil:
		return xyCut(rows[1], ext, xyCut(rows[0], ext, out))
	}
	ids = slices.Clone(ids)
	slices.SortStableFunc(ids, func(a, b int) int {
		if c := cmpFloat(ext[a].v0, ext[b].v0); c != 0 {
			return c
		}
		return cmpFloat(ext[a].u0, ext[b].u0)
	})
	return append(out, ids...)
}

// widestGap splits ids in two at the widest gap between the intervals
// span gives, and returns the parts and the gap; the parts are nil when
// there is no gap.
func widestGap(ids []int, ext map[int]extent, span func(extent) (float64, float64)) ([2][]int, float64) {
	s := slices.Clone(ids)
	slices.SortStableFunc(s, func(a, b int) int {
		x, _ := span(ext[a])
		y, _ := span(ext[b])
		return cmpFloat(x, y)
	})
	at, widest := -1, 0.0
	_, reach := span(ext[s[0]])
	for i := 1; i < len(s); i++ {
		lo, hi := span(ext[s[i]])
		if gap := lo - reach; gap > widest {
			at, widest = i, gap
		}
		reach = max(reach, hi)
	}
	if at < 0 {
		return [2][]int{}, 0
	}
	return [2][]int{s[:at], s[at:]}, widest
}

// hyphenated reports whether line li ends in a hyphen after a letter and
// the next line starts with a small letter: a word broken across them.
func (t *PageText) hyphenated(li int) bool {
	l, next := &t.Lines[li], &t.Lines[li+1]
	end := l.End
	for end > l.Start && isSpace(t.Chars[end-1].Text) {
		end--
	}
	if end-l.Start < 2 {
		return false
	}
	switch t.Chars[end-1].Text {
	case "-", "\u00ad", "\u2010":
	default:
		return false
	}
	before, _ := utf8.DecodeLastRuneInString(t.Chars[end-2].Text)
	if !unicode.IsLetter(before) {
		return false
	}
	for i := next.Start; i < next.End; i++ {
		if s := t.Chars[i].Text; !isSpace(s) {
			r, _ := utf8.DecodeRuneInString(s)
			return unicode.IsLower(r)
		}
	}
	return false
}
