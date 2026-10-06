# Command line, PDF to PNG

Wall clock of converting each file to PNGs at 150 dpi, from start to exit, relative to MuPDF (mutool draw); geometric mean per file of the median of 5 paired runs.

## One core

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF (mutool draw) | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera | 0.58× | 0.26× | 0.79× | 0.70× | 0.74× | 0.74× | 0.64× | 0.54× | 0.84× |
| Poppler (pdftoppm) | 2.63× | 1.06× | 2.64× | 5.90× | 3.08× | 2.35× | 2.62× | 4.00× | 2.95× |
| Ghostscript | 3.83× | 1.33× | 5.54× | 2.55× | 4.87× | 4.77× | 4.46× | 6.97× | 5.70× |

## All 16 cores (where the tool has threads)

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF (mutool draw) | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera | 0.50× | 0.29× | 0.76× | 0.34× | 0.64× | 0.71× | 0.59× | 0.37× | 0.75× |
| Poppler (pdftoppm) | 3.38× | 2.99× | 2.46× | 6.47× | 3.11× | 2.78× | 2.51× | 4.52× | 3.16× |
| Ghostscript | 4.82× | 3.56× | 5.86× | 2.71× | 4.96× | 5.22× | 4.34× | 7.40× | 6.05× |

AMD Ryzen 7 5800H with Radeon Graphics, 16 cores, windows/amd64; corpus 359fe26ef817 (41 files); 2026-10-06.

- MuPDF (mutool draw): mutool version 1.28.0
- cera: cera v0.4.0-9-g31bb217
- Poppler (pdftoppm): pdftoppm version 26.09.0
- Ghostscript: ghostscript 10.08.0
