# 0010. Accuracy against PDFium and robustness budgets

- Status: accepted
- Date: 2026-10-01
- Milestone: M8

## Context

"Feature-complete" needs evidence, not a list of operators. Today:

- CI gates on **robustness** only: no panic, hang, deadline or unopened
  file on the pinned corpus; the large corpora run weekly.
- **Accuracy** is not measured for cera. The stilus harness compares
  against PDFium (WebAssembly, no cgo), but a cera engine for it is still to
  be added (README).
- Fuzzing runs 60 s per CI run on the interpreter only.
- Budgets exist piecemeal (image cache, soft masks per page, mip levels);
  ADRs 0001–0006 add more (mesh triangles, pattern nesting, annotation
  count).

## Decision

**Accuracy.**

1. Add a cera engine to the stilus harness (`stilus/harness`, a module of
   its own, so importing cera there makes no cycle; the work lands in the
   stilus repository) and render the pinned corpus at
   150 dpi with cera and PDFium.
2. Per page, record: the share of pixels differing by more than 16 levels
   in any channel, and the 99th percentile of the difference. Report both in
   the CI job summary next to timing, per file, with the `Stats.Unsupported`
   keys of the page beside them, so a diff can be attributed to a feature.
3. Pin a threshold per file in the manifest (start: the current value plus
   a margin); CI fails when a file gets worse. Thresholds only ever go
   down, in the same pull request that improves a file.
4. Each ADR 0001–0009 is accepted when its feature's pages in the corpus
   are within threshold and its keys are gone from the report.
5. Diff images (cera, PDFium, difference) for the worst pages are uploaded
   as CI artifacts.

**Robustness.**

1. Fuzz targets for every reader of untrusted structure added by the
   ADRs: shading meshes, tiling pattern cells, ICC profiles, CMaps, `/OCProperties`
   and visibility expressions, `/AcroForm` trees. 60 s each per CI run,
   longer in the weekly workflow; crashers become regression files in
   `testdata/fuzz`.
2. One table of all budgets in `budget.go` with their keys, documented in
   the README, each enforced by counting in `Stats.Unsupported` and
   degrading (draw less), never by failing the page.
3. The large-corpus workflow fails on any panic or hang (as today) and
   additionally reports the 20 slowest pages and the 20 with the most
   allocations, to catch pathological inputs that budgets do not yet cover.

## Implementation notes

- **Accuracy 1** lives in this repository instead of the stilus harness:
  `accuracy/` is a module of its own (`github.com/timzifer/cera/accuracy`,
  `replace` to `../`), so cera itself never depends on PDFium and no
  module cycle arises; it renders with PDFium as the harness does (go-pdfium
  v1.21 on wazero, exact matrix into a bitmap sized like cera's) and reads
  the pinned corpus with `internal/corpus`.
- **Accuracy 2–3, 5.** `go run .` in `accuracy/` records per page the
  share of pixels differing by more than 16 levels in any channel and the
  99th percentile, with both renderers' time and the page's
  `Stats.Unsupported` keys (`report.md`, `results.csv`). Each file's worst
  page is held against `accuracy/thresholds.json` (a file of its own, keyed
  by the manifest's names, so the manifest shared with stilus keeps its
  shape; `note` records a second reference). A file worse than its
  threshold, or without one, fails the `accuracy` job; `-update` pins new
  files (value + 10 % + 0.05 points, rounded up) and lowers improved ones,
  never raising one, and an improved file shows as a notice until it is
  lowered. Cera, PDFium and difference images of the 10 worst pages are in
  the job's artifact. PDFium's renderings are cached by the manifest, the
  synthetic drawings and `accuracy/go.sum`; the pinned corpus takes about
  30 s uncached, 7 s cached.
- **Beyond PDFium.** `accuracy/reference` (local, a module of its own
  with cgo for MuPDF) compares cera with PDFium, MuPDF, Poppler and
  Ghostscript by leave-one-out consensus (an engine is wrong where all
  others agree and it does not; pixels where the references disagree are
  contested), per pixel and in 4×4-pixel boxes, and the synthetic drawings
  with an exact rendering (`accuracy/exact`). This is the "second
  reference" of the decision, for every page rather than per file; CI
  still gates on PDFium alone.
- **Large corpora** run in random batches in CI: the `Large corpora`
  workflow renders `-sample` files per source for robustness and compares
  `-accuracy` of them with PDFium (report only, no thresholds), with the
  seed in the summary; the corpora are run in full locally with the same
  commands. `corpus run` and `accuracy` both take `-sample` and `-seed`.
- **Robustness 1.** New fuzz targets `FuzzPattern` (tiling pattern cells),
  `FuzzICC` (profiles, as colour space, image and shading),
  `FuzzLayers` (`/OCProperties`, configurations, membership dictionaries and
  visibility expressions) and `FuzzForm` (AcroForm trees, rendered with and
  without changed values); `FuzzShading` covers type 7 meshes as well. Each
  target runs 60 s per CI run in a matrix job and 10 minutes in the weekly
  workflow.
- **Robustness 2** is `budget.go`: every bound in one place, as budgets
  (counted, degrading), limits (malformed input past them) and caches, with
  a table `TestBudgetsDocumented` keeps in step with the README. New keys:
  `nesting-budget` (form and `q` nesting, formerly content errors),
  `mesh-budget` (triangles, patches, lattice rows; formerly silent),
  `annot-budget` (4 096 annotations per page, new), and `pattern-budget`
  now also counts cells drawn into a tile of lower resolution.
- **Robustness 3.** `corpus run -top 20` lists the slowest pages and those
  with the most allocations; with `-runs 0` the allocations of the single
  render are measured.

## Consequences

- Every rendering change becomes measurable; regressions are caught in the
  pull request that causes them.
- CI time grows by the PDFium renders of the pinned corpus (a few minutes);
  PDFium output is cached by corpus hash.
- PDFium is a reference, not ground truth; where it is known to be wrong
  (e.g. some blend modes in non-isolated groups), the threshold is set from
  a second reference (MuPDF or pdf.js) and the reason noted in the manifest.

## Alternatives considered

- **Golden images of cera itself**: catch regressions but say nothing about
  correctness, and every intended change rewrites them.
- **Perceptual metrics (SSIM)**: better for photographs, worse at the thin
  lines of drawings that are this project's focus.
