package cera

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/timzifer/cera/internal/pdf"
)

// The actions of widgets (PDF 2.0, 12.6.4 and 12.7.6): what a push button
// does when it is pressed. Those of forms — resetting fields, showing and
// hiding widgets — a FormState carries out; where a button leads is a
// LinkTarget, as for a link; submitting is told with all a viewer needs to
// do it or to refuse, for cera sends nothing; scripts are told and not
// run.

// WidgetAction is an action of a widget.
type WidgetAction struct {
	// Kind is the type of the action: "ResetForm", "Hide", "SubmitForm",
	// "ImportData", "JavaScript", or one of those a link has ("GoTo",
	// "GoToR", "URI", "Named", "Launch", ...), which Target describes.
	Kind string
	// Fields are the fully qualified names an action for fields lists:
	// those to reset or submit, or with Exclude all but those; of a Hide
	// action those whose widgets are shown or hidden. A name may be that
	// of a node above fields and stands for them all. No names is every
	// field, for ResetForm and SubmitForm.
	Fields  []string
	Exclude bool
	// Hide says of a Hide action whether it hides (true) or shows.
	Hide bool
	// URL is where a SubmitForm action sends, and the file of an
	// ImportData action. Format is how: "FDF", "XFDF", "HTML" (the fields
	// as an HTML form sends them) or "PDF" (the document itself); Get asks
	// for HTTP GET, for HTML alone. Flags are the flags of the action as
	// the file has them (PDF 2.0, table 239).
	URL    string
	Format string
	Get    bool
	Flags  uint32
	// Target is where an action of a link leads; see [LinkTarget].
	Target *LinkTarget
	// Script is the text of a JavaScript action. cera does not run it.
	Script string

	annots []annotPlace // annotations a Hide action names, where they are widgets
}

// actions reads the action of the widget wd and those chained to it
// (/Next), in the order they are to be done.
func (fr *formReader) actions(wd pdf.Dict) []WidgetAction {
	d := fr.d
	o := wd.Get("A")
	if d.dict(o).IsZero() {
		o = d.dict(wd.Get("AA")).Get("U") // when the mouse button is let go: the same moment
	}
	var out []WidgetAction
	left := maxActions
	var walk func(o pdf.Object)
	walk = func(o pdf.Object) {
		act := d.dict(o)
		if act.IsZero() || left <= 0 {
			return
		}
		left--
		if a, ok := fr.action(act); ok {
			out = append(out, a)
		}
		next := act.Get("Next")
		list, ok := d.resolve(next).Array()
		if !ok {
			list = pdf.Array{next}
		}
		for _, n := range list {
			walk(n)
		}
	}
	walk(o)
	return out
}

// action reads one action.
func (fr *formReader) action(act pdf.Dict) (WidgetAction, bool) {
	d := fr.d
	s, _ := d.name(act.Get("S"))
	a := WidgetAction{Kind: string(s)}
	flags := uint32(0)
	if v, ok := d.integer(act.Get("Flags")); ok {
		flags = uint32(v)
	}
	switch s {
	case "":
		return a, false
	case "ResetForm":
		a.Fields, _ = fr.targets(act.Get("Fields"))
		a.Exclude = flags&1 != 0 && len(a.Fields) > 0
	case "Hide":
		a.Fields, a.annots = fr.targets(act.Get("T"))
		a.Hide = true
		if h, ok := d.resolve(act.Get("H")).Bool(); ok {
			a.Hide = h
		}
	case "SubmitForm":
		a.Fields, _ = fr.targets(act.Get("Fields"))
		a.Exclude = flags&1 != 0 && len(a.Fields) > 0
		a.URL, a.Flags = d.fileSpec(act.Get("F")), flags
		switch {
		case flags&(1<<2) != 0:
			a.Format, a.Get = "HTML", flags&(1<<3) != 0
		case flags&(1<<5) != 0:
			a.Format = "XFDF"
		case flags&(1<<8) != 0:
			a.Format = "PDF"
		default:
			a.Format = "FDF"
		}
	case "ImportData":
		a.URL = d.fileSpec(act.Get("F"))
	case "JavaScript":
		js := d.resolve(act.Get("JS"))
		if st, ok := js.Stream(); ok {
			if b, _, err := d.r.DecodeStrict(st); err == nil {
				a.Script = decodeTextString(b)
			}
		} else {
			a.Script = textString(js)
		}
		if len(a.Script) > maxInfoString {
			a.Script = a.Script[:maxInfoString]
		}
	default:
		a.Target = d.oneAction(act)
	}
	return a, true
}

