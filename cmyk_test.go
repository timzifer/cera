package cera

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/cmyk"
)

// TestDeviceCMYKPatches paints flat DeviceCMYK patches (#20). Through the
// press profile they come out as Little CMS converts them with colord's
// SWOP TR 005 profile (transicc -t 1 -b), close to the press colours of
// the other renderers (MuPDF and Ghostscript draw cyan 0 174 239, their
// own profile); NaiveCMYK gives the device values.
func TestDeviceCMYKPatches(t *testing.T) {
	patches := []struct {
		cmyk        string
		swop, naive [3]uint8
	}{
		{"1 0 0 0", [3]uint8{0, 174, 240}, [3]uint8{0, 255, 255}},
		{"0 1 0 0", [3]uint8{236, 11, 141}, [3]uint8{255, 0, 255}},
		{"0 0 1 0", [3]uint8{255, 242, 0}, [3]uint8{255, 255, 0}},
		{"0 0 0 1", [3]uint8{43, 40, 41}, [3]uint8{0, 0, 0}},
		{"0.5 0 0 0", [3]uint8{118, 208, 246}, [3]uint8{128, 255, 255}},
		{"0 0.5 0.5 0", [3]uint8{245, 152, 125}, [3]uint8{255, 128, 128}},
		{"1 1 0 0", [3]uint8{55, 53, 147}, [3]uint8{0, 0, 255}},
		{"0 0 0 0.5", [3]uint8{151, 153, 155}, [3]uint8{128, 128, 128}},
		{"0 0 0 0", [3]uint8{255, 255, 255}, [3]uint8{255, 255, 255}},
	}
	var c strings.Builder
	for i, p := range patches {
		fmt.Fprintf(&c, "%s k %d 0 20 100 re f\n", p.cmyk, 20*i)
	}
	data := buildPDF([]string{c.String()}, "")
	for _, naive := range []bool{false, true} {
		doc, err := OpenWith(data, OpenOptions{NaiveCMYK: naive})
		if err != nil {
			t.Fatal(err)
		}
		img := renderDoc(t, doc)
		for i, p := range patches {
			want := p.swop
			if naive {
				want = p.naive
			}
			assertNear(t, img, 20*i+10, 50, rgba(want[0], want[1], want[2], 255), 2)
		}
	}
}

// renderDoc renders the first page of doc on white at 72 dpi.
func renderDoc(t *testing.T, doc *Document) *image.RGBA {
	t.Helper()
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(p.Bounds(1))
	if err := p.Render(context.Background(), img, RenderOptions{Background: white}); err != nil {
		t.Fatal(err)
	}
	return img
}

