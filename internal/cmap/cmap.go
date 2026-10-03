// Package cmap reads the codes of composite fonts: which bytes of a string
// make up one code, and which character identifier (CID) each code selects
// (PDF 2.0 9.7.5). A CMap is either one of the predefined ones (compiled
// into this package from Adobe's CMap resources, see Predefined) or a CMap
// program embedded in the document (Parse).
//
// The package also knows what the CIDs of the four Adobe CJK character
// collections stand for in Unicode (ToUnicode), which is what text
// extraction needs when a document carries no ToUnicode map, and what a
// Unicode-keyed stand-in font needs to find a glyph.
package cmap

import (
	"sort"
)

// CMap maps codes to CIDs.
type CMap struct {
	// Name is the CMap's name, empty for an embedded one that gives none.
	Name string
	// WMode is the writing mode: 0 horizontal, 1 vertical.
	WMode int
	// Registry, Ordering and Supplement are the character collection the
	// CIDs belong to (the CMap's CIDSystemInfo).
	Registry, Ordering string
	Supplement         int

	// identity maps every two-byte code to the CID of the same value.
	identity bool
	spaces   []space
	ranges   []span // by (n, lo)
	notdef   []span // by (n, lo)
	parent   *CMap  // usecmap
}

// space is a codespace range of n bytes, checked byte by byte.
type space struct {
	n      uint8
	lo, hi [4]byte
}

// span maps the n-byte codes lo..hi to the CIDs cid, cid+step, ...: step
// is 1 for a cidrange, 0 for a notdefrange.
type span struct {
	lo, hi uint32
	cid    uint32
	n      uint8
	step   uint8
	seq    int32 // definition order, while sorting
}

// Limits keep a hostile CMap program from growing without bound.
const (
	MaxSpans  = 1 << 17
	MaxSpaces = 64
	MaxDepth  = 4 // usecmap nesting
)

// Identity returns Identity-H (vertical false) or Identity-V.
func Identity(vertical bool) *CMap {
	if vertical {
		return identityV
	}
	return identityH
}

var (
	identityH = &CMap{Name: "Identity-H", Registry: "Adobe", Ordering: "Identity", identity: true}
	identityV = &CMap{Name: "Identity-V", Registry: "Adobe", Ordering: "Identity", identity: true, WMode: 1}
)

// IsIdentity reports Identity-H or Identity-V: two-byte codes that are
// their own CIDs.
func (c *CMap) IsIdentity() bool { return c.identity }

// Next reads the first code of s: its value and its length in bytes
// (0 only for an empty s). Bytes that match no codespace range are read
// as one code of the length of the shortest range whose first byte
// matches, or one byte; such codes have CID 0.
func (c *CMap) Next(s []byte) (code uint32, n int) {
	if len(s) == 0 {
		return 0, 0
	}
	if c.identity {
		if len(s) < 2 {
			return uint32(s[0]), 1
		}
		return uint32(s[0])<<8 | uint32(s[1]), 2
	}
	spaces := c.codespace()
	if len(spaces) == 0 {
		// No codespace at all: two bytes, as composite fonts mostly use.
		if len(s) < 2 {
			return uint32(s[0]), 1
		}
		return uint32(s[0])<<8 | uint32(s[1]), 2
	}
	for k := 1; k <= 4 && k <= len(s); k++ {
		for i := range spaces {
			sp := &spaces[i]
			if int(sp.n) != k || !sp.contains(s) {
				continue
			}
			return value(s[:k]), k
		}
	}
	// No match: as many bytes as the shortest range starting like s.
	n = 0
	for i := range spaces {
		sp := &spaces[i]
		if s[0] >= sp.lo[0] && s[0] <= sp.hi[0] && (n == 0 || int(sp.n) < n) {
			n = int(sp.n)
		}
	}
	if n == 0 {
		n = 1
	}
	n = min(n, len(s))
	return value(s[:n]), n
}

// codespace returns the codespace ranges, inherited through usecmap.
func (c *CMap) codespace() []space {
	for m := c; m != nil; m = m.parent {
		if len(m.spaces) > 0 {
			return m.spaces
		}
	}
	return nil
}

func (sp *space) contains(s []byte) bool {
	for i := 0; i < int(sp.n); i++ {
		if s[i] < sp.lo[i] || s[i] > sp.hi[i] {
			return false
		}
	}
	return true
}

func value(b []byte) uint32 {
	var v uint32
	for _, x := range b {
		v = v<<8 | uint32(x)
	}
	return v
}

// CID returns the CID of the n-byte code, 0 if the CMap does not map it.
func (c *CMap) CID(code uint32, n int) int {
	if c.identity {
		return int(code)
	}
	for m := c; m != nil; m = m.parent {
		if cid, ok := lookup(m.ranges, code, n); ok {
			return cid
		}
	}
	for m := c; m != nil; m = m.parent {
		if cid, ok := lookup(m.notdef, code, n); ok {
			return cid
		}
	}
	return 0
}

