# 0008. Font fallbacks and vertical writing

- Status: accepted
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

## Implementation notes

- **Symbol and ZapfDingbats** are subsets of DejaVu Sans (Bitstream Vera
  licence: permissive, no AGPL), 28 and 45 KB, built by
  `internal/stdfont/gen` with go-opentype's `SubsetTrueType` from Adobe's
  Core 14 AFMs and glyph lists. URW base 35 was not used: its current
  releases are AGPL. Glyph names map to characters through the glyph
  lists; names the lists put in the private use area (the bracket and
  integral pieces of Symbol, `arrowvertex` …) map to the Unicode
  characters that look the same, `angleleft`/`angleright` to U+27E8/9.
  Only Symbol's `apple` has no glyph. A code takes its glyph by the name
  the document gives it (`/Differences`), else by the built-in encoding;
  the advance is the AFM width unless `/Widths` says otherwise, and each
  outline is scaled horizontally to that advance (clamped to 0.5–2).
  `Font.Text` reads such codes through the built-in encoding too, not
  StandardEncoding as pdffont assumes. Generated check box appearances
  (ADR 0006) still draw their marks as shapes.
- **Name mapping** (`fontsubst.go`): a table of about 120 families by a key
  reduced from the name (`TimesNewRomanPS-BoldMT` → `timesnewroman`),
  matched exactly or by the longest prefix, gives the class and a
  horizontal stretch for narrow faces (Arial Narrow 0.82, Calibri 0.9);
  words in the name (`mono`, `sans`, `serif`, `condensed` …), the
  descriptor flags and `/FontStretch` decide for the rest. Weight comes
  from the name, `ForceBold`, `/FontWeight` and `/StemV`, slope from the
  name, the italic flag and `/ItalicAngle`. The stretch applies to
  outlines and to advances the stand-in supplies itself; `/Widths` still
  wins.
- **`FontProvider`** is asked first for every font without a usable
  program, simple or composite, with a `FontRequest` (name, character
  collection, writing mode, style). Its program may be TrueType,
  OpenType, bare CFF or Type 1. For a composite font, a CID-keyed program
  finds glyphs by CID; any other is treated as Unicode-keyed and finds
  them through the character each CID stands for (for Adobe-Identity
  fonts, through the document's ToUnicode).
- **CMaps** are `internal/cmap`: a parser for CMap programs (embedded
  ones, `/UseCMap` by name or stream, inline `CIDSystemInfo`), codespace
  ranges of one to four bytes checked byte by byte, later definitions
  winning over earlier ones, and the 59 predefined CMaps of Table 116
  compiled by `internal/cmap/gen` from Adobe's cmap-resources into one
  432 KB file of separately deflated entries, inflated on first use. The
  same file holds a CID-to-Unicode table per collection (Japan1, GB1,
  CNS1, Korea1), the inverse of the `Uni*-UTF32-H` CMaps (vertical forms
  from `-V` read as their horizontal characters); it is the text of codes
  a document gives no ToUnicode for, and the character a Unicode-keyed
  provider font draws. A CMap cera cannot read or does not know is
  counted as `cmap-missing` and read as Identity. `FuzzParse` covers the
  parser.
- **Vertical writing**: `/WMode` of the CMap (or its stream) decides; `/W2`
  and `/DW2` (default `[880 −1000]`, position vector x half the width)
  give the advance and the position vector per CID, with the spec's
  advance `w1·Tfs + Tc (+ Tw)`. Glyphs are replaced by their `vert` forms
  when the program has a GSUB table (cached per glyph). `GlyphRun.Vertical`
  and `Glyph.Origin` (the position vector) tell devices; `Page.Text`
  boxes vertical glyphs an em wide from the pen down and orders them
  along the column. `vertical-text` is gone.
- **`fonts/cjk`** is a module with a `Provider` and one package per
  collection (`japan1`, `gb1`, `cns1`, `korea1`) over Noto Sans JP, SC and
  KR from go-opentype/fonts, so a binary links only the collections it
  imports. CNS1 uses Noto Sans SC (go-opentype has no TC). Those fonts are
  pinned at the default master of their variable upstream, which is a
  light weight; bold requests get the same weight.

## Consequences

- `font-missing` remains only for simple fonts no stand-in covers (none
  today but a broken program with no name); composite fonts no provider
  supplies are counted as `font-missing-` and their collection
  (`font-missing-japan1`, `-gb1`, `-cns1`, `-korea1`, `-cid`), so the
  corpus report splits them by script.
- The core module grows by the two symbol fonts and the CMap tables (a few
  hundred KB); CJK glyphs stay out of every binary that does not ask for
  them.
- `OpenWithPassword` becomes a shortcut for `OpenWith`.

## Alternatives considered

- **Embedding CJK fonts in the core**: tens of MB in every binary,
  including wasm.
- **Reading system fonts in the core**: per-OS code and non-reproducible
  output; left to a `FontProvider` outside the core.
