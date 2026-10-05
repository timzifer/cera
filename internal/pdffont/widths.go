package pdffont

import (
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
	// Runs out of order or overlapping: give every identifier the width of
	// the last run naming it, then join neighbours of equal width again.
	// Each run spans less than 1<<20 identifiers (readCIDWidths), as in
	// v0.3.1, which kept one map entry per identifier.
	byCID := map[int64]float64{}
	for _, r := range runs {
		for c := r.lo; c <= r.hi; c++ {
			byCID[c] = r.w
		}
	}
	cids := make([]int64, 0, len(byCID))
	for c := range byCID {
		cids = append(cids, c)
	}
	slices.Sort(cids)
	var out []cidRun
	for _, c := range cids {
		w := byCID[c]
		if n := len(out); n > 0 && out[n-1].hi+1 == c && out[n-1].w == w {
			out[n-1].hi = c
			continue
		}
		out = append(out, cidRun{c, c, w})
	}
	return out
}
