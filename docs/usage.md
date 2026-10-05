# Usage

Open a document, render a page into an image the caller owns, read its text:

```go
doc, err := cera.Open(data)
page, err := doc.Page(0)
dst := image.NewRGBA(page.Bounds(150.0 / 72)) // owned by the caller, reusable
var st cera.Stats
err = page.Render(ctx, dst, cera.RenderOptions{
	Scale:      150.0 / 72,
	Background: color.RGBA{255, 255, 255, 255},
	Deadline:   time.Now().Add(time.Second), // partial image + ErrDeadline
	Stats:      &st,                         // ops, fills, unsupported features
	Workers:    0,                           // goroutines per page; 0 = all cores
})
page.Release() // drop the cached display list when the page leaves the view

text, err := page.Text(ctx) // characters with boxes, including invisible text
fmt.Println(text.String())
```

Fonts a document names but does not embed can come from a provider; the
CJK module links Noto Sans only for the collections it is asked for:

```go
import (
	"github.com/timzifer/cera/fonts/cjk"
	"github.com/timzifer/cera/fonts/cjk/japan1"
)

doc, err := cera.OpenWith(data, cera.OpenOptions{
	Fonts: cjk.Provider{japan1.Collection}, // or a cera.FontProvider of your own
})
```

Layers (optional content) are switched per render, not on the document, so
several views can show different layers; switching draws the cached display
list again without interpreting the page:

```go
cfg := doc.Layers() // nil without /OCProperties
for _, l := range cfg.Layers {
	fmt.Println(l.Name, l.Visible)
}
vis := cfg.Visibility().With(cfg.Layers[0], false)
err = page.Render(ctx, dst, cera.RenderOptions{Scale: 150.0 / 72, Layers: &vis})
```

Annotations are drawn over the page content, as on screen by default;
`RenderOptions.Annotations` selects `AnnotsPrint` or `AnnotsNone`, and
`SkipAnnotation` leaves single ones out (a viewer drawing form fields
itself). Both only redraw the cached display list:

```go
for _, a := range page.Annotations() {
	if a.Link != nil {
		fmt.Println(a.Rect, a.Link.URI, a.Link.Page)
	}
}
```

Interactive forms (ADR 0006): `Document.Form` reads the fields of
`/AcroForm` with their widgets, and a `FormState` the caller owns holds
the values a user enters. Rendering with `RenderOptions.Form` shows them
through generated appearances, recorded once per widget and value; the
page itself is not interpreted again. A `FormLayer` places native widgets
of a `FormWidgetProvider` over the page image and leaves them out of it;
[`form/fyneform`](../form/fyneform) is such a provider for fyne, a module of
its own so that cera stays free of toolkits and cgo:

```go
form := doc.Form() // nil without /AcroForm
state := form.NewState()
err = state.SetValue(form.Field("address.street"), cera.TextValue("Main St 1"))

layer := cera.NewFormLayer(page, state, provider)
layer.Update(cera.View{Scale: scale, Viewport: visible})
err = page.Render(ctx, dst, cera.RenderOptions{Scale: scale, Form: state, SkipAnnotation: layer.Skip})
```

`dst` may be any sub-rectangle of the page (a tile or viewport): only that
region is drawn. `RenderOptions.Region` narrows it further. Errors in the
content never stop a page; a non-nil error means a partial image
(`ErrDeadline`, a rasterizer budget, or a recovered `*PanicError`).

`Page.Run` drives any `Device` directly, without a display list; a device
that also implements `TextDevice` receives every string shown, in every
render mode. `Page.Text` is built on it.

## Command line

```sh
go run ./cmd/cera -dpi 150 -v -o 'page-%d.png' input.pdf
```
