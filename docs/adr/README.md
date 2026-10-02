# Architecture decision records

Each record states one decision: the context that forces it, what was
decided, and what follows from it. Records are numbered and never renumbered;
a record that is replaced keeps its number and says which record supersedes
it. Status is one of *proposed*, *accepted*, *superseded by NNNN* or
*rejected*.

The records below cover what cera still lacks for a feature-complete
renderer, in the order the work should land. Every feature named here is
counted in `Stats.Unsupported` today, unless the record says otherwise; a
record is done when its keys disappear from the corpus report or only count
files that are themselves broken.

Rasterizer and compositing work these records need — shaders for shadings,
repeating textures for patterns, group compositing kernels — is decided in
[stilus's ADRs](https://github.com/timzifer/stilus/tree/main/docs/adr),
because stilus knows nothing of PDF; the records here name the stilus ADR
they depend on.

| ADR | title | milestone | status |
|---|---|---|---|
| [0001](0001-shadings.md) | Shadings through `Device.FillShading` | M7 | accepted |
| [0002](0002-patterns.md) | Tiling and shading patterns | M7 | proposed |
| [0003](0003-colour-spaces.md) | Tint transforms, Lab, ICC and CMYK | M7 | accepted |
| [0004](0004-optional-content.md) | Optional content (layers) | M7½ | accepted |
| [0005](0005-annotations.md) | Annotations from appearance streams | M7½ | accepted |
| [0006](0006-interactive-forms.md) | Interactive forms and `FormWidgetProvider` | M7½ | proposed |
| [0007](0007-graphics-state.md) | The rest of the graphics state: overprint, text knockout, transfer | M7 | proposed |
| [0008](0008-font-fallbacks.md) | Font fallbacks and vertical writing | M8 | proposed |
| [0009](0009-transparency-remainders.md) | Remaining approximations: `/Matte`, `AIS`, non-isolated blending, Type 3 clips | M8 | proposed |
| [0010](0010-accuracy-and-robustness.md) | Accuracy against PDFium and robustness budgets | M8 | proposed |
| [0011](0011-gpu-backend.md) | GPU backend | M9 | proposed |

## Template

```markdown
# NNNN. Title

- Status: proposed
- Date: YYYY-MM-DD
- Milestone: Mx

## Context

What forces the decision: the spec, the corpus, the constraints of cera
(pure Go, every GOOS/GOARCH, ≈ 0 allocations per page, no panics).

## Decision

What cera does, in enough detail to implement it: types, interfaces, where
in the pipeline (interpreter, display list, raster worker) it happens.

## Consequences

What becomes easier or harder, what stays approximated and which
`Stats.Unsupported` keys remain.

## Alternatives considered

What was rejected and why.
```
