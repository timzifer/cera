package cera

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
)

// dataForm is a form with fields below a parent, a combo box, a list box
// that takes several options, a check box, a password, a read-only field
// and a signature field.
func dataForm() []byte {
	return testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R /AcroForm <</Fields [10 0 R 13 0 R 14 0 R 15 0 R 16 0 R 17 0 R] /DA (/Helv 10 Tf 0 g) /DR <</Font <</Helv 20 0 R>>>>>>>>",
			2:  "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 300]>>",
			3:  "<</Type /Page /Parent 2 0 R /Annots [11 0 R 12 0 R 13 0 R 14 0 R 15 0 R 16 0 R 17 0 R]>>",
			10: "<</T (address) /Kids [11 0 R 12 0 R]>>",
			11: "<</Type /Annot /Subtype /Widget /Parent 10 0 R /FT /Tx /T (street) /V (Main St) /Rect [10 260 140 280]>>",
			12: "<</Type /Annot /Subtype /Widget /Parent 10 0 R /FT /Ch /Ff 131072 /T (city) /Opt [[(B) (Berlin)] [(M) (M\xfcnchen)]] /V (B) /Rect [10 230 140 250]>>",
			13: "<</Type /Annot /Subtype /Widget /FT /Tx /Ff 8192 /T (secret) /V (pw) /Rect [10 200 140 220]>>",
			14: "<</Type /Annot /Subtype /Widget /FT /Tx /Ff 1 /T (fixed) /V (const) /Rect [10 170 140 190]>>",
			15: "<</Type /Annot /Subtype /Widget /FT /Btn /T (agree) /V /Off /AS /Off /Rect [10 140 30 160] /AP <</N <</Yes 30 0 R /Off 31 0 R>>>>>>",
			16: "<</Type /Annot /Subtype /Widget /FT /Ch /Ff 2097152 /T (langs) /Opt [(de) (en) (fr)] /V (de) /Rect [10 60 140 130]>>",
			17: "<</Type /Annot /Subtype /Widget /FT /Sig /T (sig) /Rect [10 10 140 50]>>",
			20: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
			30: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 20 20]", []byte("0 0 1 rg 2 2 16 16 re f")),
			31: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 20 20]", []byte("0 0 0 RG 0.5 0.5 19 19 re S")),
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

