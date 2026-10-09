package cera

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"
)

const helvDR = "/DR << /Font << /Helv << /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >> >> >>"

// formPDF builds a one-page PDF whose page has the annotations annots
// (objects 100 on) and whose catalog has the /AcroForm entries acro.
func formPDF(acro, pageExtra string, annots []string, objs ...string) []byte {
	var refs []string
	for i := range annots {
		refs = append(refs, fmt.Sprintf("%d 0 R", 100+i))
	}
	return buildPDFCatalog("/AcroForm << "+acro+" >>", []string{""},
		"/Annots ["+strings.Join(refs, " ")+"] "+pageExtra, append(annots, objs...)...)
}

var (
	apRed   = stream("/BBox [0 0 10 10]", "1 0 0 rg 0 0 10 10 re f")
	apGreen = stream("/BBox [0 0 10 10]", "0 1 0 rg 0 0 10 10 re f")
)

// sampleForm has a text field, a check box, a radio group of two, a list
// box and a read-only text field under a parent, on a 200×100 page.
func sampleForm(pageExtra string) []byte {
	return formPDF("/Fields [100 0 R 101 0 R 106 0 R 104 0 R 107 0 R] /DA (/Helv 0 Tf 0 g) "+helvDR, pageExtra,
		[]string{
			"<< /Type /Annot /Subtype /Widget /FT /Tx /T (name) /Rect [10 60 110 80] /V (Hello) /MaxLen 10 /F 4" +
				" /MK << /BG [1 1 0] /BC [0 0 1] >> /DA (/Helv 0 Tf 0 0 1 rg) /Q 0 /TU (Your name) >>",
			"<< /Subtype /Widget /FT /Btn /T (agree) /Rect [120 60 140 80] /V /Off /AS /Off /AP << /N << /Yes 108 0 R /Off 109 0 R >> >> >>",
			"<< /Subtype /Widget /Parent 106 0 R /Rect [10 10 30 30] /AS /r /AP << /N << /r 108 0 R /Off 109 0 R >> >> >>",
			"<< /Subtype /Widget /Parent 106 0 R /Rect [40 10 60 30] /AS /Off /AP << /N << /g 108 0 R /Off 109 0 R >> >> >>",
			"<< /Subtype /Widget /FT /Ch /T (pick) /Rect [120 5 190 40] /Opt [[(a) (Alpha)] (Beta) (Gamma)] /V (Beta) /Ff 2097152 /DA (/Helv 10 Tf 0 g) >>",
			"<< /Subtype /Widget /T (street) /Parent 107 0 R /Rect [150 85 190 95] /MK << /R 90 >> >>",
		},
		"<< /FT /Btn /Ff 32768 /T (color) /Kids [102 0 R 103 0 R] /V /r >>",
		"<< /T (addr) /FT /Tx /Kids [105 0 R] /DA (/Helv 12 Tf 1 0 0 rg) /Ff 1 /Q 2 >>",
		apRed, apGreen)
}

func openForm(t *testing.T, data []byte) (*Document, *Page, *Form) {
	t.Helper()
	doc, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	f := doc.Form()
	if f == nil {
		t.Fatal("no form")
	}
	return doc, p, f
}

