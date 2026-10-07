package cera

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timzifer/cera/internal/pdf"

	"github.com/timzifer/cera/internal/cmyk"
)

// streamObj is an indirect stream object with its /Length.
func streamObj(dict string, data []byte) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
}

// imagePDF is a one-page PDF (200 × 100) whose resources name the objects
// imgs as /Im0, /Im1, … (objects 100, 101, …).
func imagePDF(content string, imgs ...string) []byte {
	var names strings.Builder
	for i := range imgs {
		fmt.Fprintf(&names, "/Im%d %d 0 R ", i, 100+i)
	}
	return buildPDF([]string{content}, "/Resources << /XObject << "+names.String()+">> >>", imgs...)
}

func near(a, b color.RGBA, tol int) bool {
	d := func(x, y uint8) bool { return int(x)-int(y) <= tol && int(y)-int(x) <= tol }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B) && d(a.A, b.A)
}

func assertNear(t *testing.T, img *image.RGBA, x, y int, want color.RGBA, tol int) {
	t.Helper()
	if got := img.RGBAAt(x, y); !near(got, want, tol) {
		t.Errorf("pixel (%d, %d) = %v, want %v ±%d", x, y, got, want, tol)
	}
}

var (
	red   = rgba(255, 0, 0, 255)
	green = rgba(0, 255, 0, 255)
	blue  = rgba(0, 0, 255, 255)
	black = rgba(0, 0, 0, 255)
)

func renderImagePage(t *testing.T, data []byte, opt RenderOptions) (*image.RGBA, Stats) {
	t.Helper()
	opt.Background = white
	img, st, err := renderPage(t, data, 0, opt)
	if err != nil {
		t.Fatal(err)
	}
	return img, st
}

func TestImageRGB(t *testing.T) {
	im := streamObj("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8",
		[]byte("\xff\x00\x00\x00\xff\x00\x00\x00\xff\xff\xff\xff"))
	img, st := renderImagePage(t, imagePDF("q 100 0 0 100 0 0 cm /Im0 Do Q", im), RenderOptions{})
	if st.Images != 1 || len(st.Unsupported) != 0 || st.Errors != 0 {
		t.Errorf("stats %+v", st)
	}
	// The first row is at the top of the unit square.
	assertPixel(t, img, 25, 25, red)
	assertPixel(t, img, 75, 25, green)
	assertPixel(t, img, 25, 75, blue)
	assertPixel(t, img, 75, 75, white)
	assertPixel(t, img, 150, 50, white)
}

func TestImageOneBitGray(t *testing.T) {
	// 1 is white, 0 black; a /Decode of [1 0] swaps them.
	for _, tc := range []struct {
		decode      string
		left, right color.RGBA
	}{{"", white, black}, {"/Decode [1 0]", black, white}} {
		im := streamObj("/Subtype /Image /Width 8 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 1 "+tc.decode, []byte{0xaa})
		img, _ := renderImagePage(t, imagePDF("q 80 0 0 10 0 0 cm /Im0 Do Q", im), RenderOptions{})
		assertPixel(t, img, 5, 95, tc.left)
		assertPixel(t, img, 15, 95, tc.right)
	}
}

func TestImageStencil(t *testing.T) {
	for _, tc := range []struct {
		decode      string
		left, right color.RGBA
	}{{"", red, white}, {"/Decode [1 0]", white, red}} {
		im := streamObj("/Subtype /Image /Width 2 /Height 1 /ImageMask true "+tc.decode, []byte{0x40})
		img, _ := renderImagePage(t, imagePDF("1 0 0 rg q 100 0 0 100 0 0 cm /Im0 Do Q", im), RenderOptions{})
		assertPixel(t, img, 25, 50, tc.left)
		assertPixel(t, img, 75, 50, tc.right)
	}
	// With the fill alpha.
	im := streamObj("/Subtype /Image /Width 1 /Height 1 /ImageMask true", []byte{0x00})
	pdf := buildPDF([]string{"/G gs 0 0 1 rg q 100 0 0 100 0 0 cm /Im0 Do Q"},
		"/Resources << /XObject << /Im0 100 0 R >> /ExtGState << /G << /ca 0.5 >> >> >>", im)
	img, _ := renderImagePage(t, pdf, RenderOptions{})
	assertNear(t, img, 50, 50, rgba(127, 127, 255, 255), 1)
}

func TestImageIndexed(t *testing.T) {
	im := streamObj("/Subtype /Image /Width 2 /Height 1 /ColorSpace [/Indexed /DeviceRGB 1 <ff0000 0000ff>] /BitsPerComponent 4",
		[]byte{0x01})
	img, _ := renderImagePage(t, imagePDF("q 100 0 0 100 0 0 cm /Im0 Do Q", im), RenderOptions{})
	assertPixel(t, img, 25, 50, red)
	assertPixel(t, img, 75, 50, blue)
}

