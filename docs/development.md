# Development

## CI

Every push and pull request runs `CI` (`.github/workflows/ci.yml`): lint
(golangci-lint), tests with race detector on Linux, macOS and Windows, a
cgo-free build for 14 targets including `js/wasm` and `wasip1/wasm`, 60 s of
fuzzing for each reader of untrusted structure, benchmarks, the pinned
corpus, its accuracy against PDFium, and `readerdiff`, which compares cera's
own reader and font layer with the go-pdfkit packages they replace
(ADR 0012) on every object, decoded stream, page and font code of the
pinned corpus. Its single
aggregate job `ci-ok` is the required check for `main`.

## Corpora

Corpora come in two tiers:

| tier | what | when | gate |
|---|---|---|---|
| pinned | 33 files from the pdf.js suite and arXiv (SHA-256 in `internal/corpus/manifest.json`, shared with the stilus harness) + 8 synthetic A3 drawings | every push | no panic, hang, deadline or unopened file; report with ms/page, allocations and unsupported features in the job summary; no file differs from PDFium more than its pinned threshold |
| large | pdf.js `test/pdfs` (~980 files), [borb-pdf-corpus](https://github.com/borb-pdf/borb-pdf-corpus), [veraPDF corpus](https://github.com/veraPDF/veraPDF-corpus), [pdfCabinetOfHorrors](https://github.com/openpreserve/format-corpus/tree/master/pdfCabinetOfHorrors), [PDF 2.0 examples](https://github.com/pdf-association/pdf20examples), qpdf test files, all at pinned commits; one sampled zip of [CC-MAIN-2021-31-PDF-UNTRUNCATED](https://digitalcorpora.org/corpora/file-corpora/cc-main-2021-31-pdf-untruncated/) (7.9 M web PDFs, a different shard each week) | weekly and on demand (`Large corpora` workflow), a random batch per source (300 files, 40 of them compared with PDFium); in full locally | no panic, hang or deadline; files the reader rejects are listed, not failed; the 20 slowest pages, the 20 with the most allocations and the differences to PDFium are reported |
| unsafe | [CC-MAIN-2021-31-UNSAFE](https://digitalcorpora.org/corpora/file-corpora/unsafe-docs-cc-main-2021-31-unsafe/) `corpora-pdf` (fuzzer output, deliberately malformed files) | on demand only: run the workflow with `sources` = `["unsafe"]` | same |

Locally:

```sh
go run ./cmd/corpus fetch  -dir testdata/corpus   # pinned files, SHA-256 verified
go run ./cmd/corpus scenes -dir testdata/corpus   # synthetic drawings
go run ./cmd/corpus run    -dir testdata/corpus -dpi 150 -out report

go run ./cmd/corpus sources                       # the large corpora
go run ./cmd/corpus get -source borb              # → testdata/borb
go run ./cmd/corpus get -source ccmain -sample 500 -seed 42
go run ./cmd/corpus run -dir testdata/borb -runs 0 -pages 3 -fail-open=false
go run ./cmd/corpus run -dir testdata/borb -runs 0 -sample 50 -seed 7   # a batch, as CI draws it
```

Customer drawings stay local: pass their directory as another `-dir`
(comma-separated); they are reported as category `local`.

`-workers` sets the goroutines per page (default 1, the single-core figure
of the spec; 0 = all cores). Every page is timed twice per run: a first
render (interpret and draw) and a render again with its display list cached.
`-page-workers N` (-1 = all cores) adds a throughput table: every page of a
freshly opened document, one after another and from N goroutines at once.

The reader comparison runs from its own module:

```sh
cd readerdiff
go run . objects -a v06 -b pdf -dir ../testdata/corpus   # objects, streams, pages
go run . objects -a v06 -b pdfra -dir ../testdata/corpus # the same through io.ReaderAt
go run . fonts -dir ../testdata/corpus                   # every font code
go test -bench . -benchmem                               # both readers, for benchstat
go test -fuzz FuzzParseDiff                              # differential fuzzing
```

Expected differences are listed in `readerdiff/allowlist.txt`, each with a
reason ADR 0012 names.

## Accuracy

`accuracy/` compares cera with PDFium (WebAssembly through go-pdfium, no
cgo; a module of its own, so cera does not depend on it) page by page
(ADR 0010): the share of pixels differing by more than 16 levels in any
channel and the 99th percentile of the difference, next to both renderers'
time and the page's `Stats.Unsupported` keys. Every file of the pinned
corpus has a threshold for its worst page in `accuracy/thresholds.json`; CI
fails when a file gets worse. Thresholds only go down, in the pull request
that improves a file: `-update` pins new files and lowers improved ones.
Cera, PDFium and difference images of the worst pages are in the job's
artifact.

```sh
cd accuracy
go run . -dir ../testdata/corpus -thresholds thresholds.json -refs ../testdata/pdfium -out ../report-accuracy
go run . -dir ../testdata/corpus -thresholds thresholds.json -refs ../testdata/pdfium -update   # after an improvement
go run . -dir ../testdata/borb -pages 3 -out ../report-borb                                     # a large corpus, report only
```

`-refs` keeps PDFium's renderings between runs; `-sample` and `-seed`
compare a random batch, as the `Large corpora` workflow does.

PDFium is a reference, not ground truth. For local runs,
`accuracy/reference` (a module of its own, with cgo) measures cera against
several references and against an exact rendering, and writes a report
(`report.html`, `pages.csv`, `summary.json`):

- **Consensus of engines.** PDFium (WebAssembly), MuPDF (linked through cgo
  with go-fitz, which ships its libraries; without cgo `mutool`), Poppler
  (`pdftoppm`) and Ghostscript (`gs`), whichever are available. Every
  engine, cera and each reference alike, is held against the median of all
  the others on the pixels where those agree; where the references disagree
  among themselves the pixel is *contested* and says nothing about who is
  right. Shares are of the inked area (pixels not paper white in some
  rendering). Each statistic comes twice: per pixel (*edges*: how lines and
  glyphs are antialiased, where renderers differ by taste) and in boxes of
  4×4 pixels (*content*: what is drawn).
- **Exact rendering** of the synthetic drawings (`accuracy/exact`): an
  independent interpreter for the operators they use computes the area of
  each pixel the geometry covers (exact along 64 sample rows per pixel,
  strokes as the union of segments, caps and joins, circles analytically).
  The report shows each engine's ink against it (1× exact, above heavier)
  and its error. `joins-caps` and `fills-subpixel` test every cap and join,
  the miter limit, skewed round caps, both winding rules, sub-pixel
  rectangles and an even-odd clip.

```sh
cd accuracy/reference
go run . -dir ../../testdata/corpus                         # → ../../report-reference/report.html
go run . -dir ../../testdata/borb -pages 3 -dpi 72 -sample 200
go run . -engines cera,pdfium,mupdf -match synthetic/       # a subset of engines and files
go run . -cmyk-profile USWebCoatedSWOP.icc                  # cera's DeviceCMYK through another profile
go run . -overprint                                         # cera with overprint simulated
```

The charts are drawn with [figure](https://github.com/timzifer/figure)
(SVG, once with light and once with dark tokens, the page showing the one
its colour scheme asks for); the tables below them hold every value.

The references' renderings are cached in `testdata/reference-cache` by
engine version, file content, resolution and page; cera is always rendered.
MuPDF is AGPL and Ghostscript AGPL as well: they are linked into or run by
this tool only, never by cera.

## Repository setup

`main` is protected by the ruleset in `.github/rulesets/main.json`: no direct
pushes, force pushes or deletion; changes land through pull requests with a
green `ci-ok`. Apply or update it with `scripts/protect-main.sh` (GitHub CLI,
admin rights) or import the file under Settings → Rules → Rulesets.
