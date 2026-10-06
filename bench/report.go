package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// meta says where the ratios come from.
type meta struct {
	Generated   string            `json:"generated"`
	CPU         string            `json:"cpu"`
	OS          string            `json:"os"`
	Cores       int               `json:"cores"`
	Go          string            `json:"go"`
	DPI         float64           `json:"dpi"`
	Runs        int               `json:"runs"`
	MultiRuns   int               `json:"multi_runs"`
	Ref         string            `json:"ref"`
	Corpus      string            `json:"corpus"` // SHA-256 prefix over the files' hashes
	Files       int               `json:"files"`
	Versions    map[string]string `json:"versions"`
	Unavailable []string          `json:"unavailable,omitempty"`
}

func newMeta(files []*file) *meta {
	keys := make([]string, len(files))
	for i, f := range files {
		keys[i] = f.rel + ":" + f.key
	}
	sort.Strings(keys)
	return &meta{
		Generated: time.Now().UTC().Format("2006-01-02"),
		CPU:       cpuName(),
		OS:        runtime.GOOS + "/" + runtime.GOARCH,
		Cores:     *cores,
		Go:        runtime.Version(),
		DPI:       *dpi,
		Runs:      *runs,
		MultiRuns: *multiRuns,
		Corpus:    shortHash(strings.Join(keys, "\n")),
		Files:     len(files),
		Versions:  map[string]string{},
	}
}

// group is the ratios of one engine over a set of pages or files.
type group struct {
	Ratio  float64 `json:"ratio"`            // geometric mean, 1 = as fast as the reference, 2 = twice the time
	Spread float64 `json:"spread,omitempty"` // median relative interquartile range of the paired ratios
	N      int     `json:"n"`                // pages (files) in the mean
	Failed int     `json:"failed,omitempty"` // pages (files) the engine could not draw where the reference could
}

type summary struct {
	Meta *meta `json:"meta"`
	// Single is the time per page on one core, per category and engine
	// ("all" for the whole corpus).
	Single map[string]map[string]group `json:"single"`
	// First is opening a file and drawing its first page in a fresh process.
	First map[string]map[string]group `json:"first"`
	// Multi is drawing whole documents on all cores against the reference
	// on all cores; Speedup each engine's own gain over one core.
	Multi   map[string]map[string]group   `json:"multi,omitempty"`
	Speedup map[string]map[string]float64 `json:"speedup,omitempty"` // group → engine
	Pooled  map[string]bool               `json:"pooled,omitempty"`  // all cores as one process per core
	// RSSMB is the median peak resident memory of an engine's process per
	// file, RSSMaxMB the largest.
	RSSMB    map[string]float64 `json:"rss_mb,omitempty"`
	RSSMaxMB map[string]float64 `json:"rss_max_mb,omitempty"`
	// Cera alone: allocations per page (deterministic), and rendering a
	// page again from its display list as a share of the first render.
	CeraAllocs struct {
		Median float64 `json:"median"`
		P90    float64 `json:"p90"`
		Max    float64 `json:"max"`
		Bytes  float64 `json:"bytes_median"`
	} `json:"cera_allocs"`
	Again map[string]float64 `json:"cera_again,omitempty"`
	// LessInk counts pages where an engine left more than half of the
	// median engine's ink undrawn: a speed bought by drawing less.
	LessInk map[string]int `json:"less_ink,omitempty"`
	// Accuracy is each engine's content outlier share from the reference
	// comparison (accuracy/reference), if its summary is there.
	Accuracy map[string]float64 `json:"accuracy,omitempty"`
}

