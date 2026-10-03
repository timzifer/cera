// Command accuracy compares cera with PDFium page by page (ADR 0010).
//
//	cd accuracy
//	go run . -dir ../testdata/corpus -thresholds thresholds.json -out ../report-accuracy
//
// For every page it records the share of pixels differing by more than 16
// levels in any channel and the 99th percentile of the difference, next to
// both renderers' time and the Stats.Unsupported keys of the page, so a
// difference can be attributed to a feature. Each file's worst page is held
// against the threshold pinned for it in -thresholds: the run fails when a
// file gets worse or has no threshold. -update pins missing thresholds and
// lowers those of improved files; it never raises one.
//
// It is a module of its own so that cera does not depend on PDFium
// (WebAssembly, go-pdfium on wazero; no cgo). PDFium's renderings are kept
// in -refs, keyed by the file's SHA-256, page and resolution, so CI renders
// them once per corpus. The pinned corpus runs on every pull request; the
// large corpora run in random batches (-sample, -seed) in the scheduled
// workflow and in full locally, there without -thresholds (report only).
package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"io/fs"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/timzifer/cera"
	"github.com/timzifer/cera/internal/corpus"
)

// maxPixels skips absurd page sizes instead of exhausting memory.
const maxPixels = 64 << 20

var white = color.RGBA{255, 255, 255, 255}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "accuracy:", err)
		os.Exit(1)
	}
}

type config struct {
	scale   float64
	dpi     float64
	pages   int
	refs    string
	timeout time.Duration
	workers int
}

// page is the comparison of one page.
type page struct {
	file, category string
	path           string
	page           int // 1-based
	diff           diff
	ceraMS, refMS  float64 // refMS is 0 when the reference came from -refs
	unsupported    []string
	err            error // cera failed: the page counts as different throughout
	skip           string
}

// file is the result of a document: its worst page against its threshold.
type file struct {
	name, category string
	pages          int
	worst          *page
	p99            int
	ceraMS, refMS  float64
	unsupported    []string
	threshold      *threshold
	status         string
	skip           string
}

