# 0010. Accuracy against PDFium and robustness budgets

- Status: proposed
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

1. Add a cera engine to the stilus harness and render the pinned corpus at
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