func TestFormRead(t *testing.T) {
	_, _, f := openForm(t, sampleForm(""))
	var names []string
	for _, fd := range f.Fields {
		names = append(names, fd.Name+":"+fd.Type.String())
	}
	if got, want := strings.Join(names, " "), "name:text agree:checkbox color:radio pick:listbox addr.street:text"; got != want {
		t.Fatalf("fields %q, want %q", got, want)
	}
	name := f.Field("name")
	if name.Alt != "Your name" || name.MaxLen != 10 || name.Saved.Text() != "Hello" || name.Default.Text() != "" {
		t.Errorf("name: %+v", name)
	}
	w := name.Widgets[0]
	a := w.Appearance
	if w.Page != 0 || w.Annotation != 0 || w.Rect != (Rect{10, 60, 110, 80}) || w.Flags != AnnotPrint {
		t.Errorf("name widget: %+v", w)
	}
	if a.FontName != "Helvetica" || a.FontSize != 0 || a.TextColor != (color.RGBA{0, 0, 255, 255}) ||
		a.Background == nil || *a.Background != (color.RGBA{255, 255, 0, 255}) ||
		a.Border == nil || *a.Border != (color.RGBA{0, 0, 255, 255}) || a.BorderWidth != 1 {
		t.Errorf("name appearance: %+v", a)
	}

	radio := f.Field("color")
	if len(radio.Widgets) != 2 || radio.Widgets[0].OnState != "r" || radio.Widgets[1].OnState != "g" || radio.Saved.State() != "r" {
		t.Errorf("radio: %+v", radio)
	}
	if f.Field("agree").Widgets[0].OnState != "Yes" || f.Field("agree").Saved.State() != "Off" {
		t.Errorf("check box: %+v", f.Field("agree"))
	}

	pick := f.Field("pick")
	if len(pick.Options) != 3 || pick.Options[0] != (Option{"a", "Alpha"}) || pick.Options[1] != (Option{"Beta", "Beta"}) {
		t.Errorf("options %v", pick.Options)
	}
	if sel := pick.Saved.Selected(); len(sel) != 1 || sel[0] != 1 || pick.Flags&FfMultiSelect == 0 {
		t.Errorf("pick: %v %b", sel, pick.Flags)
	}

	street := f.Field("addr.street")
	sw := street.Widgets[0]
	if street.Flags&FfReadOnly == 0 || sw.Rotation != 90 || sw.Appearance.Align != AlignRight ||
		sw.Appearance.FontSize != 12 || sw.Appearance.TextColor != (color.RGBA{255, 0, 0, 255}) {
		t.Errorf("inherited: %+v %+v", street, sw)
	}
	if ws := f.PageWidgets(0); len(ws) != 6 || ws[5] != sw {
		t.Errorf("page widgets %v", ws)
	}
}

func TestFormNone(t *testing.T) {
	doc, _ := Open(buildPDF([]string{""}, ""))
	if doc.Form() != nil {
		t.Error("form without /AcroForm")
	}
	var f *Form
	if f.Field("x") != nil || f.PageWidgets(0) != nil {
		t.Error("nil form")
	}
}

func TestFormState(t *testing.T) {
	_, _, f := openForm(t, sampleForm(""))
	s := f.NewState()
	var notified []string
	cancel := s.Subscribe(func(fd *Field) { notified = append(notified, fd.Name) })

	name, agree, radio, pick, street := f.Field("name"), f.Field("agree"), f.Field("color"), f.Field("pick"), f.Field("addr.street")
	for _, c := range []struct {
		f    *Field
		v    Value
		want error
	}{
		{name, TextValue("0123456789x"), ErrInvalidValue},
		{name, StateValue("Yes"), ErrInvalidValue},
		{street, TextValue("x"), ErrReadOnly},
		{agree, StateValue("Maybe"), ErrInvalidValue},
		{pick, ChoiceValue(5), ErrInvalidValue},
		{pick, TextValue("Delta"), ErrInvalidValue},
		{nil, TextValue(""), ErrInvalidValue},
	} {
		if err := s.SetValue(c.f, c.v); !errors.Is(err, c.want) {
			t.Errorf("SetValue(%v, %+v) = %v, want %v", c.f, c.v, err, c.want)
		}
	}
	if len(notified) != 0 || len(s.Changed()) != 0 {
		t.Fatalf("rejected values changed something: %v", notified)
	}

	must := func(f *Field, v Value) {
		t.Helper()
		if err := s.SetValue(f, v); err != nil {
			t.Fatal(err)
		}
	}
	must(name, TextValue("Hello")) // unchanged: no notification
	must(name, TextValue("World"))
	must(agree, TextValue("Yes"))
	must(radio, StateValue("g"))
	must(pick, ChoiceValue(2, 0))
	if v := s.Value(pick); len(v.Selected()) != 2 || v.Selected()[0] != 0 {
		t.Errorf("pick %v", v.Selected())
	}
	must(pick, TextValue("Alpha")) // by text
	if v := s.Value(pick); len(v.Selected()) != 1 || v.Selected()[0] != 0 {
		t.Errorf("pick by text %v", v.Selected())
	}
	if got := strings.Join(notified, " "); got != "name agree color pick pick" {
		t.Errorf("notified %q", got)
	}
	var changed []string
	for _, fd := range s.Changed() {
		changed = append(changed, fd.Name)
	}
	if got := strings.Join(changed, " "); got != "name agree color pick" {
		t.Errorf("changed %q", got)
	}

	notified = nil
	s.Reset()
	if s.Value(name).Text() != "" || s.Value(radio).State() != "Off" || len(s.Value(pick).Selected()) != 0 {
		t.Errorf("reset: %v %v %v", s.Value(name), s.Value(radio), s.Value(pick))
	}
	if got := strings.Join(notified, " "); got != "name agree color pick" { // agree: Yes → Off
		t.Errorf("reset notified %q", got)
	}
	cancel()
	must(name, TextValue("again"))
	if len(notified) != 4 {
		t.Error("notified after cancel")
	}
}

