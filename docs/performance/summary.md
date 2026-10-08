# Speed against other engines

Time relative to MuPDF (1.00× = as fast, 2.00× = twice the time, 0.50× = half). Geometric mean of the ratios per page; each ratio is the median of 5 paired runs. 150 dpi.

## One core, per page

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera | 0.65× | 0.11× | 0.38× | 0.83× | 0.85× | 0.47× | 0.79× | 0.48× | 0.71× |
| PDFium | 1.14× | 0.28× | 0.72× | 1.26× | 1.31× | 1.55× | 1.60× | 1.73× | 0.66× |
| hayro | 1.35× | 0.13× | 0.42× | 1.96× | 1.65× | 1.26× | 1.28× | 1.66× | 0.53× |
| pdf.js | 3.73× | 0.26× | 2.85× | 6.24× | 5.63× | 9.03× | 3.58× | 0.93× | 1.95× |

## All 16 cores, whole documents

| | all files | documents of 5+ pages | one-page files |
|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× |
| cera | 0.21× | 0.56× | 0.18× |
| PDFium | 0.81× | 1.15× | 0.71× |
| hayro | 0.64× | 1.23× | 0.53× |
| pdf.js | 2.24× | 6.60× | 1.77× |

Gain over one core, all pages: MuPDF 1.1× (one process per core); cera 2.0× (own threads); PDFium 1.2× (one process per core); hayro 1.2× (one process per core); pdf.js 1.1× (one process per core);

Gain over one core, documents of 5+ pages: MuPDF 2.7× (one process per core); cera 4.0× (own threads); PDFium 3.2× (one process per core); hayro 3.8× (one process per core); pdf.js 1.9× (one process per core);

Gain over one core, one-page files: MuPDF 1.0× (one process per core); cera 1.7× (own threads); PDFium 1.0× (one process per core); hayro 1.0× (one process per core); pdf.js 1.0× (one process per core);

## WebAssembly, one core, per page

| | all pages |
|---|---|
| MuPDF | 1.00× |
| pdf.js | 3.73× |
| cera (wasm, wazero) | 3.71× |
| cera (wasm, V8) | 1.64× |
| PDFium (wasm, wazero) | 2.05× |

## First page (open and draw page 1 in a fresh process)

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera | 0.38× | 0.13× | 0.22× | 0.93× | 0.30× | 0.41× | 0.72× | 0.45× | 0.74× |
| PDFium | 0.67× | 0.29× | 0.53× | 0.90× | 0.75× | 0.52× | 1.74× | 0.74× | 0.96× |
| hayro | 0.38× | 0.13× | 0.17× | 0.79× | 0.30× | 0.35× | 0.68× | 0.98× | 0.45× |
| pdf.js | 6.61× | 0.67× | 13.04× | 10.74× | 16.64× | 15.35× | 26.65× | 4.18× | 11.32× |
| cera (wasm, wazero) | 2.16× | 1.01× | 0.83× | 6.19× | 1.30× | 1.80× | 2.80× | 3.41× | 4.11× |
| cera (wasm, V8) | 4.17× | 0.70× | 5.65× | 7.48× | 5.92× | 6.47× | 11.19× | 3.46× | 8.84× |
| PDFium (wasm, wazero) | 0.64× | 0.52× | 0.21× | 1.31× | 0.49× | 0.31× | 1.40× | 0.95× | 0.72× |

## Memory and allocations

| | peak RSS, median per file | largest |
|---|---|---|
| MuPDF | 60 MB | 113 MB |
| cera | 38 MB | 148 MB |
| PDFium | 91 MB | 323 MB |
| hayro | 15 MB | 8644 MB |
| pdf.js | 149 MB | 994 MB |

cera allocates 1 times per page (median; 90th percentile 86, most 1713), 48 B, the bitmap not counted.
Drawing a page again from cera's display list (scrolling, another tile) takes 69 % of its first render.

## Checks


AMD Ryzen 7 5800H with Radeon Graphics, 16 cores, windows/amd64, go1.27.1; corpus 359fe26ef817 (41 files); 2026-10-08.

- MuPDF: mupdf 1.28.2 (PyMuPDF 1.28.2)
- cera: cera v0.5.0-35-g161fe64
- PDFium: pdfium 156.0.8076.0 (pypdfium2 5.14.0)
- hayro: hayro 0.8.0
- pdf.js: pdf.js 6.4.299 (Node 24.17.0)
- cera (wasm, wazero): cera v0.5.0-35-g161fe64
- cera (wasm, V8): cera v0.5.0-35-g161fe64
- PDFium (wasm, wazero): pdfium (wasm, go-pdfium 1.21.1)
