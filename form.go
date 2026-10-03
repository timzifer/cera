package cera

import (
	"image/color"
	"slices"
	"strings"
	"sync"

	"github.com/go-pdfkit/reader"
)

// Interactive forms (PDF 2.0, 12.7): the fields of the catalog's
// /AcroForm and the widget annotations that show them. cera reads the
// field tree once into a Form; the values a user enters live in a
// FormState the caller owns (formstate.go), a FormLayer places native
// widgets of a FormWidgetProvider over the rendered page (formlayer.go),
// and widgets drawn by cera show the state's values through generated
// appearances (formgen.go).

// FieldType is the kind of a form field.
type FieldType uint8

// Field types.
const (
	FieldText FieldType = iota
	FieldCheckBox
	FieldRadio
	FieldPushButton
	FieldComboBox
	FieldListBox
	FieldSignature
)

var fieldTypeNames = [...]string{"text", "checkbox", "radio", "pushbutton", "combobox", "listbox", "signature"}

func (t FieldType) String() string {
	if int(t) < len(fieldTypeNames) {
		return fieldTypeNames[t]
	}
	return "unknown"
}

// FieldFlags are the flags of a field (/Ff). Bits not named here are kept.
type FieldFlags uint32

// Field flags, at their bit positions in /Ff (PDF 2.0, tables 227, 229,
// 231 and 233). FfRichText and FfRadiosInUnison share a bit: the first
// applies to text fields, the second to radio buttons.
const (
	FfReadOnly          FieldFlags = 1 << 0
	FfRequired          FieldFlags = 1 << 1
	FfNoExport          FieldFlags = 1 << 2
	FfMultiline         FieldFlags = 1 << 12
	FfPassword          FieldFlags = 1 << 13
	FfNoToggleToOff     FieldFlags = 1 << 14
	FfRadio             FieldFlags = 1 << 15
	FfPushbutton        FieldFlags = 1 << 16
	FfCombo             FieldFlags = 1 << 17
	FfEdit              FieldFlags = 1 << 18
	FfSort              FieldFlags = 1 << 19
	FfFileSelect        FieldFlags = 1 << 20
	FfMultiSelect       FieldFlags = 1 << 21
	FfDoNotSpellCheck   FieldFlags = 1 << 22
	FfDoNotScroll       FieldFlags = 1 << 23
	FfComb              FieldFlags = 1 << 24
	FfRichText          FieldFlags = 1 << 25
	FfRadiosInUnison    FieldFlags = 1 << 25
	FfCommitOnSelChange FieldFlags = 1 << 26
)

// Form is the interactive form of a document (/AcroForm).
type Form struct {
	// Fields are the terminal fields, in tree order.
	Fields []*Field
	// NeedAppearances is /NeedAppearances: widget appearances must be
	// generated rather than taken from /AP.
	NeedAppearances bool
	// XFA reports an /XFA entry: the document carries an XFA form, of
	// which cera uses only the AcroForm fallback.
	XFA bool

	byName map[string]*Field
	pages  map[int][]*Widget // widgets by page, in /Annots order
	byAnn  map[annotPlace]*Widget
	dr     reader.Dict // default resources
}

// Option is an entry of a choice field: the value exported and the text
// shown, which are the same unless /Opt gives both.
type Option struct {
	Export string
	Text   string
}

// Field is a terminal field of a form.
type Field struct {
	// Name is the fully qualified name, "address.street".
	Name string
	// Alt is /TU, the tooltip and accessible name.
	Alt   string
	Type  FieldType
	Flags FieldFlags
	// Saved is the value the document holds (/V), Default the value a
	// reset restores (/DV).
	Saved   Value
	Default Value
	// Options are the entries of a choice field (/Opt), or the export
	// values of the widgets of a check box or radio button that has /Opt.
	Options []Option
	// MaxLen is the maximum length of a text field, 0 if unlimited.
	MaxLen int
	// TopIndex is the first option a list box shows (/TI).
	TopIndex int
	// HasActions reports JavaScript or other additional actions (/AA)
	// on the field or its widgets (format, keystroke, validate,
	// calculate). cera does not run them: computed fields do not update.
	HasActions bool
	Widgets    []*Widget

	form *Form
	da   string // the inherited /DA
	q    int    // the inherited /Q
}

