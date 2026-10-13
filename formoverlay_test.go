package cera

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
)

// overlayFile is a page whose fields have annotations on them, from the
// bottom up: a field a, a translucent highlight on a part of it, a field
// b on a part of both, a line across b, a field c with nothing on it, an
// opaque square on a corner of a, and a field d under a stamp that is not
// printed. The fields lie on pixels: a field the editor wrote is drawn
// like one that shows a value of a state only there. The content of the
// page leaves a clip open.
func overlayFile() []byte {
	field := func(name, rect, bg string) string {
		return "<</Type /Annot /Subtype /Widget /FT /Tx /T (" + name + ") /V (old) /F 4 /Rect [" + rect + "] /MK <</BG [" + bg + "] /BC [1 0 0]>>>>"
	}
	return testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R /AcroForm <</Fields [50 0 R 52 0 R 54 0 R 56 0 R] /DA (/Helv 10 Tf 0 g) /DR <</Font <</Helv 20 0 R>>>>>>>>",
			2:  "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200]>>",
			3:  "<</Type /Page /Parent 2 0 R /Contents 43 0 R /Annots [50 0 R 51 0 R 52 0 R 53 0 R 54 0 R 55 0 R 56 0 R 57 0 R]>>",
			20: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
			30: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 40 30]", []byte("0 0 1 rg 0 0 40 30 re f")),
			43: testpdf.Stream("", []byte("0.9 g 0 0 300 200 re f 0 0.6 0.6 rg 20 90 200 40 re f q 0 0 100 100 re W n")),
			50: field("a", "30 100 170 130", "1 1 1"),
			51: "<</Type /Annot /Subtype /Highlight /F 4 /Rect [10 90 120 125] /QuadPoints [10 125 120 125 10 90 120 90] /C [1 1 0] /CA 0.5>>",
			52: field("b", "90 80 220 115", "0.8 0.8 1"),
			53: "<</Type /Annot /Subtype /Line /F 4 /Rect [100 70 230 125] /L [105 75 225 120] /C [0 0.5 0] /BS <</W 3>>>>",
			54: field("c", "30 20 130 40", "1 1 1"),
			55: "<</Type /Annot /Subtype /Square /F 4 /Rect [150 120 190 150] /AP <</N 30 0 R>>>>",
			56: field("d", "200 20 280 40", "1 1 1"),
			57: "<</Type /Annot /Subtype /Square /Rect [220 10 260 50] /AP <</N 30 0 R>>>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

// TestFormOverlayOrder draws the values of a state where /Annots has
// their widgets: under the annotations that follow and lie on them, as
// the file with these values draws them (#164).
func TestFormOverlayOrder(t *testing.T) {
	doc := editOpen(t, overlayFile())
	for _, names := range [][]string{{"a"}, {"b"}, {"c"}, {"d"}, {"a", "b"}, {"a", "c", "d"}, {"a", "b", "c", "d"}} {
		t.Run(fmt.Sprint(names), func(t *testing.T) {
			values := map[string]Value{}
			for _, n := range names {
				values[n] = TextValue("new " + n)
			}
			state := fill(t, doc, values)
			e := doc.Edit()
			if err := e.SetFields(state); err != nil {
				t.Fatal(err)
			}
			saved := editOpen(t, editSave(t, e))
			want := renderState(t, saved, nil)
			got := renderState(t, doc, state)
			if n, most := pixDiff(want, got); n > 0 {
				t.Fatalf("the state renders unlike the file with its values: %d pixels, by up to %d", n, most)
			}
			if n, _ := pixDiff(renderState(t, doc, nil), got); n == 0 {
				t.Fatal("the values change nothing")
			}

			// In tiles and bands, by several workers.
			p, err := doc.Page(0)
			if err != nil {
				t.Fatal(err)
			}
			defer func(old bool) { adaptWorkers = old }(adaptWorkers)
			adaptWorkers = false
			white := color.RGBA{255, 255, 255, 255}
			tiled := image.NewRGBA(got.Rect)
			const tile = 96
			for y := got.Rect.Min.Y; y < got.Rect.Max.Y; y += tile {
				for x := got.Rect.Min.X; x < got.Rect.Max.X; x += tile {
					opt := RenderOptions{Scale: 2, Background: white, Workers: 3, Form: state, Region: image.Rect(x, y, x+tile, y+tile)}
					if err := p.Render(context.Background(), tiled, opt); err != nil {
						t.Fatal(err)
					}
				}
			}
			if n, most := pixDiff(got, tiled); n > 0 {
				t.Errorf("tiles differ from the page: %d pixels, by up to %d", n, most)
			}

			// What the mode leaves out is not drawn on a widget either.
			opt := RenderOptions{Scale: 2, Background: white, Workers: 1, Annotations: AnnotsPrint}
			printed := image.NewRGBA(got.Rect)
			sp, err := saved.Page(0)
			if err != nil {
				t.Fatal(err)
			}
			if err := sp.Render(context.Background(), printed, opt); err != nil {
				t.Fatal(err)
			}
			opt.Form = state
			form := image.NewRGBA(got.Rect)
			if err := p.Render(context.Background(), form, opt); err != nil {
				t.Fatal(err)
			}
			if n, most := pixDiff(printed, form); n > 0 {
				t.Errorf("printed, the state renders unlike the file: %d pixels, by up to %d", n, most)
			}
		})
	}
}

// TestFormOverlayPlain keeps the widgets nothing lies on overlays over a
// page drawn in one piece.
func TestFormOverlayPlain(t *testing.T) {
	doc := editOpen(t, overlayFile())
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	dl, _ := p.list(2, false, &limit{ctx: context.Background()})
	defer p.done(dl)
	for _, tt := range []struct {
		names []string
		mode  AnnotMode
		want  string
	}{
		{[]string{"c"}, AnnotsView, "[]"},
		{[]string{"a", "b", "c", "d"}, AnnotsView, "[0 2 6]"},
		{[]string{"a", "b", "c", "d"}, AnnotsPrint, "[0 2]"},
		{[]string{"a", "b", "c", "d"}, AnnotsNone, "[]"},
	} {
		values := map[string]Value{}
		for _, n := range tt.names {
			values[n] = TextValue("new")
		}
		overlays, own := p.widgetLists(fill(t, doc, values).snapshot(), nil, 2, false, &limit{ctx: context.Background()})
		af := annotFilter{mode: tt.mode, skip: func(i int) bool { return own[i] }}
		if got := fmt.Sprint(dl.cuts(overlays, &af, dl.bounds)); got != tt.want {
			t.Errorf("%v, mode %d: the list is cut at %s, want %s", tt.names, tt.mode, got, tt.want)
		}
	}
	// Outside the region nothing is cut.
	overlays, own := p.widgetLists(fill(t, doc, map[string]Value{"a": TextValue("new")}).snapshot(), nil, 2, false, &limit{ctx: context.Background()})
	af := annotFilter{skip: func(i int) bool { return own[i] }}
	if got := dl.cuts(overlays, &af, image.Rect(0, 0, 40, 40)); got != nil {
		t.Errorf("cut at %v for a region the widget is not in", got)
	}
}