func run() error {
	dirs := flag.String("dir", "../testdata/corpus", "comma-separated corpus directories")
	dpi := flag.Float64("dpi", 150, "resolution")
	pages := flag.Int("pages", 0, "pages per document (0 = all)")
	match := flag.String("match", "", "only files whose path contains this")
	sample := flag.Int("sample", 0, "compare a random batch of this many files (0 = all)")
	seed := flag.Uint64("seed", uint64(time.Now().YearDay()), "seed for -sample (default: day of year)")
	thresholdsPath := flag.String("thresholds", "", "thresholds per file (JSON); empty: report only")
	update := flag.Bool("update", false, "pin missing thresholds and lower those of improved files")
	refs := flag.String("refs", "", "directory caching PDFium's renderings (default: a temporary one)")
	out := flag.String("out", "", "directory for report.md, results.csv and diffs/ (default: report to stdout)")
	worst := flag.Int("worst", 10, "pages written as diff images (cera, PDFium, difference)")
	timeout := flag.Duration("timeout", time.Minute, "deadline per cera page render")
	workers := flag.Int("workers", 0, "goroutines drawing one page with cera (0 = all cores)")
	flag.Parse()

	files, err := listFiles(strings.Split(*dirs, ","), *match)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no PDFs in %s", *dirs)
	}
	if *sample > 0 && *sample < len(files) {
		r := rand.New(rand.NewPCG(*seed, 0x6365726120))
		r.Shuffle(len(files), func(i, j int) { files[i], files[j] = files[j], files[i] })
		files = files[:*sample]
		slices.SortFunc(files, func(a, b corpusFile) int { return strings.Compare(a.path, b.path) })
		fmt.Fprintf(os.Stderr, "batch of %d files, seed %d\n", *sample, *seed)
	}
	var th thresholds
	if *thresholdsPath != "" {
		if th, err = readThresholds(*thresholdsPath); err != nil {
			return err
		}
	}
	if *refs == "" {
		if *refs, err = os.MkdirTemp("", "cera-pdfium-*"); err != nil {
			return err
		}
		defer os.RemoveAll(*refs)
	}
	if err := os.MkdirAll(*refs, 0o755); err != nil {
		return err
	}

	cfg := config{scale: *dpi / 72, dpi: *dpi, pages: *pages, refs: *refs, timeout: *timeout, workers: *workers}
	var ref *pdfiumEngine // started on the first reference not cached
	defer func() {
		if ref != nil {
			ref.Close()
		}
	}()
	var all []page
	var results []file
	for _, f := range files {
		ps, skip := cfg.compareFile(f, &ref)
		for i := range ps {
			p := &ps[i]
			switch {
			case p.skip != "":
				fmt.Fprintf(os.Stderr, "skip %s p%d: %s\n", p.file, p.page, p.skip)
			case p.err != nil:
				fmt.Fprintf(os.Stderr, "FAIL %s p%d: %v\n", p.file, p.page, p.err)
			default:
				fmt.Fprintf(os.Stderr, "%6.2f%% p99 %3d  %s p%d\n", 100*p.diff.over, p.diff.p99, p.file, p.page)
			}
		}
		if skip != "" {
			fmt.Fprintf(os.Stderr, "skip %s: %s\n", f.rel, skip)
		}
		all = append(all, ps...)
		results = append(results, summarize(f, ps, skip))
	}

	failed, changed := judge(results, th, *thresholdsPath != "", *update)
	if *update && changed {
		if err := th.write(*thresholdsPath); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "updated %s\n", *thresholdsPath)
	}

	w := io.Writer(os.Stdout)
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			return err
		}
		f, err := os.Create(filepath.Join(*out, "report.md"))
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
		if err := writeCSV(filepath.Join(*out, "results.csv"), all); err != nil {
			return err
		}
		if err := cfg.writeDiffs(filepath.Join(*out, "diffs"), all, *worst); err != nil {
			return err
		}
	}
	writeReport(w, results, all, cfg, *thresholdsPath != "", *worst, *out != "")
	annotate(results)
	if failed > 0 {
		return fmt.Errorf("%d files worse than their threshold or without one (pin new files with -update)", failed)
	}
	return nil
}

type corpusFile struct{ path, rel, cat string }

func listFiles(dirs []string, match string) ([]corpusFile, error) {
	cats := corpus.Categories()
	var files []corpusFile
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".pdf") {
				return err
			}
			rel, _ := filepath.Rel(dir, path)
			rel = filepath.ToSlash(rel)
			if match != "" && !strings.Contains(rel, match) {
				return nil
			}
			cat, ok := cats[rel]
			switch {
			case ok:
			case strings.HasPrefix(rel, "synthetic/"):
				cat = "drawing"
			default:
				cat = "local"
			}
			files = append(files, corpusFile{path, rel, cat})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// compareFile renders every page of f with cera and compares it with
