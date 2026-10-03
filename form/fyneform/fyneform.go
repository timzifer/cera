// Package fyneform shows the form fields of a cera page as native fyne
// widgets (ADR 0006). It is a module of its own, so that cera itself never
// depends on fyne or cgo.
//
// A Provider implements cera.FormWidgetProvider: its Overlay is a
// container without layout that a viewer stacks over the canvas.Raster
// showing the page image, and a cera.FormLayer places the widgets in it:
//
//	prov := fyneform.New()
//	layer := cera.NewFormLayer(page, state, prov)
//	view := container.NewStack(pageRaster, prov.Overlay)
//	layer.Update(cera.View{Scale: scale, Viewport: visible})
//	page.Render(ctx, img, cera.RenderOptions{Scale: scale, Form: state, SkipAnnotation: layer.Skip})
//
// Call Update, and SetValue on the state, on fyne's goroutine (fyne.Do):
// the provider changes widgets from them.
package fyneform

import (
	"slices"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/timzifer/cera"
)

// Provider draws form widgets with fyne.
type Provider struct {
	// Overlay holds the widgets, at the positions of WidgetPlacement.Rect
	// divided by PixelScale.
	Overlay *fyne.Container
	// PixelScale is the number of pixels of the page image per fyne unit:
	// the canvas scale when the image is shown one pixel per device
	// pixel. 0 means 1.
	PixelScale float32
	// Canvas, when set, receives the focus of WidgetPlacement.Focus.
	Canvas fyne.Canvas
	// OnPush is called when a push button is tapped.
	OnPush func(w *cera.Widget)

	shown map[*cera.Widget]*item
}

// item is a widget shown.
type item struct {
	obj    fyne.CanvasObject
	state  *cera.FormState
	update func(v cera.Value) // shows a value set elsewhere
}

// New returns a provider with an empty overlay.
func New() *Provider {
	return &Provider{Overlay: container.NewWithoutLayout(), shown: map[*cera.Widget]*item{}}
}

// Supports reports the widgets fyne can draw: all but signatures, file
// selection fields and widgets whose content /MK /R rotates, since fyne
// widgets do not rotate. Widgets on rotated pages are shown upright.
func (p *Provider) Supports(w *cera.Widget) bool {
	if w.Rotation != 0 {
		return false
	}
	switch w.Field.Type {
	case cera.FieldSignature:
		return false
	case cera.FieldText:
		return w.Field.Flags&cera.FfFileSelect == 0
	}
	return true
}

// Show places w, creating its fyne widget on the first call.
func (p *Provider) Show(w *cera.Widget, pl cera.WidgetPlacement) {
	it := p.shown[w]
	if it == nil || it.state != pl.State {
		if it != nil {
			p.Overlay.Remove(it.obj)
		}
		it = p.create(w, pl.State)
		p.shown[w] = it
		p.Overlay.Add(it.obj)
	}
	it.update(pl.State.Value(w.Field))
	s := p.PixelScale
	if !(s > 0) {
		s = 1
	}
	r := pl.Rect
	it.obj.Move(fyne.NewPos(float32(r.Min.X)/s, float32(r.Min.Y)/s))
	it.obj.Resize(fyne.NewSize(float32(r.Dx())/s, float32(r.Dy())/s))
	if f, ok := it.obj.(fyne.Focusable); ok && pl.Focus && p.Canvas != nil {
		p.Canvas.Focus(f)
	}
	p.Overlay.Refresh()
}

// Hide removes w from the overlay.
func (p *Provider) Hide(w *cera.Widget) {
	if it := p.shown[w]; it != nil {
		p.Overlay.Remove(it.obj)
		delete(p.shown, w)
	}
}

// Object returns the fyne widget showing w, nil if it is not shown.
func (p *Provider) Object(w *cera.Widget) fyne.CanvasObject {
	if it := p.shown[w]; it != nil {
		return it.obj
	}
	return nil
}