// targets reads what an action names: fields by their names or as
// objects, which are given by their names too, and for a Hide action
// annotations, of which the places are returned. One alone need not be in
// an array.
func (fr *formReader) targets(o pdf.Object) (names []string, annots []annotPlace) {
	d := fr.d
	list, ok := d.resolve(o).Array()
	if !ok {
		list = pdf.Array{o}
	}
	for _, t := range list[:min(len(list), maxFormDataFields)] {
		ref, isRef := t.Ref()
		t = d.resolve(t)
		if _, ok := t.Str(); ok {
			if name := textString(t); name != "" {
				names = append(names, name)
			}
			continue
		}
		node, ok := t.Dict()
		if !ok {
			continue
		}
		if isRef {
			if pl, ok := fr.place[ref]; ok && !node.Has("T") && !node.Has("Kids") {
				annots = append(annots, pl) // an annotation: a widget below its field
				continue
			}
		}
		// A field, or a node above fields: its name, from the top.
		var parts []string
		for n, i := node, 0; !n.IsZero() && i < maxFieldDepth; n, i = d.dict(n.Get("Parent")), i+1 {
			if p := textString(d.resolve(n.Get("T"))); p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) > 0 {
			slices.Reverse(parts)
			names = append(names, strings.Join(parts, "."))
		}
	}
	return names, annots
}

// fields returns the fields of the form the action is for: those it
// names, or all but those.
func (a *WidgetAction) fields(form *Form) []*Field {
	if form == nil {
		return nil
	}
	named := func(f *Field) bool {
		for _, n := range a.Fields {
			if n != "" && (f.Name == n || strings.HasPrefix(f.Name, n+".")) {
				return true
			}
		}
		return false
	}
	var out []*Field
	for _, f := range form.Fields {
		if len(a.Fields) == 0 || named(f) != a.Exclude {
			out = append(out, f)
		}
	}
	return out
}

// Do carries out an action that changes the state: ResetForm sets the
// fields it names to their default values, and Hide shows or hides
// widgets, which renders with the state and a [FormLayer] obey. It
// reports whether the action was one of them; the others are the
// viewer's: where to go (Target), what to send ([FormState.Submission]),
// a script it may know.
func (s *FormState) Do(a WidgetAction) bool {
	switch a.Kind {
	case "ResetForm":
		s.ResetFields(a.fields(s.form)...)
	case "Hide":
		var ws []*Widget
		if len(a.Fields) > 0 {
			named := a
			named.Exclude = false
			for _, f := range named.fields(s.form) {
				ws = append(ws, f.Widgets...)
			}
		}
		for _, pl := range a.annots {
			if w := s.form.byAnn[pl]; w != nil {
				ws = append(ws, w)
			}
		}
		for _, w := range ws {
			s.SetHidden(w, a.Hide)
		}
	default:
		return false
	}
	return true
}

// ResetFields sets the fields to their default values (/DV), or to empty
// if they have none, as Reset does for all, and calls subscribers for
// those that change.
func (s *FormState) ResetFields(fields ...*Field) {
	s.mu.Lock()
	var changed []*Field
	for _, f := range fields {
		if f == nil || f.form != s.form || f.Type == FieldPushButton || f.Type == FieldSignature {
			continue
		}
		if !s.value(f).Equal(f.Default) {
			changed = append(changed, f)
		}
		if f.Default.Equal(f.Saved) {
			delete(s.vals, f)
		} else {
			s.vals[f] = f.Default
		}
	}
	for _, f := range s.recalculate() {
		if !slices.Contains(changed, f) {
			changed = append(changed, f)
		}
	}
	subs := s.subscribers()
	s.mu.Unlock()
	for _, f := range changed {
		for _, fn := range subs {
			fn(f)
		}
	}
}

