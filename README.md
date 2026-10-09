# cera

[![CI](https://github.com/timzifer/cera/actions/workflows/ci.yml/badge.svg)](https://github.com/timzifer/cera/actions/workflows/ci.yml)
[![Large corpora](https://github.com/timzifer/cera/actions/workflows/corpus-large.yml/badge.svg)](https://github.com/timzifer/cera/actions/workflows/corpus-large.yml)
[![Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/timzifer/cera/badges/coverage.json)](https://github.com/timzifer/cera/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/timzifer/cera.svg)](https://pkg.go.dev/github.com/timzifer/cera)

A PDF renderer in pure Go, aiming to be the fastest and leanest in the Go
ecosystem: MuPDF-class speed on technical drawings (≤ 1.5× MuPDF on one core,
faster than MuPDF on all cores), ≈ 0 allocations per page in steady state,
0 panics, no cgo, every GOOS/GOARCH including `js/wasm`.

- Text, images, transparency, shadings, patterns, colour management
- Optional content (layers), annotations, interactive forms
- Display list per page: parse once, draw only the visible region, on all cores
- Own lock-free PDF reader: pages of one document render concurrently
- Every bound on work and memory budgeted; damaged input draws less, never fails

## Performance

Time per page relative to MuPDF (lower is faster), one core, 150 dpi,
the pinned corpus of 113 pages:

| | all pages | papers | text, fonts | shadings | transparency | scans |
|---|---|---|---|---|---|---|
| MuPDF | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× | 1.00× |
| **cera** | **0.65×** | **0.82×** | **0.83×** | **0.48×** | **0.73×** | **0.70×** |
| PDFium | 1.14× | 1.25× | 1.31× | 1.80× | 0.69× | 1.62× |
| hayro | 1.38× | 2.01× | 1.70× | 1.70× | 0.55× | 1.28× |
| pdf.js | 3.80× | 6.25× | 5.86× | 0.98× | 2.07× | 3.54× |

- All 16 cores: 0.19× MuPDF on one-page files (cera splits a page into
  bands), 0.54× on documents of several pages.
- Drawing into the bitmap of the page before, as a viewer does: 0.49× on
  one core, 0.12× and 0.36× on 16. The table gives cera a new bitmap per
  page, as the other engines' bindings give them a new pixmap.
- Command line, PDF to PNG: `cmd/cera` 0.55× `mutool draw` on one core, 0.47×
  on 16 (PNGs written with [calamus](https://github.com/timzifer/calamus) at
  the fastest level, 1.27× the size of MuPDF's; with `image/png` 0.62× and
  0.73×; compressing bands while drawing, `-png stream`, 0.55× and 0.48×);
  Poppler 2.64×, Ghostscript 3.88×.
- WebAssembly, against native MuPDF: cera 1.66× on V8 (Chrome, Node), 3.74×
  on wazero; PDFium 2.02× on wazero (its module runs on wazero only).
- One allocation per page (median), 38 MB peak memory per file (median).

cera's own synthetic drawings, on which MuPDF is unusually slow (cera
0.11×), are left out of this table. Ratios only, never times: engines take
turns page by page, so the load of the machine falls on all alike; ratios
can still shift between processors, and cera's by up to 10 % with the
machine's load. Method, every category, where cera
loses and how to reproduce:
[Performance](docs/performance.md). AMD Ryzen 7 5800H, Windows, 2026-10-06.

## Install

```sh
go get github.com/timzifer/cera
```

## Use

```go
doc, err := cera.Open(data)
page, err := doc.Page(0)
dst := image.NewRGBA(page.Bounds(150.0 / 72)) // owned by the caller, reusable
err = page.Render(ctx, dst, cera.RenderOptions{
	Scale:      150.0 / 72,
	Background: color.RGBA{255, 255, 255, 255},
})
page.Release() // drop the cached display list when the page leaves the view

text, err := page.Text(ctx)
fmt.Println(text.String())
```

Command line:

```sh
go build -C cmd/cera -o "$PWD/cera" .   # a module of its own: it writes PNGs with calamus
./cera -dpi 150 -o 'page-%d.png' input.pdf
```

Pages into a new file ([`pdfedit`](https://pkg.go.dev/github.com/timzifer/cera/pdfedit)): copied as they are, in any order, turned by quarters:

```go
err = pdfedit.Extract(w, data, []pdfedit.Page{{Index: 2}, {Index: 0, Rotate: 90}})
```

## Documentation

| | |
|---|---|
| [Usage](docs/usage.md) | rendering options, font providers, layers, annotations, forms, devices |
| [Features](docs/features.md) | what is drawn today, and what is counted as unsupported |
| [Architecture](docs/architecture.md) | building blocks, display list, glyph and image caches, transparency, shadings, colour |
| [Budgets](docs/budgets.md) | every bound on work and memory per input |
| [Performance](docs/performance.md) | speed against MuPDF, PDFium, hayro, pdf.js, Poppler and Ghostscript, as ratios |
| [Development](docs/development.md) | CI, corpora, accuracy against PDFium and other engines |
| [ADRs](docs/adr/README.md) | architecture decisions; next up: GPU backend ([ADR 0011](docs/adr/0011-gpu-backend.md)) |

## License

MIT. `internal/pdf` and `internal/pdffont` contain code ported from
[go-pdfkit](https://github.com/go-pdfkit) under the BSD 3-Clause License;
see their `LICENSE-go-pdfkit` files.
