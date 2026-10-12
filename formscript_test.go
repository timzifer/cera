package cera

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/timzifer/cera/internal/testpdf"
)

func TestParseCall(t *testing.T) {
	for _, tt := range []struct {
		js   string
		name string
		args []any
	}{
		{`AFNumber_Format(2, 0, 0, 0, "$", true);`, "AFNumber_Format", []any{2.0, 0.0, 0.0, 0.0, "$", true}},
		{"  AFSimple_Calculate('SUM', new Array (\"a.b\", 'c'))  ;\n", "AFSimple_Calculate", []any{"SUM", []any{"a.b", "c"}}},
		{`AFSimple_Calculate("PRD", ["x", "y"])`, "AFSimple_Calculate", []any{"PRD", []any{"x", "y"}}},
		{`AFRange_Validate(true, -1.5e1, false, 0);`, "AFRange_Validate", []any{true, -15.0, false, 0.0}},
		{`AFDate_FormatEx("d.mm\"yy");;`, "AFDate_FormatEx", []any{`d.mm"yy`}},
		{`f()`, "f", nil},
	} {
		name, args, ok := parseCall(tt.js)
		if !ok || name != tt.name || !reflect.DeepEqual(args, tt.args) {
			t.Errorf("%s: %q %#v %v", tt.js, name, args, ok)
		}
	}
	for _, js := range []string{
		"", "event.value = 1;", `AFNumber_Format(2, x);`, `AFNumber_Format(2); app.alert(1)`, `f(1`, `f("a)`,
		`var a = 1; AFNumber_Format(2,0,0,0,"",true);`, `f(1,)`, `f(this.getField("a").value)`,
	} {
		if name, args, ok := parseCall(js); ok {
			t.Errorf("%q parsed as %s%v", js, name, args)
		}
	}
}

func TestFormats(t *testing.T) {
	num := func(dec, sep, neg int, cur string, pre bool) *formatScript {
		return &formatScript{kind: 'n', dec: dec, sep: sep, neg: neg, currency: cur, prepend: pre}
	}
	for _, tt := range []struct {
		f    *formatScript
		in   string
		want string
		red  bool
	}{
		{num(2, 0, 0, "", false), "1234567.891", "1,234,567.89", false},
		{num(2, 1, 0, "", false), "1234.5", "1234.50", false},
		{num(2, 2, 0, " €", false), "-1234,5", "-1.234,50 €", false},
		{num(0, 3, 0, "", false), "999.5", "1000", false},
		{num(1, 4, 0, "", false), "1234567", "1'234'567.0", false},
		{num(2, 0, 0, "$", true), "-5", "-$5.00", false},
		{num(2, 0, 1, "$", true), "-5", "$5.00", true},
		{num(2, 0, 2, "", false), "-5", "(5.00)", false},
		{num(2, 0, 3, "", false), "-5", "(5.00)", true},
		{num(2, 0, 3, "", false), "5", "5.00", false},
		{num(2, 0, 0, "", false), "-0.001", "0.00", false},
		{num(2, 0, 0, "", false), "1.005", "1.01", false}, // half away from zero
		{num(2, 0, 0, "", false), "", "", false},
		{num(2, 0, 0, "", false), "abc", "abc", false},
		{&formatScript{kind: 'p', dec: 1, sep: 0}, "0.125", "12.5%", false},
		{&formatScript{kind: 'p', dec: 0, sep: 0, prepend: true}, "-1", "%-100", false},
		{&formatScript{kind: 'd', pattern: "dd.mm.yyyy"}, "2026-3-7", "07.03.2026", false},
		{&formatScript{kind: 'd', pattern: "mmmm d, yyyy"}, "3/7/26", "March 7, 2026", false},
		{&formatScript{kind: 'd', pattern: "ddd, d-mmm-yy"}, "7.3.2026", "Sat, 7-Mar-26", false},
		{&formatScript{kind: 'd', pattern: "yyyy-mm-dd"}, "7 March 1999", "1999-03-07", false},
		{&formatScript{kind: 'd', pattern: "m/d/yy h:MM tt"}, "12/31/2025 23:05", "12/31/25 11:05 pm", false},
		{&formatScript{kind: 'd', pattern: "dd.mm.yyyy"}, "31.2.2026", "31.2.2026", false}, // no such day
		{&formatScript{kind: 'd', pattern: "dd.mm.yyyy"}, "soon", "soon", false},
		{&formatScript{kind: 't', pattern: "HH:MM"}, "9:05 pm", "21:05", false},
		{&formatScript{kind: 't', pattern: "h:MM:ss tt"}, "00:07:09", "12:07:09 am", false},
		{&formatScript{kind: 's', special: 0}, "12345", "12345", false},
		{&formatScript{kind: 's', special: 1}, "123456789", "12345-6789", false},
		{&formatScript{kind: 's', special: 2}, "555 123 4567", "(555) 123-4567", false},
		{&formatScript{kind: 's', special: 2}, "1234567", "123-4567", false},
		{&formatScript{kind: 's', special: 3}, "123456789", "123-45-6789", false},
		{&formatScript{kind: 's', special: 3}, "1234", "1234", false},
	} {
		got, red := tt.f.text(tt.in)
		if got != tt.want || red != tt.red {
			t.Errorf("%+v of %q: %q red %v, want %q red %v", *tt.f, tt.in, got, red, tt.want, tt.red)
		}
	}
	if got := formatDate(time.Date(2026, 1, 5, 14, 3, 9, 0, time.UTC), `dddd d\d mmm yyyy HH:MM:ss h t`); got != "Monday 5d Jan 2026 14:03:09 2 p" {
		t.Errorf("date %q", got)
	}
}