// PDFium's rendering. skip says why the file is not compared at all.
func (c config) compareFile(f corpusFile, ref **pdfiumEngine) (ps []page, skip string) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return nil, err.Error()
	}
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:12])

	var pd *pdfiumDoc
	defer func() {
		if pd != nil {
			pd.Close()
		}
	}()
	refPage := func(i int) (*image.RGBA, float64, error) {
		path := c.refPath(key, i)
		if img, err := readPNG(path); err == nil {
			return img, 0, nil
		}
		if *ref == nil {
			if *ref, err = newPDFium(); err != nil {
				return nil, 0, err
			}
		}
		if pd == nil {
			if pd, err = (*ref).open(data); err != nil {
				return nil, 0, err
			}
		}
		if i >= pd.n {
			return nil, 0, fmt.Errorf("PDFium has %d pages", pd.n)
		}
		t0 := time.Now()
		img, err := pd.render(i, c.scale)
		if err != nil {
			return nil, 0, err
		}
		t := ms(time.Since(t0))
		return img, t, writePNG(path, img)
	}

	doc, err := cera.Open(data)
	if err != nil {
		// Whether PDFium opens it decides whether this is cera's failure.
		if _, _, rerr := refPage(0); rerr != nil {
			return nil, "neither cera nor PDFium opens it"
		}
		return []page{{file: f.rel, category: f.cat, path: f.path, page: 1, err: err, diff: diff{over: 1, p99: 255, max: 255}}}, ""
	}
	n := doc.NumPages()
	if c.pages > 0 {
		n = min(n, c.pages)
	}
	for i := range n {
		p := page{file: f.rel, category: f.cat, path: f.path, page: i + 1}
		img, rms, err := refPage(i)
		if err != nil {
			p.skip = "no reference: " + err.Error()
			ps = append(ps, p)
			continue
		}
		p.refMS = rms
		got, st, t, err := c.renderCera(doc, i)
		p.ceraMS, p.unsupported = t, st.UnsupportedKeys()
		var pe *cera.PanicError
		if got == nil || errors.As(err, &pe) || errors.Is(err, cera.ErrDeadline) {
			p.err = cmp.Or(err, errors.New("not rendered"))
			p.diff = diff{over: 1, p99: 255, max: 255}
		} else {
			p.diff = compare(got, img)
		}
		ps = append(ps, p)
	}
	return ps, ""
}

func (c config) refPath(key string, i int) string {
	return filepath.Join(c.refs, fmt.Sprintf("%s-%gdpi-p%d.png", key, c.dpi, i+1))
}

func (c config) renderCera(doc *cera.Document, i int) (*image.RGBA, cera.Stats, float64, error) {
	var st cera.Stats
	p, err := doc.Page(i)
	if err != nil {
		return nil, st, 0, err
	}
	defer p.Release()
	b := p.Bounds(c.scale)
	if b.Dx()*b.Dy() > maxPixels {
		return nil, st, 0, fmt.Errorf("page of %d×%d px", b.Dx(), b.Dy())
	}
	img := image.NewRGBA(b)
	t0 := time.Now()
	err = p.Render(context.Background(), img, cera.RenderOptions{
		Scale: c.scale, Background: white, Stats: &st, Deadline: time.Now().Add(c.timeout), Workers: c.workers,
	})
	return img, st, ms(time.Since(t0)), err
}

func summarize(f corpusFile, ps []page, skip string) file {
	r := file{name: f.rel, category: f.cat, skip: skip}
	keys := map[string]bool{}
	for i := range ps {
		p := &ps[i]
		if p.skip != "" {
			continue
		}
		r.pages++
		r.ceraMS += p.ceraMS
		r.refMS += p.refMS
		if r.worst == nil || p.diff.over > r.worst.diff.over {
			r.worst = p
		}
		r.p99 = max(r.p99, p.diff.p99)
		for _, k := range p.unsupported {
			keys[k] = true
		}
	}
	for k := range keys {
		r.unsupported = append(r.unsupported, k)
	}
	slices.Sort(r.unsupported)
	if r.pages == 0 && r.skip == "" {
		r.skip = "no page compared"
	}
	return r
}

// over is the worst page's share of differing pixels, in percent.
func (r *file) over() float64 {
	if r.worst == nil {
		return 0
	}
	return 100 * r.worst.diff.over
}

// judge sets the status of every file against th and counts the failures;
// with update it pins and lowers thresholds in th.
func judge(rs []file, th thresholds, gate, update bool) (failed int, changed bool) {
	for i := range rs {
		r := &rs[i]
		if r.skip != "" {
			r.status = "skipped"
			continue
		}
		if !gate {
			r.status = "–"
			continue
		}
		v := r.over()
		t, ok := th[r.name]
		switch {
		case !ok && update:
			th[r.name] = threshold{Over: pinned(v)}
			r.status, changed = "pinned", true
		case !ok:
			r.status = "**no threshold**"
			failed++
		case v > t.Over:
			r.status = "**worse**"
			failed++
		case pinned(v) < t.Over && update:
			t.Over = pinned(v)
			th[r.name] = t
			r.status, changed = "lowered", true
		case pinned(v) < t.Over:
			r.status = "better"
		default:
			r.status = "ok"
		}
		if t, ok := th[r.name]; ok {
			r.threshold = &t
		}
	}
	return failed, changed
}

