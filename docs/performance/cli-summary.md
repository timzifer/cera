# Command line, PDF to PNG

Wall clock of converting each file to PNGs at 150 dpi, from start to exit, relative to MuPDF (mutool draw); geometric mean per file of the median of 5 paired runs.

## One core

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF (mutool draw) | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera | 0.63× | 0.27× | 0.87× | 0.83× | 0.81× | 0.72× | 0.70× | 0.60× | 0.91× |
| Poppler (pdftoppm) | 2.65× | 1.06× | 2.49× | 5.94× | 3.17× | 2.26× | 2.60× | 4.32× | 2.94× |
| Ghostscript | 3.88× | 1.32× | 5.84× | 2.54× | 4.98× | 4.94× | 4.49× | 7.35× | 5.57× |

## All 16 cores (where the tool has threads)

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF (mutool draw) | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera | 0.76× | 0.67× | 0.86× | 0.82× | 0.84× | 0.82× | 0.73× | 0.56× | 0.92× |
| Poppler (pdftoppm) | 3.36× | 3.03× | 2.40× | 6.43× | 3.17× | 2.52× | 2.67× | 4.36× | 3.18× |
| Ghostscript | 4.88× | 3.67× | 5.83× | 2.66× | 5.06× | 5.42× | 4.60× | 7.35× | 5.95× |

AMD Ryzen 7 5800H with Radeon Graphics, 16 cores, windows/amd64; corpus 359fe26ef817 (41 files); 2026-10-06.

- MuPDF (mutool draw): mutool version 1.28.0
- cera: cera v0.4.0-dirty
- Poppler (pdftoppm): pdftoppm version 26.09.0
- Ghostscript: ghostscript 10.08.0
