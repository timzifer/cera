package cera

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"testing"
)

// stream returns a stream object with dictionary entries dict.
func stream(dict, data string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
}

// annotPDF builds a one-page PDF whose page has the annotations objs
// (objects 100 on) and content c.
func annotPDF(c string, annots []string, objs ...string) []byte {
	var refs []string
	for i := range annots {
		refs = append(refs, fmt.Sprintf("%d 0 R", 100+i))
	}
	return buildPDF([]string{c}, "/Annots ["+strings.Join(refs, " ")+"]", append(annots, objs...)...)
}

func renderAnnots(t *testing.T, data []byte, opt RenderOptions) (*image.RGBA, Stats) {
	t.Helper()
	opt.Background = white
	img, st, err := renderPage(t, data, 0, opt)
	if err != nil {
		t.Fatal(err)
	}
	return img, st
}

// apSquare is an appearance of a 10×10 blue square.
var apSquare = stream("/Type /XObject /Subtype /Form /BBox [0 0 10 10]", "0 0 1 rg 0 0 10 10 re f")

func TestAnnotAppearance(t *testing.T) {
	// The 10×10 appearance is fitted to a 20×20 rectangle.
	data := annotPDF("", []string{"<< /Type /Annot /Subtype /Stamp /Rect [20 20 40 40] /AP << /N 101 0 R >> >>"}, apSquare)
	img, st := renderAnnots(t, data, RenderOptions{})
	assertPixel(t, img, 21, 79, blue)
	assertPixel(t, img, 39, 61, blue)
	assertPixel(t, img, 41, 70, white)
	assertPixel(t, img, 30, 59, white)
	if len(st.Unsupported) != 0 || st.Errors != 0 {
		t.Errorf("unsupported %v, errors %d", st.Unsupported, st.Errors)
	}
	img, _ = renderAnnots(t, data, RenderOptions{Annotations: AnnotsNone})
	assertPixel(t, img, 30, 70, white)
}

func TestAnnotAppearanceMatrix(t *testing.T) {
	// A rotated appearance: its BBox under Matrix is what is fitted.
	ap := stream("/Type /XObject /Subtype /Form /BBox [0 0 20 10] /Matrix [0 1 -1 0 0 0]",
		"0 0 1 rg 0 0 20 10 re f 1 0 0 rg 0 0 5 10 re f")
	data := annotPDF("", []string{"<< /Subtype /Stamp /Rect [100 20 110 40] /AP << /N 101 0 R >> >>"}, ap)
	img, _ := renderAnnots(t, data, RenderOptions{})
	// x of the form becomes y: its red first quarter is at the bottom.
	assertPixel(t, img, 105, 78, red)
	assertPixel(t, img, 105, 62, blue)
	assertPixel(t, img, 111, 70, white)
}

func TestAnnotState(t *testing.T) {
	on := stream("/BBox [0 0 10 10]", "1 0 0 rg 0 0 10 10 re f")
	off := stream("/BBox [0 0 10 10]", "0 1 0 rg 0 0 10 10 re f")
	data := annotPDF("", []string{
		"<< /Subtype /Widget /Rect [0 0 10 10] /AS /Off /AP << /N << /On 102 0 R /Off 103 0 R >> >> >>",
		"<< /Subtype /Widget /Rect [20 0 30 10] /AS /On /AP << /N << /On 102 0 R /Off 103 0 R >> >> >>",
	}, on, off)
	img, _ := renderAnnots(t, data, RenderOptions{})
	assertPixel(t, img, 5, 95, green)
	assertPixel(t, img, 25, 95, red)
}

