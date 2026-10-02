# 0002. Tiling and shading patterns

- Status: proposed
- Date: 2026-10-01
- Milestone: M7
- Depends on: ADR 0001 (shadings), stilus ADR 0002 (repeating textures)

## Context

`Pattern` colour spaces are recognised, but `scn` with a pattern name is
ignored and the object counted as `pattern` (`interp.go`). Two kinds exist:

- **Shading patterns** (`PatternType 2`): a shading (ADR 0001) as the paint
  of a fill, stroke or text.
- **Tiling patterns** (`PatternType 1`): a content stream (a cell) repeated
  with `XStep`/`YStep`, either coloured (`PaintType 1`, the cell sets its
  own colours) or uncoloured (`PaintType 2`, a stencil painted in the colour
  given with `scn`).

The pattern matrix maps pattern space to the **default** coordinate space of
the page (or of the form the pattern is a resource of), not to the current
transform. Hatching in CAD drawings, the most frequent use in our target
files, is tiling patterns with tiny cells of a few lines each, often rotated.

## Decision

**Paint.** The interpreter's paint for fills, strokes and glyph runs becomes
one of: solid colour, shading pattern, or tiling pattern. All three reach the
device through `*Paint` (a premultiplied colour or a per-span shader in
stilus), so `FillPath`, `StrokePath` and `FillGlyphs` keep their signatures.
The display list stores a paint index instead of a colour for non-solid
paints.

**Shading patterns** use the compiled `Shading` of ADR 0001 with the pattern
matrix in place of the current transform, and draw the shading's
`Background` where the shading itself paints nothing.

**Tiling patterns** are drawn by one of three strategies, chosen at record
time from the size of one cell in device pixels and from the number of cells
the painted area covers:

1. **Tile image** (the default): the cell is interpreted once into its own
   small display list and rasterized into an RGBA tile (or an alpha tile for
   `PaintType 2`) at device resolution, in pattern space scaled to the
   device, at most 1024 × 1024, one tile per `XStep` × `YStep` step (cells
   larger than a step are drawn at their wrapped offsets). The fill uses
   stilus's `ImageShader` in wrap mode ([stilus ADR 0002](https://github.com/timzifer/stilus/blob/main/docs/adr/0002-repeating-textures.md)),
   which maps each device pixel through the inverse pattern matrix and wraps
   it into the tile, sampled bilinearly. Rotated and skewed patterns stay
   exact up to resampling. Tiles are cached per pattern, scale and colour
   (for uncoloured patterns the colour is applied at fill time, so one alpha
   tile serves every colour).
2. **Replay** for few, large cells (at most 64 cells, or a cell larger than
   the tile limit): the cell's display list is replayed once per cell,
   translated, inside a clip of the painted shape. Exact vector output.
3. **Average colour** for cells smaller than one device pixel: the tile is
   rasterized at a coarser resolution and its average coverage and colour
   paint the area as a solid fill. This matches what the eye sees and what
   PDFium does for hatching zoomed far out.

Patterns nest (a cell may paint with a pattern); nesting is bounded at 4
levels like soft masks, counted as `pattern-budget` beyond.

**Bands.** A tile is made by the first worker that needs it under a
`sync.Once` per tile, then read-only; all others wait for it. Tiles are kept
in the document's image cache and count against its budget.

## Consequences

- `pattern` disappears from `Stats.Unsupported`; new keys `pattern-budget`
  and `pattern-bad`.
- Text and strokes can be painted with patterns at no extra cost, since all
  three paths already take a `*Paint`.
- Strategy 1 introduces resampling: a hatch line may be up to half a device
  pixel off its exact position. Corpus diffs against PDFium (ADR 0010)
  decide the thresholds between strategies; they are constants, not options.
- The display list no longer holds only colours; items get a paint index.

## Progress

Shading patterns (`PatternType 2`) are drawn as part of ADR 0001: the
shading filled through a clip of the painted shape, with the pattern
matrix against the pattern's base space and `Background` honoured.
Tiling patterns (`PatternType 1`) are still counted as `pattern` and not
drawn; this record stays proposed until they are.

## Alternatives considered

- **Always replay**: exact, but a hatch of 1 mm cells over an A3 sheet is
  tens of thousands of replays per band.
- **Tile in device space, axis-aligned**: cheaper shader, but wrong for
  rotated hatching, which is the common case in drawings.
