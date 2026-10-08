# Speed against other engines

Time relative to MuPDF (1.00× = as fast, 2.00× = twice the time, 0.50× = half). Geometric mean of the ratios per page; each ratio is the median of 5 paired runs. 150 dpi.

## One core, per page

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera | 0.65× | 0.11× | 0.34× | 0.82× | 0.83× | 0.48× | 0.71× | 0.49× | 0.75× |
| PDFium | 1.11× | 0.27× | 0.61× | 1.25× | 1.27× | 1.60× | 1.63× | 1.67× | 0.67× |
| hayro | 1.31× | 0.12× | 0.35× | 1.94× | 1.62× | 1.30× | 1.19× | 1.52× | 0.52× |
| pdf.js | 3.73× | 0.27× | 2.57× | 6.19× | 5.87× | 9.09× | 3.10× | 0.93× | 2.03× |

## All 16 cores, whole documents

| | all files | documents of 5+ pages | one-page files |
|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× |
| cera | 0.23× | 0.57× | 0.20× |
| PDFium | 0.77× | 1.07× | 0.68× |
| hayro | 0.61× | 1.05× | 0.51× |
| pdf.js | 2.12× | 5.82× | 1.69× |

Gain over one core, all pages: MuPDF 1.0× (one process per core); cera 1.8× (own threads); PDFium 1.1× (one process per core); hayro 1.1× (one process per core); pdf.js 1.0× (one process per core);

Gain over one core, documents of 5+ pages: MuPDF 2.3× (one process per core); cera 3.2× (own threads); PDFium 2.9× (one process per core); hayro 4.1× (one process per core); pdf.js 1.8× (one process per core);

Gain over one core, one-page files: MuPDF 0.9× (one process per core); cera 1.6× (own threads); PDFium 0.9× (one process per core); hayro 0.9× (one process per core); pdf.js 0.9× (one process per core);

## WebAssembly, one core, per page

| | all pages |
|---|---|
| MuPDF | 1.00× |
| pdf.js | 3.73× |
| cera (wasm, wazero) | 3.80× |
| cera (wasm, V8) | 1.62× |
| PDFium (wasm, wazero) | 2.07× |

## First page (open and draw page 1 in a fresh process)

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera | 0.36× | 0.12× | 0.16× | 1.07× | 0.30× | 0.39× | 0.57× | 0.45× | 0.71× |
| PDFium | 0.59× | 0.28× | 0.41× | 0.91× | 0.58× | 0.51× | 1.07× | 0.67× | 0.92× |
| hayro | 0.42× | 0.13× | 0.12× | 0.81× | 0.32× | 0.47× | 1.20× | 1.02× | 0.58× |
| pdf.js | 6.25× | 0.67× | 9.36× | 11.30× | 14.70× | 15.16× | 24.68× | 3.46× | 11.91× |
| cera (wasm, wazero) | 2.12× | 0.98× | 0.58× | 6.58× | 1.31× | 1.93× | 2.68× | 2.87× | 4.63× |
| cera (wasm, V8) | 4.01× | 0.73× | 3.62× | 8.23× | 5.55× | 6.93× | 10.11× | 2.90× | 8.99× |
| PDFium (wasm, wazero) | 0.63× | 0.60× | 0.14× | 1.45× | 0.46× | 0.30× | 1.42× | 0.86× | 0.73× |

## Memory and allocations

| | peak RSS, median per file | largest |
|---|---|---|
| MuPDF | 60 MB | 113 MB |
| cera | 38 MB | 148 MB |
| PDFium | 91 MB | 323 MB |
| hayro | 15 MB | 8643 MB |
| pdf.js | 153 MB | 995 MB |

cera allocates 3 times per page (median; 90th percentile 88, most 1715), 184 B, the bitmap not counted.
Drawing a page again from cera's display list (scrolling, another tile) takes 69 % of its first render.

## Checks


AMD Ryzen 7 5800H with Radeon Graphics, 16 cores, windows/amd64, go1.27.1; corpus 359fe26ef817 (41 files); 2026-10-08.

- MuPDF: mupdf 1.28.2 (PyMuPDF 1.28.2)
- cera: cera v0.5.0-27-ge4cd710
- PDFium: pdfium 156.0.8076.0 (pypdfium2 5.14.0)
- hayro: hayro 0.8.0
- pdf.js: pdf.js 6.4.299 (Node 24.17.0)
- cera (wasm, wazero): cera v0.5.0-27-ge4cd710
- cera (wasm, V8): cera v0.5.0-27-ge4cd710
- PDFium (wasm, wazero): pdfium (wasm, go-pdfium 1.21.1)
