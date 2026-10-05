package cera

import (
	"image"
	"math"
	"slices"
	"sync"
)

// A FormWidgetProvider draws form widgets with a UI toolkit, over the
// rendered page. cera tells it which widget to show where; the provider
// owns everything about the widget's look and input. Its methods are
// called on the goroutine that calls FormLayer.Update, and, for a field
// whose value changes, on the goroutine that changed it.
type FormWidgetProvider interface {
	// Supports reports whether the provider draws w natively. Widgets it
	// does not support are drawn by cera from their appearance and are
	// not interactive. Asked once per widget and remembered.
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
	// view's Origin, i.e. directly the position in the overlay. It covers
	// every pixel the widget's appearance can touch.
	Rect image.Rectangle
	// Rotation is the clockwise rotation of the widget's content in the
	// view, in degrees: the page's /Rotate less the counterclockwise /MK
	// /R, 0, 90, 180 or 270.
	Rotation int
	// Scale is pixels per point, for font sizes and border widths: a
	// provider sets the font to Appearance.FontSize * Scale.
	Scale float64
	State *FormState
	// Focus is true when the widget should take keyboard focus (see
	// FormLayer.Focus and FormLayer.Next).
	Focus bool
}

// View is what a viewer shows of a page.
type View struct {
	// Scale is pixels per point, as for RenderOptions.
	Scale float64
	// Viewport is the visible part of the page in device pixels of the
	// whole page at Scale; empty means the whole page.
	Viewport image.Rectangle
	// Origin is where the page's (0, 0) pixel is in the overlay.
	Origin image.Point
	// Layers and Usage select optional content as for RenderOptions;
	// widgets in hidden layers are hidden.
	Layers *Visibility
	Usage  Usage
}

// FormLayer joins a page, a form state and a provider: it translates
// changes of the view into Show and Hide calls, and tells a render which
// widgets the provider draws.
type FormLayer struct {
	page  *Page
	state *FormState
	prov  FormWidgetProvider

	widgets   []*Widget // of the page, in /Annots order
	supported map[*Widget]bool

	mu     sync.Mutex
	shown  map[*Widget]WidgetPlacement
	focus  *Widget
	cancel func()
}

// NewFormLayer returns the form layer of page p showing the values of s
// through prov. It asks prov which widgets of the page it supports; Update
// shows them.
func NewFormLayer(p *Page, s *FormState, prov FormWidgetProvider) *FormLayer {
	l := &FormLayer{
		page: p, state: s, prov: prov,
		supported: map[*Widget]bool{},
		shown:     map[*Widget]WidgetPlacement{},
	}
	if s != nil && s.form == p.doc.Form() {
		l.widgets = s.form.PageWidgets(p.index)
	}
	for _, w := range l.widgets {
		l.supported[w] = prov.Supports(w)
	}
	if len(l.widgets) > 0 {
		l.cancel = s.Subscribe(l.changed)
	}
	return l
}

// changed shows the widgets of f again, for the provider to pick up the
// new value.
func (l *FormLayer) changed(f *Field) {
	l.mu.Lock()
	var again []*Widget
	var places []WidgetPlacement
	for _, w := range f.Widgets {
		if pl, ok := l.shown[w]; ok && w.Page == l.page.index {
			again = append(again, w)
			places = append(places, pl)
		}
	}
	l.mu.Unlock()
	for i, w := range again {
		l.prov.Show(w, places[i])
	}
}

// Update is called when the view changes: scroll, zoom, page rotation,
// layer visibility. It calls Show for supported widgets in the viewport
// whose placement changed and Hide for those that left it.
func (l *FormLayer) Update(v View) {
	scale := normScale(v.Scale)
	vp := v.Viewport
	if vp.Empty() {
		vp = l.page.Bounds(scale)
	}
	vis := v.Layers
	if vis == nil {
		vis = l.page.doc.defaultVisibility(v.Usage)
	}
	type call struct {
		w    *Widget
		p    WidgetPlacement
		show bool
	}
	var calls []call
	l.mu.Lock()
	for _, w := range l.widgets {
		if !l.supported[w] {
			continue
		}
		dev := l.page.widgetBox(w, scale)
		visible := w.Flags&(AnnotHidden|AnnotNoView) == 0 &&
			(w.oc == nil || w.oc.eval(vis, scale)) &&
			dev.Overlaps(vp)
		old, wasShown := l.shown[w]
		if !visible {
			if wasShown {
				delete(l.shown, w)
				calls = append(calls, call{w: w})
			}
			continue
		}
		pl := WidgetPlacement{
			Rect:     dev.Add(v.Origin),
			Rotation: ((l.page.Rotate-w.Rotation)%360 + 360) % 360,
			Scale:    scale,
			State:    l.state,
			Focus:    w == l.focus,
		}
		if wasShown && old == pl {
			continue
		}
		l.shown[w] = pl
		calls = append(calls, call{w, pl, true})
	}
	l.mu.Unlock()
	for _, c := range calls {
		if c.show {
			l.prov.Show(c.w, c.p)
		} else {
			l.prov.Hide(c.w)
		}
	}
}

