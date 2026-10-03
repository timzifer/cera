package fyneform

import (
	"bytes"
	"fmt"
	"image"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/timzifer/cera"
)

// formPDF builds a one-page PDF of 200×100 points whose annotations, and
// fields, are annots (objects 100 on).
func formPDF(annots ...string) []byte {
	var b bytes.Buffer
	offsets := map[int]int{}
	obj := func(n int, body string) {
		offsets[n] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	var refs []string
	for i := range annots {
		refs = append(refs, fmt.Sprintf("%d 0 R", 100+i))
	}
	b.WriteString("%PDF-1.7\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R /AcroForm << /Fields ["+strings.Join(refs, " ")+
		"] /DA (/Helv 0 Tf 0 g) /DR << /Font << /Helv << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >> >> >> >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 100] >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /Annots ["+strings.Join(refs, " ")+"] >>")
	for i, a := range annots {
		obj(100+i, a)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", 100+len(annots))
	for n := 1; n < 100+len(annots); n++ {
		if off, ok := offsets[n]; ok {
			fmt.Fprintf(&b, "%010d 00000 n \n", off)
		} else {
			b.WriteString("0000000000 65535 f \n")
		}
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", 100+len(annots), xref)
	return b.Bytes()
}

func TestProvider(t *testing.T) {
	test.NewTempApp(t)
	doc, err := cera.Open(formPDF(
		"<< /Subtype /Widget /FT /Tx /T (name) /Rect [10 60 110 80] /V (Hello) /MaxLen 8 >>",
		"<< /Subtype /Widget /FT /Btn /T (agree) /Rect [120 60 140 80] /V /Off /AP << /N << /Yes 108 0 R >> >> >>",
		"<< /Subtype /Widget /FT /Btn /Ff 32768 /T (r1) /Rect [10 10 30 30] /V /a /AP << /N << /a 108 0 R >> >> >>",
		"<< /Subtype /Widget /FT /Ch /Ff 131072 /T (combo) /Rect [40 10 100 30] /Opt [(Alpha) (Beta) (Gamma)] >>",
		"<< /Subtype /Widget /FT /Ch /Ff 2097152 /T (list) /Rect [120 5 190 40] /Opt [(x) (y) (z)] >>",
		"<< /Subtype /Widget /FT /Btn /Ff 65536 /T (push) /Rect [150 85 190 95] /MK << /CA (Go) >> >>",
		"<< /Subtype /Widget /FT /Tx /T (rotated) /Rect [100 85 110 95] /MK << /R 90 >> >>",
		"<< /Subtype /Widget /FT /Sig /T (sig) /Rect [0 85 10 95] >>",
	))
	if err != nil {
		t.Fatal(err)
	}
	page, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	form := doc.Form()
	state := form.NewState()
	prov := New()
	var pushed *cera.Widget
	prov.OnPush = func(w *cera.Widget) { pushed = w }
	layer := cera.NewFormLayer(page, state, prov)
	defer layer.Close()
	layer.Update(cera.View{Scale: 2})

	if n := len(prov.Overlay.Objects); n != 6 {
		t.Fatalf("%d widgets shown, want 6 (not the rotated field and the signature)", n)
	}
	widgetOf := func(name string) fyne.CanvasObject {
		t.Helper()
		o := prov.Object(form.Field(name).Widgets[0])
		if o == nil {
			t.Fatalf("%s not shown", name)
		}
		return o
	}
	value := func(name string) cera.Value { return state.Value(form.Field(name)) }

	entry := widgetOf("name").(*widget.Entry)
	if entry.Text != "Hello" || entry.Position() != fyne.NewPos(20, 40) || entry.Size() != fyne.NewSize(200, 40) {
		t.Errorf("entry %q at %v, %v", entry.Text, entry.Position(), entry.Size())
	}
	entry.SetText("World")
	if v := value("name").Text(); v != "World" {
		t.Errorf("name = %q", v)
	}
	entry.SetText("much too long")
	if v := value("name").Text(); v != "World" || entry.Text != "World" {
		t.Errorf("too long: name = %q, entry %q", v, entry.Text)
	}

	check := widgetOf("agree").(*widget.Check)
	test.Tap(check)
	if v := value("agree").State(); v != "Yes" {
		t.Errorf("agree = %q", v)
	}
	if err := state.SetValue(form.Field("agree"), cera.StateValue("Off")); err != nil {
		t.Fatal(err)
	}
	if check.Checked {
		t.Error("check box not updated from the state")
	}

	if !widgetOf("r1").(*widget.Check).Checked {
		t.Error("radio button not on")
	}

	sel := widgetOf("combo").(*widget.Select)
	sel.SetSelected("Gamma")
	if v := value("combo").Selected(); len(v) != 1 || v[0] != 2 {
		t.Errorf("combo = %v", v)
	}

	list := widgetOf("list").(*widget.List)
	list.Select(2)
	list.Select(0)
	if v := value("list").Selected(); len(v) != 2 || v[0] != 0 || v[1] != 2 {
		t.Errorf("list = %v", v)
	}
	list.Select(2)
	if v := value("list").Selected(); len(v) != 1 || v[0] != 0 {
		t.Errorf("list after toggling = %v", v)
	}

	test.Tap(widgetOf("push").(*widget.Button))
	if pushed != form.Field("push").Widgets[0] {
		t.Error("push button")
	}

	// Scrolled away from all but the push button.
	layer.Update(cera.View{Scale: 2, Viewport: image.Rect(300, 0, 400, 20)})
	if n := len(prov.Overlay.Objects); n != 1 || prov.Object(form.Field("push").Widgets[0]) == nil {
		t.Errorf("%d widgets after scrolling", n)
	}
}
