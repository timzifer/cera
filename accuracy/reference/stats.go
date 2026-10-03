package main

import (
	"image"
	"runtime"
	"slices"
	"sync"

	"github.com/timzifer/cera/accuracy/metric"
)

// tol is the difference, in 1/255 steps per channel, within which two
// renderings agree (ADR 0010).
const tol = metric.OverLevels

// pageStats are the pixel counts of one page. Shares are taken over the
// inked area: pixels that are not paper white in some rendering, so that a
// page of mostly paper does not dilute them.
type pageStats struct {
	ink int64
	// pair[a][b] counts inked pixels on which engines a and b differ.
	pair map[string]map[string]int64
	// For each engine, against the median of the others: pixels on which
	// the others agree, and among them those where it differs.
	agree, outlier map[string]int64
	// contested counts pixels on which the references (every engine but
	// cera) disagree among themselves.
	contested int64
	exact     map[string]*exactStats // per engine, synthetic drawings only
}

// exactStats compare an engine with the exact rendering.
type exactStats struct {
	over     int64      // inked pixels differing by more than tol
	absLuma  float64    // summed |luma difference|
	ink, ref float64    // summed ink (255 − luma) of the engine and of the exact rendering
	hist     [256]int64 // largest channel difference, inked pixels
}

func (e *exactStats) add(o *exactStats) {
	e.over += o.over
	e.absLuma += o.absLuma
	e.ink += o.ink
	e.ref += o.ref
	for i := range e.hist {
		e.hist[i] += o.hist[i]
	}
}

// p99 is the 99th percentile of the difference.
func (e *exactStats) p99() int {
	var total, seen int64
	for _, n := range e.hist {
		total += n
	}
	for v, n := range e.hist {
		if seen += n; seen*100 >= total*99 {
			return v
		}
	}
	return 0
}

// analyse compares the renderings of one page; names[i] rendered imgs[i],
// all of the same size; ex is the exact rendering or nil. It also returns
// the median of the references and a map of cera's outliers.
func analyse(names []string, imgs []*image.RGBA, ex *image.RGBA) (pageStats, *image.RGBA, *image.RGBA) {
	st := pageStats{
		pair:    map[string]map[string]int64{},
		agree:   map[string]int64{},
		outlier: map[string]int64{},
		exact:   map[string]*exactStats{},
	}
	n := len(names)
	b := imgs[0].Rect
	cons := image.NewRGBA(b)
	omap := image.NewRGBA(b)
	ceraAt := slices.Index(names, "cera")

	type partial struct {
		ink, contested int64
		pair           [][]int64
		agree, outlier []int64
		exact          []exactStats
	}
	rows := make(chan int, b.Dy())
	for y := range b.Dy() {
		rows <- y
	}
	close(rows)
	var mu sync.Mutex
	var wg sync.WaitGroup
	var parts []*partial
	for range runtime.NumCPU() {
		wg.Go(func() {
			p := &partial{pair: make([][]int64, n), agree: make([]int64, n), outlier: make([]int64, n), exact: make([]exactStats, n)}
			for i := range p.pair {
				p.pair[i] = make([]int64, n)
			}
			v := make([][3]int, n)
			others := make([]int, 0, n)
			for y := range rows {
				for x := range b.Dx() {
					o := y*imgs[0].Stride + 4*x
					inked := false
					for i, img := range imgs {
						px := img.Pix[o : o+3]
						v[i] = [3]int{int(px[0]), int(px[1]), int(px[2])}
						inked = inked || min(v[i][0], v[i][1], v[i][2]) < 255-tol
					}
					var e [3]int
					if ex != nil {
						px := ex.Pix[o : o+3]
						e = [3]int{int(px[0]), int(px[1]), int(px[2])}
						inked = inked || min(e[0], e[1], e[2]) < 255-tol
					}
					// The median of the references, for the consensus image.
					var med [3]int
					refs := others[:0]
					for i := range n {
						if i != ceraAt {
							refs = append(refs, i)
						}
					}
					agreeRefs := true
					for c := range 3 {
						var lo, hi int
						med[c], lo, hi = median(v, refs, c)
						agreeRefs = agreeRefs && hi-lo <= tol
					}
					cons.Pix[o], cons.Pix[o+1], cons.Pix[o+2], cons.Pix[o+3] = uint8(med[0]), uint8(med[1]), uint8(med[2]), 255
					// The outlier map: cera's outliers red, contested pixels
					// grey, the rest a faint copy of the consensus.
					g := uint8(255 - (255-luma(med))/6)
					mc := [3]uint8{g, g, g}
					if !inked {
						omap.Pix[o], omap.Pix[o+1], omap.Pix[o+2], omap.Pix[o+3] = 255, 255, 255, 255
						continue
					}
					p.ink++
					if !agreeRefs && len(refs) > 1 {
						p.contested++
						mc = [3]uint8{170, 170, 170}
					}
					for i := range n {
						for j := i + 1; j < n; j++ {
							if differ(v[i], v[j]) {
								p.pair[i][j]++
							}
						}
						// Leave one out: do the others agree, and does i?
						rest := others[:0]
						for j := range n {
							if j != i {
								rest = append(rest, j)
							}
						}
						if len(rest) < 2 {
							continue
						}
						agree := true
						var m [3]int
						for c := range 3 {
							var lo, hi int
							m[c], lo, hi = median(v, rest, c)
							agree = agree && hi-lo <= tol
						}
						if agree {
							p.agree[i]++
							if differ(v[i], m) {
								p.outlier[i]++
								if i == ceraAt {
									mc = outlierColor
								}
							}
						}
						if ex != nil {
							es := &p.exact[i]
							d := max(abs(v[i][0]-e[0]), abs(v[i][1]-e[1]), abs(v[i][2]-e[2]))
							es.hist[d]++
							if d > tol {
								es.over++
							}
							li, le := luma(v[i]), luma(e)
							es.absLuma += float64(abs(li - le))
							es.ink += float64(255 - li)
							es.ref += float64(255 - le)
						}
					}
					omap.Pix[o], omap.Pix[o+1], omap.Pix[o+2], omap.Pix[o+3] = mc[0], mc[1], mc[2], 255
				}
			}
			mu.Lock()
			parts = append(parts, p)
			mu.Unlock()
		})
	}
	wg.Wait()
	for _, nm := range names {
		st.pair[nm] = map[string]int64{}
	}
	for _, p := range parts {
		st.ink += p.ink
		st.contested += p.contested
		for i := range n {
			for j := i + 1; j < n; j++ {
				st.pair[names[i]][names[j]] += p.pair[i][j]
				st.pair[names[j]][names[i]] += p.pair[i][j]
			}
			st.agree[names[i]] += p.agree[i]
			st.outlier[names[i]] += p.outlier[i]
			if ex != nil {
				if st.exact[names[i]] == nil {
					st.exact[names[i]] = &exactStats{}
				}
				st.exact[names[i]].add(&p.exact[i])
			}
		}
	}
	return st, cons, omap
}