// BenchmarkDecodeCMYK decodes and draws a 1200 × 800 DeviceCMYK photo (a
// smooth one and one of noise) through the press profile and naively.
func BenchmarkDecodeCMYK(b *testing.B) {
	for _, kind := range []string{"smooth", "noise"} {
		px := make([]byte, 4*1200*800)
		for i := range 1200 * 800 {
			x, y := i%1200, i/1200
			c := px[4*i:][:4]
			if kind == "smooth" {
				c[0], c[1], c[2], c[3] = uint8(x/5), uint8(y*255/800), uint8((x+y)/8), uint8(x*y/4000)
			} else {
				c[0], c[1], c[2], c[3] = uint8(i*13), uint8(i*7+y), uint8(i*29), uint8(i*3+x)
			}
		}
		im := streamObj("/Subtype /Image /Width 1200 /Height 800 /ColorSpace /DeviceCMYK /BitsPerComponent 8", px)
		data := buildPDF([]string{"q 200 0 0 100 0 0 cm /Im0 Do Q"}, "/Resources << /XObject << /Im0 100 0 R >> >>", im)
		for _, naive := range []bool{false, true} {
			name := kind + "/swop"
			if naive {
				name = kind + "/naive"
			}
			b.Run(name, func(b *testing.B) {
				dst := image.NewRGBA(image.Rect(0, 0, 200, 100))
				for b.Loop() {
					doc, _ := OpenWith(data, OpenOptions{NaiveCMYK: naive})
					p, _ := doc.Page(0)
					if err := p.Render(context.Background(), dst, RenderOptions{Workers: 1}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// toyPatches are inks and what Little CMS 2.18 makes of them through
// internal/cmyk/testdata/toy-lut16.icc (relative colorimetric, black
// point compensation), far from what the bundled profile makes.
var toyPatches = []struct {
	ink [4]uint8
	rgb [3]uint8
}{
	{[4]uint8{255, 0, 0, 0}, [3]uint8{0, 192, 252}},
	{[4]uint8{0, 255, 0, 0}, [3]uint8{234, 89, 155}},
	{[4]uint8{0, 0, 255, 0}, [3]uint8{233, 213, 19}},
	{[4]uint8{0, 0, 0, 255}, [3]uint8{79, 79, 79}},
	{[4]uint8{128, 0, 0, 0}, [3]uint8{135, 217, 247}},
	{[4]uint8{0, 128, 128, 0}, [3]uint8{238, 156, 108}},
	{[4]uint8{255, 255, 0, 0}, [3]uint8{67, 70, 162}},
	{[4]uint8{0, 0, 0, 128}, [3]uint8{156, 156, 156}},
	{[4]uint8{0, 0, 0, 0}, [3]uint8{240, 240, 240}},
}

func readToyProfile(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("internal", "cmyk", "testdata", "toy-lut16.icc"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// toyPatchPDF paints toyPatches side by side, each 20 wide, in the colour
// space set by csOp ("" for DeviceCMYK).
func toyPatchPDF(csOp, pageExtra string, objs ...string) []byte {
	var c strings.Builder
	op := "k"
	if csOp != "" {
		c.WriteString(csOp + "\n")
		op = "scn"
	}
	for i, p := range toyPatches {
		fmt.Fprintf(&c, "%.5f %.5f %.5f %.5f %s %d 0 20 100 re f\n",
			float64(p.ink[0])/255, float64(p.ink[1])/255, float64(p.ink[2])/255, float64(p.ink[3])/255, op, 20*i)
	}
	return buildPDF([]string{c.String()}, pageExtra, objs...)
}

// renderDocStats renders the first page of doc on white at 72 dpi, with
// its statistics.
func renderDocStats(t *testing.T, doc *Document) (*image.RGBA, Stats) {
	t.Helper()
	p, err := doc.Page(0)
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

// TestCMYKProfile converts DeviceCMYK through a caller's profile (#29).
func TestCMYKProfile(t *testing.T) {
	doc, err := OpenWith(toyPatchPDF("", ""), OpenOptions{CMYKProfile: readToyProfile(t)})
	if err != nil {
		t.Fatal(err)
	}
	img := renderDoc(t, doc)
	for i, p := range toyPatches {
		assertNear(t, img, 20*i+10, 50, rgba(p.rgb[0], p.rgb[1], p.rgb[2], 255), 2)
	}
	// NaiveCMYK takes precedence.
	doc, err = OpenWith(toyPatchPDF("", ""), OpenOptions{CMYKProfile: readToyProfile(t), NaiveCMYK: true})
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, renderDoc(t, doc), 10, 50, rgba(0, 255, 255, 255), 1)

	for name, b := range map[string][]byte{
		"garbage": []byte("not a profile"),
		"rgb":     func() []byte { b := bytes.Clone(readToyProfile(t)); copy(b[16:], "RGB "); return b }(),
	} {
		if _, err := OpenWith(toyPatchPDF("", ""), OpenOptions{CMYKProfile: b}); err == nil {
			t.Errorf("%s: opened", name)
		}
	}
}

// TestICCBasedCMYK converts ICCBased spaces of four components through
// their own profile (#29), fills and images; one cera cannot read goes
// through DeviceCMYK's and is counted as icc-lut.
func TestICCBasedCMYK(t *testing.T) {
	toy := readToyProfile(t)
	res := "/Resources << /ColorSpace << /CS0 [/ICCBased 100 0 R] >> >>"
	icc := streamObj("/N 4", toy)
	data := toyPatchPDF("/CS0 cs", res, icc)
	for _, opt := range []OpenOptions{{}, {CMYKProfile: swopProfileFile(t)}} {
		doc, err := OpenWith(data, opt)
		if err != nil {
			t.Fatal(err)
		}
		img, st := renderDocStats(t, doc)
		for i, p := range toyPatches {
			assertNear(t, img, 20*i+10, 50, rgba(p.rgb[0], p.rgb[1], p.rgb[2], 255), 2)
		}
		if len(st.Unsupported) != 0 {
			t.Errorf("unsupported %v", st.Unsupported)
		}
	}
	doc, err := OpenWith(data, OpenOptions{NaiveCMYK: true})
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, renderDoc(t, doc), 10, 50, rgba(0, 255, 255, 255), 1)

	// An image in the space: the patches as pixels, each 20 wide.
	var px []byte
	for _, p := range toyPatches {
		px = append(px, p.ink[:]...)
	}
	im := streamObj(fmt.Sprintf("/Subtype /Image /Width %d /Height 1 /ColorSpace [/ICCBased 101 0 R] /BitsPerComponent 8", len(toyPatches)), px)
	data = buildPDF([]string{fmt.Sprintf("q %d 0 0 100 0 0 cm /Im0 Do Q", 20*len(toyPatches))},
		"/Resources << /XObject << /Im0 100 0 R >> >>", im, icc)
	doc, err = Open(data)
	if err != nil {
		t.Fatal(err)
	}
	img := renderDoc(t, doc)
	for i, p := range toyPatches {
		assertNear(t, img, 20*i+10, 50, rgba(p.rgb[0], p.rgb[1], p.rgb[2], 255), 2)
	}

	// A profile cera cannot read: through the bundled one, counted.
	bad := bytes.Clone(toy)
	copy(bad[len(bad)-200:], bytes.Repeat([]byte{0xff}, 200))
	copy(bad[132+12+4:], []byte{0xff, 0xff, 0xff, 0xff}) // A2B0 out of range
	copy(bad[132+24+4:], []byte{0xff, 0xff, 0xff, 0xff}) // A2B1 too
	doc, err = OpenWith(toyPatchPDF("/CS0 cs", res, streamObj("/N 4", bad)), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	img, st := renderDocStats(t, doc)
	assertNear(t, img, 10, 50, rgba(0, 174, 240, 255), 2)
	if st.Unsupported["icc-lut"] == 0 {
		t.Errorf("unsupported %v, want icc-lut", st.Unsupported)
	}
}

func swopProfileFile(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("internal", "cmyk", "testdata", "swop-lut16.icc"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCMYKProfileShared(t *testing.T) {
	a, err := OpenWith(toyPatchPDF("", ""), OpenOptions{CMYKProfile: readToyProfile(t)})
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenWith(toyPatchPDF("", ""), OpenOptions{CMYKProfile: readToyProfile(t)})
	if err != nil {
		t.Fatal(err)
	}
	if a.cmykSpace().cmyk != b.cmykSpace().cmyk || a.cmykSpace().cmyk == cmyk.Default() {
		t.Error("documents opened with one profile do not share its table")
	}
}
