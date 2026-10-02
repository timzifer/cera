# 0008. Font fallbacks and vertical writing

- Status: proposed
- Date: 2026-10-01
- Milestone: M8

## Context

Non-embedded fonts are replaced by Arimo, Tinos and Cousine
(Helvetica, Times, Courier). Not covered, counted as `font-missing`:

- **Symbol** and **ZapfDingbats** — also needed by ADR 0006 for check box
  and radio appearances (`/MK /CA` is a ZapfDingbats character).
- **Non-embedded composite fonts**, mostly CJK (`Adobe-Japan1`, `-GB1`,
  `-CNS1`, `-Korea1` with predefined CMaps).
- Non-embedded TrueType fonts with names that map to none of the three
  families (Arial Narrow, Calibri, Verdana …).

Vertical writing (`Identity-V` and other `-V` CMaps, `/W2`, `/DW2`) is drawn
with horizontal metrics and counted as `vertical-text`.

cera must stay pure Go, and its binary size matters for `js/wasm`.

## Decision

**Symbol and ZapfDingbats.** Embed two small open fonts that cover their
glyph sets with the standard encodings' names, metric-compatible as far as
the AFM widths allow, and scale each glyph to the AFM width like the other
stand-ins. Candidates must have an OFL or comparable licence (e.g. the
Symbol and Dingbats subsets of URW base 35, AGPL-free releases only); the
choice is part of implementing this ADR.

**Name mapping.** A table maps frequent non-embedded font names to the
existing families by class (sans, serif, mono), weight, width and slope,
using `/FontDescriptor` flags (`FixedPitch`, `Serif`, `Italic`, `ForceBold`),
`/FontWeight` and `/StemV` when the name is unknown. Widths from `/Widths`
always win, so line lengths stay right whatever the stand-in looks like.

**CJK** is opt-in, not embedded in the core module:

```go
// FontProvider supplies programs for fonts the PDF does not embed.
type FontProvider interface {
	Font(req FontRequest) (program []byte, ok bool)
}
// Document option:
func OpenWith(data []byte, opt OpenOptions) (*Document, error)
type OpenOptions struct {
	Password string
	Fonts    FontProvider // nil: built-in stand-ins only
}
```

A separate module `github.com/timzifer/cera/fonts/cjk` provides Noto Sans
CJK / Source Han subsets per ordering; desktop applications can implement
`FontProvider` over the system's fonts instead. The predefined CMaps
(`UniJIS-UCS2-H` …) are compiled into a compact table in the core, because
they are needed to read codes at all, independent of the glyphs.

**Vertical writing.** Text in fonts with a `-V` CMap advances along y, uses
`/W2`/`/DW2` for vertical metrics and position vectors, and replaces glyphs
with their vertical forms through the font program's `vert` feature when an
OpenType table offers it. Glyph runs gain a vertical flag; the glyph cache
is unaffected (positions only). `TextDevice` reports the writing mode.

## Consequences

- `font-missing` remains only for fonts no provider supplies; the corpus
  report can split it by script.
- The core module grows by the two symbol fonts and the CMap tables (a few
  hundred KB); CJK glyphs stay out of every binary that does not ask for
  them.
- `OpenWithPassword` becomes a shortcut for `OpenWith`.

## Alternatives considered

- **Embedding CJK fonts in the core**: tens of MB in every binary,
  including wasm.
- **Reading system fonts in the core**: per-OS code and non-reproducible
  output; left to a `FontProvider` outside the core.
