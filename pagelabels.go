package cera

import (
	"slices"
	"strconv"
	"strings"

	"github.com/timzifer/cera/internal/pdf"
)

// Page labels (PDF 2.0, 12.4.2): the numbers printed on the pages — i to
// xii, then 1 and on, or A-1 — which a viewer shows in its page box and
// takes in "go to page". /PageLabels of the catalogue is a number tree
// from the index of a page to the labelling of the pages from there to the
// next entry: a numbering style, a prefix, and the number of the first
// page.

// labelRange labels the pages from start on.
type labelRange struct {
	start  int
	style  byte   // 'D', 'R', 'r', 'A', 'a', or 0: no number
	prefix string // before the number
	first  int    // the number of page start, 1 or more
}

// label returns the label of the page n pages after the start of r.
func (r labelRange) label(n int) string {
	v := r.first + n
	switch r.style {
	case 'D':
		return r.prefix + strconv.Itoa(v)
	case 'R':
		return r.prefix + roman(v)
	case 'r':
		return r.prefix + strings.ToLower(roman(v))
	case 'A', 'a':
		// A to Z, then AA to ZZ, AAA to ZZZ and so on.
		s := strings.Repeat(string(rune(int(r.style)+(v-1)%26)), (v-1)/26+1)
		return r.prefix + s
	}
	return r.prefix
}

// roman returns v in upper-case roman numerals; thousands are Ms.
func roman(v int) string {
	var b strings.Builder
	for ; v >= 1000 && b.Len() < 64; v -= 1000 {
		b.WriteByte('M')
	}
	v %= 1000
	for _, d := range []struct {
		n int
		s string
	}{{900, "CM"}, {500, "D"}, {400, "CD"}, {100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"}, {10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"}} {
		for v >= d.n {
			b.WriteString(d.s)
			v -= d.n
		}
	}
	return b.String()
}

// labelRanges returns the label ranges of the document in the order of
// their pages, nil without /PageLabels; read once. The tree is read
// leniently: entries in any order, one for a page the document does not
// have or with a key that is no page index left out, the later of two for
// one page taken, a start below 1 taken as 1, and a tree that comes back
// to itself read once.
func (d *Document) labelRanges() []labelRange {
	d.labelOnce.Do(func() {
		defer func() {
			if recover() != nil {
				d.labels = nil
			}
		}()
		cat, err := d.r.Catalog()
		if err != nil {
			return
		}
		root := cat.Get("PageLabels")
		if d.dict(root).IsZero() {
			return
		}
		var ranges []labelRange
		seen := map[pdf.Ref]bool{}
		nodes := 0
		var walk func(o pdf.Object, depth int)
		walk = func(o pdf.Object, depth int) {
			if ref, ok := o.Ref(); ok {
				if seen[ref] {
					return
				}
				seen[ref] = true
			}
			node := d.dict(o)
			if nodes++; node.IsZero() || depth > maxLabelDepth || nodes > maxLabelNodes {
				return
			}
			nums, _ := d.resolve(node.Get("Nums")).Array()
			for i := 0; i+1 < len(nums); i += 2 {
				start, ok := d.integer(nums[i])
				ld := d.dict(nums[i+1])
				if !ok || start < 0 || start >= d.NumPages() {
					continue
				}
				r := labelRange{start: start, first: 1}
				if s, _ := d.name(ld.Get("S")); len(s) == 1 && strings.Contains("DRrAa", string(s)) {
					r.style = s[0]
				}
				r.prefix = textString(d.resolve(ld.Get("P")))
				if st, ok := d.integer(ld.Get("St")); ok && st >= 1 {
					r.first = st
				}
				ranges = append(ranges, r)
			}
			kids, _ := d.resolve(node.Get("Kids")).Array()
			for _, k := range kids {
				walk(k, depth+1)
			}
		}
		walk(root, 0)
		slices.SortStableFunc(ranges, func(a, b labelRange) int { return a.start - b.start })
		// The later of two entries for one page.
		out := ranges[:0]
		for i, r := range ranges {
			if i+1 < len(ranges) && ranges[i+1].start == r.start {
				continue
			}
			out = append(out, r)
		}
		d.labels = out
	})
	return d.labels
}

