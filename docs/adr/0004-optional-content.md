# 0004. Optional content (layers)

- Status: proposed
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

## Alternatives considered

- **Visibility as document state** (`doc.SetLayerVisible`): simpler API,
  but forces one view per document and invalidates every cached display
  list on change.
- **Re-interpreting on toggle**: no change to the display list, but loses
  the main benefit of M3 for exactly the interactive case.
