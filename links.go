package cera

import (
	"context"
	"math"
	"regexp"
	"strings"
)

// Link is a place on a page that leads somewhere.
type Link struct {
	// Index is the link annotation's index in /Annots (Annotation.Index),
	// -1 for an address found in the text (PageText.URLs).
	Index int
	// Quads are the areas that react, in points of the displayed page: the
	// /QuadPoints of the annotation, else its rectangle.
	Quads []Quad
	// Target is where the link leads.
	Target LinkTarget
}

// Contains reports whether (x, y), in points of the displayed page, is on
// the link.
func (l *Link) Contains(x, y float64) bool {
	for i := range l.Quads {
		if l.Quads[i].Contains(x, y) {
			return true
		}
	}
	return false
}

// Links returns the link annotations of the page that lead somewhere,
// in the order of /Annots, leaving out hidden ones.
func (p *Page) Links() []Link {
	m := p.DisplayMatrix()
	var out []Link
	for i := range p.Annotations() {
		a := &p.annots[i]
		if a.Link == nil || a.Flags&(AnnotHidden|AnnotNoView) != 0 {
			continue
		}
		l := Link{Index: a.Index, Target: *a.Link}
		for _, q := range p.doc.quadPoints(a) {
			l.Quads = append(l.Quads, q.Transform(m))
		}
		out = append(out, l)
	}
	return out
}

// quadPoints returns the /QuadPoints of a in default user space, or its
// rectangle when it has none or they reach outside it, which tells to
// ignore them (PDF 2.0, 12.5.6.5).
func (d *Document) quadPoints(a *Annotation) []Quad {
	r := a.Rect
	rect := []Quad{{[4][2]float64{{r.X0, r.Y1}, {r.X1, r.Y1}, {r.X0, r.Y0}, {r.X1, r.Y0}}}}
	arr, _ := d.resolve(a.dict.Get("QuadPoints")).Array()
	if len(arr) < 8 || len(arr)%8 != 0 {
		return rect
	}
	const slack = 1 // a point: writers round
	var qs []Quad
	for i := 0; i < len(arr); i += 8 {
		var q Quad
		for k := range 4 {
			x, okx := d.num(arr[i+2*k])
			y, oky := d.num(arr[i+2*k+1])
			if !okx || !oky || x < r.X0-slack || x > r.X1+slack || y < r.Y0-slack || y > r.Y1+slack {
				return rect
			}
			q.P[k] = [2]float64{x, y}
		}
		qs = append(qs, q)
	}
	return qs
}

// urlPattern finds web and mail addresses in text.
var urlPattern = regexp.MustCompile(`(?i)\b(?:https?://|www\.|mailto:)[^\s<>"]+`)

// URLs returns the web and mail addresses written in the text (http://,
// https://, www. and mailto:), as links with Index -1; an address that
// starts with www. leads to http://. An address ends at white space, so
// one broken across lines is found up to the break.
func (t *PageText) URLs() []Link {
	c := t.searchCache()
	c.urlsOnce.Do(func() { c.urls = t.findURLs() })
	return c.urls
}

func (t *PageText) findURLs() []Link {
	s := t.plainText().s
	var out []Link
	for _, m := range urlPattern.FindAllStringIndex(s, -1) {
		u := trimURL(s[m[0]:m[1]])
		if strings.HasSuffix(u, "//") || strings.HasSuffix(u, ":") || strings.EqualFold(u, "www.") {
			continue // a scheme without an address
		}
		r := t.RangeOfOffsets(m[0], m[0]+len(u))
		uri := u
		if strings.HasPrefix(strings.ToLower(u), "www.") {
			uri = "http://" + u
		}
		out = append(out, Link{Index: -1, Quads: t.Quads(r), Target: LinkTarget{URI: uri, Page: -1}})
	}
	return out
}

// trimURL drops the punctuation that ends a sentence around an address,
// and closing brackets without an opening one in it.
func trimURL(u string) string {
	for u != "" {
		switch c := u[len(u)-1]; c {
		case '.', ',', ';', ':', '!', '?', '\'', '"':
		case ')', ']', '}':
			open := map[byte]byte{')': '(', ']': '[', '}': '{'}[c]
			if strings.Count(u, string(open)) >= strings.Count(u, string(c)) {
				return u
			}
		default:
			return u
		}
		u = u[:len(u)-1]
	}
	return u
}

// Destination is the view a link leads to, in points of the displayed
// target page. Fit is the kind of view, turned with the page: a /FitH on a
// page rotated by 90° fits the height of the displayed page and is a FitV.
type Destination struct {
	// Page is the 0-based target page.
	Page int
	// Fit is "XYZ", "Fit", "FitH", "FitV", "FitR", "FitB", "FitBH" or
	// "FitBV".
	Fit string
	// X and Y are the point to show at the top-left corner of the window
	// (XYZ), the top edge (FitH, FitBH: Y) or the left edge (FitV, FitBV:
	// X); NaN keeps the current one.
	X, Y float64
	// Zoom is the magnification of XYZ, 0 to keep the current one.
	Zoom float64
	// Rect is the area to fit into the window for FitR.
	Rect Rect
}

// Destination resolves t to a view of a page of d. It reports false for a
// target that is not in d: a URI, an external link, a named destination
// the document does not define or a page out of range. The coordinates
// are those of the page as the file turns it; a viewer that turns its view
// asks the page it shows, [Page.Destination].
func (d *Document) Destination(t *LinkTarget) (Destination, bool) {
	if t == nil || t.Page < 0 || t.Page >= d.NumPages() {
		return Destination{}, false
	}
	p, err := d.Page(t.Page)
	if err != nil {
		return Destination{}, false
	}
	return p.Destination(t)
}

