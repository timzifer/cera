# Command line, PDF to PNG

Wall clock of converting each file to PNGs at 150 dpi, from start to exit, relative to MuPDF (mutool draw); geometric mean per file of the median of 5 paired runs.

## One core

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF (mutool draw) | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera (`cmd/cera`: calamus after drawing, bands on all cores) | 0.55× | 0.25× | 0.71× | 0.72× | 0.69× | 0.69× | 0.56× | 0.53× | 0.83× |
| cera (`-png stream`: calamus, bands while drawn) | 0.55× | 0.24× | 0.70× | 0.71× | 0.70× | 0.68× | 0.56× | 0.52× | 0.82× |
| cera (`-png stdlib`: image/png, one core) | 0.62× | 0.28× | 0.80× | 0.81× | 0.80× | 0.76× | 0.64× | 0.58× | 0.88× |
| Poppler (pdftoppm) | 2.64× | 1.09× | 2.27× | 5.87× | 3.15× | 2.30× | 2.48× | 4.24× | 2.97× |
| Ghostscript | 3.88× | 1.34× | 5.52× | 2.56× | 4.95× | 4.98× | 4.37× | 7.06× | 5.77× |

## All 16 cores (where the tool has threads)

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF (mutool draw) | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera (`cmd/cera`: calamus after drawing, bands on all cores) | 0.47× | 0.27× | 0.74× | 0.33× | 0.62× | 0.70× | 0.55× | 0.34× | 0.76× |
| cera (`-png stream`: calamus, bands while drawn) | 0.48× | 0.28× | 0.73× | 0.38× | 0.61× | 0.66× | 0.53× | 0.35× | 0.75× |
| cera (`-png stdlib`: image/png, one core) | 0.73× | 0.65× | 0.83× | 0.81× | 0.80× | 0.80× | 0.67× | 0.52× | 0.94× |
| Poppler (pdftoppm) | 3.32× | 3.10× | 2.27× | 6.41× | 3.11× | 2.37× | 2.57× | 4.35× | 3.16× |
| Ghostscript | 4.89× | 3.75× | 5.61× | 2.70× | 5.07× | 5.40× | 4.44× | 7.25× | 6.10× |

## Size of the PNGs

Bytes written, relative to MuPDF (mutool draw); geometric mean per file.

| | all pages | technical drawings | vector graphics | papers (arXiv) | text and fonts | images | scans | shadings | transparency |
|---|---|---|---|---|---|---|---|---|---|
| MuPDF (mutool draw) | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| cera (`cmd/cera`: calamus after drawing, bands on all cores) | 1.27× | 1.17× | 1.95× | 1.25× | 1.75× | 0.90× | 1.77× | 0.90× | 1.29× |
| cera (`-png stream`: calamus, bands while drawn) | 1.27× | 1.17× | 1.95× | 1.25× | 1.75× | 0.90× | 1.77× | 0.90× | 1.29× |
| cera (`-png stdlib`: image/png, one core) | 1.27× | 1.17× | 1.94× | 1.25× | 1.74× | 0.90× | 1.77× | 0.90× | 1.29× |
| Poppler (pdftoppm) | 0.67× | 0.44× | 0.69× | 1.15× | 0.95× | 0.67× | 0.76× | 0.48× | 0.66× |
| Ghostscript | 0.61× | 0.61× | 0.78× | 0.55× | 0.61× | 0.58× | 0.52× | 0.61× | 0.66× |

AMD Ryzen 7 5800H with Radeon Graphics, 16 cores, windows/amd64; corpus 359fe26ef817 (41 files); 2026-10-08.

- MuPDF (mutool draw): mutool version 1.28.0
- cera (`cmd/cera`: calamus after drawing, bands on all cores): cera v0.5.0-23-gc72856f, -png encode
- cera (`-png stream`: calamus, bands while drawn): cera v0.5.0-23-gc72856f, -png stream
- cera (`-png stdlib`: image/png, one core): cera v0.5.0-23-gc72856f, -png stdlib
- Poppler (pdftoppm): pdftoppm version 26.09.0
- Ghostscript: ghostscript 10.08.0
