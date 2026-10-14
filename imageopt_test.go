package cera

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// photo returns w by h RGB samples of a picture: gradients and noise, of
// many colours.
func photo(w, h int) []byte {
	rng := rand.New(rand.NewPCG(3, 4))
	b := make([]byte, 0, 3*w*h)
	for y := range h {
		for x := range w {
			n := rng.IntN(24)
			b = append(b, byte(x*200/w+n), byte(y*200/h+n), byte((x+y)*100/(w+h)+n))
		}
	}
	return b
}

// lineArt returns w by h RGB samples of a drawing: a few colours in
// blocks.
func lineArt(w, h int) []byte {
	b := make([]byte, 0, 3*w*h)
	for y := range h {
		for x := range w {
			switch {
			case (x/10+y/10)%3 == 0:
				b = append(b, 255, 0, 0)
			case x%17 == 0:
				b = append(b, 0, 0, 0)
			default:
				b = append(b, 255, 255, 255)
			}
		}
	}
	return b
}

// bilevel returns w by h one-bit samples of something like text: lines
// of rings of random sizes, which change from row to row as glyphs do.
func bilevel(w, h int) []byte {
	rng := rand.New(rand.NewPCG(5, 6))
	stride := (w + 7) / 8
	b := bytes.Repeat([]byte{0xff}, stride*h)
	for top := 4; top+40 < h; top += 50 {
		for x := 4; x < w-40; {
			r := 10 + rng.Float64()*8
			cx, cy := float64(x)+r, float64(top)+20
			for y := top; y < top+41; y++ {
				for xx := x; xx < x+int(2*r)+1 && xx < w; xx++ {
					d := math.Hypot(float64(xx)-cx, (float64(y)-cy)*0.8)
					if d < r && d > r-4 {
						b[y*stride+xx/8] &^= 1 << (7 - uint(xx%8))
					}
				}
			}
			x += int(2*r) + 4 + rng.IntN(10)
		}
	}
	return b
}

// imagePage is a page of 300 by 200 points with content and the images
// /Im0, /Im1, … (objects 10, 11, …); more are other objects from 20, res
// more resources and cat more entries of the catalogue.
func imagePage(content string, images []string, more map[int]string, res, cat string) []byte {
	var names strings.Builder
	for i := range images {
		fmt.Fprintf(&names, "/Im%d %d 0 R ", i, 10+i)
	}
	objs := map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R " + cat + ">>",
		2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200]>>",
		3: "<</Type /Page /Parent 2 0 R /Contents 4 0 R /Resources <<" + res + " /XObject <<" + names.String() + ">>>>>>",
		4: testpdf.Stream("", []byte(content)),
	}
	for i, im := range images {
		objs[10+i] = im
	}
	for k, v := range more {
		objs[k] = v
	}
	return testpdf.File{Objs: objs, Trailer: "/Root 1 0 R"}.Bytes()
}

func flateImage(dict string, data []byte) string {
	var b bytes.Buffer
	z := zlib.NewWriter(&b)
	z.Write(data)
	z.Close()
	return testpdf.Stream(dict+" /Filter /FlateDecode", b.Bytes())
}

// savedImage returns the dictionary and the stored bytes of image name of
// the first page of a saved file.
func savedImage(t *testing.T, d *Document, name string) (pdf.Dict, int) {
	t.Helper()
	pd, _ := d.r.Page(1)
	o := d.dict(d.dict(pd.Get("Resources")).Get("XObject")).Get(pdf.Name(name))
	s, ok := d.resolve(o).Stream()
	if !ok {
		t.Fatalf("%s is no stream", name)
	}
	return s.Dict, len(d.r.Raw(s))
}