func TestImageIndexedDeviceN(t *testing.T) {
	// A palette over five colorants: its entries are wider than CMYK
	// (borb's 0499.pdf). The tint transform keeps the first as red.
	im := streamObj("/Subtype /Image /Width 2 /Height 1 /BitsPerComponent 8 /ColorSpace [/Indexed [/DeviceN [/A /B /C /D /E] /DeviceRGB 101 0 R] 1 <ff00000000 0000000000>]",
		[]byte{0, 1})
	fn := streamObj("/FunctionType 4 /Domain [0 1 0 1 0 1 0 1 0 1] /Range [0 1 0 1 0 1]", []byte("{pop pop pop pop 0 0}"))
	img, _ := renderImagePage(t, imagePDF("q 100 0 0 100 0 0 cm /Im0 Do Q", im, fn), RenderOptions{})
	assertPixel(t, img, 25, 50, red)
	assertPixel(t, img, 75, 50, black)
}

func TestImageCMYKAnd16Bit(t *testing.T) {
	inks := streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceCMYK /BitsPerComponent 8", []byte{0, 255, 255, 0})
	gray16 := streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 16", []byte{0x80, 0x00})
	img, _ := renderImagePage(t, imagePDF("q 100 0 0 100 0 0 cm /Im0 Do Q q 100 0 0 100 100 0 cm /Im1 Do Q", inks, gray16), RenderOptions{})
	r, g, b := cmyk.RGB8(0, 255, 255, 0) // magenta and yellow: a press red
	assertPixel(t, img, 50, 50, rgba(r, g, b, 255))
	assertPixel(t, img, 150, 50, rgba(128, 128, 128, 255))
}

func TestImageSoftMask(t *testing.T) {
	im := streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8 /SMask 101 0 R", []byte{0, 0, 0})
	sm := streamObj("/Subtype /Image /Width 2 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8", []byte{0x80, 0xff})
	img, _ := renderImagePage(t, imagePDF("q 100 0 0 100 0 0 cm /Im0 Do Q", im, sm), RenderOptions{})
	// The mask has its own resolution: half grey, half opaque.
	assertNear(t, img, 25, 50, rgba(127, 127, 127, 255), 1)
	assertPixel(t, img, 75, 50, black)
}

func TestImageStencilMask(t *testing.T) {
	// A one-pixel blue image shaped by a finer stencil mask: its 0 samples
	// show the image.
	im := streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Mask 101 0 R", []byte{0, 0, 255})
	mask := streamObj("/Subtype /Image /Width 2 /Height 1 /ImageMask true", []byte{0x40})
	img, _ := renderImagePage(t, imagePDF("q 100 0 0 100 0 0 cm /Im0 Do Q", im, mask), RenderOptions{})
	assertPixel(t, img, 25, 50, blue)
	assertPixel(t, img, 75, 50, white)
}

func TestImageBrokenMaskIsNotDrawn(t *testing.T) {
	im := streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8 /SMask 101 0 R", []byte{0, 0, 0})
	sm := streamObj("/Subtype /Image /Width 0 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8", []byte{0})
	img, st := renderImagePage(t, imagePDF("q 100 0 0 100 0 0 cm /Im0 Do Q", im, sm), RenderOptions{})
	assertPixel(t, img, 50, 50, white)
	if st.Images != 0 || st.Errors == 0 {
		t.Errorf("stats %+v", st)
	}
}

func TestImageColorKey(t *testing.T) {
	for _, tc := range []struct {
		name, dict string
		data       []byte
	}{
		{"rgb", "/ColorSpace /DeviceRGB /BitsPerComponent 8 /Mask [250 255 0 10 0 10]", []byte{255, 0, 0, 0, 255, 0}},
		{"indexed", "/ColorSpace [/Indexed /DeviceRGB 1 <ff0000 00ff00>] /BitsPerComponent 8 /Mask [0 0]", []byte{0, 1}},
	} {
		im := streamObj("/Subtype /Image /Width 2 /Height 1 "+tc.dict, tc.data)
		img, _ := renderImagePage(t, imagePDF("q 100 0 0 100 0 0 cm /Im0 Do Q", im), RenderOptions{})
		if got := img.RGBAAt(25, 50); got != white {
			t.Errorf("%s: keyed pixel %v, want white", tc.name, got)
		}
		if got := img.RGBAAt(75, 50); got != green {
			t.Errorf("%s: pixel %v, want green", tc.name, got)
		}
	}
}

