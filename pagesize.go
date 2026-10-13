package cera

import (
	"errors"
	"fmt"
	"math"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
)

// The size of pages: their boxes set, their content put on a page of
// another format, several of them put on a sheet.
//
// The content of a page is never rewritten for it. A box is an entry of
// the page; on a page of another size the content is drawn as a form
// XObject through one matrix, and its annotations are moved to where the
// content went.

// Fit says how [Editor.ResizePage] puts what a page shows on the page of
// another size.
type Fit int

const (
	// FitInside scales it, its proportions kept, to lie within the new
	// page, in its middle: a Letter page on A4 keeps margins left and
	// right.
	FitInside Fit = iota
	// FitFill scales it, its proportions kept, to cover the new page, in
	// its middle: what hangs over two of the edges is cut off.
	FitFill
	// FitStretch scales it to the new page in each direction.
	FitStretch
	// FitNone does not scale it: it lies in the middle of the new page,
	// with a margin around it or cut off by the edges.
	FitNone
)

// pageGeo is what an editor changes of the geometry of a page.
type pageGeo struct {
	// m takes the default user space the page has in its document to the
	// one it is written with.
	m Matrix
	// boxes are the boxes of the page as they are now, in the space it
	// is written with; has says which of them the page states itself, and
	// set which are written.
	boxes [numBoxes]Rect
	has   [numBoxes]bool
	set   [numBoxes]bool
	// clip is the part of the page, in the space it has in its document,
	// that a resized page shows; clipped says that there is one.
	clip    Rect
	clipped bool
}

// boxNames are the keys of the boxes of a page.
var boxNames = [numBoxes]pdf.Name{BoxCrop: "CropBox", BoxMedia: "MediaBox", BoxBleed: "BleedBox", BoxTrim: "TrimBox", BoxArt: "ArtBox"}

func clipRect(r, to Rect) Rect {
	return Rect{max(r.X0, to.X0), max(r.Y0, to.Y0), min(r.X1, to.X1), min(r.Y1, to.Y1)}
}

// effective returns the boxes as a reader takes them: each within the
// MediaBox, the CropBox for one that is not stated or has no area there.
func (g *pageGeo) effective() PageBoxes {
	media := g.boxes[BoxMedia]
	box := func(b PageBox, def Rect) Rect {
		if g.has[b] || g.set[b] {
			if c := clipRect(g.boxes[b], media); c.Dx() > 0 && c.Dy() > 0 {
				return c
			}
		}
		return def
	}
	crop := box(BoxCrop, media)
	return PageBoxes{Media: media, Crop: crop, Bleed: box(BoxBleed, crop), Trim: box(BoxTrim, crop), Art: box(BoxArt, crop)}
}

// geometry returns the geometry of the page as the editor has it so far,
// with what a point of the page is in units of its user space and how the
// page is turned.
func (ep *editPage) geometry() (g *pageGeo, unit float64, rotate int, err error) {
	unit, rotate = 1, (ep.rotate%360+360)%360
	var boxes PageBoxes
	var has [numBoxes]bool
	if ep.blank {
		r := Rect{0, 0, ep.w, ep.h}
		boxes = PageBoxes{r, r, r, r, r}
	} else {
		pg, err := ep.src.Page(ep.index)
		if err != nil {
			return nil, 0, 0, err
		}
		boxes, unit, rotate = pg.Boxes(), pg.unit, (pg.Rotate+rotate)%360
		for b, key := range boxNames {
			_, has[b] = pg.doc.rect(pg.dict.Get(key))
		}
	}
	if ep.geo != nil {
		c := *ep.geo
		return &c, unit, rotate, nil
	}
	g = &pageGeo{m: Matrix{1, 0, 0, 1, 0, 0}, has: has}
	for b := range g.boxes {
		g.boxes[b] = boxes.Box(PageBox(b))
	}
	return g, unit, rotate, nil
}

