# 0011. GPU backend on stilus/gpu

- Status: accepted (revised 2026-11-13)
- Date: 2026-10-01
- Milestone: M9

## Context

The roadmap lists M9: a second backend for interactive viewers at high
resolutions (4K and above, smooth zoom). It is not needed for a
feature-complete renderer.

The first version of this record (2026-10-01) planned a backend of cera's
own on gogpu/gg: it would draw display-list items itself, keep a glyph
atlas, turn shading LUTs and meshes into textures and vertex buffers and
blend modes into fragment shaders. It was deferred until ADRs 0001–0006
had settled the `Device` interface.

Since then stilus has grown the GPU path that plan would have built in
cera (stilus ADRs 0007–0018, module `github.com/timzifer/stilus/gpu`,
gogpu/wgpu):

- **Compositing, not a second rasterizer.** A `stilus.Canvas` retargeted
  with `ResetCompositor` sends its spans to a `gpu.Target` instead of an
  image; rasterization, stroking and clipping stay on the CPU and are the
  same code as the CPU path. Within 1/255 of the CPU canvas on stilus's
  scenes (Metal, Vulkan/lavapipe, wgpu's software adapter).
- **The paints cera uses run on the GPU**: solid colours, strokes as
  strips (0008), linear and radial gradients (0009), images and pattern
  tiles through `ImageShader` (0011), Gouraud meshes (0012), glyph masks
  (0013), `LayerShader` and `gpu.Layer` for groups with every blend mode,
  soft masks, non-isolated backdrops and ink amounts (0014). cera has no
  `Shader` of its own; a caller's shader would be shaded on the CPU, or on
  the GPU with `Device.NewShaderType` (0018).
- **CMYK destinations** can be two targets through one canvas,
  `ResetCompositorPair` (0015). cera's `RenderCMYK` does not use pairs: it
  draws the page twice, with colours projected to C, M, Y and to K, onto
  two images; on the GPU these are two targets, drawn the same way.
- **Bands** draw onto one target at once through `gpu.Recorder`s, one
  canvas and goroutine each (0017), layers included.
- **Texture planes stay on the GPU** from frame to frame (0016).
- **Presenting**: `gpu.Presenter` samples a target into a render pass the
  viewer owns (a window's surface), with offset, scale, source rectangle
  and fade, without a copy (0010).
- **Platforms**: the module is pure Go (no cgo; wgpu's native backends
  through goffi) and builds for js/wasm, where it uses the browser's
  WebGPU.

What cera itself does to pixels, outside the canvas, is what remains
(`transparency.go`, `render.go`, `cmykout.go`):

- non-isolated groups start as a copy of their backdrop;
- knockout groups: K' = S + (1−f)·(K − B0) per object, over a scratch, a
  shape and the initial backdrop;
- soft masks: a group drawn into a layer, turned into an alpha plane by
  luminosity (with `/BC`) or alpha, through the transfer function `/TR`;
- a page that blends is drawn into a transparent band first, then over the
  background;
- the background fill of a region.

## Decision

1. **The GPU backend is the `RasterDevice` on a different destination,
   not a second device.** The interpreter, the display list and the
   raster worker's drawing of items are shared; the worker retargets its
   canvas with `ResetCompositor` (a `gpu.Target` or a band's
   `gpu.Recorder`) instead of `Reset` (an image). The CPU path stays the
   reference; there is no separate drawing code that could drift. This
   replaces points 2–4 of the first version (glyph atlas, LUT textures,
   cera's own blend shaders): stilus already does that work.
2. **The destination is abstracted inside the raster device**, not in the
   `Device` interface: what `RasterDevice` and `layers` do to an
   `*image.RGBA` today goes through a small internal surface with an image
   implementation (today's code) and a GPU implementation. Layers become
   `gpu.Target`s from a pool per page and size class, composited with
   `gpu.Layer` (Src, Mask, Initial, Alone, Blend, Subtractive, Black) where
   the CPU path uses `LayerShader`.
3. **Pixel work cera does itself falls back per group.** A group whose
   compositing has no GPU form yet (knockout, soft-mask conversion,
   `/TR`) is drawn on the CPU into an image, as today, and composited onto
   the target with `LayerShader` and images (stilus ADR 0014, source 1).
   The page is then correct from the first version; each fallback is
   counted in `Stats` and replaced by a GPU form later (a copy of the
   backdrop by `CopyTextureToTexture`, the soft-mask conversion and the
   knockout step as custom shader types, ADR 0018) when a scene shows it
   matters.
4. **Banded rendering keeps its shape**: one recorder per band and worker,
   `Target.Flush` once per render; budgets, deadlines and `PanicError`
   apply as on the CPU. A render either completes the target or reports
   the same errors `Page.Render` does.
5. **A module of its own, `github.com/timzifer/cera/gpu`**, so that the
   core module keeps no dependency on wgpu. Its API, as a sketch:

   ```go
   func NewRenderer(dev *gpu.Device) *Renderer
   func (r *Renderer) Render(ctx context.Context, p *cera.Page, dst *gpu.Target, opt cera.RenderOptions) error
   func (r *Renderer) RenderCMYK(ctx context.Context, p *cera.Page, cmy, k *gpu.Target, opt cera.RenderOptions) error
   ```

   The core module exports only what the GPU module needs to drive the
   raster worker (an internal package shared by both, or a narrow hook on
   `Page`); no wgpu type appears in `cera`. Presenting is the viewer's:
   it draws the target with `gpu.Presenter` (tiles, zoom, fades).
6. **Done is measured, not asserted**:
   - accuracy: the harness of ADR 0010 renders the corpus on the CPU and
     through the GPU module (software adapter in CI, Metal locally);
     differences above 2/255 per channel, outside the pages that use a
     counted fallback, are bugs in the GPU path;
   - speed: at 4K (3840 × 2160) and A3 at 300 dpi, a redraw of an
     interpreted page (display list cached, planes in the texture store)
     beats the CPU path's `Render` with all workers, readback excluded;
     the bench lists both per corpus class;
   - the CI builds the module for js/wasm and runs its tests on Vulkan
     through lavapipe, as stilus does.

## Consequences

- Most of M9 is plumbing in cera: retargeting the worker, a layer pool of
  targets, the fallback for groups. The GPU work lives in stilus, which
  is tested against its own CPU canvas.
- cera follows stilus's releases: features and fixes of the GPU path
  arrive by a version bump. The core module requires the stilus release
  the gpu module was tagged against (`gpu/vX.Y.Z requires stilus vA.B.C`).
- Layered pages cost a render pass and a copy per layer (stilus ADR 0014);
  pages with many small groups may be slower on the GPU than on the CPU,
  which the bench shows per class.
- Not byte-identical: stilus computes some blend modes in float32 on the
  GPU where the CPU uses float64.
- No GPU path for a knockout group or a soft mask's conversion at first:
  those groups are rasterized on the CPU and composited on the GPU.

## Alternatives considered

- **cera's own backend on gogpu/gg drawing display-list items** (the
  first version): a second implementation of every paint, glyph atlas
  and blend mode next to stilus's, which would drift from the CPU path;
  stilus/gpu already composites the same spans the CPU canvas draws.
- **Render on the CPU and upload the image per frame**: no new code, but
  a page at 4K is 33 MB per frame to upload, and none of the GPU's
  gradients, images and layers would be used; it remains the fallback of
  a viewer without a GPU.
- **A `GPUDevice` implementing `Device` next to `RasterDevice`**: would
  duplicate clip, text and group handling that the raster device already
  has; the destination is the only thing that differs.