func TestAnnotFlagsAndSkip(t *testing.T) {
	annot := func(x, flags int) string {
		return fmt.Sprintf("<< /Subtype /Square /Rect [%d 0 %d 20] /F %d /AP << /N 104 0 R >> >>", x, x+20, flags)
	}
	data := annotPDF("", []string{
		annot(0, 0),     // shown on screen, not printed
		annot(40, 4),    // Print
		annot(80, 4|32), // Print, NoView
		annot(120, 2|4), // Hidden
	}, apSquare)
	doc, _ := Open(data)
	p, _ := doc.Page(0)
	shown := func(opt RenderOptions) (got [4]bool, st Stats) {
		t.Helper()
		opt.Background, opt.Stats = white, &st
		img := image.NewRGBA(p.Bounds(1))
		if err := p.Render(context.Background(), img, opt); err != nil {
			t.Fatal(err)
		}
		for i := range got {
			got[i] = img.RGBAAt(i*40+10, 90) == blue
		}
		return got, st
	}
	if got, _ := shown(RenderOptions{}); got != [4]bool{true, true, false, false} {
		t.Errorf("view: %v", got)
	}
	got, st := shown(RenderOptions{Annotations: AnnotsPrint})
	if got != [4]bool{false, true, true, false} {
		t.Errorf("print: %v", got)
	}
	if !st.Reused {
		t.Error("another mode interpreted the page again")
	}
	var asked []int
	skip := func(i int) bool {
		asked = append(asked, i)
		return i == 1
	}
	if got, _ := shown(RenderOptions{SkipAnnotation: skip}); got != [4]bool{true, false, false, false} {
		t.Errorf("skip 1: %v", got)
	}
	if fmt.Sprint(asked) != "[0 1]" {
		t.Errorf("asked about %v", asked)
	}
}

func TestAnnotOpacityAndLayer(t *testing.T) {
	data := buildPDFCatalog("/OCProperties << /OCGs [102 0 R] /D << /OFF [102 0 R] >> >>", []string{""},
		"/Annots [100 0 R 101 0 R]",
		"<< /Subtype /Stamp /Rect [0 0 20 20] /CA 0.5 /AP << /N 103 0 R >> >>",
		"<< /Subtype /Stamp /Rect [40 0 60 20] /OC 102 0 R /AP << /N 103 0 R >> >>",
		"<< /Type /OCG /Name (Stamps) >>",
		apSquare)
	img, _ := renderAnnots(t, data, RenderOptions{})
	if c := img.RGBAAt(10, 90); diff(c, color.RGBA{127, 127, 255, 255}) > 1 {
		t.Errorf("half-transparent blue is %v", c)
	}
	assertPixel(t, img, 50, 90, white)
}

func TestAnnotGenerated(t *testing.T) {
	data := annotPDF("0 g 0 0 200 10 re f", []string{
		// A square with a blue border 4 wide and a red interior.
		"<< /Subtype /Square /Rect [10 50 50 90] /C [0 0 1] /IC [1 0 0] /BS << /W 4 >> >>",
		// A circle with only a border.
		"<< /Subtype /Circle /Rect [60 50 100 90] /C [0 1 0] /Border [0 0 2] >>",
		// A highlight across the black bar and the white above it.
		"<< /Subtype /Highlight /Rect [110 0 150 20] /QuadPoints [110 20 150 20 110 0 150 0] /C [1 1 0] >>",
		// A note without appearance: not drawn.
		"<< /Subtype /Text /Rect [160 50 180 70] >>",
		// A thick line with a closed arrow at its end.
		"<< /Subtype /Line /Rect [100 20 200 50] /L [110 30 180 30] /LE [/None /ClosedArrow] /IC [0 0 1] /C [0 0 1] /BS << /W 2 >> >>",
	})
	img, st := renderAnnots(t, data, RenderOptions{})
	assertPixel(t, img, 12, 30, blue) // border
	assertPixel(t, img, 30, 30, red)  // interior
	assertPixel(t, img, 61, 30, green)
	assertPixel(t, img, 80, 30, white)
	assertPixel(t, img, 130, 85, color.RGBA{255, 255, 0, 255})
	assertPixel(t, img, 130, 95, black) // multiplied
	assertPixel(t, img, 140, 70, blue)  // the line
	assertPixel(t, img, 172, 67, blue)  // the arrow, wider than the line
	assertPixel(t, img, 190, 70, white)
	if st.Unsupported["annot-no-ap"] != 1 {
		t.Errorf("unsupported %v", st.Unsupported)
	}
}