func TestInlineImage(t *testing.T) {
	for _, c := range []string{
		"q 100 0 0 100 0 0 cm BI /W 2 /H 1 /CS /RGB /BPC 8 ID \xff\x00\x00\x00\x00\xff EI Q",
		"q 100 0 0 100 0 0 cm BI /W 2 /H 1 /CS /RGB /BPC 8 /F /AHx ID ff0000 0000ff> EI Q",
		"q 100 0 0 100 0 0 cm BI /W 2 /H 1 /CS /I1 /BPC 8 ID \x00\x01 EI Q",
		"q 100 0 0 100 0 0 cm BI /W 2 /H 1 /CS [/I /RGB 1 <ff0000 0000ff>] /BPC 8 ID \x00\x01 EI Q",
	} {
		pdf := buildPDF([]string{c}, "/Resources << /ColorSpace << /I1 [/Indexed /DeviceRGB 1 <ff0000 0000ff>] >> >>")
		img, st := renderImagePage(t, pdf, RenderOptions{})
		if st.Images != 1 || st.Errors != 0 {
			t.Errorf("%q: stats %+v", c, st)
		}
		assertPixel(t, img, 25, 50, red)
		assertPixel(t, img, 75, 50, blue)
	}
	// A stencil in the fill colour.
	img, _ := renderImagePage(t, buildPDF([]string{"0 1 0 rg q 100 0 0 100 0 0 cm BI /W 2 /H 1 /IM true ID \x40 EI Q"}, ""), RenderOptions{})
	assertPixel(t, img, 25, 50, green)
	assertPixel(t, img, 75, 50, white)
}

func jpegBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestImageJPEG(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for i := 0; i < len(src.Pix); i += 4 {
		copy(src.Pix[i:], []byte{200, 60, 30, 255})
	}
	gray := image.NewGray(image.Rect(0, 0, 16, 16))
	for i := range gray.Pix {
		gray.Pix[i] = 50
	}
	rgbJPEG, grayJPEG := jpegBytes(t, src), jpegBytes(t, gray)
	ims := []string{
		streamObj("/Subtype /Image /Width 16 /Height 16 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode", rgbJPEG),
		streamObj("/Subtype /Image /Width 16 /Height 16 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /DCTDecode", grayJPEG),
		streamObj("/Subtype /Image /Width 16 /Height 16 /ColorSpace /DeviceGray /BitsPerComponent 8 /Decode [1 0] /Filter /DCTDecode", grayJPEG),
		streamObj("/Subtype /Image /Width 16 /Height 16 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode", []byte("not a jpeg")),
	}
	c := "q 50 0 0 50 0 0 cm /Im0 Do Q q 50 0 0 50 50 0 cm /Im1 Do Q q 50 0 0 50 100 0 cm /Im2 Do Q q 50 0 0 50 150 0 cm /Im3 Do Q"
	img, st := renderImagePage(t, imagePDF(c, ims...), RenderOptions{})
	assertNear(t, img, 25, 75, rgba(200, 60, 30, 255), 4)
	assertNear(t, img, 75, 75, rgba(50, 50, 50, 255), 2)
	assertNear(t, img, 125, 75, rgba(205, 205, 205, 255), 2)
	assertPixel(t, img, 175, 75, white)
	if st.Images != 3 || st.Errors != 1 {
		t.Errorf("stats %+v", st)
	}
}

// jbig2Segments is a JBIG2 generic region 16 by 8 whose right half is ink,
// in the embedded form a /JBIG2Decode stream holds (synthetic; from the
// tests of github.com/go-gfx/gfx/codec, BSD-3-Clause).
var jbig2Segments = []byte{
	0x00, 0x00, 0x00, 0x00, 0x30, 0x00, 0x01, 0x00, 0x00, 0x00, 0x13, 0x00,
	0x00, 0x00, 0x10, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x27, 0x00,
	0x01, 0x00, 0x00, 0x00, 0x1e, 0x00, 0x00, 0x00, 0x10, 0x00, 0x00, 0x00,
	0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x08, 0x03,
	0xff, 0xfd, 0xff, 0x02, 0xfe, 0xfe, 0xfe, 0x8f, 0x66, 0xff, 0xac,
}

func TestImageJBIG2(t *testing.T) {
	gray := streamObj("/Subtype /Image /Width 16 /Height 8 /ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /JBIG2Decode", jbig2Segments)
	mask := streamObj("/Subtype /Image /Width 16 /Height 8 /ImageMask true /Filter /JBIG2Decode", jbig2Segments)
	c := "q 100 0 0 100 0 0 cm /Im0 Do Q 0 0 1 rg q 100 0 0 100 100 0 cm /Im1 Do Q"
	img, st := renderImagePage(t, imagePDF(c, gray, mask), RenderOptions{})
	if st.Images != 2 || st.Errors != 0 {
		t.Errorf("stats %+v", st)
	}
	assertPixel(t, img, 25, 50, white)
	assertPixel(t, img, 75, 50, black)
	assertPixel(t, img, 125, 50, white)
	assertPixel(t, img, 175, 50, blue)
}

