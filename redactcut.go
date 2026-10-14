package cera

import (
	"bytes"
	"math"
	"slices"
	"strconv"

	"github.com/timzifer/stilus"
)

// Cutting paths. A path that reaches into an area is clipped to what is
// outside, and its numbers are rewritten too, so that what lies in the
// area is not in the file to be read back: each segment is split where it
// crosses the edge of an area, and a run of pieces in the area is replaced
// by the straight line between where the run enters and leaves it. An
// area is convex, so the line stays in it, and the loop of the run and the
// line encloses only points of the area: what the path fills outside the
// area is the same.
//
// A stroke reaches beyond its path, by half its width and further at a
// join: for a path that is stroked the area is first narrowed by that
// much, so that the stroke of the line, and of the joins it makes, stays
// in the area. What of the path is nearer the edge than that stays; with
// mitre joins, as by default, that is up to the mitre limit, ten half
// widths.
//
// The pieces of a curve outside an area are the curve, but a renderer
// flattens a piece apart from the rest: along its edge a pixel may differ
// from the whole curve's by a few levels. A dashed stroke is not cut — a shorter path moves its
// dashes — and keeps its numbers, clipped. A path that fills is cut as a
// loop, its last point joined to its first; one only stroked loses the
// runs at its ends.

// cutSeg is a segment of a path in user space: a line from p[0] to p[1],
// or a cubic curve p[0] to p[3].
type cutSeg struct {
	cubic bool
	p     [4][2]float64
}

func (s cutSeg) end() [2]float64 {
	if s.cubic {
		return s.p[3]
	}
	return s.p[1]
}

// at returns the point of s at t.
func (s cutSeg) at(t float64) [2]float64 {
	if !s.cubic {
		return lerp2(s.p[0], s.p[1], t)
	}
	u := 1 - t
	var out [2]float64
	for i := range 2 {
		out[i] = u*u*u*s.p[0][i] + 3*u*u*t*s.p[1][i] + 3*u*t*t*s.p[2][i] + t*t*t*s.p[3][i]
	}
	return out
}

// split returns s before and after t.
func (s cutSeg) split(t float64) (cutSeg, cutSeg) {
	if !s.cubic {
		m := lerp2(s.p[0], s.p[1], t)
		return cutSeg{p: [4][2]float64{s.p[0], m}}, cutSeg{p: [4][2]float64{m, s.p[1]}}
	}
	a, b, c, d := s.p[0], s.p[1], s.p[2], s.p[3]
	ab, bc, cd := lerp2(a, b, t), lerp2(b, c, t), lerp2(c, d, t)
	abc, bcd := lerp2(ab, bc, t), lerp2(bc, cd, t)
	m := lerp2(abc, bcd, t)
	return cutSeg{cubic: true, p: [4][2]float64{a, ab, abc, m}}, cutSeg{cubic: true, p: [4][2]float64{m, bcd, cd, d}}
}

func lerp2(a, b [2]float64, t float64) [2]float64 {
	return [2]float64{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t}
}

// cutSub is a subpath: its segments from start, and whether it is closed.
type cutSub struct {
	start  [2]float64
	segs   []cutSeg
	closed bool
}