// outlierColor marks cera's outliers in the map (status critical).
var outlierColor = [3]uint8{208, 59, 59}

// median returns the median of channel c over the values v[idx], and its
// range. An even count takes the mean of the middle two.
func median(v [][3]int, idx []int, c int) (m, lo, hi int) {
	var buf [8]int
	s := buf[:0]
	for _, i := range idx {
		s = append(s, v[i][c])
	}
	slices.Sort(s)
	k := len(s)
	if k == 0 {
		return 255, 0, 0
	}
	if k%2 == 1 {
		m = s[k/2]
	} else {
		m = (s[k/2-1] + s[k/2] + 1) / 2
	}
	return m, s[0], s[k-1]
}

func differ(a, b [3]int) bool {
	return abs(a[0]-b[0]) > tol || abs(a[1]-b[1]) > tol || abs(a[2]-b[2]) > tol
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func luma(v [3]int) int { return (299*v[0] + 587*v[1] + 114*v[2]) / 1000 }

// Box is the side, in pixels, of the boxes of the coarse comparison.
const Box = 4

// boxDown averages boxes of k×k pixels.
func boxDown(img *image.RGBA, k int) *image.RGBA {
	return thumb(img, max(1, img.Rect.Dx()/k), nil)
}

// thumb scales img down to width w by averaging boxes; with keep, a box
// holding that colour takes it, so that thin marks survive.
func thumb(img *image.RGBA, w int, keep *[3]uint8) *image.RGBA {
	b := img.Rect
	if b.Dx() <= w {
		return img
	}
	h := max(1, b.Dy()*w/b.Dx())
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		y0, y1 := y*b.Dy()/h, max(y*b.Dy()/h+1, (y+1)*b.Dy()/h)
		for x := range w {
			x0, x1 := x*b.Dx()/w, max(x*b.Dx()/w+1, (x+1)*b.Dx()/w)
			var s [3]int
			kept := false
			for yy := y0; yy < y1; yy++ {
				row := img.Pix[yy*img.Stride:]
				for xx := x0; xx < x1; xx++ {
					s[0] += int(row[4*xx])
					s[1] += int(row[4*xx+1])
					s[2] += int(row[4*xx+2])
					kept = kept || keep != nil && [3]uint8(row[4*xx:4*xx+3]) == *keep
				}
			}
			k := (y1 - y0) * (x1 - x0)
			o := y*out.Stride + 4*x
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = uint8(s[0]/k), uint8(s[1]/k), uint8(s[2]/k), 255
			if kept {
				out.Pix[o], out.Pix[o+1], out.Pix[o+2] = keep[0], keep[1], keep[2]
			}
		}
	}
	return out
}
