package cera

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
)

// pageIntentPDF is a document of three pages that show the same: an image
// in DeviceCMYK of toyPatches, below the patches painted in DeviceCMYK,
// below a form that fills a square in an Indexed space on DeviceCMYK
// (cyan) and one with a shading in DeviceCMYK (cyan). The image, the form,
// the space and the shading are one object each for all pages. catalog
// and pages are the /OutputIntents of the catalogue and of each page, ""
// for none; objects 100 and on are profiles.
func pageIntentPDF(catalog string, pages [3]string, profiles ...[]byte) []byte {
	n := len(toyPatches)
	var c strings.Builder
	fmt.Fprintf(&c, "q %d 0 0 50 0 0 cm /Im0 Do Q\n", 20*n)
	for i, p := range toyPatches {
		fmt.Fprintf(&c, "%.5f %.5f %.5f %.5f k %d 50 20 50 re f\n",
			float64(p.ink[0])/255, float64(p.ink[1])/255, float64(p.ink[2])/255, float64(p.ink[3])/255, 20*i)
	}
	c.WriteString("/Fm0 Do\n")
	var px []byte
	for _, p := range toyPatches {
		px = append(px, p.ink[:]...)
	}
	objs := map[int]string{
		1:  "<</Type /Catalog /Pages 2 0 R>>",
		2:  fmt.Sprintf("<</Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 /MediaBox [0 0 %d 150]>>", 20*n),
		10: testpdf.Stream("", []byte(c.String())),
		11: testpdf.Stream(fmt.Sprintf("/Subtype /Image /Width %d /Height 1 /ColorSpace /DeviceCMYK /BitsPerComponent 8", n), px),
		12: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 1000 1000] /Resources <</ColorSpace <</CS0 13 0 R>> /Shading <</Sh0 14 0 R>>>>",
			[]byte("/CS0 cs 1 sc 0 100 20 50 re f q 20 100 20 50 re W n /Sh0 sh Q")),
		13: "[/Indexed /DeviceCMYK 1 <00000000FF000000>]",
		14: "<</ShadingType 2 /ColorSpace /DeviceCMYK /Coords [0 0 1000 0] /Function <</FunctionType 2 /Domain [0 1] /C0 [1 0 0 0] /C1 [1 0 0 0] /N 1>>>>",
	}
	if catalog != "" {
		objs[1] = "<</Type /Catalog /Pages 2 0 R /OutputIntents [" + catalog + "]>>"
	}
	for i, in := range pages {
		extra := ""
		if in != "" {
			extra = " /OutputIntents [" + in + "]"
		}
		objs[3+i] = "<</Type /Page /Parent 2 0 R /Contents 10 0 R /Resources <</XObject <</Im0 11 0 R /Fm0 12 0 R>>>>" + extra + ">>"
	}
	for i, p := range profiles {
		objs[100+i] = testpdf.Stream("/N 4", p)
	}
	return testpdf.File{Objs: objs, Trailer: "/Root 1 0 R"}.Bytes()
}

// renderPageStats renders page i of doc at scale 1 on white.
func renderPageStats(t *testing.T, doc *Document, i int) (*image.RGBA, Stats) {
	t.Helper()
	p, err := doc.Page(i)
	if err != nil {
		t.Fatal(err)
	}
	var st Stats
	img := image.NewRGBA(p.Bounds(1))
	if err := p.Render(context.Background(), img, RenderOptions{Background: white, Stats: &st}); err != nil {
		t.Fatal(err)
	}
	return img, st
}