// annotate marks changed files in the GitHub Actions log.
func annotate(rs []file) {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return
	}
	for i := range rs {
		r := &rs[i]
		switch r.status {
		case "**worse**":
			fmt.Printf("::error title=accuracy::%s: %.2f%% of pixels differ (page %d), threshold %.2f%%\n", r.name, r.over(), r.worst.page, r.threshold.Over)
		case "**no threshold**":
			fmt.Printf("::error title=accuracy::%s has no threshold; pin it with -update\n", r.name)
		case "better":
			fmt.Printf("::notice title=accuracy::%s improved to %.2f%%; lower its threshold %.2f%% with -update in this pull request\n", r.name, r.over(), r.threshold.Over)
		}
	}
}

func writeReport(w io.Writer, rs []file, ps []page, c config, gate bool, worst int, diffs bool) {
	var pages, failed int
	var ceraMS, refMS float64
	for i := range rs {
		pages += rs[i].pages
		ceraMS += rs[i].ceraMS
		refMS += rs[i].refMS
		if strings.HasPrefix(rs[i].status, "**") {
			failed++
		}
	}
	fmt.Fprintf(w, "# cera against PDFium\n\n%s · %.0f dpi · %d files · %d pages · %s/%s · %s\n\n",
		time.Now().UTC().Format("2006-01-02 15:04 MST"), c.dpi, len(rs), pages, runtime.GOOS, runtime.GOARCH, runtime.Version())
	fmt.Fprintf(w, "Per page: the share of pixels differing by more than %d levels in any channel (`>%d`) and the 99th percentile of the difference (`p99`); per file its worst page.", overLevels, overLevels)
	if gate {
		fmt.Fprintf(w, " **%d files fail** their threshold.", failed)
	}
	fmt.Fprintf(w, " cera %.0f ms in all, PDFium %.0f ms (0 for renderings from the cache).\n\n", ceraMS, refMS)

	slices.SortStableFunc(rs, func(a, b file) int {
		if fa, fb := strings.HasPrefix(a.status, "**"), strings.HasPrefix(b.status, "**"); fa != fb {
			if fa {
				return -1
			}
			return 1
		}
		return cmp.Compare(b.over(), a.over())
	})
	fmt.Fprintf(w, "## Files\n\n| file | category | pages | >%d (worst page) | p99 | threshold | status | cera ms | PDFium ms | unsupported |\n|---|---|---:|---:|---:|---:|---|---:|---:|---|\n", overLevels)
	for i := range rs {
		r := &rs[i]
		if r.skip != "" {
			fmt.Fprintf(w, "| %s | %s | – | – | – | – | skipped: %s | | | |\n", r.name, r.category, cell(r.skip))
			continue
		}
		t := "–"
		if r.threshold != nil {
			t = fmt.Sprintf("%.2f %%", r.threshold.Over)
		}
		fmt.Fprintf(w, "| %s | %s | %d | %.2f %% (p%d) | %d | %s | %s | %.0f | %.0f | %s |\n",
			r.name, r.category, r.pages, r.over(), r.worst.page, r.p99, t, r.status, r.ceraMS, r.refMS, strings.Join(r.unsupported, " "))
	}

	// Which features the differing pages use, to attribute differences.
	type agg struct {
		pages int
		over  float64
	}
	byKey := map[string]*agg{}
	for i := range ps {
		p := &ps[i]
		if p.skip != "" {
			continue
		}
		for _, k := range p.unsupported {
			a := byKey[k]
			if a == nil {
				a = &agg{}
				byKey[k] = a
			}
			a.pages++
			a.over += p.diff.over
		}
	}
	if len(byKey) > 0 {
		keys := make([]string, 0, len(byKey))
		for k := range byKey {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int { return cmp.Compare(byKey[b].over, byKey[a].over) })
		fmt.Fprintf(w, "\n## Unsupported features on compared pages\n\n| key | pages | mean >%d |\n|---|---:|---:|\n", overLevels)
		for _, k := range keys {
			a := byKey[k]
			fmt.Fprintf(w, "| %s | %d | %.2f %% |\n", k, a.pages, 100*a.over/float64(a.pages))
		}
	}

	if diffs && worst > 0 {
		fmt.Fprintf(w, "\n## Worst pages\n\nIn the artifact under `diffs/`: cera, PDFium, and their difference (red: cera darker, blue: lighter).\n\n| page | >%d | p99 | unsupported | images |\n|---|---:|---:|---|---|\n", overLevels)
		for _, p := range worstPages(ps, worst) {
			fmt.Fprintf(w, "| %s p%d | %.2f %% | %d | %s | `%s-*.png` |\n", p.file, p.page, 100*p.diff.over, p.diff.p99, strings.Join(p.unsupported, " "), diffName(p))
		}
	}
}