// Widget is a widget annotation of a field: where and how the field is
// shown on a page.
type Widget struct {
	Field *Field
	// Page is the 0-based page and Annotation the index of the widget in
	// the page's /Annots (Annotation.Index).
	Page       int
	Annotation int
	// Rect is the widget's rectangle in default user space of the page.
	Rect Rect
	// Rotation is /MK /R: the counterclockwise rotation of the widget's
	// content on the page in degrees, 0, 90, 180 or 270.
	Rotation int
	// Flags are the annotation flags.
	Flags AnnotFlags
	// OnState is the name of the "on" appearance of a check box or radio
	// button, its export value; empty for other fields.
	OnState    string
	Appearance Appearance

	dict    reader.Dict
	fontRes reader.Name // the /DA font's resource name
	oc      *ocExpr     // the /OC membership, nil if always visible
}

// BorderStyle is the style of a widget's border (/BS /S).
type BorderStyle uint8

// Border styles.
const (
	BorderSolid BorderStyle = iota
	BorderDashed
	BorderBeveled
	BorderInset
	BorderUnderline
)

// Align is the alignment of text in a field (/Q).
type Align uint8

// Alignments.
const (
	AlignLeft Align = iota
	AlignCenter
	AlignRight
)

// Appearance is what the PDF asks a widget to look like, read from /DA,
// /Q, /MK and /BS. Providers use it to style native widgets like the PDF.
type Appearance struct {
	// FontName is the /DA font's base name, "Helvetica"; FontSize its size
	// in points, 0 for auto-size.
	FontName  string
	FontSize  float64
	TextColor color.RGBA
	// Background is /MK /BG and Border /MK /BC, nil for none.
	Background *color.RGBA
	Border     *color.RGBA
	// BorderWidth is /BS /W (or /Border), in points.
	BorderWidth float64
	BorderStyle BorderStyle
	Align       Align
	// Caption is /MK /CA: the label of a push button, the ZapfDingbats
	// character of a check box or radio button's mark.
	Caption string
}

// annotPlace is an annotation of a page.
type annotPlace struct{ page, index int }

// Form returns the interactive form of the document, read once; nil if the
// document has none or its form has no fields.
func (d *Document) Form() *Form {
	d.formOnce.Do(func() {
		defer func() {
			if recover() != nil {
				d.form = nil // a broken form is no form
			}
		}()
		d.form = d.readForm()
	})
	return d.form
}

// Field returns the field of fully qualified name, nil if none.
func (f *Form) Field(name string) *Field {
	if f == nil {
		return nil
	}
	return f.byName[name]
}

// PageWidgets returns the widgets on page i (0-based) in the order of its
// /Annots. The slice is shared; do not modify it.
func (f *Form) PageWidgets(i int) []*Widget {
	if f == nil {
		return nil
	}
	return f.pages[i]
}

// widget returns the widget that is annotation index of page, nil if none.
func (f *Form) widget(page, index int) *Widget {
	if f == nil {
		return nil
	}
	return f.byAnn[annotPlace{page, index}]
}

// inherited holds the inheritable entries of the field tree (PDF 2.0,
// table 226 and 12.7.4).
type inherited struct {
	ft     reader.Name
	ff     FieldFlags
	v, dv  reader.Object
	da     string
	q      int
	maxLen int
	opt    reader.Object
	ti     int
	aa     bool
}

// formReader reads the field tree.
type formReader struct {
	d     *Document
	f     *Form
	seen  map[reader.Ref]bool
	place map[reader.Ref]annotPlace
}

func (d *Document) readForm() *Form {
	cat, err := d.r.Catalog()
	if err != nil {
		return nil
	}
	af := d.dict(cat["AcroForm"])
	if af == nil {
		return nil
	}
	fields, _ := reader.ToArray(d.resolve(af["Fields"]))
	f := &Form{
		NeedAppearances: d.boolean(af["NeedAppearances"]),
		XFA:             d.resolve(af["XFA"]) != nil,
		byName:          map[string]*Field{},
		pages:           map[int][]*Widget{},
		byAnn:           map[annotPlace]*Widget{},
		dr:              d.dict(af["DR"]),
	}
	if _, isNull := d.resolve(af["XFA"]).(reader.Null); isNull {
		f.XFA = false
	}
	d.xfa = f.XFA
	fr := &formReader{d: d, f: f, seen: map[reader.Ref]bool{}, place: d.annotPlaces()}
	inh := inherited{da: string(stringOf(d, af["DA"]))}
	if q, ok := d.integer(af["Q"]); ok {
		inh.q = q
	}
	for _, o := range fields {
		fr.walk(o, "", inh, 0)
	}
	if len(f.Fields) == 0 {
		return nil
	}
	for _, ws := range f.pages {
		slices.SortFunc(ws, func(a, b *Widget) int { return a.Annotation - b.Annotation })
	}
	return f
}

