package cera

import (
	"errors"
	"fmt"
	"image/color"
	"slices"
	"strconv"
	"strings"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
)

// Making a form (PDF 2.0, 12.7): fields added to pages and fields removed.
// A field is a node of the tree under /AcroForm /Fields, named by the
// partial names from the top down to it, and shows on pages through its
// widgets, which are annotations. A new field is written as a node with
// its widgets below it, with a default appearance (/DA), a font for it in
// the form's resources, and an appearance stream from the generator that
// draws the fields of forms without one.

// NewField describes a form field for [Editor.AddField], or one more
// widget of a field.
type NewField struct {
	// Name is the fully qualified name: "street", or "address.street" for
	// a field below a node "address", which is made if the form has none.
	// The name of a field the document has, or of one added before, adds a
	// widget to that field: another button of a radio group, or the same
	// field shown in another place. What describes the field — all but
	// OnState and Look — is then that field's and is ignored here.
	Name string
	// Type is FieldText (the zero value), FieldCheckBox, FieldRadio,
	// FieldComboBox, FieldListBox, FieldPushButton or FieldSignature.
	Type FieldType
	// Flags are the flags of the field: FfReadOnly, FfRequired,
	// FfMultiline, FfPassword, FfComb, FfEdit, FfMultiSelect and the
	// others. Those that say the type (FfRadio, FfPushbutton, FfCombo)
	// follow Type.
	Flags FieldFlags
	// Alt is the description of the field for a user who does not see it,
	// and the tooltip (/TU).
	Alt string
	// Value is the value the field starts with: a TextValue, a
	// ChoiceValue of Options, or for a check box or a radio group the
	// StateValue of the OnState of the widget that is on.
	Value Value
	// Options are the entries of a choice field.
	Options []Option
	// MaxLen is the most characters of a text field, 0 for any number.
	MaxLen int
	// OnState is the state of this widget of a check box or radio button
	// when it is on, the value the field then has: "Yes" where empty, and
	// for the buttons of a radio group, which need one each, "1", "2" and
	// on.
	OnState string
	// Look is how the widget is drawn. The zero Appearance is black text
	// in Helvetica of a size that fits, with no border and no background;
	// FontName is not used.
	Look Appearance
}

// addedField is a field AddField adds, or the widgets it adds to a field
// of the edited document.
type addedField struct {
	spec    NewField
	base    *Field // the field of the document that gets the widgets; nil for a new one
	widgets []addedWidget
}

// addedWidget is a widget of an added field: on a page of the edited
// document, at rect in its default user space, turned by rotate as /MK /R
// says it.
type addedWidget struct {
	page   int
	rect   Rect
	rotate int
	on     string
	look   Appearance
}