// parsePath reads the path construction operators of b — m, l, c, v, y,
// h, re, and W and W*, which it passes over — in the user space they are
// in. It reports false for anything else.
func parsePath(b []byte) ([]cutSub, bool) {
	var (
		subs     []cutSub
		nums     []float64
		cur      [2]float64
		has      bool // a current point
		startNew bool // the next segment starts a subpath at cur
	)
	sub := func() *cutSub {
		if startNew || len(subs) == 0 {
			subs = append(subs, cutSub{start: cur})
			startNew = false
		}
		return &subs[len(subs)-1]
	}
	for _, tok := range bytes.Fields(b) {
		if v, err := strconv.ParseFloat(string(tok), 64); err == nil {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, false
			}
			nums = append(nums, v)
			continue
		}
		op := string(tok)
		want := map[string]int{"m": 2, "l": 2, "c": 6, "v": 4, "y": 4, "h": 0, "re": 4, "W": 0, "W*": 0}
		n, ok := want[op]
		if !ok || len(nums) != n {
			return nil, false
		}
		v := nums
		nums = nums[:0]
		pt := func(i int) [2]float64 { return [2]float64{v[i], v[i+1]} }
		switch op {
		case "m":
			cur, has = pt(0), true
			subs = append(subs, cutSub{start: cur})
			startNew = false
		case "l", "c", "v", "y":
			if !has {
				return nil, false
			}
			s := sub()
			seg := cutSeg{p: [4][2]float64{cur, pt(0)}}
			switch op {
			case "c":
				seg = cutSeg{cubic: true, p: [4][2]float64{cur, pt(0), pt(2), pt(4)}}
			case "v":
				seg = cutSeg{cubic: true, p: [4][2]float64{cur, cur, pt(0), pt(2)}}
			case "y":
				seg = cutSeg{cubic: true, p: [4][2]float64{cur, pt(0), pt(2), pt(2)}}
			}
			s.segs = append(s.segs, seg)
			cur = seg.end()
		case "h":
			if !has || len(subs) == 0 || startNew {
				continue
			}
			s := &subs[len(subs)-1]
			if cur != s.start {
				s.segs = append(s.segs, cutSeg{p: [4][2]float64{cur, s.start}})
			}
			s.closed, cur, startNew = true, s.start, true
		case "re":
			x, y, w, h := v[0], v[1], v[2], v[3]
			subs = append(subs, cutSub{start: [2]float64{x, y}, closed: true, segs: []cutSeg{
				{p: [4][2]float64{{x, y}, {x + w, y}}},
				{p: [4][2]float64{{x + w, y}, {x + w, y + h}}},
				{p: [4][2]float64{{x + w, y + h}, {x, y + h}}},
				{p: [4][2]float64{{x, y + h}, {x, y}}},
			}})
			cur, has, startNew = [2]float64{x, y}, true, true
		}
	}
	return subs, len(nums) == 0
}

// cutArea is an area in user space: the half-planes of the edges of a
// convex quad, each n·p >= c inside, narrowed by a distance.
type cutArea struct {
	n [4][2]float64
	c [4]float64
}

// newCutArea returns the area of quad q (its corners in the order of
// Quad: lower left, lower right, upper left, upper right) narrowed by d,
// false for one without an inside.
func newCutArea(q Quad, d float64) (cutArea, bool) {
	v := [4][2]float64{q.P[0], q.P[1], q.P[3], q.P[2]}
	area := 0.0
	for i := range 4 {
		a, b := v[i], v[(i+1)%4]
		area += a[0]*b[1] - b[0]*a[1]
	}
	if math.Abs(area) < 1e-9 {
		return cutArea{}, false
	}
	var ca cutArea
	for i := range 4 {
		a, b := v[i], v[(i+1)%4]
		n := [2]float64{-(b[1] - a[1]), b[0] - a[0]} // left of the edge
		if area < 0 {
			n = [2]float64{-n[0], -n[1]}
		}
		l := math.Hypot(n[0], n[1])
		if l < 1e-12 {
			return cutArea{}, false
		}
		n = [2]float64{n[0] / l, n[1] / l}
		ca.n[i], ca.c[i] = n, n[0]*a[0]+n[1]*a[1]+d
	}
	return ca, true
}

// inside reports whether p is inside the area, off its edges.
func (a *cutArea) inside(p [2]float64) bool {
	for i := range 4 {
		if a.n[i][0]*p[0]+a.n[i][1]*p[1]-a.c[i] <= 1e-9 {
			return false
		}
	}
	return true
}

// crossings returns where s crosses the lines of the edges of the area,
// sorted, in (0, 1).
func (a *cutArea) crossings(s cutSeg) []float64 {
	var ts []float64
	for i := range 4 {
		f := func(p [2]float64) float64 { return a.n[i][0]*p[0] + a.n[i][1]*p[1] - a.c[i] }
		if !s.cubic {
			f0, f1 := f(s.p[0]), f(s.p[1])
			if (f0 < 0) != (f1 < 0) && f0 != f1 {
				ts = append(ts, f0/(f0-f1))
			}
			continue
		}
		// The distance along the normal is a cubic polynomial in t.
		p0, p1, p2, p3 := f(s.p[0]), f(s.p[1]), f(s.p[2]), f(s.p[3])
		ts = append(ts, cubicRoots(-p0+3*p1-3*p2+p3, 3*p0-6*p1+3*p2, -3*p0+3*p1, p0)...)
	}
	ts = slices.DeleteFunc(ts, func(t float64) bool { return !(t > 1e-9 && t < 1-1e-9) })
	slices.Sort(ts)
	return slices.CompactFunc(ts, func(x, y float64) bool { return math.Abs(x-y) < 1e-9 })
}

