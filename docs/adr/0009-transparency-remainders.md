# 0009. Remaining approximations: `/Matte`, `AIS`, non-isolated blending, Type 3 clips

- Status: proposed
- Date: 2026-10-01
- Milestone: M8
- Depends on: stilus ADR 0003 (group compositing), for items 3 and 4

## Context

M4–M6 left documented approximations, each counted in `Stats.Unsupported`:

| key | today |
|---|---|
| `smask-matte` | image soft masks with `/Matte` (pre-multiplied image data) drawn as if not pre-multiplied: dark fringes at soft edges |
| `alpha-is-shape` | `AIS true` ignored; alpha treated as opacity |
| `non-isolated-blend` | a non-isolated group with blend modes inside that is itself blended or a knockout object: composited as Normal or drawn isolated |
| `type3-clip` | Type 3 glyphs in clipping render modes (4–7) do not clip |
| `smask-transfer` | unreadable transfer functions of soft masks |
| `smask-budget`, `image-too-large` | budgets, intended |

These are rare in the corpus but visible when they occur.

## Decision

1. **`/Matte`**: un-premultiply at decode time —
   `c = m + (c' − m) / α` per component, clamped — when the soft mask and
   the image have the same size; otherwise resample the mask to the image
   first. Done once per decoded image, so drawing is unaffected. Fixes
   `smask-matte` fully.
2. **Type 3 clips**: the glyph procedure is interpreted into a path
   accumulator instead of the device (fills become clip paths; images and
   strokes inside a glyph are approximated by their bounding box and
   counted as `type3-clip-approx`). Uses the existing text clip machinery.
3. **Non-isolated groups with blending, blended or knocked out**: draw the
   group's layer twice — the backdrop-included composite (as today) and the
   group alone (shape and alpha) — and use the second to remove the
   backdrop's contribution as in PDF 2.0 11.4.8. The removal is a stilus
   compositing kernel (`LayerShader` with `Initial` and `Alone`,
   [stilus ADR 0003](https://github.com/timzifer/stilus/blob/main/docs/adr/0003-group-compositing.md));
   cera draws the second layer and passes both. Costs one extra layer for
   these groups only; implemented only if the corpus shows files beyond
   synthetic tests.
4. **`AIS`**: carry a separate shape channel only inside groups that use
   it (an `image.Alpha` cera keeps, composited by stilus's `LayerShader`
   with `Shape`, stilus ADR 0003); otherwise unchanged. Lowest priority;
   stays counted until a real file needs it.
5. **Budgets** (`smask-budget`, `image-too-large`, and new ones from other
   ADRs) stay; they are robustness limits, listed in ADR 0010.

### As implemented

- **3** is done for groups that are not objects of a knockout group: the
  display list marks such a group (`Group.alone`) and draws it twice for a
  raster device, first on its own into a layer the device keeps, then onto
  its backdrop; `LayerShader` with `Initial` and `Alone` removes the
  backdrop and blends. Non-isolated groups with blend modes inside a
  knockout group are still drawn isolated and counted as
  `non-isolated-blend`. `Page.Run` on a raster device, without a display
  list, composites them as Normal.
- Isolated layers are composited with stilus's `LayerShader` too; cera's
  own compositor and blend functions are gone, and `BlendMode` is stilus's.

## Consequences

- 1 and 2 are small, local changes and are done with M7.
- 4 is justified only by corpus evidence; until then the key stays in the
  report as a known approximation.

## Alternatives considered

- **A full shape/opacity separated compositing model** (as the spec
  describes): exact for 3 and 4 together, but doubles layer memory for
  every group to fix a handful of files.