// AddField adds a form field to page i of the result, a page of the
// edited document: rect is where its widget shows, in points of the
// displayed page. A name that is a field's already adds a widget to that
// field. The field is written with an appearance for its value; a
// signature field is empty, for [Editor.Sign] to sign once the document
// is written.
//
// It returns [ErrCertified] for a document whose certification allows no
// such change; an encrypted document needs the permission to modify.
// [Editor.Flatten] in the same editor flattens the form as the document
// has it: write the fields first.
func (e *Editor) AddField(i int, rect Rect, f NewField) error {
	if err := e.check(i); err != nil {
		return err
	}
	ep := &e.pages[i]
	if e.base == nil || !ep.own || ep.blank {
		return errors.New("cera: a field is added to a page of the edited document")
	}
	if !e.unguarded && e.base.signing().mdp != 0 {
		return ErrCertified
	}
	parts := strings.Split(f.Name, ".")
	if slices.Contains(parts, "") {
		return fmt.Errorf("cera: field name %q: want partial names with periods between", f.Name)
	}
	if !(rect.Dx() > 0 && rect.Dy() > 0) {
		return fmt.Errorf("cera: the rectangle %v of the field %q has no area", rect, f.Name)
	}
	pg, err := e.base.Page(ep.index)
	if err != nil {
		return err
	}
	view := pg.Rotated(ep.rotate)
	inv, ok := view.DisplayMatrix().Invert()
	if !ok {
		return errors.New("cera: the page has no area")
	}
	w := addedWidget{page: ep.index, rect: QuadOf(rect).Transform(inv).Bounds(), rotate: view.Rotate, on: f.OnState, look: f.Look}

	// The field the widget is one more of: an added one, or the document's.
	target := -1
	for k, a := range e.addFields {
		if a.spec.Name == f.Name {
			target = k
		}
	}
	var base *Field
	if target < 0 && !e.delFields[f.Name] {
		base = e.base.Form().Field(f.Name)
	}
	typ := f.Type
	switch {
	case target >= 0:
		typ = e.addFields[target].spec.Type
	case base != nil:
		typ = base.Type
		if base.ref.Num <= 0 {
			return fmt.Errorf("cera: the field %q is not an object of its own", f.Name)
		}
	}
	if (target >= 0 || base != nil) && f.Type != 0 && f.Type != typ {
		return fmt.Errorf("cera: the field %q is a %s field, not a %s field", f.Name, typ, f.Type)
	}
	switch typ {
	case FieldText, FieldComboBox, FieldListBox, FieldPushButton, FieldSignature:
	case FieldCheckBox, FieldRadio:
		if w.on == "" {
			w.on = "Yes"
			if typ == FieldRadio {
				n := 1
				if target >= 0 {
					n += len(e.addFields[target].widgets)
				} else if base != nil {
					n += len(base.Widgets)
				}
				w.on = strconv.Itoa(n)
			}
		}
		if w.on == "Off" {
			return fmt.Errorf("cera: the field %q: Off is the state of a button that is not on", f.Name)
		}
	default:
		return fmt.Errorf("cera: the field %q has no type cera adds", f.Name)
	}
	if target >= 0 {
		a := e.addFields[target]
		if a.spec.Value.kind == valueNone {
			a.spec.Value = f.Value
		}
		a.widgets = append(a.widgets, w)
		return nil
	}
	if base != nil {
		e.addFields = append(e.addFields, &addedField{spec: NewField{Name: f.Name, Type: typ}, base: base, widgets: []addedWidget{w}})
		return nil
	}

	// A new field: its name is free, and no field is in its way.
	for _, name := range e.fieldNames() {
		if strings.HasPrefix(name, f.Name+".") || strings.HasPrefix(f.Name, name+".") {
			return fmt.Errorf("cera: the field name %q and the field %q: a field has no fields below it", f.Name, name)
		}
	}
	spec := f
	spec.Options = slices.Clone(f.Options)
	for k := range spec.Options {
		if spec.Options[k].Text == "" {
			spec.Options[k].Text = spec.Options[k].Export
		}
	}
	spec.Flags &^= FfRadio | FfPushbutton | FfCombo
	switch typ {
	case FieldRadio:
		spec.Flags |= FfRadio
	case FieldPushButton:
		spec.Flags |= FfPushbutton
	case FieldComboBox:
		spec.Flags |= FfCombo
	}
	if spec.MaxLen < 0 {
		spec.MaxLen = 0
	}
	// The value, as the field takes it.
	if v := spec.Value; v.kind != valueNone {
		probe := &Field{Name: spec.Name, Type: typ, Flags: spec.Flags, Options: spec.Options, MaxLen: spec.MaxLen,
			Widgets: []*Widget{{OnState: w.on}}}
		switch typ {
		case FieldPushButton, FieldSignature:
			return fmt.Errorf("%w: %s: %s fields have no value", ErrInvalidValue, spec.Name, typ)
		case FieldRadio:
			if v.kind != ValueState && v.kind != ValueText {
				return fmt.Errorf("%w: %s: not a button state", ErrInvalidValue, spec.Name)
			}
			spec.Value = StateValue(v.text) // of this button or of one to come
		default:
			c, err := probe.check(v)
			if err != nil {
				return err
			}
			spec.Value = c
		}
	}
	e.addFields = append(e.addFields, &addedField{spec: spec, widgets: []addedWidget{w}})
	return nil
}