func TestAnnotMarkup(t *testing.T) {
	// Text 20 high from y=40 to 60, quads in Acrobat's order (top edge
	// first) and in the specification's (counterclockwise).
	data := annotPDF("", []string{
		"<< /Subtype /Underline /Rect [0 40 50 60] /QuadPoints [0 60 50 60 0 40 50 40] /C [0 0 1] >>",
		"<< /Subtype /StrikeOut /Rect [60 40 110 60] /QuadPoints [60 40 110 40 110 60 60 60] /C [0 0 1] >>",
		"<< /Subtype /Squiggly /Rect [120 40 170 60] /QuadPoints [120 60 170 60 120 40 170 40] /C [0 0 1] >>",
		"<< /Subtype /Ink /Rect [0 0 200 30] /InkList [[10 10 190 10]] /C [1 0 0] /BS << /W 4 >> >>",
	})
	img, _ := renderAnnots(t, data, RenderOptions{Scale: 4})
	near := func(x0, y0, x1, y1 float64) bool { // any blue pixel in the box
		for y := int(y0 * 4); y < int(y1*4); y++ {
			for x := int(x0 * 4); x < int(x1*4); x++ {
				if c := img.RGBAAt(x, y); c.B > 200 && c.R < 100 {
					return true
				}
			}
		}
		return false
	}
	if !near(10, 58, 40, 60) || near(10, 41, 40, 55) {
		t.Error("underline not at the bottom of the text")
	}
	if !near(70, 51, 100, 54) || near(70, 41, 100, 50) || near(70, 55, 100, 60) {
		t.Error("strike-out not through the text")
	}
	if !near(130, 56, 160, 60) || near(130, 41, 160, 54) {
		t.Error("squiggly not at the bottom of the text")
	}
	if c := img.RGBAAt(100*4, 90*4); c != red {
		t.Errorf("ink is %v", c)
	}
}

