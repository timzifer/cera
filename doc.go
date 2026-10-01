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
//     whose cost per path is edge length plus covered spans, with the
//     shaders cera paints through: images, glyph masks, layers, gradients
//     and meshes.
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
// # Images
//
// Images reach a device as DrawImage with the matrix of their unit square.
// A document decodes each image once and keeps it in a bounded cache; an
// Image holds its samples as compactly as they come (one bit or one byte
// a pixel with a palette where it can) and its mask at the mask's own
// resolution. The raster device samples the mip level that fits the
// device resolution, so an image drawn small costs what its device pixels
// cost, not what its samples do.
//
// # Transparency
//
// Transparency groups and soft masks reach a device as BeginGroup/EndGroup
// and BeginMask/EndMask; an object painted with a blend mode or a soft
// mask arrives as a group of its own. The raster device draws a group into
// a layer covering only what the group can touch in the band being drawn
// and composites it through the clips around it. The display list drops
// the groups that need no layer, which are most of them.
//
// # Shadings and colour
//
// Shadings reach a device as FillShading, painting the current clip; a
// path, stroke or text painted with a shading pattern arrives as the
// shading filled through a clip of its shape. A document reads each
// shading once into what a device draws without PDF functions: a colour
// ramp for axial and radial shadings, a sampled texture for function-based
// ones and Gouraud-shaded triangles for the four mesh kinds. Colour spaces
// are converted to sRGB: Separation and DeviceN through their tint
// transforms (tabulated for one ink), CalGray, CalRGB, Lab and ICC
// profiles of the matrix/TRC kind through CIE XYZ; other ICC profiles
// fall back on the device space of as many components.
//
// # Robustness
//
// Broken content is skipped, not fatal: unknown or malformed operators are
// ignored, unsupported features are counted in Stats, a panic is recovered
// and reported as a *PanicError, and RenderOptions.Deadline bounds the time
// spent on one page.
package cera