func lookup(spans []span, code uint32, n int) (int, bool) {
	i := sort.Search(len(spans), func(i int) bool {
		s := &spans[i]
		return int(s.n) > n || (int(s.n) == n && s.hi >= code)
	})
	if i < len(spans) {
		s := &spans[i]
		if int(s.n) == n && s.lo <= code && code <= s.hi {
			return int(s.cid + (code-s.lo)*uint32(s.step)), true
		}
	}
	return 0, false
}

// sortSpans orders spans for lookup and resolves overlaps: a code defined
// twice takes its later definition, as it would in the PostScript
// interpreter that runs a CMap. spans are in definition order.
func sortSpans(spans []span) []span {
	for i := range spans {
		spans[i].seq = int32(i)
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].n != spans[j].n {
			return spans[i].n < spans[j].n
		}
		return spans[i].lo < spans[j].lo
	})
	overlap := false
	for i := 1; i < len(spans); i++ {
		if spans[i].n == spans[i-1].n && spans[i].lo <= spans[i-1].hi {
			overlap = true
			break
		}
	}
	if !overlap {
		return spans
	}
	var out []span
	for start := 0; start < len(spans); {
		end := start + 1
		for end < len(spans) && spans[end].n == spans[start].n {
			end++
		}
		out = paint(out, spans[start:end])
		start = end
	}
	return out
}

// paint appends the visible parts of spans (of one length, sorted by lo)
// to out: where spans overlap, the one with the higher seq.
func paint(out []span, spans []span) []span {
	// Breakpoints are every lo and hi+1; between two of them the same
	// spans are active. Active spans are kept in a heap by seq, removed
	// lazily once they end.
	type point struct {
		at  uint64
		idx int // the span starting here, or -1
	}
	pts := make([]point, 0, 2*len(spans))
	for i := range spans {
		pts = append(pts, point{uint64(spans[i].lo), i}, point{uint64(spans[i].hi) + 1, -1})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].at < pts[j].at })
	var heap []int // indices into spans, max seq on top
	less := func(a, b int) bool { return spans[heap[a]].seq > spans[heap[b]].seq }
	push := func(x int) {
		heap = append(heap, x)
		for i := len(heap) - 1; i > 0; {
			p := (i - 1) / 2
			if !less(i, p) {
				break
			}
			heap[i], heap[p] = heap[p], heap[i]
			i = p
		}
	}
	pop := func() {
		last := len(heap) - 1
		heap[0] = heap[last]
		heap = heap[:last]
		for i := 0; ; {
			l, r, m := 2*i+1, 2*i+2, i
			if l < len(heap) && less(l, m) {
				m = l
			}
			if r < len(heap) && less(r, m) {
				m = r
			}
			if m == i {
				break
			}
			heap[i], heap[m] = heap[m], heap[i]
			i = m
		}
	}
	for k := 0; k < len(pts); {
		at := pts[k].at
		for ; k < len(pts) && pts[k].at == at; k++ {
			if pts[k].idx >= 0 {
				push(pts[k].idx)
			}
		}
		for len(heap) > 0 && uint64(spans[heap[0]].hi) < at {
			pop()
		}
		if len(heap) == 0 || k == len(pts) {
			continue
		}
		next := pts[k].at
		top := &spans[heap[0]]
		hi := min(next-1, uint64(top.hi))
		seg := span{lo: uint32(at), hi: uint32(hi), cid: top.cid + (uint32(at)-top.lo)*uint32(top.step), n: top.n, step: top.step, seq: top.seq}
		if l := len(out) - 1; l >= 0 && out[l].seq == seg.seq && out[l].n == seg.n && out[l].hi+1 == seg.lo {
			out[l].hi = seg.hi
		} else {
			out = append(out, seg)
		}
	}
	return out
}

// Parent returns the CMap this one uses (usecmap), nil if none.
func (c *CMap) Parent() *CMap { return c.parent }

// Each calls fn for every code the CMap itself maps to a CID (not those of
// its parent, nor notdef ranges), in code order.
func (c *CMap) Each(fn func(code uint32, n int, cid int)) {
	for _, s := range c.ranges {
		for code := uint64(s.lo); code <= uint64(s.hi); code++ {
			fn(uint32(code), int(s.n), int(s.cid+(uint32(code)-s.lo)*uint32(s.step)))
		}
	}
}

// Lookup returns the CID of a code whose length is not known: that of the
// shortest codespace range the code fits.
func (c *CMap) Lookup(code uint32) int {
	if c.identity {
		return int(code)
	}
	spaces := c.codespace()
	if len(spaces) == 0 {
		return c.CID(code, 2)
	}
	var b [4]byte
	for n := 1; n <= 4; n++ {
		if n < 4 && code >= 1<<(8*n) {
			continue
		}
		for i := range n {
			b[i] = byte(code >> (8 * (n - 1 - i)))
		}
		for i := range spaces {
			if sp := &spaces[i]; int(sp.n) == n && sp.contains(b[:n]) {
				return c.CID(code, n)
			}
		}
	}
	return 0
}

// WithParent returns a copy of c that uses parent for codes it does not map
// itself.
func (c *CMap) WithParent(parent *CMap) *CMap {
	d := *c
	d.parent = parent
	if d.Ordering == "" {
		d.Registry, d.Ordering, d.Supplement = parent.Registry, parent.Ordering, parent.Supplement
	}
	return &d
}
