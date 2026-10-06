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
go run ./cmd/cera -dpi 150 -o 'page-%d.png' input.pdf
```

## Documentation

| | |
|---|---|
| [Usage](docs/usage.md) | rendering options, font providers, layers, annotations, forms, devices |
| [Features](docs/features.md) | what is drawn today, and what is counted as unsupported |
| [Architecture](docs/architecture.md) | building blocks, display list, glyph and image caches, transparency, shadings, colour |
| [Budgets](docs/budgets.md) | every bound on work and memory per input |
| [Development](docs/development.md) | CI, corpora, accuracy against PDFium and other engines |
| [ADRs](docs/adr/README.md) | architecture decisions; next up: GPU backend ([ADR 0011](docs/adr/0011-gpu-backend.md)) |

## License

MIT. `internal/pdf` and `internal/pdffont` contain code ported from
[go-pdfkit](https://github.com/go-pdfkit) under the BSD 3-Clause License;
see their `LICENSE-go-pdfkit` files.