// widgetBox returns the device pixels of the page at scale that widget w
// can touch: its rectangle, rounded outwards.
func (p *Page) widgetBox(w *Widget, scale float64) image.Rectangle {
	m := p.deviceMatrix(scale)
	r := w.Rect
	x0, y0 := math.Inf(1), math.Inf(1)
	x1, y1 := math.Inf(-1), math.Inf(-1)
	for _, c := range [4][2]float64{{r.X0, r.Y0}, {r.X1, r.Y0}, {r.X0, r.Y1}, {r.X1, r.Y1}} {
		x, y := m.Apply(c[0], c[1])
		x0, x1 = min(x0, x), max(x1, x)
		y0, y1 = min(y0, y), max(y1, y)
	}
	return image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1)), int(math.Ceil(y1)))
}

// Skip is passed as RenderOptions.SkipAnnotation: widgets the provider
// draws natively are left out of the page image, so they are not drawn
// twice. Everything else on the page, including unsupported widgets,
// stays in the image.
func (l *FormLayer) Skip(annotIndex int) bool {
	i, ok := slices.BinarySearchFunc(l.widgets, annotIndex, func(w *Widget, idx int) int { return w.Annotation - idx })
	return ok && l.supported[l.widgets[i]]
}

// Focus gives w the keyboard focus (nil: none); the widget that had it
// and w are shown again with WidgetPlacement.Focus set accordingly.
func (l *FormLayer) Focus(w *Widget) {
	l.mu.Lock()
	old := l.focus
	if old == w {
		l.mu.Unlock()
		return
	}
	l.focus = w
	var again []*Widget
	var places []WidgetPlacement
	for _, x := range [2]*Widget{old, w} {
		if pl, ok := l.shown[x]; ok && x != nil {
			pl.Focus = x == w
			l.shown[x] = pl
			again = append(again, x)
			places = append(places, pl)
		}
	}
	l.mu.Unlock()
	for i, x := range again {
		l.prov.Show(x, places[i])
	}
}

// Next returns the widget after w in tab order (nil w: the first), for
// keyboard navigation: the page's widgets in the order of its /Tabs (row,
// column, or /Annots order for structure order and none), then those of
// the following pages. Hidden widgets and read-only fields are passed
// over. It returns nil after the last widget of the document.
func (l *FormLayer) Next(w *Widget) *Widget {
	form := l.page.doc.Form()
	if form == nil {
		return nil
	}
	page := l.page.index
	if w != nil {
		page = w.Page
	}
	found := w == nil
	for ; page < l.page.doc.NumPages(); page++ {
		for _, x := range l.page.doc.tabOrder(form, page) {
			if !found {
				found = x == w
				continue
			}
			if x.Flags&(AnnotHidden|AnnotNoView) == 0 && x.Field.Flags&FfReadOnly == 0 {
				return x
			}
		}
		found = true
	}
	return nil
}

// tabOrder returns the widgets of page i in the order of its /Tabs.
func (d *Document) tabOrder(form *Form, i int) []*Widget {
	ws := slices.Clone(form.PageWidgets(i))
	pd, err := d.r.Page(i + 1)
	if err != nil {
		return ws
	}
	switch tabs, _ := d.name(pd.Get("Tabs")); tabs {
	case "R": // rows: top to bottom, then left to right
		slices.SortStableFunc(ws, func(a, b *Widget) int {
			if c := cmpFloat(b.Rect.Y1, a.Rect.Y1); c != 0 {
				return c
			}
			return cmpFloat(a.Rect.X0, b.Rect.X0)
		})
	case "C": // columns: left to right, then top to bottom
		slices.SortStableFunc(ws, func(a, b *Widget) int {
			if c := cmpFloat(a.Rect.X0, b.Rect.X0); c != 0 {
				return c
			}
			return cmpFloat(b.Rect.Y1, a.Rect.Y1)
		})
	}
	return ws
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Close hides every widget shown and stops following the state.
func (l *FormLayer) Close() {
	l.mu.Lock()
	shown := make([]*Widget, 0, len(l.shown))
	for _, w := range l.widgets {
		if _, ok := l.shown[w]; ok {
			shown = append(shown, w)
		}
	}
	clear(l.shown)
	cancel := l.cancel
	l.cancel = nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, w := range shown {
		l.prov.Hide(w)
	}
}