// cubicRoots returns the roots in [0, 1] of a t³ + b t² + c t + d: the
// interval split where the polynomial turns, each part bisected where it
// changes sign.
func cubicRoots(a, b, c, d float64) []float64 {
	f := func(t float64) float64 { return ((a*t+b)*t+c)*t + d }
	cuts := []float64{0, 1}
	// The turning points: roots of 3a t² + 2b t + c.
	qa, qb, qc := 3*a, 2*b, c
	switch {
	case math.Abs(qa) > 1e-15:
		if disc := qb*qb - 4*qa*qc; disc >= 0 {
			r := math.Sqrt(disc)
			cuts = append(cuts, (-qb-r)/(2*qa), (-qb+r)/(2*qa))
		}
	case math.Abs(qb) > 1e-15:
		cuts = append(cuts, -qc/qb)
	}
	cuts = slices.DeleteFunc(cuts, func(t float64) bool { return !(t >= 0 && t <= 1) })
	slices.Sort(cuts)
	var out []float64
	for i := 0; i+1 < len(cuts); i++ {
		lo, hi := cuts[i], cuts[i+1]
		flo, fhi := f(lo), f(hi)
		if flo == 0 {
			out = append(out, lo)
			continue
		}
		if (flo < 0) == (fhi < 0) {
			continue
		}
		for range 64 {
			mid := (lo + hi) / 2
			fm := f(mid)
			if (fm < 0) == (flo < 0) {
				lo, flo = mid, fm
			} else {
				hi = mid
			}
		}
		out = append(out, (lo+hi)/2)
	}
	return out
}

// cutPiece is a piece of a segment, on one side of the area.
type cutPiece struct {
	seg cutSeg
	in  bool
}

// pieces splits the segments of a subpath where they cross the edges of
// the area.
func (a *cutArea) pieces(segs []cutSeg) []cutPiece {
	var out []cutPiece
	for _, s := range segs {
		ts := a.crossings(s)
		rest, from := s, 0.0
		for _, t := range ts {
			var head cutSeg
			head, rest = rest.split((t - from) / (1 - from))
			from = t
			out = append(out, cutPiece{head, a.inside(head.at(0.5))})
		}
		out = append(out, cutPiece{rest, a.inside(rest.at(0.5))})
	}
	return out
}