// labelAt returns the range that labels page i and how far into it the
// page is; false for a page without a label.
func (d *Document) labelAt(i int) (labelRange, int, bool) {
	ranges := d.labelRanges()
	k, found := slices.BinarySearchFunc(ranges, i, func(r labelRange, i int) int { return r.start - i })
	if !found {
		k--
	}
	if k < 0 || i < 0 || i >= d.NumPages() {
		return labelRange{}, 0, false
	}
	return ranges[k], i - ranges[k].start, true
}

// PageLabel returns the label of page i (0-based): the number the document
// gives the page, as "iv", "12" or "A-3". It is empty for a document
// without page labels, for a page before the first that has one, and for
// a page labelled with nothing.
func (d *Document) PageLabel(i int) string {
	r, n, ok := d.labelAt(i)
	if !ok {
		return ""
	}
	return r.label(n)
}

// PageByLabel returns the first page with the given label, for a "go to
// page" that takes what is printed on the pages. It reports false when no
// page has the label, as in a document without page labels; a viewer then
// takes the input as a page number.
func (d *Document) PageByLabel(label string) (int, bool) {
	if label == "" || len(d.labelRanges()) == 0 {
		return 0, false
	}
	d.labelIdxOnce.Do(func() {
		d.labelIdx = map[string]int{}
		for i := range d.NumPages() {
			if l := d.PageLabel(i); l != "" {
				if _, ok := d.labelIdx[l]; !ok {
					d.labelIdx[l] = i
				}
			}
		}
	})
	i, ok := d.labelIdx[label]
	return i, ok
}

// pageLabels returns the /PageLabels of the result, which keeps the labels
// with their pages: the tree is keyed by the index of a page, so after
// pages are deleted, moved or put in front of others the labels would sit
// on other pages. The ranges are written anew for the pages of the result;
// a page from another document has its label when it was imported with it
// ([ImportOptions.Labels]), and else none. It reports false when no page
// has a label to keep, or none moved. Labels set by SetPageLabels are
// the labels instead; the dictionary is then zero for a result without.
func (e *Editor) pageLabels() (pdf.Dict, bool) {
	if e.labelsSet {
		// Set: these, or none where the document has some.
		labels := e.setLabels()
		return labels, !labels.IsZero() || e.base != nil && len(e.base.labelRanges()) > 0
	}
	keeps := func(p editPage) bool {
		return (p.own || p.labels) && len(p.src.labelRanges()) > 0
	}
	if !slices.ContainsFunc(e.pages, keeps) || e.base != nil && !e.pagesMoved() {
		return pdf.Dict{}, false
	}
	var nums pdf.Array
	var prev labelRange
	prevN, labelled := 0, false
	for k, p := range e.pages {
		var r labelRange
		n, has := 0, false
		if p.own || p.labels {
			r, n, has = p.src.labelAt(p.index)
		}
		// A page goes on with the range of the page before it when it
		// has the next number in the same style after the same prefix.
		if k > 0 && has == labelled && r.style == prev.style && r.prefix == prev.prefix &&
			(r.style == 0 || r.first+n == prev.first+prevN+1) {
			prev, prevN = r, n
			continue
		}
		prev, prevN, labelled = r, n, has
		var entries []pdf.Entry
		if r.style != 0 {
			entries = append(entries, pdf.Entry{Key: "S", Val: pdf.Name(string(r.style)).Object()})
			if v := r.first + n; v != 1 {
				entries = append(entries, pdf.Entry{Key: "St", Val: pdf.Integer(int64(v))})
			}
		}
		if r.prefix != "" {
			entries = append(entries, pdf.Entry{Key: "P", Val: pdf.String(encodeText(r.prefix))})
		}
		nums = append(nums, pdf.Integer(int64(k)), pdf.NewDict(entries...).Object())
	}
	return pdf.NewDict(pdf.Entry{Key: "Nums", Val: nums.Object()}), true
}

// pagesMoved reports whether a page of the result is not where it was in
// the edited document.
func (e *Editor) pagesMoved() bool {
	if e.base == nil || len(e.pages) != e.base.NumPages() {
		return true
	}
	for i, p := range e.pages {
		if !p.own || p.index != i {
			return true
		}
	}
	return false
}