func cell(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

func worstPages(ps []page, n int) []*page {
	var list []*page
	for i := range ps {
		if ps[i].skip == "" && ps[i].diff.over > 0 {
			list = append(list, &ps[i])
		}
	}
	slices.SortStableFunc(list, func(a, b *page) int { return cmp.Compare(b.diff.over, a.diff.over) })
	return list[:min(n, len(list))]
}

func diffName(p *page) string {
	return fmt.Sprintf("%s-p%d", strings.ReplaceAll(p.file, "/", "_"), p.page)
}

// writeDiffs writes cera's rendering, PDFium's and their difference for
// the n worst pages.
func (c config) writeDiffs(dir string, ps []page, n int) error {
	for _, p := range worstPages(ps, n) {
		if p.err != nil {
			continue
		}
		data, err := os.ReadFile(p.path)
		if err != nil {
			return err
		}
		doc, err := cera.Open(data)
		if err != nil {
			continue
		}
		got, _, _, _ := c.renderCera(doc, p.page-1)
		sum := sha256.Sum256(data)
		ref, err := readPNG(c.refPath(hex.EncodeToString(sum[:12]), p.page-1))
		if got == nil || err != nil {
			continue
		}
		base := filepath.Join(dir, diffName(p))
		for suffix, img := range map[string]*image.RGBA{"cera": got, "pdfium": ref, "diff": diffImage(got, ref)} {
			if err := writePNG(base+"-"+suffix+".png", img); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeCSV(path string, ps []page) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cw := csv.NewWriter(f)
	cw.Write([]string{"file", "category", "page", "over16", "p99", "max", "cera_ms", "pdfium_ms", "unsupported", "error"})
	for i := range ps {
		p := &ps[i]
		e := p.skip
		if p.err != nil {
			e = p.err.Error()
		}
		cw.Write([]string{
			p.file, p.category, strconv.Itoa(p.page), strconv.FormatFloat(p.diff.over, 'f', 6, 64),
			strconv.Itoa(p.diff.p99), strconv.Itoa(p.diff.max),
			fmt.Sprintf("%.3f", p.ceraMS), fmt.Sprintf("%.3f", p.refMS), strings.Join(p.unsupported, " "), e,
		})
	}
	cw.Flush()
	return cw.Error()
}

func ms(d time.Duration) float64 { return float64(d) / 1e6 }

func readPNG(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	if m, ok := img.(*image.RGBA); ok && m.Rect.Min == (image.Point{}) {
		return m, nil
	}
	b := img.Bounds()
	m := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(m, m.Rect, img, b.Min, draw.Src)
	return m, nil
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// pinned is the threshold pinned for a file whose worst page differs in v
// percent of its pixels: v plus a margin of a tenth and 0.05 points, rounded
// up to hundredths.
func pinned(v float64) float64 {
	return math.Ceil(math.Round((v*1.1+0.05)*1e6)/1e4) / 100
}
