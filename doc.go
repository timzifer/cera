// Package cera renders PDF pages to raster images in pure Go.
//
// cera joins four layers:
//
//   - parsing: github.com/go-pdfkit/reader (objects, cross-references,
//     filters, repair, encryption);
//   - interpretation: this package scans content streams without
//     allocating per operand and turns their operators into calls on a
//     Device (graphics state, paths, clips, colours, forms, text), with
//     fonts read by github.com/go-pdfkit/pdffont and
//     github.com/go-opentype/opentype;
//   - a display list: what a page draws at one scale, with device-space
//     boxes and a band index, so a page is interpreted once and only the
//     visible part is drawn, by several cores;
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
// a viewport), and only that part is drawn. The page keeps its display list
// for the next render at the same scale until Page.Release; bands of the
// page are drawn by RenderOptions.Workers goroutines (all cores by default).
//
// # Text
//
// Filled text reaches a device as GlyphRuns (Device.FillGlyphs): glyph
// outlines in em units with their device matrices. The raster device keeps
// a coverage mask per glyph, size and subpixel position. Page.Run drives
// any Device without a display list, and a device implementing TextDevice
// also receives the text shown in every render mode; Page.Text extracts
// the characters of a page with their boxes.
//
// # Robustness
//
// Broken content is skipped, not fatal: unknown or malformed operators are
// ignored, unsupported features are counted in Stats, a panic is recovered
// and reported as a *PanicError, and RenderOptions.Deadline bounds the time
// spent on one page.
package cera
