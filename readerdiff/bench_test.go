package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-pdfkit/reader"
	"github.com/timzifer/cera/internal/corpus"
	"github.com/timzifer/cera/internal/pdf"
)

// The benchmarks run every reader over the same files, so benchstat can
// compare them: go test -bench . -benchmem -count 5 | benchstat -col /reader
//
// READERDIFF_CORPUS names the directories (comma-separated) to read; the
// default is the pinned corpus of the cera checkout.

// A benchReader is one reader under benchmark.
type benchReader struct {
	name string
	// open parses a file and returns a handle for walk.
	open func(data []byte, password string) (any, error)
	// walk resolves every object reachable from the trailer and the pages and
	// returns how many it resolved; with decode it also decodes every stream.
	walk func(doc any, decode bool) int
}

var benchReaders = []benchReader{{
	name: "v06",
	open: func(data []byte, pw string) (any, error) { return reader.OpenWithPassword(data, pw) },
	walk: walkV06,
}, {
	name: "pdf",
	open: func(data []byte, pw string) (any, error) { return pdf.OpenWithPassword(data, pw) },
	walk: walkPDF,
}}

func walkPDF(doc any, decode bool) int {
	d := doc.(*pdf.Document)
	seen := map[pdf.Ref]bool{}
	var queue []pdf.Ref
	var visit func(o pdf.Object)
	visit = func(o pdf.Object) {
		switch o.Kind() {
		case pdf.KindRef:
			r, _ := o.Ref()
			if !seen[r] {
				seen[r] = true
				queue = append(queue, r)
			}
		case pdf.KindArray:
			a, _ := o.Array()
			for _, e := range a {
				visit(e)
			}
		case pdf.KindDict:
			dict, _ := o.Dict()
			for _, e := range dict.Entries() {
				visit(e.Val)
			}
		case pdf.KindStream:
			s, _ := o.Stream()
			for _, e := range s.Dict.Entries() {
				visit(e.Val)
			}
			if decode {
				d.Decode(s)
			}
		}
	}
	visit(d.Trailer().Object())
	for i := range d.PageCount() {
		if r, ok := d.PageRef(i + 1); ok {
			visit(r.Object())
		}
	}
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		if o, err := d.Get(r); err == nil {
			visit(o)
		}
	}
	return len(seen)
}

func walkV06(doc any, decode bool) int {
	d := doc.(*reader.Document)
	seen := map[reader.Ref]bool{}
	var queue []reader.Ref
	var visit func(o reader.Object)
	visit = func(o reader.Object) {
		switch o := o.(type) {
		case reader.Ref:
			if !seen[o] {
				seen[o] = true
				queue = append(queue, o)
			}
		case reader.Array:
			for _, e := range o {
				visit(e)
			}
		case reader.Dict:
			for _, e := range o {
				visit(e)
			}
		case *reader.Stream:
			visit(o.Dict)
			if decode {
				d.DecodeStreamRecovering(o)
			}
		}
	}
	visit(d.Trailer())
	for i := range d.PageCount() {
		if r, ok := d.PageRef(i + 1); ok {
			visit(r)
		}
	}
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		if o, err := d.Get(r); err == nil {
			visit(o)
		}
	}
	return len(seen)
}

type benchFile struct {
	name, password string
	data           []byte
}

func benchFiles(b *testing.B) []benchFile {
	dirs := os.Getenv("READERDIFF_CORPUS")
	if dirs == "" {
		dirs = "../testdata/corpus"
	}
	paths, err := pdfFiles(dirs)
	if err != nil || len(paths) == 0 {
		b.Skipf("no PDFs in %s (set READERDIFF_CORPUS)", dirs)
	}
	var fs []benchFile
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			b.Fatal(err)
		}
		pw, _ := corpus.Password(p)
		fs = append(fs, benchFile{filepath.Base(p), pw, data})
	}
	return fs
}

// BenchmarkOpen parses the cross-reference data and the catalogue.
func BenchmarkOpen(b *testing.B) {
	fs := benchFiles(b)
	for _, r := range benchReaders {
		b.Run("reader="+r.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for _, f := range fs {
					r.open(f.data, f.password)
				}
			}
		})
	}
}

// BenchmarkGetAll opens every file and resolves every reachable object.
func BenchmarkGetAll(b *testing.B) { benchWalk(b, false) }

// BenchmarkDecodeAll is BenchmarkGetAll that also decodes every stream.
func BenchmarkDecodeAll(b *testing.B) { benchWalk(b, true) }

func benchWalk(b *testing.B, decode bool) {
	fs := benchFiles(b)
	for _, r := range benchReaders {
		b.Run("reader="+r.name, func(b *testing.B) {
			b.ReportAllocs()
			var objs int
			for b.Loop() {
				objs = 0
				for _, f := range fs {
					d, err := r.open(f.data, f.password)
					if err == nil {
						objs += r.walk(d, decode)
					}
				}
			}
			b.ReportMetric(float64(objs), "objects")
		})
	}
}