// SetPageBox sets a box of page i to r, in default user space, as
// [Page.Boxes] has them: the CropBox to show and print less of a page, or
// more; the MediaBox to change the medium; the boxes of a page for print.
// The content of the page stays where it is. A box other than the MediaBox
// has to have an area within it — a reader clips it to that.
//
// Like [Editor.ResizePage], it changes the page as it is written: the
// other calls of this editor — Draw, the annotations, a redaction — go on
// speaking of the page as its document shows it.
func (e *Editor) SetPageBox(i int, box PageBox, r Rect) error {
	if err := e.check(i); err != nil {
		return err
	}
	if box < 0 || box >= numBoxes {
		return fmt.Errorf("cera: page box %d", box)
	}
	r = Rect{min(r.X0, r.X1), min(r.Y0, r.Y1), max(r.X0, r.X1), max(r.Y0, r.Y1)}
	for _, v := range [...]float64{r.X0, r.Y0, r.X1, r.Y1} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("cera: a box of %v", r)
		}
	}
	ep := &e.pages[i]
	g, unit, _, err := ep.geometry()
	if err != nil {
		return err
	}
	if box == BoxMedia {
		if w, h := r.Dx()*unit, r.Dy()*unit; !(w >= 1 && h >= 1) || w > maxPageSide || h > maxPageSide {
			return fmt.Errorf("cera: a page of %g by %g points: sides are from 1 to %d", w, h, maxPageSide)
		}
	} else if c := clipRect(r, g.boxes[BoxMedia]); !(c.Dx() > 0 && c.Dy() > 0) {
		return fmt.Errorf("cera: the box %v has no area within the MediaBox %v", r, g.boxes[BoxMedia])
	}
	g.boxes[box], g.set[box] = r, true
	ep.geo = g
	return nil
}

// ResizePage makes page i a page of width by height points, as it is
// displayed: what it shows — its CropBox — is put on the new page as fit
// says, and nothing else of it. Its annotations move with the content, and
// so do its BleedBox, TrimBox and ArtBox where it has them; the MediaBox
// and the CropBox are the new page. A Letter page becomes an A4 page with
// ResizePage(i, 595.28, 841.89, FitInside).
//
// The content is not rewritten: it becomes a form XObject that the page
// draws through a matrix, so that patterns, which a matrix in front of the
// content would leave where they were, move with what they fill. The
// content of a tagged page stays the page's, behind a matrix, so that the
// structure tree still finds what it names — unless the page paints with
// patterns: then it is a form like the others, and its structure
// elements no longer lead to their content. What lay outside the CropBox
// is still in the file and no longer shown.
// Destinations that name a place on the page by coordinates — of links, of
// the outline — are not moved; they are off by what the page was scaled.
// The text of form fields keeps its size in a rectangle that changed, and
// borders their width.
//
// It changes the page as it is written; see [Editor.SetPageBox]. Calls
// follow each other: a page resized twice is fitted twice.
func (e *Editor) ResizePage(i int, width, height float64, fit Fit) error {
	if err := e.check(i); err != nil {
		return err
	}
	if !(width >= 1 && height >= 1) || width > maxPageSide || height > maxPageSide {
		return fmt.Errorf("cera: a page of %g by %g points: sides are from 1 to %d", width, height, maxPageSide)
	}
	ep := &e.pages[i]
	g, unit, rotate, err := ep.geometry()
	if err != nil {
		return err
	}
	w, h := width/unit, height/unit
	if rotate%180 != 0 {
		w, h = h, w
	}
	eff := g.effective()
	crop := eff.Crop
	if !(crop.Dx() > 0 && crop.Dy() > 0) {
		return errors.New("cera: the page has no area")
	}
	sx, sy := w/crop.Dx(), h/crop.Dy()
	switch fit {
	case FitInside:
		sx = min(sx, sy)
		sy = sx
	case FitFill:
		sx = max(sx, sy)
		sy = sx
	case FitStretch:
	case FitNone:
		sx, sy = 1, 1
	default:
		return fmt.Errorf("cera: fit %d", fit)
	}
	m := Matrix{sx, 0, 0, sy, (w-sx*crop.Dx())/2 - sx*crop.X0, (h-sy*crop.Dy())/2 - sy*crop.Y0}
	// What the page shows now is all the new page gets of it.
	if back, ok := g.m.Invert(); ok {
		x0, y0 := back.Apply(crop.X0, crop.Y0)
		x1, y1 := back.Apply(crop.X1, crop.Y1)
		shown := Rect{min(x0, x1), min(y0, y1), max(x0, x1), max(y0, y1)}
		if g.clipped {
			shown = clipRect(shown, g.clip)
			shown.X1, shown.Y1 = max(shown.X0, shown.X1), max(shown.Y0, shown.Y1)
		}
		g.clip, g.clipped = shown, true
	}
	g.m = g.m.Mul(m)
	page := Rect{0, 0, w, h}
	for _, b := range [...]PageBox{BoxBleed, BoxTrim, BoxArt} {
		if !g.has[b] && !g.set[b] {
			continue
		}
		r := eff.Box(b)
		x0, y0 := m.Apply(r.X0, r.Y0)
		x1, y1 := m.Apply(r.X1, r.Y1)
		g.boxes[b], g.set[b] = Rect{x0, y0, x1, y1}, true
	}
	g.boxes[BoxMedia], g.set[BoxMedia] = page, true
	g.boxes[BoxCrop], g.set[BoxCrop] = page, true
	ep.geo = g
	return nil
}