// RemoveField removes the field with the fully qualified name, with its
// widgets: one of the edited document, or one added with AddField. It
// returns [ErrLocked] for a field a signature locks and [ErrCertified]
// for a document whose certification allows no such change. The nodes
// above the field stay, also when it was their last.
func (e *Editor) RemoveField(name string) error {
	for k, a := range e.addFields {
		if a.spec.Name == name && a.base == nil {
			e.addFields = slices.Delete(e.addFields, k, k+1)
			return nil
		}
	}
	var f *Field
	if e.base != nil && !e.delFields[name] {
		f = e.base.Form().Field(name)
	}
	if f == nil {
		return fmt.Errorf("cera: no field %q", name)
	}
	if f.ref.Num <= 0 {
		return fmt.Errorf("cera: the field %q is not an object of its own", name)
	}
	if !e.unguarded {
		sg := e.base.signing()
		if sg.mdp != 0 {
			return ErrCertified
		}
		if sg.locked(name) {
			return ErrLocked
		}
	}
	if e.delFields == nil {
		e.delFields = map[string]bool{}
	}
	e.delFields[name] = true
	// Widgets added to it go with it.
	e.addFields = slices.DeleteFunc(e.addFields, func(a *addedField) bool { return a.base == f })
	delete(e.fields, f)
	return nil
}

// fieldNames returns the names of the fields of the result: those of the
// document that are not removed, and those added.
func (e *Editor) fieldNames() []string {
	var names []string
	if form := e.base.Form(); form != nil {
		for _, f := range form.Fields {
			if !e.delFields[f.Name] {
				names = append(names, f.Name)
			}
		}
	}
	for _, a := range e.addFields {
		if a.base == nil {
			names = append(names, a.spec.Name)
		}
	}
	return names
}

// formed reports whether the editor adds or removes fields.
func (e *Editor) formed() bool { return len(e.addFields) > 0 || len(e.delFields) > 0 }

// formEntry returns the entry key of the form dictionary of the edited
// document as patches leave it.
func (e *Editor) formEntry(patches []pdfedit.Patch, key pdf.Name) pdf.Object {
	d := e.base
	cat, _ := d.r.Catalog()
	root, _ := d.r.Trailer().Get("Root").Ref()
	af := cat.Get("AcroForm")
	if v, ok := lastSet(patches, root, "AcroForm"); ok {
		af = v
	}
	if r, ok := af.Ref(); ok {
		if v, ok := lastSet(patches, r, key); ok {
			return v
		}
	}
	return d.dict(af).Get(key)
}

// formSet sets entries of the form dictionary of the edited document: of
// the object it is, or in the catalogue for a form that is direct there,
// and for a document without a form.
func (e *Editor) formSet(patches []pdfedit.Patch, set ...pdf.Entry) []pdfedit.Patch {
	d := e.base
	cat, _ := d.r.Catalog()
	root, _ := d.r.Trailer().Get("Root").Ref()
	af := cat.Get("AcroForm")
	if v, ok := lastSet(patches, root, "AcroForm"); ok {
		af = v
	}
	if r, ok := af.Ref(); ok && !d.dict(af).IsZero() {
		return append(patches, pdfedit.Patch{Ref: r, Set: set})
	}
	var entries []pdf.Entry
	for k, v := range d.dict(af).All() {
		if !slices.ContainsFunc(set, func(s pdf.Entry) bool { return s.Key == k }) {
			entries = append(entries, pdf.Entry{Key: k, Val: v})
		}
	}
	return append(patches, pdfedit.Patch{Ref: root, Set: []pdf.Entry{{Key: "AcroForm", Val: pdf.NewDict(append(entries, set...)...).Object()}}})
}

