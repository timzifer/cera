// Command readerdiff compares cera's own PDF reader (internal/pdf) with
// go-pdfkit/reader v0.6.0, the reader it replaces (ADR 0012).
//
//	go run . snapshot -backend v06 file.pdf
//	go run . objects -a v06 -b pdf -dir ../testdata/corpus
//	go run . fonts -dir ../testdata/corpus
//
// objects reads every PDF below -dir with both backends and compares their
// snapshots entry by entry: open result, version, repair, trailer, page tree
// with inherited attributes, every object reachable from the trailer and the
// pages, and what decoding made of every stream. A difference fails the run
// unless allowlist.txt names it with a reason.
//
// fonts compares go-pdfkit/pdffont v0.3.1 with internal/pdffont on every
// font the pages use: kind, matrix, program, and per code its text, width,
// glyph name and glyph number.
//
// It is a module of its own so go-pdfkit never enters cera's go.mod.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/timzifer/cera/internal/corpus"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "snapshot":
		fl := flag.NewFlagSet("snapshot", flag.ExitOnError)
		be := fl.String("backend", "v06", "backend")
		fl.Parse(os.Args[2:])
		err = runSnapshot(*be, fl.Args())
	case "objects":
		fl := flag.NewFlagSet("objects", flag.ExitOnError)
		a := fl.String("a", "v06", "reference backend")
		b := fl.String("b", "pdf", "backend under test")
		dir := fl.String("dir", "../testdata/corpus", "comma-separated directories of PDFs")
		allow := fl.String("allow", "allowlist.txt", "allowed differences")
		max := fl.Int("max", 5, "differences printed per file")
		fl.Parse(os.Args[2:])
		err = runObjects(*a, *b, *dir, *allow, *max)
	case "fonts":
		fl := flag.NewFlagSet("fonts", flag.ExitOnError)
		dir := fl.String("dir", "../testdata/corpus", "comma-separated directories of PDFs")
		allow := fl.String("allow", "allowlist.txt", "allowed differences")
		max := fl.Int("max", 5, "differences printed per file")
		fl.Parse(os.Args[2:])
		err = runFonts(*dir, *allow, *max)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "readerdiff:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: readerdiff snapshot|objects|fonts [flags]")
	os.Exit(2)
}

func backendNamed(n string) (backend, error) {
	b, ok := backends[n]
	if !ok {
		var names []string
		for k := range backends {
			names = append(names, k)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("no backend %q (have %s)", n, strings.Join(names, ", "))
	}
	return b, nil
}

func runSnapshot(be string, files []string) error {
	b, err := backendNamed(be)
	if err != nil {
		return err
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		pw, _ := corpus.Password(f)
		s := b.snapshot(data, pw)
		for _, k := range s.keys {
			fmt.Printf("%s\t%s\n", k, s.entries[k])
		}
	}
	return nil
}

func pdfFiles(dirs string) ([]string, error) {
	var files []string
	for _, dir := range strings.Split(dirs, ",") {
		err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !e.IsDir() && strings.EqualFold(filepath.Ext(p), ".pdf") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(files)
	return files, nil
}

func runObjects(an, bn, dirs, allowPath string, max int) error {
	a, err := backendNamed(an)
	if err != nil {
		return err
	}
	b, err := backendNamed(bn)
	if err != nil {
		return err
	}
	allow, err := readAllowlist(allowPath)
	if err != nil {
		return err
	}
	files, err := pdfFiles(dirs)
	if err != nil {
		return err
	}
	var failed, allowed, entries int
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		pw, _ := corpus.Password(f)
		sa, sb := safeSnapshot(a, data, pw), safeSnapshot(b, data, pw)
		diffs := compare(sa, sb)
		entries += len(sa.keys)
		var bad []diff
		for _, d := range diffs {
			if r := allow.reason(f, d.key); r != "" {
				allowed++
				continue
			}
			bad = append(bad, d)
		}
		if len(bad) == 0 {
			continue
		}
		failed++
		fmt.Printf("%s: %d differences\n", filepath.ToSlash(f), len(bad))
		for i, d := range bad {
			if i == max {
				fmt.Printf("  …\n")
				break
			}
			fmt.Printf("  %s\n    %s: %s\n    %s: %s\n", d.key, an, clip(d.a), bn, clip(d.b))
		}
	}
	fmt.Printf("%d files, %d entries, %d files differ, %d differences allowed\n",
		len(files), entries, failed, allowed)
	if failed > 0 {
		return fmt.Errorf("%d files differ", failed)
	}
	return nil
}

// safeSnapshot is b.snapshot that records a panic as an entry instead of
// stopping the run.
func safeSnapshot(b backend, data []byte, pw string) (s *snapshot) {
	defer func() {
		if r := recover(); r != nil {
			s = &snapshot{}
			s.set("panic", fmt.Sprint(r))
		}
	}()
	return b.snapshot(data, pw)
}

type diff struct{ key, a, b string }

// compare lists the entries two snapshots disagree on, in a's order and
// then the entries only b has.
func compare(a, b *snapshot) []diff {
	var ds []diff
	for _, k := range a.keys {
		vb, ok := b.entries[k]
		if !ok {
			vb = "(missing)"
		}
		if va := a.entries[k]; va != vb {
			ds = append(ds, diff{k, va, vb})
		}
	}
	for _, k := range b.keys {
		if _, ok := a.entries[k]; !ok {
			ds = append(ds, diff{k, "(missing)", b.entries[k]})
		}
	}
	return ds
}

func clip(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