// TestPageOutputIntent converts DeviceCMYK on a page with an output intent
// of its own through its profile, and on the other pages as the document
// says (#186): the patches, the image, and the space and shading of a form
// the pages share.
func TestPageOutputIntent(t *testing.T) {
	toy, swop := readToyProfile(t), swopProfileFile(t)
	bad := bytes.Clone(toy)
	copy(bad[132+12+4:], []byte{0xff, 0xff, 0xff, 0xff}) // A2B0 out of range
	copy(bad[132+24+4:], []byte{0xff, 0xff, 0xff, 0xff}) // A2B1 too
	toyCyan := rgba(toyPatches[0].rgb[0], toyPatches[0].rgb[1], toyPatches[0].rgb[2], 255)
	naiveCyan := rgba(0, 255, 255, 255)
	for _, tt := range []struct {
		name     string
		data     []byte
		opt      OpenOptions
		want     [3]any // a colour of cyan, or nil for every toy patch
		views    int
		unsupp   [3]string
		ownViews [3]bool // the page is drawn with a view of its own
	}{
		{"pages with and without", pageIntentPDF("", [3]string{intent("GTS_PDFX", 100), "", intent("GTS_PDFX", 100)}, toy),
			OpenOptions{}, [3]any{nil, swopCyan, nil}, 1, [3]string{}, [3]bool{true, false, true}},
		{"with a shared cache", pageIntentPDF("", [3]string{intent("GTS_PDFX", 100), "", intent("GTS_PDFX", 100)}, toy),
			OpenOptions{Cache: NewCache(0, 0)}, [3]any{nil, swopCyan, nil}, 1, [3]string{}, [3]bool{true, false, true}},
		{"over the document's", pageIntentPDF(intent("GTS_PDFX", 101), [3]string{intent("GTS_PDFX", 100), "", ""}, toy, swop),
			OpenOptions{}, [3]any{nil, swopCyan, swopCyan}, 1, [3]string{}, [3]bool{true, false, false}},
		{"the document's", pageIntentPDF(intent("GTS_PDFX", 100), [3]string{intent("GTS_PDFX", 100), "", ""}, toy),
			OpenOptions{}, [3]any{nil, nil, nil}, 0, [3]string{}, [3]bool{}},
		{"of another kind", pageIntentPDF("", [3]string{intent("Custom", 100), "", ""}, toy),
			OpenOptions{}, [3]any{swopCyan, swopCyan, swopCyan}, 0, [3]string{}, [3]bool{}},
		{"ignored", pageIntentPDF("", [3]string{intent("GTS_PDFX", 100), "", ""}, toy),
			OpenOptions{IgnoreOutputIntent: true}, [3]any{swopCyan, swopCyan, swopCyan}, 0, [3]string{}, [3]bool{}},
		{"naive", pageIntentPDF("", [3]string{intent("GTS_PDFX", 100), "", ""}, toy),
			OpenOptions{NaiveCMYK: true}, [3]any{naiveCyan, naiveCyan, naiveCyan}, 0, [3]string{}, [3]bool{}},
		{"the caller's profile", pageIntentPDF("", [3]string{intent("GTS_PDFX", 100), "", ""}, toy),
			OpenOptions{CMYKProfile: swop}, [3]any{swopCyan, swopCyan, swopCyan}, 0, [3]string{}, [3]bool{}},
		{"unreadable", pageIntentPDF(intent("GTS_PDFX", 101), [3]string{intent("GTS_PDFX", 100), "", ""}, bad, toy),
			OpenOptions{}, [3]any{nil, nil, nil}, 1, [3]string{"icc-lut", "", ""}, [3]bool{true, false, false}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := OpenWith(tt.data, tt.opt)
			if err != nil {
				t.Fatal(err)
			}
			// Twice, in an order that would show a page drawn with what
			// another page made.
			for _, i := range []int{0, 1, 2, 1, 0} {
				img, st := renderPageStats(t, doc, i)
				for _, y := range []int{125, 75} {
					if c, ok := tt.want[i].(interface{ RGBA() (r, g, b, a uint32) }); ok {
						r, g, b, _ := c.RGBA()
						assertNear(t, img, 10, y, rgba(uint8(r>>8), uint8(g>>8), uint8(b>>8), 255), 2)
					} else {
						for j, p := range toyPatches {
							assertNear(t, img, 20*j+10, y, rgba(p.rgb[0], p.rgb[1], p.rgb[2], 255), 2)
						}
					}
				}
				cyan := tt.want[i]
				if cyan == nil {
					cyan = toyCyan
				}
				c := cyan.(interface{ RGBA() (r, g, b, a uint32) })
				r, g, b, _ := c.RGBA()
				for _, x := range []int{10, 30} {
					assertNear(t, img, x, 25, rgba(uint8(r>>8), uint8(g>>8), uint8(b>>8), 255), 2)
				}
				want := tt.unsupp[i]
				if want == "" && len(st.Unsupported) != 0 || want != "" && st.Unsupported[want] == 0 {
					t.Errorf("page %d: unsupported %v, want %q", i, st.Unsupported, want)
				}
				if t.Failed() {
					t.Fatalf("page %d", i)
				}
			}
			if len(doc.views) != tt.views {
				t.Errorf("%d views, want %d", len(doc.views), tt.views)
			}
			for i, own := range tt.ownViews {
				p, _ := doc.Page(i)
				if got := p.cmykDoc() != doc; got != own {
					t.Errorf("page %d: a view of its own %v, want %v", i, got, own)
				}
			}
			// Trimming drops the images of the views too.
			doc.Trim()
			if _, st := renderPageStats(t, doc, 0); len(st.Unsupported) != 0 && tt.unsupp[0] == "" {
				t.Errorf("after Trim: unsupported %v", st.Unsupported)
			}
		})
	}
}
