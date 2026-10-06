// Command bench measures how fast cera renders against other PDF engines,
// and reports only ratios: times depend on the machine and its load, so
// they are measured, turned into ratios at once and never written out.
// See docs/performance.md.
//
//	cd bench
//	go run .                                  # every available engine on the pinned corpus
//	go run . -engines cera,mupdf -match synthetic/
//	go run . -cli                             # command-line tools, PDF to PNG
//	go run . -dir ../testdata/borb -runs 0    # robustness only: who fails where
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/timzifer/cera/internal/corpus"
)

var (
	dirs      = flag.String("dir", "../testdata/corpus", "comma-separated corpus directories")
	match     = flag.String("match", "", "only files whose path below the directory contains this")
	dpi       = flag.Float64("dpi", 150, "resolution")
	runs      = flag.Int("runs", 5, "timed renders per page and engine (0: one untimed pass, for robustness)")
	multiRuns = flag.Int("multiruns", 3, "timed renders of the whole document on all cores")
	cores     = flag.Int("cores", runtime.NumCPU(), "cores for the multi-core comparison (0: none)")
	maxPages  = flag.Int("pages", 0, "pages per file at most (0: all)")
	enginesF  = flag.String("engines", strings.Join(engineOrder, ","), "engines to compare, if available")
	ref       = flag.String("ref", "mupdf", "engine every ratio is relative to")
	timeout   = flag.Duration("timeout", 2*time.Minute, "deadline per request to a worker")
	python    = flag.String("python", "python", "Python with pymupdf and pypdfium2")
	node      = flag.String("node", "node", "Node.js for pdf.js")
	mutool    = flag.String("mutool", "mutool", "MuPDF's command-line tool, for -cli")
	outDir    = flag.String("out", "../report-bench", "report directory")
	cliMode   = flag.Bool("cli", false, "compare command-line tools end to end instead of libraries")
	verbose   = flag.Bool("v", false, "show the workers' stderr")
	charts    = flag.Bool("charts", false, "only recompute the summary, its tables and charts from the report's CSV files")
)

// file is one corpus file.
type file struct {
	path, rel, category, password string
	key                           string // SHA-256 prefix of the content
}

func main() {
	log.SetFlags(0)
	flag.Parse()
	if *charts {
		if err := aggregate(*outDir); err != nil {
			log.Fatal(err)
		}
		return
	}
	if _, err := os.Stat("ceraworker"); err != nil {
		log.Fatal("run from the bench directory (cd bench; go run .)")
	}
	files, err := listFiles(strings.Split(*dirs, ","), *match)
	if err != nil {
		log.Fatal(err)
	}
	if len(files) == 0 {
		log.Fatal("no PDF files; fetch the corpus with: go run ./cmd/corpus fetch && go run ./cmd/corpus scenes")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatal(err)
	}
	// Built workers live next to the tool: some machines run nothing from
	// the user's cache directory.
	cache, err := filepath.Abs(".cache")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		log.Fatal(err)
	}
	m := newMeta(files)
	if *cliMode {
		runCLI(files, m, cache)
		return
	}

	var engines []*engine
	for _, name := range strings.Split(*enginesF, ",") {
		e, err := setup(strings.TrimSpace(name), cache)
		if err != nil {
			log.Printf("%-12s not available: %v", name, err)
			m.Unavailable = append(m.Unavailable, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		log.Printf("%-12s %s", e.name, e.version)
		m.Versions[e.name] = e.version
		engines = append(engines, e)
	}
	refName := *ref
	if !slices.ContainsFunc(engines, func(e *engine) bool { return e.name == refName }) {
		refName = "cera"
		log.Printf("reference %s not available; ratios are relative to cera", *ref)
	}
	m.Ref = refName

	var results []*fileResult
	for k, f := range files {
		t0 := time.Now()
		r := measureFile(f, engines)
		results = append(results, r)
		log.Printf("[%d/%d] %s: %d pages (%v)", k+1, len(files), f.rel, r.pages, time.Since(t0).Round(time.Second))
	}
	if err := writeReport(*outDir, m, engines, results); err != nil {
		log.Fatal(err)
	}
	log.Printf("report in %s", *outDir)
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
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			abs, _ := filepath.Abs(path)
			pw, _ := corpus.Password(path)
			files = append(files, &file{path: abs, rel: rel, category: cat, password: pw, key: hex.EncodeToString(sum[:8])})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}
