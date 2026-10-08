# Speed against other engines

Time relative to MuPDF (1.00× = as fast, 2.00× = twice the time, 0.50× = half). Geometric mean of the ratios per page; each ratio is the median of 5 paired runs. 150 dpi.

## One core, per page

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera | 0.65× | 0.11× | 0.37× | 0.82× | 0.82× | 0.50× | 0.71× | 0.49× | 0.71× |
| PDFium | 1.13× | 0.27× | 0.71× | 1.27× | 1.30× | 1.57× | 1.63× | 1.67× | 0.65× |
| hayro | 1.33× | 0.12× | 0.41× | 1.94× | 1.61× | 1.29× | 1.36× | 1.59× | 0.52× |
| pdf.js | 3.68× | 0.25× | 2.99× | 6.07× | 5.55× | 10.21× | 3.47× | 0.92× | 2.05× |

## All 16 cores, whole documents

| | all files | documents of 5+ pages | one-page files |
|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× |
| cera | 0.22× | 0.58× | 0.19× |
| PDFium | 0.82× | 1.17× | 0.71× |
| hayro | 0.63× | 1.26× | 0.52× |
| pdf.js | 2.28× | 6.53× | 1.82× |

Gain over one core, all pages: MuPDF 1.2× (one process per core); cera 1.9× (own threads); PDFium 1.2× (one process per core); hayro 1.2× (one process per core); pdf.js 1.1× (one process per core);

Gain over one core, documents of 5+ pages: MuPDF 3.0× (one process per core); cera 3.8× (own threads); PDFium 3.2× (one process per core); hayro 4.0× (one process per core); pdf.js 1.9× (one process per core);

Gain over one core, one-page files: MuPDF 1.0× (one process per core); cera 1.7× (own threads); PDFium 1.0× (one process per core); hayro 1.0× (one process per core); pdf.js 1.0× (one process per core);

## WebAssembly, one core, per page

| | all pages |
|---|---|
| MuPDF | 1.00× |
| pdf.js | 3.68× |
| cera (wasm, wazero) | 3.72× |
| cera (wasm, V8) | 1.61× |
| PDFium (wasm, wazero) | 2.05× |

## First page (open and draw page 1 in a fresh process)

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera | 0.39× | 0.12× | 0.24× | 1.19× | 0.31× | 0.54× | 0.47× | 0.47× | 0.74× |
| PDFium | 0.62× | 0.29× | 0.58× | 0.96× | 0.61× | 0.61× | 0.91× | 0.67× | 0.92× |
| hayro | 0.42× | 0.13× | 0.18× | 0.88× | 0.30× | 0.64× | 0.86× | 0.98× | 0.53× |
| pdf.js | 6.60× | 0.68× | 12.95× | 13.14× | 14.41× | 19.50× | 20.52× | 3.75× | 12.10× |
| cera (wasm, wazero) | 2.20× | 1.00× | 0.75× | 7.73× | 1.15× | 2.21× | 2.79× | 3.29× | 4.41× |
| cera (wasm, V8) | 4.33× | 0.71× | 6.29× | 10.19× | 5.75× | 7.73× | 8.42× | 3.37× | 9.51× |
| PDFium (wasm, wazero) | 0.68× | 0.56× | 0.19× | 1.54× | 0.46× | 0.43× | 1.33× | 1.08× | 0.77× |

## Memory and allocations

| | peak RSS, median per file | largest |
|---|---|---|
| MuPDF | 60 MB | 113 MB |
| cera | 38 MB | 149 MB |
| PDFium | 91 MB | 323 MB |
| hayro | 15 MB | 8644 MB |
| pdf.js | 153 MB | 995 MB |

cera allocates 1 times per page (median; 90th percentile 86, most 1713), 48 B, the bitmap not counted.
Drawing a page again from cera's display list (scrolling, another tile) takes 69 % of its first render.

## Checks


AMD Ryzen 7 5800H with Radeon Graphics, 16 cores, windows/amd64, go1.27.1; corpus 359fe26ef817 (41 files); 2026-10-08.

- MuPDF: mupdf 1.28.2 (PyMuPDF 1.28.2)
- cera: cera v0.5.0-32-g484701e
- PDFium: pdfium 156.0.8076.0 (pypdfium2 5.14.0)
- hayro: hayro 0.8.0
- pdf.js: pdf.js 6.4.299 (Node 24.17.0)
- cera (wasm, wazero): cera v0.5.0-32-g484701e
- cera (wasm, V8): cera v0.5.0-32-g484701e
- PDFium (wasm, wazero): pdfium (wasm, go-pdfium 1.21.1)