func saveImages(t *testing.T, src []byte, opt ImageOptions) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := editOpen(t, src).Edit().SaveWith(&out, SaveOptions{Images: &opt}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// TestImageOptions resamples images drawn finer than asked, stores
// photographs as JPEG and bilevel images as CCITT, and leaves the rest.
func TestImageOptions(t *testing.T) {
	rgb := func(w, h int, data []byte, extra string) string {
		return flateImage(fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8%s", w, h, extra), data)
	}
	bits := func(w, h int, extra string) string {
		return flateImage(fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /BitsPerComponent 1%s", w, h, extra), bilevel(w, h))
	}
	// Im0: a photograph of 300 pixels drawn 72 points wide (300 ppi) and
	// again 144 (150 ppi); Im1: line art at 300 ppi; Im2: text at 1 bit
	// in grey; Im3: a stencil mask; Im4: a photograph with a soft mask,
	// both 300 ppi; Im5: an indexed image at 300 ppi.
	content := "q 72 0 0 72 0 0 cm /Im0 Do Q q 144 0 0 144 150 50 cm /Im0 Do Q " +
		"q 72 0 0 72 80 0 cm /Im1 Do Q q 100 0 0 50 0 120 cm /Im2 Do Q 1 0 0 rg q 100 0 0 50 120 120 cm /Im3 Do Q " +
		"q 36 0 0 36 0 80 cm /Im4 Do Q q 36 0 0 36 40 80 cm /Im5 Do Q"
	idx := make([]byte, 150*150)
	for i := range idx {
		idx[i] = byte((i%150/15 + i/150/15) % 3)
	}
	src := imagePage(content, []string{
		rgb(300, 300, photo(300, 300), ""),
		rgb(300, 300, lineArt(300, 300), ""),
		bits(400, 200, " /ColorSpace /DeviceGray"),
		bits(400, 200, " /ImageMask true /Decode [1 0]"),
		rgb(150, 150, photo(150, 150), " /SMask 20 0 R"),
		flateImage("/Type /XObject /Subtype /Image /Width 150 /Height 150 /ColorSpace [/Indexed /DeviceRGB 2 <FF0000 00FF00 0000FF>] /BitsPerComponent 8", idx),
	}, map[int]string{
		20: flateImage("/Type /XObject /Subtype /Image /Width 150 /Height 150 /ColorSpace /DeviceGray /BitsPerComponent 8", photo(150, 50)),
	}, "", "")
	doc := editOpen(t, src)
	before := renderAt(t, firstPage(t, doc, 0), 1)

	out := saveImages(t, src, ImageOptions{MaxPPI: 200, TargetPPI: 150, JPEGQuality: 80, Bilevel: true})
	od := editOpen(t, out)
	type want struct {
		w, h   int
		filter string
	}
	for name, w := range map[string]want{
		"Im0": {300, 300, "DCTDecode"},      // drawn at 150 ppi as well: not resampled
		"Im1": {150, 150, "FlateDecode"},    // line art: resampled, lossless
		"Im2": {400, 200, "CCITTFaxDecode"}, // one bit: not resampled
		"Im3": {400, 200, "CCITTFaxDecode"},
		"Im4": {75, 75, "DCTDecode"},
		"Im5": {75, 75, "FlateDecode"},
	} {
		dict, _ := savedImage(t, od, name)
		gw, _ := od.integer(dict.Get("Width"))
		gh, _ := od.integer(dict.Get("Height"))
		f, _ := od.name(dict.Get("Filter"))
		if gw != w.w || gh != w.h || string(f) != w.filter {
			t.Errorf("%s: %d by %d, %s; want %d by %d, %s", name, gw, gh, f, w.w, w.h, w.filter)
		}
	}
	// The soft mask is resampled with its image, the palette kept.
	dict, _ := savedImage(t, od, "Im4")
	if sm, ok := od.resolve(dict.Get("SMask")).Stream(); !ok || sm.Dict.Len() == 0 {
		t.Error("the soft mask went")
	} else if w, _ := od.integer(sm.Dict.Get("Width")); w != 75 {
		t.Errorf("the soft mask is %d wide", w)
	}
	if dict, _ := savedImage(t, od, "Im5"); od.resolve(dict.Get("ColorSpace")).Kind() != pdf.KindArray {
		t.Error("the indexed image lost its palette")
	}
	if len(out) >= len(src) {
		t.Errorf("the file is %d bytes, was %d", len(out), len(src))
	}
	// What the page shows is near what it showed: each image as bright on
	// average, and the pictures within a few levels.
	after := renderAt(t, firstPage(t, od, 0), 1)
	for name, r := range map[string]image.Rectangle{
		"Im0": image.Rect(150, 6, 294, 150), "Im1": image.Rect(80, 128, 152, 200), "Im2": image.Rect(0, 30, 100, 80),
		"Im3": image.Rect(120, 30, 220, 80), "Im4": image.Rect(0, 84, 36, 120), "Im5": image.Rect(40, 84, 76, 120),
	} {
		a, b := before.SubImage(r).(*image.RGBA), after.SubImage(r).(*image.RGBA)
		if ma, mb := meanOf(a), meanOf(b); math.Abs(ma-mb) > 2 {
			t.Errorf("%s is %.1f bright on average, was %.1f", name, mb, ma)
		}
		if d := meanDiff(a, b); d > 12 {
			t.Errorf("%s differs by %.1f levels on average", name, d)
		}
	}

	// Bilevel only: the CCITT images show exactly what they did.
	out = saveImages(t, src, ImageOptions{Bilevel: true})
	od = editOpen(t, out)
	for _, name := range []string{"Im2", "Im3"} {
		if dict, _ := savedImage(t, od, name); dict.Get("Filter").String() != "/CCITTFaxDecode" {
			t.Errorf("%s: %v", name, dict.Get("Filter"))
		}
	}
	for _, name := range []string{"Im0", "Im1", "Im4"} {
		if dict, _ := savedImage(t, od, name); dict.Get("Filter").String() != "/FlateDecode" {
			t.Errorf("%s changed: %v", name, dict.Get("Filter"))
		}
	}
	if n, most := pixDiff(before, renderAt(t, firstPage(t, od, 0), 1)); n != 0 {
		t.Errorf("CCITT changed %d pixels, by up to %d", n, most)
	}

	// Bad options.
	for _, opt := range []ImageOptions{{MaxPPI: -1}, {MaxPPI: 100, TargetPPI: 200}, {JPEGQuality: 101}} {
		if err := doc.Edit().SaveWith(&bytes.Buffer{}, SaveOptions{Images: &opt}); err == nil {
			t.Errorf("%+v was taken", opt)
		}
	}
}

// TestImageOptionsKept leaves JPEG images, images with a colour key mask
// and images drawn nowhere as they are; measures images in hidden layers
// and in forms; and makes the images of imported pages smaller.
func TestImageOptionsKept(t *testing.T) {
	var j bytes.Buffer
	pic := image.NewRGBA(image.Rect(0, 0, 300, 300))
	copy(pic.Pix, bytes.Repeat([]byte{10, 200, 30, 255}, 300*300))
	if err := jpeg.Encode(&j, pic, nil); err != nil {
		t.Fatal(err)
	}
	rgb := flateImage("/Type /XObject /Subtype /Image /Width 300 /Height 300 /ColorSpace /DeviceRGB /BitsPerComponent 8", lineArt(300, 300))
	src := imagePage("q 72 0 0 72 0 0 cm /Im0 Do /Im1 Do Q /OC /L BDC q 72 0 0 72 100 0 cm /Im2 Do Q EMC /Fm Do", []string{
		testpdf.Stream("/Type /XObject /Subtype /Image /Width 300 /Height 300 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode", j.Bytes()),
		strings.Replace(rgb, "/BitsPerComponent 8", "/BitsPerComponent 8 /Mask [255 255 255 255 255 255]", 1),
		rgb, rgb, rgb,
	}, map[int]string{
		30: "<</Type /OCG /Name (Off)>>",
		31: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 300 200] /Resources <</XObject <</Im 14 0 R>>>>", []byte("q 72 0 0 72 200 0 cm /Im Do Q")),
	}, "/Properties <</L 30 0 R>> /XObject <</Fm 31 0 R>>", "/OCProperties <</OCGs [30 0 R] /D <</OFF [30 0 R]>>>>")
	src = bytes.Replace(src, []byte("/XObject <</Fm 31 0 R>> /XObject <<"), []byte("/XObject <</Fm 31 0 R "), 1)

	opt := ImageOptions{MaxPPI: 200, TargetPPI: 100}
	od := editOpen(t, saveImages(t, src, opt))
	for name, w := range map[string]int{"Im0": 300, "Im1": 300, "Im2": 100, "Im3": 300} {
		dict, _ := savedImage(t, od, name)
		if got, _ := od.integer(dict.Get("Width")); got != w {
			t.Errorf("%s is %d wide, want %d", name, got, w)
		}
	}
	// In the form.
	pd, _ := od.r.Page(1)
	fm, _ := od.resolve(od.dict(od.dict(pd.Get("Resources")).Get("XObject")).Get("Fm")).Stream()
	if s, ok := od.resolve(od.dict(od.dict(fm.Dict.Get("Resources")).Get("XObject")).Get("Im")).Stream(); !ok {
		t.Error("the form lost its image")
	} else if w, _ := od.integer(s.Dict.Get("Width")); w != 100 {
		t.Errorf("the image of the form is %d wide", w)
	}

	// Imported into a new document.
	e := NewEditor()
	if err := e.ImportPages(0, editOpen(t, src), EditPage{Index: 0}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := e.SaveWith(&out, SaveOptions{Images: &opt}); err != nil {
		t.Fatal(err)
	}
	if dict, _ := savedImage(t, editOpen(t, out.Bytes()), "Im2"); dict.Get("Width").String() != "100" {
		t.Errorf("the imported image: %v", dict)
	}
}

func firstPage(t *testing.T, d *Document, i int) *Page {
	t.Helper()
	p, err := d.Page(i)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// meanDiff returns the mean difference of the channels of a and b.
func meanDiff(a, b *image.RGBA) float64 {
	sum, n := 0, 0
	r := a.Rect
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			ca, cb := a.RGBAAt(x, y), b.RGBAAt(x, y)
			sum += absDiff(ca.R, cb.R) + absDiff(ca.G, cb.G) + absDiff(ca.B, cb.B)
			n += 3
		}
	}
	return float64(sum) / float64(n)
}

// meanOf returns the mean of the red channel of a.
func meanOf(a *image.RGBA) float64 {
	sum, n := 0, 0
	r := a.Rect
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			sum += int(a.RGBAAt(x, y).R)
			n++
		}
	}
	return float64(sum) / float64(n)
}

