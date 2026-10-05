package cera

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/timzifer/cera/internal/corpus"
)

// sharedResourcesPDF has pages that draw the same font, image, form and
// shading, so rendering them concurrently loads the same objects from many
// goroutines at once.
func sharedResourcesPDF(pages int) []byte {
	var contents []string
	for i := range pages {
		contents = append(contents, fmt.Sprintf(
			"q 0.%d 0 0 rg BT /F1 %d Tf 10 70 Td (Page %d: shared resources) Tj ET Q "+
				"q 40 0 0 30 120 10 cm /Im0 Do Q q 0.5 0 0 0.5 %d 20 cm /Fm0 Do Q "+
				"q 10 10 60 40 re W n /Sh0 sh Q", i%10, 8+i%5, i, 5*i))
	}
	font := "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"
	img := streamObj("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8",
		[]byte{255, 0, 0, 0, 255, 0, 0, 0, 255, 255, 255, 0})
	form := streamObj("/Type /XObject /Subtype /Form /BBox [0 0 60 60] /Resources << /Font << /F1 100 0 R >> >>",
		[]byte("0 0 1 rg 0 0 60 60 re f BT /F1 9 Tf 2 2 Td (form) Tj ET"))
	shading := "<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [10 0 70 0] " +
		"/Function << /FunctionType 2 /Domain [0 1] /C0 [1 1 0] /C1 [0 0.5 1] /N 1 >> >>"
	return buildPDF(contents,
		"/Resources << /Font << /F1 100 0 R >> /XObject << /Im0 101 0 R /Fm0 102 0 R >> /Shading << /Sh0 103 0 R >> >>",
		font, img, form, shading)
}

// TestConcurrentPages renders every page of one document from its own
// goroutine and requires the pixels a sequential render gives.
func TestConcurrentPages(t *testing.T) {
	const pages = 12
	data := sharedResourcesPDF(pages)
	render := func(doc *Document, i int) []byte {
		p, err := doc.Page(i)
		if err != nil {
			t.Error(err)
			return nil
		}
		dst := image.NewRGBA(p.Bounds(1.5))
		if err := p.Render(context.Background(), dst, RenderOptions{Scale: 1.5}); err != nil {
			t.Error(err)
		}
		return dst.Pix
	}
	seq, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	want := make([][]byte, pages)
	for i := range pages {
		want[i] = render(seq, i)
		if !bytes.ContainsFunc(want[i], func(r rune) bool { return r != 0xFF }) {
			t.Fatalf("page %d is blank", i)
		}
	}

	par, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	got := make([][]byte, pages)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range pages {
		wg.Go(func() {
			<-start
			got[i] = render(par, i)
		})
	}
	close(start)
	wg.Wait()
	for i := range pages {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("page %d differs when pages are rendered concurrently", i)
		}
	}
}

// TestOpenReaderAt renders pages of a document read through an io.ReaderAt,
// concurrently, as they render from memory.
func TestOpenReaderAt(t *testing.T) {
	data := sharedResourcesPDF(4)
	mem, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	ra, err := OpenReaderAt(bytes.NewReader(data), int64(len(data)), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	render := func(doc *Document, i int) []byte {
		p, err := doc.Page(i)
		if err != nil {
			t.Fatal(err)
		}
		dst := image.NewRGBA(p.Bounds(1))
		if err := p.Render(context.Background(), dst, RenderOptions{Scale: 1}); err != nil {
			t.Error(err)
		}
		return dst.Pix
	}
	got := make([][]byte, 4)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() { got[i] = render(ra, i) })
	}
	wg.Wait()
	for i := range 4 {
		if !bytes.Equal(got[i], render(mem, i)) {
			t.Errorf("page %d differs read through an io.ReaderAt", i)
		}
	}
}

// TestConcurrentCorpusPages is TestConcurrentPages over every PDF below
// $CERA_CORPUS (for example testdata/corpus), at most 8 pages a file: real
// files bring embedded, composite and Type 3 fonts, images and patterns.
// Run it under the race detector.
func TestConcurrentCorpusPages(t *testing.T) {
	dir := os.Getenv("CERA_CORPUS")
	if dir == "" {
		t.Skip("CERA_CORPUS not set")
	}
	var files []string
	_ = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && strings.EqualFold(filepath.Ext(p), ".pdf") {
			files = append(files, p)
		}
		return nil
	})
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		pw, _ := corpus.Password(f)
		render := func(doc *Document, i int) []byte {
			p, err := doc.Page(i)
			if err != nil {
				return nil
			}
			dst := image.NewRGBA(p.Bounds(0.5))
			_ = p.Render(context.Background(), dst, RenderOptions{Scale: 0.5})
			return dst.Pix
		}
		seq, err := OpenWithPassword(data, pw)
		if err != nil {
			continue
		}
		n := min(seq.NumPages(), 8)
		want := make([][]byte, n)
		for i := range n {
			want[i] = render(seq, i)
		}
		par, _ := OpenWithPassword(data, pw)
		got := make([][]byte, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() { got[i] = render(par, i) })
		}
		wg.Wait()
		for i := range n {
			if !bytes.Equal(got[i], want[i]) {
				t.Errorf("%s page %d differs when pages are rendered concurrently", f, i)
			}
		}
	}
}