// dataState returns a state of the form with every field changed.
func dataState(t testing.TB) *FormState {
	t.Helper()
	doc, err := Open(dataForm())
	if err != nil {
		t.Fatal(err)
	}
	form := doc.Form()
	s := form.NewState()
	for name, v := range map[string]Value{
		"address.street": TextValue("Straße (1) \\ 中"),
		"address.city":   TextValue("M"),
		"agree":          StateValue("Yes"),
		"langs":          ChoiceValue(0, 2),
	} {
		if err := s.SetValue(form.Field(name), v); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func sameValues(t *testing.T, what string, got, want *FormState) {
	t.Helper()
	for _, f := range want.Form().Fields {
		g := got.Form().Field(f.Name)
		if a, b := got.Value(g), want.Value(f); !a.Equal(b) {
			t.Errorf("%s: %s is %+v, want %+v", what, f.Name, a, b)
		}
	}
}

func TestExportFormData(t *testing.T) {
	s := dataState(t)
	var fdf, xfdf bytes.Buffer
	if err := s.ExportFDF(&fdf); err != nil {
		t.Fatal(err)
	}
	if err := s.ExportXFDF(&xfdf); err != nil {
		t.Fatal(err)
	}
	// Names in their tree, the state of a button as a name, what is not
	// ASCII in hexadecimal, several options as an array; no password, and
	// no field without a value.
	wantFDF := "%FDF-1.2\n%\xe2\xe3\xcf\xd3\n1 0 obj\n<</FDF <</Fields [\n" +
		"<</T (address) /Kids [\n" +
		"<</T (street) /V <FEFF005300740072006100DF006500200028003100290020005C00204E2D>>>\n" +
		"<</T (city) /V (M)>>]>>\n" +
		"<</T (fixed) /V (const)>>\n" +
		"<</T (agree) /V /Yes>>\n" +
		"<</T (langs) /V [(de) (fr)]>>]>>>>\nendobj\ntrailer\n<</Root 1 0 R>>\n%%EOF\n"
	if got := fdf.String(); got != wantFDF {
		t.Errorf("FDF:\n%s\nwant:\n%s", got, wantFDF)
	}
	wantXFDF := `<?xml version="1.0" encoding="UTF-8"?>
<xfdf xmlns="http://ns.adobe.com/xfdf/" xml:space="preserve">
<fields>
<field name="address">
<field name="street"><value>Straße (1) \ 中</value></field>
<field name="city"><value>M</value></field>
</field>
<field name="fixed"><value>const</value></field>
<field name="agree"><value>Yes</value></field>
<field name="langs"><value>de</value><value>fr</value></field>
</fields>
</xfdf>
`
	if got := xfdf.String(); got != wantXFDF {
		t.Errorf("XFDF:\n%s\nwant:\n%s", got, wantXFDF)
	}

	// What is written is read: a new state takes every value.
	for name, c := range map[string]struct {
		data []byte
		read func(*FormState, *bytes.Reader) error
	}{
		"FDF":  {fdf.Bytes(), func(s *FormState, r *bytes.Reader) error { return s.ImportFDF(r) }},
		"XFDF": {xfdf.Bytes(), func(s *FormState, r *bytes.Reader) error { return s.ImportXFDF(r) }},
	} {
		fresh := editOpen(t, dataForm()).Form().NewState()
		told := 0
		fresh.Subscribe(func(*Field) { told++ })
		if err := c.read(fresh, bytes.NewReader(c.data)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		sameValues(t, name, fresh, s)
		if told != 4 {
			t.Errorf("%s: %d changes told, want 4", name, told)
		}
		// Nothing to set: the same again.
		if err := c.read(fresh, bytes.NewReader(c.data)); err != nil || told != 4 {
			t.Errorf("%s again: %v, %d changes told", name, err, told)
		}
	}
}

func TestImportFDF(t *testing.T) {
	form := editOpen(t, dataForm()).Form()
	s := form.NewState()
	// As tools write it: no trailer, objects for a value and for the
	// fields, a value in a stream, a state as a string, text as a name, a
	// field the form lacks, a value no option, a field that does not
	// change, rich text beside the value.
	src := "%FDF-1.2\r\n" +
		"3 0 obj (indirect street) endobj\r\n" +
		"4 0 obj\n<</Length 12>>\nstream\r\nhello\nstream\r\nendstream\nendobj\n" +
		"5 0 obj <</Filter /FlateDecode /Length 3>> stream\nabc\nendstream endobj\n" +
		"2 0 obj [<</T (address) /Kids [<</T (street) /V 3 0 R /RV (<p>rich</p>)>> <</T (city) /V (M\xfcnchen)>> <</T (zip) /V (12345)>>]>>\n" +
		"<</T (agree) /V (Yes)>> <</T (langs) /V [(en) /fr]>> <</T (fixed) /V (const)>> <</T (nothing)>>\n" +
		"<</T (sig) /V (x)>> <</T (other) /V 4 0 R>> <</T (secret) /V 5 0 R>>] endobj\n" +
		"1 0 obj <</FDF <</Fields 2 0 R /F (form.pdf)>>>> endobj\n"
	err := s.ImportFDF(strings.NewReader(src))
	var fail *FormDataError
	if !errors.As(err, &fail) || strings.Join(fail.Unknown, " ") != "address.zip other" || len(fail.Refused) != 1 ||
		!errors.Is(err, ErrInvalidValue) || !strings.Contains(fail.Refused[0].Error(), "secret") {
		t.Fatalf("import: %v (%+v)", err, fail)
	}
	for name, want := range map[string]Value{
		"address.street": TextValue("indirect street"),
		"address.city":   ChoiceValue(1),
		"agree":          StateValue("Yes"),
		"langs":          ChoiceValue(1, 2),
		"fixed":          TextValue("const"),
		"secret":         TextValue("pw"),
	} {
		if got := s.Value(form.Field(name)); !got.Equal(want) {
			t.Errorf("%s is %+v, want %+v", name, got, want)
		}
	}

	// A value in a stream; what a field does not take.
	s = form.NewState()
	err = s.ImportFDF(strings.NewReader("%FDF-1.2\n4 0 obj\n<</Length 5>>\nstream\nhello\nendstream\nendobj\n" +
		"1 0 obj <</FDF <</Fields [<</T (address.street) /V 4 0 R>> <</T (fixed) /V (changed)>> <</T (agree) /V /Maybe>>" +
		" <</T (langs) /V [(de) (it)]>> <</T (address) /Kids [<</T (city) /V (Paris)>>]>>]>>>> endobj\ntrailer <</Root 1 0 R>>"))
	if !errors.As(err, &fail) || len(fail.Unknown) != 0 || len(fail.Refused) != 4 || !errors.Is(err, ErrReadOnly) || !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("import: %v (%+v)", err, fail)
	}
	if got := s.Value(form.Field("address.street")).Text(); got != "hello" {
		t.Errorf("a value in a stream: %q", got)
	}
	if n := len(s.Changed()); n != 1 {
		t.Errorf("%d fields changed, want 1", n)
	}

	// What is no FDF sets nothing.
	for _, bad := range []string{"", "%PDF-1.7\n1 0 obj <</FDF <</Fields []>>>> endobj", "%FDF-1.2\n1 0 obj <</Type /Catalog>> endobj", "%FDF-1.2"} {
		err := s.ImportFDF(strings.NewReader(bad))
		if err == nil || errors.As(err, &fail) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// No fields: nothing to do.
	if err := s.ImportFDF(strings.NewReader("%FDF-1.2\n1 0 obj <</FDF <</Annots []>>>> endobj")); err != nil {
		t.Errorf("FDF without fields: %v", err)
	}
}

func TestImportXFDF(t *testing.T) {
	form := editOpen(t, dataForm()).Form()
	s := form.NewState()
	src := `<?xml version="1.0" encoding="UTF-8"?>
<xfdf xmlns="http://ns.adobe.com/xfdf/" xml:space="preserve">
  <f href="form.pdf"/>
  <annots><text><contents>a note <field name="agree"><value>Off</value></field></contents></text></annots>
  <fields>
    <field name="address">
      <field name="street"><value>A &amp; B &lt;1&gt; &#252;</value><value-richtext><body><p><value>rich</value></p></body></value-richtext></field>
      <field name="city"><value>Berlin</value></field>
      <field name="zip"><value>12345</value></field>
    </field>
    <field name="agree"><value>Yes</value></field>
    <field name="langs"><value>en</value><value>fr</value></field>
    <field name="empty"/>
    <field name="sig"><value>x</value></field>
  </fields>
  <ids original="A" modified="B"/>
</xfdf>`
	err := s.ImportXFDF(strings.NewReader(src))
	var fail *FormDataError
	if !errors.As(err, &fail) || strings.Join(fail.Unknown, " ") != "address.zip" || len(fail.Refused) != 0 {
		t.Fatalf("import: %v (%+v)", err, fail)
	}
	for name, want := range map[string]Value{
		"address.street": TextValue("A & B <1> ü"),
		"address.city":   ChoiceValue(0),
		"agree":          StateValue("Yes"),
		"langs":          ChoiceValue(1, 2),
	} {
		if got := s.Value(form.Field(name)); !got.Equal(want) {
			t.Errorf("%s is %+v, want %+v", name, got, want)
		}
	}
	for _, bad := range []string{"", "<fields><field name='agree'><value>Yes</value></field></fields>", "<xfdf><fields><field name='agree'><value>Yes</value>", "%FDF-1.2"} {
		fresh := form.NewState()
		err := fresh.ImportXFDF(strings.NewReader(bad))
		if err == nil || errors.As(err, &fail) || len(fresh.Changed()) != 0 {
			t.Errorf("%q: %v, %d changed", bad, err, len(fresh.Changed()))
		}
	}
	// More than is read.
	if err := s.ImportXFDF(io.LimitReader(spaces{}, maxFormDataBytes+1)); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("data too large: %v", err)
	}
}

// spaces reads as spaces without end.
type spaces struct{}

func (spaces) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

// FuzzFormData reads any bytes as FDF and as XFDF into a state: neither
// panics, and what a state then holds is written and read back the same.
func FuzzFormData(f *testing.F) {
	s := dataState(f)
	var fdf, xfdf bytes.Buffer
	_ = s.ExportFDF(&fdf)
	_ = s.ExportXFDF(&xfdf)
	f.Add(fdf.Bytes())
	f.Add(xfdf.Bytes())
	f.Add([]byte("%FDF-1.2\n1 0 obj <</FDF <</Fields [<</T (a) /Kids [1 0 R 2 0 R]>>]>>>> endobj\n2 0 obj <</Length 3>> stream\nabc\nendstream endobj trailer <</Root 1 0 R>>"))
	f.Add([]byte("<xfdf><fields><field name='langs'><value>de</value><value>en</value><field name='x'/></field></fields></xfdf>"))
	form := dataForm()
	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := Open(form)
		if err != nil {
			t.Fatal(err)
		}
		for _, xml := range []bool{false, true} {
			s := doc.Form().NewState()
			var out bytes.Buffer
			if xml {
				_ = s.ImportXFDF(bytes.NewReader(data))
				err = s.ExportXFDF(&out)
			} else {
				_ = s.ImportFDF(bytes.NewReader(data))
				err = s.ExportFDF(&out)
			}
			if err != nil {
				t.Fatal(err)
			}
			back := doc.Form().NewState()
			if xml {
				err = back.ImportXFDF(bytes.NewReader(out.Bytes()))
			} else {
				err = back.ImportFDF(bytes.NewReader(out.Bytes()))
			}
			if err != nil {
				t.Fatalf("xml %v: what was written does not read: %v\n%s", xml, err, out.Bytes())
			}
			for _, f := range doc.Form().Fields {
				if f.Flags&FfPassword != 0 {
					continue
				}
				if a, b := back.Value(f), s.Value(f); !a.Equal(b) {
					t.Fatalf("xml %v: %s is %+v after a round trip, was %+v\n%s", xml, f.Name, a, b, out.Bytes())
				}
			}
		}
	})
}