// inkIn counts the pixels in r that are neither white nor the colours
// listed.
func inkIn(img *image.RGBA, r image.Rectangle, not ...color.RGBA) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
	pixels:
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if c == white {
				continue
			}
			for _, o := range not {
				if c == o {
					continue pixels
				}
			}
			n++
		}
	}
	return n
}

var yellow = color.RGBA{255, 255, 0, 255}

func TestFormRenderValues(t *testing.T) {
	_, p, f := openForm(t, sampleForm(""))
	s := f.NewState()
	render := func(s *FormState) (*image.RGBA, Stats) {
		t.Helper()
		var st Stats
		img := image.NewRGBA(p.Bounds(1))
		if err := p.Render(context.Background(), img, RenderOptions{Background: white, Form: s, Stats: &st}); err != nil {
			t.Fatal(err)
		}
		return img, st
	}
	img, st := render(nil)
	if st.Errors != 0 || len(st.Unsupported) != 0 {
		t.Errorf("errors %d, unsupported %v", st.Errors, st.Unsupported)
	}
	// The text field has no /AP: generated, yellow with a blue border
	// and the saved text at its left.
	assertPixel(t, img, 10, 30, blue)
	assertPixel(t, img, 100, 30, yellow)
	if n := inkIn(img, image.Rect(12, 22, 60, 38), yellow); n < 20 {
		t.Errorf("saved text: %d pixels of ink", n)
	}
	if n := inkIn(img, image.Rect(62, 22, 108, 38), yellow); n != 0 {
		t.Errorf("ink right of the saved text: %d", n)
	}
	assertPixel(t, img, 130, 30, green) // check box off
	assertPixel(t, img, 20, 80, red)    // radio r on
	assertPixel(t, img, 50, 80, green)
	// The list box shows Beta selected.
	if n := inkIn(img, image.Rect(121, 61, 189, 94)); n < 20 {
		t.Errorf("list box: %d pixels of ink", n)
	}
	sel := color.RGBA{153, 193, 220, 255}
	if c := img.RGBAAt(185, 100-40+16); c != sel {
		t.Errorf("selected row %v, want %v", c, sel)
	}

	must := func(f *Field, v Value) {
		t.Helper()
		if err := s.SetValue(f, v); err != nil {
			t.Fatal(err)
		}
	}
	must(f.Field("name"), TextValue("Hello Hi!!"))
	must(f.Field("agree"), StateValue("Yes"))
	must(f.Field("color"), StateValue("g"))
	img, st = render(s)
	if !st.Reused {
		t.Error("values interpreted the page again")
	}
	if n := inkIn(img, image.Rect(62, 22, 108, 38), yellow); n < 20 {
		t.Errorf("new text: %d pixels of ink", n)
	}
	assertPixel(t, img, 130, 30, red) // the /Yes appearance
	assertPixel(t, img, 20, 80, green)
	assertPixel(t, img, 50, 80, red)

	// The widget lists are cached by value.
	p.mu.Lock()
	cached := p.wl[0]
	p.mu.Unlock()
	render(s)
	p.mu.Lock()
	if p.wl[0] != cached || cached == nil {
		t.Error("widget list recorded again")
	}
	p.mu.Unlock()

	// The skip function still applies to widgets with values.
	img = image.NewRGBA(p.Bounds(1))
	_ = p.Render(context.Background(), img, RenderOptions{Background: white, Form: s, SkipAnnotation: func(i int) bool { return i == 0 }})
	assertPixel(t, img, 100, 30, white)

	// Back to the saved value: drawn from the page's list.
	must(f.Field("agree"), StateValue("Off"))
	img, _ = render(s)
	assertPixel(t, img, 130, 30, green)
}