// writeReport writes what was measured as ratios, one row per page
// (pages.csv) and per file (files.csv), with the setup in meta.json; the
// summary, its tables and charts are computed from those files alone
// (aggregate), so they can be recomputed without measuring again.
func writeReport(dir string, m *meta, engines []*engine, results []*fileResult) error {
	refName := m.Ref
	pages, err := os.Create(filepath.Join(dir, "pages.csv"))
	if err != nil {
		return err
	}
	defer pages.Close()
	pw := csv.NewWriter(pages)
	pw.Write([]string{"file", "category", "page", "engine", "ratio", "spread", "ink", "width", "height", "allocs", "bytes", "again", "error"})
	filesCSV, err := os.Create(filepath.Join(dir, "files.csv"))
	if err != nil {
		return err
	}
	defer filesCSV.Close()
	fw := csv.NewWriter(filesCSV)
	fw.Write([]string{"file", "category", "pages", "engine", "first", "multi", "multi_spread", "speedup", "pooled", "peak_rss_mb", "error"})

	for _, fr := range results {
		cat := fr.f.category
		for _, pr := range fr.results {
			refT := pr.times[refName]
			for _, e := range engines {
				name := e.name
				ratio, spread := math.NaN(), math.NaN()
				if t := pr.times[name]; len(t) == *runs && len(refT) == *runs && *runs > 0 {
					paired := make([]float64, len(t))
					for k := range t {
						paired[k] = float64(t[k]) / float64(refT[k])
					}
					ratio = median(paired)
					spread = iqr(paired) / ratio
				}
				row := []string{fr.f.rel, cat, strconv.Itoa(pr.page + 1), name, fmtF(ratio, 4), fmtF(spread, 4)}
				if ink, ok := pr.ink[name]; ok {
					row = append(row, fmtF(ink, 5))
				} else {
					row = append(row, "")
				}
				if sz, ok := pr.size[name]; ok {
					row = append(row, strconv.Itoa(sz[0]), strconv.Itoa(sz[1]))
				} else {
					row = append(row, "", "")
				}
				if name == "cera" && pr.allocs > 0 {
					row = append(row, strconv.FormatInt(pr.allocs, 10), strconv.FormatInt(pr.bytes, 10), fmtF(median(pr.again), 4))
				} else {
					row = append(row, "", "", "")
				}
				pw.Write(append(row, oneLine(pr.err[name])))
			}
		}
		for _, e := range engines {
			name := e.name
			firstR, multiR, multiS, speed := math.NaN(), math.NaN(), math.NaN(), math.NaN()
			if ft, ok := fr.first[name]; ok {
				if rt, ok := fr.first[refName]; ok && rt > 0 {
					firstR = float64(ft) / float64(rt)
				}
			}
			at, rt := fr.all[name], fr.all[refName]
			if len(at) == *multiRuns && len(rt) == *multiRuns && *multiRuns > 0 {
				paired := make([]float64, len(at))
				for k := range at {
					paired[k] = float64(at[k]) / float64(rt[k])
				}
				multiR = median(paired)
				multiS = iqr(paired) / multiR
				// One core: the sum of the page medians, timed the same way
				// (wall clock), against all cores.
				var one float64
				complete := true
				for _, pr := range fr.results {
					if len(pr.times[name]) != *runs {
						complete = false
						break
					}
					one += medianInt(pr.times[name])
				}
				if complete && medianInt(at) > 0 {
					speed = one / medianInt(at)
				}
			}
			mb := math.NaN()
			if v := fr.rss[name]; v > 0 {
				mb = float64(v) / (1 << 20)
			}
			msg := fr.openErr[name]
			if msg == "" {
				msg = fr.allErr[name]
			}
			fw.Write([]string{fr.f.rel, cat, strconv.Itoa(fr.pages), name, fmtF(firstR, 4), fmtF(multiR, 4), fmtF(multiS, 4), fmtF(speed, 3),
				strconv.FormatBool(fr.pooled[name]), fmtF(mb, 1), oneLine(msg)})
		}
	}
	pw.Flush()
	fw.Flush()
	if err := pw.Error(); err != nil {
		return err
	}
	if err := fw.Error(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644); err != nil {
		return err
	}
	return aggregate(dir)
}

// multiPages is the least number of pages a document needs to count as
// one of several pages in the all-cores comparison.
const multiPages = 5

