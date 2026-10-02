# 0006. Interactive forms and `FormWidgetProvider`

- Status: proposed
- Date: 2026-10-01
- Milestone: M7½
- Depends on: ADR 0005 (annotations), ADR 0004 (optional content)

## Context

AcroForm fields are widget annotations (`/Subtype /Widget`) tied to fields
of the catalog's `/AcroForm` tree. With ADR 0005 cera draws their
appearance streams, which shows a *filled* form as it was saved. A viewer
needs more:

- **Editing**: text fields, check boxes, radio buttons, combo and list
  boxes, push buttons, signature fields must accept input.
- **Native look and behaviour**: cursor, selection, IME, clipboard,
  accessibility, focus order, dropdowns. Rebuilding a text editor inside a
  rasterizer is out of scope and would be worse than the toolkit's own.
- **No toolkit dependency in cera**: cera is pure Go and builds for every
  GOOS/GOARCH including `js/wasm`. Fyne, Gio or a web front end must not
  become a dependency (fyne needs cgo on desktop).
- **Appearance streams are stale** once a value changes, and some files
  set `/NeedAppearances` and carry none at all.

The idea: the viewer hands cera a **`FormWidgetProvider`**. cera knows
*what* widget belongs *where*; the provider knows *how* to draw it with its
toolkit — "draw me a text-field widget at xyz". With fyne, the widgets live
in an overlay container above the rendered page image.

## Decision

### 1. Form model in cera

`Document.Form()` reads `/AcroForm` once (fields are inherited down the
tree; `/Kids`, `/Parent`, `/T`, `/FT`, `/Ff`, `/V`, `/DV`, `/Opt`, `/DA`,
`/Q`, `/MaxLen`, `/TI`, `/I` are resolved with inheritance):

```go
type Form struct {
	Fields []*Field // terminal fields, in tree order
	// NeedAppearances is /NeedAppearances: widget appearances must be
	// generated rather than taken from /AP.
	NeedAppearances bool
}

type FieldType uint8 // FieldText, FieldCheckBox, FieldRadio, FieldPushButton,
                     // FieldComboBox, FieldListBox, FieldSignature

type Field struct {
	Name     string // fully qualified, "address.street"
	Alt      string // /TU, tooltip and accessible name
	Type     FieldType
	Flags    FieldFlags // ReadOnly, Required, Multiline, Password, Comb,
	                    // DoNotScroll, Edit, MultiSelect, NoToggleToOff, ...
	Default  Value
	Options  []Option // choice fields: export value and display text
	MaxLen   int
	Widgets  []*Widget
}

type Widget struct {
	Field      *Field
	Page       int
	Annotation int      // index in the page's /Annots (ADR 0005)
	Rect       Rect     // default user space of the page
	Rotation   int      // /MK /R, degrees, 0/90/180/270
	OnState    string   // check boxes and radio buttons: the name of the
	                    // "on" appearance (the export value)
	Appearance Appearance
}

// Appearance is what the PDF asks the widget to look like, read from /DA,
// /Q and /MK. Providers use it to style native widgets like the PDF.
type Appearance struct {
	FontName   string  // the /DA font's base name, e.g. "Helvetica"
	FontSize   float64 // 0 = auto-size
	TextColor  color.RGBA
	Background *color.RGBA // /MK /BG, nil = transparent
	Border     *color.RGBA // /MK /BC
	BorderWidth float64
	BorderStyle BorderStyle // solid, dashed, beveled, inset, underline
	Align      Align        // /Q
	Caption    string       // /MK /CA (push buttons, check box glyph)
}
```

### 2. Form state belongs to the caller

Values that a user enters are not written into the document. They live in a
`FormState`, a value the caller owns (like `Visibility` in ADR 0004), so the
document and its cached display lists stay immutable and shareable:

```go
type FormState struct{ /* field values keyed by *Field */ }

func (f *Form) NewState() *FormState                // values from /V
func (s *FormState) Value(f *Field) Value
func (s *FormState) SetValue(f *Field, v Value) error // checks type, MaxLen,
                                                      // options, ReadOnly
func (s *FormState) Reset()                          // to /DV
func (s *FormState) Changed() []*Field
func (s *FormState) Subscribe(fn func(*Field)) (cancel func())
```

`Value` is a small sum type: text, a set of selected option indices, or an
on/off state name. Fields with the same name on several pages, and radio
groups, share one value; `SetValue` notifies every widget of the field.

### 3. The provider interface

```go
// A FormWidgetProvider draws form widgets with a UI toolkit, over the
// rendered page. cera tells it which widget to show where; the provider
// owns everything about the widget's look and input.
type FormWidgetProvider interface {
	// Supports reports whether the provider draws w natively. Widgets it
	// does not support are drawn by cera from their appearance (ADR 0005)
	// and are not interactive. Asked once per widget and remembered.
	Supports(w *Widget) bool

	// Show places the widget, creating it on first call and moving or
	// restyling it on later ones. p is in view coordinates (see
	// WidgetPlacement); the provider reads the current value from
	// p.State and writes user input back with p.State.SetValue.
	Show(w *Widget, p WidgetPlacement)

	// Hide removes a widget that left the view (scrolled out, page
	// released, layer switched off, field hidden).
	Hide(w *Widget)
}

// WidgetPlacement is where and how large a widget is in the view.
type WidgetPlacement struct {
	// Rect is the widget's rectangle in device pixels of the whole page at
	// Scale — the same space as RenderOptions.Region — offset by the
	// view's Origin, i.e. directly the position in the overlay.
	Rect image.Rectangle
	// Rotation is the clockwise rotation of the widget's content in the
	// view: page /Rotate plus /MK /R.
	Rotation int
	// Scale is pixels per point, for font sizes and border widths: a
	// provider sets the font to Appearance.FontSize * Scale.
	Scale float64
	State *FormState
	// Focus is true when the widget should take keyboard focus (tab order,
	// see FormLayer.Next).
	Focus bool
}
```

### 4. `FormLayer` drives the provider

The viewer does not compute placements itself. A `FormLayer` per visible page
joins a page, a state and a provider, and translates view changes into
`Show` and `Hide` calls:

```go
func NewFormLayer(p *Page, s *FormState, prov FormWidgetProvider) *FormLayer

// Update is called when the view changes: scroll, zoom, page rotation,
// layer visibility. It calls Show for widgets in the viewport (changed
// placements only) and Hide for those that left it.
func (l *FormLayer) Update(v View)

type View struct {
	Scale    float64
	Viewport image.Rectangle // visible part of the page, device pixels
	Origin   image.Point     // where the page's (0,0) pixel is in the overlay
	Layers   *Visibility     // ADR 0004
	Usage    Usage
}

// Skip is passed as RenderOptions.SkipAnnotation: widgets the provider
// draws natively are left out of the page image, so they are not drawn
// twice. Everything else on the page, including unsupported widgets,
// stays in the image.
func (l *FormLayer) Skip(annotIndex int) bool

// Next returns the widget after w in tab order (/Tabs of the page: row,
// column or structure order), for keyboard navigation across pages.
func (l *FormLayer) Next(w *Widget) *Widget

func (l *FormLayer) Close() // Hide everything
```

Rendering stays exactly as it is: the page image comes from `Page.Render`
with `SkipAnnotation: layer.Skip`; skipped widgets are display-list tags
(ADR 0005), so showing or hiding native widgets never re-interprets the page.

The flow in a viewer:

```
scroll / zoom ──► FormLayer.Update(view) ──► provider.Show / Hide  (overlay)
             └──► Page.Render(dst, {Region, SkipAnnotation: layer.Skip}) (image)
user types   ──► provider ──► FormState.SetValue ──► Subscribe callbacks
                                                 └──► other widgets of the field: Show
```

### 5. Appearance generation for unsupported and printed widgets