// fieldTree is the tree of fields while fields are added to it and
// removed: the kids of the nodes that change, and the nodes that are new.
type fieldTree struct {
	e    *Editor
	doc  *pdfedit.Doc
	top  pdf.Array // /Fields
	kids map[pdf.Ref]pdf.Array
	made map[pdf.Ref]*madeNode
}

// madeNode is a new node of the tree: a field, or a node above fields.
type madeNode struct {
	name    string
	parent  pdf.Object // null at the top
	entries []pdf.Entry
	field   bool
}

// kidsOf returns the kids of a node, of the top for the zero Ref.
func (t *fieldTree) kidsOf(node pdf.Ref) pdf.Array {
	if node == (pdf.Ref{}) {
		return t.top
	}
	if a, ok := t.kids[node]; ok || isNew(node) {
		return a // a new node has the kids it was given, and no object of the document
	}
	a, _ := t.e.base.resolve(t.e.base.dict(node.Object()).Get("Kids")).Array()
	return a
}

// isNew reports whether ref names a new object, one of pdfedit.Doc.Objects:
// the document has none for it, though it may have one of its number.
func isNew(ref pdf.Ref) bool { return ref.Gen == pdfedit.NewRef(0).Gen }

func (t *fieldTree) setKids(node pdf.Ref, kids pdf.Array) {
	if node == (pdf.Ref{}) {
		t.top = kids
		return
	}
	t.kids[node] = kids
}

// below returns the node named name under node that fields can go under,
// making it where there is none.
func (t *fieldTree) below(node pdf.Ref, name string) pdf.Ref {
	d := t.e.base
	for _, k := range t.kidsOf(node) {
		ref, ok := k.Ref()
		if !ok {
			continue
		}
		if m := t.made[ref]; m != nil {
			if m.name == name && !m.field {
				return ref
			}
			continue
		}
		if isNew(ref) {
			continue // made elsewhere: not a node to look into
		}
		kd := d.dict(k)
		if textString(d.resolve(kd.Get("T"))) != name {
			continue
		}
		// A node above fields has kids that are named or have kids.
		kids, _ := d.resolve(kd.Get("Kids")).Array()
		for _, x := range kids {
			if xd := d.dict(x); xd.Has("T") || xd.Has("Kids") {
				return ref
			}
		}
	}
	ref := t.add(&madeNode{name: name, parent: refOrNull(node)})
	t.setKids(node, append(slices.Clone(t.kidsOf(node)), ref.Object()))
	return ref
}

func refOrNull(r pdf.Ref) pdf.Object {
	if r == (pdf.Ref{}) {
		return pdf.Null
	}
	return r.Object()
}

// add numbers a new node.
func (t *fieldTree) add(m *madeNode) pdf.Ref {
	t.doc.Objects = append(t.doc.Objects, pdf.Null)
	ref := pdfedit.NewRef(len(t.doc.Objects) - 1)
	t.made[ref] = m
	return ref
}