func TestFormText(t *testing.T) {
	_, p, f := openForm(t, sampleForm(""))
	s := f.NewState()
	if err := s.SetValue(f.Field("name"), TextValue("Grace")); err != nil {
		t.Fatal(err)
	}
	td := &textDevice{}
	if err := p.RunWith(context.Background(), td, RunOptions{Form: s}); err != nil {
		t.Fatal(err)
	}
	if txt := (&PageText{Chars: td.chars}).String(); !strings.Contains(txt, "Grace") || strings.Contains(txt, "Hello") {
		t.Errorf("text %q", txt)
	}
	td = &textDevice{}
	_ = p.RunWith(context.Background(), td, RunOptions{})
	if txt := (&PageText{Chars: td.chars}).String(); !strings.Contains(txt, "Hello") || !strings.Contains(txt, "Beta") {
		t.Errorf("saved text %q", txt)
	}
}

func TestFormNeedAppearances(t *testing.T) {
	ap := stream("/BBox [0 0 10 10]", "0 0 1 rg 0 0 10 10 re f")
	field := "<< /Subtype /Widget /FT /Tx /T (t) /Rect [10 10 90 40] /V (Text) /DA (/Helv 0 Tf 0 g) /AP << /N 101 0 R >> >>"
	for _, need := range []bool{false, true} {
		data := formPDF(fmt.Sprintf("/Fields [100 0 R] /NeedAppearances %v %s", need, helvDR), "", []string{field}, ap)
		img, _ := renderAnnots(t, data, RenderOptions{})
		if got := img.RGBAAt(80, 65) == blue; got == need {
			t.Errorf("NeedAppearances %v: appearance drawn %v", need, got)
		}
		if need {
			if n := inkIn(img, image.Rect(12, 62, 60, 88)); n < 20 {
				t.Errorf("generated text: %d pixels", n)
			}
		}
	}
}

