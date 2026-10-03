package cera

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"unicode/utf8"
)

// ValueKind tells the kinds of Value apart.
type ValueKind uint8

const (
	valueNone ValueKind = iota
	// ValueText is the text of a text field, or of an editable combo box
	// whose text is none of its options.
	ValueText
	// ValueState is the state of a check box or radio button: the name of
	// the "on" appearance of the widget that is on, or "Off".
	ValueState
	// ValueChoice is the set of selected options of a choice field.
	ValueChoice
)

// Value is the value of a field: a text, a button state or a set of
// selected options. The zero value is no value (push buttons and
// signatures). Values are immutable.
type Value struct {
	kind ValueKind
	text string // the text or the state name
	sel  []int  // selected options, ascending; never modified
}

// TextValue returns a text value.
func TextValue(s string) Value { return Value{kind: ValueText, text: s} }

// StateValue returns the state of a check box or radio button: a widget's
// OnState, or "Off".
func StateValue(name string) Value { return Value{kind: ValueState, text: name} }

// ChoiceValue returns the selection of the options at the given indices of
// Field.Options.
func ChoiceValue(indices ...int) Value {
	sel := slices.Clone(indices)
	slices.Sort(sel)
	return Value{kind: ValueChoice, sel: slices.Compact(sel)}
}

// Kind returns the kind of v; 0 for no value.
func (v Value) Kind() ValueKind { return v.kind }

// Text returns the text of a text value, or the state name of a state.
func (v Value) Text() string { return v.text }

// State returns the state name of a button state, "" for other values.
func (v Value) State() string {
	if v.kind != ValueState {
		return ""
	}
	return v.text
}

// Selected returns the indices of the selected options, ascending. The
// slice is shared; do not modify it.
func (v Value) Selected() []int { return v.sel }

// Equal reports whether v and w are the same value.
func (v Value) Equal(w Value) bool {
	return v.kind == w.kind && v.text == w.text && slices.Equal(v.sel, w.sel)
}

// Errors of FormState.SetValue.
var (
	ErrReadOnly     = errors.New("cera: field is read-only")
	ErrInvalidValue = errors.New("cera: invalid field value")
)

// FormState holds the values of a form's fields as a user edits them. It
// belongs to the caller, like a Visibility: the document and the display
// lists of its pages are not changed, so one document can be shown with
// several states. Fields with several widgets, and radio groups, share one
// value. A FormState is safe for concurrent use; callbacks run on the
// goroutine that changed the value.
type FormState struct {
	form *Form

	mu   sync.Mutex
	vals map[*Field]Value // values set; others are Field.Saved
	subs map[int]func(*Field)
	next int
}

// NewState returns a state holding the values the document saved (/V).
func (f *Form) NewState() *FormState {
	return &FormState{form: f, vals: map[*Field]Value{}}
}

// Form returns the form the state is of.
func (s *FormState) Form() *Form { return s.form }

