package cera

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
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

// stackFile is a page whose annotations lie on one another, from the
// bottom up: a highlight without an appearance, a translucent square with
// one, an opaque black square over a part of the highlight, its popup, a
// text field on the squares but not on the highlight, a line across the
// field (with line), and apart from them a circle and a link. The field
// has no appearance and a border that does not fall on pixels.
func stackFile(line bool) []byte {
	annots := "50 0 R 51 0 R 52 0 R 53 0 R 54 0 R 57 0 R 55 0 R 56 0 R"
	if !line {
		annots = strings.Replace(annots, " 57 0 R", "", 1)
	}
	return testpdf.File{
		Objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R /AcroForm <</Fields [54 0 R] /DA (/Helv 10 Tf 0 g) /DR <</Font <</Helv 20 0 R>>>>>>>>",
			2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200]>>",
			3: "<</Type /Page /Parent 2 0 R /Contents 43 0 R /Resources <</XObject <</CeraFlat0 30 0 R>>>>" +
				" /Annots [" + annots + "]>>",
			20: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
			30: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 40 30]", []byte("1 0 0 rg 0 0 30 20 re f 0 1 0 rg 10 10 30 20 re f")),
			43: testpdf.Stream("", []byte("0.9 g 0 0 300 200 re f q 1 0 0 1 250 10 cm /CeraFlat0 Do Q 1 0 0 1 5 5 cm")),
			50: "<</Type /Annot /Subtype /Highlight /Rect [10 100 100 120] /QuadPoints [10 120 100 120 10 100 100 100] /C [1 1 0] /CA 0.5>>",
			51: "<</Type /Annot /Subtype /Square /Rect [150.5 60.5 230.5 120.5] /CA 0.5 /AP <</N 30 0 R>>>>",
			52: "<</Type /Annot /Subtype /Square /Rect [20 20 180 110] /C [0 0 0] /IC [0 0 0] /Popup 53 0 R>>",
			53: "<</Type /Annot /Subtype /Popup /Parent 52 0 R /Rect [180 110 280 190]>>",
			54: "<</Type /Annot /Subtype /Widget /FT /Tx /T (name) /V (over the box) /Rect [30.4 60.3 170.7 80.6] /MK <</BG [1 1 1] /BC [1 0 0]>>>>",
			55: "<</Type /Annot /Subtype /Circle /Rect [240 150 280 190] /C [0 0 1]>>",
			57: "<</Type /Annot /Subtype /Line /Rect [100 50 160 90] /L [105 55 155 85] /C [0 0.5 0] /BS <</W 2>>>>",
			56: "<</Type /Annot /Subtype /Link /Rect [240 100 280 120] /A <</S /URI /URI (https://example.com)>>>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

// subtypes returns the subtypes of the annotations of the first page.
func subtypes(t *testing.T, d *Document) string {
	t.Helper()
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, a := range p.Annotations() {
		s = append(s, a.Subtype)
	}
	return strings.Join(s, " ")
}

// TestFlattenStacking keeps what the page shows: an annotation under a
// flattened widget is flattened with it, and so is one under that, while
// annotations that touch none of them stay, and so does one on a widget
// that stays.
func TestFlattenStacking(t *testing.T) {
	src := stackFile(true)
	doc := editOpen(t, src)
	want := renderState(t, doc, nil)
	for _, tt := range []struct {
		name         string
		form, annots bool
		left         string
		hasForm      bool
	}{
		{"form", true, false, "Line Circle Link", false},
		{"annotations", false, true, "Widget Line Link", true},
		{"both", true, true, "Link", false},
	} {
		for _, mode := range []string{"save", "update"} {
			t.Run(tt.name+"/"+mode, func(t *testing.T) {
				e := doc.Edit()
				if tt.form {
					if err := e.Flatten(); err != nil {
						t.Fatal(err)
					}
				}
				if tt.annots {
					if err := e.FlattenAnnotations(); err != nil {
						t.Fatal(err)
					}
				}
				var got []byte
				if mode == "save" {
					got = editSave(t, e)
				} else {
					got = editUpdate(t, e, src)
				}
				out := editOpen(t, got)
				if s := subtypes(t, out); s != tt.left {
					t.Errorf("annotations left: %s, want %s", s, tt.left)
				}
				if (out.Form() != nil) != tt.hasForm {
					t.Errorf("form %v, want %v", out.Form() != nil, tt.hasForm)
				}
				if n, max := pixDiff(want, renderState(t, out, nil)); n > 0 {
					t.Errorf("the flattened page renders unlike the page: %d pixels, by up to %d", n, max)
				}
			})
		}
	}
}

