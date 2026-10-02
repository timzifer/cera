# 0007. The rest of the graphics state: overprint, text knockout, transfer

- Status: accepted
- Date: 2026-10-01
- Milestone: M7

## Context

`gs` reads `LW`, `LC`, `LJ`, `ML`, `D`, `CA`, `ca`, `BM`, `SMask` and `AIS`
(`interp.go`). Silently ignored, and not counted in `Stats.Unsupported`:

- `OP`, `op`, `OPM` — overprint. On screen, a print file that relies on
  overprint (a black text over a coloured area, a spot-colour varnish
  plate) shows knocked-out colour instead of the mixed result. Typical in
  prepress, rare in drawings.
- `TK` — text knockout: whether glyphs of one text object knock each other
  out inside a transparency group.
- `Font` — the font and size set through `gs` instead of `Tf`.
- `TR`, `TR2` — transfer functions outside soft masks.
- `BG`, `BG2`, `UCR`, `UCR2`, `HT`, `FL`, `SM`, `SA`, `RI`, `ri` — device
  dependent parameters.

## Decision

1. **`Font`** is read like `Tf` (font reference and size). Pure bug fix.
2. **Count, then decide.** `OP`/`op` set to true, text objects that `TK` would change (see 4),
   `TR`/`TR2` other than `/Identity` or `/Default` are counted
   (`overprint`, `text-knockout`, `transfer`) so the corpus report
   shows how often they occur.
3. **Overprint simulation** for the case that matters on screen: when
   overprint is on and the colour space is DeviceCMYK with `OPM 1`, or
   Separation/DeviceN, the object is composited with a blend that keeps the
   backdrop's components where the object's are zero (CMYK) or absent
   (spot colourants). cera composites in RGB, so this is approximated as
   `Multiply` of the object's colour with the backdrop, applied only to
   such objects. It is enabled by `RenderOptions.SimulateOverprint`
   (default off, as in Acrobat's default for non-PDF/X files).
4. **`TK true`** (the default) means overlapping glyphs of one text object
   knock each other out instead of compositing on each other. cera draws
   glyphs as separate fills, which is the `TK false` behaviour; the two
   differ only where glyphs overlap and are drawn with opacity below 1 or a
   blend mode. For exactly that case (a text object with alpha < 1 or a
   blend mode whose glyph boxes overlap) the text object is drawn as a
   knockout group of its glyphs; everything else stays as it is.
5. **`TR`/`TR2`** apply per component after colour conversion, sampled into
   a 256-entry table per channel (the machinery soft masks already have).
6. **`BG`, `UCR`, `HT`, `FL`, `SM`, `SA`** are and stay ignored: they
   describe the output device, not the page. `ri` stays ignored (ADR 0003).

## Implementation notes

- **1–6** are in `extgstate.go`. `op` falls back to `OP` when absent.
- **3**: overprinted objects (fills, strokes, text, stencil masks in a
  solid colour) become object groups with `Multiply`, unless the state
  already blends. The display list records the page once per
  `SimulateOverprint` setting (`Page.Render` keeps the last one, like
  the scale); `RunOptions` has the same field. `overprint` is counted only
  where overprint would apply and is not simulated, so it stays in the
  default corpus report as a measure of how often it occurs.
- **4** works per text-showing operator, not per text object: a run is
  checked for overlapping glyph boxes (more than 256 glyphs are taken to
  overlap) when filled at an opacity below 1, and then drawn as an
  isolated knockout group with one `FillGlyphs` per glyph, carrying the
  state's blend mode and soft mask. For Normal compositing an isolated
  knockout group equals the non-isolated one the spec describes. Glyphs
  of different operators in one `BT … ET`, stroked text, pattern fills
  and Type 3 glyphs are not knocked out; the stroked and pattern-filled
  cases are counted as `text-knockout`.
- **5**: transfer functions apply to solid colours (fills, strokes,
  text, stencil masks), as a table per RGB component made once per
  ExtGState object. Images, shadings and patterns are drawn without and
  counted as `transfer`, as is a function that does not read. `TR2
  /Default` is the identity.

## Consequences

- One correctness fix (`Font`) without new options.
- Overprint stays an approximation in RGB; exact overprint needs a
  CMYK+spot composite, which cera's RGBA layers do not have. Not planned.
- The ignored device parameters are documented as intentionally ignored,
  not forgotten.

## Alternatives considered

- **A CMYK + spot compositing pipeline** for exact overprint: doubles the
  layer memory and the compositing code for a prepress feature outside the
  project's focus.