func TestImageCCITT(t *testing.T) {
	// One all-white row of 8 pixels, K = -1 (G4): a pass-less vertical 0
	// code (1) per row.
	im := streamObj("/Subtype /Image /Width 8 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /CCITTFaxDecode /DecodeParms << /K -1 /Columns 8 /Rows 1 >>",
		[]byte{0x80})
	img, st := renderImagePage(t, imagePDF("0 g 0 0 200 100 re f q 100 0 0 100 0 0 cm /Im0 Do Q", im), RenderOptions{})
	if st.Images != 1 {
		t.Fatalf("stats %+v", st)
	}
	assertPixel(t, img, 50, 50, white)
}

// TestImageTransforms checks that every device pixel shows the sample the
// inverse transform lands in, for mirrored and rotated images.
func TestImageTransforms(t *testing.T) {
	// 4 × 2 image, every sample a different colour.
	var data []byte
	cols := make([]color.RGBA, 8)
	for i := range cols {
		cols[i] = rgba(uint8(30*i), uint8(255-30*i), uint8(100+i*10), 255)
		data = append(data, cols[i].R, cols[i].G, cols[i].B)
	}
	im := streamObj("/Subtype /Image /Width 4 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8", data)
	for _, m := range []Matrix{
		{80, 0, 0, 40, 10, 10},
		{-80, 0, 0, 40, 190, 10},
		{80, 0, 0, -40, 10, 90},
		{0, 80, -40, 0, 150, 10},
		{0, -80, 40, 0, 50, 90},
	} {
		c := fmt.Sprintf("q %g %g %g %g %g %g cm /Im0 Do Q", m[0], m[1], m[2], m[3], m[4], m[5])
		img, _ := renderImagePage(t, imagePDF(c, im), RenderOptions{})
		// User space (y up, page height 100) of a device pixel centre,
		// then the unit square.
		inv, _ := m.Invert()
		for y := 0; y < 100; y += 3 {
			for x := 0; x < 200; x += 3 {
				u, v := inv.Apply(float64(x)+0.5, 100-(float64(y)+0.5))
				if u < 0.02 || u > 0.98 || v < 0.02 || v > 0.98 {
					continue // edges are antialiased
				}
				// Stay away from sample boundaries too.
				fu, fv := u*4, (1-v)*2
				if fu-float64(int(fu)) < 0.05 || fu-float64(int(fu)) > 0.95 || fv-float64(int(fv)) < 0.05 || fv-float64(int(fv)) > 0.95 {
					continue
				}
				want := cols[int(fv)*4+int(fu)]
				if got := img.RGBAAt(x, y); got != want {
					t.Fatalf("matrix %v pixel (%d, %d) = %v, want %v", m, x, y, got, want)
				}
			}
		}
	}
}

// checker returns a w × h one-bit image of alternating pixels.
func checker(w, h int) []byte {
	stride := (w + 7) / 8
	data := make([]byte, stride*h)
	for y := range h {
		for i := range stride {
			data[y*stride+i] = 0xaa >> (y & 1)
		}
	}
	return data
}

func TestImageMipmapAverages(t *testing.T) {
	// A 512 × 512 checkerboard drawn 32 pixels wide: sampled at single
	// pixels it would alias to black and white; its mip levels are grey.
	gray := streamObj("/Subtype /Image /Width 512 /Height 512 /ColorSpace /DeviceGray /BitsPerComponent 1", checker(512, 512))
	mask := streamObj("/Subtype /Image /Width 512 /Height 512 /ImageMask true", checker(512, 512))
	rgbData := make([]byte, 3*300*300)
	for y := range 300 {
		for x := range 300 {
			if (x+y)&1 == 0 {
				copy(rgbData[3*(y*300+x):], []byte{255, 0, 0})
			} else {
				copy(rgbData[3*(y*300+x):], []byte{0, 0, 255})
			}
		}
	}
	rgb := streamObj("/Subtype /Image /Width 300 /Height 300 /ColorSpace /DeviceRGB /BitsPerComponent 8", rgbData)
	c := "q 32 0 0 32 10 10 cm /Im0 Do Q 0 g q 32 0 0 32 60 10 cm /Im1 Do Q q 21.3 0 0 21.3 110 10 cm /Im2 Do Q"
	img, _ := renderImagePage(t, imagePDF(c, gray, mask, rgb), RenderOptions{})
	for y := 60; y < 88; y++ {
		for x := 12; x < 40; x++ {
			assertNear(t, img, x, y, rgba(128, 128, 128, 255), 3)
			assertNear(t, img, x+50, y, rgba(128, 128, 128, 255), 3)
		}
		for x := 112; x < 130 && y > 70; x++ {
			assertNear(t, img, x, y, rgba(128, 0, 128, 255), 12)
		}
	}
}

