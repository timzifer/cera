package cera

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/cmyk"
)

// intentPDF paints toyPatches in DeviceCMYK, or in the space csOp sets, in
// a document with the given /OutputIntents; objs are objects 100 and on.
func intentPDF(intents, csOp, pageExtra string, objs ...string) []byte {
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
	return buildPDFCatalog("/OutputIntents ["+intents+"]", []string{c.String()}, pageExtra, objs...)
}

// intent is an output intent of kind s with the profile in object n.
func intent(s string, n int) string {
	return fmt.Sprintf("<< /Type /OutputIntent /S /%s /OutputConditionIdentifier (Toy) /DestOutputProfile %d 0 R >> ", s, n)
}

// swopCyan is what the bundled profile makes of pure cyan.
var swopCyan = rgba(0, 174, 240, 255)

// TestOutputIntent converts DeviceCMYK through the profile of the
// document's output intent (#137), unless the options say otherwise.
func TestOutputIntent(t *testing.T) {
	toy := readToyProfile(t)
	icc := streamObj("/N 4", toy)
	rgbProfile := bytes.Clone(toy)
	copy(rgbProfile[16:], "RGB ")
	bad := bytes.Clone(toy)
	copy(bad[len(bad)-200:], bytes.Repeat([]byte{0xff}, 200))
	copy(bad[132+12+4:], []byte{0xff, 0xff, 0xff, 0xff}) // A2B0 out of range
	copy(bad[132+24+4:], []byte{0xff, 0xff, 0xff, 0xff}) // A2B1 too

	toyCyan := rgba(toyPatches[0].rgb[0], toyPatches[0].rgb[1], toyPatches[0].rgb[2], 255)
	for _, tt := range []struct {
		name    string
		data    []byte
		opt     OpenOptions
		want    any // nil: every toy patch
		counted string
	}{
		{"PDF/X", intentPDF(intent("GTS_PDFX", 100), "", "", icc), OpenOptions{}, nil, ""},
		{"PDF/A", intentPDF(intent("GTS_PDFA1", 100), "", "", icc), OpenOptions{}, nil, ""},
		{"PDF/E", intentPDF(intent("ISO_PDFE1", 100), "", "", icc), OpenOptions{}, nil, ""},
		{"another kind", intentPDF(intent("Custom", 100), "", "", icc), OpenOptions{}, swopCyan, ""},
		{"no profile", intentPDF("<< /S /GTS_PDFX /OutputConditionIdentifier (FOGRA39) >>", "", ""), OpenOptions{}, swopCyan, ""},
		{"ignored", intentPDF(intent("GTS_PDFX", 100), "", "", icc), OpenOptions{IgnoreOutputIntent: true}, swopCyan, ""},
		{"the caller's profile", intentPDF(intent("GTS_PDFX", 100), "", "", icc), OpenOptions{CMYKProfile: swopProfileFile(t)}, swopCyan, ""},
		{"naive", intentPDF(intent("GTS_PDFX", 100), "", "", icc), OpenOptions{NaiveCMYK: true}, rgba(0, 255, 255, 255), ""},
		{"an RGB profile", intentPDF(intent("GTS_PDFA1", 100), "", "", streamObj("/N 3", rgbProfile)), OpenOptions{}, swopCyan, ""},
		{"RGB, then CMYK", intentPDF(intent("GTS_PDFA1", 100)+intent("GTS_PDFX", 101), "", "", streamObj("/N 3", rgbProfile), icc), OpenOptions{}, nil, ""},
		{"unreadable", intentPDF(intent("GTS_PDFX", 100), "", "", streamObj("/N 4", bad)), OpenOptions{}, swopCyan, "icc-lut"},
		{"unreadable, ignored", intentPDF(intent("GTS_PDFX", 100), "", "", streamObj("/N 4", bad)), OpenOptions{IgnoreOutputIntent: true}, swopCyan, ""},
		{"no array", buildPDFCatalog("/OutputIntents 7", []string{"1 0 0 0 k 0 0 20 100 re f"}, ""), OpenOptions{}, swopCyan, ""},
		// DefaultCMYK remaps DeviceCMYK before the intent is asked.
		{"DefaultCMYK", intentPDF(intent("GTS_PDFX", 100), "", "/Resources << /ColorSpace << /DefaultCMYK [/ICCBased 101 0 R] >> >>",
			streamObj("/N 4", swopProfileFile(t)), icc), OpenOptions{}, toyCyan, ""},
	} {
		doc, err := OpenWith(tt.data, tt.opt)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		img, st := renderDocStats(t, doc)
		if c, ok := tt.want.(interface{ RGBA() (r, g, b, a uint32) }); ok {
			r, g, b, _ := c.RGBA()
			assertNear(t, img, 10, 50, rgba(uint8(r>>8), uint8(g>>8), uint8(b>>8), 255), 2)
		} else {
			for i, p := range toyPatches {
				assertNear(t, img, 20*i+10, 50, rgba(p.rgb[0], p.rgb[1], p.rgb[2], 255), 2)
			}
		}
		if t.Failed() {
			t.Fatalf("%s", tt.name)
		}
		if tt.counted == "" && len(st.Unsupported) != 0 || tt.counted != "" && st.Unsupported[tt.counted] == 0 {
			t.Errorf("%s: unsupported %v, want %q", tt.name, st.Unsupported, tt.counted)
		}
	}
}

