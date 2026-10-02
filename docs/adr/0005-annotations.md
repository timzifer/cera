# 0005. Annotations from appearance streams

- Status: accepted
- Date: 2026-10-01
- Milestone: M7½

## Context

cera draws the page content stream only. `/Annots` are not read, so stamps,
comments, highlights, redline markups, signatures and filled form fields are
invisible. This is not counted in `Stats.Unsupported` today; it is the
largest gap outside M7 for anyone using cera as a viewer.

Every annotation that should be visible normally carries an appearance
stream (`/AP`), a form XObject cera can already draw. Form fields have
their own needs and are the subject of ADR 0006, which builds on this one.

## Decision

**Reading.** `Page.Annotations()` returns the page's annotations, read once
and cached on the page:

```go
type Annotation struct {
	Index    int    // position in /Annots
	Subtype  string // "Text", "Link", "Widget", "Square", ...
	Rect     Rect   // in default user space
	Flags    AnnotFlags
	Contents string
	Name     string // /NM
	state    reader.Name // /AS
	ref      reader.Ref
	oc       ocTag // ADR 0004
}
```

`Link` annotations additionally expose their target (URI, page and
destination, named destination), so viewers can make them clickable without
going to the reader.

**Drawing.** After the page content, the interpreter draws each annotation:

- Skipped when `Hidden`, when `NoView` (for `UsageView`) or not `Print` (for
  `UsagePrint`), when its `/OC` is off (ADR 0004), or `Popup`.
- The normal appearance `/AP /N`, with the sub-dictionary entry `/AS`
  selects when `/N` is a dictionary. Rollover and down appearances are not
  used by the renderer.
- Placed per PDF 2.0 12.5.5: the appearance's `BBox` transformed by its
  `Matrix`, fitted to `Rect` by the matrix A; drawn as a form XObject, in a
  transparency group when the form has `/Group`.
- `NoZoom` and `NoRotate`: honoured — the annotation is drawn at its
  unscaled size anchored at the upper-left corner of `Rect` (NoZoom), and
  counter-rotated against `/Rotate` (NoRotate).

**Missing appearances.** For annotations without `/AP`, cera generates an
appearance for the markup types whose look is fully specified: `Square`,
`Circle`, `Line` (with line endings), `PolyLine`, `Polygon`, `Ink`,
`Highlight`, `Underline`, `StrikeOut`, `Squiggly`. Generated appearances are
display-list items, not PDF objects. `FreeText` and `Text` (the note icon)
without `/AP` are counted as `annot-no-ap` and not drawn. Widget annotations
without appearance are handled in ADR 0006.

**Display list.** Annotation items are recorded after the page content, each
annotation as a tagged range (tag = annotation index + 1; page content is
tag 0). Drawing can skip a set of annotation tags without re-recording, the
way ADR 0004 skips layers. This is what lets a form overlay (ADR 0006) hide
the widgets it draws natively.

**Options.**

```go
type RenderOptions struct {
	// ...
	// Annotations selects which annotations are drawn: AnnotsView (the
	// default), AnnotsPrint, or AnnotsNone for the page content alone.
	Annotations AnnotMode
	// SkipAnnotation, when set, is asked once per annotation tag per
	// render; annotations it returns true for are not drawn.
	SkipAnnotation func(index int) bool
}
```

`Page.Run` and `Page.Text` take the same mode; text of appearance streams is
passed to `TextDevice`, so text of filled fields and of FreeText annotations
can be extracted and searched.

## Consequences

- A new key `annot-no-ap` (and `annot-bad` for unreadable ones) in
  `Stats.Unsupported`.
- Filled forms, stamps and signatures look like they do in other viewers;
  the `cmd/cera` tool gets `-annots view|print|none`.
- Interactive behaviour (hover, click, editing) is not part of rendering;
  viewers build it on `Page.Annotations()` and ADR 0006.
- Default output changes for every file with annotations; corpus reference
  images are regenerated once.

## Implementation notes

Implemented in `annot.go` (reading, links, drawing) and `annotgen.go`
(generated appearances). Where it refines the decision above:

- **Tags are shared with ADR 0004.** An annotation's tag is an entry of
  the display list's optional-content tags with the annotation's index and
  flags (and its `/OC` as membership); optional content inside its
  appearance nests under it. One evaluation per tag and render covers
  layers, `RenderOptions.Annotations` and `SkipAnnotation`, so changing
  the mode or the skip function draws the cached list again.
- `Hidden` and `Popup` are never recorded; `NoView` and `Print` are
  evaluated per render. `Invisible` and `ToggleNoView` are ignored.
- `/CA` of every annotation, and the Multiply blend of generated
  highlights, apply to the annotation as one isolated group.
- `NoRotate` rotates the annotation about the upper-left corner of its
  `Rect` so that it stays upright on screen; `NoZoom` scales it by
  1/`Scale` about the same corner.
- Generated appearances are content streams run through the interpreter
  in default user space. Sizes the specification leaves open: line
  endings 6 × the border width (at least 6), underline and strike-out
  1/14 of the quad's height, strike-out at 3/8 of it. Highlights without
  `/C` are yellow, other types black; an empty `/C` is transparent.
  Quad points are accepted in either order writers use.
- Links: `Annotation.Link` holds URI and GoTo targets, explicit and named
  destinations (catalog `/Dests` and the `/Dests` name tree); other
  actions leave it nil.
- `annot-no-ap` counts annotations without `/AP` that cera does not
  generate (except `Link`, `Widget` and `Popup`). Annotations without a
  readable `/Rect` count as content errors, except those with an empty one,
  which writers use for invisible annotations. `annot-bad` is not used.
- `Page.Run`/`RunWith` and `Page.Text` take `RunOptions.Annotations` and
  `SkipAnnotation`; the default (`AnnotsView`) includes the text of
  appearances.

## Alternatives considered

- **Leave annotations to the viewer** (only expose them): every viewer
  would need to draw form XObjects itself, which it cannot without cera's
  interpreter.
- **Generate appearances for all types**: FreeText needs a text layout
  engine with rich text (`/RC`, XHTML); out of scope until the corpus shows
  files without `/AP` matter.
