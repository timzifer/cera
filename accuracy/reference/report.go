package main

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"image"
	"image/png"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// report is what the run found, as written to summary.json and rendered
// into report.html. Shares are of the inked area (see pageStats).
type report struct {
	Generated string   `json:"generated"`
	Dirs      string   `json:"dirs"`
	DPI       float64  `json:"dpi"`
	Sample    int      `json:"sample,omitempty"`
	Seed      uint64   `json:"seed,omitempty"`
	Engines   []string `json:"engines"`
	Missing   []string `json:"missing,omitempty"`
	Skipped   []string `json:"skipped,omitempty"`
	Files     int      `json:"files"`
	Pages     int      `json:"pages"`
	Compared  int      `json:"compared_pages"`

	// Fine compares pixels; Coarse boxes of Box×Box pixels, which leaves
	// out how edges are antialiased and keeps what is drawn.
	Fine   *view      `json:"fine"`
	Coarse *view      `json:"coarse"`
	Exact  []exactRow `json:"exact,omitempty"`
}

// view is the statistics at one resolution.
type view struct {
	// Outlier[e] is the share of the inked area on which every other
	// engine agrees and e does not; Agree[e] the share on which the
	// others agree at all.
	Outlier   map[string]float64 `json:"outlier"`
	Agree     map[string]float64 `json:"agree"`
	Contested float64            `json:"contested"` // the references (all but cera) disagree
	// Pair[a][b] is the share of the inked area on which a and b differ.
	Pair       map[string]map[string]float64 `json:"pair"`
	Categories []categoryRow                 `json:"categories"`
	FileRows   []fileRow                     `json:"files_detail"`
	Keys       []keyRow                      `json:"unsupported_keys,omitempty"`
	Compared   int                           `json:"-"`
}

type categoryRow struct {
	Category  string             `json:"category"`
	Pages     int                `json:"pages"`
	Outlier   map[string]float64 `json:"outlier"`
	Contested float64            `json:"contested"`
}

// exactRow compares one engine with the exact rendering of a drawing.
type exactRow struct {
	Scene    string  `json:"scene"`
	Engine   string  `json:"engine"`
	InkRatio float64 `json:"ink_ratio"` // the engine's ink over the exact ink: 1 is exact, above is heavier
	Over     float64 `json:"over16"`    // share of the inked area differing by more than 16
	MAE      float64 `json:"mae"`       // mean absolute luma difference on the inked area, 1/255 steps
	P99      int     `json:"p99"`
}

type fileRow struct {
	File        string             `json:"file"`
	Category    string             `json:"category"`
	Pages       int                `json:"pages"`
	CeraOutlier float64            `json:"cera_outlier"`
	Contested   float64            `json:"contested"`
	VsCera      map[string]float64 `json:"vs_cera"` // share differing from cera, per engine
	Failed      []string           `json:"failed,omitempty"`
	Unsupported []string           `json:"unsupported,omitempty"`
}

type keyRow struct {
	Key         string  `json:"key"`
	Pages       int     `json:"pages"`
	CeraOutlier float64 `json:"cera_outlier"` // on the pages using it
}

type tally struct{ num, den int64 }

func (t tally) share() float64 {
	if t.den == 0 {
		return math.NaN()
	}
	return float64(t.num) / float64(t.den)
}

func summarize(rs []pageResult, engs []engine, exactOn bool) *report {
	rep := &report{Generated: time.Now().UTC().Format("2006-01-02 15:04 MST")}
	for _, e := range engs {
		rep.Engines = append(rep.Engines, e.name())
	}
	files := map[string]bool{}
	for i := range rs {
		files[rs[i].rel] = true
		rep.Pages++
		if rs[i].fine.pair != nil {
			rep.Compared++
		}
	}
	rep.Files = len(files)
	rep.Fine = summarizeView(rs, rep.Engines, func(r *pageResult) *pageStats { return &r.fine })
	rep.Coarse = summarizeView(rs, rep.Engines, func(r *pageResult) *pageStats { return &r.coarse })
	rep.Fine.Compared, rep.Coarse.Compared = rep.Compared, rep.Compared
	if exactOn {
		rep.Exact = summarizeExact(rs, rep.Engines)
	}
	return rep
}