// annotPlaces finds every annotation of the document that is an indirect
// object: the widgets of fields are found there, whatever their /P says.
func (d *Document) annotPlaces() map[reader.Ref]annotPlace {
	m := map[reader.Ref]annotPlace{}
	for i := range d.NumPages() {
		pd, err := d.r.Page(i + 1)
		if err != nil {
			continue
		}
		arr, _ := reader.ToArray(d.resolve(pd["Annots"]))
		for j, o := range arr {
			if ref, ok := o.(reader.Ref); ok {
				if _, dup := m[ref]; !dup {
					m[ref] = annotPlace{i, j}
				}
			}
		}
	}
	return m
}

func stringOf(d *Document, o reader.Object) []byte {
	b, _ := reader.ToString(d.resolve(o))
	return b
}

// walk reads the field node o, whose parent is called parent.
func (fr *formReader) walk(o reader.Object, parent string, inh inherited, depth int) {
	d := fr.d
	if depth > maxFieldDepth {
		return
	}
	if ref, ok := o.(reader.Ref); ok {
		if fr.seen[ref] {
			return
		}
		fr.seen[ref] = true
	}
	node := d.dict(o)
	if node == nil {
		return
	}
	if n, ok := d.name(node["FT"]); ok {
		inh.ft = n
	}
	if v, ok := d.integer(node["Ff"]); ok {
		inh.ff = FieldFlags(uint32(v))
	}
	if v, ok := node["V"]; ok {
		inh.v = v
	}
	if v, ok := node["DV"]; ok {
		inh.dv = v
	}
	if s, ok := reader.ToString(d.resolve(node["DA"])); ok {
		inh.da = string(s)
	}
	if v, ok := d.integer(node["Q"]); ok {
		inh.q = v
	}
	if v, ok := d.integer(node["MaxLen"]); ok {
		inh.maxLen = v
	}
	if v, ok := node["Opt"]; ok {
		inh.opt = v
	}
	if v, ok := d.integer(node["TI"]); ok {
		inh.ti = v
	}
	if d.dict(node["AA"]) != nil {
		inh.aa = true
	}
	name := parent
	if t := textString(d.resolve(node["T"])); t != "" {
		if name != "" {
			name += "."
		}
		name += t
	}

	kids, _ := reader.ToArray(d.resolve(node["Kids"]))
	var widgets []reader.Object
	fieldKids := false
	for _, k := range kids {
		kd := d.dict(k)
		if kd == nil {
			continue
		}
		if _, hasT := kd["T"]; hasT || kd["Kids"] != nil {
			fieldKids = true
			continue
		}
		widgets = append(widgets, k)
	}
	if fieldKids {
		for _, k := range kids {
			fr.walk(k, name, inh, depth+1)
		}
		return
	}
	if len(kids) == 0 {
		widgets = []reader.Object{o} // the field is its own widget
	}
	fr.field(node, name, inh, widgets)
}

// field adds the terminal field node with its widgets.
func (fr *formReader) field(node reader.Dict, name string, inh inherited, widgets []reader.Object) {
	d := fr.d
	f := &Field{
		Name:       name,
		Alt:        textString(d.resolve(node["TU"])),
		Flags:      inh.ff,
		MaxLen:     max(inh.maxLen, 0),
		TopIndex:   max(inh.ti, 0),
		HasActions: inh.aa,
		form:       fr.f,
		da:         inh.da,
		q:          inh.q,
	}
	switch inh.ft {
	case "Tx":
		f.Type = FieldText
	case "Btn":
		switch {
		case f.Flags&FfPushbutton != 0:
			f.Type = FieldPushButton
		case f.Flags&FfRadio != 0:
			f.Type = FieldRadio
		default:
			f.Type = FieldCheckBox
		}
	case "Ch":
		f.Type = FieldListBox
		if f.Flags&FfCombo != 0 {
			f.Type = FieldComboBox
		}
	case "Sig":
		f.Type = FieldSignature
	default:
		return // not a field cera knows
	}
	f.Options = fr.options(inh.opt)

	for _, o := range widgets {
		ref, ok := o.(reader.Ref)
		if !ok {
			continue
		}
		pl, ok := fr.place[ref]
		if !ok {
			continue // on no page: never shown
		}
		w := fr.widget(f, d.dict(o), pl)
		if w == nil {
			continue
		}
		if d.dict(w.dict["AA"]) != nil {
			f.HasActions = true
		}
		f.Widgets = append(f.Widgets, w)
		if _, dup := fr.f.byAnn[pl]; !dup {
			fr.f.byAnn[pl] = w
			fr.f.pages[pl.page] = append(fr.f.pages[pl.page], w)
		}
	}
	f.Saved = fr.value(f, inh.v)
	f.Default = fr.value(f, inh.dv)
	if f.Default.kind == valueNone {
		f.Default = f.empty()
	}
	if f.Saved.kind == valueNone {
		f.Saved = f.empty()
	}
	fr.f.Fields = append(fr.f.Fields, f)
	if _, dup := fr.f.byName[name]; !dup && name != "" {
		fr.f.byName[name] = f
	}
}

