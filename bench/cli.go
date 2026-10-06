package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/timzifer/cera/bench/internal/clock"
)

// tool is a renderer's command-line program, timed end to end: start,
// open, draw and write every page as PNG, as someone converting a PDF
// would run it.
type tool struct {
	name, label, bin, version string
	// args builds the command line drawing f into dir; multi uses all
	// cores where the tool can.
	args func(f *file, dir string, multi bool) []string
	env  func(multi bool) []string
	// threads says whether the tool has threads of its own.
	threads bool
}

var toolOrder = []string{"mupdf", "cera", "poppler", "ghostscript"}

func setupTools(cache string) ([]*tool, []string) {
	var tools []*tool
	var missing []string
	d := strconv.FormatFloat(*dpi, 'g', -1, 64)
	for _, name := range toolOrder {
		t := &tool{name: name}
		var err error
		switch name {
		case "cera":
			exe := ""
			if runtime.GOOS == "windows" {
				exe = ".exe"
			}
			t.bin = filepath.Join(cache, "cera"+exe)
			cmd := exec.Command("go", "build", "-trimpath", "-o", t.bin, "github.com/timzifer/cera/cmd/cera")
			if out, e := cmd.CombinedOutput(); e != nil {
				err = fmt.Errorf("go build: %v: %s", e, firstLine(out))
			}
			t.label, t.version, t.threads = "cera", "cera "+ceraVersion(), true
			t.args = func(f *file, dir string, multi bool) []string {
				a := []string{"-dpi", d, "-o", filepath.Join(dir, "p-%d.png")}
				if !multi {
					a = append(a, "-workers", "1")
				}
				if f.password != "" {
					a = append(a, "-password", f.password)
				}
				return append(a, f.path)
			}
			t.env = func(multi bool) []string {
				if multi {
					return nil
				}
				return []string{"GOMAXPROCS=1"}
			}
		case "mupdf":
			t.bin, err = exec.LookPath(*mutool)
			t.label, t.version, t.threads = "MuPDF (mutool draw)", toolVersion(t.bin, "-v"), true
			t.args = func(f *file, dir string, multi bool) []string {
				a := []string{"draw", "-q", "-r", d, "-o", filepath.Join(dir, "p-%d.png")}
				if multi {
					a = append(a, "-T", strconv.Itoa(*cores), "-B", "256")
				}
				if f.password != "" {
					a = append(a, "-p", f.password)
				}
				return append(a, f.path)
			}
		case "poppler":
			t.bin, err = exec.LookPath("pdftoppm")
			t.label, t.version = "Poppler (pdftoppm)", toolVersion(t.bin, "-v")
			t.args = func(f *file, dir string, _ bool) []string {
				a := []string{"-r", d, "-png"}
				if f.password != "" {
					a = append(a, "-opw", f.password, "-upw", f.password)
				}
				return append(a, f.path, filepath.Join(dir, "p"))
			}
		case "ghostscript":
			t.bin, err = exec.LookPath("gs")
			if err != nil {
				t.bin, err = exec.LookPath("gswin64c")
			}
			t.label, t.version, t.threads = "Ghostscript", "ghostscript "+toolVersion(t.bin, "--version"), true
			t.args = func(f *file, dir string, multi bool) []string {
				a := []string{"-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-sDEVICE=png16m", "-r" + d,
					"-dTextAlphaBits=4", "-dGraphicsAlphaBits=4", "-o", filepath.Join(dir, "p-%d.png")}
				if multi {
					a = append(a, fmt.Sprintf("-dNumRenderingThreads=%d", *cores))
				}
				if f.password != "" {
					a = append(a, "-sPDFPassword="+f.password)
				}
				return append(a, f.path)
			}
		}
		if t.env == nil {
			t.env = func(bool) []string { return nil }
		}
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s: %v", name, err))
			log.Printf("%-12s not available: %v", name, err)
			continue
		}
		log.Printf("%-12s %s", name, t.version)
		tools = append(tools, t)
	}
	return tools, missing
}

func toolVersion(bin, flag string) string {
	if bin == "" {
		return ""
	}
	out, _ := exec.Command(bin, flag).CombinedOutput()
	return firstLine(out)
}