// fieldPlan writes the fields added and removed: new objects for fields,
// nodes and widgets, and patches of the nodes, the pages and the form
// they hang on.
func (e *Editor) fieldPlan(doc *pdfedit.Doc) {
	d := e.base
	if _, ok := d.r.Trailer().Get("Root").Ref(); !ok {
		return
	}
	top, _ := d.resolve(e.formEntry(doc.Patches, "Fields")).Array()
	t := &fieldTree{e: e, doc: doc, top: slices.Clone(top), kids: map[pdf.Ref]pdf.Array{}, made: map[pdf.Ref]*madeNode{}}
	annots := map[int]pdf.Array{} // of the pages that change
	pageAnnots := func(i int) pdf.Array {
		if a, ok := annots[i]; ok {
			return a
		}
		var a pdf.Array
		if ref, ok := d.r.PageRef(i + 1); ok {
			if pg, err := d.Page(i); err == nil {
				a = e.annots(doc.Patches, ref, pg)
			}
		}
		annots[i] = a
		return a
	}

	// Removed: the field from the node it hangs on, its widgets from
	// their pages, and from the order of calculation.
	form := d.Form()
	gone := map[pdf.Ref]bool{}
	for _, f := range form.fieldsOf() {
		if !e.delFields[f.Name] {
			continue
		}
		node := d.dict(f.ref.Object())
		parent, _ := node.Get("Parent").Ref()
		if !slices.Contains(t.kidsOf(parent), f.ref.Object()) {
			parent = pdf.Ref{} // not where it says: look at the top
		}
		t.setKids(parent, slices.DeleteFunc(slices.Clone(t.kidsOf(parent)), func(o pdf.Object) bool { return o == f.ref.Object() }))
		gone[f.ref] = true
		kids, _ := d.resolve(node.Get("Kids")).Array()
		for _, k := range kids {
			if r, ok := k.Ref(); ok {
				gone[r] = true
			}
		}
	}
	if len(gone) > 0 {
		for i := range d.NumPages() {
			pd, err := d.r.Page(i + 1)
			if err != nil {
				continue
			}
			arr, _ := d.resolve(pd.Get("Annots")).Array()
			if slices.ContainsFunc(arr, func(o pdf.Object) bool { r, ok := o.Ref(); return ok && gone[r] }) {
				annots[i] = slices.DeleteFunc(pageAnnots(i), func(o pdf.Object) bool { r, ok := o.Ref(); return ok && gone[r] })
			}
		}
		if co, ok := d.resolve(e.formEntry(doc.Patches, "CO")).Array(); ok {
			if kept := slices.DeleteFunc(slices.Clone(co), func(o pdf.Object) bool { r, ok := o.Ref(); return ok && gone[r] }); len(kept) != len(co) {
				doc.Patches = e.formSet(doc.Patches, pdf.Entry{Key: "CO", Val: kept.Object()})
			}
		}
	}

	// The font of the default appearance: the form's /Helv, or Helvetica
	// under that name.
	dr := d.dict(e.formEntry(doc.Patches, "DR"))
	fonts := d.dict(dr.Get("Font"))
	needFont := false
	lookForm := &Form{dr: dr, byName: map[string]*Field{}}

	for _, a := range e.addFields {
		spec := &a.spec
		var fieldRef pdf.Ref
		var f *Field
		var m *madeNode
		if a.base != nil {
			// More widgets of a field of the document. One that is its
			// own widget becomes a node above it first.
			f, fieldRef = a.base, a.base.ref
			node := patched(doc.Patches, fieldRef, d.dict(fieldRef.Object()))
			if _, has := t.kids[fieldRef]; !has && !node.Has("Kids") {
				var keep, move []pdf.Entry
				var del []pdf.Name
				for k, v := range node.All() {
					if fieldKeys[k] {
						keep = append(keep, pdf.Entry{Key: k, Val: v})
						continue
					}
					move = append(move, pdf.Entry{Key: k, Val: v})
					del = append(del, k)
				}
				doc.Objects = append(doc.Objects, pdf.NewDict(append(move, pdf.Entry{Key: "Parent", Val: fieldRef.Object()})...).Object())
				widget := pdfedit.NewRef(len(doc.Objects) - 1).Object()
				doc.Patches = append(doc.Patches, pdfedit.Patch{Ref: fieldRef, Del: del})
				t.kids[fieldRef] = pdf.Array{widget}
				// The page shows the widget in the place of the field.
				for _, w := range f.Widgets {
					if w.ref == fieldRef {
						arr := slices.Clone(pageAnnots(w.Page))
						for k, o := range arr {
							if o == fieldRef.Object() {
								arr[k] = widget
							}
						}
						annots[w.Page] = arr
					}
				}
			}
		} else {
			parts := strings.Split(spec.Name, ".")
			var node pdf.Ref
			for _, p := range parts[:len(parts)-1] {
				node = t.below(node, p)
			}
			f = &Field{Name: spec.Name, Alt: spec.Alt, Type: spec.Type, Flags: spec.Flags, Options: spec.Options, MaxLen: spec.MaxLen, form: lookForm}
			look := a.widgets[0].look
			f.da = defaultAppearance(look)
			f.q = int(look.Align)
			needFont = true
			m = &madeNode{name: parts[len(parts)-1], parent: refOrNull(node), field: true}
			fieldRef = t.add(m)
			t.setKids(node, append(slices.Clone(t.kidsOf(node)), fieldRef.Object()))
			f.Saved = f.empty()
			if spec.Value.kind != valueNone {
				f.Saved = spec.Value
			}
			// The state of a button that is none of its widgets': off.
			if f.Type == FieldCheckBox || f.Type == FieldRadio {
				if !slices.ContainsFunc(a.widgets, func(w addedWidget) bool { return w.on == f.Saved.State() }) {
					f.Saved = StateValue("Off")
				}
			}
		}
		value := f.Saved
		if a.base != nil {
			if v, ok := e.fields[f]; ok {
				value = v
			}
		}

		for _, aw := range a.widgets {
			wd := []pdf.Entry{
				{Key: "Type", Val: pdf.Name("Annot").Object()},
				{Key: "Subtype", Val: pdf.Name("Widget").Object()},
				{Key: "Rect", Val: rectArray(aw.rect)},
				{Key: "F", Val: pdf.Integer(int64(AnnotPrint))},
				{Key: "Parent", Val: fieldRef.Object()},
			}
			if ref, ok := d.r.PageRef(aw.page + 1); ok {
				wd = append(wd, pdf.Entry{Key: "P", Val: ref.Object()})
			}
			wd = append(wd, lookEntries(aw.look, aw.rotate)...)
			if a.base != nil {
				// Its own default appearance, where it is not the field's.
				if da := defaultAppearance(aw.look); aw.look != (Appearance{}) && da != f.da {
					wd = append(wd, pdf.Entry{Key: "DA", Val: pdf.String([]byte(da))})
					needFont = true
				}
			}
			w := (&formReader{d: d, f: f.form}).widget(f, pdf.NewDict(wd...), annotPlace{aw.page, len(pageAnnots(aw.page))})
			if w != nil {
				w.OnState = aw.on
				switch f.Type {
				case FieldCheckBox, FieldRadio:
					state := pdf.Name("Off")
					if value.State() == aw.on {
						state = pdf.Name(aw.on)
					}
					var states []pdf.Entry
					if ref, ok := e.appearance(w, StateValue(aw.on), &doc.Objects); ok {
						states = append(states, pdf.Entry{Key: pdf.Name(aw.on), Val: ref.Object()})
					}
					if ref, ok := e.appearance(w, StateValue("Off"), &doc.Objects); ok {
						states = append(states, pdf.Entry{Key: "Off", Val: ref.Object()})
					}
					wd = append(wd, pdf.Entry{Key: "AS", Val: state.Object()},
						pdf.Entry{Key: "AP", Val: pdf.NewDict(pdf.Entry{Key: "N", Val: pdf.NewDict(states...).Object()}).Object()})
				case FieldSignature:
				default:
					if ref, ok := e.appearance(w, value, &doc.Objects); ok {
						wd = append(wd, pdf.Entry{Key: "AP", Val: pdf.NewDict(pdf.Entry{Key: "N", Val: ref.Object()}).Object()})
					}
				}
			}
			doc.Objects = append(doc.Objects, pdf.NewDict(wd...).Object())
			widget := pdfedit.NewRef(len(doc.Objects) - 1).Object()
			t.setKids(fieldRef, append(slices.Clone(t.kidsOf(fieldRef)), widget))
			annots[aw.page] = append(slices.Clone(pageAnnots(aw.page)), widget)
		}

		if m != nil {
			m.entries = fieldEntries(f, spec, value)
		}
	}

	// The nodes made, the kids that changed, the pages, and the form.
	for ref, m := range t.made {
		entries := append([]pdf.Entry{{Key: "T", Val: pdf.String(encodeText(m.name))}}, m.entries...)
		if !m.parent.IsNull() {
			entries = append(entries, pdf.Entry{Key: "Parent", Val: m.parent})
		}
		entries = append(entries, pdf.Entry{Key: "Kids", Val: t.kids[ref].Object()})
		doc.Objects[ref.Num] = pdf.NewDict(entries...).Object()
	}
	changed := make([]pdf.Ref, 0, len(t.kids))
	for ref := range t.kids {
		if t.made[ref] == nil {
			changed = append(changed, ref)
		}
	}
	slices.SortFunc(changed, func(a, b pdf.Ref) int { return int(a.Num - b.Num) })
	for _, ref := range changed {
		doc.Patches = append(doc.Patches, pdfedit.Patch{Ref: ref, Set: []pdf.Entry{{Key: "Kids", Val: t.kids[ref].Object()}}})
	}
	pages := make([]int, 0, len(annots))
	for i := range annots {
		pages = append(pages, i)
	}
	slices.Sort(pages)
	for _, i := range pages {
		if ref, ok := d.r.PageRef(i + 1); ok {
			doc.Patches = append(doc.Patches, pdfedit.Patch{Ref: ref, Set: []pdf.Entry{{Key: "Annots", Val: annots[i].Object()}}})
		}
	}
	set := []pdf.Entry{{Key: "Fields", Val: t.top.Object()}}
	if needFont && !fonts.Has("Helv") {
		var fe, de []pdf.Entry
		for k, v := range fonts.All() {
			fe = append(fe, pdf.Entry{Key: k, Val: v})
		}
		fe = append(fe, pdf.Entry{Key: "Helv", Val: e.gen.helvetica()})
		for k, v := range dr.All() {
			if k != "Font" {
				de = append(de, pdf.Entry{Key: k, Val: v})
			}
		}
		set = append(set, pdf.Entry{Key: "DR", Val: pdf.NewDict(append(de, pdf.Entry{Key: "Font", Val: pdf.NewDict(fe...).Object()})...).Object()})
	}
	doc.Patches = e.formSet(doc.Patches, set...)
}