// summarizeView aggregates the statistics sel picks from each page.
func summarizeView(rs []pageResult, engines []string, sel func(*pageResult) *pageStats) *view {
	rep := &view{}
	outlier, agree := map[string]*tally{}, map[string]*tally{}
	pair := map[string]map[string]*tally{}
	for _, a := range engines {
		outlier[a], agree[a] = &tally{}, &tally{}
		pair[a] = map[string]*tally{}
		for _, b := range engines {
			pair[a][b] = &tally{}
		}
	}
	var contested tally
	type catAgg struct {
		pages     int
		outlier   map[string]*tally
		contested tally
	}
	cats := map[string]*catAgg{}
	type fileAgg struct {
		row     fileRow
		outlier tally
		cont    tally
		vs      map[string]*tally
		keys    map[string]bool
		failed  map[string]bool
	}
	byFile := map[string]*fileAgg{}
	var fileOrder []string
	keys := map[string]*struct {
		pages int
		t     tally
	}{}
	for i := range rs {
		r := &rs[i]
		st := sel(r)
		fa := byFile[r.rel]
		if fa == nil {
			fa = &fileAgg{row: fileRow{File: r.rel, Category: r.category}, vs: map[string]*tally{}, keys: map[string]bool{}, failed: map[string]bool{}}
			byFile[r.rel] = fa
			fileOrder = append(fileOrder, r.rel)
		}
		fa.row.Pages++
		for e := range r.failed {
			fa.failed[e] = true
		}
		for _, k := range r.unsupported {
			fa.keys[k] = true
		}
		if st.pair == nil {
			continue
		}
		ink := st.ink
		ca := cats[r.category]
		if ca == nil {
			ca = &catAgg{outlier: map[string]*tally{}}
			for _, e := range engines {
				ca.outlier[e] = &tally{}
			}
			cats[r.category] = ca
		}
		ca.pages++
		ca.contested.num += st.contested
		ca.contested.den += ink
		contested.num += st.contested
		contested.den += ink
		for _, e := range r.engines {
			outlier[e].num += st.outlier[e]
			outlier[e].den += ink
			agree[e].num += st.agree[e]
			agree[e].den += ink
			ca.outlier[e].num += st.outlier[e]
			ca.outlier[e].den += ink
			for _, f := range r.engines {
				if f != e {
					pair[e][f].num += st.pair[e][f]
					pair[e][f].den += ink
				}
			}
			if e != "cera" && slices.Contains(r.engines, "cera") {
				t := fa.vs[e]
				if t == nil {
					t = &tally{}
					fa.vs[e] = t
				}
				t.num += st.pair["cera"][e]
				t.den += ink
			}
		}
		if slices.Contains(r.engines, "cera") {
			fa.outlier.num += st.outlier["cera"]
			fa.outlier.den += ink
			for _, k := range r.unsupported {
				kt := keys[k]
				if kt == nil {
					kt = &struct {
						pages int
						t     tally
					}{}
					keys[k] = kt
				}
				kt.pages++
				kt.t.num += st.outlier["cera"]
				kt.t.den += ink
			}
		}
		fa.cont.num += st.contested
		fa.cont.den += ink
	}
	rep.Outlier, rep.Agree = map[string]float64{}, map[string]float64{}
	rep.Pair = map[string]map[string]float64{}
	for _, e := range engines {
		rep.Outlier[e] = outlier[e].share()
		rep.Agree[e] = agree[e].share()
		rep.Pair[e] = map[string]float64{}
		for _, f := range engines {
			if f != e {
				rep.Pair[e][f] = pair[e][f].share()
			}
		}
	}
	rep.Contested = contested.share()
	catNames := make([]string, 0, len(cats))
	for c := range cats {
		catNames = append(catNames, c)
	}
	slices.Sort(catNames)
	for _, c := range catNames {
		ca := cats[c]
		row := categoryRow{Category: c, Pages: ca.pages, Outlier: map[string]float64{}, Contested: ca.contested.share()}
		for e, t := range ca.outlier {
			if t.den > 0 {
				row.Outlier[e] = t.share()
			}
		}
		rep.Categories = append(rep.Categories, row)
	}
	for _, name := range fileOrder {
		fa := byFile[name]
		fa.row.CeraOutlier = fa.outlier.share()
		fa.row.Contested = fa.cont.share()
		fa.row.VsCera = map[string]float64{}
		for e, t := range fa.vs {
			fa.row.VsCera[e] = t.share()
		}
		for k := range fa.keys {
			fa.row.Unsupported = append(fa.row.Unsupported, k)
		}
		for e := range fa.failed {
			fa.row.Failed = append(fa.row.Failed, e)
		}
		slices.Sort(fa.row.Unsupported)
		slices.Sort(fa.row.Failed)
		rep.FileRows = append(rep.FileRows, fa.row)
	}
	slices.SortStableFunc(rep.FileRows, func(a, b fileRow) int { return cmpNaN(b.CeraOutlier, a.CeraOutlier) })
	for k, kt := range keys {
		rep.Keys = append(rep.Keys, keyRow{Key: k, Pages: kt.pages, CeraOutlier: kt.t.share()})
	}
	slices.SortFunc(rep.Keys, func(a, b keyRow) int { return cmpNaN(b.CeraOutlier, a.CeraOutlier) })
	return rep
}

