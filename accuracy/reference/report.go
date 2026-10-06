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
	Generated string  `json:"generated"`
	Dirs      string  `json:"dirs"`
	DPI       float64 `json:"dpi"`
	Sample    int     `json:"sample,omitempty"`
	Seed      uint64  `json:"seed,omitempty"`
	// Annotations says whether annotations were drawn; without them only
	// the page content is compared.
	Annotations bool `json:"annotations"`
	// Overprint says cera simulated overprint (-overprint).
	Overprint bool `json:"overprint,omitempty"`
	// ImageFilter is how cera sampled magnified images (-image-filter).
	ImageFilter string `json:"image_filter"`
	// CMYKProfile names the profile cera converted DeviceCMYK through
	// (-cmyk-profile); empty for the bundled one.
	CMYKProfile string   `json:"cmyk_profile,omitempty"`
	Engines     []string `json:"engines"`
	Missing     []string `json:"missing,omitempty"`
	Skipped     []string `json:"skipped,omitempty"`
	// Excepted lists the pages left out (exceptions.go), with the reason.
	Excepted []string `json:"excepted,omitempty"`
	Files    int      `json:"files"`
	Pages    int      `json:"pages"`
	Compared int      `json:"compared_pages"`

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
		if rs[i].excepted != "" {
			rep.Excepted = append(rep.Excepted, fmt.Sprintf("%s p%d: %s", rs[i].rel, rs[i].page, rs[i].excepted))
		}
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
	head = append(head, "failed", "unsupported", "excepted")
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
		row = append(row, strings.Join(failed, "; "), strings.Join(r.unsupported, " "), r.excepted)
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