// TestResample averages samples, of eight and of four bits, and picks the
// middle one of palette indices.
func TestResample(t *testing.T) {
	src := samples{w: 4, h: 2, n: 1, bpc: 4, data: []byte{0x0f, 0x0f, 0xff, 0x00}}
	got := resample(src, 2, 1, false)
	// Columns 0-1 average (0+15+15+15)/4, columns 2-3 (0+15+0+0)/4, of 15.
	if got.bpc != 8 || got.at(0, 0, 0) != 191 || got.at(1, 0, 0) != 64 {
		t.Errorf("four bits: %d bits, %d %d", got.bpc, got.at(0, 0, 0), got.at(1, 0, 0))
	}
	idx := samples{w: 3, h: 1, n: 1, bpc: 8, data: []byte{1, 2, 3}}
	if got := resample(idx, 1, 1, true); got.at(0, 0, 0) != 2 || got.bpc != 8 {
		t.Errorf("indices: %d", got.at(0, 0, 0))
	}
	rgb := samples{w: 2, h: 1, n: 3, bpc: 16, data: []byte{0, 0, 0xff, 0xff, 0x10, 0, 0xff, 0xff, 0, 0, 0x30, 0}}
	if got := resample(rgb, 1, 1, false); got.at(0, 0, 0) != 0x8000 || got.at(0, 0, 2) != 0x2000 {
		t.Errorf("sixteen bits: %#x %#x", got.at(0, 0, 0), got.at(0, 0, 2))
	}
}
