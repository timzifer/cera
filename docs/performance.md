# Performance

How fast cera renders against other PDF engines, measured by
[`bench/`](../bench) on the pinned corpus (see [Development](development.md#corpora)).

**Every number here is a ratio, never a time.** Times depend on the
processor and on what else the machine is doing; the ratio of two engines
measured side by side does much less. `bench` measures times, turns them
into ratios straight away and writes none out. Absolute values are given
only for what does not depend on speed: allocations, memory, failed pages.

Below, **1.00× is MuPDF**; 0.50× takes half its time, 2.00× twice. 150
dpi, the pinned corpus of 41 files and 113 pages; machine and versions at
the end. The data behind every table is in [`performance/`](performance):
ratios per page and per file, no times.

## One core, per page

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="performance/single-dark.svg">
  <img alt="Time per page on one core relative to MuPDF, per category" src="performance/single-light.svg">
</picture>

| pages | all<br>113 | drawings¹<br>8 | papers<br>58 | text, fonts<br>20 | shadings<br>11 | transparency<br>7 | images<br>4 | scans<br>3 | vector<br>2 |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| **cera** | **0.68×** | **0.11×** | **0.82×** | **0.89×** | **0.48×** | **0.72×** | **0.57×** | **1.43×** | **0.47×** |
| PDFium | 1.13× | 0.28× | 1.25× | 1.29× | 1.71× | 0.67× | 1.64× | 1.62× | 0.72× |
| hayro | 1.32× | 0.13× | 1.95× | 1.59× | 1.61× | 0.49× | 1.32× | 1.19× | 0.43× |
| pdf.js | 3.72× | 0.25× | 6.07× | 5.85× | 0.95× | 2.06× | 9.43× | 3.76× | 2.91× |

¹ The drawings are cera's own synthetic A3 scenes (`cmd/corpus scenes`):
hatches, short strokes and contours of thousands of paths. MuPDF is unusually
slow on them (seconds per page; go-fitz, MuPDF linked statically, confirms
it), so this column says more about MuPDF's stroker than about cera. Read
the papers and text columns for everyday documents.

Where cera loses: scans (CCITT and JBIG2 images, 3 pages), where it takes
1.4× MuPDF's time; on transparency hayro and PDFium are ahead of it. A
page's ratio varies between paired runs by 9 % (median interquartile
range); for how far the means hold, see [How stable the ratios are](#how-stable-the-ratios-are).

## All cores, whole documents

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="performance/multi-dark.svg">
  <img alt="Time per document on all cores relative to MuPDF on all cores" src="performance/multi-light.svg">
</picture>

| 16 cores | all files (41) | documents of 5+ pages (6) | one-page files (34) |
|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× |
| **cera** | **0.27×** | **0.86×** | **0.23×** |
| PDFium | 0.81× | 1.16× | 0.71× |
| hayro | 0.63× | 1.21× | 0.52× |
| pdf.js | 2.18× | 6.77× | 1.71× |

Each engine's own gain over one core:

| | documents of 5+ pages | one-page files | how |
|---|---|---|---|
| cera | 1.9× | 1.4× | its own threads: pages concurrently, bands of a page |
| MuPDF | 2.2× | 0.8× | one process per core |
| PDFium | 2.4× | 0.8× | one process per core |
| hayro | 3.3× | 0.9× | one process per core |
| pdf.js | 1.5× | 0.8× | one process per core |

On a single page only cera uses more than one core, hence its lead on
one-page files. On documents of several pages the others scale better than
cera does: a page's bands and the pages of a document share the cores
poorly so far. On 16 cores, every engine is far from 16×: the documents
are of 5 to 22 pages, and their slowest page bounds the whole.

## First page

Opening a file and drawing page 1 in a fresh process, the wait before a
viewer shows something (one sample per file, noisier):

| | all files | drawings | papers | text, fonts |
|---|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× | 1.00× |
| **cera** | **0.43×** | **0.11×** | **1.21×** | **0.32×** |
| PDFium | 0.69× | 0.28× | 0.93× | 0.71× |
| hayro | 0.39× | 0.13× | 0.69× | 0.27× |
| pdf.js | 7.07× | 0.68× | 11.93× | 14.71× |

pdf.js pays here for compiling its JavaScript before its first page. cera is
behind MuPDF on the first page of the papers; why is not yet examined.

## WebAssembly

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="performance/wasm-dark.svg">
  <img alt="Time per page in WebAssembly relative to native MuPDF" src="performance/wasm-light.svg">
</picture>

One core, per page, against native MuPDF:

| | all pages |
|---|---|
| MuPDF, native | 1.00× |
| PDFium, WebAssembly (go-pdfium on wazero) | 2.10× |
| pdf.js (JavaScript, Node) | 3.72× |
| **cera, WebAssembly (`wasip1` on wazero)** | **5.05×** |

cera compiled to WebAssembly takes 7.4× its native time; PDFium's
WebAssembly build loses less against the native one measured here (1.9×;
not the same build). This is the weakest spot of the comparison for cera.

Much of it is the runtime. Both WebAssembly engines run on wazero, whose
compiler does not optimize. On seven pages (papers, a drawing, text, a
shading) the same `ceraworker.wasm` took 3.5× its native time under Node's
V8, the engine of Chrome, and 9.4× under wazero. In a browser, expect the
former. Within cera, builtin `min` and `max` on floats compile to runtime
calls in WebAssembly and cost about 30 % under V8; see
[#65](https://github.com/timzifer/cera/issues/65). PDFium was not measured
under V8.

## Memory

Peak resident memory of an engine's process per file (median over files,
and the largest), the interpreter included for pdf.js:

| | median | largest |
|---|---|---|
| hayro | 15 MB | 8.6 GB² |
| **cera** | **41 MB** | **165 MB** |
| MuPDF | 60 MB | 113 MB |
| PDFium | 91 MB | 323 MB |
| pdf.js | 149 MB | 995 MB |

² `tiling-pattern-box.pdf`.

cera allocates once per page (median; 90th percentile 86, most 1 713),
48 bytes, the bitmap not counted, counted as `cmd/corpus run` counts: the
page drawn, released, and drawn again from scratch. The count depends a
little on what the caches and pools hold: against `cmd/corpus run` it agrees
within 10 % or 5 allocations on 103 of 113 pages. Drawing a page again from
cera's display list (another tile, a scrolled viewport) takes 71 % of the
first render.

## Command line, PDF to PNG

The plain question "how long does converting this PDF to PNGs take?": each
tool's wall clock from start to exit, PNG encoding included, against
`mutool draw`.

| | all files | drawings | papers | text, fonts | scans |
|---|---|---|---|---|---|
| **one core** | | | | | |
| MuPDF (`mutool draw`) | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| **cera** (`cmd/cera`) | **0.63×** | **0.27×** | **0.83×** | **0.81×** | **0.70×** |
| Poppler (`pdftoppm`) | 2.65× | 1.06× | 5.94× | 3.17× | 2.60× |
| Ghostscript | 3.88× | 1.32× | 2.54× | 4.98× | 4.49× |
| **16 cores** | | | | | |
| MuPDF (`mutool draw -T 16 -B 256`) | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| **cera** | **0.76×** | **0.67×** | **0.82×** | **0.84×** | **0.73×** |
| Poppler (no threads) | 3.36× | 3.03× | 6.43× | 3.17× | 2.67× |
| Ghostscript (`-dNumRenderingThreads=16`) | 4.88× | 3.67× | 2.66× | 5.06× | 4.60× |

On the command line, starting the process and writing PNGs take a large
share, which narrows the differences. On the three scan pages cera's
library is slower than MuPDF's but `cmd/cera` is faster than `mutool`;
presumably start-up and encoding outweigh drawing there, which is not
yet examined. `mutool` gains much from
threads on the drawings; `cmd/cera` hardly does. It spends 61–88 % of its
time in Go's PNG encoder (about 90 % when rendering on 16 cores), which
runs on one core and tries every filter on every row; so this row measures
the encoder more than cera. Writing PNGs in parallel bands is
[#67](https://github.com/timzifer/cera/issues/67).

## Accuracy alongside

Share of the inked area whose content differs from the consensus of the
other engines (`accuracy/reference`, same corpus): MuPDF 0.33 %, cera
1.10 %, PDFium 5.17 %. hayro and pdf.js are not in that comparison yet.
No engine drew less than half the median engine's ink on any page.

## How stable the ratios are

The 47 pages of the pdf.js files were measured three times with cera,
MuPDF, PDFium and hayro: twice on a quiet machine (once inside the full run
above, once alone) and once with 8 of the 16 hardware threads kept busy by
another program:

| relative to MuPDF | quiet | quiet, again | 8 threads busy |
|---|---|---|---|
| PDFium | 1.26× | 1.21× | 1.21× |
| hayro | 1.23× | 1.22× | 1.21× |
| cera | 0.72× | 0.76× | 0.84× |

Between the native engines the ratios hold within a few per cent, under load
too: taking turns works. cera's ratio does not hold as well: it differs by
6 % between the two quiet runs and grows by 10 % under load, so cera loses
more than the C and Rust engines when other programs share the processor
(caches, memory bandwidth or its garbage collector; not yet examined). The
tables above come from a quiet machine; read cera's ratios as ±10 %.

## Machine and versions

AMD Ryzen 7 5800H (8 cores, 16 threads), Windows 11, Go 1.27.1; corpus
`359fe26ef817`; 2026-10-06. cera v0.4.0 (this branch); MuPDF 1.28.2
(PyMuPDF 1.28.2) and `mutool` 1.28.0; PDFium 156.0.8076.0 (pypdfium2
5.14.0); hayro 0.8.0; pdf.js 6.4.299 on Node 24.17.0 and @napi-rs/canvas
1.0.10; go-pdfium 1.21.1 on wazero 1.12.0; Poppler 26.09.0; Ghostscript
10.08.0.

## What is compared

| engine | language | binding used | threads of its own | WebAssembly | licence |
|---|---|---|---|---|---|
| cera | Go | the library | yes (pages and bands) | yes (`js`, `wasip1`) | MIT |
| MuPDF | C | PyMuPDF (official wheels, native library) | no¹ | — | AGPL |
| PDFium | C++ | pypdfium2 (native library) | no | via go-pdfium | BSD-3 / Apache-2 |
| hayro | Rust | the crate | no | — | MIT / Apache-2 |
| pdf.js | JavaScript | Node, drawing on @napi-rs/canvas | no² | runs in browsers | Apache-2 |

¹ MuPDF can split one page into bands on several threads (`mutool draw
-T`); its library API through PyMuPDF does not. ² pdf.js parses in a web
worker in a browser; in Node it parses and draws on one thread.

Each engine is driven through the binding a program in that language
would use, inside a worker process ([protocol](../bench/protocol.md)); the
worker times only the engine's own calls. The binding adds a call per page,
microseconds against pages of milliseconds.

## How it is measured

- **One core, per page.** Each page is loaded afresh and drawn at 150 dpi
  on white into a new bitmap, annotations included, as a viewer showing the
  page for the first time does. What an engine caches per document (fonts,
  decoded images) stays, as it would in a viewer. cera runs with
  `GOMAXPROCS=1` and `Workers: 1`, so even its garbage collector shares the
  one core.
- **Taking turns.** The engines are interleaved page by page and run by run
  (A B C, B C A, …), never one engine's block after another's: a change of
  load falls on all of them alike. Each page's ratio is the median of the
  ratios of paired runs (5 by default); a run of a very fast page
  is several renders averaged.
- **Untimed first.** Each page is drawn once before timing: caches, pools
  and JIT compilers (pdf.js, WebAssembly) warm up, and pages an engine
  cannot draw are found.
- **The mean.** Per category, the geometric mean of the page ratios: a
  page twice as slow counts as much as a page twice as fast, and a single
  enormous page does not decide the result, as it would in a sum of times.
- **All cores.** Whole documents, each engine in its own best way: cera
  draws pages concurrently and bands of a page on the cores left over; the
  engines without threads of their own run one process per core, each with
  the document open, taking the next page when done, as their
  documentation advises. The ratio is against MuPDF on all cores; the gain
  is each engine's own, all cores against one.
- **First page.** Opening a file and drawing its first page in a fresh
  process: the wait before a viewer shows anything. One sample per file,
  so noisier than the rest.
- **WebAssembly.** cera built for `wasip1` and PDFium as go-pdfium ships it,
  both on wazero, against the native engines.
- **Fairness checks.** A page an engine fails on leaves that engine's mean
  and is counted. Each first render records how much of the page is inked;
  an engine drawing less than half the median engine's ink is listed, so
  that speed bought by drawing less shows. Next to speed stands each
  engine's share of content differing from the consensus of the others
  (`accuracy/reference`).

## What it does not say

- Ratios shift between processors, compilers and versions; the footnote of
  each table names them. On a machine of your own, run `bench` there.
- The pinned corpus is 41 files. Categories with few pages are a hint, not
  a law.
- MuPDF's speed depends on its build: the official PyMuPDF wheel was
  checked against go-fitz (MuPDF linked statically) on the drawings and
  differs by about a third, both ways the same order.
- Speed is not quality: an engine may be fast on a page it draws wrongly.
  See [Development](development.md#accuracy) for accuracy.

## Reproduce

```sh
pip install -r bench/workers/requirements.txt
(cd bench/workers/pdfjs && npm ci)
cd bench
go run .              # → ../report-bench
go run . -cli -mutool path/to/mutool
```

hayro's worker is built with `cargo` if installed. On Windows, `mutool`
comes with MuPDF's release archive (`mupdf-<version>-windows.zip` from
mupdf.com). Engines that are missing are skipped and named in the report.