func TestAnnotNoZoomNoRotate(t *testing.T) {
	ap := stream("/BBox [0 0 40 20]", "0 0 1 rg 0 0 40 20 re f")
	annot := "<< /Subtype /Stamp /Rect [20 20 60 40] /F %d /AP << /N 100 0 R >> >>"
	for _, tc := range []struct {
		name   string
		flags  int
		rotate string
		scale  float64
		want   image.Rectangle // blue, in device pixels
	}{
		{"plain", 0, "", 2, image.Rect(40, 120, 120, 160)},
		// Both keep the upper-left corner (20, 40) where it is.
		{"NoZoom", 8, "", 2, image.Rect(40, 120, 80, 140)},
		{"rotated", 0, "/Rotate 90", 1, image.Rect(20, 20, 40, 60)},
		{"NoRotate", 16, "/Rotate 90", 1, image.Rect(40, 20, 80, 40)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := buildPDF([]string{""}, tc.rotate+" /Annots [101 0 R]", ap, fmt.Sprintf(annot, tc.flags))
			img, _ := renderAnnots(t, data, RenderOptions{Scale: tc.scale})
			var got image.Rectangle
			b := img.Bounds()
			for y := b.Min.Y; y < b.Max.Y; y++ {
				for x := b.Min.X; x < b.Max.X; x++ {
					if img.RGBAAt(x, y) == blue {
						got = got.Union(image.Rect(x, y, x+1, y+1))
					}
				}
			}
			if got != tc.want {
				t.Errorf("blue %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAnnotationsAndLinks(t *testing.T) {
	data := buildPDFCatalog("/Names << /Dests 104 0 R >>", []string{"", ""},
		"/Annots [100 0 R 101 0 R 102 0 R 103 0 R 105 0 R]",
		"<< /Subtype /Link /Rect [0 0 10 10] /A << /S /URI /URI (https://example.com/) >> >>",
		"<< /Subtype /Link /Rect [0 0 10 10] /Dest [12 0 R /XYZ 10 null 2] /F 4 >>",
		"<< /Subtype /Link /Rect [0 0 10 10] /A << /S /GoTo /D (chap) >> >>",
		"<< /Subtype /Text /Rect [5 5 25 25] /Contents <FEFF00C4> /NM (n1) >>",
		"<< /Kids [106 0 R] >>",
		"<< /Subtype /Text >>", // no rectangle
		"<< /Limits [(a) (d)] /Names [(chap) [12 0 R /Fit]] >>",
	)
	doc, _ := Open(data)
	p, _ := doc.Page(0)
	as := p.Annotations()
	if len(as) != 4 {
		t.Fatalf("%d annotations", len(as))
	}
	if l := as[0].Link; l == nil || l.URI != "https://example.com/" || l.Page != -1 {
		t.Errorf("URI link %+v", l)
	}
	l := as[1].Link
	if l == nil || l.Page != 1 || l.Fit != "XYZ" || len(l.Params) != 3 || l.Params[0] != 10 || !math.IsNaN(l.Params[1]) || as[1].Flags != AnnotPrint {
		t.Errorf("explicit link %+v, flags %v", l, as[1].Flags)
	}
	if l := as[2].Link; l == nil || l.Named != "chap" || l.Page != 1 || l.Fit != "Fit" {
		t.Errorf("named link %+v", l)
	}
	if a := as[3]; a.Index != 3 || a.Subtype != "Text" || a.Contents != "Ä" || a.Name != "n1" || a.Rect != (Rect{5, 5, 25, 25}) || a.Link != nil {
		t.Errorf("note %+v", a)
	}
	_, st, _ := renderPage(t, data, 0, RenderOptions{})
	if st.Errors != 1 {
		t.Errorf("errors %d, want 1 for the annotation without /Rect", st.Errors)
	}
}

func TestAnnotText(t *testing.T) {
	ap := stream("/BBox [0 0 100 20] /Resources << /Font << /F1 101 0 R >> >>", "BT /F1 10 Tf 2 5 Td (Note) Tj ET")
	data := annotPDF("BT /F1 10 Tf 10 80 Td (Body) Tj ET", []string{
		"<< /Subtype /FreeText /Rect [10 20 110 40] /AP << /N 102 0 R >> >>",
	}, helvetica, ap)
	data = []byte(strings.Replace(string(data), "/Annots", "/Resources << /Font << /F1 101 0 R >> >> /Annots", 1))
	doc, _ := Open(data)
	p, _ := doc.Page(0)
	text, err := p.Text(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s := text.String(); s != "Body\nNote" {
		t.Errorf("text %q", s)
	}
	var td textDevice
	if err := p.RunWith(context.Background(), &td, RunOptions{Annotations: AnnotsNone}); err != nil {
		t.Fatal(err)
	}
	if s := (&PageText{Chars: td.chars}).String(); s != "Body" {
		t.Errorf("text without annotations %q", s)
	}
}

func TestAnnotSteadyStateAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("sync.Pool drops items under the race detector")
	}
	data := annotPDF("", []string{
		"<< /Subtype /Stamp /Rect [20 20 40 40] /CA 0.5 /AP << /N 102 0 R >> >>",
		"<< /Subtype /Square /Rect [60 20 90 50] /C [1 0 0] >>",
	}, apSquare)
	doc, _ := Open(data)
	p, _ := doc.Page(0)
	dst := image.NewRGBA(p.Bounds(1))
	opt := RenderOptions{Workers: 1, SkipAnnotation: func(i int) bool { return i == 1 }}
	if err := p.Render(context.Background(), dst, opt); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(20, func() {
		if err := p.Render(context.Background(), dst, opt); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 1 {
		t.Errorf("%v allocations per cached render", allocs)
	}
}
