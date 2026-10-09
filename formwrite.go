package cera

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
)

// Saving form values (ADR 0013, PDF 2.0, 12.7.4). A value is written on
// the terminal field node, where it overrides one inherited from above:
//
//   - a text as a text string, PDFDocEncoding when it can be, else UTF-16BE;
//     a rich text value (/RV) of the field is removed, or viewers would
//     show the old one;
//   - a check box or radio button state as a name, and every widget of the
//     field shows its on state or Off (/AS);
//   - a selection as the export value of the option, or an array of them,
//     and for a list box the indices of the options (/I); the text of an
//     editable combo box as a string.
//
// Until cera writes appearance streams for new values (#93), the form is
// marked /NeedAppearances when a text or choice changes, so viewers make
// them; check boxes and radio buttons switch between the appearances they
// have.

// SetFields records the values of the fields of state that differ from
// what the document holds, to be written by [Editor.Save] and
// [Editor.Update]. state must be of the form of the edited document. A
// later call replaces what an earlier one recorded.
func (e *Editor) SetFields(state *FormState) error {
	if e.base == nil {
		return errors.New("cera: a new document has no form")
	}
	if state == nil || state.Form() == nil || state.Form() != e.base.Form() {
		return errors.New("cera: the state is not of the edited document's form")
	}
	values := map[*Field]Value{}
	for _, f := range state.Changed() {
		switch f.Type {
		case FieldPushButton, FieldSignature:
			continue
		}
		if f.ref.Num <= 0 {
			return fmt.Errorf("cera: field %q is a direct object and cannot be written", f.Name)
		}
		values[f] = state.Value(f)
	}
	e.fields = values
	return nil
}

// fieldPatches encodes the recorded values as changes of the edited
// document's objects, and reports whether viewers must make appearances.
func (e *Editor) fieldPatches() ([]pdfedit.Patch, bool) {
	var out []pdfedit.Patch
	needAP := false
	fields := make([]*Field, 0, len(e.fields))
	for f := range e.fields {
		fields = append(fields, f)
	}
	// In form order, so that what is written does not depend on a map.
	slices.SortFunc(fields, func(a, b *Field) int {
		return slices.Index(a.form.Fields, a) - slices.Index(b.form.Fields, b)
	})
	for _, f := range fields {
		v := e.fields[f]
		p := pdfedit.Patch{Ref: f.ref}
		switch f.Type {
		case FieldText:
			p.Set = []pdf.Entry{{Key: "V", Val: pdf.String(encodeText(v.Text()))}}
			p.Del = []pdf.Name{"RV"}
			needAP = true
		case FieldCheckBox, FieldRadio:
			state := v.State()
			if state == "" {
				state = "Off"
			}
			p.Set = []pdf.Entry{{Key: "V", Val: pdf.Name(state).Object()}}
		case FieldComboBox, FieldListBox:
			p.Set, p.Del = choiceEntries(f, v)
			needAP = true
		}
		out = append(out, p)
		if f.Type == FieldCheckBox || f.Type == FieldRadio {
			for _, w := range f.Widgets {
				as := pdf.Name("Off")
				if w.OnState == v.State() {
					as = pdf.Name(w.OnState)
				}
				out = append(out, pdfedit.Patch{Ref: w.ref, Set: []pdf.Entry{{Key: "AS", Val: as.Object()}}})
			}
		}
	}
	return out, needAP
}

// choiceEntries encodes the value of a choice field: /V and /I set or
// removed.
func choiceEntries(f *Field, v Value) ([]pdf.Entry, []pdf.Name) {
	if v.Kind() == ValueText {
		return []pdf.Entry{{Key: "V", Val: pdf.String(encodeText(v.Text()))}}, []pdf.Name{"I"}
	}
	sel := v.Selected()
	if len(sel) == 0 {
		return nil, []pdf.Name{"V", "I"}
	}
	export := func(i int) pdf.Object { return pdf.String(encodeText(f.Options[i].Export)) }
	var set []pdf.Entry
	if len(sel) == 1 {
		set = append(set, pdf.Entry{Key: "V", Val: export(sel[0])})
	} else {
		a := make(pdf.Array, len(sel))
		for i, s := range sel {
			a[i] = export(s)
		}
		set = append(set, pdf.Entry{Key: "V", Val: a.Object()})
	}
	if f.Type != FieldListBox {
		return set, []pdf.Name{"I"}
	}
	idx := make(pdf.Array, len(sel))
	for i, s := range sel {
		idx[i] = pdf.Integer(int64(s))
	}
	return append(set, pdf.Entry{Key: "I", Val: idx.Object()}), nil
}

var (
	pdfDocOnce sync.Once
	pdfDocByte map[rune]byte // the PDFDocEncoding byte of a rune
)

// encodeText encodes s as a PDF text string: in PDFDocEncoding when every
// character has a byte there, else as UTF-16BE with a byte order mark.
func encodeText(s string) []byte {
	pdfDocOnce.Do(func() {
		pdfDocByte = map[rune]byte{}
		for c := 255; c >= 0; c-- { // the lowest byte of a rune wins
			if r := pdfDocRune(byte(c)); r != utf8.RuneError {
				pdfDocByte[r] = byte(c)
			}
		}
	})
	b := make([]byte, 0, len(s))
	for _, r := range s {
		c, ok := pdfDocByte[r]
		if !ok || r == 0x1b { // ESC starts a language mark in text strings
			return encodeUTF16(s)
		}
		b = append(b, c)
	}
	// Bytes that read as a byte order mark would make it another string.
	if len(b) >= 2 && b[0] == 0xfe && b[1] == 0xff || len(b) >= 3 && b[0] == 0xef && b[1] == 0xbb && b[2] == 0xbf {
		return encodeUTF16(s)
	}
	return b
}

// encodeUTF16 encodes s as UTF-16BE with a byte order mark.
func encodeUTF16(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 2, 2+2*len(u))
	out[0], out[1] = 0xfe, 0xff
	for _, x := range u {
		out = append(out, byte(x>>8), byte(x))
	}
	return out
}
