package cera

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
	"github.com/timzifer/stilus"
)

// actionFile is a form with fields below a node, a note, a check box the
// document hides, and push buttons with every action.
func actionFile() []byte {
	button := func(n int, action string) string {
		return "<</Type /Annot /Subtype /Widget /FT /Btn /Ff 65536 /T (b" + string(rune('0'+n)) + ") /Rect [10 10 20 20] " + action + ">>"
	}
	return testpdf.File{
		Objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R /AcroForm <</Fields [10 0 R 13 0 R 14 0 R 15 0 R 20 0 R 21 0 R 22 0 R 23 0 R 24 0 R 25 0 R 26 0 R 27 0 R 28 0 R 29 0 R]" +
				" /DA (/Helv 10 Tf 0 g) /DR <</Font <</Helv 9 0 R>>>>>>>>",
			2:  "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 300 200]>>",
			3:  "<</Type /Page /Parent 2 0 R /Annots [11 0 R 12 0 R 13 0 R 14 0 R 16 0 R 20 0 R 21 0 R 22 0 R 23 0 R 24 0 R 25 0 R 26 0 R 27 0 R 28 0 R 29 0 R]>>",
			4:  "<</Type /Page /Parent 2 0 R>>",
			9:  "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
			10: "<</T (address) /Kids [11 0 R 12 0 R]>>",
			11: "<</Type /Annot /Subtype /Widget /Parent 10 0 R /FT /Tx /T (street) /V (Main St) /DV (a street) /Rect [10 160 140 180]>>",
			12: "<</Type /Annot /Subtype /Widget /Parent 10 0 R /FT /Tx /T (city) /V (Bonn) /Rect [150 160 290 180]>>",
			13: "<</Type /Annot /Subtype /Widget /FT /Tx /T (note) /V (a & b) /Rect [10 100 140 140] /MK <</BG [1 0 0]>>>>",
			14: "<</Type /Annot /Subtype /Widget /FT /Tx /Ff 4 /T (internal) /V (x) /Rect [150 130 290 140]>>",
			// A field with a widget below it, hidden.
			15: "<</FT /Tx /T (more) /Kids [16 0 R]>>",
			16: "<</Type /Annot /Subtype /Widget /Parent 15 0 R /F 2 /Rect [150 100 290 125] /MK <</BG [0 0 1]>>>>",

			20: button(0, "/A <</S /ResetForm>>"),
			21: button(1, "/A <</S /ResetForm /Fields [(address.street) 13 0 R]>>"),
			22: button(2, "/A <</S /ResetForm /Fields [10 0 R] /Flags 1>>"),
			23: button(3, "/A <</S /Hide /T (note) /Next [<</S /Hide /T [16 0 R (nothing)] /H false>> <</S /Named /N /NextPage>>]>>"),
			24: button(4, "/A <</S /Hide /T 15 0 R>>"),
			25: button(5, "/A <</S /SubmitForm /F <</FS /URL /F (https://example.com/submit)>> /Fields [(address) (note) (internal)] /Flags 4>>"),
			26: button(6, "/A <</S /SubmitForm /F (https://example.com/x) /Fields [(note)] /Flags 33>>"),
			27: button(7, "/AA <</U <</S /JavaScript /JS (app.alert\\(1\\);) /Next <</S /URI /URI (https://example.com/)>>>>>>"),
			28: button(8, "/A <</S /SubmitForm /F (https://example.com/pdf) /Flags 256>>"),
			29: button(9, "/A <</S /GoTo /D [4 0 R /Fit] /Next <</S /SubmitForm /F (https://example.com/fdf) /Flags 10>>>>"),
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

func TestWidgetActions(t *testing.T) {
	doc := editOpen(t, actionFile())
	form := doc.Form()
	acts := func(n int) []WidgetAction {
		f := form.Field("b" + string(rune('0'+n)))
		if f == nil || len(f.Widgets) != 1 {
			t.Fatalf("button %d: %+v", n, f)
		}
		return f.Widgets[0].Actions
	}
	kinds := func(as []WidgetAction) string {
		var k []string
		for _, a := range as {
			k = append(k, a.Kind)
		}
		return strings.Join(k, " ")
	}
	for n, want := range []string{"ResetForm", "ResetForm", "ResetForm", "Hide Hide Named", "Hide", "SubmitForm", "SubmitForm", "JavaScript URI", "SubmitForm", "GoTo SubmitForm"} {
		if got := kinds(acts(n)); got != want {
			t.Errorf("button %d does %q, want %q", n, got, want)
		}
	}
	if as := form.Field("note").Widgets[0].Actions; as != nil {
		t.Errorf("a text field does %v", as)
	}
	values := func(s *FormState) string {
		return s.Value(form.Field("address.street")).Text() + "|" + s.Value(form.Field("address.city")).Text() + "|" + s.Value(form.Field("note")).Text()
	}
	change := func() *FormState {
		s := form.NewState()
		for _, name := range []string{"address.street", "address.city", "note"} {
			if err := s.SetValue(form.Field(name), TextValue("changed")); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}

	// Resetting: all, those named, all but those named.
	for n, want := range []string{"a street||", "a street|changed|", "changed|changed|"} {
		s := change()
		told := 0
		s.Subscribe(func(*Field) { told++ })
		if a := acts(n)[0]; !s.Do(a) || values(s) != want {
			t.Errorf("button %d: fields %v, exclude %v: %q, want %q", n, a.Fields, a.Exclude, values(s), want)
		}
		if told == 0 {
			t.Errorf("button %d: no subscriber told", n)
		}
	}

	// Hiding and showing, and what the page shows then.
	s := form.NewState()
	note, more := form.Field("note").Widgets[0], form.Field("more").Widgets[0]
	if s.Hidden(note) || !s.Hidden(more) || (*FormState)(nil).Hidden(more) != true {
		t.Fatal("what is hidden to begin with")
	}
	p, _ := doc.Page(0)
	render := func() *image.RGBA {
		dst := image.NewRGBA(p.Bounds(1))
		if err := p.Render(context.Background(), dst, RenderOptions{Scale: 1, Background: white, Workers: 1, Form: s}); err != nil {
			t.Fatal(err)
		}
		return dst
	}
	blue := color.RGBA{0, 0, 255, 255}
	// The note at [10 100 140 140], the hidden widget at [150 100 290 125],
	// of a page 200 high.
	check := func(what string, wantNote, wantMore color.RGBA) {
		t.Helper()
		img := render()
		if a, b := img.RGBAAt(135, 65), img.RGBAAt(285, 80); a != wantNote || b != wantMore {
			t.Errorf("%s: the note is %v, want %v; the hidden widget is %v, want %v", what, a, wantNote, b, wantMore)
		}
	}
	check("as the document is", red, white)
	told := 0
	s.Subscribe(func(*Field) { told++ })
	for _, a := range acts(3) {
		if did := s.Do(a); did != (a.Kind == "Hide") {
			t.Errorf("Do of %s: %v", a.Kind, did)
		}
	}
	if !s.Hidden(note) || s.Hidden(more) || told != 2 {
		t.Errorf("after the button: the note hidden %v, the other %v, %d told", s.Hidden(note), s.Hidden(more), told)
	}
	check("after the button", white, blue)
	// With a value changed as well, and through a device that is not a
	// display list.
	if err := s.SetValue(form.Field("more"), TextValue("now shown")); err != nil {
		t.Fatal(err)
	}
	check("with a value", white, blue)
	dst := image.NewRGBA(p.Bounds(1))
	fillRegion(dst, dst.Bounds(), white)
	if err := p.RunWith(context.Background(), &RasterDevice{C: stilus.NewCanvas(dst)}, RunOptions{Scale: 1, Form: s}); err != nil {
		t.Fatal(err)
	}
	if a, b := dst.RGBAAt(135, 65), dst.RGBAAt(285, 80); a != white || b != blue {
		t.Errorf("a run: the note is %v, the hidden widget %v", a, b)
	}
	// Back: by its field as an object, and by hand.
	if a := acts(4)[0]; !s.Do(a) || !s.Hidden(more) {
		t.Errorf("hiding a field named as an object: %+v", a)
	}
	s.SetHidden(note, false)
	if s.Hidden(note) || len(s.hidden()) != 0 || told != 5 {
		t.Errorf("shown again: %v, %d told, %v", s.Hidden(note), told, s.hidden())
	}
	s.Reset()
	check("as it was", red, white)
	// A layer of native widgets places what is shown, and takes away what
	// is hidden then.
	fp := &fakeProvider{shown: map[*Widget]WidgetPlacement{}}
	layer := NewFormLayer(p, s, fp)
	layer.Update(View{Scale: 1})
	if _, ok := fp.shown[note]; !ok || len(fp.shown) != 4 {
		t.Errorf("the layer shows %d widgets, the note among them %v", len(fp.shown), ok)
	}
	s.SetHidden(note, true)
	s.SetHidden(more, false)
	layer.Update(View{Scale: 1})
	_, noteShown := fp.shown[note]
	_, moreShown := fp.shown[more]
	if noteShown || !moreShown {
		t.Errorf("after hiding the note and showing the other: %v, %v", noteShown, moreShown)
	}
	// The focus passes the hidden note, and reaches the widget shown.
	next := layer.Next(form.Field("address.city").Widgets[0])
	if next == nil || next.Field.Name != "internal" || layer.Next(next) != more {
		t.Errorf("the focus goes to %v after the city", next)
	}

	// Where a button leads.
	if as := acts(3); as[2].Target == nil || as[2].Target.NamedAction != "NextPage" {
		t.Errorf("named action %+v", as[2])
	}
	if as := acts(7); as[0].Script != "app.alert(1);" || as[1].Target == nil || as[1].Target.URI != "https://example.com/" {
		t.Errorf("script and address %+v", as)
	}
	if as := acts(9); as[0].Target == nil || as[0].Target.Page != 1 || as[0].Target.Fit != "Fit" {
		t.Errorf("destination %+v", as[0])
	}

	// What a button submits.
	s = form.NewState()
	a := acts(5)[0]
	body, typ, err := s.Submission(a)
	if a.URL != "https://example.com/submit" || a.Format != "HTML" || a.Get || err != nil || typ != "application/x-www-form-urlencoded" ||
		string(body) != "address.street=Main+St&address.city=Bonn&note=a+%26+b" {
		t.Errorf("HTML submission to %s: %q, %s, %v", a.URL, body, typ, err)
	}
	a = acts(6)[0]
	body, typ, err = s.Submission(a)
	if a.Format != "XFDF" || !a.Exclude || err != nil || typ != "application/vnd.adobe.xfdf" || strings.Contains(string(body), "note") ||
		!strings.Contains(string(body), `<field name="city"><value>Bonn</value></field>`) || strings.Contains(string(body), "internal") {
		t.Errorf("XFDF submission: %s, %s, %v", body, typ, err)
	}
	// As FDF, with the fields that have no value.
	a = acts(9)[1]
	body, typ, err = s.Submission(a)
	if a.Format != "FDF" || a.Get || err != nil || typ != "application/vnd.fdf" || !bytes.HasPrefix(body, []byte("%FDF-1.2")) ||
		!bytes.Contains(body, []byte("<</T (more) /V ()>>")) || !bytes.Contains(body, []byte("<</T (street) /V (Main St)>>")) {
		t.Errorf("FDF submission: %s, %s, %v", body, typ, err)
	}
	if _, _, err := s.Submission(acts(8)[0]); !errors.Is(err, ErrSubmitDocument) {
		t.Errorf("submitting the document: %v", err)
	}
	if _, _, err := s.Submission(acts(0)[0]); err == nil {
		t.Error("a reset was submitted")
	}
}
