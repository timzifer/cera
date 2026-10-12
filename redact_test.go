package cera

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/color"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// redactFile is a page of 300 by 200 points with text, a line of text in
// a form XObject that a second page shows too, text with an /ActualText,
// shapes, an image, an inline image, text in a hidden layer, links and a
// field; and a structure tree that speaks for some of it.
func redactFile(extra string) []byte {
	return testpdf.File{Objs: redactObjs(extra), Trailer: "/Root 1 0 R"}.Bytes()
}

func redactObjs(extra string) map[int]string {
	page := "BT /F1 12 Tf 20 170 Td (Public SECRET words) Tj 0 -16 Td [(Left ) -200 (HIDDEN) -300 ( right)] TJ ET\n" +
		"/Span <</MCID 0 /ActualText (TOPSECRET)>> BDC BT /F1 12 Tf 200 170 Td (XY) Tj ET EMC\n" +
		"/P <</MCID 1>> BDC BT /F1 12 Tf 240 170 Td (Keep) Tj ET EMC\n" +
		"1 0 0 rg 20 100 30 20 re f\n" + // red, beside the area
		"0 1 0 rg 100 100 40 20 re f\n" + // green, in the area
		"0 0 1 RG 4 w 10 60 m 290 60 l S\n" + // blue, through the area
		"q 60 0 0 40 200 20 cm /Im1 Do Q\n" + // red, half in an area
		"q 10 0 0 10 110 70 cm BI /W 1 /H 1 /CS /G /BPC 8 ID \x00 EI Q\n" + // black, in the area
		"/OC /L1 BDC BT /F1 12 Tf 100 110 Td (LAYERED) Tj ET EMC\n" +
		"/Fm1 Do\n" + extra
	return map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R /StructTreeRoot 30 0 R /OCProperties <</OCGs [40 0 R] /D <</OFF [40 0 R]>>>>" +
			" /AcroForm <</Fields [22 0 R 23 0 R] /DA (/Helv 10 Tf 0 g) /DR <</Font <</Helv 6 0 R>>>>>>>>",
		2: "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 300 200] /Resources 7 0 R>>",
		3: "<</Type /Page /Parent 2 0 R /Contents 5 0 R /Annots [20 0 R 21 0 R 22 0 R 23 0 R] /Thumb 9 0 R /StructParents 0>>",
		4: "<</Type /Page /Parent 2 0 R /Contents 8 0 R>>",
		5: testpdf.Stream("", []byte(page)),
		6: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
		7: "<</Font <</F1 6 0 R>> /XObject <</Im1 9 0 R /Fm1 10 0 R>> /Properties <</L1 40 0 R>>>>",
		8: testpdf.Stream("", []byte("/Fm1 Do")),
		9: testpdf.Stream("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8", bytes.Repeat([]byte{255, 0, 0}, 4)),
		10: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 300 200] /Resources <</Font <</F1 6 0 R>> /XObject <</In 11 0 R>>>>",
			[]byte("BT /F1 12 Tf 20 30 Td (Form SECRET2 text) Tj ET /In Do")),
		11: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 300 200] /Resources <</Font <</F1 6 0 R>>>>",
			[]byte("BT /F1 12 Tf 20 10 Td (Inner SECRET3 end) Tj ET")),
		20: "<</Type /Annot /Subtype /Link /Rect [100 100 140 120] /A <</S /URI /URI (https://secret.example/)>>>>",
		21: "<</Type /Annot /Subtype /Link /Rect [20 100 50 120] /A <</S /URI /URI (https://example.com/)>>>>",
		22: "<</Type /Annot /Subtype /Widget /FT /Tx /T (inside) /V (SECRETVALUE) /Rect [110 60 140 75]>>",
		23: "<</Type /Annot /Subtype /Widget /FT /Tx /T (outside) /V (plain) /Rect [20 60 50 75]>>",
		30: "<</Type /StructTreeRoot /K 31 0 R>>",
		31: "<</Type /StructElem /S /Document /Alt (About TOPSECRET things) /K [32 0 R 33 0 R]>>",
		32: "<</Type /StructElem /S /Span /P 31 0 R /Pg 3 0 R /K 0 /ActualText (TOPSECRET) /E (TOPSECRET in full)>>",
		33: "<</Type /StructElem /S /P /P 31 0 R /Pg 3 0 R /K 1 /Alt (Kept words)>>",
		40: "<</Type /OCG /Name (Hidden)>>",
	}
}