// Hidden reports whether the widget is hidden: as the state says where a
// Hide action or SetHidden said so, else as the document does
// ([AnnotHidden]).
func (s *FormState) Hidden(w *Widget) bool {
	if w == nil {
		return false
	}
	if s != nil {
		s.mu.Lock()
		h, ok := s.hide[w]
		s.mu.Unlock()
		if ok {
			return h
		}
	}
	return w.Flags&AnnotHidden != 0
}

// SetHidden hides the widget or shows it, in renders with the state and
// in a [FormLayer], and calls subscribers with its field when that
// changes what is shown. The document is not changed, and [Editor.SetFields]
// does not write it.
func (s *FormState) SetHidden(w *Widget, hidden bool) {
	if w == nil || w.Field == nil || w.Field.form != s.form {
		return
	}
	s.mu.Lock()
	was, set := s.hide[w]
	if !set {
		was = w.Flags&AnnotHidden != 0
	}
	if hidden == (w.Flags&AnnotHidden != 0) {
		delete(s.hide, w)
	} else {
		if s.hide == nil {
			s.hide = map[*Widget]bool{}
		}
		s.hide[w] = hidden
	}
	subs := s.subscribers()
	s.mu.Unlock()
	if was != hidden {
		for _, fn := range subs {
			fn(w.Field)
		}
	}
}

// hidden returns the widgets the state shows otherwise than the document
// does, nil for none.
func (s *FormState) hidden() map[*Widget]bool {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hide) == 0 {
		return nil
	}
	m := make(map[*Widget]bool, len(s.hide))
	for w, h := range s.hide {
		m[w] = h
	}
	return m
}

// ErrSubmitDocument is returned by [FormState.Submission] for an action
// that sends the document itself, which is the viewer's to write.
var ErrSubmitDocument = errors.New("cera: the action submits the document, not the values of its fields")

// Submission returns what a SubmitForm action sends, for the viewer to
// send to a.URL if it will: the values of the fields the action names as
// FDF, as XFDF, or as an HTML form sends them (contentType
// "application/x-www-form-urlencoded"; with a.Get they belong into the
// query of the address). Fields flagged FfNoExport are left out, and
// fields without a value unless the action asks for them. cera sends
// nothing. It returns [ErrSubmitDocument] for the format "PDF".
func (s *FormState) Submission(a WidgetAction) (body []byte, contentType string, err error) {
	if a.Kind != "SubmitForm" {
		return nil, "", fmt.Errorf("cera: a %s action submits nothing", a.Kind)
	}
	if a.Format == "PDF" {
		return nil, "", ErrSubmitDocument
	}
	empty := a.Flags&(1<<1) != 0 // IncludeNoValueFields
	var data []formDatum
	fields := a.fields(s.form)
	for _, d := range s.formData() {
		f := s.form.Field(d.name)
		if f == nil || !slices.Contains(fields, f) || f.Flags&FfNoExport != 0 {
			continue
		}
		if !empty && (len(d.values) == 0 || len(d.values) == 1 && d.values[0] == "") {
			continue
		}
		data = append(data, d)
	}
	var out bytes.Buffer
	switch a.Format {
	case "XFDF":
		err = s.writeXFDF(&out, data)
		contentType = "application/vnd.adobe.xfdf"
	case "HTML":
		out.WriteString(encodeForm(data))
		contentType = "application/x-www-form-urlencoded"
	default:
		err = s.writeFDF(&out, data)
		contentType = "application/vnd.fdf"
	}
	return out.Bytes(), contentType, err
}

// encodeForm encodes data as an HTML form does, in its order.
func encodeForm(data []formDatum) string {
	var b strings.Builder
	for _, d := range data {
		for _, v := range d.values {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(d.name) + "=" + url.QueryEscape(v))
		}
	}
	return b.String()
}
