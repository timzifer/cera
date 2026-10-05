package cera

import (
	"image/color"
	"testing"
)

// TestDefaultSpaces remaps device colours through the Default spaces of
// the resources in effect (#40): DefaultRGB a linear RGB profile (grey 0.5
// becomes sRGB 188), DefaultGray a CalGray of gamma 1 (the same),
// DefaultCMYK the toy CMYK profile of internal/cmyk.
func TestDefaultSpaces(t *testing.T) {
	toy := readToyProfile(t)
	cyan := toyPatches[0].rgb
	toyCyan := rgba(cyan[0], cyan[1], cyan[2], 255)
	lin := rgba(188, 188, 188, 255)
	defaults := "/DefaultRGB [/ICCBased 100 0 R] /DefaultGray [/CalGray << /WhitePoint [0.9505 1 1.089] /Gamma 1 >>] /DefaultCMYK [/ICCBased 101 0 R]"
	objs := []string{streamObj("/N 3", iccBytes(1)), streamObj("/N 4", toy)}
	page := func(content, extraRes string, more ...string) []byte {
		return buildPDF([]string{content}, "/Resources << /ColorSpace << "+defaults+" >> "+extraRes+" >>", append(objs, more...)...)
	}
	for _, c := range []struct {
		name    string
		data    []byte
		naive   bool
		missing bool
	}{
		{name: "operators", data: page("0.5 0.5 0.5 rg 0 0 40 100 re f 0.5 g 40 0 40 100 re f 1 0 0 0 k 80 0 40 100 re f", "")},
		{name: "cs operators", data: page("/DeviceRGB cs 0.5 0.5 0.5 sc 0 0 40 100 re f /DeviceGray cs 0.5 sc 40 0 40 100 re f /DeviceCMYK cs 1 0 0 0 sc 80 0 40 100 re f", "")},
		{name: "images", data: page("q 40 0 0 100 0 0 cm /A Do Q q 40 0 0 100 40 0 cm /B Do Q q 40 0 0 100 80 0 cm /C Do Q",
			"/XObject << /A 102 0 R /B 103 0 R /C 104 0 R >>",
			streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8", []byte{128, 128, 128}),
			streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8", []byte{128}),
			streamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceCMYK /BitsPerComponent 8", []byte{255, 0, 0, 0}))},
		{name: "inline images", data: page("q 40 0 0 100 0 0 cm BI /W 1 /H 1 /CS /RGB /BPC 8 ID \x80\x80\x80 EI Q q 40 0 0 100 40 0 cm BI /W 1 /H 1 /CS /G /BPC 8 ID \x80 EI Q q 40 0 0 100 80 0 cm BI /W 1 /H 1 /CS /CMYK /BPC 8 ID \xff\x00\x00\x00 EI Q", "")},
		{name: "NaiveCMYK", data: page("1 0 0 0 k 80 0 40 100 re f", ""), naive: true},
		// A form with resources of its own and no Default spaces draws
		// device colours.
		{name: "form", missing: true, data: page("/F Do", "/XObject << /F 102 0 R >>",
			stream("/Type /XObject /Subtype /Form /BBox [0 0 200 100] /Resources << >>",
				"0.5 0.5 0.5 rg 0 0 40 100 re f 0.5 g 40 0 40 100 re f 1 0 0 0 k 80 0 40 100 re f"))},
	} {
		doc, err := OpenWith(c.data, OpenOptions{NaiveCMYK: c.naive})
		if err != nil {
			t.Fatal(err)
		}
		img, st := renderDocStats(t, doc)
		// The colours of the three bands, x = 20, 60, 100; zero to skip.
		want := [3]color.RGBA{lin, lin, toyCyan}
		switch {
		case c.naive:
			want = [3]color.RGBA{2: rgba(0, 255, 255, 255)}
		case c.missing:
			want = [3]color.RGBA{rgba(128, 128, 128, 255), rgba(128, 128, 128, 255), rgba(0, 174, 240, 255)}
		}
		for i, w := range want {
			if w.A == 0 {
				continue
			}
			if got := img.RGBAAt(40*i+20, 50); !near(got, w, 2) {
				t.Errorf("%s, x %d: %v, want %v", c.name, 40*i+20, got, w)
			}
		}
		if len(st.Unsupported) != 0 {
			t.Errorf("%s: unsupported %v", c.name, st.Unsupported)
		}
	}
}

// TestDefaultSpacesNotInside leaves the device spaces inside other spaces
// alone, as PDFium does: the base of an Indexed space, the alternate of a
// Separation. A Default space of the wrong number of components is not
// used.
func TestDefaultSpacesNotInside(t *testing.T) {
	res := "/Resources << /ColorSpace << /DefaultRGB [/ICCBased 100 0 R] /DefaultCMYK [/ICCBased 101 0 R] " +
		"/I [/Indexed /DeviceRGB 0 <808080>] /S [/Separation /Spot /DeviceCMYK << /FunctionType 2 /Domain [0 1] /C0 [0 0 0 0] /C1 [1 0 0 0] /N 1 >>] >> >>"
	data := buildPDF([]string{"/I cs 0 sc 0 0 40 100 re f /S cs 1 sc 40 0 40 100 re f"}, res,
		streamObj("/N 3", iccBytes(1)), streamObj("/N 4", readToyProfile(t)))
	doc, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	img := renderDoc(t, doc)
	assertNear(t, img, 20, 50, rgba(128, 128, 128, 255), 1)
	assertNear(t, img, 60, 50, rgba(0, 174, 240, 255), 2)

	// DefaultRGB of one component: DeviceRGB stays.
	data = buildPDF([]string{"0.5 0.5 0.5 rg 0 0 40 100 re f"}, "/Resources << /ColorSpace << /DefaultRGB /DeviceGray >> >>")
	img, _, err = renderPage(t, data, 0, RenderOptions{Background: white})
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, img, 20, 50, rgba(128, 128, 128, 255), 1)
}