func TestFormGenerated(t *testing.T) {
	// Fields without appearances of every kind are drawn without errors,
	// each inside its rectangle.
	data := formPDF("/Fields [100 0 R 101 0 R 102 0 R 103 0 R 104 0 R 105 0 R 106 0 R] /DA (/Helv 0 Tf 0 g) "+helvDR, "",
		[]string{
			"<< /Subtype /Widget /FT /Tx /T (multi) /Ff 4096 /Rect [5 50 60 95] /V (one two three four five six seven) /MK << /BC [0] >> /BS << /S /B /W 1 >> >>",
			"<< /Subtype /Widget /FT /Tx /T (comb) /Ff 16777216 /MaxLen 4 /Rect [65 80 125 95] /V (1234) /MK << /BC [0] >> >>",
			"<< /Subtype /Widget /FT /Btn /T (cb) /Rect [130 80 145 95] /V /Yes /MK << /CA (8) /BC [0] /BG [1] >> >>",
			"<< /Subtype /Widget /FT /Btn /Ff 32768 /T (rb) /Rect [150 80 165 95] /V /Yes /MK << /BC [0] /BG [1] >> >>",
			"<< /Subtype /Widget /FT /Ch /Ff 131072 /T (combo) /Rect [65 55 125 70] /Opt [(x) (y)] /V (y) /MK << /BC [0] >> /BS << /S /U >> >>",
			"<< /Subtype /Widget /FT /Btn /Ff 65536 /T (push) /Rect [130 55 190 70] /MK << /CA (OK) /BG [0.8] /BC [0] >> /BS << /S /I >> >>",
			"<< /Subtype /Widget /FT /Tx /T (fallback) /Rect [5 5 120 40] /V (No such font) /DA (/Nope 0 Tf 0 0 1 rg) >>",
		})
	_, p, f := openForm(t, data)
	var st Stats
	img := image.NewRGBA(p.Bounds(1))
	if err := p.Render(context.Background(), img, RenderOptions{Background: white, Stats: &st}); err != nil {
		t.Fatal(err)
	}
	if st.Errors != 0 || len(st.Unsupported) != 0 {
		t.Errorf("errors %d, unsupported %v", st.Errors, st.Unsupported)
	}
	total := inkIn(img, img.Bounds())
	for _, fd := range f.Fields {
		w := fd.Widgets[0]
		box := p.widgetBox(w, 1)
		n := inkIn(img, box)
		if n < 10 {
			t.Errorf("%s: %d pixels of ink", fd.Name, n)
		}
		total -= n
	}
	if total != 0 {
		t.Errorf("%d pixels of ink outside the fields", total)
	}
	// Multiline text wraps onto several lines.
	td := &textDevice{}
	_ = p.RunWith(context.Background(), td, RunOptions{})
	if txt := (&PageText{Chars: td.chars}).String(); strings.Count(txt, "\n") < 3 || !strings.Contains(txt, "OK") || !strings.Contains(txt, "No such font") {
		t.Errorf("text %q", txt)
	}
}

func TestFormXFA(t *testing.T) {
	data := formPDF("/Fields [] /XFA [(template) 101 0 R]", "", []string{"<< /Subtype /Square /Rect [0 0 1 1] >>"}, stream("", "<xdp/>"))
	_, st := renderAnnots(t, data, RenderOptions{})
	if st.Unsupported["xfa"] != 1 {
		t.Errorf("unsupported %v", st.Unsupported)
	}
}

// fakeProvider records what a FormLayer asks of it.
type fakeProvider struct {
	shown map[*Widget]WidgetPlacement
	calls []string
}

func (fp *fakeProvider) Supports(w *Widget) bool { return w.Field.Type == FieldText }

func (fp *fakeProvider) Show(w *Widget, p WidgetPlacement) {
	fp.shown[w] = p
	fp.calls = append(fp.calls, "show "+w.Field.Name)
}

func (fp *fakeProvider) Hide(w *Widget) {
	delete(fp.shown, w)
	fp.calls = append(fp.calls, "hide "+w.Field.Name)
}

func TestFormLayer(t *testing.T) {
	_, p, f := openForm(t, sampleForm(""))
	s := f.NewState()
	fp := &fakeProvider{shown: map[*Widget]WidgetPlacement{}}
	l := NewFormLayer(p, s, fp)
	name, street := f.Field("name").Widgets[0], f.Field("addr.street").Widgets[0]

	l.Update(View{Scale: 2, Origin: image.Pt(5, 7)})
	if got := strings.Join(fp.calls, ","); got != "show name,show addr.street" {
		t.Errorf("calls %q", got)
	}
	pl := fp.shown[name]
	if pl.Rect != image.Rect(25, 47, 225, 87) || pl.Scale != 2 || pl.State != s || pl.Rotation != 0 || pl.Focus {
		t.Errorf("placement %+v", pl)
	}
	if r := fp.shown[street].Rotation; r != 270 {
		t.Errorf("rotation %d", r)
	}
	if !l.Skip(0) || l.Skip(1) || !l.Skip(5) || l.Skip(42) {
		t.Error("skip")
	}

	// Nothing changed: no calls. A value changed: its widget again.
	fp.calls = nil
	l.Update(View{Scale: 2, Origin: image.Pt(5, 7)})
	if len(fp.calls) != 0 {
		t.Errorf("calls %v", fp.calls)
	}
	_ = s.SetValue(f.Field("name"), TextValue("x"))
	if got := strings.Join(fp.calls, ","); got != "show name" {
		t.Errorf("calls after SetValue %q", got)
	}

	// Scrolled so that only the street is in view.
	fp.calls = nil
	l.Update(View{Scale: 2, Viewport: image.Rect(290, 0, 400, 40)})
	if got := strings.Join(fp.calls, ","); got != "hide name,show addr.street" {
		t.Errorf("calls after scrolling %q", got)
	}

	fp.calls = nil
	l.Focus(street)
	if !fp.shown[street].Focus || len(fp.calls) != 1 {
		t.Errorf("focus: %v", fp.calls)
	}
	l.Close()
	if len(fp.shown) != 0 {
		t.Errorf("shown after close: %v", fp.shown)
	}
	fp.calls = nil
	_ = s.SetValue(f.Field("name"), TextValue("y"))
	if len(fp.calls) != 0 {
		t.Error("called after close")
	}
}

