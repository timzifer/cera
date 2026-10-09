package cera

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestTextCharGeometry(t *testing.T) {
	text := pageText(t, textPDF("BT /F1 20 Tf 10 30 Td (AV) Tj ET", helvetica), nil)
	if len(text.Chars) != 2 {
		t.Fatalf("%d chars", len(text.Chars))
	}
	a := text.Chars[0]
	if a.Code != 'A' || a.Text != "A" {
		t.Errorf("code %d text %q", a.Code, a.Text)
	}
	// Baseline at y 70 on the 100 pt high page; A is 0.667 em wide.
	if a.Origin != [2]float64{10, 70} || a.Dir != [2]float64{1, 0} || !approx(a.Advance, 0.667*20) {
		t.Errorf("origin %v dir %v advance %g", a.Origin, a.Dir, a.Advance)
	}
	q := a.Quad.P
	if !approx(q[0][0], 10) || !approx(q[1][0], 10+a.Advance) || q[0][1] != q[1][1] || q[2][1] != q[3][1] {
		t.Errorf("quad %v", q)
	}
	// Ascent side first, above the baseline; descent side below it.
	if !(q[0][1] < 70 && q[2][1] > 70) {
		t.Errorf("quad %v not across the baseline", q)
	}
	if a.Box != a.Quad.Bounds() {
		t.Errorf("box %v, quad bounds %v", a.Box, a.Quad.Bounds())
	}
	if a.MCID != -1 || a.Artifact {
		t.Errorf("unmarked text: MCID %d artifact %v", a.MCID, a.Artifact)
	}
	if v := text.Chars[1]; !approx(v.Origin[0], 10+a.Advance) || !a.Quad.Contains(15, 65) || v.Quad.Contains(15, 65) {
		t.Errorf("V at %v", v.Origin)
	}
}

func TestTextCharTurned(t *testing.T) {
	// Text running up the page, turned 90° counter-clockwise.
	text := pageText(t, textPDF("BT /F1 20 Tf 0 1 -1 0 100 20 Tm (A) Tj ET", helvetica), nil)
	if len(text.Chars) != 1 {
		t.Fatalf("%d chars", len(text.Chars))
	}
	c := text.Chars[0]
	if !approx(c.Dir[0], 0) || !approx(c.Dir[1], -1) || !approx(c.Advance, 0.667*20) {
		t.Errorf("dir %v advance %g", c.Dir, c.Advance)
	}
	q := c.Quad.P
	// Along the text from P0 to P1, up; across to P2, to the right, where
	// the next line would be.
	if !approx(q[1][1]-q[0][1], -c.Advance) || !approx(q[1][0], q[0][0]) || !(q[2][0] > q[0][0]) {
		t.Errorf("quad %v", q)
	}
	if !c.Quad.Contains(c.Origin[0]-2, c.Origin[1]-5) || c.Quad.Contains(c.Origin[0]-2, c.Origin[1]+5) {
		t.Errorf("quad %v around origin %v", q, c.Origin)
	}
}

func TestDisplayMatrix(t *testing.T) {
	for _, extra := range []string{"", "/Rotate 90", "/Rotate 180", "/Rotate 270", "/UserUnit 2", "/Rotate 90 /CropBox [20 10 180 90]"} {
		data := buildPDF([]string{"BT /F1 20 Tf 30 40 Td (A) Tj ET"}, "/Resources << /Font << /F1 100 0 R >> >> "+extra, helvetica)
		doc, err := Open(data)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := doc.Page(0)
		m := p.DisplayMatrix()
		text, err := p.Text(context.Background())
		if err != nil || len(text.Chars) != 1 {
			t.Fatalf("%s: %v, %d chars", extra, err, len(text.Chars))
		}
		x, y := m.Apply(30, 40)
		if o := text.Chars[0].Origin; !approx(o[0], x) || !approx(o[1], y) {
			t.Errorf("%s: origin %v, matrix gives %g %g", extra, o, x, y)
		}
		// The visible box goes to the displayed page.
		w, h := p.Size()
		b := QuadOf(p.Box).Transform(m).Bounds()
		if !approx(b.X0, 0) || !approx(b.Y0, 0) || !approx(b.X1, w) || !approx(b.Y1, h) {
			t.Errorf("%s: box maps to %v, size %g×%g", extra, b, w, h)
		}
		inv, ok := m.Invert()
		if ux, uy := inv.Apply(x, y); !ok || !approx(ux, 30) || !approx(uy, 40) {
			t.Errorf("%s: inverse gives %g %g", extra, ux, uy)
		}
	}
}

