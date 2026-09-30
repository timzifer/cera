// Package cera renders PDF pages to raster images in pure Go.
//
// cera joins three layers:
//
//   - parsing: github.com/go-pdfkit/reader (objects, cross-references,
//     filters, repair, encryption, content-stream tokenising);
//   - interpretation: this package turns content-stream operators into
//     calls on a Device (graphics state, paths, clips, colours, forms);
//   - rasterization: github.com/timzifer/stilus, a sparse CPU rasterizer
//     whose cost per path is edge length plus covered spans.
//
// # Use
//
//	doc, err := cera.Open(data)
//	page, err := doc.Page(0)
//	dst := image.NewRGBA(page.Bounds(150.0 / 72))
//	err = page.Render(ctx, dst, cera.RenderOptions{Scale: 150.0 / 72})
//
// The destination belongs to the caller and can be reused; rasterizer
// buffers live in pooled workers, so steady-state rendering does not
// allocate page-sized memory. dst may cover only part of the page (a tile or
// a viewport), and only that part is drawn.
//
// # Robustness
//
// Broken content is skipped, not fatal: unknown or malformed operators are
// ignored, unsupported features are counted in Stats, a panic is recovered
// and reported as a *PanicError, and RenderOptions.Deadline bounds the time
// spent on one page.
package cera