// summarizeExact compares each engine with the exact drawings.
func summarizeExact(rs []pageResult, engines []string) []exactRow {
	exact := map[[2]string]*exactStats{}
	exactInk := map[[2]string]int64{} // inked pixels of the pages compared
	var scenes []string
	for i := range rs {
		r := &rs[i]
		if len(r.fine.exact) == 0 {
			continue
		}
		scene := strings.TrimSuffix(strings.TrimPrefix(r.rel, "synthetic/"), ".pdf")
		if !slices.Contains(scenes, scene) {
			scenes = append(scenes, scene)
		}
		for e, es := range r.fine.exact {
			k := [2]string{scene, e}
			if exact[k] == nil {
				exact[k] = &exactStats{}
			}
			exact[k].add(es)
			exactInk[k] += r.fine.ink
		}
	}
	slices.Sort(scenes)
	var rows []exactRow
	for _, s := range scenes {
		for _, e := range engines {
			es := exact[[2]string{s, e}]
			if es == nil {
				continue
			}
			ink := float64(exactInk[[2]string{s, e}])
			rows = append(rows, exactRow{
				Scene: s, Engine: e, InkRatio: es.ink / es.ref, Over: float64(es.over) / ink, MAE: es.absLuma / ink, P99: es.p99(),
			})
		}
	}
	return rows
}

// cmpNaN orders NaN last.
func cmpNaN(a, b float64) int {
	switch {
	case math.IsNaN(a) && math.IsNaN(b):
		return 0
	case math.IsNaN(a):
		return -1
	case math.IsNaN(b):
		return 1
	}
	return cmpF(a, b)
}