// scriptForm is an order form: prices with a currency, a quantity that
// must be 1 to 10, line totals, their sum over a parent name, a tax in
// percent, a date, and fields whose scripts cera does not run.
func scriptForm() []byte {
	js := func(s string) string {
		return "<</S /JavaScript /JS (" + strings.NewReplacer("(", `\(`, ")", `\)`).Replace(s) + ")>>"
	}
	field := func(name, rect, rest string) string {
		return "<</Type /Annot /Subtype /Widget /FT /Tx /T (" + name + ") /Rect [" + rect + "] " + rest + ">>"
	}
	money := `AFNumber_Format(2, 0, 1, 0, "$", true);`
	return testpdf.File{
		Objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R /AcroForm <</Fields [10 0 R 11 0 R 20 0 R 13 0 R 14 0 R 15 0 R 16 0 R 17 0 R 18 0 R 19 0 R]" +
				" /CO [21 0 R 22 0 R 13 0 R 14 0 R] /DA (/Helv 10 Tf 0 g) /DR <</Font <</Helv 30 0 R>>>>>>>>",
			2:  "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 300]>>",
			3:  "<</Type /Page /Parent 2 0 R /Annots [10 0 R 11 0 R 21 0 R 22 0 R 13 0 R 14 0 R 15 0 R 16 0 R 17 0 R 18 0 R 19 0 R]>>",
			10: field("price", "10 260 140 280", "/V (2.5) /AA <</F "+js(money)+" /K "+js(`AFNumber_Keystroke(2, 0, 1, 0, "$", true);`)+">>"),
			11: field("qty", "10 230 140 250", "/V (2) /AA <</V "+js("AFRange_Validate(true, 1, true, 10);")+">>"),
			// Line totals under one parent.
			20: "<</T (line) /Kids [21 0 R 22 0 R]>>",
			21: field("a", "10 200 140 220", "/Parent 20 0 R /Ff 1 /V (5) /AA <</C "+js(`AFSimple_Calculate("PRD", new Array("price", "qty"));`)+" /F "+js(money)+">>"),
			22: field("b", "10 170 140 190", "/Parent 20 0 R /V (-1)"),
			13: field("sum", "10 140 140 160", "/Ff 1 /V (4) /AA <</C "+js(`AFSimple_Calculate("SUM", "line");`)+" /F "+js(money)+">>"),
			14: field("tax", "10 110 140 130", "/Ff 1 /AA <</C "+js(`AFSimple_Calculate("PRD", ["sum", "rate"]);`)+">>"),
			15: field("rate", "10 80 140 100", "/V (0.19) /AA <</F "+js("AFPercent_Format(0, 0);")+">>"),
			16: field("when", "10 50 140 70", "/AA <</F "+js(`AFDate_FormatEx("dd.mm.yyyy");`)+">>"),
			17: field("custom", "150 260 290 280", "/AA <</C "+js(`event.value = this.getField("price").value * 2;`)+">>"),
			18: field("mouse", "150 230 290 250", "/AA <</F "+js("AFSpecial_Format(0);")+" /Fo "+js("app.beep();")+">>"),
			19: field("plain", "150 200 290 220", ""),
			30: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

// TestFormScripts formats, validates and calculates as the scripts of the
// fields say, without running them.
func TestFormScripts(t *testing.T) {
	src := scriptForm()
	doc := editOpen(t, src)
	form := doc.Form()
	for name, want := range map[string][2]bool{ // Computed, HasActions
		"price": {true, false}, "qty": {true, false}, "line.a": {true, false}, "line.b": {false, false},
		"sum": {true, false}, "tax": {true, false}, "rate": {true, false}, "when": {true, false},
		"custom": {false, true}, "mouse": {false, true}, "plain": {false, false},
	} {
		f := form.Field(name)
		if f == nil {
			t.Fatalf("no field %s", name)
		}
		if f.Computed != want[0] || f.HasActions != want[1] {
			t.Errorf("%s: computed %v, actions %v", name, f.Computed, f.HasActions)
		}
	}

	state := form.NewState()
	var notified []string
	state.Subscribe(func(f *Field) { notified = append(notified, f.Name) })
	set := func(name, text string) error { return state.SetValue(form.Field(name), TextValue(text)) }
	value := func(name string) string { return state.Value(form.Field(name)).Text() }

	// The values of the file stay until something changes.
	if value("line.a") != "5" || value("sum") != "4" {
		t.Errorf("before a change: a %q, sum %q", value("line.a"), value("sum"))
	}
	if err := set("price", "3"); err != nil {
		t.Fatal(err)
	}
	// a = 3 × 2, sum = a + b, tax = sum × rate: in the order of /CO.
	if value("line.a") != "6" || value("sum") != "5" || value("tax") != "0.95" {
		t.Errorf("a %q, sum %q, tax %q", value("line.a"), value("sum"), value("tax"))
	}
	if got := strings.Join(notified, " "); got != "price line.a sum tax" {
		t.Errorf("notified of %q", got)
	}
	// Validation: a quantity from 1 to 10.
	for _, bad := range []string{"0", "11", "-3", "10.5"} {
		if err := set("qty", bad); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("quantity %s: %v", bad, err)
		}
	}
	if value("qty") != "2" {
		t.Errorf("quantity %q after refused values", value("qty"))
	}
	if err := set("qty", "10"); err != nil {
		t.Fatal(err)
	}
	if err := set("line.b", "-40.5"); err != nil {
		t.Fatal(err)
	}
	if value("line.a") != "30" || value("sum") != "-10.5" || value("tax") != "-1.995" {
		t.Errorf("a %q, sum %q, tax %q", value("line.a"), value("sum"), value("tax"))
	}
	// A calculated field is read-only to the user here.
	if err := set("sum", "1"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("setting the sum: %v", err)
	}

	// What the fields show.
	if err := set("when", "2026-10-12"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"price": "$3.00", "line.a": "$30.00", "sum": "$10.50", "tax": "-1.995", "rate": "19%", "when": "12.10.2026", "qty": "10", "plain": "",
	} {
		f := form.Field(name)
		if got := f.Format(state.Value(f)); got != want {
			t.Errorf("%s shows %q, want %q", name, got, want)
		}
	}

	// Drawn: the sum is negative, and red.
	img := renderState(t, doc, state)
	red := 0
	for y := (300 - 160) * 2; y < (300-140)*2; y++ {
		for x := 20; x < 280; x++ {
			if c := img.RGBAAt(x, y); c.R > 200 && c.G < 80 && c.B < 80 {
				red++
			}
		}
	}
	if red < 20 {
		t.Errorf("%d red pixels in the negative sum", red)
	}

	// Written: the values as they are, the appearances formatted.
	e := doc.Edit()
	if err := e.SetFields(state); err != nil {
		t.Fatal(err)
	}
	out := editOpen(t, editSave(t, e))
	of := out.Form()
	for name, want := range map[string]string{"price": "3", "qty": "10", "line.a": "30", "line.b": "-40.5", "sum": "-10.5", "tax": "-1.995", "when": "2026-10-12"} {
		if got := of.Field(name).Saved.Text(); got != want {
			t.Errorf("saved %s %q, want %q", name, got, want)
		}
	}
	if n, most := pixDiff(img, renderState(t, out, nil)); n > 0 {
		t.Errorf("the saved form renders unlike the filled one: %d pixels, by up to %d", n, most)
	}
	ap := out.appearance(&Annotation{dict: of.Field("price").Widgets[0].dict}, "")
	if ap == nil {
		t.Fatal("the price has no appearance")
	}
	if content, _, _ := out.r.DecodeStrict(ap); !strings.Contains(string(content), "<24332E3030>") { // $3.00
		t.Errorf("appearance of the price: %s", content)
	}

	// Reset: back to the defaults, calculated again.
	state.Reset()
	if value("price") != "" || value("line.a") != "0" || value("sum") != "0" {
		t.Errorf("after a reset: price %q, a %q, sum %q", value("price"), value("line.a"), value("sum"))
	}
}
