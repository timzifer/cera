package cera

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/timzifer/cera/internal/pdf"
)

// Form data in its exchange formats (PDF 2.0, 12.7.8; ISO 19444-1): FDF,
// which is PDF syntax — a catalogue with /FDF /Fields, a tree of partial
// names with values — and XFDF, the same in XML. Tools export the data of
// a form in them, merge it into a template, and the buttons of forms
// submit it. Both carry more than values (annotations, pages, scripts);
// the values are what is read and written here.

// FormDataError reports what an import of form data could not do. The
// rest of the data is set.
type FormDataError struct {
	// Unknown are the names of fields the data has values for and the
	// form does not have.
	Unknown []string
	// Refused says, a field each, why the form does not take its value:
	// errors that wrap [ErrReadOnly] or [ErrInvalidValue] and name the
	// field.
	Refused []error
}

func (e *FormDataError) Error() string {
	var parts []string
	if n := len(e.Unknown); n > 0 {
		parts = append(parts, fmt.Sprintf("%d fields the form does not have (%s)", n, some(e.Unknown)))
	}
	if n := len(e.Refused); n > 0 {
		parts = append(parts, fmt.Sprintf("%d values refused (%v)", n, e.Refused[0]))
	}
	return "cera: form data: " + strings.Join(parts, ", ")
}

// Unwrap returns the errors of the values refused.
func (e *FormDataError) Unwrap() []error { return e.Refused }

// some lists the first names of a list.
func some(names []string) string {
	if len(names) > 3 {
		return strings.Join(names[:3], ", ") + ", …"
	}
	return strings.Join(names, ", ")
}

// formDatum is a field of form data: its fully qualified name and its
// values — one, or the several options selected of a list box.
type formDatum struct {
	name   string
	values []string
	// unread says why a value that is there could not be read.
	unread string
}

// formData returns the values of the state for export, in the order of
// the form: of the fields that have a value, but for passwords.
func (s *FormState) formData() []formDatum {
	var out []formDatum
	for _, f := range s.form.Fields {
		if f.Flags&FfPassword != 0 && f.Type == FieldText {
			continue // never exported (PDF 2.0, 12.7.5.3)
		}
		d := formDatum{name: f.Name}
		switch v := s.Value(f); v.Kind() {
		case ValueText:
			d.values = []string{v.Text()}
		case ValueState:
			d.values = []string{v.State()}
		case ValueChoice:
			for _, i := range v.Selected() {
				if i >= 0 && i < len(f.Options) {
					d.values = append(d.values, f.Options[i].Export)
				}
			}
			if len(d.values) == 0 {
				d.values = []string{""}
			}
		default:
			continue
		}
		out = append(out, d)
	}
	return out
}

// dataNode is a node of the tree of names form data is written as.
type dataNode struct {
	name   string
	datum  *formDatum
	button bool
	kids   []*dataNode
}

// dataTree puts data into the tree of its names, parted at the periods.
func (s *FormState) dataTree(data []formDatum) *dataNode {
	root := &dataNode{}
	for i := range data {
		parts := strings.Split(data[i].name, ".")
		n := root
		for _, part := range parts[:len(parts)-1] {
			var next *dataNode
			for _, k := range n.kids {
				if k.name == part && k.datum == nil {
					next = k
				}
			}
			if next == nil {
				next = &dataNode{name: part}
				n.kids = append(n.kids, next)
			}
			n = next
		}
		leaf := &dataNode{name: parts[len(parts)-1], datum: &data[i]}
		if f := s.form.Field(data[i].name); f != nil {
			leaf.button = f.Type == FieldCheckBox || f.Type == FieldRadio
		}
		n.kids = append(n.kids, leaf)
	}
	return root
}

// ExportFDF writes the values of the state as FDF: of every field that
// has one, by its name, but for password fields. The state of a check box
// or radio button is written as a name, text as a string, the options
// selected in a choice field by their export values.
func (s *FormState) ExportFDF(w io.Writer) error { return s.writeFDF(w, s.formData()) }

