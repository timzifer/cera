# Speed against other engines

Time relative to MuPDF (1.00× = as fast, 2.00× = twice the time, 0.50× = half). Geometric mean of the ratios per page; each ratio is the median of 5 paired runs. 150 dpi.

## One core, per page

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera | 0.68× | 0.11× | 0.47× | 0.82× | 0.89× | 0.57× | 1.43× | 0.48× | 0.72× |
| PDFium | 1.13× | 0.28× | 0.72× | 1.25× | 1.29× | 1.64× | 1.62× | 1.71× | 0.67× |
| hayro | 1.32× | 0.13× | 0.43× | 1.95× | 1.59× | 1.32× | 1.19× | 1.61× | 0.49× |
| pdf.js | 3.72× | 0.25× | 2.91× | 6.07× | 5.85× | 9.43× | 3.76× | 0.95× | 2.06× |

## All 16 cores, whole documents

| | all files | documents of 5+ pages | one-page files |
|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× |
| cera | 0.27× | 0.86× | 0.23× |
| PDFium | 0.81× | 1.16× | 0.71× |
| hayro | 0.63× | 1.21× | 0.52× |
| pdf.js | 2.18× | 6.77× | 1.71× |

Gain over one core, all pages: MuPDF 1.0× (one process per core); cera 1.5× (own threads); PDFium 1.0× (one process per core); hayro 1.0× (one process per core); pdf.js 0.9× (one process per core);

Gain over one core, documents of 5+ pages: MuPDF 2.2× (one process per core); cera 1.9× (own threads); PDFium 2.4× (one process per core); hayro 3.3× (one process per core); pdf.js 1.5× (one process per core);

Gain over one core, one-page files: MuPDF 0.8× (one process per core); cera 1.4× (own threads); PDFium 0.8× (one process per core); hayro 0.9× (one process per core); pdf.js 0.8× (one process per core);

## WebAssembly, one core, per page

| | all pages |
|---|---|
| MuPDF | 1.00× |
| pdf.js | 3.72× |
| cera (wasm) | 5.05× |
| PDFium (wasm) | 2.10× |

## First page (open and draw page 1 in a fresh process)

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera | 0.43× | 0.11× | 0.24× | 1.21× | 0.32× | 0.52× | 1.45× | 0.46× | 0.86× |
| PDFium | 0.69× | 0.28× | 0.65× | 0.93× | 0.71× | 0.57× | 1.73× | 0.87× | 1.00× |
| hayro | 0.39× | 0.13× | 0.23× | 0.69× | 0.27× | 0.39× | 1.05× | 1.02× | 0.44× |
| pdf.js | 7.07× | 0.68× | 15.03× | 11.93× | 14.71× | 21.15× | 42.25× | 4.03× | 11.87× |
| cera (wasm) | 2.64× | 1.05× | 0.90× | 7.69× | 1.23× | 2.39× | 7.44× | 4.10× | 5.55× |
| PDFium (wasm) | 0.70× | 0.59× | 0.28× | 1.59× | 0.43× | 0.42× | 3.03× | 0.79× | 0.73× |

## Memory and allocations

| | peak RSS, median per file | largest |
|---|---|---|
| MuPDF | 60 MB | 113 MB |
| cera | 41 MB | 165 MB |
| PDFium | 91 MB | 323 MB |
| hayro | 15 MB | 8644 MB |
| pdf.js | 149 MB | 995 MB |

cera allocates 1 times per page (median; 90th percentile 86, most 1713), 48 B, the bitmap not counted.
Drawing a page again from cera's display list (scrolling, another tile) takes 71 % of its first render.

## Checks

- Content differing from the other engines' consensus (accuracy/reference): MuPDF 0.33 %; cera 1.10 %; PDFium 5.17 %;

AMD Ryzen 7 5800H with Radeon Graphics, 16 cores, windows/amd64, go1.27.1; corpus 359fe26ef817 (41 files); 2026-10-06.

- MuPDF: mupdf 1.28.2 (PyMuPDF 1.28.2)
- cera: cera v0.4.0-dirty
- PDFium: pdfium 156.0.8076.0 (pypdfium2 5.14.0)
- hayro: hayro 0.8.0
- pdf.js: pdf.js 6.4.299 (Node 24.17.0)
- cera (wasm): cera v0.4.0-dirty
- PDFium (wasm): pdfium (wasm, go-pdfium 1.21.1)