// Value returns the value of f.
func (s *FormState) Value(f *Field) Value {
	if s == nil || f == nil {
		return Value{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value(f)
}

func (s *FormState) value(f *Field) Value {
	if v, ok := s.vals[f]; ok {
		return v
	}
	return f.Saved
}

// SetValue sets the value of f, checking it against the field: its type,
// MaxLen, options, ReadOnly and NoToggleToOff. A text value of a choice
// field selects the option of that export value or text; an editable
// combo box also keeps other text. Subscribers are called when the value
// changes.
func (s *FormState) SetValue(f *Field, v Value) error {
	if f == nil || f.form != s.form {
		return fmt.Errorf("%w: field of another form", ErrInvalidValue)
	}
	if f.Flags&FfReadOnly != 0 {
		return ErrReadOnly
	}
	v, err := f.check(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := s.value(f)
	if old.Equal(v) {
		s.mu.Unlock()
		return nil
	}
	if f.Type == FieldCheckBox || f.Type == FieldRadio {
		if f.Flags&FfNoToggleToOff != 0 && f.Type == FieldRadio && v.text == "Off" && old.text != "Off" {
			s.mu.Unlock()
			return fmt.Errorf("%w: %s cannot be switched off", ErrInvalidValue, f.Name)
		}
	}
	s.vals[f] = v
	subs := s.subscribers()
	s.mu.Unlock()
	for _, fn := range subs {
		fn(f)
	}
	return nil
}

// check validates v for f and returns it in its canonical form.
func (f *Field) check(v Value) (Value, error) {
	bad := func(why string) (Value, error) {
		return Value{}, fmt.Errorf("%w: %s: %s", ErrInvalidValue, f.Name, why)
	}
	switch f.Type {
	case FieldText:
		if v.kind != ValueText {
			return bad("not text")
		}
		if f.MaxLen > 0 && utf8.RuneCountInString(v.text) > f.MaxLen {
			return bad(fmt.Sprintf("longer than %d characters", f.MaxLen))
		}
		return v, nil
	case FieldCheckBox, FieldRadio:
		if v.kind == ValueText {
			v.kind = ValueState
		}
		if v.kind != ValueState {
			return bad("not a button state")
		}
		if v.text == "Off" {
			return v, nil
		}
		for _, w := range f.Widgets {
			if w.OnState == v.text {
				return v, nil
			}
		}
		return bad(fmt.Sprintf("no widget has state %q", v.text))
	case FieldComboBox, FieldListBox:
		if v.kind == ValueText {
			if i := f.option(v.text); i >= 0 {
				return ChoiceValue(i), nil
			}
			if v.text == "" {
				return ChoiceValue(), nil
			}
			if f.Type == FieldComboBox && f.Flags&FfEdit != 0 {
				return v, nil
			}
			return bad(fmt.Sprintf("%q is no option", v.text))
		}
		if v.kind != ValueChoice {
			return bad("not a choice")
		}
		for _, i := range v.sel {
			if i < 0 || i >= len(f.Options) {
				return bad(fmt.Sprintf("option %d out of range", i))
			}
		}
		if len(v.sel) > 1 && (f.Type == FieldComboBox || f.Flags&FfMultiSelect == 0) {
			return bad("one option at most")
		}
		return v, nil
	}
	return bad(f.Type.String() + " fields have no value")
}

// Reset sets every field to its default value (/DV), or to empty if it has
// none, and calls subscribers for those that change.
func (s *FormState) Reset() {
	s.mu.Lock()
	var changed []*Field
	for _, f := range s.form.Fields {
		if f.Type == FieldPushButton || f.Type == FieldSignature {
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
	subs := s.subscribers()
	s.mu.Unlock()
	for _, f := range changed {
		for _, fn := range subs {
			fn(f)
		}
	}
}

// Changed returns the fields whose value differs from the one the
// document saved, in form order.
func (s *FormState) Changed() []*Field {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Field
	for _, f := range s.form.Fields {
		if v, ok := s.vals[f]; ok && !v.Equal(f.Saved) {
			out = append(out, f)
		}
	}
	return out
}

// Subscribe calls fn with every field whose value changes, until cancel
// is called.
func (s *FormState) Subscribe(fn func(*Field)) (cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs == nil {
		s.subs = map[int]func(*Field){}
	}
	id := s.next
	s.next++
	s.subs[id] = fn
	return func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

// subscribers returns the callbacks in subscription order; s.mu is held.
func (s *FormState) subscribers() []func(*Field) {
	ids := make([]int, 0, len(s.subs))
	for id := range s.subs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	fns := make([]func(*Field), len(ids))
	for i, id := range ids {
		fns[i] = s.subs[id]
	}
	return fns
}

// snapshot returns the values that differ from the saved ones, for a
// render; nil if none do.
func (s *FormState) snapshot() map[*Field]Value {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var m map[*Field]Value
	for f, v := range s.vals {
		if !v.Equal(f.Saved) {
			if m == nil {
				m = make(map[*Field]Value, len(s.vals))
			}
			m[f] = v
		}
	}
	return m
}