// writeFDF writes data as FDF.
func (s *FormState) writeFDF(w io.Writer, data []formDatum) error {
	bw := &bytes.Buffer{}
	bw.WriteString("%FDF-1.2\n%\xe2\xe3\xcf\xd3\n1 0 obj\n<</FDF <</Fields [")
	var write func(n *dataNode)
	write = func(n *dataNode) {
		bw.WriteString("\n<</T ")
		fdfString(bw, n.name)
		switch d := n.datum; {
		case d == nil:
			bw.WriteString(" /Kids [")
			for _, k := range n.kids {
				write(k)
			}
			bw.WriteString("]")
		case n.button:
			bw.WriteString(" /V ")
			fdfName(bw, d.values[0])
		case len(d.values) == 1:
			bw.WriteString(" /V ")
			fdfString(bw, d.values[0])
		default:
			bw.WriteString(" /V [")
			for i, v := range d.values {
				if i > 0 {
					bw.WriteByte(' ')
				}
				fdfString(bw, v)
			}
			bw.WriteString("]")
		}
		bw.WriteString(">>")
	}
	for _, k := range s.dataTree(data).kids {
		write(k)
	}
	bw.WriteString("]>>>>\nendobj\ntrailer\n<</Root 1 0 R>>\n%%EOF\n")
	_, err := w.Write(bw.Bytes())
	return err
}

// fdfString writes s as a text string: literally where it is printable
// ASCII, else in hexadecimal, so that the file is text.
func fdfString(w *bytes.Buffer, s string) {
	plain := true
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			plain = false
			break
		}
	}
	if !plain {
		fmt.Fprintf(w, "<%X>", encodeText(s))
		return
	}
	w.WriteByte('(')
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '(' || c == ')' || c == '\\' {
			w.WriteByte('\\')
		}
		w.WriteByte(s[i])
	}
	w.WriteByte(')')
}

// fdfName writes s as a name.
func fdfName(w *bytes.Buffer, s string) {
	w.WriteByte('/')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7e || c == '#' || strings.IndexByte("()<>[]{}/%", c) >= 0 {
			fmt.Fprintf(w, "#%02X", c)
			continue
		}
		w.WriteByte(c)
	}
}

// xfdfNS is the namespace of XFDF.
const xfdfNS = "http://ns.adobe.com/xfdf/"

// ExportXFDF writes the values of the state as XFDF, as ExportFDF writes
// them as FDF.
func (s *FormState) ExportXFDF(w io.Writer) error { return s.writeXFDF(w, s.formData()) }

// writeXFDF writes data as XFDF.
func (s *FormState) writeXFDF(w io.Writer, data []formDatum) error {
	bw := &bytes.Buffer{}
	bw.WriteString(xml.Header + `<xfdf xmlns="` + xfdfNS + `" xml:space="preserve">` + "\n<fields>")
	var write func(n *dataNode)
	write = func(n *dataNode) {
		bw.WriteString("\n<field name=\"")
		_ = xml.EscapeText(bw, []byte(n.name))
		bw.WriteString("\">")
		if n.datum == nil {
			for _, k := range n.kids {
				write(k)
			}
			bw.WriteString("\n")
		} else {
			for _, v := range n.datum.values {
				bw.WriteString("<value>")
				_ = xml.EscapeText(bw, []byte(xmlText(v)))
				bw.WriteString("</value>")
			}
		}
		bw.WriteString("</field>")
	}
	for _, k := range s.dataTree(data).kids {
		write(k)
	}
	bw.WriteString("\n</fields>\n</xfdf>\n")
	_, err := w.Write(bw.Bytes())
	return err
}

// xmlText returns s without what XML 1.0 cannot hold: control characters
// but tab and line ends.
func xmlText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return -1
		}
		return r
	}, s)
}

// ImportFDF sets the fields of the state to the values FDF data has for
// them, by their names; fields the data does not name stay as they are.
// A value is set as [FormState.SetValue] sets it, so the form checks it,
// calculated fields follow and subscribers are told. Rich text
// (/RV) is dropped for the plain value beside it; what else FDF may hold
// — annotations, pages, scripts — is not read.
//
// It returns a [*FormDataError] when the data names fields the form does
// not have, or has values the form does not take, after setting the
// others; and another error, with nothing set, for data that is not FDF
// or is larger than cera reads (docs/budgets.md).
func (s *FormState) ImportFDF(r io.Reader) error {
	b, err := readFormData(r)
	if err != nil {
		return err
	}
	data, err := parseFDF(b)
	if err != nil {
		return err
	}
	return s.setData(data)
}

// ImportXFDF is ImportFDF for XFDF data; rich text (value-richtext) is
// dropped.
func (s *FormState) ImportXFDF(r io.Reader) error {
	b, err := readFormData(r)
	if err != nil {
		return err
	}
	data, err := parseXFDF(b)
	if err != nil {
		return err
	}
	return s.setData(data)
}

