# 0007. The rest of the graphics state: overprint, text knockout, transfer

- Status: proposed
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
