// Command corpus fetches the test corpus and renders it with cera: speed,
// allocations, unsupported features and robustness per page.
//
//	go run ./cmd/corpus fetch  -dir testdata/corpus
//	go run ./cmd/corpus scenes -dir testdata/corpus
//	go run ./cmd/corpus run    -dir testdata/corpus -dpi 150 -out report
//
// Large external corpora (robustness only, scheduled CI):
//
//	go run ./cmd/corpus sources
//	go run ./cmd/corpus get -source borb
//	go run ./cmd/corpus get -source ccmain -sample 500 -seed 42
//	go run ./cmd/corpus run -dir testdata/borb -runs 0 -pages 3 -fail-open=false
//
// CI renders random batches of the large corpora (-sample files chosen by
// -seed); the whole of them is run locally. Every report lists the slowest
// pages and those with the most allocations (-top), to catch inputs the
// budgets do not yet cover.
//
// run exits with status 1 when any page panics, hangs or exceeds its
// deadline, and (with -fail-open, the default) when a file does not open,
// so CI blocks on robustness regressions.
package main

import (
	"cmp"
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
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

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "fetch":
		fl := flag.NewFlagSet("fetch", flag.ExitOnError)
		dir := fl.String("dir", "testdata/corpus", "corpus directory")
		fl.Parse(os.Args[2:])
		err = corpus.Fetch(*dir, os.Stdout)
	case "scenes":
		fl := flag.NewFlagSet("scenes", flag.ExitOnError)
		dir := fl.String("dir", "testdata/corpus", "corpus directory")
		fl.Parse(os.Args[2:])
		var paths []string
		if paths, err = corpus.WriteScenes(*dir); err == nil {
			fmt.Printf("wrote %d synthetic drawings\n", len(paths))
		}
	case "run":
		err = run(os.Args[2:])
	case "sources":
		for _, s := range corpus.Sources() {
			kind := "git " + s.Commit[:min(12, len(s.Commit))]
			if s.Repo == "" {
				kind = fmt.Sprintf("zip, %d shards", s.Shards)
			}
			unsafe := ""
			if s.Unsafe {
				unsafe = " [unsafe: on demand only]"
			}
			fmt.Printf("%-8s %-22s %s%s\n", s.Name, kind, s.Note, unsafe)
		}
	case "get":
		fl := flag.NewFlagSet("get", flag.ExitOnError)
		name := fl.String("source", "", "source name (see corpus sources)")
		dir := fl.String("dir", "", "target directory (default testdata/<source>)")
		shard := fl.Int("shard", -1, "zip shard (-1 = chosen by -seed)")
		sample := fl.Int("sample", 0, "PDFs to extract from a zip shard (0 = all)")
		seed := fl.Uint64("seed", uint64(time.Now().YearDay()), "seed for shard and sample (default: day of year)")
		fl.Parse(os.Args[2:])
		src, ok := corpus.LookupSource(*name)
		if !ok {
			err = fmt.Errorf("unknown source %q", *name)
			break
		}
		if *dir == "" {
			*dir = filepath.Join("testdata", src.Name)
		}
		err = src.Get(*dir, *shard, *sample, *seed, os.Stdout)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: corpus fetch|scenes|sources|get|run [flags]")
	os.Exit(2)
}

type result struct {
	file, category string
	page           int
	w, h           int
	min, median    time.Duration
	allocs         uint64
	again          time.Duration // median render with the display list cached
	bytes          uint64
	stats          cera.Stats
	err            error
	hang           bool
}

func (r *result) fatal() bool {
	var pe *cera.PanicError
	return r.hang || errors.As(r.err, &pe) || errors.Is(r.err, cera.ErrDeadline)
}