// readFormData reads form data, of maxFormDataBytes at most.
func readFormData(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxFormDataBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cera: form data: %w", err)
	}
	if len(b) > maxFormDataBytes {
		return nil, fmt.Errorf("cera: form data of more than %d bytes", maxFormDataBytes)
	}
	return b, nil
}

// setData sets the fields data names.
func (s *FormState) setData(data []formDatum) error {
	var fail FormDataError
	for _, d := range data {
		f := s.form.Field(d.name)
		if f == nil {
			fail.Unknown = append(fail.Unknown, d.name)
			continue
		}
		if d.unread != "" {
			fail.Refused = append(fail.Refused, fmt.Errorf("%w: %s: %s", ErrInvalidValue, f.Name, d.unread))
			continue
		}
		var v Value
		switch f.Type {
		case FieldText:
			v = TextValue(strings.Join(d.values, "\n"))
		case FieldCheckBox, FieldRadio:
			v = StateValue(strings.Join(d.values, ""))
			if v.text == "" {
				v.text = "Off"
			}
		case FieldComboBox, FieldListBox:
			if len(d.values) == 1 {
				v = TextValue(d.values[0])
				break
			}
			var sel []int
			missing := ""
			for _, o := range d.values {
				i := f.option(o)
				if i < 0 {
					missing = o
				}
				sel = append(sel, i)
			}
			if missing != "" {
				fail.Refused = append(fail.Refused, fmt.Errorf("%w: %s: %q is no option", ErrInvalidValue, f.Name, missing))
				continue
			}
			v = ChoiceValue(sel...)
		default:
			continue // push buttons and signatures have no value to set
		}
		if s.Value(f).Equal(canonical(f, v)) {
			continue // also where the field is read-only
		}
		if err := s.SetValue(f, v); err != nil {
			if errors.Is(err, ErrReadOnly) {
				err = fmt.Errorf("%w: %s", err, f.Name)
			}
			fail.Refused = append(fail.Refused, err)
		}
	}
	if len(fail.Unknown) > 0 || len(fail.Refused) > 0 {
		return &fail
	}
	return nil
}

// canonical returns v as the field would keep it, or v itself where the
// field does not take it.
func canonical(f *Field, v Value) Value {
	if c, err := f.check(v); err == nil {
		return c
	}
	return v
}

var fdfObj = regexp.MustCompile(`(\d{1,10})[\x00\t\n\f\r ]+(\d{1,5})[\x00\t\n\f\r ]+obj`)