// aggregate computes summary.json, summary.md and the charts from
// meta.json, pages.csv and files.csv.
func aggregate(dir string) error {
	var m meta
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	pages, err := readCSV(filepath.Join(dir, "pages.csv"))
	if err != nil {
		return err
	}
	files, err := readCSV(filepath.Join(dir, "files.csv"))
	if err != nil {
		return err
	}
	var names []string
	for _, n := range engineOrder {
		if _, ok := m.Versions[n]; ok {
			names = append(names, n)
		}
	}
	s := &summary{Meta: &m, Speedup: map[string]map[string]float64{}, Pooled: map[string]bool{},
		RSSMB: map[string]float64{}, RSSMaxMB: map[string]float64{}, Again: map[string]float64{}, LessInk: map[string]int{}}

	type acc struct {
		logs, spreads []float64
		failed        int
	}
	tables := map[string]map[string]map[string]*acc{"single": {}, "first": {}, "multi": {}}
	add := func(t, cat, eng string) *acc {
		if tables[t][cat] == nil {
			tables[t][cat] = map[string]*acc{}
		}
		if tables[t][cat][eng] == nil {
			tables[t][cat][eng] = &acc{}
		}
		return tables[t][cat][eng]
	}

	// Pages; the reference's own row has ratio 1 where it was timed.
	type pageKey struct{ file, page string }
	refOK := map[pageKey]bool{}
	inks := map[pageKey][]float64{}
	for _, r := range pages {
		k := pageKey{r["file"], r["page"]}
		if r["engine"] == m.Ref && r["ratio"] != "" {
			refOK[k] = true
		}
		if r["ink"] != "" {
			inks[k] = append(inks[k], parseFloat(r["ink"]))
		}
	}
	again := map[string][]float64{}
	var allocs, bytes []float64
	for _, r := range pages {
		k, name, cat := pageKey{r["file"], r["page"]}, r["engine"], r["category"]
		ratio := math.NaN()
		if r["ratio"] != "" {
			ratio = parseFloat(r["ratio"])
		}
		if finite(ratio) {
			for _, c := range []string{cat, "all"} {
				a := add("single", c, name)
				a.logs = append(a.logs, math.Log(ratio))
				if r["spread"] != "" {
					a.spreads = append(a.spreads, parseFloat(r["spread"]))
				}
			}
		} else if refOK[k] && name != m.Ref {
			for _, c := range []string{cat, "all"} {
				add("single", c, name).failed++
			}
		}
		if med := median(inks[k]); r["ink"] != "" && med > 0.002 && parseFloat(r["ink"]) < med/2 {
			s.LessInk[name]++
		}
		if r["allocs"] != "" {
			allocs = append(allocs, parseFloat(r["allocs"]))
			bytes = append(bytes, parseFloat(r["bytes"]))
		}
		if a := parseFloat(r["again"]); r["again"] != "" && finite(a) {
			again[cat] = append(again[cat], math.Log(a))
			again["all"] = append(again["all"], math.Log(a))
		}
	}

	// Files: first page, all cores, memory.
	speedups := map[string]map[string][]float64{}
	rss := map[string][]float64{}
	for _, r := range files {
		name, cat := r["engine"], r["category"]
		if v := parseFloat(r["first"]); r["first"] != "" && finite(v) {
			for _, c := range []string{cat, "all"} {
				a := add("first", c, name)
				a.logs = append(a.logs, math.Log(v))
			}
		}
		groups := []string{"all"}
		switch n := parseInt(r["pages"]); {
		case n >= multiPages:
			groups = append(groups, "multipage")
		case n == 1:
			groups = append(groups, "singlepage")
		}
		if v := parseFloat(r["multi"]); r["multi"] != "" && finite(v) {
			for _, g := range groups {
				a := add("multi", g, name)
				a.logs = append(a.logs, math.Log(v))
				if r["multi_spread"] != "" {
					a.spreads = append(a.spreads, parseFloat(r["multi_spread"]))
				}
			}
		}
		if v := parseFloat(r["speedup"]); r["speedup"] != "" && finite(v) {
			for _, g := range groups {
				if speedups[g] == nil {
					speedups[g] = map[string][]float64{}
				}
				speedups[g][name] = append(speedups[g][name], math.Log(v))
			}
		}
		if r["pooled"] == "true" {
			s.Pooled[name] = true
		}
		if v := parseFloat(r["peak_rss_mb"]); r["peak_rss_mb"] != "" && v > 0 {
			rss[name] = append(rss[name], v)
		}
	}

	fold := func(t map[string]map[string]*acc) map[string]map[string]group {
		out := map[string]map[string]group{}
		for cat, mm := range t {
			out[cat] = map[string]group{}
			for name, a := range mm {
				g := group{N: len(a.logs), Failed: a.failed}
				if len(a.logs) > 0 {
					g.Ratio = math.Exp(mean(a.logs))
				}
				if len(a.spreads) > 0 {
					g.Spread = median(a.spreads)
				}
				out[cat][name] = g
			}
		}
		return out
	}
	s.Single, s.First, s.Multi = fold(tables["single"]), fold(tables["first"]), fold(tables["multi"])
	for g, mm := range speedups {
		s.Speedup[g] = map[string]float64{}
		for name, l := range mm {
			s.Speedup[g][name] = math.Exp(mean(l))
		}
	}
	for name, v := range rss {
		s.RSSMB[name] = median(v)
		s.RSSMaxMB[name] = slices.Max(v)
	}
	for cat, l := range again {
		s.Again[cat] = math.Exp(mean(l))
	}
	if len(allocs) > 0 {
		s.CeraAllocs.Median = median(allocs)
		s.CeraAllocs.P90 = quantile(allocs, 0.9)
		s.CeraAllocs.Max = slices.Max(allocs)
		s.CeraAllocs.Bytes = median(bytes)
	}
	s.Accuracy = readAccuracy(filepath.Join(dir, "..", "report-reference", "summary.json"))

	b, err = json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), b, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(markdown(s, names)), 0o644); err != nil {
		return err
	}
	return plot(dir, s, names)
}

