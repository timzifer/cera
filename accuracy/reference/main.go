// Command reference measures cera against several references and, for
// the synthetic drawings, against an exact rendering, and writes a report
// with the statistics (HTML, CSV and JSON). It is meant for local runs on
// the pinned and the large corpora; CI gates on PDFium alone (../).
//
//	cd accuracy/reference
//	go run . -dir ../../testdata/corpus -out ../../report-reference
//	go run . -dir ../../testdata/borb -pages 3 -dpi 72 -sample 200
//
// The references are PDFium (WebAssembly), MuPDF (linked through cgo; its
// command-line tool without cgo), Poppler (pdftoppm) and Ghostscript (gs),
// whichever are available. No single reference is ground truth, so the
// report counts, for every engine, the pixels on which all the other
// engines agree and it does not: where the others disagree among
// themselves, a page says nothing about who is right. The synthetic
// drawings are also compared with an exact rendering (package exact).
//
// Renderings of the references are cached in -cache by engine version,
// file content, resolution and page; cera is always rendered.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/timzifer/cera"
	"github.com/timzifer/cera/internal/corpus"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "reference:", err)
		os.Exit(1)
	}
}

// pageResult is one compared page.
type pageResult struct {
	rel, category string
	page          int // 1-based
	size          image.Point
	engines       []string // rendered, in engineOrder
	failed        map[string]string
	fine, coarse  pageStats // pixels, and boxes of Box×Box pixels
	unsupported   []string
	ms            map[string]float64 // render time, 0 when cached
}

// shot is a page of the gallery: cera, the consensus of the references,
// and cera's outliers.
type shot struct {
	rel     string
	page    int
	share   float64
	cera    *image.RGBA
	cons    *image.RGBA
	outlier *image.RGBA
}

func run() error {
	dirs := flag.String("dir", "../../testdata/corpus", "comma-separated corpus directories")
	dpi := flag.Float64("dpi", 150, "resolution")
	pages := flag.Int("pages", 0, "pages per document (0 = all)")
	match := flag.String("match", "", "only files whose path contains this")
	sample := flag.Int("sample", 0, "compare a random batch of this many files (0 = all)")
	seed := flag.Uint64("seed", uint64(time.Now().YearDay()), "seed for -sample")
	engines := flag.String("engines", strings.Join(engineOrder, ","), "engines to compare")
	exactOn := flag.Bool("exact", true, "compare the synthetic drawings with an exact rendering")
	cacheDir := flag.String("cache", "../../testdata/reference-cache", "cache of the references' renderings")
	out := flag.String("out", "../../report-reference", "directory for report.html, pages.csv and summary.json")
	gallery := flag.Int("gallery", 12, "pages with cera's largest outlier share shown in the report")
	timeout := flag.Duration("timeout", time.Minute, "deadline per cera page")
	flag.Parse()

	files, err := listFiles(strings.Split(*dirs, ","), *match)
	if err != nil {
		return err
	}
	if *sample > 0 && *sample < len(files) {
		r := rand.New(rand.NewPCG(*seed, 0x6365726120))
		r.Shuffle(len(files), func(i, j int) { files[i], files[j] = files[j], files[i] })
		files = files[:*sample]
		slices.SortFunc(files, func(a, b *file) int { return strings.Compare(a.path, b.path) })
	}
	if len(files) == 0 {
		return fmt.Errorf("no PDFs in %s", *dirs)
	}

	var engs []engine
	var missing []string
	for _, name := range strings.Split(*engines, ",") {
		e, err := newEngine(strings.TrimSpace(name), *timeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", name, err)
			missing = append(missing, fmt.Sprintf("%s (%v)", name, err))
			continue
		}
		defer e.close()
		engs = append(engs, e)
	}
	if len(engs) < 3 {
		return fmt.Errorf("need at least three engines, have %d", len(engs))
	}
	var ex engine
	if *exactOn {
		ex = exactEngine{}
	}
	scale := *dpi / 72
	c := cache{dir: *cacheDir}

	var results []pageResult
	var shots []shot
	var skipped []string
	t0 := time.Now()
	for fi, f := range files {
		if err := f.load(scale); err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", f.rel, err))
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", f.rel, err)
			continue
		}
		idx := make([]int, len(f.sizes))
		for i := range idx {
			idx[i] = i
		}
		if *pages > 0 && len(idx) > *pages {
			idx = idx[:*pages]
		}
		// Every engine renders the file at once, each in its own goroutine.
		type rendering struct {
			imgs []*image.RGBA
			errs []error
			ms   float64
		}
		all := make([]rendering, len(engs)+1)
		var wg sync.WaitGroup
		for k, e := range append(slices.Clone(engs), ex) {
			if e == nil {
				continue
			}
			wg.Go(func() {
				t := time.Now()
				imgs, errs := c.render(e, f, idx, scale)
				all[k] = rendering{imgs, errs, float64(time.Since(t).Milliseconds())}
			})
		}
		wg.Wait()
		for k, i := range idx {
			r := pageResult{rel: f.rel, category: f.category, page: i + 1, size: f.sizes[i], failed: map[string]string{}, ms: map[string]float64{}}
			var names []string
			var imgs []*image.RGBA
			for j, e := range engs {
				if err := all[j].errs[k]; err != nil {
					r.failed[e.name()] = err.Error()
					continue
				}
				names = append(names, e.name())
				imgs = append(imgs, fit(all[j].imgs[k], f.sizes[i]))
				r.ms[e.name()] = all[j].ms / float64(len(idx))
			}
			var exImg *image.RGBA
			if ex != nil && all[len(engs)].errs[k] == nil {
				exImg = fit(all[len(engs)].imgs[k], f.sizes[i])
			}
			r.engines = names
			r.unsupported = f.unsupported[i]
			if len(names) < 3 {
				results = append(results, r)
				continue
			}
			var cons, omap *image.RGBA
			r.fine, cons, omap = analyse(names, imgs, exImg)
			small := make([]*image.RGBA, len(imgs))
			for j, img := range imgs {
				small[j] = boxDown(img, Box)
			}
			r.coarse, _, _ = analyse(names, small, nil)
			results = append(results, r)
			if at := slices.Index(names, "cera"); at >= 0 && r.coarse.agree["cera"] > 0 {
				share := float64(r.coarse.outlier["cera"]) / float64(r.coarse.ink)
				shots = append(shots, shot{f.rel, i + 1, share, thumb(imgs[at], 420, nil), thumb(cons, 420, nil), thumb(omap, 420, &outlierColor)})
				slices.SortFunc(shots, func(a, b shot) int { return cmpF(b.share, a.share) })
				shots = shots[:min(len(shots), *gallery)]
			}
		}
		fmt.Fprintf(os.Stderr, "[%d/%d] %s: %d pages, %s\n", fi+1, len(files), f.rel, len(idx), time.Since(t0).Round(time.Second))
		f.data = nil
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	rep := summarize(results, engs, ex != nil)
	rep.Dirs, rep.DPI, rep.Missing, rep.Skipped = *dirs, *dpi, missing, skipped
	rep.Seed, rep.Sample = *seed, *sample
	if err := writeCSV(filepath.Join(*out, "pages.csv"), results, rep.Engines); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(*out, "summary.json"), rep); err != nil {
		return err
	}
	if err := writeHTML(filepath.Join(*out, "report.html"), rep, shots); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", filepath.Join(*out, "report.html"))
	return nil
}

