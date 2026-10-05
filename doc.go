// Package cera renders PDF pages to raster images in pure Go.
//
// cera joins four layers:
//
//   - parsing: internal/pdf (objects, cross-references, filters, repair,
//     encryption, a cache of decoded streams), safe for concurrent use;
//   - interpretation: this package scans content streams without
//     allocating per operand and turns their operators into calls on a
//     Device (graphics state, paths, clips, colours, forms, text), with
//     fonts read by internal/pdffont and github.com/go-opentype/opentype;
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
// path, stroke or text painted with a pattern arrives as the pattern
// painted through a clip of its shape: FillShading for a shading pattern,
// FillTile for a tiling pattern (one step rasterized at device resolution
// and repeated with wrap-around). A document reads each
// shading once into what a device draws without PDF functions: a colour
// ramp for axial and radial shadings, a sampled texture for function-based
// ones and Gouraud-shaded triangles for the four mesh kinds. Colour spaces
// are converted to sRGB: Separation and DeviceN through their tint
// transforms (tabulated for one ink), CalGray, CalRGB, Lab and ICC
// profiles of the matrix/TRC kind through CIE XYZ; other ICC profiles
// fall back on the device space of as many components.
//
// # Optional content
//
// Document.Layers reads the layers of a document (optional content groups
// and configurations). A Visibility selects which of them a render shows,
// RenderOptions.Layers passes it; it is a value the caller owns, so views
// of one document can show different layers. The display list records
// hidden content too and evaluates its layer tags once per render, so
// switching layers draws again without interpreting the page. Page.RunWith
// gives other devices only the content a Visibility shows.
//
// # Annotations
//
// Page.Annotations lists the annotations of a page with their flags and
// link targets. Rendering draws them after the page content from their
// appearance streams, or from appearances cera generates for markup
// annotations that have none; RenderOptions.Annotations and
// SkipAnnotation choose which, without interpreting the page again.
//
// # Forms
//
// Document.Form reads the fields of an interactive form and their widgets.
// The values a user enters live in a FormState the caller owns; rendering
// with RenderOptions.Form draws widgets whose value changed with generated
// appearances, each recorded once per value and drawn over the page's
// display list. A FormLayer drives a FormWidgetProvider, which draws
// widgets natively with a UI toolkit over the page image, and tells a
// render (FormLayer.Skip) to leave those widgets out of the image.
//
// # Robustness
//
// Broken content is skipped, not fatal: unknown or malformed operators are
// ignored, unsupported features are counted in Stats, a panic is recovered
// and reported as a *PanicError, and RenderOptions.Deadline bounds the time
// spent on one page.
package cera