// Destination resolves t, a target on this page, to a view of it in the
// points of the page as it is displayed — turned, for a page made by
// [Page.Rotated]. It reports false for a target that is not on the page.
func (p *Page) Destination(t *LinkTarget) (Destination, bool) {
	if t == nil || t.External || t.Page != p.index {
		return Destination{}, false
	}
	nan := math.NaN()
	param := func(i int) float64 {
		if i < len(t.Params) {
			return t.Params[i]
		}
		return nan
	}
	m := p.DisplayMatrix()
	// swap reports a page turned by 90° or 270°: user x runs along y.
	swap := math.Abs(m[0]) < math.Abs(m[2])
	dest := Destination{Page: t.Page, Fit: t.Fit, X: nan, Y: nan}
	switch t.Fit {
	case "XYZ":
		dest.X, dest.Y = displayPoint(m, param(0), param(1))
		if z := param(2); z > 0 {
			dest.Zoom = z
		}
	case "FitH", "FitBH":
		dest.X, dest.Y = displayPoint(m, nan, param(0))
		if swap {
			dest.Fit = strings.Replace(t.Fit, "H", "V", 1)
		}
	case "FitV", "FitBV":
		dest.X, dest.Y = displayPoint(m, param(0), nan)
		if swap {
			dest.Fit = strings.Replace(t.Fit, "V", "H", 1)
		}
	case "FitR":
		x0, y0, x1, y1 := param(0), param(1), param(2), param(3)
		if math.IsNaN(x0 + y0 + x1 + y1) {
			dest.Fit = "Fit"
			break
		}
		dest.Rect = Quad{[4][2]float64{{x0, y0}, {x1, y0}, {x0, y1}, {x1, y1}}}.Transform(m).Bounds()
	case "Fit", "FitB":
	default:
		dest.Fit = "Fit"
	}
	return dest, true
}

// displayPoint maps the point (x, y) of user space by m, a page's display
// matrix; a coordinate of the result that comes from a NaN is NaN.
func displayPoint(m Matrix, x, y float64) (float64, float64) {
	ux, uy := x, y
	if math.IsNaN(ux) {
		ux = 0
	}
	if math.IsNaN(uy) {
		uy = 0
	}
	dx, dy := m.Apply(ux, uy)
	const eps = 1e-9
	if math.IsNaN(x) && math.Abs(m[0]) > eps || math.IsNaN(y) && math.Abs(m[2]) > eps {
		dx = math.NaN()
	}
	if math.IsNaN(x) && math.Abs(m[1]) > eps || math.IsNaN(y) && math.Abs(m[3]) > eps {
		dy = math.NaN()
	}
	return dx, dy
}

// HitKind is what lies at a point of a page.
type HitKind uint8

// Hit kinds, for the pointer: an arrow, a hand, a hand or the widget's
// own, an I-beam.
const (
	HitNone HitKind = iota
	HitLink
	HitWidget
	HitText
)

// Hit is what lies at a point of a page.
type Hit struct {
	Kind HitKind
	// Link is the link under the point (HitLink).
	Link *Link
	// Widget is the form field widget under the point (HitWidget); cera
	// only reports it, editing is the form layer's.
	Widget *Widget
	// Pos is the text position nearest to the point (HitText), and Char
	// the character under it, -1 between characters.
	Pos  TextPos
	Char int
}

// Hit returns what lies at (x, y) on page i, in points of the displayed
// page: a link annotation, else an address in the text, else a visible
// widget, else a line of text, else nothing.
func (x *TextIndex) Hit(ctx context.Context, i int, px, py float64) (Hit, error) {
	t, err := x.Page(ctx, i)
	if t == nil {
		return Hit{}, err
	}
	links, p := x.links(i)
	for k := range links {
		if links[k].Contains(px, py) {
			return Hit{Kind: HitLink, Link: &links[k]}, nil
		}
	}
	urls := t.URLs()
	for k := range urls {
		if urls[k].Contains(px, py) {
			return Hit{Kind: HitLink, Link: &urls[k]}, nil
		}
	}
	if p != nil {
		m := p.DisplayMatrix()
		for _, w := range x.doc.Form().PageWidgets(i) {
			r := w.Rect
			q := Quad{[4][2]float64{{r.X0, r.Y1}, {r.X1, r.Y1}, {r.X0, r.Y0}, {r.X1, r.Y0}}}.Transform(m)
			if w.Flags&(AnnotHidden|AnnotNoView) == 0 && q.Contains(px, py) {
				return Hit{Kind: HitWidget, Widget: w}, nil
			}
		}
	}
	for li := range t.Lines {
		if t.Lines[li].Quad.Contains(px, py) {
			h := Hit{Kind: HitText, Pos: t.PosAt(px, py), Char: -1}
			if c, ok := t.CharAt(px, py); ok {
				h.Char = c
			}
			return h, nil
		}
	}
	return Hit{}, nil
}

// links returns the links and the page of page i, read once while the
// index keeps the page.
func (x *TextIndex) links(i int) ([]Link, *Page) {
	x.mu.Lock()
	e := x.pages[i]
	x.mu.Unlock()
	if e != nil {
		select {
		case <-e.ready:
		default:
			e = nil // laid out again by another caller
		}
	}
	if e == nil {
		p, err := x.page(i)
		if err != nil {
			return nil, nil
		}
		return p.Links(), p
	}
	e.linksOnce.Do(func() {
		if e.page != nil {
			e.links = e.page.Links()
		}
	})
	return e.links, e.page
}