func run(args []string) error {
	fl := flag.NewFlagSet("run", flag.ExitOnError)
	dirs := fl.String("dir", "testdata/corpus", "comma-separated corpus directories")
	dpi := fl.Float64("dpi", 150, "resolution")
	runs := fl.Int("runs", 3, "timed renders per page after one warm-up (0 = robustness only)")
	failOpen := fl.Bool("fail-open", true, "count files that do not open as failures")
	pages := fl.Int("pages", 0, "pages per document (0 = all)")
	timeout := fl.Duration("timeout", time.Minute, "deadline per page render")
	match := fl.String("match", "", "only files whose path contains this")
	out := fl.String("out", "", "directory for report.md and results.csv (default: report to stdout)")
	images := fl.String("images", "", "directory to write every rendered page as PNG")
	workers := fl.Int("workers", 1, "goroutines drawing one page (0 = all cores)")
	sample := fl.Int("sample", 0, "render a random batch of this many files (0 = all)")
	seed := fl.Uint64("seed", uint64(time.Now().YearDay()), "seed for -sample (default: day of year)")
	top := fl.Int("top", 20, "slowest pages and pages with the most allocations listed")
	fl.Parse(args)

	cats := corpus.Categories()
	var files []struct{ path, rel, cat string }
	for _, dir := range strings.Split(*dirs, ",") {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".pdf") {
				return err
			}
			rel, _ := filepath.Rel(dir, path)
			rel = filepath.ToSlash(rel)
			if *match != "" && !strings.Contains(rel, *match) {
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
			files = append(files, struct{ path, rel, cat string }{path, rel, cat})
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(files) == 0 {
		return fmt.Errorf("no PDFs in %s (run fetch and scenes first)", *dirs)
	}
	if *sample > 0 && *sample < len(files) {
		// A batch reproducible from its seed, printed for reruns.
		r := rand.New(rand.NewPCG(*seed, 0x6365726120))
		r.Shuffle(len(files), func(i, j int) { files[i], files[j] = files[j], files[i] })
		files = files[:*sample]
		slices.SortFunc(files, func(a, b struct{ path, rel, cat string }) int { return strings.Compare(a.path, b.path) })
		fmt.Fprintf(os.Stderr, "batch of %d files, seed %d\n", *sample, *seed)
	}

	cfg := runConfig{scale: *dpi / 72, runs: *runs, pages: *pages, timeout: *timeout, images: *images, workers: *workers}
	var results []result
	for _, f := range files {
		rs, err := cfg.file(f.path, f.rel, f.cat)
		if err != nil {
			return err
		}
		for i := range rs {
			r := &rs[i]
			status := "ok  "
			switch {
			case r.fatal():
				status = "FAIL"
			case r.page < 0 && !*failOpen:
				status = "skip"
			}
			if r.page < 0 {
				fmt.Fprintf(os.Stderr, "%s %s: %v\n", status, r.file, r.err)
			} else {
				fmt.Fprintf(os.Stderr, "%s %s p%d %.1f ms %v\n", status, r.file, r.page, ms(r.median), errString(r.err))
			}
		}
		results = append(results, rs...)
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
		if err := writeCSV(filepath.Join(*out, "results.csv"), results); err != nil {
			return err
		}
	}
	writeReport(w, results, *dpi, *runs, *workers, *top)

	var fatal, unopened int
	for i := range results {
		switch {
		case results[i].fatal():
			fatal++
		case results[i].page < 0:
			unopened++
		}
	}
	if *failOpen {
		fatal += unopened
	}
	if fatal > 0 {
		return fmt.Errorf("%d of %d results failed (panic, hang, deadline or unopened file)", fatal, len(results))
	}
	return nil
}

type runConfig struct {
	scale   float64
	runs    int
	pages   int
	timeout time.Duration
	images  string
	workers int
}

// file renders one document. A watchdog bounds the whole file (opening
// included): a hang is reported as a failure and the stuck goroutine is
// abandoned, so one bad file cannot stall a corpus run.
func (c runConfig) file(path, rel, cat string) ([]result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	type outcome struct {
		rs  []result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		pw, _ := corpus.Password(path)
		rs, err := c.render(data, pw, rel, cat)
		done <- outcome{rs, err}
	}()
	budget := c.timeout * time.Duration(max(c.runs, 1)+2)
	if c.pages > 0 {
		budget *= time.Duration(c.pages)
	} else {
		budget *= 4
	}
	select {
	case o := <-done:
		return o.rs, o.err
	case <-time.After(budget):
		return []result{{file: rel, category: cat, page: 0, hang: true, err: fmt.Errorf("hang: no result after %v", budget)}}, nil
	}
}

// render renders a document; password opens it if it is encrypted.
func (c runConfig) render(data []byte, password, rel, cat string) ([]result, error) {
	doc, err := cera.OpenWith(data, cera.OpenOptions{Password: password})
	if err != nil {
		return []result{{file: rel, category: cat, page: -1, err: err}}, nil
	}
	n := doc.NumPages()
	if c.pages > 0 {
		n = min(n, c.pages)
	}
	var rs []result
	var dst *image.RGBA
	paper := color.RGBA{255, 255, 255, 255}
	for i := range n {
		r := result{file: rel, category: cat, page: i + 1}
		p, err := doc.Page(i)
		if err != nil {
			r.err = err
			rs = append(rs, r)
			continue
		}
		b := p.Bounds(c.scale)
		if b.Dx()*b.Dy() > maxPixels {
			r.err = fmt.Errorf("page of %d×%d px skipped", b.Dx(), b.Dy())
			rs = append(rs, r)
			continue
		}
		if dst == nil || dst.Rect != b {
			dst = image.NewRGBA(b)
		}
		r.w, r.h = b.Dx(), b.Dy()
		render := func(st *cera.Stats) error {
			return p.Render(context.Background(), dst, cera.RenderOptions{
				Scale: c.scale, Background: paper, Stats: st, Deadline: time.Now().Add(c.timeout),
				Workers: c.workers,
			})
		}
		// Warm-up render: fills caches and pools, reports stats and errors.
		// Without timed runs its allocations are the page's.
		var m0, m1 runtime.MemStats
		if c.runs == 0 {
			runtime.ReadMemStats(&m0)
		}
		t0 := time.Now()
		r.err = render(&r.stats)
		r.min, r.median = time.Since(t0), time.Since(t0)
		if c.runs == 0 {
			runtime.ReadMemStats(&m1)
			r.allocs, r.bytes = m1.Mallocs-m0.Mallocs, m1.TotalAlloc-m0.TotalAlloc
		}
		if !r.fatal() && c.runs > 0 {
			// A first render interprets the page into a display list; a
			// render again at the same scale (another tile, a scrolled
			// viewport) only draws it. Both are timed.
			var times, again []time.Duration
			for k := range c.runs {
				p.Release()
				if k == 0 {
					runtime.ReadMemStats(&m0)
				}
				t0 := time.Now()
				_ = render(nil) // errors were reported by the warm-up render
				times = append(times, time.Since(t0))
				if k == 0 {
					runtime.ReadMemStats(&m1)
					r.allocs = m1.Mallocs - m0.Mallocs
					r.bytes = m1.TotalAlloc - m0.TotalAlloc
				}
				t0 = time.Now()
				_ = render(nil)
				again = append(again, time.Since(t0))
			}
			slices.Sort(times)
			slices.Sort(again)
			r.min, r.median = times[0], times[len(times)/2]
			r.again = again[len(again)/2]
		}
		p.Release()
		if c.images != "" {
			name := fmt.Sprintf("%s-p%d.png", strings.ReplaceAll(rel, "/", "_"), i+1)
			if err := writePNG(filepath.Join(c.images, name), dst); err != nil {
				return nil, err
			}
		}
		rs = append(rs, r)
	}
	return rs, nil
}

// maxPixels skips absurd page sizes (a 200×200 inch page at 150 dpi is
// 900 M px) instead of exhausting the runner's memory.
const maxPixels = 200 << 20

func ms(d time.Duration) float64 { return float64(d) / 1e6 }

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeReport(w io.Writer, rs []result, dpi float64, runs, workers, top int) {
	ws := fmt.Sprintf("%d workers", workers)
	if workers <= 0 {
		ws = "all cores"
	}
	fmt.Fprintf(w, "# cera corpus report\n\n%s · %.0f dpi · %d timed runs/page after warm-up · %s · %s/%s, %d CPUs · %s\n\n",
		time.Now().UTC().Format("2006-01-02 15:04 MST"), dpi, runs, ws, runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.Version())

	type agg struct {
		pages, fatal, errs int
		times, again       []float64
		allocs             []float64
	}
	byCat := map[string]*agg{}
	all := &agg{}
	unsup := map[string]int{}
	for i := range rs {
		r := &rs[i]
		for _, a := range []*agg{all, byCat[r.category]} {
			if a == nil {
				a = &agg{}
				byCat[r.category] = a
			}
			a.pages++
			if r.fatal() {
				a.fatal++
				continue
			}
			if r.err != nil {
				a.errs++
			}
			a.times = append(a.times, ms(r.median))
			a.again = append(a.again, ms(r.again))
			a.allocs = append(a.allocs, float64(r.allocs))
		}
		for k, v := range r.stats.Unsupported {
			unsup[k] += v
		}
	}
	med := func(v []float64) float64 {
		if len(v) == 0 {
			return 0
		}
		v = slices.Clone(v)
		slices.Sort(v)
		return v[len(v)/2]
	}
	sum := func(v []float64) (s float64) {
		for _, x := range v {
			s += x
		}
		return
	}
	fmt.Fprintf(w, "## Summary\n\n| category | pages | failed | partial | median ms/page | total ms | total ms again (cached) | median allocs/page |\n|---|---:|---:|---:|---:|---:|---:|---:|\n")
	row := func(name string, a *agg) {
		fmt.Fprintf(w, "| %s | %d | %d | %d | %.1f | %.0f | %.0f | %.0f |\n", name, a.pages, a.fatal, a.errs, med(a.times), sum(a.times), sum(a.again), med(a.allocs))
	}
	row("**all**", all)
	cats := make([]string, 0, len(byCat))
	for c := range byCat {
		cats = append(cats, c)
	}
	slices.Sort(cats)
	for _, c := range cats {
		row(c, byCat[c])
	}

	if len(unsup) > 0 {
		fmt.Fprintf(w, "\n## Not yet supported (occurrences)\n\n| feature | count |\n|---|---:|\n")
		keys := make([]string, 0, len(unsup))
		for k := range unsup {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int { return unsup[b] - unsup[a] })
		for _, k := range keys {
			fmt.Fprintf(w, "| %s | %d |\n", k, unsup[k])
		}
	}

	writeTop(w, rs, top)

	fmt.Fprintf(w, "\n## Pages\n\n| file | page | size | ms (min) | ms (median) | ms again | allocs | ops | content errors | note |\n|---|---:|---|---:|---:|---:|---:|---:|---:|---|\n")
	for i := range rs {
		r := &rs[i]
		note := errString(r.err)
		if r.fatal() {
			note = "**FAIL** " + note
		}
		note = strings.ReplaceAll(note, "|", "\\|")
		page := strconv.Itoa(r.page)
		if r.page < 0 {
			page = "–"
		}
		fmt.Fprintf(w, "| %s | %s | %d×%d | %.1f | %.1f | %.1f | %d | %d | %d | %s |\n",
			r.file, page, r.w, r.h, ms(r.min), ms(r.median), ms(r.again), r.allocs, r.stats.Ops, r.stats.Errors, note)
	}
}

func writeCSV(path string, rs []result) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cw := csv.NewWriter(f)
	cw.Write([]string{"file", "category", "page", "width", "height", "min_ms", "median_ms", "again_ms", "allocs", "alloc_bytes", "ops", "fills", "strokes", "clips", "content_errors", "unsupported", "error"})
	for i := range rs {
		r := &rs[i]
		var unsup []string
		for _, k := range r.stats.UnsupportedKeys() {
			unsup = append(unsup, fmt.Sprintf("%s=%d", k, r.stats.Unsupported[k]))
		}
		cw.Write([]string{
			r.file, r.category, strconv.Itoa(r.page), strconv.Itoa(r.w), strconv.Itoa(r.h),
			fmt.Sprintf("%.3f", ms(r.min)), fmt.Sprintf("%.3f", ms(r.median)), fmt.Sprintf("%.3f", ms(r.again)),
			strconv.FormatUint(r.allocs, 10), strconv.FormatUint(r.bytes, 10),
			strconv.Itoa(r.stats.Ops), strconv.Itoa(r.stats.Fills), strconv.Itoa(r.stats.Strokes), strconv.Itoa(r.stats.Clips),
			strconv.Itoa(r.stats.Errors), strings.Join(unsup, " "), errString(r.err),
		})
	}
	cw.Flush()
	return cw.Error()
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// writeTop lists the n slowest pages and the n with the most allocations.
func writeTop(w io.Writer, rs []result, n int) {
	if n <= 0 {
		return
	}
	var pages []*result
	for i := range rs {
		if rs[i].page > 0 {
			pages = append(pages, &rs[i])
		}
	}
	table := func(title string, cmp func(a, b *result) int) {
		slices.SortStableFunc(pages, cmp)
		fmt.Fprintf(w, "\n## %s (top %d)\n\n| file | page | ms | allocs | MB allocated | unsupported | note |\n|---|---:|---:|---:|---:|---|---|\n", title, n)
		for _, r := range pages[:min(n, len(pages))] {
			note := errString(r.err)
			if r.fatal() {
				note = "**FAIL** " + note
			}
			fmt.Fprintf(w, "| %s | %d | %.1f | %d | %.1f | %s | %s |\n", r.file, r.page, ms(r.median), r.allocs,
				float64(r.bytes)/(1<<20), strings.Join(r.stats.UnsupportedKeys(), " "), strings.ReplaceAll(note, "|", "\\|"))
		}
	}
	table("Slowest pages", func(a, b *result) int { return cmp.Compare(b.median, a.median) })
	table("Most allocations", func(a, b *result) int { return cmp.Compare(b.allocs, a.allocs) })
}
