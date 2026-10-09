# Speed against other engines

Time relative to MuPDF (1.00× = as fast, 2.00× = twice the time, 0.50× = half). Geometric mean of the ratios per page; each ratio is the median of 5 paired runs. 150 dpi.

## One core, per page

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera | 0.65× | 0.11× | 0.37× | 0.82× | 0.83× | 0.48× | 0.70× | 0.48× | 0.73× |
| cera (bitmap reused) | 0.49× | 0.10× | 0.14× | 0.64× | 0.58× | 0.24× | 0.33× | 0.45× | 0.56× |
| PDFium | 1.14× | 0.29× | 0.71× | 1.25× | 1.31× | 1.55× | 1.62× | 1.80× | 0.69× |
| hayro | 1.38× | 0.13× | 0.40× | 2.01× | 1.70× | 1.35× | 1.28× | 1.70× | 0.55× |
| pdf.js | 3.80× | 0.27× | 2.78× | 6.25× | 5.86× | 9.28× | 3.54× | 0.98× | 2.07× |

## All 16 cores, whole documents

| | all files | documents of 5+ pages | one-page files |
|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× |
| cera | 0.22× | 0.54× | 0.19× |
| cera (bitmap reused) | 0.14× | 0.36× | 0.12× |
| PDFium | 0.84× | 1.20× | 0.73× |
| hayro | 0.62× | 1.22× | 0.51× |
| pdf.js | 1.91× | 6.42× | 1.47× |

Gain over one core, all pages: MuPDF 1.2× (one process per core); cera 2.0× (own threads); cera (bitmap reused) 2.4× (own threads); PDFium 1.2× (one process per core); hayro 1.2× (one process per core); pdf.js 1.1× (one process per core);

Gain over one core, documents of 5+ pages: MuPDF 2.6× (one process per core); cera 4.1× (own threads); cera (bitmap reused) 5.1× (own threads); PDFium 2.9× (one process per core); hayro 3.9× (one process per core); pdf.js 1.8× (one process per core);

Gain over one core, one-page files: MuPDF 1.0× (one process per core); cera 1.7× (own threads); cera (bitmap reused) 2.1× (own threads); PDFium 1.0× (one process per core); hayro 1.0× (one process per core); pdf.js 1.0× (one process per core);

## WebAssembly, one core, per page

| | all pages |
|---|---|
| MuPDF | 1.00× |
| pdf.js | 3.80× |
| cera (wasm, wazero) | 3.74× |
| cera (wasm, V8) | 1.66× |
| PDFium (wasm, wazero) | 2.02× |

## First page (open and draw page 1 in a fresh process)

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera | 0.37× | 0.13× | 0.22× | 1.08× | 0.31× | 0.28× | 0.63× | 0.45× | 0.73× |
| cera (bitmap reused) | 0.37× | 0.12× | 0.24× | 1.12× | 0.31× | 0.29× | 0.61× | 0.47× | 0.74× |
| PDFium | 0.67× | 0.29× | 0.61× | 0.94× | 0.74× | 0.53× | 1.21× | 0.75× | 1.07× |
| hayro | 0.35× | 0.13× | 0.15× | 0.71× | 0.28× | 0.23× | 0.63× | 0.95× | 0.44× |
| pdf.js | 6.28× | 0.70× | 11.49× | 11.77× | 14.46× | 11.09× | 24.06× | 4.18× | 11.36× |
| cera (wasm, wazero) | 2.10× | 1.00× | 0.79× | 7.04× | 1.26× | 1.40× | 2.65× | 3.34× | 4.11× |
| cera (wasm, V8) | 3.96× | 0.71× | 5.11× | 8.51× | 5.50× | 4.46× | 9.78× | 3.43× | 8.72× |
| PDFium (wasm, wazero) | 0.61× | 0.54× | 0.19× | 1.51× | 0.45× | 0.24× | 1.26× | 0.88× | 0.71× |

## Memory and allocations

| | peak RSS, median per file | largest |
|---|---|---|
| MuPDF | 60 MB | 114 MB |
| cera | 38 MB | 148 MB |
| cera (bitmap reused) | 22 MB | 89 MB |
| PDFium | 91 MB | 323 MB |
| hayro | 15 MB | 8644 MB |
| pdf.js | 153 MB | 994 MB |

cera allocates 1 times per page (median; 90th percentile 86, most 1713), 48 B, the bitmap not counted.
Drawing a page again from cera's display list (scrolling, another tile) takes 69 % of its first render.

## Checks


AMD Ryzen 7 5800H with Radeon Graphics, 16 cores, windows/amd64, go1.27.1; corpus 359fe26ef817 (41 files); 2026-10-09.

- MuPDF: mupdf 1.28.2 (PyMuPDF 1.28.2)
- cera: cera v0.6.0-2-g8e59a7f
- cera (bitmap reused): cera v0.6.0-2-g8e59a7f
- PDFium: pdfium 156.0.8076.0 (pypdfium2 5.14.0)
- hayro: hayro 0.8.0
- pdf.js: pdf.js 6.4.299 (Node 24.17.0)
- cera (wasm, wazero): cera v0.6.0-2-g8e59a7f
- cera (wasm, V8): cera v0.6.0-2-g8e59a7f
- PDFium (wasm, wazero): pdfium (wasm, go-pdfium 1.21.1)