func writeCSV(path string, rs []pageResult, engines []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	head := []string{"file", "category", "page", "width", "height"}
	for _, v := range []string{"", "coarse_"} {
		head = append(head, v+"ink_px", v+"contested_px")
		for _, e := range engines {
			head = append(head, v+e+"_agree_px", v+e+"_outlier_px")
		}
		for i, a := range engines {
			for _, b := range engines[i+1:] {
				head = append(head, v+a+"_vs_"+b+"_px")
			}
		}
	}
	head = append(head, "failed", "unsupported")
	w.Write(head)
	for i := range rs {
		r := &rs[i]
		row := []string{r.rel, r.category, strconv.Itoa(r.page), strconv.Itoa(r.size.X), strconv.Itoa(r.size.Y)}
		for _, st := range []*pageStats{&r.fine, &r.coarse} {
			row = append(row, strconv.FormatInt(st.ink, 10), strconv.FormatInt(st.contested, 10))
			for _, e := range engines {
				row = append(row, strconv.FormatInt(st.agree[e], 10), strconv.FormatInt(st.outlier[e], 10))
			}
			for i, a := range engines {
				for _, b := range engines[i+1:] {
					row = append(row, strconv.FormatInt(st.pair[a][b], 10))
				}
			}
		}
		var failed []string
		for e, msg := range r.failed {
			failed = append(failed, e+": "+msg)
		}
		slices.Sort(failed)
		row = append(row, strings.Join(failed, "; "), strings.Join(r.unsupported, " "))
		w.Write(row)
	}
	w.Flush()
	return w.Error()
}

func writeJSON(path string, rep *report) error {
	data, err := json.MarshalIndent(nanToNull(rep), "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// nanToNull drops shares without data (NaN is not JSON): from maps, and
// as -1 elsewhere.
func nanToNull(rep *report) any {
	clean := *rep
	clean.Fine, clean.Coarse = rep.Fine.clean(), rep.Coarse.clean()
	clean.Exact = slices.Clone(rep.Exact)
	for i := range clean.Exact {
		if math.IsNaN(clean.Exact[i].InkRatio) || math.IsInf(clean.Exact[i].InkRatio, 0) {
			clean.Exact[i].InkRatio = -1
		}
	}
	return clean
}

func (v *view) clean() *view {
	c := *v
	fix := func(m map[string]float64) map[string]float64 {
		out := map[string]float64{}
		for k, x := range m {
			if !math.IsNaN(x) {
				out[k] = x
			}
		}
		return out
	}
	num := func(x float64) float64 {
		if math.IsNaN(x) {
			return -1
		}
		return x
	}
	c.Outlier, c.Agree, c.Contested = fix(v.Outlier), fix(v.Agree), num(v.Contested)
	c.Pair = map[string]map[string]float64{}
	for k, m := range v.Pair {
		c.Pair[k] = fix(m)
	}
	c.Categories = slices.Clone(v.Categories)
	for i := range c.Categories {
		c.Categories[i].Outlier = fix(c.Categories[i].Outlier)
		c.Categories[i].Contested = num(c.Categories[i].Contested)
	}
	c.FileRows = slices.Clone(v.FileRows)
	for i := range c.FileRows {
		r := &c.FileRows[i]
		r.VsCera, r.CeraOutlier, r.Contested = fix(r.VsCera), num(r.CeraOutlier), num(r.Contested)
	}
	c.Keys = slices.Clone(v.Keys)
	for i := range c.Keys {
		c.Keys[i].CeraOutlier = num(c.Keys[i].CeraOutlier)
	}
	return &c
}

// pct formats a share as a percentage.
func pct(v float64) string {
	switch {
	case math.IsNaN(v):
		return "–"
	case v == 0:
		return "0 %"
	case v < 0.0001:
		return "<0.01 %"
	case v < 0.1:
		return fmt.Sprintf("%.2f %%", 100*v)
	}
	return fmt.Sprintf("%.1f %%", 100*v)
}

func pngURI(img *image.RGBA) template.URL {
	var b bytes.Buffer
	png.Encode(&b, img)
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes()))
}

// slot is the colour slot of an engine, fixed by engineOrder.
func slot(e string) int { return slices.Index(engineOrder, e) + 1 }

func esc(s string) string { return html.EscapeString(s) }

