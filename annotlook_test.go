package cera

import (
	"fmt"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
)

// lookFile is a page of annotations without appearance streams whose look
// cera makes up: free texts, the icons of notes and attachments, a caret,
// stamps and links with borders.
func lookFile() []byte {
	objs := map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R>>",
		2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 300]>>",
		// A free text with a red text, a blue border and a yellow box.
		10: "<</Type /Annot /Subtype /FreeText /Rect [10 230 190 290] /Contents (Free text that wraps into more than one line of the box)" +
			" /DA (/Helv 12 Tf 0 0 1 rg) /DS (font: Helvetica 10pt; color: #FF0000) /C [1 1 0.6] /BS <</W 2>>>>",
		// One with a callout, centred text beyond WinAnsi, no box colour.
		11: "<</Type /Annot /Subtype /FreeText /Rect [200 200 390 290] /RD [60 30 0 0] /CL [205 205 240 260 260 260] /LE /OpenArrow" +
			" /Contents <FEFF041F04400438043204350442002C0020043C04380440> /DA (0 g /Helv 11 Tf) /Q 1 /IT /FreeTextCallout>>",
		20: "<</Type /Annot /Subtype /Caret /Rect [10 150 30 170] /C [0 0.6 0]>>",
		21: "<</Type /Annot /Subtype /Stamp /Rect [200 140 390 190] /Name /NotForPublicRelease>>",
		22: "<</Type /Annot /Subtype /Stamp /Rect [200 90 300 130] /Name /Approved>>",
		23: "<</Type /Annot /Subtype /Stamp /Rect [310 90 390 130]>>", // Draft
		// Links: a red border, a dashed one, an underline, and two without.
		30: "<</Type /Annot /Subtype /Link /Rect [10 10 60 30] /Border [0 0 2] /C [1 0 0]>>",
		31: "<</Type /Annot /Subtype /Link /Rect [70 10 120 30] /BS <</W 1 /S /D /D [3 2]>>>>",
		32: "<</Type /Annot /Subtype /Link /Rect [130 10 180 30] /BS <</W 2 /S /U>> /C [0 0 1]>>",
		33: "<</Type /Annot /Subtype /Link /Rect [190 10 240 30]>>",
		34: "<</Type /Annot /Subtype /Link /Rect [250 10 300 30] /Border [0 0 0]>>",
	}
	annots := "10 0 R 11 0 R 20 0 R 21 0 R 22 0 R 23 0 R 30 0 R 31 0 R 32 0 R 33 0 R 34 0 R"
	for i, name := range []string{"Note", "Comment", "Help", "Insert", "Key", "NewParagraph", "Paragraph", "Unknown"} {
		objs[40+i] = fmt.Sprintf("<</Type /Annot /Subtype /Text /Rect [%d 110 %d 130] /Name /%s>>", 10+23*i, 30+23*i, name)
		annots += fmt.Sprintf(" %d 0 R", 40+i)
	}
	for i, name := range []string{"PushPin", "Paperclip", "Graph", "Tag"} {
		objs[50+i] = fmt.Sprintf("<</Type /Annot /Subtype /FileAttachment /Rect [%d 70 %d 100] /Name /%s /C [0.4 0.7 1]>>", 10+33*i, 40+33*i, name)
		annots += fmt.Sprintf(" %d 0 R", 50+i)
	}
	objs[3] = "<</Type /Page /Parent 2 0 R /Annots [" + annots + "]>>"
	return testpdf.File{Objs: objs, Trailer: "/Root 1 0 R"}.Bytes()
}

// TestAnnotLook draws every annotation of lookFile, and nothing for a link
// without a border.
func TestAnnotLook(t *testing.T) {
	doc := editOpen(t, lookFile())
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	img, st := renderAnnots(t, lookFile(), RenderOptions{Scale: 2})
	if n := st.Unsupported["annot-no-ap"]; n != 0 {
		t.Errorf("annot-no-ap counted %d times", n)
	}
	// ink counts the pixels of the rectangle r, in points with y up, that
	// are not white.
	ink := func(r Rect) int {
		n := 0
		for y := int((300 - r.Y1) * 2); y < int((300-r.Y0)*2); y++ {
			for x := int(r.X0 * 2); x < int(r.X1*2); x++ {
				if c := img.RGBAAt(x, y); c.R < 250 || c.G < 250 || c.B < 250 {
					n++
				}
			}
		}
		return n
	}
	for _, a := range p.Annotations() {
		n := ink(a.Rect)
		switch {
		case a.Index >= 9 && a.Index <= 10: // the links without a border
			if n != 0 {
				t.Errorf("link %d without a border draws %d pixels", a.Index, n)
			}
		case n < 40:
			t.Errorf("%s %d (%s) draws %d pixels", a.Subtype, a.Index, a.Markup.iconOrEmpty(), n)
		}
	}
	at := func(x, y float64) [3]uint8 {
		c := img.RGBAAt(int(x*2), int((300-y)*2))
		return [3]uint8{c.R, c.G, c.B}
	}
	// The free text: box colour, border in the colour of /DA, text in that
	// of /DS.
	if c := at(180, 240); c != [3]uint8{255, 255, 153} {
		t.Errorf("box of the free text %v", c)
	}
	if c := at(11, 260); c != [3]uint8{0, 0, 255} {
		t.Errorf("border of the free text %v", c)
	}
	red := 0
	for x := 14.0; x < 186; x += 0.5 {
		for y := 270.0; y < 286; y += 0.5 {
			if c := at(x, y); c[0] > 200 && c[1] < 100 && c[2] < 100 {
				red++
			}
		}
	}
	if red < 50 {
		t.Errorf("%d red pixels of text in the free text", red)
	}
	// The box of the callout starts 60 points right of the rectangle and
	// 30 above its bottom: the text is there, the callout line left of it.
	if n := ink(Rect{262, 262, 388, 288}); n < 100 {
		t.Errorf("the Cyrillic free text draws %d pixels", n)
	}
	if n := ink(Rect{203, 203, 258, 262}); n < 30 {
		t.Errorf("the callout draws %d pixels", n)
	}
	// Link borders.
	if c := at(11, 20); c != [3]uint8{255, 0, 0} {
		t.Errorf("link border %v", c)
	}
	if c := at(155, 11); c != [3]uint8{0, 0, 255} {
		t.Errorf("underlined link %v", c)
	}
	if c := at(155, 29); c != [3]uint8{255, 255, 255} {
		t.Errorf("top of an underlined link %v", c)
	}
}

func (m *Markup) iconOrEmpty() string {
	if m == nil {
		return ""
	}
	return m.Icon
}

// TestAnnotLookFlatten flattens the annotations with generated looks: the
// page shows what it showed, text and its fonts included.
func TestAnnotLookFlatten(t *testing.T) {
	doc := editOpen(t, lookFile())
	want := renderState(t, doc, nil)
	e := doc.Edit()
	if err := e.FlattenAnnotations(); err != nil {
		t.Fatal(err)
	}
	out := editOpen(t, editSave(t, e))
	if s := subtypes(t, out); s != "Link Link Link Link Link" {
		t.Errorf("annotations left: %s", s)
	}
	if n, most := pixDiff(want, renderState(t, out, nil)); n > 0 {
		t.Errorf("the flattened page renders unlike the page: %d pixels, by up to %d", n, most)
	}
}