// resized reports whether the editor changes the geometry of a page.
func (e *Editor) resized() bool {
	for i := range e.pages {
		if e.pages[i].geo != nil {
			return true
		}
	}
	return false
}

// plan describes the geometry for the internal writer.
func (g *pageGeo) plan() *pdfedit.Geometry {
	out := &pdfedit.Geometry{Boxes: map[pdf.Name][4]float64{}}
	eff := g.effective()
	for b, key := range boxNames {
		if g.set[b] {
			r := eff.Box(PageBox(b))
			out.Boxes[key] = [4]float64{r.X0, r.Y0, r.X1, r.Y1}
		}
	}
	if g.m != (Matrix{1, 0, 0, 1, 0, 0}) {
		out.M, out.Moves = g.m, true
		out.Clip = [4]float64{g.clip.X0, g.clip.Y0, g.clip.X1, g.clip.Y1}
	}
	return out
}

// Sheet says how [Editor.AddSheets] puts pages on sheets: Columns by Rows
// of them on a sheet of Width by Height points, with a Margin around the
// sheet and a Gap between the pages.
type Sheet struct {
	Width, Height float64
	Columns, Rows int
	Margin, Gap   float64
}

// AddSheets inserts, before page at (NumPages to append), sheets with
// pages of src on them: Columns by Rows on each, row by row from the top
// left, each page scaled to fit its place and set in the middle of it.
// pages are their indices in that order, -1 for a place left empty, which
// is how the pages of a booklet are imposed; without any, all pages of src
// are taken in their order. The last sheet may have places left over.
//
// A page is drawn as it is displayed, without its annotations — it is
// [Canvas.DrawPage] on new pages, and more can be drawn on the sheets
// with [Editor.Draw]. The pages themselves are not in the result unless
// they are in the editor for another reason.
func (e *Editor) AddSheets(at int, src *Document, s Sheet, pages ...int) error {
	if at < 0 || at > len(e.pages) {
		return fmt.Errorf("cera: insert position %d is out of range [0, %d]", at, len(e.pages))
	}
	if src == nil {
		return errors.New("cera: no document to take pages from")
	}
	if s.Columns < 1 || s.Rows < 1 || s.Columns*s.Rows > 1<<12 {
		return fmt.Errorf("cera: %d by %d pages on a sheet", s.Columns, s.Rows)
	}
	cw := (s.Width - 2*s.Margin - float64(s.Columns-1)*s.Gap) / float64(s.Columns)
	ch := (s.Height - 2*s.Margin - float64(s.Rows-1)*s.Gap) / float64(s.Rows)
	if s.Margin < 0 || s.Gap < 0 || !(cw > 0 && ch > 0) {
		return fmt.Errorf("cera: a sheet of %g by %g points leaves no room for %d by %d pages", s.Width, s.Height, s.Columns, s.Rows)
	}
	if len(pages) == 0 {
		for i := range src.NumPages() {
			pages = append(pages, i)
		}
	}
	for _, p := range pages {
		if p < -1 || p >= src.NumPages() {
			return fmt.Errorf("cera: page index %d is out of range", p)
		}
	}
	before := append([]editPage(nil), e.pages...)
	per := s.Columns * s.Rows
	for n := 0; n*per < len(pages); n++ {
		if err := e.AddPage(at+n, s.Width, s.Height); err != nil {
			e.pages = before
			return err
		}
		var failed error
		err := e.Draw(at+n, true, func(c *Canvas) {
			for k, p := range pages[n*per : min((n+1)*per, len(pages))] {
				if p < 0 {
					continue
				}
				pg, err := src.Page(p)
				if err != nil {
					failed = err
					return
				}
				w, h := pg.Size()
				if !(w > 0 && h > 0) {
					continue
				}
				x := s.Margin + float64(k%s.Columns)*(cw+s.Gap)
				y := s.Margin + float64(k/s.Columns)*(ch+s.Gap)
				f := min(cw/w, ch/h)
				r := Rect{x + (cw-f*w)/2, y + (ch-f*h)/2, x + (cw+f*w)/2, y + (ch+f*h)/2}
				if err := c.DrawPage(src, p, r); err != nil {
					failed = err
					return
				}
			}
		})
		if err == nil {
			err = failed
		}
		if err != nil {
			e.pages = before
			return err
		}
	}
	return nil
}