// readCSV reads a CSV file with a header into maps by column.
func readCSV(path string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	var out []map[string]string
	for _, r := range rows[1:] {
		m := map[string]string{}
		for i, h := range rows[0] {
			if i < len(r) {
				m[h] = r[i]
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// plot draws the charts of s.
func plot(dir string, s *summary, names []string) error {
	m, refName := s.Meta, s.Meta.Ref
	var nat, wasm []string
	for _, n := range names {
		if strings.HasSuffix(n, "-wasm") {
			wasm = append(wasm, n)
		} else {
			nat = append(nat, n)
		}
	}
	if err := writeCharts(dir, "single", "One core, per page", refName, s.Single, nat, present(categoryOrder, s.Single)); err != nil {
		return err
	}
	if err := writeCharts(dir, "multi", fmt.Sprintf("All %d cores, whole documents", m.Cores), refName, s.Multi, nat, present(multiOrder, s.Multi)); err != nil {
		return err
	}
	return writeCharts(dir, "wasm", "WebAssembly, one core, per page", refName, s.Single, append([]string{refName, "pdfjs"}, wasm...), []string{"all"})
}

// readAccuracy reads each engine's content outlier share from the
// reference comparison's summary.
func readAccuracy(path string) map[string]float64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var v struct {
		Coarse struct {
			Outlier map[string]float64 `json:"outlier"`
		} `json:"coarse"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	out := map[string]float64{}
	for k, x := range v.Coarse.Outlier {
		out[k] = x
	}
	return out
}

// categoryOrder is the order of categories in tables.
var categoryOrder = []string{"all", "drawing", "vector", "paper", "text", "image", "scan", "shading", "transparency", "local"}

// multiOrder is the order of the groups of the all-cores comparison.
var multiOrder = []string{"all", "multipage", "singlepage"}

var categoryLabels = map[string]string{
	"all": "all pages", "drawing": "technical drawings", "vector": "vector graphics", "paper": "papers (arXiv)",
	"text": "text and fonts", "image": "images", "scan": "scans", "shading": "shadings",
	"transparency": "transparency", "local": "other",
	"multipage": "documents of 5+ pages", "singlepage": "one-page files",
}

func markdown(s *summary, names []string) string {
	var b strings.Builder
	m := s.Meta
	ref := engineLabels[m.Ref]
	fmt.Fprintf(&b, "# Speed against other engines\n\n")
	fmt.Fprintf(&b, "Time relative to %s (1.00× = as fast, 2.00× = twice the time, 0.50× = half). "+
		"Geometric mean of the ratios per page; each ratio is the median of %d paired runs. %v dpi.\n\n", ref, m.Runs, m.DPI)

	cats := present(categoryOrder, s.Single)
	table := func(title string, t map[string]map[string]group, cats []string, filter func(string) bool) {
		fmt.Fprintf(&b, "## %s\n\n| |", title)
		for _, c := range cats {
			l := categoryLabels[c]
			if c == "all" && strings.HasPrefix(title, "All") {
				l = "all files"
			}
			fmt.Fprintf(&b, " %s |", l)
		}
		b.WriteString("\n|---|")
		for range cats {
			b.WriteString("---|")
		}
		b.WriteString("\n")
		for _, name := range names {
			if !filter(name) {
				continue
			}
			fmt.Fprintf(&b, "| %s |", engineLabels[name])
			for _, c := range cats {
				g, ok := t[c][name]
				switch {
				case name == m.Ref && ok:
					b.WriteString(" 1.00× |")
				case !ok || g.N == 0:
					b.WriteString(" — |")
				default:
					fmt.Fprintf(&b, " %s |", ratioCell(g))
				}
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	native := func(n string) bool { return !strings.HasSuffix(n, "-wasm") }
	table("One core, per page", s.Single, cats, native)
	if len(s.Multi) > 0 {
		table(fmt.Sprintf("All %d cores, whole documents", m.Cores), s.Multi, present(multiOrder, s.Multi), native)
		for _, g := range present(multiOrder, s.Multi) {
			if len(s.Speedup[g]) == 0 {
				continue
			}
			fmt.Fprintf(&b, "Gain over one core, %s:", categoryLabels[g])
			for _, name := range names {
				if v, ok := s.Speedup[g][name]; ok {
					how := "own threads"
					if s.Pooled[name] {
						how = "one process per core"
					}
					fmt.Fprintf(&b, " %s %.1f× (%s);", engineLabels[name], v, how)
				}
			}
			b.WriteString("\n\n")
		}
	}
	table("WebAssembly, one core, per page", s.Single, []string{"all"}, func(n string) bool {
		return !native(n) || n == m.Ref || n == "pdfjs"
	})
	table("First page (open and draw page 1 in a fresh process)", s.First, present(categoryOrder, s.First), func(string) bool { return true })

	b.WriteString("## Memory and allocations\n\n| | peak RSS, median per file | largest |\n|---|---|---|\n")
	for _, name := range names {
		if v, ok := s.RSSMB[name]; ok {
			fmt.Fprintf(&b, "| %s | %.0f MB | %.0f MB |\n", engineLabels[name], v, s.RSSMaxMB[name])
		}
	}
	if s.CeraAllocs.Median > 0 {
		fmt.Fprintf(&b, "\ncera allocates %.0f times per page (median; 90th percentile %.0f, most %.0f), %s, the bitmap not counted.\n",
			s.CeraAllocs.Median, s.CeraAllocs.P90, s.CeraAllocs.Max, fmtBytes(s.CeraAllocs.Bytes))
	}
	if v, ok := s.Again["all"]; ok {
		fmt.Fprintf(&b, "Drawing a page again from cera's display list (scrolling, another tile) takes %.0f %% of its first render.\n", v*100)
	}
	b.WriteString("\n## Checks\n\n")
	for _, name := range names {
		if g, ok := s.Single["all"][name]; ok && g.Failed > 0 {
			fmt.Fprintf(&b, "- %s failed on %d pages %s drew.\n", engineLabels[name], g.Failed, ref)
		}
		if n := s.LessInk[name]; n > 0 {
			fmt.Fprintf(&b, "- %s drew less than half the ink of the median engine on %d pages.\n", engineLabels[name], n)
		}
	}
	if len(s.Accuracy) > 0 {
		b.WriteString("- Content differing from the other engines' consensus (accuracy/reference):")
		for _, name := range names {
			if v, ok := s.Accuracy[name]; ok {
				fmt.Fprintf(&b, " %s %.2f %%;", engineLabels[name], v*100)
			}
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\n%s, %d cores, %s, %s; corpus %s (%d files); %s.\n\n", m.CPU, m.Cores, m.OS, m.Go, m.Corpus, m.Files, m.Generated)
	for _, name := range names {
		fmt.Fprintf(&b, "- %s: %s\n", engineLabels[name], m.Versions[name])
	}
	for _, u := range m.Unavailable {
		fmt.Fprintf(&b, "- not available: %s\n", u)
	}
	return b.String()
}

func ratioCell(g group) string {
	s := fmt.Sprintf("%.2f×", g.Ratio)
	if g.Failed > 0 {
		s += fmt.Sprintf(" (%d failed)", g.Failed)
	}
	return s
}

func present(order []string, t map[string]map[string]group) []string {
	var out []string
	for _, c := range order {
		if _, ok := t[c]; ok {
			out = append(out, c)
		}
	}
	return out
}

// finite says whether a ratio can go into a geometric mean.
func finite(v float64) bool { return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }

func median(v []float64) float64 { return quantile(v, 0.5) }

func medianInt(v []int64) float64 {
	f := make([]float64, len(v))
	for i, x := range v {
		f[i] = float64(x)
	}
	return median(f)
}

// quantile interpolates linearly between order statistics.
func quantile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := slices.Clone(v)
	slices.Sort(s)
	p := q * float64(len(s)-1)
	lo := int(p)
	if lo+1 >= len(s) {
		return s[lo]
	}
	return s[lo] + (p-float64(lo))*(s[lo+1]-s[lo])
}

func iqr(v []float64) float64 { return quantile(v, 0.75) - quantile(v, 0.25) }

func mean(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func fmtF(v float64, prec int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return ""
	}
	return strconv.FormatFloat(v, 'f', prec, 64)
}

func fmtBytes(v float64) string {
	switch {
	case v >= 1<<20:
		return fmt.Sprintf("%.1f MB", v/(1<<20))
	case v >= 1<<10:
		return fmt.Sprintf("%.0f kB", v/(1<<10))
	}
	return fmt.Sprintf("%.0f B", v)
}

func oneLine(s string) string {
	s = strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
