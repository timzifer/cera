package pdfedit

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

	"github.com/timzifer/cera"
)

// TestCorpus extracts the last and the first page of every file of the
// pinned corpus, when it has been fetched (go run ./cmd/corpus fetch), and
// checks that they render as in the source. Annotations are left out of
// the comparison: widgets are no longer fields.
func TestCorpus(t *testing.T) {
	dir := filepath.Join("..", "testdata", "corpus")
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
		t.Run(filepath.ToSlash(path[len(dir)+1:]), func(t *testing.T) { corpusFile(t, path) })
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func corpusFile(t *testing.T, path string) {
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := cera.Open(src)
	if err != nil {
		t.Skip(err)
	}
	n := sd.NumPages()
	if n == 0 {
		t.Skip("no pages")
	}
	sel := pages(n-1, 0)
	var out bytes.Buffer
	if err := Extract(&out, src, sel); err != nil {
		if errors.Is(err, ErrEncrypted) {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	strict(t, out.Bytes())
	od := open(t, out.Bytes())
	for i, p := range sel {
		want, werr := renderPlain(sd, p.Index, cera.AnnotsNone)
		got, gerr := renderPlain(od, i, cera.AnnotsNone)
		if werr != nil || gerr != nil {
			if (werr == nil) != (gerr == nil) {
				t.Errorf("page %d: source %v, output %v", p.Index, werr, gerr)
			}
			continue
		}
		if want.Bounds() != got.Bounds() || !bytes.Equal(want.Pix, got.Pix) {
			t.Errorf("page %d renders differently", p.Index)
		}
	}
}

func renderPlain(d *cera.Document, i int, annots cera.AnnotMode) (*image.RGBA, error) {
	p, err := d.Page(i)
	if err != nil {
		return nil, err
	}
	const scale = 0.5
	dst := image.NewRGBA(p.Bounds(scale))
	err = p.Render(context.Background(), dst, cera.RenderOptions{
		Scale: scale, Background: color.RGBA{255, 255, 255, 255}, Workers: 1,
		Annotations: annots, Deadline: time.Now().Add(20 * time.Second),
	})
	return dst, err
}
