package cera

import (
	"bytes"
	"errors"
	"image/color"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// formString lists the fields of a form: name, type, value and where its
// widgets are.
func formString(d *Document) string {
	var b strings.Builder
	for _, f := range d.Form().fieldsOf() {
		b.WriteString(f.Name + " " + f.Type.String())
		switch v := f.Saved; v.Kind() {
		case ValueText:
			b.WriteString(" (" + v.Text() + ")")
		case ValueState:
			b.WriteString(" /" + v.State())
		case ValueChoice:
			for _, i := range v.Selected() {
				b.WriteString(" [" + f.Options[i].Export + "=" + f.Options[i].Text + "]")
			}
		}
		for _, w := range f.Widgets {
			b.WriteString(" p" + string(rune('0'+w.Page)))
			if w.OnState != "" {
				b.WriteString(":" + w.OnState)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// TestAddField makes a form in a document that has none.
func TestAddField(t *testing.T) {
	src := viewFile()
	doc := editOpen(t, src)
	e := doc.Edit()
	boxed := Appearance{Background: &yellow, Border: &black, BorderWidth: 2}
	for _, c := range []struct {
		page int
		rect Rect
		f    NewField
	}{
		{1, Rect{20, 20, 150, 40}, NewField{Name: "address.street", Type: FieldText, Value: TextValue("Main St"), Alt: "Street", MaxLen: 40, Look: boxed}},
		{1, Rect{160, 20, 280, 40}, NewField{Name: "address.city", Type: FieldText, Flags: FfRequired}},
		{1, Rect{20, 50, 40, 70}, NewField{Name: "agree", Type: FieldCheckBox, Value: StateValue("Yes"), Look: boxed}},
		{1, Rect{20, 80, 40, 100}, NewField{Name: "color", Type: FieldRadio, OnState: "red", Value: StateValue("blue"), Look: boxed}},
		{1, Rect{50, 80, 70, 100}, NewField{Name: "color", OnState: "blue", Look: boxed}},
		{0, Rect{250, 150, 270, 170}, NewField{Name: "color", Type: FieldRadio}},
		{1, Rect{20, 110, 150, 130}, NewField{Name: "country", Type: FieldComboBox, Options: []Option{{"DE", "Deutschland"}, {Export: "FR"}}, Value: ChoiceValue(1)}},
		{1, Rect{160, 50, 280, 130}, NewField{Name: "langs", Type: FieldListBox, Flags: FfMultiSelect, Options: []Option{{"de", "Deutsch"}, {"en", "English"}, {"fr", "Fran\u00e7ais"}}, Value: ChoiceValue(0, 2)}},
		{1, Rect{20, 140, 100, 160}, NewField{Name: "send", Type: FieldPushButton, Look: Appearance{Caption: "Send", Border: &black, Background: &yellow}}},
		{1, Rect{120, 140, 280, 190}, NewField{Name: "sig", Type: FieldSignature}},
	} {
		if err := e.AddField(c.page, c.rect, c.f); err != nil {
			t.Fatalf("%s: %v", c.f.Name, err)
		}
	}
	want := `address.street text (Main St) p1
address.city text () p1
agree checkbox /Yes p1:Yes
color radio /blue p1:red p1:blue p0:3
country combobox [FR=FR] p1
langs listbox [de=Deutsch] [fr=Français] p1
send pushbutton p1
sig signature p1
`
	upd := editUpdate(t, e, src)
	saved := editSave(t, e)
	for name, out := range map[string][]byte{"update": upd, "save": saved} {
		od := editOpen(t, out)
		if got := formString(od); got != want {
			t.Errorf("%s:\n%s\nwant:\n%s", name, got, want)
		}
		form := od.Form()
		street := form.Field("address.street")
		if street.Alt != "Street" || street.MaxLen != 40 || form.Field("address.city").Flags != FfRequired ||
			form.Field("langs").Flags&FfMultiSelect == 0 || form.Field("color").Flags&FfRadio == 0 {
			t.Errorf("%s: what the fields are is not what was asked for", name)
		}
		// Where the page shows it, and how.
		p, _ := od.Page(1)
		w := street.Widgets[0]
		if got := QuadOf(w.Rect).Transform(p.DisplayMatrix()).Bounds(); !sameRect(got, Rect{20, 20, 150, 40}) {
			t.Errorf("%s: the street is at %v", name, got)
		}
		if a := w.Appearance; a.Background == nil || *a.Background != yellow || a.Border == nil || a.BorderWidth != 2 || a.FontName != "Helvetica" {
			t.Errorf("%s: appearance %+v", name, a)
		}
		img := renderView(t, p)
		for at, want := range map[[2]int]color.RGBA{{21, 30}: black, {140, 25}: yellow, {10, 30}: white, {200, 30}: white, {24, 54}: yellow, {21, 60}: black} {
			if got := img.RGBAAt(at[0], at[1]); got != want {
				t.Errorf("%s: pixel %v is %v, want %v", name, at, got, want)
			}
		}
		// The button has its background, shaded for its bevel or not.
		if c := img.RGBAAt(60, 150); c.B != 0 || c.R < 200 || c.R != c.G {
			t.Errorf("%s: the button is %v", name, c)
		}
		// The check box has its mark, the radio button that is on its dot,
		// the other none; and the text of the street is drawn.
		dark := func(r Rect) (n int) {
			for y := int(r.Y0); y < int(r.Y1); y++ {
				for x := int(r.X0); x < int(r.X1); x++ {
					if c := img.RGBAAt(x, y); c.R < 100 && c.G < 100 {
						n++
					}
				}
			}
			return n
		}
		if a, b, c, d := dark(Rect{24, 54, 36, 66}), dark(Rect{24, 84, 36, 96}), dark(Rect{54, 84, 66, 96}), dark(Rect{24, 24, 146, 36}); a < 10 || b != 0 || c < 10 || d < 20 {
			t.Errorf("%s: %d dark pixels in the check box, %d and %d in the radio buttons, %d in the text field", name, a, b, c, d)
		}
		// The form takes values as any other.
		state := form.NewState()
		for f, v := range map[string]Value{"color": StateValue("3"), "address.city": TextValue("K\u00f6ln"), "country": TextValue("Deutschland"), "langs": ChoiceValue(1)} {
			if err := state.SetValue(form.Field(f), v); err != nil {
				t.Errorf("%s: %s: %v", name, f, err)
			}
		}
		ne := od.Edit()
		if err := ne.SetFields(state); err != nil {
			t.Fatal(err)
		}
		if got := formString(editOpen(t, editSave(t, ne))); !strings.Contains(got, "color radio /3 ") || !strings.Contains(got, "address.city text (Köln)") ||
			!strings.Contains(got, "country combobox [DE=Deutschland]") {
			t.Errorf("%s: after filling in:\n%s", name, got)
		}
	}
	sameAsSave(t, e, upd)

	// The signature field is signed.
	ada := ecSigner(t, "Ada")
	opt := signOptions(ada)
	opt.Field = "sig"
	sd := editOpen(t, signWith(t, editOpen(t, upd).Edit(), upd, opt))
	if s, _ := intact(t, sd, 0, ada); s.Field.Name != "sig" || len(sd.Form().Fields) != 8 {
		t.Errorf("signed the field %q of %d", s.Field.Name, len(sd.Form().Fields))
	}
	// Not in the update that adds it.
	if err := e.Sign(&bytes.Buffer{}, opt); err == nil {
		t.Error("signed with fields added in the same update")
	}
}

// TestAddFieldToForm adds fields and widgets to a form, and removes
// fields.
func TestAddFieldToForm(t *testing.T) {
	src := dataForm()
	doc := editOpen(t, src)
	e := doc.Edit()
	// Below a node the form has; one more widget of a field that is its
	// own widget, with the value it has; and a field that goes.
	if err := e.AddField(0, Rect{160, 20, 290, 40}, NewField{Name: "address.zip", Type: FieldText, Value: TextValue("12345")}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddField(0, Rect{160, 110, 290, 130}, NewField{Name: "fixed", Look: Appearance{Background: &yellow}}); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveField("agree"); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveField("address.city"); err != nil {
		t.Fatal(err)
	}
	// Added and taken back.
	if err := e.AddField(0, Rect{160, 200, 290, 220}, NewField{Name: "gone", Type: FieldText}); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveField("gone"); err != nil {
		t.Fatal(err)
	}
	want := `address.street text (Main St) p0
address.zip text (12345) p0
secret text (pw) p0
fixed text (const) p0 p0
langs listbox [de=de] p0
sig signature p0
`
	upd := editUpdate(t, e, src)
	for name, out := range map[string][]byte{"update": upd, "save": editSave(t, e)} {
		od := editOpen(t, out)
		if got := formString(od); got != want {
			t.Errorf("%s:\n%s\nwant:\n%s", name, got, want)
		}
		checkFieldTree(t, name, od)
		// The page has the widgets of the fields that are there, once.
		p, _ := od.Page(0)
		widgets := 0
		for _, a := range p.Annotations() {
			if a.Subtype == "Widget" {
				widgets++
			}
		}
		if widgets != 7 {
			t.Errorf("%s: %d widgets on the page, want 7", name, widgets)
		}
		// The new widget shows the value of its field.
		img := renderView(t, p)
		n := 0
		for y := 112; y < 128; y++ {
			for x := 162; x < 288; x++ {
				if c := img.RGBAAt(x, y); c != yellow {
					n++
				}
			}
		}
		if img.RGBAAt(285, 120) != yellow || n < 20 || n > 600 {
			t.Errorf("%s: the second widget of the field shows %d pixels of text on %v", name, n, img.RGBAAt(285, 120))
		}
		// Its names are a tree still.
		var fdf bytes.Buffer
		if err := od.Form().NewState().ExportFDF(&fdf); err != nil || !strings.Contains(fdf.String(), "<</T (address) /Kids [\n<</T (street) /V (Main St)>>\n<</T (zip) /V (12345)>>]>>") {
			t.Errorf("%s: FDF %v:\n%s", name, err, fdf.String())
		}
		// A value written reaches both widgets.
		st := od.Form().NewState()
		ne := od.Edit()
		ne.unguarded = true
		st.vals[od.Form().Field("fixed")] = TextValue("changed")
		if err := ne.SetFields(st); err != nil {
			t.Fatal(err)
		}
		if got := formString(editOpen(t, editSave(t, ne))); !strings.Contains(got, "fixed text (changed) p0 p0") {
			t.Errorf("%s: after a value was written:\n%s", name, got)
		}
	}
	sameAsSave(t, e, upd)

	// A button more in a radio group of the document, and a value set in
	// the same editor.
	src = fieldsFile(false)
	doc = editOpen(t, src)
	e = doc.Edit()
	if err := e.AddField(0, Rect{70, 80, 90, 100}, NewField{Name: "color", OnState: "green"}); err != nil {
		t.Fatal(err)
	}
	od := editOpen(t, editUpdate(t, e, src))
	color := od.Form().Field("color")
	if len(color.Widgets) != 3 || color.Widgets[2].OnState != "green" || color.Saved.State() != "red" {
		t.Fatalf("radio group %+v", color)
	}
	state := od.Form().NewState()
	if err := state.SetValue(color, StateValue("green")); err != nil {
		t.Fatal(err)
	}
	ne := od.Edit()
	if err := ne.SetFields(state); err != nil {
		t.Fatal(err)
	}
	if got := formString(editOpen(t, editSave(t, ne))); !strings.Contains(got, "color radio /green p0:red p0:blue p0:green") {
		t.Errorf("after choosing the new button:\n%s", got)
	}
}

func TestAddFieldOdd(t *testing.T) {
	doc := editOpen(t, dataForm())
	text := NewField{Name: "x", Type: FieldText}
	rect := Rect{10, 10, 60, 30}
	with := func(f func(*NewField)) NewField {
		n := text
		f(&n)
		return n
	}
	for name, c := range map[string]struct {
		page int
		rect Rect
		f    NewField
	}{
		"no page":             {3, rect, text},
		"no area":             {0, Rect{10, 10, 10, 30}, text},
		"no name":             {0, rect, with(func(n *NewField) { n.Name = "" })},
		"an empty part":       {0, rect, with(func(n *NewField) { n.Name = "a..b" })},
		"below a field":       {0, rect, with(func(n *NewField) { n.Name = "fixed.x" })},
		"above fields":        {0, rect, with(func(n *NewField) { n.Name = "address" })},
		"another type":        {0, rect, with(func(n *NewField) { n.Name, n.Type = "fixed", FieldCheckBox })},
		"too long":            {0, rect, with(func(n *NewField) { n.MaxLen, n.Value = 3, TextValue("four") })},
		"no such option":      {0, rect, NewField{Name: "c", Type: FieldComboBox, Options: []Option{{Export: "a"}}, Value: ChoiceValue(1)}},
		"two of a combo":      {0, rect, NewField{Name: "c", Type: FieldComboBox, Options: []Option{{Export: "a"}, {Export: "b"}}, Value: ChoiceValue(0, 1)}},
		"a state of none":     {0, rect, NewField{Name: "c", Type: FieldCheckBox, Value: StateValue("On")}},
		"the state Off":       {0, rect, NewField{Name: "c", Type: FieldCheckBox, OnState: "Off"}},
		"a value of a button": {0, rect, NewField{Name: "c", Type: FieldPushButton, Value: TextValue("x")}},
	} {
		e := doc.Edit()
		if err := e.AddField(c.page, c.rect, c.f); err == nil || e.formed() {
			t.Errorf("%s: %v", name, err)
		}
	}
	e := doc.Edit()
	if err := e.AddField(0, rect, NewField{Name: "a.b", Type: FieldText}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "a.b.c"} {
		if err := e.AddField(0, rect, NewField{Name: name, Type: FieldText}); err == nil {
			t.Errorf("the field %q was added beside a.b", name)
		}
	}
	if err := e.RemoveField("nothing"); err == nil {
		t.Error("a field that is not there was removed")
	}
	if err := e.RemoveField("fixed"); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveField("fixed"); err == nil {
		t.Error("a field was removed twice")
	}
	// Its name is free again.
	if err := e.AddField(0, rect, NewField{Name: "fixed", Type: FieldCheckBox}); err != nil {
		t.Errorf("a field with the name of a removed one: %v", err)
	}
	if got := formString(editOpen(t, editSave(t, e))); !strings.Contains(got, "fixed checkbox /Off p0:Yes") || strings.Contains(got, "fixed text") {
		t.Errorf("after a field was replaced:\n%s", got)
	}

	// A new document has no pages of an edited one.
	ne := NewEditor()
	if err := ne.AddPage(0, 100, 100); err != nil {
		t.Fatal(err)
	}
	if err := ne.AddField(0, rect, text); err == nil {
		t.Error("a field was added to a page that is no page of a document")
	}

	// What signatures do not allow.
	locked := editOpen(t, signedFile(true, 0, false)).Edit()
	if err := locked.RemoveField("name"); !errors.Is(err, ErrLocked) {
		t.Errorf("removing a locked field: %v", err)
	}
	if err := locked.RemoveField("city"); err != nil {
		t.Errorf("removing a field that is not locked: %v", err)
	}
	certified := editOpen(t, signedFile(true, 2, false)).Edit()
	if err := certified.RemoveField("city"); !errors.Is(err, ErrCertified) {
		t.Errorf("removing a field of a certified document: %v", err)
	}
	if err := certified.AddField(0, rect, text); !errors.Is(err, ErrCertified) {
		t.Errorf("adding a field to a certified document: %v", err)
	}

	// A turned page: where it is shown, its content upright.
	src := testpdf.File{
		Objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R>>",
			2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200]>>",
			3: "<</Type /Page /Parent 2 0 R /Rotate 90>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
	te := editOpen(t, src).Edit()
	if err := te.AddField(0, Rect{20, 30, 120, 50}, NewField{Name: "t", Type: FieldText, Value: TextValue("Up"), Look: Appearance{Background: &yellow}}); err != nil {
		t.Fatal(err)
	}
	od := editOpen(t, editUpdate(t, te, src))
	p, _ := od.Page(0)
	w := od.Form().Field("t").Widgets[0]
	if got := QuadOf(w.Rect).Transform(p.DisplayMatrix()).Bounds(); !sameRect(got, Rect{20, 30, 120, 50}) || w.Rotation != 90 {
		t.Errorf("the field of a turned page is at %v, turned by %d", got, w.Rotation)
	}
	img := renderView(t, p)
	if a, b := img.RGBAAt(110, 40), img.RGBAAt(130, 40); a != yellow || b != white {
		t.Errorf("the field of a turned page shows %v, and beside it %v", a, b)
	}
	// Its text is at the left of the field as it is shown, not at one of
	// its ends as the page lies.
	left, right := 0, 0
	for y := 31; y < 49; y++ {
		for x := 21; x < 119; x++ {
			if img.RGBAAt(x, y) != yellow {
				if x < 60 {
					left++
				} else {
					right++
				}
			}
		}
	}
	if left < 10 || right != 0 {
		t.Errorf("%d pixels of text at the left of the turned field and %d at its right", left, right)
	}
}

// TestAddFieldNumbers adds fields whose new objects are counted from the
// start, as the objects of the document are, and are none of them: found
// by the corpus run in a file with a page tree of two levels, where a new
// field took the kids of the node with its number, and was no field then.
func TestAddFieldNumbers(t *testing.T) {
	// Every object but the page has kids that have kids.
	src := testpdf.File{
		Objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R /Kids [2 0 R]>>",
			2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200]>>",
			3: "<</Type /Pages /Parent 2 0 R /Kids [4 0 R] /Count 1>>",
			4: "<</Type /Pages /Parent 3 0 R /Kids [5 0 R] /Count 1>>",
			5: "<</Type /Pages /Parent 4 0 R /Kids [6 0 R] /Count 1>>",
			6: "<</Type /Pages /Parent 5 0 R /Kids [7 0 R] /Count 1>>",
			7: "<</Type /Pages /Parent 6 0 R /Kids [8 0 R] /Count 1>>",
			8: "<</Type /Pages /Parent 7 0 R /Kids [9 0 R] /Count 1>>",
			9: "<</Type /Page /Parent 8 0 R>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
	e := editOpen(t, src).Edit()
	for _, name := range []string{"a.b.c", "a.b.d", "e", "f.g"} {
		if err := e.AddField(0, Rect{10, 10, 60, 30}, NewField{Name: name, Value: TextValue(name)}); err != nil {
			t.Fatal(err)
		}
	}
	want := "a.b.c text (a.b.c) p0\na.b.d text (a.b.d) p0\ne text (e) p0\nf.g text (f.g) p0\n"
	for name, out := range map[string][]byte{"update": editUpdate(t, e, src), "save": editSave(t, e)} {
		od := editOpen(t, out)
		if got := formString(od); got != want {
			t.Errorf("%s: fields:\n%s\nwant:\n%s", name, got, want)
		}
		checkFieldTree(t, name, od)
	}
}

// checkFieldTree checks that the tree of fields of d is one: what a node names
// as its kids names the node as its parent.
func checkFieldTree(t *testing.T, what string, d *Document) {
	t.Helper()
	cat, _ := d.r.Catalog()
	var walk func(o, parent pdf.Object, depth int)
	walk = func(o, parent pdf.Object, depth int) {
		node := d.dict(o)
		if p := node.Get("Parent"); p != parent {
			t.Errorf("%s: %v is below %v and names %v as its parent", what, o, parent, p)
			return
		}
		kids, _ := d.resolve(node.Get("Kids")).Array()
		for _, k := range kids {
			if depth < maxFieldDepth {
				walk(k, o, depth+1)
			}
		}
	}
	fields, _ := d.resolve(d.dict(cat.Get("AcroForm")).Get("Fields")).Array()
	for _, f := range fields {
		walk(f, pdf.Null, 0)
	}
}