// create makes the fyne widget of w, writing input to s.
func (p *Provider) create(w *cera.Widget, s *cera.FormState) *item {
	f := w.Field
	it := &item{state: s}
	readOnly := f.Flags&cera.FfReadOnly != 0
	set := func(v cera.Value) bool { return s.SetValue(f, v) == nil }

	switch f.Type {
	case cera.FieldText:
		var e *widget.Entry
		switch {
		case f.Flags&cera.FfPassword != 0:
			e = widget.NewPasswordEntry()
		case f.Flags&cera.FfMultiline != 0:
			e = widget.NewMultiLineEntry()
			e.Wrapping = fyne.TextWrapWord
		default:
			e = widget.NewEntry()
		}
		e.PlaceHolder = f.Alt
		e.OnChanged = func(text string) {
			if !set(cera.TextValue(text)) {
				e.SetText(s.Value(f).Text()) // too long: undo
			}
		}
		it.obj = e
		it.update = func(v cera.Value) {
			if e.Text != v.Text() {
				e.SetText(v.Text())
			}
		}
		if readOnly {
			e.Disable()
		}

	case cera.FieldCheckBox, cera.FieldRadio:
		c := widget.NewCheck("", nil)
		c.OnChanged = func(on bool) {
			switch {
			case on:
				set(cera.StateValue(w.OnState))
			case s.Value(f).State() == w.OnState && !set(cera.StateValue("Off")):
				c.SetChecked(true) // a radio button that cannot be switched off
			}
		}
		it.obj = c
		it.update = func(v cera.Value) {
			if on := v.State() == w.OnState; c.Checked != on {
				c.SetChecked(on)
			}
		}
		if readOnly {
			c.Disable()
		}

	case cera.FieldComboBox:
		texts := optionTexts(f)
		text := func(v cera.Value) string {
			if sel := v.Selected(); len(sel) > 0 {
				return texts[sel[0]]
			}
			return v.Text()
		}
		if f.Flags&cera.FfEdit != 0 {
			e := widget.NewSelectEntry(texts)
			e.OnChanged = func(t string) { set(cera.TextValue(t)) }
			it.obj = e
			it.update = func(v cera.Value) {
				if t := text(v); e.Text != t {
					e.SetText(t)
				}
			}
			if readOnly {
				e.Disable()
			}
			break
		}
		sel := widget.NewSelect(texts, func(t string) { set(cera.TextValue(t)) })
		it.obj = sel
		it.update = func(v cera.Value) {
			if t := text(v); sel.Selected != t {
				if t == "" {
					sel.ClearSelected()
				} else {
					sel.SetSelected(t)
				}
			}
		}
		if readOnly {
			sel.Disable()
		}

	case cera.FieldListBox:
		texts := optionTexts(f)
		multi := f.Flags&cera.FfMultiSelect != 0
		var selected []int
		var l *widget.List
		l = widget.NewList(
			func() int { return len(texts) },
			func() fyne.CanvasObject { return widget.NewLabel("") },
			func(id widget.ListItemID, o fyne.CanvasObject) {
				mark := "   "
				if slices.Contains(selected, id) {
					mark = "✓ "
				}
				o.(*widget.Label).SetText(mark + texts[id])
			})
		l.OnSelected = func(id widget.ListItemID) {
			l.Unselect(id) // the check marks show the selection
			if readOnly {
				return
			}
			next := []int{id}
			if multi {
				next = slices.Clone(selected)
				if i := slices.Index(next, id); i >= 0 {
					next = slices.Delete(next, i, i+1)
				} else {
					next = append(next, id)
				}
			}
			set(cera.ChoiceValue(next...))
		}
		it.obj = l
		it.update = func(v cera.Value) {
			if !slices.Equal(selected, v.Selected()) {
				selected = slices.Clone(v.Selected())
				l.Refresh()
			}
		}
		if f.TopIndex > 0 {
			l.ScrollTo(f.TopIndex)
		}

	case cera.FieldPushButton:
		b := widget.NewButton(w.Appearance.Caption, func() {
			if p.OnPush != nil {
				p.OnPush(w)
			}
		})
		it.obj = b
		it.update = func(cera.Value) {}
		if readOnly {
			b.Disable()
		}

	default:
		it.obj = widget.NewLabel("")
		it.update = func(cera.Value) {}
	}
	return it
}

func optionTexts(f *cera.Field) []string {
	texts := make([]string, len(f.Options))
	for i, o := range f.Options {
		texts[i] = o.Text
	}
	return texts
}
