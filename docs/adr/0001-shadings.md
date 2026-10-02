# 0001. Shadings through `Device.FillShading`

- Status: accepted
- Date: 2026-10-01
- Milestone: M7
- Depends on: stilus ADR 0001 (shading shaders)

## Context

The `sh` operator and shading patterns (ADR 0002) are counted as
`shading` and skipped (`interp.go`). Gradients are among the most frequent
unsupported features in the pinned corpus; in technical drawings they mostly
appear as axial and radial fills of title blocks and logos, in brochures and
maps as free-form and patch meshes (types 4–7).

PDF functions of all four types already exist (`function.go`), so are the
colour spaces of the interpreter. What is missing is a device operation, a
display-list item and a raster path. The README names the operation the
`Device` interface grows by: `FillShading`.

Constraints: drawing must stay band-local (a band samples only its own rows),
allocation-free in steady state, and must not evaluate PDF functions per
pixel (type 4 calculator functions are an interpreter of their own).

## Decision

**Device operation.**

```go
// FillShading paints sh over the area of path p under m, or over the
// current clip when p is nil (the sh operator). sh maps shading space to
// device space through sm.
FillShading(p *Path, m Matrix, sh *Shading, sm Matrix, alpha uint8)
```

`Shading` is the compiled, read-only form of a shading dictionary, shared by
all pages and scales and kept in the document's cache next to images (same
LRU budget):

```go
type Shading struct {
	Type       int     // 1–7
	BBox       Rect    // in shading space; empty if none
	Extend     [2]bool // types 2 and 3
	Background *color.RGBA // used only by shading patterns, never by sh
	Coords     [6]float64  // types 2 and 3
	Domain     [4]float64  // t0 t1 for 2/3, x0 x1 y0 y1 for type 1
	LUT        *[lutSize]color.RGBA // types 2 and 3, and meshes with a Function
	Grid       *ShadingGrid         // type 1, sampled
	Mesh       *Mesh                // types 4–7, triangles in shading space
}
```

**Colours are converted once, at compile time.** A function is never
evaluated by a raster worker:

- Types 2 and 3: the function (or array of functions), the colour space and
  its tint transform (ADR 0003) are sampled into a LUT of 1024 premultiplied
  RGBA entries over `[t0, t1]`. Stitching functions whose bounds fall
  between samples get their bounds as extra exact samples (the LUT is
  piecewise linear in t, not uniform), so hard stops stay hard.
- Type 1: the function is sampled on a grid in its domain, 64 × 64 by
  default and up to 256 × 256 when neighbouring samples differ by more than
  one 8-bit level; the shader interpolates bilinearly.
- Types 4–7: vertices carry either colour components or a single t. With a
  function the triangles carry t and colour through the LUT; without, the
  per-vertex colour is converted at compile time.

**Meshes are triangles.** Types 4 and 5 are read as triangles; coons and
tensor-product patches (6 and 7) are subdivided into triangles at compile
time to a flatness of 0.2 shading units scaled by the largest scale the
display list has seen (re-subdivided only if a later scale needs more than
4× finer; bounded at 2¹⁶ triangles per shading, beyond which
`shading-mesh-budget` is counted and the coarser mesh used).

**Raster: stilus shaders, one fill per shading.** The shaders are stilus's
and know nothing of PDF ([stilus ADR 0001](https://github.com/timzifer/stilus/blob/main/docs/adr/0001-shading-shaders.md)):
types 2 and 3 use `LinearGradient` and `RadialGradient` (the two-circle form,
larger root first, with `Extend`) with the LUT as a ramp whose knots carry
the stitching bounds; type 1 is the sampled grid as a texture under
`ImageShader`; types 4–7 use `MeshShader`. A mesh is not drawn as thousands
of antialiased triangles, which leaves hairline seams and costs a pass per
triangle, but as one fill of the mesh's outline (or the path, or the clip)
with the mesh shader. cera keeps one shader per shading and raster worker.

**Display list.** A new item `dlShading` carries the path range (or none for
`sh`), the matrices, an index into the list's shadings, and the device box:
for `sh` the current clip box, intersected with `BBox` when present.

**Anti-aliasing flag and smoothness tolerance** (`/AntiAlias`, `SM`) are
ignored; the LUT resolution is fixed.

## Consequences

- `shading` disappears from `Stats.Unsupported`. New keys only for files
  that do not read: `shading-bad` (type or data unreadable, nothing drawn)
  and `shading-mesh-budget`.
- Shadings are recorded once per document and drawn by any number of
  workers without locks; a LUT costs 4 KB, a grid at most 256 KB.
- Dithering is not done; 8-bit banding on long gradients matches what
  PDFium and MuPDF show.
- `TextDevice` and `Page.Text` are unaffected; `textDevice` gains an empty
  `FillShading`.
- The `Device` interface changes; every implementation outside cera has to
  add one method. Acceptable before 1.0.

## Implementation notes

Implemented in `shading.go`, `shadingdraw.go` and `pattern.go`, on the
gradient, texture and mesh shaders of stilus. Where it differs from the
decision above:

- The device operation is `FillShading(sh *Shading, m Matrix, paint
  *Paint)`: it paints over the current clip. A fill, stroke or text in a
  shading pattern reaches the device as a clip of its shape (`ClipPath`,
  or the new `ClipStroke`) followed by `FillShading`; there is no path
  argument.
- Axial and radial shadings use a ramp of 512 entries; function-based
  ones a fixed 128 × 128 texture sampled bilinearly (not the adaptive
  64–256 grid).
- Budgets: 2²⁰ mesh triangles and 2¹⁶ patches per shading.
  `shading-function` counts shadings whose function does not read.
- A page that blends is drawn transparent and composited onto the
  background afterwards.
- Shading items carry the optional content and annotation tags of ADRs
  0004 and 0005 like every other painting item.

## Alternatives considered

- **Evaluate functions per pixel**: exact, but a type 4 function per pixel
  is two orders of magnitude slower than a LUT, and the result differs from
  the LUT by less than one 8-bit level for every function in the corpus.
- **Triangles as individual antialiased fills**: simplest, but seams are
  visible on every mesh at 150 dpi; drawing without antialiasing instead
  makes the outline of the mesh jagged.
- **Rasterize shadings into images at record time**: ties the display list
  to the scale more than it already is and multiplies memory by the area.