// options reads /Opt: strings, or [export text] pairs.
func (fr *formReader) options(o reader.Object) []Option {
	d := fr.d
	arr, _ := reader.ToArray(d.resolve(o))
	var opts []Option
	for _, e := range arr {
		e = d.resolve(e)
		if pair, ok := reader.ToArray(e); ok {
			if len(pair) < 2 {
				continue
			}
			opts = append(opts, Option{Export: textString(d.resolve(pair[0])), Text: textString(d.resolve(pair[1]))})
			continue
		}
		s := textString(e)
		opts = append(opts, Option{Export: s, Text: s})
	}
	return opts
}

// widget reads the widget annotation wd of f at pl.
func (fr *formReader) widget(f *Field, wd reader.Dict, pl annotPlace) *Widget {
	d := fr.d
	r, ok := d.rect(wd["Rect"])
	if !ok {
		return nil
	}
	w := &Widget{Field: f, Page: pl.page, Annotation: pl.index, Rect: r, dict: wd}
	if v, ok := d.integer(wd["F"]); ok {
		w.Flags = AnnotFlags(uint32(v))
	}
	mk := d.dict(wd["MK"])
	if v, ok := d.integer(mk["R"]); ok {
		w.Rotation = ((v/90)%4 + 4) % 4 * 90
	}
	if f.Type == FieldCheckBox || f.Type == FieldRadio {
		w.OnState = onState(d, wd)
	}
	if oc, ok := wd["OC"]; ok {
		w.oc, _ = d.membership(oc)
	}

	a := &w.Appearance
	da := f.da
	if s, ok := reader.ToString(d.resolve(wd["DA"])); ok {
		da = string(s)
	}
	pda := parseDA(da)
	w.fontRes, a.FontSize, a.TextColor = pda.font, pda.size, pda.color
	if fd := d.dict(d.dict(fr.f.dr["Font"])[pda.font]); fd != nil {
		if n, ok := d.name(fd["BaseFont"]); ok {
			a.FontName = string(n)
			if len(a.FontName) > 7 && a.FontName[6] == '+' {
				a.FontName = a.FontName[7:]
			}
		}
	}
	q := f.q
	if v, ok := d.integer(wd["Q"]); ok {
		q = v
	}
	if q >= 0 && q <= 2 {
		a.Align = Align(q)
	}
	if c, ok := d.annotColor(mk["BG"]); ok && len(c) > 0 {
		a.Background = rgbaOf(c)
	}
	if c, ok := d.annotColor(mk["BC"]); ok && len(c) > 0 {
		a.Border = rgbaOf(c)
	}
	a.BorderWidth, _ = d.border(wd)
	if bs := d.dict(wd["BS"]); bs != nil {
		switch s, _ := d.name(bs["S"]); s {
		case "D":
			a.BorderStyle = BorderDashed
		case "B":
			a.BorderStyle = BorderBeveled
		case "I":
			a.BorderStyle = BorderInset
		case "U":
			a.BorderStyle = BorderUnderline
		}
	}
	a.Caption = textString(d.resolve(mk["CA"]))
	return w
}

// onState returns the name of the appearance of a button that is not Off.
func onState(d *Document, wd reader.Dict) string {
	ap := d.dict(wd["AP"])
	for _, key := range [2]reader.Name{"N", "D"} {
		states := d.dict(ap[key])
		names := make([]string, 0, len(states))
		for k := range states {
			if k != "Off" {
				names = append(names, string(k))
			}
		}
		if len(names) > 0 {
			slices.Sort(names)
			return names[0]
		}
	}
	if as, ok := d.name(wd["AS"]); ok && as != "Off" {
		return string(as)
	}
	return "Yes"
}