func TestImageCache(t *testing.T) {
	im := streamObj("/Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8",
		[]byte("\xff\x00\x00\x00\xff\x00\x00\x00\xff\xff\xff\xff"))
	c := "q 100 0 0 100 0 0 cm /Im0 Do Q"
	pdf := buildPDF([]string{c, c}, "/Resources << /XObject << /Im0 100 0 R >> >>", im)
	doc, err := Open(pdf)
	if err != nil {
		t.Fatal(err)
	}
	var first *Image
	for i := range 2 {
		p, _ := doc.Page(i)
		dst := image.NewRGBA(p.Bounds(1))
		if err := p.Render(context.Background(), dst, RenderOptions{}); err != nil {
			t.Fatal(err)
		}
		if len(doc.imgs) != 1 {
			t.Fatalf("%d cached images", len(doc.imgs))
		}
		img := doc.imgLRU.Front().Value.(*imageEntry).res.img
		if first == nil {
			first = img
		} else if img != first {
			t.Error("the image was decoded again")
		}
		assertPixel(t, dst, 25, 25, red)
	}
	if got := first.RGBA().RGBAAt(1, 1); got != white {
		t.Errorf("Image.RGBA (1, 1) = %v", got)
	}
}

// bigImageObj is an unfiltered n × n DeviceRGB image, red.
func bigImageObj(n int) string {
	data := bytes.Repeat([]byte{255, 0, 0}, n*n)
	return streamObj(fmt.Sprintf("/Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8", n, n), data)
}

func TestImageConcurrentMisses(t *testing.T) {
	doc, err := Open(imagePDF("", bigImageObj(1024)))
	if err != nil {
		t.Fatal(err)
	}
	ref := pdf.Ref{Num: 100}
	s := doc.stream(ref.Object())
	if s == nil {
		t.Fatal("no image stream")
	}
	const n = 8
	var (
		results [n]imageResult
		start   = make(chan struct{})
		wg      sync.WaitGroup
	)
	for i := range n {
		wg.Go(func() {
			<-start
			results[i] = doc.image(ref, s, pdf.Dict{})
		})
	}
	close(start)
	wg.Wait()
	for i := range results {
		if results[i].img == nil || results[i].img != results[0].img {
			t.Fatalf("request %d got image %p, request 0 %p", i, results[i].img, results[0].img)
		}
	}
	if len(doc.imgs) != 1 || len(doc.imgFlights) != 0 {
		t.Errorf("%d cached images, %d decodes pending", len(doc.imgs), len(doc.imgFlights))
	}
}

