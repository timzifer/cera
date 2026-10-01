# 0011. GPU backend

- Status: proposed
- Date: 2026-10-01
- Milestone: M9

## Context

The roadmap lists M9: a `GGDevice` on gogpu/gg, a glyph atlas, lux. It is
not needed for a feature-complete renderer; it is a second backend for
interactive viewers at high resolutions (4K and above, smooth zoom).

The `Device` interface and the display list were designed for more than one
backend. The ADRs before this one add `FillShading` (0001), non-solid paints
(0002) and tagged items (0004, 0005), all of which a GPU backend must
support.

## Decision

1. **Defer** M9 until ADRs 0001–0006 are accepted, so the GPU backend is
   built once against the complete `Device` interface.
2. The GPU backend consumes the **display list**, not the interpreter: it
   implements drawing of display-list items (like the raster worker does),
   so parsing stays shared and the CPU path remains the reference.
3. Everything the CPU path computes at record or compile time is reused:
   shading LUTs and meshes (as textures and vertex buffers), pattern tiles,
   decoded image planes and mip levels, glyph outlines (rasterized into a
   shared atlas instead of per-worker caches).
4. Transparency groups and soft masks map to offscreen render targets with
   the same bounds the CPU layers use; blend modes become fragment shaders.
5. The GPU backend lives in its own module (cgo and platform code), like
   the fyne adapter of ADR 0006; the core module stays pure Go.
6. Accuracy is measured against the CPU backend with the harness of
   ADR 0010; differences above the threshold are bugs in the GPU backend.

## Consequences

- No work on M9 now; the interfaces settled by the M7 ADRs are its contract.
- Backends cannot drift: both draw the same display list, and the CPU
  backend is the reference.

## Alternatives considered

- **Start M9 in parallel with M7**: the `Device` interface and display list
  items would change under it several times.