// value reads a /V or /DV entry of f.
func (fr *formReader) value(f *Field, o reader.Object) Value {
	d := fr.d
	o = d.resolve(o)
	if o == nil {
		return Value{}
	}
	if _, isNull := o.(reader.Null); isNull {
		return Value{}
	}
	switch f.Type {
	case FieldText:
		if s, ok := o.(*reader.Stream); ok {
			if b, _, err := d.r.DecodeStream(s); err == nil {
				return TextValue(textString(reader.String(b)))
			}
			return Value{}
		}
		return TextValue(textString(o))
	case FieldCheckBox, FieldRadio:
		if n, ok := reader.ToName(o); ok {
			return StateValue(string(n))
		}
		if s, ok := reader.ToString(o); ok {
			return StateValue(textString(reader.String(s)))
		}
	case FieldComboBox, FieldListBox:
		var texts []string
		if arr, ok := reader.ToArray(o); ok {
			for _, e := range arr {
				texts = append(texts, textString(d.resolve(e)))
			}
		} else {
			texts = []string{textString(o)}
		}
		var sel []int
		for _, t := range texts {
			i := f.option(t)
			if i < 0 {
				if f.Type == FieldComboBox && len(texts) == 1 {
					return TextValue(t) // an edited value
				}
				continue
			}
			if !slices.Contains(sel, i) {
				sel = append(sel, i)
			}
		}
		slices.Sort(sel)
		return ChoiceValue(sel...)
	}
	return Value{}
}

// option returns the index of the option whose export value (or else
// text) is s, -1 if none.
func (f *Field) option(s string) int {
	for i, o := range f.Options {
		if o.Export == s {
			return i
		}
	}
	for i, o := range f.Options {
		if o.Text == s {
			return i
		}
	}
	return -1
}

// empty is the value of f when the document gives none.
func (f *Field) empty() Value {
	switch f.Type {
	case FieldText:
		return TextValue("")
	case FieldCheckBox, FieldRadio:
		return StateValue("Off")
	case FieldComboBox, FieldListBox:
		return ChoiceValue()
	}
	return Value{}
}

// da is a parsed default appearance string.
type da struct {
	font  reader.Name
	size  float64
	color color.RGBA
	comps []float64 // the colour as /DA gives it
}

// parseDA reads the font, size and fill colour of a /DA string.
func parseDA(s string) da {
	out := da{color: color.RGBA{0, 0, 0, 255}, comps: []float64{0}}
	var nums []float64
	var lastName reader.Name
	for _, tok := range strings.Fields(s) {
		switch {
		case strings.HasPrefix(tok, "/"):
			lastName = reader.Name(tok[1:])
			nums = nums[:0]
		case tok == "Tf":
			if len(nums) >= 1 {
				out.font, out.size = lastName, max(nums[len(nums)-1], 0)
			}
			nums = nums[:0]
		case tok == "g" || tok == "rg" || tok == "k":
			n := map[string]int{"g": 1, "rg": 3, "k": 4}[tok]
			if len(nums) >= n {
				c := slices.Clone(nums[len(nums)-n:])
				for i := range c {
					c[i] = clamp01(c[i])
				}
				out.comps = c
				out.color = *rgbaOf(c)
			}
			nums = nums[:0]
		default:
			if v, ok := parseNum(tok); ok {
				nums = append(nums, v)
			} else {
				nums = nums[:0]
			}
		}
	}
	return out
}

func parseNum(s string) (float64, bool) {
	var v, frac float64
	neg, dot, digits := false, false, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c == '-' || c == '+') && i == 0:
			neg = c == '-'
		case c == '.' && !dot:
			dot, frac = true, 1
		case c >= '0' && c <= '9':
			digits++
			if dot {
				frac /= 10
				v += float64(c-'0') * frac
			} else {
				v = v*10 + float64(c-'0')
			}
		default:
			return 0, false
		}
	}
	if digits == 0 {
		return 0, false
	}
	if neg {
		v = -v
	}
	return v, true
}

// rgbaOf converts grey, RGB or CMYK components to an opaque colour.
func rgbaOf(c []float64) *color.RGBA {
	var r, g, b float64
	switch len(c) {
	case 1:
		r, g, b = c[0], c[0], c[0]
	case 3:
		r, g, b = c[0], c[1], c[2]
	case 4:
		r, g, b = spaceCMYK.rgb(c)
	default:
		return nil
	}
	return &color.RGBA{uint8(clamp01(r)*255 + 0.5), uint8(clamp01(g)*255 + 0.5), uint8(clamp01(b)*255 + 0.5), 255}
}

// formFonts are fonts that generated appearances use when the form names
// none it can show, one per document.
type formFonts struct {
	once sync.Once
	helv *Font
}