// TestImageSharedAcrossPages renders pages that draw one large image from
// many goroutines: their display lists hold the same decoded image.
func TestImageSharedAcrossPages(t *testing.T) {
	const pages = 8
	c := "q 100 0 0 100 0 0 cm /Im0 Do Q"
	contents := make([]string, pages)
	for i := range contents {
		contents[i] = c
	}
	doc, err := Open(buildPDF(contents, "/Resources << /XObject << /Im0 100 0 R >> >>", bigImageObj(1024)))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	dsts := make([]*image.RGBA, pages)
	ps := make([]*Page, pages)
	for i := range pages {
		p, _ := doc.Page(i)
		ps[i] = p
		dsts[i] = image.NewRGBA(p.Bounds(1))
		wg.Go(func() {
			if err := p.Render(context.Background(), dsts[i], RenderOptions{Workers: 2}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var first *Image
	for i, p := range ps {
		if len(p.dl.images) != 1 {
			t.Fatalf("page %d: %d images", i, len(p.dl.images))
		}
		if img := p.dl.images[0]; first == nil {
			first = img
		} else if img != first {
			t.Errorf("page %d holds its own decode of the image", i)
		}
		assertPixel(t, dsts[i], 25, 25, red)
	}
}

func imageDrawingPDF() []byte {
	rgbData := make([]byte, 3*64*48)
	for i := range rgbData {
		rgbData[i] = uint8(i * 7)
	}
	rgb := streamObj("/Subtype /Image /Width 64 /Height 48 /ColorSpace /DeviceRGB /BitsPerComponent 8 /SMask 101 0 R", rgbData)
	sm := streamObj("/Subtype /Image /Width 16 /Height 16 /ColorSpace /DeviceGray /BitsPerComponent 8", bytes.Repeat([]byte{0x40, 0xff, 0x90, 0}, 64))
	mask := streamObj("/Subtype /Image /Width 300 /Height 200 /ImageMask true", checker(300, 200))
	c := "q 150 20 -30 90 40 5 cm /Im0 Do Q 0 0 1 rg q 60 0 0 40 130 30 cm /Im1 Do Q q 10 0 0 10 5 5 cm /Im0 Do Q"
	return imagePDF(c, rgb, sm, mask)
}

func TestImageWorkersAndTilesAgree(t *testing.T) {
	data := imageDrawingPDF()
	one, _ := renderImagePage(t, data, RenderOptions{Scale: 3, Workers: 1})
	many, _ := renderImagePage(t, data, RenderOptions{Scale: 3, Workers: 8})
	if d := maxDiff(t, one, many, one.Rect); d > 1 {
		t.Errorf("workers differ by %d", d)
	}
	doc, _ := Open(data)
	p, _ := doc.Page(0)
	for _, r := range []image.Rectangle{image.Rect(0, 0, 100, 100), image.Rect(250, 90, 600, 300), image.Rect(33, 17, 71, 280)} {
		tile := image.NewRGBA(r)
		if err := p.Render(context.Background(), tile, RenderOptions{Scale: 3, Background: white}); err != nil {
			t.Fatal(err)
		}
		if d := maxDiff(t, one, tile, r); d > 1 {
			t.Errorf("tile %v differs by %d", r, d)
		}
	}
}

func TestImageSteadyStateAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("sync.Pool drops items under the race detector")
	}
	doc, _ := Open(imageDrawingPDF())
	p, _ := doc.Page(0)
	dst := image.NewRGBA(p.Bounds(2))
	opt := RenderOptions{Scale: 2, Workers: 1}
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

func TestImageUnsupportedFilter(t *testing.T) {
	im := streamObj("/Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /Foo", []byte("xx"))
	_, st := renderImagePage(t, imagePDF("q 100 0 0 100 0 0 cm /Im0 Do Q", im), RenderOptions{})
	if st.Unsupported["image-filter"] != 1 {
		t.Errorf("stats %+v", st)
	}
}

func BenchmarkRenderImage(b *testing.B) {
	// A bilevel "scan" (2480 × 3508, 300 dpi A4) drawn at 100 dpi, and a
	// colour photo.
	scan := streamObj("/Subtype /Image /Width 2480 /Height 3508 /ColorSpace /DeviceGray /BitsPerComponent 1", checker(2480, 3508))
	photo := make([]byte, 3*1200*800)
	for i := range photo {
		photo[i] = uint8(i * 13)
	}
	ph := streamObj("/Subtype /Image /Width 1200 /Height 800 /ColorSpace /DeviceRGB /BitsPerComponent 8", photo)
	data := buildPDF([]string{"q 200 0 0 100 0 0 cm /Im0 Do Q q 150 20 -20 80 30 5 cm /Im1 Do Q"}, "/Resources << /XObject << /Im0 100 0 R /Im1 101 0 R >> >>", scan, ph)
	doc, _ := Open(data)
	p, _ := doc.Page(0)
	scale := 600.0 / 72
	dst := image.NewRGBA(p.Bounds(scale))
	opt := RenderOptions{Scale: scale, Workers: 1}
	if err := p.Render(context.Background(), dst, opt); err != nil { // decode and record
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := p.Render(context.Background(), dst, opt); err != nil {
			b.Fatal(err)
		}
	}
}

func FuzzImage(f *testing.F) {
	f.Add("/Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8", []byte("\xff\x00\x00\x00\xff\x00\x00\x00\xff\xff\xff\xff"), "q 100 0 0 100 0 0 cm /Im0 Do Q")
	f.Add("/Width 8 /Height 3 /ImageMask true /Decode [1 0]", []byte{0xaa, 0x55}, "q 0 30 -80 0 90 5 cm /Im0 Do Q")
	f.Add("/Width 3 /Height 2 /ColorSpace [/Indexed /DeviceCMYK 2 <00ff00ff ff00ff00 00000000>] /BitsPerComponent 2 /Mask [1 2]", []byte{0x6c, 0x93}, "q 1e3 0 0 1e-3 -5 5 cm /Im0 Do Q")
	f.Add("/Width 16 /Height 8 /ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /JBIG2Decode", jbig2Segments, "q 10 0 0 10 0 0 cm /Im0 Do Q")
	f.Add("/Width 4 /Height 4 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode", []byte("\xff\xd8\xff\xc0\x00\x11\x08\x00\x04\x00\x04\x03"), "q 50 0 0 50 0 0 cm /Im0 Do Q")
	f.Add("/Width 5 /Height 5 /ColorSpace /DeviceGray /BitsPerComponent 16 /Decode [1 0] /Interpolate true", []byte{1, 2, 3}, "q 190 0 0 90 5 5 cm /Im0 Do Q")
	f.Fuzz(func(t *testing.T, dict string, data []byte, c string) {
		if len(dict) > 400 || len(data) > 1<<12 || len(c) > 200 {
			return
		}
		doc, err := Open(imagePDF(c, streamObj("/Subtype /Image "+dict, data)))
		if err != nil {
			return
		}
		p, err := doc.Page(0)
		if err != nil {
			return
		}
		dst := image.NewRGBA(p.Bounds(0.5))
		for _, workers := range []int{1, 3} {
			err = p.Render(context.Background(), dst, RenderOptions{Scale: 0.5, Workers: workers, Deadline: time.Now().Add(2 * time.Second)})
			var pe *PanicError
			if errors.As(err, &pe) {
				t.Fatalf("%v\n%s", pe.Value, pe.Stack)
			}
		}
	})
}

func TestImageSoftMaskMatte(t *testing.T) {
	// Red at half alpha, premultiplied against a white matte: the samples
	// hold (255, 128, 128). Undone, red is drawn at half alpha over white.
	im := streamObj("/Subtype /Image /Width 2 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8 /SMask 101 0 R", []byte{255, 128, 128, 0, 0, 255})
	sm := streamObj("/Subtype /Image /Width 2 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Matte [1 1 1]", []byte{0x80, 0xff})
	img, st := renderImagePage(t, imagePDF("q 100 0 0 100 0 0 cm /Im0 Do Q", im, sm), RenderOptions{})
	assertNear(t, img, 25, 50, rgba(255, 128, 128, 255), 2)
	assertPixel(t, img, 75, 50, blue)
	if st.Unsupported["smask-matte"] != 0 {
		t.Errorf("unsupported %v", st.Unsupported)
	}

	// A grey image against a black matte, with a mask of twice its
	// resolution: the mask is sampled at the image's pixels.
	im = streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /SMask 101 0 R", []byte{64})
	sm = streamObj("/Subtype /Image /Width 2 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Matte [0]", []byte{0x40, 0x40})
	img, _ = renderImagePage(t, imagePDF("q 100 0 0 100 0 0 cm /Im0 Do Q", im, sm), RenderOptions{})
	// c = 64/64·255 = 255 (white) at a quarter alpha over white.
	assertNear(t, img, 50, 50, white, 2)

	// A CMYK matte is applied in RGB and stays counted.
	im = streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceCMYK /BitsPerComponent 8 /SMask 101 0 R", []byte{0, 0, 0, 0})
	sm = streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Matte [0 0 0 1]", []byte{0x80})
	_, st = renderImagePage(t, imagePDF("q 100 0 0 100 0 0 cm /Im0 Do Q", im, sm), RenderOptions{})
	if st.Unsupported["smask-matte"] != 1 {
		t.Errorf("unsupported %v", st.Unsupported)
	}
}

func BenchmarkDecodeMatte(b *testing.B) {
	const w, h = 512, 512
	rgb := make([]byte, 3*w*h)
	alpha := make([]byte, w*h)
	for i := range alpha {
		alpha[i] = uint8(i)
		// Colours premultiplied against white: c' = 255 + α(c − 255).
		a := i & 255
		for c, v := range [3]int{i >> 3 & 255, i >> 5 & 255, i * 7 & 255} {
			rgb[3*i+c] = uint8(255 - (255-v)*a/255)
		}
	}
	for _, matte := range []string{"", "/Matte [1 1 1]"} {
		b.Run(fmt.Sprintf("matte=%v", matte != ""), func(b *testing.B) {
			doc, err := Open(imagePDF("", streamObj("/Width 512 /Height 512 /ColorSpace /DeviceRGB /BitsPerComponent 8 /SMask 101 0 R", rgb),
				streamObj("/Width 512 /Height 512 /ColorSpace /DeviceGray /BitsPerComponent 8 "+matte, alpha)))
			if err != nil {
				b.Fatal(err)
			}
			s := doc.stream(pdf.Ref{Num: 100}.Object())
			b.ReportAllocs()
			b.SetBytes(4 * w * h)
			for b.Loop() {
				if r := doc.decodeImage(s.Dict, doc.r.Raw(s), pdf.Dict{}); r.img == nil {
					b.Fatal("not decoded")
				}
			}
		})
	}
}

func TestImageFilter(t *testing.T) {
	// 64 × 64 samples, columns black and white in turn, drawn 100 pixels
	// square: magnified 1.5625×, which ImageSmooth smooths.
	stripes := make([]byte, 64*64)
	for i := 1; i < len(stripes); i += 2 {
		stripes[i] = 255
	}
	im := streamObj("/Subtype /Image /Width 64 /Height 64 /ColorSpace /DeviceGray /BitsPerComponent 8", stripes)
	interp := streamObj("/Subtype /Image /Width 2 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Interpolate true", []byte{0, 255})
	// Two samples drawn 100 pixels wide, magnified 50×: crisp either way.
	big := streamObj("/Subtype /Image /Width 2 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8", []byte{0, 255})
	pdf := imagePDF("q 100 0 0 100 0 0 cm /Im0 Do Q q 100 0 0 50 100 0 cm /Im1 Do Q q 100 0 0 50 100 50 cm /Im2 Do Q", im, interp, big)
	doc, err := Open(pdf)
	if err != nil {
		t.Fatal(err)
	}
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	smoothed := func(v uint8) bool { return v > 20 && v < 235 }
	for i, c := range []struct {
		filter ImageFilter
		smooth bool
	}{{ImageNearest, false}, {ImageSmooth, true}, {ImageNearest, false}} {
		dst := image.NewRGBA(p.Bounds(1))
		var st Stats
		if err := p.Render(context.Background(), dst, RenderOptions{Background: white, ImageFilter: c.filter, Stats: &st}); err != nil {
			t.Fatal(err)
		}
		if i > 0 && !st.Reused {
			t.Errorf("filter %d: the page was interpreted again", c.filter)
		}
		// Pixel 40 is centred between samples 25 and 26.
		if got := dst.RGBAAt(40, 50).R; smoothed(got) != c.smooth {
			t.Errorf("filter %d: pixel 40 is %d, smoothed %v", c.filter, got, c.smooth)
		}
		// Between the sample centres at x = 125 and 175, but magnified 50×.
		if got := dst.RGBAAt(140, 25).R; smoothed(got) {
			t.Errorf("filter %d: pixel 140 of the 50× image is %d", c.filter, got)
		}
		// /Interpolate is smoothed either way.
		if got := dst.RGBAAt(140, 75).R; !smoothed(got) {
			t.Errorf("filter %d: /Interpolate pixel 140 is %d", c.filter, got)
		}
	}
}

// An opaque image drawn right over another on the same parallelogram
// replaces it, edges included (#25).
func TestImageOverImageEdges(t *testing.T) {
	gray := func(v byte, extra string) string {
		return streamObj("/Subtype /Image /Width 4 /Height 4 /ColorSpace /DeviceGray /BitsPerComponent 8"+extra, bytes.Repeat([]byte{v}, 16))
	}
	blk, wht := gray(0, ""), gray(255, "")
	masked := gray(255, " /SMask 102 0 R")
	opaqueMask := gray(255, "")
	// The image covers x, y in [20.3, 70.3]: device column 20 and row 29
	// (y = 70.3 is row 29.7) are 70 % covered.
	const under = "q 50 0 0 50 20.3 20.3 cm /Im0 Do Q "
	edges := []image.Point{{20, 50}, {70, 50}, {45, 29}, {45, 79}}
	for _, c := range []struct {
		name, over string
		hidden     bool
	}{
		{"same matrix", "q 50 0 0 50 20.3 20.3 cm /Im1 Do Q", true},
		{"flipped", "q 50 0 0 -50 20.3 70.3 cm /Im1 Do Q", true},
		{"masked", "q 50 0 0 50 20.3 20.3 cm /Im2 Do Q", false},
		{"clip between", "0 0 200 100 re W n q 50 0 0 50 20.3 20.3 cm /Im1 Do Q", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, workers := range []int{1, 4} {
				img, _ := renderImagePage(t, imagePDF(under+c.over, blk, wht, masked, opaqueMask), RenderOptions{Workers: workers})
				for _, e := range edges {
					got := img.RGBAAt(e.X, e.Y).R
					// Drawn one after the other, about 0.7·255 + 0.3·0.3·255 = 201.
					if c.hidden && got < 254 || !c.hidden && got > 230 {
						t.Errorf("workers %d: edge %v is %d, lower image hidden %v", workers, e, got, c.hidden)
					}
				}
				assertPixel(t, img, 45, 50, white)
			}
		})
	}
	// A smaller image on top leaves the lower one where it does not
	// reach.
	img, _ := renderImagePage(t, imagePDF(under+"q 40 0 0 40 25.3 25.3 cm /Im1 Do Q", blk, wht), RenderOptions{})
	assertPixel(t, img, 22, 50, black)
	assertPixel(t, img, 45, 50, white)
}

func TestSameParallelogram(t *testing.T) {
	m := Matrix{50, 0, 0, 50, 20.3, 20.3}
	for _, c := range []struct {
		b    Matrix
		want bool
	}{
		{m, true},
		{Matrix{50, 0, 0, -50, 20.3, 70.3}, true},
		{Matrix{-50, 0, 0, 50, 70.3, 20.3}, true},
		{Matrix{0, 50, 50, 0, 20.3, 20.3}, true}, // transposed
		{Matrix{50, 0, 0, 50, 20.4, 20.3}, false},
		{Matrix{40, 0, 0, 40, 20.3, 20.3}, false},
		{Matrix{50, 0, 10, 50, 20.3, 20.3}, false},
	} {
		if got := sameParallelogram(m, c.b); got != c.want {
			t.Errorf("%v: %v, want %v", c.b, got, c.want)
		}
	}
}

// BenchmarkColdSharedImagePages opens a document and renders its pages,
// which all draw one large image, concurrently.
func BenchmarkColdSharedImagePages(b *testing.B) {
	const pages = 8
	contents := make([]string, pages)
	for i := range contents {
		contents[i] = "q 100 0 0 100 0 0 cm /Im0 Do Q"
	}
	data := buildPDF(contents, "/Resources << /XObject << /Im0 100 0 R >> >>", bigImageObj(1024))
	b.ReportAllocs()
	for b.Loop() {
		doc, err := Open(data)
		if err != nil {
			b.Fatal(err)
		}
		var wg sync.WaitGroup
		for i := range pages {
			p, _ := doc.Page(i)
			wg.Go(func() {
				_ = p.Render(context.Background(), image.NewRGBA(p.Bounds(1)), RenderOptions{Workers: 1})
			})
		}
		wg.Wait()
	}
}