func cmpF(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func listFiles(dirs []string, match string) ([]*file, error) {
	cats := corpus.Categories()
	var files []*file
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
			files = append(files, &file{path: path, rel: rel, category: cat})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// load reads f and its page sizes as cera sees them.
func (f *file) load(scale float64) error {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return err
	}
	f.data = data
	sum := sha256.Sum256(data)
	f.key = hex.EncodeToString(sum[:12])
	doc, err := cera.Open(data)
	if err != nil {
		return fmt.Errorf("cera does not open it: %w", err)
	}
	for i := range doc.NumPages() {
		p, err := doc.Page(i)
		if err != nil {
			return fmt.Errorf("page %d: %w", i+1, err)
		}
		w, h := p.Size()
		f.sizes = append(f.sizes, p.Bounds(scale).Size())
		f.pts = append(f.pts, point{w, h})
	}
	if len(f.sizes) == 0 {
		return fmt.Errorf("no pages")
	}
	return nil
}

// cache keeps the references' renderings as PNG files.
type cache struct{ dir string }

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (c cache) path(e engine, f *file, scale float64, page int) string {
	v := unsafeChars.ReplaceAllString(e.version(), "_")
	return filepath.Join(c.dir, v, fmt.Sprintf("%s-%gdpi-p%d.png", f.key, scale*72, page+1))
}

// render returns the pages of f, from the cache where it has them.
func (c cache) render(e engine, f *file, pages []int, scale float64) ([]*image.RGBA, []error) {
	if e.version() == "" || c.dir == "" {
		return e.render(f, pages, scale)
	}
	imgs, errs := make([]*image.RGBA, len(pages)), make([]error, len(pages))
	var todo []int
	var at []int
	for k, i := range pages {
		if img, err := readPNG(c.path(e, f, scale, i)); err == nil {
			imgs[k] = img
			continue
		}
		todo = append(todo, i)
		at = append(at, k)
	}
	if len(todo) == 0 {
		return imgs, errs
	}
	ri, re := e.render(f, todo, scale)
	for j, k := range at {
		imgs[k], errs[k] = ri[j], re[j]
		if re[j] == nil {
			if err := writePNG(c.path(e, f, scale, todo[j]), ri[j]); err != nil {
				fmt.Fprintln(os.Stderr, "cache:", err)
			}
		}
	}
	return imgs, errs
}

func readPNG(path string) (*image.RGBA, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	img, err := png.Decode(fh)
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
	fh, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(fh, img); err != nil {
		fh.Close()
		return err
	}
	return fh.Close()
}
