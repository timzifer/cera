package pdffont

import (
	"cmp"
	"math"
	"slices"
)

// cidWidths holds a composite font's widths as runs of identifiers, sorted
// and disjoint, found by halving. /W arrays name tens of thousands of
// identifiers in a few hundred runs.
type cidWidths []cidRun

type cidRun struct {
	lo, hi int64
	w      float64
}

func (c cidWidths) get(cid int) (float64, bool) {
	v := int64(cid)
	i, _ := slices.BinarySearchFunc(c, v, func(r cidRun, v int64) int {
		switch {
		case r.hi < v:
			return -1
		case r.lo > v:
			return 1
		}
		return 0
	})
	if i < len(c) && c[i].lo <= v && v <= c[i].hi {
		return c[i].w, true
	}
	return 0, false
}

// A cidWidthBuilder collects runs in the order the array names them; a later
// run wins where it overlaps an earlier one.
type cidWidthBuilder struct {
	runs []cidRun
}

func (b *cidWidthBuilder) add(lo, hi int64, w float64) {
	if lo < math.MinInt32 || hi > math.MaxInt32 {
		return
	}
	// Single identifiers written one after another with the same width
	// join the run before them.
	if n := len(b.runs); n > 0 && b.runs[n-1].hi+1 == lo && b.runs[n-1].w == w {
		b.runs[n-1].hi = hi
		return
	}
	b.runs = append(b.runs, cidRun{lo, hi, w})
}

// build makes the runs disjoint and sorted. Arrays almost always name runs
// in ascending order without overlap, which costs nothing here.
func (b *cidWidthBuilder) build() cidWidths {
	runs := b.runs
	ordered := true
	for i := 1; i < len(runs); i++ {
		if runs[i].lo <= runs[i-1].hi {
			ordered = false
			break
		}
	}
	if ordered {
		return slices.Clip(runs)
	}
	// Lay the runs down in order, each cutting what it covers out of the
	// ones before it.
	var out []cidRun
	for _, r := range runs {
		var next []cidRun
		for _, o := range out {
			if o.hi < r.lo || o.lo > r.hi {
				next = append(next, o)
				continue
			}
			if o.lo < r.lo {
				next = append(next, cidRun{o.lo, r.lo - 1, o.w})
			}
			if o.hi > r.hi {
				next = append(next, cidRun{r.hi + 1, o.hi, o.w})
			}
		}
		out = append(next, r)
	}
	slices.SortFunc(out, func(a, b cidRun) int { return cmp.Compare(a.lo, b.lo) })
	return out
}