// patched returns the dictionary d of object ref as patches leave it.
func patched(patches []pdfedit.Patch, ref pdf.Ref, d pdf.Dict) pdf.Dict {
	for _, p := range patches {
		if p.Ref != ref {
			continue
		}
		var entries []pdf.Entry
		for k, v := range d.All() {
			if !slices.Contains(p.Del, k) && !slices.ContainsFunc(p.Set, func(s pdf.Entry) bool { return s.Key == k }) {
				entries = append(entries, pdf.Entry{Key: k, Val: v})
			}
		}
		d = pdf.NewDict(append(entries, p.Set...)...)
	}
	return d
}

// fieldsOf returns the fields of a form, none for no form.
func (f *Form) fieldsOf() []*Field {
	if f == nil {
		return nil
	}
	return f.Fields
}

// fieldKeys are the entries of a field that is its own widget which are
// the field's: the others are the widget's.
var fieldKeys = map[pdf.Name]bool{
	"FT": true, "Parent": true, "Kids": true, "T": true, "TU": true, "TM": true, "Ff": true, "V": true, "DV": true,
	"DA": true, "Q": true, "DS": true, "RV": true, "Opt": true, "MaxLen": true, "TI": true, "I": true, "Lock": true, "SV": true,
}

// defaultAppearance returns the /DA of a look: the font /Helv of the
// form's resources at its size, and its colour.
func defaultAppearance(a Appearance) string {
	c := a.TextColor
	size := strconv.FormatFloat(max(a.FontSize, 0), 'f', -1, 64)
	if c.A == 0 || c.R == 0 && c.G == 0 && c.B == 0 {
		return "/Helv " + size + " Tf 0 g"
	}
	u := func(v uint8) string { return strconv.FormatFloat(float64(v)/255, 'f', 3, 64) }
	return "/Helv " + size + " Tf " + u(c.R) + " " + u(c.G) + " " + u(c.B) + " rg"
}

