package cera

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
)

// TestFlatten flattens the form: the page then looks as the form showed
// it, with the values written, and has no form.
func TestFlatten(t *testing.T) {
	values := map[string]Value{"name": TextValue("flat"), "agree": StateValue("Yes"), "langs": ChoiceValue(1)}
	for _, stm := range []bool{false, true} {
		src := fieldsFile(stm)
		for _, mode := range []string{"save", "update"} {
			for _, filled := range []bool{false, true} {
				t.Run(fmt.Sprintf("objstm=%v/%s/values=%v", stm, mode, filled), func(t *testing.T) {
					doc := editOpen(t, src)
					// What the form shows: written, not flattened.
					shown := doc.Edit()
					flat := doc.Edit()
					if filled {
						for _, e := range []*Editor{shown, flat} {
							if err := e.SetFields(fill(t, doc, values)); err != nil {
								t.Fatal(err)
							}
						}
					}
					if err := flat.Flatten(); err != nil {
						t.Fatal(err)
					}
					write := func(e *Editor) []byte {
						if mode == "save" {
							return editSave(t, e)
						}
						return editUpdate(t, e, src)
					}
					want, got := write(shown), write(flat)
					fd := editOpen(t, got)
					if fd.Form() != nil {
						t.Fatal("the form stays")
					}
					d, _ := pdf.Open(got)
					page, _ := d.Page(1)
					annots, _ := d.Resolve(page.Get("Annots")).Array()
					if len(annots) != 1 {
						t.Errorf("%d annotations, want the square alone", len(annots))
					}
					if !bytes.Equal(renderState(t, editOpen(t, want), nil).Pix, renderState(t, fd, nil).Pix) {
						t.Error("the flattened page renders unlike the form")
					}
				})
			}
		}
	}
}

// TestFlattenLayer keeps a flattened widget in its optional content group:
// shown when the layer is.
func TestFlattenLayer(t *testing.T) {
	src := fieldsFile(false)
	doc := editOpen(t, src)
	e := doc.Edit()
	if err := e.Flatten(); err != nil {
		t.Fatal(err)
	}
	out := editOpen(t, editSave(t, e))
	cfg := out.Layers()
	if cfg == nil || len(cfg.Layers) != 1 {
		t.Fatalf("layers %+v", cfg)
	}
	hidden := renderState(t, out, nil)
	v := cfg.Visibility().With(cfg.Layers[0], true)
	p, _ := out.Page(0)
	shown := renderWithLayers(t, p, &v)
	if bytes.Equal(hidden.Pix, shown.Pix) {
		t.Error("the widget in the layer does not show with it")
	}
}

func TestFlattenSigned(t *testing.T) {
	if err := editOpen(t, signedFile(true, 0, false)).Edit().Flatten(); !errors.Is(err, ErrSigned) {
		t.Errorf("err %v", err)
	}
	if err := NewEditor().Flatten(); err == nil {
		t.Error("a new document flattened")
	}
}

func renderWithLayers(t *testing.T, p *Page, v *Visibility) *image.RGBA {
	t.Helper()
	dst := image.NewRGBA(p.Bounds(2))
	err := p.Render(context.Background(), dst, RenderOptions{
		Scale: 2, Background: color.RGBA{255, 255, 255, 255}, Workers: 1, Layers: v,
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}