// TestFormLayerPlacement checks that a placement covers exactly the
// pixels its widget changes in the page image, for a rotated page and a
// scale that puts the widget's edges between pixels.
func TestFormLayerPlacement(t *testing.T) {
	for _, rot := range []int{0, 90, 180, 270} {
		_, p, f := openForm(t, sampleForm(fmt.Sprintf("/Rotate %d", rot)))
		fp := &fakeProvider{shown: map[*Widget]WidgetPlacement{}}
		l := NewFormLayer(p, f.NewState(), fp)
		const scale = 1.37
		l.Update(View{Scale: scale})
		w := f.Field("name").Widgets[0]
		render := func(skip func(int) bool) *image.RGBA {
			img := image.NewRGBA(p.Bounds(scale))
			if err := p.Render(context.Background(), img, RenderOptions{Scale: scale, Background: white, SkipAnnotation: skip}); err != nil {
				t.Fatal(err)
			}
			return img
		}
		with, without := render(nil), render(l.Skip)
		var diff image.Rectangle
		b := with.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if with.RGBAAt(x, y) != without.RGBAAt(x, y) && !image.Pt(x, y).In(fp.shown[f.Field("addr.street").Widgets[0]].Rect) {
					diff = diff.Union(image.Rect(x, y, x+1, y+1))
				}
			}
		}
		if got := fp.shown[w].Rect; got != diff {
			t.Errorf("rotate %d: placement %v, pixels changed %v", rot, got, diff)
		}
	}
}

func TestFormLayerTabOrder(t *testing.T) {
	_, p, f := openForm(t, sampleForm("/Tabs /R"))
	l := NewFormLayer(p, f.NewState(), &fakeProvider{shown: map[*Widget]WidgetPlacement{}})
	var order []string
	for w := l.Next(nil); w != nil; w = l.Next(w) {
		order = append(order, fmt.Sprintf("%s@%v", w.Field.Name, w.Rect.X0))
		if len(order) > 10 {
			break
		}
	}
	if got := strings.Join(order, " "); got != "name@10 agree@120 pick@120 color@10 color@40" {
		t.Errorf("tab order %q", got)
	}
}

// TestFormRenderValuesLarge draws a changed value over a page large enough
// to be filled in one streaming pass: the widget's list, drawn over the
// page, must not fill it again.
func TestFormRenderValuesLarge(t *testing.T) {
	_, p, f := openForm(t, sampleForm("/MediaBox [0 0 1836 2376]"))
	s := f.NewState()
	if err := s.SetValue(f.Field("name"), TextValue("Other")); err != nil {
		t.Fatal(err)
	}
	const scale = 0.5 // 918×1188: past streamFill
	img := image.NewRGBA(p.Bounds(scale))
	if err := p.Render(context.Background(), img, RenderOptions{Scale: scale, Background: white, Form: s, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	y := func(v float64) int { return int((2376 - v) * scale) }
	assertPixel(t, img, int(130*scale), y(70), green)  // the check box, page content
	assertPixel(t, img, 600, 100, white)               // the background
	assertPixel(t, img, int(100*scale), y(70), yellow) // the changed field
}