// TestFlattenTwice flattens what an earlier flatten left: the names it
// gives its resources do not take those of the page.
func TestFlattenTwice(t *testing.T) {
	doc := editOpen(t, stackFile(true))
	want := renderState(t, doc, nil)
	e := doc.Edit()
	if err := e.FlattenAnnotations(); err != nil {
		t.Fatal(err)
	}
	once := editOpen(t, editSave(t, e))
	e = once.Edit()
	if err := e.Flatten(); err != nil {
		t.Fatal(err)
	}
	twice := editOpen(t, editSave(t, e))
	if s := subtypes(t, twice); s != "Line Link" {
		t.Errorf("annotations left: %s", s)
	}
	if n, max := pixDiff(want, renderState(t, twice, nil)); n > 0 {
		t.Errorf("flattened twice the page renders unlike the page: %d pixels, by up to %d", n, max)
	}
}

// TestFlattenValue draws the value SetFields wrote, at a place where the
// border of the field does not fall on pixels. Nothing lies on the field:
// the renderer draws a value that is not the file's above the annotations
// of the page.
func TestFlattenValue(t *testing.T) {
	doc := editOpen(t, stackFile(false))
	state := fill(t, doc, map[string]Value{"name": TextValue("new")})
	e := doc.Edit()
	if err := e.SetFields(state); err != nil {
		t.Fatal(err)
	}
	if err := e.Flatten(); err != nil {
		t.Fatal(err)
	}
	got := renderState(t, editOpen(t, editSave(t, e)), nil)
	if n, max := pixDiff(renderState(t, doc, state), got); n > 0 {
		t.Errorf("the flattened page renders unlike the form: %d pixels, by up to %d", n, max)
	}
}

// TestFlattenNotPermitted refuses to flatten in an update what the
// permissions do not let modify.
func TestFlattenNotPermitted(t *testing.T) {
	const all = int32(-4)
	for _, tt := range []struct {
		name string
		perm int32
		want error
	}{
		{"modify", all &^ (1<<8 | 1<<5 | 1<<10), nil},
		{"no modify", all &^ (1 << 3), ErrNotPermitted},
		{"nothing", all &^ (1<<8 | 1<<5 | 1<<3 | 1<<10), ErrNotPermitted},
	} {
		for _, annots := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/annotations=%v", tt.name, annots), func(t *testing.T) {
				e := editOpen(t, testpdf.EncryptedWith("aes128", "", tt.perm)).Edit()
				flatten := e.Flatten
				if annots {
					flatten = e.FlattenAnnotations
				}
				if err := flatten(); err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				if err := e.Update(&out); !errors.Is(err, tt.want) {
					t.Errorf("err %v, want %v", err, tt.want)
				}
				if tt.want != nil && out.Len() > 0 {
					t.Error("something was written")
				}
			})
		}
	}
}

// TestFlattenCertified refuses to flatten annotations of a certified
// document, and lets a signed one be updated.
func TestFlattenCertified(t *testing.T) {
	for mdp, want := range map[int]error{0: nil, 1: ErrCertified, 2: ErrCertified, 3: ErrCertified} {
		e := editOpen(t, signedFile(true, mdp, false)).Edit()
		if err := e.FlattenAnnotations(); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := e.Update(&out); !errors.Is(err, want) {
			t.Errorf("DocMDP %d: err %v, want %v", mdp, err, want)
		}
	}
	if err := NewEditor().FlattenAnnotations(); err == nil {
		t.Error("the annotations of a new document flattened")
	}
}

// pixDiff counts the pixels that differ between a and b, and returns the
// largest difference of a channel.
func pixDiff(a, b *image.RGBA) (n, most int) {
	if len(a.Pix) != len(b.Pix) {
		return len(a.Pix), 255
	}
	for i := 0; i < len(a.Pix); i += 4 {
		d := 0
		for c := range 4 {
			d = max(d, int(a.Pix[i+c])-int(b.Pix[i+c]), int(b.Pix[i+c])-int(a.Pix[i+c]))
		}
		if d > 0 {
			n++
			most = max(most, d)
		}
	}
	return n, most
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