// barFacets draws the outlier share per engine as horizontal bars, one
// small multiple per facet, all on one scale.
func barFacets(rep *view, engines []string) template.HTML {
	type facet struct {
		title string
		vals  map[string]float64
		pages int
	}
	facets := []facet{{"All pages", rep.Outlier, rep.Compared}}
	for _, c := range rep.Categories {
		facets = append(facets, facet{c.Category, c.Outlier, c.Pages})
	}
	top := 0.0
	for _, f := range facets {
		for _, v := range f.vals {
			if !math.IsNaN(v) {
				top = max(top, v)
			}
		}
	}
	top = niceCeil(top)
	const w, label, valW, barH, gap = 300.0, 86.0, 58.0, 14.0, 6.0
	var b strings.Builder
	for _, f := range facets {
		h := float64(len(engines))*(barH+gap) + 22
		fmt.Fprintf(&b, `<figure class="facet"><figcaption>%s <span class="muted">· %d pages</span></figcaption><svg viewBox="0 0 %.0f %.0f" role="img" aria-label="%s">`,
			esc(f.title), f.pages, w, h, esc("Outlier share per engine, "+f.title))
		plot := w - label - valW
		// Gridlines at quarters of the scale.
		for k := 0; k <= 4; k++ {
			x := label + plot*float64(k)/4
			fmt.Fprintf(&b, `<line class="grid" x1="%.1f" x2="%.1f" y1="0" y2="%.0f"/>`, x, x, h-18)
			fmt.Fprintf(&b, `<text class="tick" x="%.1f" y="%.0f" text-anchor="middle">%s</text>`, x, h-4, pct(top*float64(k)/4))
		}
		for i, e := range engines {
			y := float64(i) * (barH + gap)
			v, ok := f.vals[e]
			fmt.Fprintf(&b, `<text class="lab" x="%.0f" y="%.1f" text-anchor="end">%s</text>`, label-8, y+barH-3, esc(e))
			if !ok || math.IsNaN(v) {
				fmt.Fprintf(&b, `<text class="tick" x="%.0f" y="%.1f">no data</text>`, label+4, y+barH-3)
				continue
			}
			bw := max(2, plot*v/top)
			fmt.Fprintf(&b, `<path class="s%d" d="%s" data-tip="%s · %s: %s of the inked area"/>`, slot(e), barPath(label, y, bw, barH), esc(e), esc(f.title), pct(v))
			fmt.Fprintf(&b, `<rect class="hit" x="%.0f" y="%.1f" width="%.0f" height="%.0f" data-tip="%s · %s: %s of the inked area"/>`, label, y-gap/2, plot+valW, barH+gap, esc(e), esc(f.title), pct(v))
			fmt.Fprintf(&b, `<text class="val" x="%.1f" y="%.1f">%s</text>`, label+bw+6, y+barH-3, pct(v))
		}
		b.WriteString(`</svg></figure>`)
	}
	return template.HTML(b.String())
}

// barPath is a bar from x0 with a rounded data end (4 px).
func barPath(x0, y, w, h float64) string {
	r := min(4, w/2, h/2)
	return fmt.Sprintf("M%.1f %.1fH%.1fa%.1f %.1f 0 0 1 %.1f %.1fV%.1fa%.1f %.1f 0 0 1 %.1f %.1fH%.1fZ",
		x0, y, x0+w-r, r, r, r, r, y+h-r, r, r, -r, r, x0)
}

func niceCeil(v float64) float64 {
	if !(v > 0) {
		return 0.01
	}
	e := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*e >= v {
			return m * e
		}
	}
	return 10 * e
}

