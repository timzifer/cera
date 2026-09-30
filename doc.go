// Package cera renders PDF pages to raster images in pure Go.
//
// cera joins three layers:
//
//   - parsing: github.com/go-pdfkit/reader (objects, cross-references,
//     filters, repair, encryption, content-stream tokenising);
//   - interpretation: this package turns content-stream operators into
//     device calls (graphics state, paths, clips, colours, form XObjects);
//   - rasterization: github.com/timzifer/stilus, a sparse CPU rasterizer
//     whose cost per path is edge length plus covered spans.
//
// # Use
//
//	doc, err := cera.Open(data)
//	r := cera.NewRenderer()            // one per goroutine, reuses buffers
//	img, stats, err := r.Render(doc, 0, cera.Options{DPI: 150})
//
// A Renderer owns its canvas and scratch buffers, so rendering many pages
// with one Renderer does not allocate per path after warm-up. Renderers are
// not safe for concurrent use.
//
// # Robustness
//
// Broken content is skipped, not fatal: unknown or malformed operators are
// ignored, unsupported features are counted in Stats, a panic inside the
// interpreter is recovered and reported as a *PanicError, and
// Options.Deadline bounds the time spent on one page.
package cera