// lookEntries returns what a widget dictionary says of how it is drawn:
// /MK with the colours, the caption and the rotation, and /BS.
func lookEntries(a Appearance, rotate int) []pdf.Entry {
	rgb := func(c *color.RGBA) pdf.Object {
		return pdf.Array{pdf.Real(float64(c.R) / 255), pdf.Real(float64(c.G) / 255), pdf.Real(float64(c.B) / 255)}.Object()
	}
	var mk, out []pdf.Entry
	if a.Background != nil {
		mk = append(mk, pdf.Entry{Key: "BG", Val: rgb(a.Background)})
	}
	if a.Border != nil {
		mk = append(mk, pdf.Entry{Key: "BC", Val: rgb(a.Border)})
	}
	if a.Caption != "" {
		mk = append(mk, pdf.Entry{Key: "CA", Val: pdf.String(encodeText(a.Caption))})
	}
	if rotate != 0 {
		mk = append(mk, pdf.Entry{Key: "R", Val: pdf.Integer(int64(rotate))})
	}
	if len(mk) > 0 {
		out = append(out, pdf.Entry{Key: "MK", Val: pdf.NewDict(mk...).Object()})
	}
	if a.Border != nil {
		width := a.BorderWidth
		if width <= 0 {
			width = 1
		}
		style := map[BorderStyle]pdf.Name{BorderDashed: "D", BorderBeveled: "B", BorderInset: "I", BorderUnderline: "U"}[a.BorderStyle]
		if style == "" {
			style = "S"
		}
		out = append(out, pdf.Entry{Key: "BS", Val: pdf.NewDict(
			pdf.Entry{Key: "W", Val: pdf.Real(width)},
			pdf.Entry{Key: "S", Val: style.Object()},
		).Object()})
	}
	return out
}