// heatmap draws how often each pair of engines differs.
func heatmap(rep *view, engines []string) template.HTML {
	n := len(engines)
	const cell, label = 64.0, 92.0
	top := 0.0
	for _, m := range rep.Pair {
		for _, v := range m {
			if !math.IsNaN(v) {
				top = max(top, v)
			}
		}
	}
	top = niceCeil(top)
	w, h := label+cell*float64(n), label+cell*float64(n)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="heat" viewBox="0 0 %.0f %.0f" role="img" aria-label="Share of the inked area on which two engines differ">`, w, h)
	for i, e := range engines {
		fmt.Fprintf(&b, `<text class="lab" x="%.0f" y="%.1f" text-anchor="end">%s</text>`, label-8, label+cell*float64(i)+cell/2+4, esc(e))
		fmt.Fprintf(&b, `<text class="lab" x="%.1f" y="%.0f" text-anchor="middle">%s</text>`, label+cell*float64(i)+cell/2, label-10, esc(e))
	}
	for i, a := range engines {
		for j, c := range engines {
			x, y := label+cell*float64(j), label+cell*float64(i)
			if i == j {
				fmt.Fprintf(&b, `<rect class="diag" x="%.0f" y="%.0f" width="%.0f" height="%.0f" rx="4"/>`, x+1, y+1, cell-2, cell-2)
				continue
			}
			v := rep.Pair[a][c]
			bin := 0
			if !math.IsNaN(v) {
				bin = min(6, int(7*v/top))
			}
			fmt.Fprintf(&b, `<rect class="q%d" x="%.0f" y="%.0f" width="%.0f" height="%.0f" rx="4" data-tip="%s vs %s: differ on %s of the inked area"/>`,
				bin, x+1, y+1, cell-2, cell-2, esc(a), esc(c), pct(v))
			fmt.Fprintf(&b, `<text class="cellv q%dt" x="%.1f" y="%.1f" text-anchor="middle">%s</text>`, bin, x+cell/2, y+cell/2+4, pct(v))
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// inkPlot draws, per drawing, each engine's ink against the exact
// rendering: 1 is exact, right of it heavier.
func inkPlot(rep *report) template.HTML {
	if len(rep.Exact) == 0 {
		return ""
	}
	var scenes []string
	lo, hi := 1.0, 1.0
	for _, r := range rep.Exact {
		if !slices.Contains(scenes, r.Scene) {
			scenes = append(scenes, r.Scene)
		}
		if !math.IsNaN(r.InkRatio) && !math.IsInf(r.InkRatio, 0) {
			lo, hi = min(lo, r.InkRatio), max(hi, r.InkRatio)
		}
	}
	span := max(1-lo, hi-1, 0.05)
	span = niceCeil(span)
	lo, hi = 1-span, 1+span
	const w, label, rowH = 760.0, 170.0, 44.0
	plot := w - label - 20
	h := rowH*float64(len(scenes)) + 34
	x := func(v float64) float64 { return label + plot*(v-lo)/(hi-lo) }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" role="img" aria-label="Ink of each engine relative to the exact rendering">`, w, h)
	for k := 0; k <= 4; k++ {
		v := lo + (hi-lo)*float64(k)/4
		cls := "grid"
		if k == 2 {
			cls = "ref"
		}
		fmt.Fprintf(&b, `<line class="%s" x1="%.1f" x2="%.1f" y1="0" y2="%.0f"/>`, cls, x(v), x(v), h-22)
		fmt.Fprintf(&b, `<text class="tick" x="%.1f" y="%.0f" text-anchor="middle">%s</text>`, x(v), h-6, strconv.FormatFloat(v, 'f', -1, 64)+"×")
	}
	fmt.Fprintf(&b, `<text class="tick" x="%.1f" y="10" text-anchor="start">exact</text>`, x(1)+4)
	for i, s := range scenes {
		y := float64(i)*rowH + rowH/2 + 4
		fmt.Fprintf(&b, `<line class="grid" x1="%.0f" x2="%.0f" y1="%.1f" y2="%.1f"/>`, label, w-20, y, y)
		fmt.Fprintf(&b, `<text class="lab" x="%.0f" y="%.1f" text-anchor="end">%s</text>`, label-8, y+4, esc(s))
		for _, r := range rep.Exact {
			if r.Scene != s || math.IsNaN(r.InkRatio) {
				continue
			}
			v := min(hi, max(lo, r.InkRatio))
			// Engines are dodged vertically, so that equal values stay visible.
			dy := (float64(slot(r.Engine)) - 3) * 6.5
			fmt.Fprintf(&b, `<circle class="s%d dot" cx="%.1f" cy="%.1f" r="5" data-tip="%s · %s: %.3f× the exact ink, %s differ by more than 16, mean error %.1f/255"/>`,
				slot(r.Engine), x(v), y+dy, esc(s), esc(r.Engine), r.InkRatio, pct(r.Over), r.MAE)
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func legend(engines []string) template.HTML {
	var b strings.Builder
	b.WriteString(`<ul class="legend">`)
	for _, e := range engines {
		fmt.Fprintf(&b, `<li><span class="key s%d"></span>%s</li>`, slot(e), esc(e))
	}
	b.WriteString(`</ul>`)
	return template.HTML(b.String())
}

func writeHTML(path string, rep *report, shots []shot) error {
	type shotView struct {
		Title             string
		Share             string
		Cera, Cons, Outly template.URL
	}
	var views []shotView
	for _, s := range shots {
		views = append(views, shotView{fmt.Sprintf("%s p%d", s.rel, s.page), pct(s.share), pngURI(s.cera), pngURI(s.cons), pngURI(s.outlier)})
	}
	var ceraInk []float64
	for _, r := range rep.Exact {
		if r.Engine == "cera" && !math.IsNaN(r.InkRatio) {
			ceraInk = append(ceraInk, r.InkRatio)
		}
	}
	slices.Sort(ceraInk)
	inkMedian := "–"
	if len(ceraInk) > 0 {
		inkMedian = fmt.Sprintf("%.3f×", ceraInk[len(ceraInk)/2])
	}
	place := func(v *view) int {
		rank := slices.Clone(rep.Engines)
		slices.SortStableFunc(rank, func(a, b string) int { return cmpNaN(v.Outlier[a], v.Outlier[b]) })
		return slices.Index(rank, "cera") + 1
	}
	type viewData struct {
		ID, Name, What string
		V              *view
		Engines        []string
		Bars, Heat     template.HTML
	}
	data := map[string]any{
		"R": rep,
		"Views": []viewData{
			{"coarse", "Content", fmt.Sprintf("boxes of %d×%d pixels (%.1f mm): what is drawn, leaving out how edges are smoothed", Box, Box, Box*25.4/rep.DPI), rep.Coarse, rep.Engines, barFacets(rep.Coarse, rep.Engines), heatmap(rep.Coarse, rep.Engines)},
			{"fine", "Edges", "single pixels: what is drawn and how its edges are antialiased", rep.Fine, rep.Engines, barFacets(rep.Fine, rep.Engines), heatmap(rep.Fine, rep.Engines)},
		},
		"Ink":         inkPlot(rep),
		"Legend":      legend(rep.Engines),
		"Shots":       views,
		"InkMedian":   inkMedian,
		"PlaceCoarse": place(rep.Coarse),
		"PlaceFine":   place(rep.Fine),
		"Box":         Box,
	}
	t, err := template.New("report").Funcs(template.FuncMap{
		"pct":  pct,
		"slot": slot,
		"get": func(m map[string]float64, k string) string {
			v, ok := m[k]
			if !ok {
				return "–"
			}
			return pct(v)
		},
		"num": func(v float64) string { return fmt.Sprintf("%.1f", v) },
		"ratio": func(v float64) string {
			if math.IsNaN(v) {
				return "–"
			}
			return fmt.Sprintf("%.3f×", v)
		},
		"join": strings.Join,
		"notCera": func(es []string) []string {
			return slices.DeleteFunc(slices.Clone(es), func(e string) bool { return e == "cera" })
		},
	}).Parse(reportHTML)
	if err != nil {
		return err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}