Widgets not drawn natively (no provider, `Supports` false, printing,
`Page.Render` without a layer) must still show the current value. cera
generates their appearance from the `FormState` when:

- the value in the state differs from `/V`, or
- `/NeedAppearances` is set, or
- the widget has no `/AP`.

Generation covers text fields (single line, multiline with word wrap, comb,
alignment, auto-size), check boxes and radio buttons (`/MK /CA` glyph from
ZapfDingbats, see ADR 0008), choice fields (combo: the selected text; list:
the visible options with the selection highlighted), using the `/DA` font
and colour. Rich text (`/RV`) is drawn as plain text. Generated appearances
are display-list items of the widget's tag; changing a value re-records only
that widget's range (`Page.Invalidate(annotIndex)`), not the page.

`RenderOptions` gains `Form *FormState`; without it the saved `/V` and
`/AP` are used, as in ADR 0005.

### 6. The fyne adapter lives outside the core module

`github.com/timzifer/cera/form/fyneform` is a **separate Go module** (own
`go.mod`), so the core module never imports fyne or cgo. It implements
`FormWidgetProvider` with:

| field | fyne widget |
|---|---|
| text, single line | `widget.Entry` (password: `widget.PasswordEntry`) |
| text, multiline | `widget.Entry` with `MultiLine` |
| check box | `widget.Check` |
| radio group | one `widget.Check`-like toggle per widget, grouped by field |
| combo box | `widget.Select` (`Edit` flag: `widget.SelectEntry`) |
| list box | `widget.List` (multi-select via check marks) |
| push button | `widget.Button`, caption from `/MK /CA` |
| signature | not supported in v1 (`Supports` false) |

placed with absolute positioning in a `container.NewWithoutLayout` stacked
over the `canvas.Raster` that shows the page image. Rotation other than 0 is
not supported by fyne widgets; such widgets return `Supports` false and fall
back to generated appearances. Other adapters (Gio, a DOM overlay for wasm)
follow the same pattern.

### Out of scope

- **JavaScript** (`/AA`, format, keystroke, validate, calculate actions):
  not executed. Fields with calculate or format actions are still editable;
  computed fields do not update. Exposed as `Field.HasActions` so a viewer
  can warn.
- **Saving**: writing a `FormState` back to a PDF (incremental update) or
  exporting FDF/XFDF needs a writer; separate ADR once go-pdfkit offers one.
  Until then `FormState` exposes the values for the application to export.
- **XFA**: dynamic XFA forms are not supported; static XFA with an AcroForm
  fallback uses the AcroForm. Counted as `xfa` in `Stats.Unsupported`.
- **Digital signature validation**.

## Consequences

- cera stays toolkit-free and wasm-capable; the provider is the only seam,
  and its contract is small (three methods).
- Viewers get native input behaviour (IME, clipboard, accessibility) for
  free from their toolkit, and the page image stays the single source for
  everything else.
- Native widgets can look different from the PDF's appearance. `Appearance`
  gives providers what they need to come close; exact fidelity is reserved
  for print, which always uses generated appearances.
- Two coordinate systems meet at `WidgetPlacement.Rect`; it is defined in
  the same pixel space as `RenderOptions.Region`, so overlay and image
  cannot drift apart under zoom and rotation. A test renders a page with and
  without `Skip` and checks that every placement covers exactly the pixels
  that changed.
- `FormState` is not safe for concurrent `SetValue`; providers call it from
  their UI goroutine. Rendering reads a snapshot.

## Alternatives considered

- **cera draws the widgets itself** (focus ring, caret, dropdowns) into the
  page image: no dependency, but a reimplementation of text editing without
  IME, accessibility or native look, and every keystroke re-renders a band.
- **The viewer reads `/AcroForm` itself**: duplicates inheritance rules,
  appearance placement and rotation in every viewer, and cannot hide the
  drawn appearance from the page image without cera's help.
- **One provider method per field type** (`TextField(...)`,
  `CheckBox(...)`): more explicit, but every new field kind changes the
  interface; `Widget.Field.Type` carries the same information.