// TestOutputIntentUses converts what stands on DeviceCMYK through the
// intent too — images, the bases of other spaces — and shares a profile
// with the ICCBased spaces that name it.
func TestOutputIntentUses(t *testing.T) {
	icc := streamObj("/N 4", readToyProfile(t))
	var px []byte
	for _, p := range toyPatches {
		px = append(px, p.ink[:]...)
	}
	im := streamObj(fmt.Sprintf("/Subtype /Image /Width %d /Height 1 /ColorSpace /DeviceCMYK /BitsPerComponent 8", len(toyPatches)), px)
	data := buildPDFCatalog("/OutputIntents ["+intent("GTS_PDFX", 101)+"]",
		[]string{fmt.Sprintf("q %d 0 0 100 0 0 cm /Im0 Do Q", 20*len(toyPatches))},
		"/Resources << /XObject << /Im0 100 0 R >> >>", im, icc)
	doc, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	img := renderDoc(t, doc)
	for i, p := range toyPatches {
		assertNear(t, img, 20*i+10, 50, rgba(p.rgb[0], p.rgb[1], p.rgb[2], 255), 2)
	}

	// An Indexed space on DeviceCMYK, and a shading in it.
	lookup := "<" + strings.Repeat("FF000000", 2) + ">"
	data = buildPDFCatalog("/OutputIntents ["+intent("GTS_PDFX", 100)+"]",
		[]string{"/CS0 cs 1 sc 0 0 100 100 re f /Sh0 sh"},
		"/Resources << /ColorSpace << /CS0 [/Indexed /DeviceCMYK 1 "+lookup+"] >> /Shading << /Sh0 << /ShadingType 2 /ColorSpace /DeviceCMYK"+
			" /Coords [100 0 200 0] /Function << /FunctionType 2 /Domain [0 1] /C0 [1 0 0 0] /C1 [1 0 0 0] /N 1 >> /Extend [false false] >> >> >>", icc)
	if doc, err = Open(data); err != nil {
		t.Fatal(err)
	}
	img = renderDoc(t, doc)
	cyan := rgba(toyPatches[0].rgb[0], toyPatches[0].rgb[1], toyPatches[0].rgb[2], 255)
	assertNear(t, img, 50, 50, cyan, 2)
	assertNear(t, img, 150, 50, cyan, 2)

	// One profile object for the intent and an ICCBased space: one table.
	data = intentPDF(intent("GTS_PDFX", 100), "", "/Resources << /ColorSpace << /CS0 [/ICCBased 100 0 R] >> >>", icc)
	data = bytes.Replace(data, []byte(" re f\n"), []byte(" re f /CS0 cs 1 0 0 0 scn 0 0 5 5 re f\n"), 1)
	if doc, err = Open(data); err != nil {
		t.Fatal(err)
	}
	if _, st := renderDocStats(t, doc); len(st.Unsupported) != 0 || len(doc.cmykTables) != 1 {
		t.Errorf("unsupported %v, %d tables", st.Unsupported, len(doc.cmykTables))
	}

	// The profile of the intent counts against the budget of profiles.
	if doc, err = Open(intentPDF(intent("GTS_PDFX", 100), "", "", icc)); err != nil {
		t.Fatal(err)
	}
	doc.cmykTables = map[*cmyk.Table]bool{}
	for range maxCMYKProfiles {
		doc.cmykTables[cmyk.NewTable(nil)] = true
	}
	img, st := renderDocStats(t, doc)
	assertNear(t, img, 10, 50, swopCyan, 2)
	if st.Unsupported["icc-budget"] == 0 {
		t.Errorf("unsupported %v, want icc-budget", st.Unsupported)
	}
}