// everything returns all a file holds, to look for what should be gone:
// its objects as they read, and the data of its streams decoded.
func everything(t *testing.T, file []byte) []byte {
	t.Helper()
	d, err := pdf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if d.Repaired() {
		t.Fatal("the file needed a repair")
	}
	var all bytes.Buffer
	size, _ := d.Trailer().Get("Size").Int()
	for n := int32(1); n < int32(size); n++ {
		o, err := d.Get(pdf.Ref{Num: n})
		if err != nil {
			continue
		}
		fmt.Fprintf(&all, "%d: %v\n", n, o)
		if s, ok := o.Stream(); ok {
			all.Write(d.Decode(s).Data)
			all.WriteByte('\n')
		}
	}
	return all.Bytes()
}

// reachable returns all that page i of a file leads to — its content, its
// resources and what they name, its annotations — but for the page tree
// above it: the objects as they read and the data of their streams.
func reachable(t *testing.T, file []byte, i int) []byte {
	t.Helper()
	d, err := pdf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	page, err := d.Page(i + 1)
	if err != nil {
		t.Fatal(err)
	}
	var all bytes.Buffer
	seen := map[pdf.Ref]bool{}
	var walk func(o pdf.Object, depth int)
	walk = func(o pdf.Object, depth int) {
		if depth > 40 {
			return
		}
		if r, ok := o.Ref(); ok {
			if seen[r] {
				return
			}
			seen[r] = true
			o = d.Resolve(o)
			fmt.Fprintf(&all, "%d: %v\n", r.Num, o)
		}
		switch o.Kind() {
		case pdf.KindArray:
			a, _ := o.Array()
			for _, x := range a {
				walk(x, depth+1)
			}
		case pdf.KindDict, pdf.KindStream:
			dd, _ := o.Dict()
			if s, ok := o.Stream(); ok {
				dd = s.Dict
				all.Write(d.Decode(s).Data)
				all.WriteByte('\n')
			}
			for k, v := range dd.All() {
				if k != "Parent" && k != "P" && k != "Pg" {
					walk(v, depth+1)
				}
			}
		}
	}
	fmt.Fprintf(&all, "page: %v\n", page)
	for k, v := range page.All() {
		if k != "Parent" {
			walk(v, 0)
		}
	}
	return all.Bytes()
}