// fieldEntries returns the entries of a new field but its name and what
// it hangs on and what hangs on it.
func fieldEntries(f *Field, spec *NewField, v Value) []pdf.Entry {
	ft := map[FieldType]pdf.Name{FieldText: "Tx", FieldCheckBox: "Btn", FieldRadio: "Btn", FieldPushButton: "Btn",
		FieldComboBox: "Ch", FieldListBox: "Ch", FieldSignature: "Sig"}[f.Type]
	out := []pdf.Entry{{Key: "FT", Val: ft.Object()}}
	if f.Flags != 0 {
		out = append(out, pdf.Entry{Key: "Ff", Val: pdf.Integer(int64(f.Flags))})
	}
	if spec.Alt != "" {
		out = append(out, pdf.Entry{Key: "TU", Val: pdf.String(encodeText(spec.Alt))})
	}
	if f.Type == FieldSignature {
		return out
	}
	out = append(out, pdf.Entry{Key: "DA", Val: pdf.String([]byte(f.da))})
	if f.q != 0 {
		out = append(out, pdf.Entry{Key: "Q", Val: pdf.Integer(int64(f.q))})
	}
	switch f.Type {
	case FieldText:
		if f.MaxLen > 0 {
			out = append(out, pdf.Entry{Key: "MaxLen", Val: pdf.Integer(int64(f.MaxLen))})
		}
		if v.Text() != "" {
			out = append(out, pdf.Entry{Key: "V", Val: pdf.String(encodeText(v.Text()))})
		}
	case FieldCheckBox, FieldRadio:
		state := v.State()
		if state == "" {
			state = "Off"
		}
		out = append(out, pdf.Entry{Key: "V", Val: pdf.Name(state).Object()})
	case FieldComboBox, FieldListBox:
		opts := make(pdf.Array, len(f.Options))
		for i, o := range f.Options {
			if o.Text == "" || o.Text == o.Export {
				opts[i] = pdf.String(encodeText(o.Export))
				continue
			}
			opts[i] = pdf.Array{pdf.String(encodeText(o.Export)), pdf.String(encodeText(o.Text))}.Object()
		}
		out = append(out, pdf.Entry{Key: "Opt", Val: opts.Object()})
		set, _ := choiceEntries(f, v)
		out = append(out, set...)
	}
	return out
}