// parseFDF reads the fields of FDF data. The objects are found by looking
// for them: an FDF file need not have a cross-reference table, and those
// tools write are often wrong.
func parseFDF(b []byte) ([]formDatum, error) {
	if !bytes.Contains(b[:min(len(b), 1024)], []byte("%FDF-")) {
		return nil, errors.New("cera: form data: not an FDF file")
	}
	objs := map[int64]pdf.Object{}
	for _, m := range fdfObj.FindAllSubmatchIndex(b, -1) {
		if m[2] > 0 && b[m[2]-1] >= '0' && b[m[2]-1] <= '9' {
			continue
		}
		var num int64
		for _, c := range b[m[2]:m[3]] {
			num = num*10 + int64(c-'0')
		}
		o, n, err := pdf.ParseObject(b[m[1]:])
		if err != nil {
			continue
		}
		// A stream holds a long value.
		if d, ok := o.Dict(); ok {
			rest := bytes.TrimLeft(b[m[1]+n:], "\x00\t\n\f\r ")
			if data, ok := bytes.CutPrefix(rest, []byte("stream")); ok {
				data = bytes.TrimPrefix(bytes.TrimPrefix(data, []byte("\r")), []byte("\n"))
				if end := bytes.Index(data, []byte("endstream")); end >= 0 {
					data = bytes.TrimSuffix(bytes.TrimSuffix(data[:end], []byte("\n")), []byte("\r"))
					o = pdf.NewStream(d, data).Object()
				}
			}
		}
		objs[num] = o // the last of a number is the newest
	}
	resolve := func(o pdf.Object) pdf.Object {
		for range maxFieldDepth {
			r, ok := o.Ref()
			if !ok {
				return o
			}
			o = objs[int64(r.Num)]
		}
		return pdf.Null
	}
	dict := func(o pdf.Object) pdf.Dict {
		d, _ := resolve(o).Dict()
		return d
	}
	// The catalogue: the trailer's, or the object that has /FDF.
	var fdf pdf.Dict
	if i := bytes.LastIndex(b, []byte("trailer")); i >= 0 {
		if t, _, err := pdf.ParseObject(b[i+len("trailer"):]); err == nil {
			td, _ := t.Dict()
			fdf = dict(dict(td.Get("Root")).Get("FDF"))
		}
	}
	if fdf.IsZero() {
		for _, o := range objs {
			if d := dict(dict(o).Get("FDF")); !d.IsZero() {
				fdf = d
			}
		}
	}
	if fdf.IsZero() {
		return nil, errors.New("cera: form data: the FDF file has no /FDF dictionary")
	}

	var out []formDatum
	var walk func(o pdf.Object, parent string, depth int)
	walk = func(o pdf.Object, parent string, depth int) {
		fd := dict(o)
		if fd.IsZero() || depth > maxFieldDepth || len(out) >= maxFormDataFields {
			return
		}
		name := parent
		if t := textString(resolve(fd.Get("T"))); t != "" {
			if name != "" {
				name += "."
			}
			name += t
		}
		if kids, ok := resolve(fd.Get("Kids")).Array(); ok {
			for _, k := range kids {
				walk(k, name, depth+1)
			}
			return
		}
		v := resolve(fd.Get("V"))
		d := formDatum{name: name}
		switch v.Kind() {
		case pdf.KindString:
			d.values = []string{textString(v)}
		case pdf.KindName:
			n, _ := v.Name()
			d.values = []string{string(n)}
		case pdf.KindArray:
			a, _ := v.Array()
			for _, x := range a {
				x = resolve(x)
				if n, ok := x.Name(); ok {
					d.values = append(d.values, string(n))
				} else if _, ok := x.Str(); ok {
					d.values = append(d.values, textString(x))
				}
			}
			if len(d.values) == 0 {
				d.values = []string{""}
			}
		case pdf.KindStream:
			st, _ := v.Stream()
			data, ok := st.Unencrypted()
			if !ok || st.Dict.Has("Filter") {
				d.unread = "a value in an encoded stream is not read"
				break
			}
			d.values = []string{decodeTextString(data)}
		default:
			return // a field without a value: nothing to set
		}
		if name != "" {
			out = append(out, d)
		}
	}
	fields, _ := resolve(fdf.Get("Fields")).Array()
	for _, f := range fields {
		walk(f, "", 0)
	}
	return out, nil
}

// parseXFDF reads the fields of XFDF data.
func parseXFDF(b []byte) ([]formDatum, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	var out []formDatum
	type open struct {
		name   string
		values []string
		kids   bool
	}
	var stack []open
	inFields, seen := 0, false
	var value *strings.Builder
	skip := 0 // depth inside an element that is not read
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("cera: form data: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case skip > 0 || value != nil:
				skip++
			case !seen:
				if t.Name.Local != "xfdf" {
					return nil, fmt.Errorf("cera: form data: not an XFDF file: its root is %q", t.Name.Local)
				}
				seen = true
			case t.Name.Local == "fields" && len(stack) == 0:
				inFields++
			case t.Name.Local == "field" && inFields > 0:
				if len(stack) >= maxFieldDepth || len(out) >= maxFormDataFields {
					skip++
					break
				}
				name := ""
				for _, a := range t.Attr {
					if a.Name.Local == "name" {
						name = a.Value
					}
				}
				if n := len(stack); n > 0 {
					stack[n-1].kids = true
					if stack[n-1].name != "" && name != "" {
						name = stack[n-1].name + "." + name
					} else {
						name = stack[n-1].name + name
					}
				}
				stack = append(stack, open{name: name})
			case t.Name.Local == "value" && len(stack) > 0:
				value = &strings.Builder{}
			default:
				skip++ // value-richtext, annots, ids, f and what else there is
			}
		case xml.EndElement:
			switch {
			case skip > 0:
				skip--
			case value != nil:
				stack[len(stack)-1].values = append(stack[len(stack)-1].values, value.String())
				value = nil
			case t.Name.Local == "field" && len(stack) > 0:
				f := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if !f.kids && len(f.values) > 0 && f.name != "" {
					out = append(out, formDatum{name: f.name, values: f.values})
				}
			case t.Name.Local == "fields" && inFields > 0:
				inFields--
			}
		case xml.CharData:
			if value != nil && skip == 0 {
				value.Write(t)
			}
		}
	}
	if !seen {
		return nil, errors.New("cera: form data: not an XFDF file")
	}
	return out, nil
}
