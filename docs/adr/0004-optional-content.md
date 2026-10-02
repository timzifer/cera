# 0004. Optional content (layers)

- Status: accepted
- Date: 2026-10-01
- Milestone: M7½

## Context

`BDC /OC …` is counted as `optional-content` and its content drawn
unconditionally (`interp.go`); `/OC` on XObjects and annotations is ignored;
`/OCProperties` of the catalog is not read. CAD exports (AutoCAD, MicroStation,
Revit) put every drawing layer in an optional content group (OCG), often with
layers that are off by default — construction lines, alternative plot
styles, other languages. Drawing them all is the most visible error on such
files. Viewers of drawings also need to switch layers on and off.

## Decision

**Reading.** `Document.Layers()` reads `/OCProperties` once:

```go
type Layer struct {
	Name    string
	ref     reader.Ref
	Visible bool // in the current configuration
	Locked  bool
}

type LayerConfig struct {
	Name   string
	Layers []*Layer
	Order  []LayerNode // the tree of /Order, for a layer panel
	RBGroups [][]*Layer // radio-button groups
}

func (d *Document) Layers() *LayerConfig        // default configuration /D
func (d *Document) LayerConfigs() []*LayerConfig // /Configs
```

**Visibility state** is a value the caller owns and passes to rendering,
not state on the document, so several views of one document can show
different layers:

```go
type Visibility struct{ /* bitset over the document's OCGs */ }

func (c *LayerConfig) Visibility() Visibility
func (v Visibility) With(l *Layer, on bool) Visibility

type RenderOptions struct {
	// ...
	// Layers selects the optional content to draw; the zero value is the
	// document's default configuration, for the view usage.
	Layers *Visibility
	// Usage is UsageView (default), UsagePrint or UsageExport; it applies
	// the /Usage and /AS entries of the configuration.
	Usage Usage
}
```

**Evaluation.** Membership is evaluated in the interpreter:

- OCGs, and OCMDs with `/OCGs` + `/P` (AnyOn, AllOn, AnyOff, AllOff) and
  with visibility expressions `/VE` (And, Or, Not; depth bounded at 16).
- Marked content `BDC /OC`, nested; XObjects with `/OC`; annotations with
  `/OC` (ADR 0005).
- Hidden content is still **interpreted** (the graphics state must be
  tracked), but its drawing operations are not passed to the device. Text in
  hidden content is not passed to `TextDevice` either.

**Display list.** To switch layers without re-interpreting, the display list
records every item with a layer tag (a small index into the list's
distinct membership expressions, 0 = always visible) and keeps hidden items.
The band index stays valid; drawing evaluates each tag once per render
against the `Visibility` (a bitset lookup per tag, not per item) and skips
the items whose tag is off. Clips, groups and masks inside hidden content are
skipped with their bodies, which the existing bbox pairing already supports.

## Consequences

- `optional-content` disappears from `Stats.Unsupported`; `oc-bad` for
  unreadable membership dictionaries, drawn visible.
- Toggling a layer costs one render from the cached display list, no
  parsing — the case drawing viewers need.
- Display lists of files with layers off by default grow by the hidden
  content. The spec's memory targets are per page and scale; acceptable.
- The cache key of the display list does not include visibility.
- Group elimination (M6) must not merge items of different layer tags.

## Implementation notes

Implemented in `layers.go`, with tags in `displaylist.go` and the marked
content stack in `interp.go`. Where it refines the decision above:

- `Visibility` stores the layers that are *off*, so its zero value shows
  every layer; `Visibility.Visible(l)` reads it. `LayerConfig.Visibility()`
  is `VisibilityFor(UsageView)`; `RenderOptions.Usage` applies only when
  `Layers` is nil, an explicit `Visibility` is taken as given.
- Automatic states: a group listed in an `/AS` entry for the event is off
  if any listed category (`View`, `Print`, `Export`) says so; `Zoom`
  limits it to `[min, max)` of the render scale (1 = 100 %). `With`
  removes a group's zoom limit. `Language` and `User` are not evaluated.
- Groups whose `/Intent` the configuration's does not include are on and
  stay out of the configuration's states.
- **Clips are not tagged.** Hidden content does not paint, but its clips
  still apply (PDF 2.0, 8.11), so the display list keeps them
  untagged, and devices driven by `Page.Run` see hidden content as its
  clips only. Group and mask ends carry the tag of their begin, so a
  hidden group is skipped with its body.
- `Page.RunWith(ctx, dev, RunOptions{Layers, Usage, ...})` selects the
  visibility for other devices; `Page.Run` and `Page.Text` use the default
  configuration. Hidden XObjects are not run for them at all.
- Marked content does not outlive the content stream it starts in: what a
  form, glyph procedure or soft mask leaves open is closed at its end, and
  an unbalanced `EMC` is ignored.
- Group elimination stays correct without extra rules: a single object
  that takes over its group's opacity is nested in the group's content,
  so its tag is never visible when the group's is not.
- `/OC` of annotations waits for ADR 0005.
- Without `/OCProperties`, `/OC` is ignored (spec). A group missing from
  `/OCGs` counts as on. `oc-bad`: a `/Properties` entry that is missing, a
  membership that is not a dictionary, or a visibility expression that does
  not read or nests deeper than 16.

## Alternatives considered

- **Visibility as document state** (`doc.SetLayerVisible`): simpler API,
  but forces one view per document and invalidates every cached display
  list on change.
- **Re-interpreting on toggle**: no change to the display list, but loses
  the main benefit of M3 for exactly the interactive case.