// run draws f once; it returns the wall clock.
func (t *tool) run(f *file, multi bool) (int64, error) {
	dir, err := os.MkdirTemp("", "cera-bench-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.bin, t.args(f, dir, multi)...)
	cmd.Env = append(os.Environ(), t.env(multi)...)
	t0 := clock.Now()
	out, err := cmd.CombinedOutput()
	ns := clock.Since(t0)
	if err != nil {
		return 0, fmt.Errorf("%v: %s", err, firstLine(out))
	}
	if pngs, _ := filepath.Glob(filepath.Join(dir, "*.png")); len(pngs) == 0 {
		return 0, fmt.Errorf("no page written")
	}
	return int64(ns), nil
}

// runCLI compares the command-line tools file by file, taking turns.
func runCLI(files []*file, m *meta, cache string) {
	tools, missing := setupTools(cache)
	m.Unavailable = missing
	refName := *ref
	if !slices.ContainsFunc(tools, func(t *tool) bool { return t.name == refName }) {
		refName = "poppler"
		log.Printf("reference %s not available; ratios are relative to %s", *ref, refName)
	}
	m.Ref = refName
	for _, t := range tools {
		m.Versions[t.name] = t.version
	}

	type key struct {
		tool  string
		multi bool
	}
	type res struct {
		times map[key][]int64
		err   map[key]string
	}
	modes := []bool{false}
	if *cores > 1 {
		modes = append(modes, true)
	}
	n := max(*runs, 1)
	logs := map[key]map[string][]float64{} // per tool and mode, per category
	failed := map[key]int{}
	rows := [][]string{{"file", "category", "tool", "cores", "ratio", "spread", "error"}}
	for fi, f := range files {
		r := res{times: map[key][]int64{}, err: map[key]string{}}
		step := 0
		for range n + 1 { // the first round warms the disk cache and is not counted
			for _, multi := range modes {
				step++
				for _, t := range rotate(tools, step) {
					k := key{t.name, multi}
					if _, bad := r.err[k]; bad {
						continue
					}
					ns, err := t.run(f, multi)
					if err != nil {
						r.err[k] = err.Error()
						continue
					}
					r.times[k] = append(r.times[k], ns)
				}
			}
		}
		for _, multi := range modes {
			refT := r.times[key{refName, multi}]
			for _, t := range tools {
				k := key{t.name, multi}
				tt := r.times[k]
				ratio, spread := math.NaN(), math.NaN()
				if len(tt) == n+1 && len(refT) == n+1 {
					paired := make([]float64, n)
					for i := range n {
						paired[i] = float64(tt[i+1]) / float64(refT[i+1])
					}
					ratio = median(paired)
					spread = iqr(paired) / ratio
					if logs[k] == nil {
						logs[k] = map[string][]float64{}
					}
					for _, c := range []string{f.category, "all"} {
						logs[k][c] = append(logs[k][c], math.Log(ratio))
					}
				} else if len(refT) == n+1 {
					failed[k]++
				}
				c := "1"
				if multi {
					c = strconv.Itoa(*cores)
				}
				rows = append(rows, []string{f.rel, f.category, t.name, c, fmtF(ratio, 4), fmtF(spread, 4), oneLine(r.err[k])})
			}
		}
		log.Printf("[%d/%d] %s", fi+1, len(files), f.rel)
	}

	out := map[string]map[string]map[string]group{"1": {}, "all": {}}
	for k, cats := range logs {
		mode := "1"
		if k.multi {
			mode = "all"
		}
		for c, l := range cats {
			if out[mode][c] == nil {
				out[mode][c] = map[string]group{}
			}
			g := group{Ratio: math.Exp(mean(l)), N: len(l)}
			if c == "all" {
				g.Failed = failed[k]
			}
			out[mode][c][k.tool] = g
		}
	}
	threads := map[string]bool{}
	for _, t := range tools {
		threads[t.name] = t.threads
	}
	sum := struct {
		Meta    *meta                                  `json:"meta"`
		Ratios  map[string]map[string]map[string]group `json:"ratios"` // cores ("1", "all") → category → tool
		Threads map[string]bool                        `json:"threads"`
	}{m, out, threads}
	b, _ := json.MarshalIndent(sum, "", " ")
	must(os.WriteFile(filepath.Join(*outDir, "cli-summary.json"), b, 0o644))
	fh, err := os.Create(filepath.Join(*outDir, "cli-files.csv"))
	must(err)
	w := csv.NewWriter(fh)
	w.WriteAll(rows)
	fh.Close()

	var md strings.Builder
	fmt.Fprintf(&md, "# Command line, PDF to PNG\n\nWall clock of converting each file to PNGs at %v dpi, from start to exit, relative to %s; "+
		"geometric mean per file of the median of %d paired runs.\n\n", *dpi, labelOf(tools, refName), n)
	for _, mode := range []string{"1", "all"} {
		cats := present(categoryOrder, out[mode])
		if len(cats) == 0 {
			continue
		}
		if mode == "1" {
			md.WriteString("## One core\n\n| |")
		} else {
			fmt.Fprintf(&md, "## All %d cores (where the tool has threads)\n\n| |", *cores)
		}
		for _, c := range cats {
			fmt.Fprintf(&md, " %s |", categoryLabels[c])
		}
		md.WriteString("\n|---|" + strings.Repeat("---|", len(cats)) + "\n")
		for _, t := range tools {
			fmt.Fprintf(&md, "| %s |", t.label)
			for _, c := range cats {
				if g, ok := out[mode][c][t.name]; ok {
					fmt.Fprintf(&md, " %s |", ratioCell(g))
				} else {
					md.WriteString(" — |")
				}
			}
			md.WriteString("\n")
		}
		md.WriteString("\n")
	}
	fmt.Fprintf(&md, "%s, %d cores, %s; corpus %s (%d files); %s.\n\n", m.CPU, m.Cores, m.OS, m.Corpus, m.Files, m.Generated)
	for _, t := range tools {
		fmt.Fprintf(&md, "- %s: %s\n", t.label, t.version)
	}
	for _, u := range missing {
		fmt.Fprintf(&md, "- not available: %s\n", u)
	}
	must(os.WriteFile(filepath.Join(*outDir, "cli-summary.md"), []byte(md.String()), 0o644))
	log.Printf("report in %s", *outDir)
}

func labelOf(tools []*tool, name string) string {
	for _, t := range tools {
		if t.name == name {
			return t.label
		}
	}
	return name
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