// cut cuts the subpaths out of the area; fill says that the path is
// filled (so that a subpath is a loop), stroke that it is stroked. It
// reports false where it cannot: a subpath both filled and stroked that
// is open with an end in the area.
func (a *cutArea) cut(subs []cutSub, fill, stroke bool) ([]cutSub, bool) {
	var out []cutSub
	for _, s := range subs {
		if len(s.segs) == 0 {
			if !a.inside(s.start) {
				out = append(out, s)
			}
			continue
		}
		ps := a.pieces(s.segs)
		anyIn, allIn := false, true
		for _, p := range ps {
			anyIn = anyIn || p.in
			allIn = allIn && p.in
		}
		closingIn := false
		if !s.closed && fill {
			// Filled, it is closed by a line back to its start.
			close := cutSeg{p: [4][2]float64{s.segs[len(s.segs)-1].end(), s.start}}
			closingIn = a.inside(s.start) || a.inside(close.p[0])
		}
		switch {
		case !anyIn && !closingIn:
			out = append(out, s)
			continue
		case allIn && (s.closed || fill || !stroke):
			continue // it draws only in the area
		}
		loop := s.closed
		if !loop && closingIn {
			if stroke {
				return nil, false
			}
			// Only filled: a loop.
			s.segs = append(slices.Clip(s.segs), cutSeg{p: [4][2]float64{s.segs[len(s.segs)-1].end(), s.start}})
			ps = a.pieces(s.segs)
			loop = true
		}
		if loop {
			// From a piece outside, round to it.
			k := slices.IndexFunc(ps, func(p cutPiece) bool { return !p.in })
			if k < 0 {
				continue
			}
			ps = append(ps[k:], ps[:k]...)
			out = append(out, chords(ps, true))
			continue
		}
		// Open and stroked: the runs at its ends go, the others become
		// lines.
		first, last := -1, -1
		for i, p := range ps {
			if !p.in {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		if first < 0 {
			continue
		}
		out = append(out, chords(ps[first:last+1], false))
	}
	return out, true
}

// chords makes a subpath of pieces that starts outside: each run of
// pieces in the area replaced by a line.
func chords(ps []cutPiece, closed bool) cutSub {
	s := cutSub{start: ps[0].seg.p[0], closed: closed}
	for i := 0; i < len(ps); i++ {
		if !ps[i].in {
			s.segs = append(s.segs, ps[i].seg)
			continue
		}
		from := ps[i].seg.p[0]
		for i+1 < len(ps) && ps[i+1].in {
			i++
		}
		s.segs = append(s.segs, cutSeg{p: [4][2]float64{from, ps[i].seg.end()}})
	}
	return s
}

// writePath writes subpaths as path construction operators.
func writePath(subs []cutSub) []byte {
	var b []byte
	num := func(v float64) {
		if v == 0 {
			v = 0 // no -0
		}
		b = strconv.AppendFloat(b, v, 'f', -1, 64)
		b = append(b, ' ')
	}
	for _, s := range subs {
		num(s.start[0])
		num(s.start[1])
		b = append(b, "m\n"...)
		for k, seg := range s.segs {
			if s.closed && k == len(s.segs)-1 && !seg.cubic && seg.end() == s.start {
				break // h draws it
			}
			if seg.cubic {
				for _, p := range seg.p[1:] {
					num(p[0])
					num(p[1])
				}
				b = append(b, "c\n"...)
				continue
			}
			num(seg.p[1][0])
			num(seg.p[1][1])
			b = append(b, "l\n"...)
		}
		if s.closed {
			b = append(b, "h\n"...)
		}
	}
	return b
}

// cutPath rewrites the path under construction, which paint (a painting
// operator, or n for a clip) ends, cut out of the areas. It reports false
// where it leaves it as it is: a dashed stroke, a path it cannot read, one
// left with nothing.
func (fr *redFrame) cutPath(in *interp, paint string) bool {
	if len(fr.path) == 0 || fr.mixed {
		return false
	}
	stroke := paint == "S" || paint == "s" || paint == "B" || paint == "B*" || paint == "b" || paint == "b*"
	fill := paint != "S" && paint != "s"
	st := &in.gs.style
	if stroke && len(st.Dash) > 0 {
		return false
	}
	from, to := fr.path[0][0], fr.path[len(fr.path)-1][1]
	subs, ok := parsePath(fr.out[from:to])
	if !ok || len(subs) == 0 {
		return false
	}
	closes := paint == "s" || paint == "b" || paint == "b*"
	if last := &subs[len(subs)-1]; closes && !last.closed && len(last.segs) > 0 {
		// The operator closes it; what is written closes it itself, and
		// is painted by the operator that does not (paintAs).
		if e := last.segs[len(last.segs)-1].end(); e != last.start {
			last.segs = append(last.segs, cutSeg{p: [4][2]float64{e, last.start}})
		}
		last.closed = true
	}
	if stroke {
		for k := range subs {
			// A line there and back, closed: stroked as the line, as
			// the renderer strokes it, and not as a loop with more
			// points, whose two halves it would cover twice.
			s := &subs[k]
			if s.closed && len(s.segs) == 2 && !s.segs[0].cubic && !s.segs[1].cubic &&
				s.segs[1].p[0] == s.segs[0].p[1] && s.segs[1].p[1] == s.segs[0].p[0] {
				s.segs, s.closed = s.segs[:1], false
			}
		}
	}
	inv, ok := fr.ctm.Invert()
	if !ok {
		return false
	}
	// How far a stroke reaches from its path: half its width, at a
	// square cap's corner √2 of it, at a mitre join up to the limit; a
	// hair is taken as a unit of user space.
	d := 0.0
	if stroke {
		d = 1
		if st.Width > 0 {
			reach := math.Sqrt2
			if st.Join == stilus.MiterJoin {
				reach = max(st.MiterLimit, reach)
			}
			d = st.Width / 2 * reach
		}
	}
	for _, a := range fr.r.areas {
		ca, ok := newCutArea(a.Transform(inv), d)
		if !ok {
			continue
		}
		if subs, ok = ca.cut(subs, fill, stroke); !ok {
			return false
		}
	}
	if len(subs) == 0 {
		return false
	}
	out := writePath(subs)
	if fr.clipOp != "" {
		out = append(append(out, fr.clipOp...), '\n')
	}
	fr.out = slices.Replace(fr.out, from, to, out...)
	fr.path = append(fr.path[:0], [2]int{from, from + len(out)})
	if closes {
		fr.paintAs = map[string]string{"s": "S", "b": "B", "b*": "B*"}[paint]
	}
	return true
}