func TestTextMarkedContent(t *testing.T) {
	content := `/P <</MCID 3>> BDC BT /F1 20 Tf 10 30 Td (a) Tj ET EMC
		/Artifact BMC BT /F1 20 Tf 30 30 Td (b) Tj /Span <</MCID 4>> BDC (c) Tj EMC ET EMC
		/Span /p1 BDC /X BMC BT /F1 20 Tf 60 30 Td (d) Tj ET EMC EMC
		BT /F1 20 Tf 90 30 Td (e) Tj ET`
	data := buildPDF([]string{content},
		"/Resources << /Font << /F1 100 0 R >> /Properties << /p1 101 0 R >> >>",
		helvetica, "<< /MCID 7 >>")
	text := pageText(t, data, nil)
	want := []struct {
		text     string
		mcid     int
		artifact bool
	}{{"a", 3, false}, {"b", -1, true}, {"c", 4, true}, {"d", 7, false}, {"e", -1, false}}
	if len(text.Chars) != len(want) {
		t.Fatalf("%d chars", len(text.Chars))
	}
	for i, w := range want {
		if c := text.Chars[i]; c.Text != w.text || c.MCID != w.mcid || c.Artifact != w.artifact {
			t.Errorf("char %d: %q MCID %d artifact %v, want %+v", i, c.Text, c.MCID, c.Artifact, w)
		}
	}
}

// mcRecorder records the marked content a page shows, with the text inside.
type mcRecorder struct {
	textDevice
	log []string
}

func (r *mcRecorder) BeginMarkedContent(mc *MarkedContent) {
	s := fmt.Sprintf("<%s %d", mc.Tag, mc.MCID)
	if mc.HasActualText {
		s += fmt.Sprintf(" actual=%q", mc.ActualText)
	}
	if mc.Alt != "" {
		s += fmt.Sprintf(" alt=%q", mc.Alt)
	}
	if mc.Lang != "" {
		s += fmt.Sprintf(" lang=%q", mc.Lang)
	}
	r.log = append(r.log, s+">")
}

func (r *mcRecorder) EndMarkedContent() { r.log = append(r.log, "</>") }

func (r *mcRecorder) ShowText(run *GlyphRun, mode TextMode) {
	n := len(r.chars)
	r.textDevice.ShowText(run, mode)
	for _, c := range r.chars[n:] {
		r.log = append(r.log, c.Text)
	}
}

func TestMarkedContentDevice(t *testing.T) {
	// A form that leaves a sequence open, an EMC without a sequence, a
	// layer that is off, a form in that layer (not a sequence), and
	// properties inline and as resources.
	content := `/Span <</ActualText (fi) /Lang (de-DE) /MCID 1>> BDC BT /F1 20 Tf 10 30 Td (x) Tj ET EMC EMC
		/Figure /p1 BDC /Fm Do EMC
		/OC /off BDC /Hidden BMC BT /F1 20 Tf 10 60 Td (h) Tj ET EMC EMC
		/Span <</ActualText <FEFF00E4> /MCID 2.5>> BDC EMC
		/Span <</ActualText ()>> BDC EMC
		/Fo Do
		/Open BMC`
	fc := "/Inner BMC BT /F1 20 Tf 50 30 Td (y) Tj ET"
	form := fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 200 100] /Resources << /Font << /F1 100 0 R >> >> /Length %d >>\nstream\n%s\nendstream", len(fc), fc)
	data := buildPDF([]string{content},
		"/Resources << /Font << /F1 100 0 R >> /XObject << /Fm 102 0 R /Fo 104 0 R >> /Properties << /p1 101 0 R /off 103 0 R >> >>",
		helvetica, "<< /MCID 5 /Alt <FEFF0041> >>", form, "<< /Type /OCG /Name (Off) >>",
		strings.Replace(form, "/Form", "/Form /OC 103 0 R", 1))
	data = []byte(strings.Replace(string(data), "/Pages 2 0 R", "/Pages 2 0 R /OCProperties << /OCGs [103 0 R] /D << /OFF [103 0 R] >> >>", 1))
	doc, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := doc.Page(0)
	var r mcRecorder
	if err := p.Run(context.Background(), &r, 1, nil); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(r.log, " ")
	want := `<Span 1 actual="fi" lang="de-DE"> x </> <Figure 5 alt="A"> <Inner -1> y </> </> <OC -1> <Hidden -1> </> </> <Span -1 actual="ä"> </> <Span -1 actual=""> </> <Open -1> </>`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
