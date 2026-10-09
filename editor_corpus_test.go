package cera

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEditCorpus edits every file of the pinned corpus, when it has been
// fetched (go run ./cmd/corpus fetch): saved as it is, its pages render as
// before, annotations and form fields included, and the form keeps its
// fields; with its first page deleted, by a new file or an incremental
// update, the others still do.
func TestEditCorpus(t *testing.T) {
	dir := filepath.Join("testdata", "corpus")
	if testing.Short() {
		t.Skip("short")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no corpus")
	}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.EqualFold(filepath.Ext(path), ".pdf") {
			return err
		}
		t.Run(filepath.ToSlash(path[len(dir)+1:]), func(t *testing.T) { editCorpusFile(t, path) })
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func editCorpusFile(t *testing.T, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := Open(data)
	if err != nil {
		t.Skip(err)
	}
	n := sd.NumPages()
	if n == 0 {
		t.Skip("no pages")
	}
	save := func(e *Editor) *Document {
		var out bytes.Buffer
		if err := e.Save(&out); err != nil {
			if errors.Is(err, ErrNoAssembly) {
				t.Skip(err)
			}
			t.Fatal(err)
		}
		editStrict(t, out.Bytes())
		return editOpen(t, out.Bytes())
	}
	same := func(od *Document, i, j int) {
		want, werr := editCorpusRender(sd, i)
		got, gerr := editCorpusRender(od, j)
		if werr != nil || gerr != nil {
			if (werr == nil) != (gerr == nil) {
				t.Errorf("page %d: source %v, output %v", i, werr, gerr)
			}
			return
		}
		if want.Bounds() != got.Bounds() || !bytes.Equal(want.Pix, got.Pix) {
			t.Errorf("page %d renders differently", i)
		}
	}

	od := save(sd.Edit())
	if od.NumPages() != n {
		t.Fatalf("%d pages, want %d", od.NumPages(), n)
	}
	if sf, of := sd.Form(), od.Form(); (sf == nil) != (of == nil) || sf != nil && len(sf.Fields) != len(of.Fields) {
		t.Errorf("form %v, source %v", of, sf)
	}
	for i := range n {
		if i < 3 || i == n-1 {
			same(od, i, i)
		}
	}

	if n < 2 {
		return
	}
	e := sd.Edit()
	if err := e.DeletePages(0); err != nil {
		t.Fatal(err)
	}
	od = save(e)
	if od.NumPages() != n-1 {
		t.Fatalf("%d pages after a delete, want %d", od.NumPages(), n-1)
	}
	for i := 1; i < min(n, 3); i++ {
		same(od, i, i-1)
	}

	// The same delete as an incremental update.
	var upd bytes.Buffer
	if err := e.Update(&upd); err != nil {
		if errors.Is(err, ErrNoUpdate) {
			return
		}
		t.Fatal(err)
	}
	if !bytes.HasPrefix(upd.Bytes(), data) {
		t.Fatal("the update does not start with the file")
	}
	ud := editOpen(t, upd.Bytes())
	if ud.r.Repaired() || ud.NumPages() != n-1 {
		t.Fatalf("update: repaired %v, %d pages", ud.r.Repaired(), ud.NumPages())
	}
	for i := 1; i < min(n, 3); i++ {
		same(ud, i, i-1)
	}
}

func editCorpusRender(d *Document, i int) (*image.RGBA, error) {
	p, err := d.Page(i)
	if err != nil {
		return nil, err
	}
	dst := image.NewRGBA(p.Bounds(0.5))
	err = p.Render(context.Background(), dst, RenderOptions{
		Scale: 0.5, Background: color.RGBA{255, 255, 255, 255}, Workers: 1,
		Deadline: time.Now().Add(20 * time.Second),
	})
	return dst, err
}