// redactAreas returns the quads of the passages of the text of page i.
func redactAreas(t *testing.T, doc *Document, i int, passages ...string) []Quad {
	t.Helper()
	p, _ := doc.Page(i)
	text, err := p.Text(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var qs []Quad
	for _, s := range passages {
		ms := text.Find(s, SearchOptions{})
		if len(ms) == 0 {
			t.Fatalf("page %d has no %q in %q", i, s, text.String())
		}
		for _, m := range ms {
			qs = append(qs, text.Quads(m.Range)...)
		}
	}
	return qs
}

func textOf(t *testing.T, doc *Document, i int) *PageText {
	t.Helper()
	p, _ := doc.Page(i)
	text, err := p.Text(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestRedact(t *testing.T) {
	src := redactFile("")
	doc := editOpen(t, src)
	before := textOf(t, doc, 0)
	for _, s := range []string{"SECRET", "HIDDEN", "TOPSECRET", "SECRET2", "SECRET3"} {
		if !strings.Contains(before.String(), s) {
			t.Fatalf("the page does not read %q: %q", s, before.String())
		}
	}
	e := doc.Edit()
	black := color.RGBA{0, 0, 0, 255}
	if err := e.Redact(0, redactAreas(t, doc, 0, "SECRET ", "HIDDEN", "TOPSECRET", "SECRET2", "SECRET3"), black); err != nil {
		t.Fatal(err)
	}
	// The shapes, a link and a field: without a box, to see what is left.
	if err := e.Redact(0, []Quad{QuadOf(Rect{95, 75, 150, 150}), QuadOf(Rect{230, 130, 300, 190})}, nil); err != nil {
		t.Fatal(err)
	}
	// An update would keep it all.
	if err := e.Update(io.Discard); !errors.Is(err, ErrNoUpdate) {
		t.Errorf("an update with a redaction: %v", err)
	}
	out := editSave(t, e)
	od := editOpen(t, out)

	// What is read, and where: the rest of each line has not moved.
	after := textOf(t, od, 0)
	got := strings.Join(strings.Fields(after.String()), " ")
	for _, s := range []string{"SECRET", "HIDDEN", "TOP", "XY", "LAYERED"} {
		if strings.Contains(got, s) {
			t.Errorf("the redacted page reads %q, with %q", got, s)
		}
	}
	where := func(text *PageText, s string) [2]float64 {
		ms := text.Find(s, SearchOptions{})
		if len(ms) != 1 {
			t.Fatalf("%q is there %d times", s, len(ms))
		}
		return text.Chars[ms[0].Range.Start].Origin
	}
	for _, s := range []string{"Public", "words", "Left", "right", "Keep", "Form", "text", "Inner", "end"} {
		if a, b := where(before, s), where(after, s); !samePoints([][2]float64{a}, [][2]float64{b}) {
			t.Errorf("%q moved from %v to %v", s, a, b)
		}
	}

	// What is in the file: of what only this page had, nothing; and
	// nothing of the rest in what the page leads to. The second page
	// shows the form still, as it was.
	all, page := everything(t, out), reachable(t, out, 0)
	check := func(where string, in []byte, gone ...string) {
		t.Helper()
		for _, s := range gone {
			if i := bytes.Index(in, []byte(s)); i >= 0 {
				t.Errorf("%s has %q still: …%q…", where, s, in[max(i-60, 0):min(i+60, len(in))])
			}
		}
	}
	check("the file", all, "HIDDEN", "LAYERED", "48494444454E", "TOPSECRET", "secret.example", "SECRETVALUE", "/Thumb 9", "\xff\x00\x00\xff\x00\x00\xff\x00\x00\xff")
	check("the page", page, "SECRET", "534543524554", "Im1", "Fm1")
	for _, kept := range []string{"Kept words", "example.com", `"plain"`} {
		if !bytes.Contains(all, []byte(kept)) {
			t.Errorf("the file lost %q", kept)
		}
	}
	if got := strings.Join(strings.Fields(textOf(t, od, 1).String()), " "); got != "Form SECRET2 text Inner SECRET3 end" {
		t.Errorf("page 2 reads %q", got)
	}

	// What is drawn.
	p, _ := od.Page(0)
	img := renderView(t, p)
	blue := color.RGBA{0, 0, 255, 255}
	for name, c := range map[string]struct {
		x, y int
		want color.RGBA
	}{
		"the red shape beside the area":   {35, 90, red},
		"the green shape in the area":     {120, 90, white},
		"the line left of the area":       {50, 140, blue},
		"the line in the area":            {120, 140, white},
		"the line right of the area":      {180, 140, blue},
		"the image beside the area":       {210, 160, red},
		"the image in the area":           {250, 160, white},
		"the inline image in the area":    {115, 125, white},
		"the box over the word":           {70, 25, black},
		"the box over the word in a form": {70, 165, black},
	} {
		if got := img.RGBAAt(c.x, c.y); got != c.want {
			t.Errorf("%s is %v, want %v", name, got, c.want)
		}
	}

	// The links and the fields.
	if links := p.Links(); len(links) != 1 || links[0].Target.URI != "https://example.com/" {
		t.Errorf("links %+v", links)
	}
	if form := od.Form(); form == nil || form.Field("inside") != nil || form.Field("outside") == nil {
		t.Errorf("fields:\n%s", formString(od))
	}
	// The structure: what stood for the content is gone, the rest not.
	tree := od.Structure()
	if tree == nil || len(tree.Elems) != 1 {
		t.Fatalf("structure %+v", tree)
	}
	root := tree.Elems[0]
	span, para := root.Kids[0].Elem, root.Kids[1].Elem
	if root.Alt != "" || span.ActualText != "" || span.Expansion != "" || para.Alt != "Kept words" || len(span.Kids) != 1 || span.Kids[0].MCID != 0 {
		t.Errorf("structure: %+v, %+v, %+v", root, span, para)
	}

	// The document it was made of is as it was.
	if got := textOf(t, doc, 0).String(); got != before.String() {
		t.Errorf("the edited document reads %q now", got)
	}
}

// TestRedactShapes redacts what a text search does not find: paths that
// are clips, shadings, a stencil, content that leaves the graphics state
// open, and a page that is turned.
func TestRedactShapes(t *testing.T) {
	// A clip in the area with a fill through it; a clip that reaches into
	// the area; a shading over all; a stencil mask in the area; and a
	// state left open with another matrix.
	extra := "q 100 100 40 20 re W n 1 0 1 rg 0 0 300 200 re f Q\n" +
		"q 0 20 300 20 re W n 0 1 1 rg 0 0 300 200 re f Q\n" +
		"q 10 0 0 10 120 80 cm /Mask Do Q\n" +
		"q 2 0 0 2 500 500 cm\n"
	src := redactFile(extra)
	src = bytes.Replace(src, []byte("/Im1 9 0 R"), []byte("/Im1 9 0 R /Mask 12 0 R"), 1)
	doc := editOpen(t, src)
	if _, err := doc.Page(0); err != nil {
		t.Fatal(err)
	}
	e := doc.Edit()
	green := color.RGBA{0, 255, 0, 255}
	if err := e.Redact(0, []Quad{QuadOf(Rect{95, 75, 150, 190})}, green); err != nil {
		t.Fatal(err)
	}
	od := editOpen(t, editSave(t, e))
	p, _ := od.Page(0)
	img := renderView(t, p)
	cyan := color.RGBA{0, 255, 255, 255}
	for name, c := range map[string]struct {
		x, y int
		want color.RGBA
	}{
		// The box is where the area is, whatever the content left open.
		"the box":                           {120, 100, green},
		"the box at its corner":             {96, 189, green},
		"beside the box":                    {93, 100, white},
		"the band left of the area":         {50, 170, cyan},
		"the band right of the area":        {200, 170, cyan},
		"what showed through the clip only": {160, 90, white},
	} {
		if got := img.RGBAAt(c.x, c.y); got != c.want {
			t.Errorf("%s is %v, want %v", name, got, c.want)
		}
	}
	// Nothing of the area is in the content but the box: the same page
	// without the box is empty there.
	e = doc.Edit()
	if err := e.Redact(0, []Quad{QuadOf(Rect{95, 75, 150, 190})}, nil); err != nil {
		t.Fatal(err)
	}
	p, _ = editOpen(t, editSave(t, e)).Page(0)
	img = renderView(t, p)
	for y := 76; y < 189; y++ {
		for x := 96; x < 149; x++ {
			if c := img.RGBAAt(x, y); c != white {
				t.Fatalf("the area has %v at %d, %d", c, x, y)
			}
		}
	}

	// A turned page: the area is where the page shows it.
	turned := bytes.Replace(redactFile(""), []byte("/Contents 5 0 R"), []byte("/Rotate 90 /Contents 5 0 R"), 1)
	doc = editOpen(t, turned)
	e = doc.Edit()
	if err := e.Redact(0, redactAreas(t, doc, 0, "SECRET ", "HIDDEN"), nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(strings.Fields(textOf(t, editOpen(t, editSave(t, e)), 0).String()), " "); strings.Contains(got, "SECRET ") || strings.Contains(got, "HIDDEN") ||
		!strings.Contains(got, "Public") || !strings.Contains(got, "words") || !strings.Contains(got, "Left") {
		t.Errorf("the turned page reads %q", got)
	}
}

func TestRedactAnnotations(t *testing.T) {
	// Redaction annotations: one with quads and a colour, one with its
	// rectangle alone.
	doc := editOpen(t, redactFile(""))
	quads := redactAreas(t, doc, 0, "HIDDEN")
	p0, _ := doc.Page(0)
	inv, _ := p0.DisplayMatrix().Invert()
	u := quads[0].Transform(inv)
	pts := fmt.Sprintf("%g %g %g %g %g %g %g %g", u.P[0][0], u.P[0][1], u.P[1][0], u.P[1][1], u.P[2][0], u.P[2][1], u.P[3][0], u.P[3][1])
	objs := redactObjs("")
	objs[3] = strings.Replace(objs[3], "23 0 R]", "23 0 R 50 0 R 51 0 R]", 1)
	objs[50] = "<</Type /Annot /Subtype /Redact /Rect [0 0 1 1] /QuadPoints [" + pts + "] /IC [0 0 1] /OverlayText (REDACTED)>>"
	objs[51] = "<</Type /Annot /Subtype /Redact /Rect [95 110 150 125]>>"
	src := testpdf.File{Objs: objs, Trailer: "/Root 1 0 R"}.Bytes()
	doc = editOpen(t, src)
	e := doc.Edit()
	n, err := e.ApplyRedactions()
	if err != nil || n != 2 {
		t.Fatalf("%d redactions applied: %v", n, err)
	}
	out := editSave(t, e)
	od := editOpen(t, out)
	got := strings.Join(strings.Fields(textOf(t, od, 0).String()), " ")
	if strings.Contains(got, "HIDDEN") || !strings.Contains(got, "SECRET words") || !strings.Contains(got, "Left") || !strings.Contains(got, "right") {
		t.Errorf("the page reads %q", got)
	}
	p, _ := od.Page(0)
	for _, a := range p.Annotations() {
		if a.Subtype == "Redact" {
			t.Errorf("a redaction annotation is left: %+v", a)
		}
	}
	img := renderView(t, p)
	blue := color.RGBA{0, 0, 255, 255}
	if a, b := img.RGBAAt(68, 42), img.RGBAAt(120, 83); a != blue || b != white {
		t.Errorf("the redacted word is %v, the shape under the second annotation %v", a, b)
	}
	if again, err := od.Edit().ApplyRedactions(); again != 0 || err != nil {
		t.Errorf("%d redactions in the redacted file: %v", again, err)
	}
}

func TestRedactOdd(t *testing.T) {
	doc := editOpen(t, redactFile(""))
	area := []Quad{QuadOf(Rect{95, 75, 150, 150})}
	// Nothing to do.
	e := doc.Edit()
	if err := e.Redact(0, nil, nil); err != nil || e.redacts() {
		t.Errorf("no areas: %v", err)
	}
	if err := e.Redact(0, []Quad{QuadOf(Rect{10, 10, 10, 50})}, nil); err != nil || e.redacts() {
		t.Errorf("an area without area: %v", err)
	}
	if err := e.Redact(5, area, nil); err == nil {
		t.Error("a page that is not there was redacted")
	}
	nan := QuadOf(Rect{0, 0, 10, 10})
	nan.P[1][0] = math.NaN()
	if err := e.Redact(0, []Quad{nan}, nil); err == nil {
		t.Error("an area that is nowhere was redacted")
	}
	// Drawings and redactions of one page are written one after the other.
	if err := e.Draw(0, true, func(c *Canvas) { c.FillRect(Rect{0, 0, 5, 5}, red) }); err != nil {
		t.Fatal(err)
	}
	if err := e.Redact(0, area, nil); err == nil {
		t.Error("a page with drawings was redacted")
	}
	e = doc.Edit()
	if err := e.Redact(0, area, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.Draw(0, true, func(c *Canvas) { c.FillRect(Rect{0, 0, 5, 5}, red) }); err == nil {
		t.Error("a redacted page was drawn on")
	}
	// A new page has nothing to redact.
	ne := NewEditor()
	if err := ne.AddPage(0, 100, 100); err != nil {
		t.Fatal(err)
	}
	if err := ne.Redact(0, area, nil); err == nil {
		t.Error("a blank page was redacted")
	}
	// What signatures do not allow.
	if err := editOpen(t, signedFile(true, 2, false)).Edit().Redact(0, area, nil); !errors.Is(err, ErrCertified) {
		t.Errorf("redacting a certified document: %v", err)
	}
	locked := editOpen(t, signedFile(true, 0, false)).Edit()
	if err := locked.Redact(0, []Quad{QuadOf(Rect{0, 0, 200, 100})}, nil); !errors.Is(err, ErrLocked) || locked.redacts() {
		t.Errorf("redacting a locked field: %v", err)
	}

	// Content that does not run to its end: nothing is written.
	bad := bytes.Replace(redactFile(""), []byte("/Fm1 Do\n"), []byte("/Fm1 Do\n"+strings.Repeat("q ", 5)), 1)
	be := editOpen(t, bad).Edit()
	if err := be.Redact(0, area, color.Black); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := be.Save(&out); err != nil {
		t.Errorf("content that leaves states open: %v", err)
	}
}

// TestRedactText redacts text shown by every operator, in a font of glyph
// procedures and in a form drawn twice, with text that stands for it in
// the resources.
func TestRedactText(t *testing.T) {
	page := "BT /F1 12 Tf 14 TL 20 180 Td (first line) Tj (second AAAA line) ' 2 1 (third BBBB line) \" 0 Tw 0 Tc ET\n" +
		"/Span /MC0 BDC BT /F1 12 Tf 20 120 Td (CC) Tj ET EMC\n" +
		"/Span /MC1 BDC BT /F1 12 Tf 60 120 Td (stays) Tj ET EMC\n" +
		"BT /T3 10 Tf 20 100 Td (ab) Tj /F1 12 Tf ( after) Tj ET\n" +
		"/Fm Do q 1 0 0 1 0 -30 cm /Fm Do Q\n"
	src := testpdf.File{
		Objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R>>",
			2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200]>>",
			3: "<</Type /Page /Parent 2 0 R /Contents 5 0 R /Resources <</Font <</F1 6 0 R /T3 7 0 R>> /XObject <</Fm 10 0 R>>" +
				" /Properties <</MC0 <</ActualText (DDDD)>> /MC1 <</ActualText (EEEE)>>>>>>>>",
			5: testpdf.Stream("", []byte(page)),
			6: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
			7: "<</Type /Font /Subtype /Type3 /FontBBox [0 0 1000 1000] /FontMatrix [0.001 0 0 0.001 0 0] /CharProcs <</a 8 0 R /b 9 0 R>>" +
				" /Encoding <</Type /Encoding /Differences [97 /a /b]>> /FirstChar 97 /LastChar 98 /Widths [1000 1000] /Resources <<>>>>",
			8:  testpdf.Stream("", []byte("1000 0 d0 0 0 1000 1000 re f")),
			9:  testpdf.Stream("", []byte("1000 0 d0 100 100 800 800 re f")),
			10: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 300 200] /Resources <</Font <</F1 6 0 R>>>>", []byte("BT /F1 12 Tf 20 60 Td (twice FFFF here) Tj ET")),
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
	doc := editOpen(t, src)
	before := textOf(t, doc, 0)
	e := doc.Edit()
	// The words, the first glyph of the other font, and of the form its
	// first use alone.
	areas := redactAreas(t, doc, 0, "AAAA", "BBBB", "DDDD")
	areas = append(areas, QuadOf(Rect{20, 90, 29, 100}))
	twice := redactAreas(t, doc, 0, "FFFF")
	if len(twice) != 2 {
		t.Fatalf("the form is drawn %d times", len(twice))
	}
	areas = append(areas, twice[0])
	if err := e.Redact(0, areas, nil); err != nil {
		t.Fatal(err)
	}
	out := editSave(t, e)
	od := editOpen(t, out)
	after := textOf(t, od, 0)
	got := strings.Join(strings.Fields(after.String()), " ")
	for _, s := range []string{"AAAA", "BBBB", "DDDD", "CC"} {
		if strings.Contains(got, s) {
			t.Errorf("the page reads %q, with %q", got, s)
		}
	}
	for _, s := range []string{"first line", "second", "third", "EEEE", "after", "twice", "here"} {
		if !strings.Contains(got+" "+after.String(), s) {
			t.Errorf("the page reads %q, without %q", got, s)
		}
	}
	if n := strings.Count(got, "FFFF"); n != 1 {
		t.Errorf("the word of the form is there %d times in %q, want once", n, got)
	}
	// Nothing moved: the line ends, and what follows the glyph that went.
	where := func(text *PageText, s string, nth int) [2]float64 {
		ms := text.Find(s, SearchOptions{})
		if len(ms) <= nth {
			t.Fatalf("%q is there %d times", s, len(ms))
		}
		return text.Chars[ms[nth].Range.Start].Origin
	}
	for _, c := range []struct {
		s      string
		a, b   int // which of them before and after
		before string
	}{{"line", 1, 1, ""}, {"line", 2, 2, ""}, {"second", 0, 0, ""}, {"third", 0, 0, ""}, {"after", 0, 0, ""}, {"here", 0, 0, ""}, {"here", 1, 1, ""}, {"twice", 1, 1, ""}} {
		// To the thousandth of a point: the numbers that stand for the
		// glyphs are written to four places.
		if a, b := where(before, c.s, c.a), where(after, c.s, c.b); math.Abs(a[0]-b[0]) > 1e-3 || math.Abs(a[1]-b[1]) > 1e-3 {
			t.Errorf("%q moved from %v to %v", c.s, a, b)
		}
	}
	// The first glyph of the drawn font is gone and the second is there:
	// squares of 10 points at 20 and at 30, 100 from the bottom.
	p, _ := od.Page(0)
	img := renderView(t, p)
	if a, b := img.RGBAAt(25, 95), img.RGBAAt(35, 95); a != white || b == white {
		t.Errorf("the glyphs of the drawn font are %v and %v", a, b)
	}
	// In the file: the text that stood for what went is out of the
	// resources, the other is there; the form is there once as it was.
	all, pageBytes := everything(t, out), reachable(t, out, 0)
	for _, s := range []string{"AAAA", "BBBB", "DDDD", "41414141", "42424242"} {
		if bytes.Contains(all, []byte(s)) {
			t.Errorf("the file has %q still", s)
		}
	}
	if !bytes.Contains(pageBytes, []byte("EEEE")) || bytes.Count(pageBytes, []byte("FFFF")) != 1 {
		t.Errorf("the page keeps %d of the form's word and the other text %v", bytes.Count(pageBytes, []byte("FFFF")), bytes.Contains(pageBytes, []byte("EEEE")))
	}

	// Redacted again, where nothing is left: the same.
	e = od.Edit()
	if err := e.Redact(0, areas, nil); err != nil {
		t.Fatal(err)
	}
	if again := strings.Join(strings.Fields(textOf(t, editOpen(t, editSave(t, e)), 0).String()), " "); again != got {
		t.Errorf("redacted again, the page reads %q, was %q", again, got)
	}
}

// TestRedactScan redacts a scanned page: an image of the whole page under
// text that is not drawn.
func TestRedactScan(t *testing.T) {
	src := testpdf.File{
		Objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R>>",
			2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100]>>",
			3: "<</Type /Page /Parent 2 0 R /Contents 5 0 R /Resources <</Font <</F1 6 0 R>> /XObject <</Scan 7 0 R>>>>>>",
			5: testpdf.Stream("", []byte("q 100 0 0 100 0 0 cm /Scan Do Q BT 3 Tr /F1 10 Tf 5 50 Td (scanned NAME here) Tj ET")),
			6: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
			7: testpdf.Stream("/Type /XObject /Subtype /Image /Width 50 /Height 50 /ColorSpace /DeviceRGB /BitsPerComponent 8", bytes.Repeat([]byte{255, 0, 0}, 2500)),
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
	doc := editOpen(t, src)
	areas := redactAreas(t, doc, 0, "NAME")
	e := doc.Edit()
	if err := e.Redact(0, areas, nil); err != nil {
		t.Fatal(err)
	}
	out := editSave(t, e)
	od := editOpen(t, out)
	if got := strings.Join(strings.Fields(textOf(t, od, 0).String()), " "); got != "scanned here" {
		t.Errorf("the scan reads %q", got)
	}
	// The picture of the word is gone with the word, and no more of the
	// picture than a sample around it.
	p, _ := od.Page(0)
	img := renderView(t, p)
	b := areas[0].Bounds()
	for y := range 100 {
		for x := range 100 {
			in := float64(x) >= b.X0 && float64(x+1) <= b.X1 && float64(y) >= b.Y0 && float64(y+1) <= b.Y1
			far := float64(x+1) < b.X0-3 || float64(x) > b.X1+3 || float64(y+1) < b.Y0-3 || float64(y) > b.Y1+3
			switch c := img.RGBAAt(x, y); {
			case in && c != white:
				t.Fatalf("the picture has %v at %d, %d, in the area %v", c, x, y, b)
			case far && c != red:
				t.Fatalf("the picture has %v at %d, %d, away from the area %v", c, x, y, b)
			}
		}
	}
	// The picture as it was is not in the file.
	if bytes.Contains(everything(t, out), bytes.Repeat([]byte{255, 0, 0}, 2500)) {
		t.Error("the file has the scan as it was")
	}
}
