package cera

import (
	"time"

	"github.com/timzifer/cera/internal/pdf"
)

// What markup annotations say (PDF 2.0, 12.5.6.2): who made them and when,
// how they look, where exactly they are, their popup and the thread of
// replies they belong to. A viewer lists comments from it and an editor
// shows a mark as a mark again.

// Markup describes a markup annotation beyond its type, rectangle and
// text. Geometry is in points of the displayed page, as the quads of
// [Link] and of [PageText] are, with y down.
type Markup struct {
	// Author is the text of the title bar of the popup, by convention
	// who made the annotation (/T), Subject what it is about (/Subj).
	Author  string
	Subject string
	// Created and Modified are zero when the annotation does not say.
	Created  time.Time
	Modified time.Time
	// RichText is the text of the annotation as XHTML (/RC), raw as the
	// file holds it; Annotation.Contents is its plain form.
	RichText string
	// Intent is what the annotation is meant as when its type leaves
	// that open (/IT): "FreeTextCallout", "LineArrow", "PolygonCloud", ...
	Intent string

	// Color is the colour (/C) and Interior that of the inside of a shape
	// and of line endings (/IC): gray, RGB or CMYK components from 0 to
	// 1, nil when the annotation gives none (for Interior: not filled).
	Color    []float64
	Interior []float64
	// Opacity is the constant opacity from 0 to 1 (/CA), 1 by default.
	Opacity float64
	// BorderWidth is the width of the lines in points, 1 by default,
	// BorderStyle their style and Dash the dash pattern of a dashed one.
	BorderWidth float64
	BorderStyle BorderStyle
	Dash        []float64

	// Bounds is the rectangle of the annotation (Annotation.Rect).
	Bounds Rect
	// Quads are the areas of text a Highlight, Underline, Squiggly,
	// StrikeOut or Redact annotation marks, corners in /QuadPoints order;
	// the rectangle when the annotation has none that hold.
	Quads []Quad
	// Ink are the strokes of an Ink annotation, each a path of points.
	Ink [][][2]float64
	// Vertices are the corners of a Polygon or PolyLine annotation.
	Vertices [][2]float64
	// Line is the start and end of a Line annotation, nil for others.
	Line [][2]float64
	// LineEndings are the styles of the ends of a Line or PolyLine and
	// of the callout of a FreeText annotation, which has only the first
	// (/LE): "None", "OpenArrow", "ClosedArrow", "Circle", ...
	LineEndings [2]string
	// Callout is the callout line of a FreeText annotation (/CL), two or
	// three points from the end at the place it points to.
	Callout [][2]float64

	// Popup is the window that shows the text, nil for none.
	Popup *Popup
	// Reply names the annotation this one replies to (/IRT), nil for
	// none.
	Reply *Reply
	// State and StateModel are the review state a reply sets on the
	// annotation it replies to: "Accepted" of "Review", "Marked" of
	// "Marked", ...
	State      string
	StateModel string
	// Icon names the icon of a Text, Stamp, FileAttachment or Sound
	// annotation (/Name): "Note", "Comment", "Approved", "PushPin", ...
	Icon string

	// DefaultAppearance is the font, size and colour of the text of a
	// FreeText annotation as content operators (/DA), and Align how its
	// text is aligned (/Q).
	DefaultAppearance string
	Align             Align
}

// Reply is the relation of a markup annotation to another one of its page.
type Reply struct {
	// To is the Index of the annotation replied to.
	To int
	// Group says that the annotation is a part of that one rather than
	// a reply to it (/RT /Group): viewers show both as one.
	Group bool
}

// Popup is the popup window of a markup annotation.
type Popup struct {
	// Index is the Index of the popup annotation.
	Index int
	// Bounds is its rectangle in points of the displayed page.
	Bounds Rect
	// Open says that the window is shown.
	Open bool
}

// markupTypes are the markup annotations (PDF 2.0, table 171).
var markupTypes = map[string]bool{
	"Text": true, "FreeText": true, "Line": true, "Square": true, "Circle": true,
	"Polygon": true, "PolyLine": true, "Highlight": true, "Underline": true,
	"Squiggly": true, "StrikeOut": true, "Caret": true, "Stamp": true, "Ink": true,
	"FileAttachment": true, "Sound": true, "Redact": true, "Projection": true,
}

// readMarkup fills in Annotation.Markup of the markup annotations of the
// page.
func (p *Page) readMarkup(annots []Annotation) {
	d := p.doc
	m := p.DisplayMatrix()
	bounds := func(r Rect) Rect {
		return Quad{[4][2]float64{{r.X0, r.Y0}, {r.X1, r.Y0}, {r.X0, r.Y1}, {r.X1, r.Y1}}}.Transform(m).Bounds()
	}
	points := func(o pdf.Object) [][2]float64 {
		v := d.floats(o)
		var out [][2]float64
		for i := 0; i+1 < len(v); i += 2 {
			x, y := m.Apply(v[i], v[i+1])
			out = append(out, [2]float64{x, y})
		}
		return out
	}

	// The annotations by reference, and the popups by their parent: a
	// popup names its annotation even where that does not name the popup.
	byRef := map[pdf.Ref]int{}
	popupOf := map[pdf.Ref]int{}
	for k := range annots {
		a := &annots[k]
		if a.ref != (pdf.Ref{}) {
			byRef[a.ref] = k
		}
		if parent, ok := a.dict.Get("Parent").Ref(); ok && a.Subtype == "Popup" {
			popupOf[parent] = k
		}
	}
	index := func(o pdf.Object) (int, bool) {
		ref, ok := o.Ref()
		if !ok {
			return 0, false
		}
		k, ok := byRef[ref]
		return k, ok
	}

	for k := range annots {
		a := &annots[k]
		if !markupTypes[a.Subtype] {
			continue
		}
		ad := a.dict
		text := func(key pdf.Name) string { return textString(d.resolve(ad.Get(key))) }
		name := func(key pdf.Name) string {
			n, _ := d.name(ad.Get(key))
			return string(n)
		}
		mk := &Markup{
			Author:     text("T"),
			Subject:    text("Subj"),
			Created:    parseDate(text("CreationDate")),
			Modified:   parseDate(text("M")),
			Intent:     name("IT"),
			Opacity:    1,
			Bounds:     bounds(a.Rect),
			State:      text("State"),
			StateModel: text("StateModel"),
		}
		switch rc := d.resolve(ad.Get("RC")); rc.Kind() {
		case pdf.KindString:
			mk.RichText = textString(rc)
		case pdf.KindStream:
			if s, ok := rc.Stream(); ok {
				mk.RichText = decodeTextString(d.r.Decode(s).Data)
			}
		}
		if len(mk.RichText) > maxInfoString {
			mk.RichText = "" // a part of it would not be XHTML
		}
		mk.Color, _ = d.annotColor(ad.Get("C"))
		mk.Interior, _ = d.annotColor(ad.Get("IC"))
		if v, ok := d.num(ad.Get("CA")); ok {
			mk.Opacity = clamp01(v)
		}
		mk.BorderWidth, mk.Dash = d.border(ad)
		switch s, _ := d.name(d.dict(ad.Get("BS")).Get("S")); {
		case s == "D" || s == "" && len(mk.Dash) > 0:
			mk.BorderStyle = BorderDashed
		case s == "B":
			mk.BorderStyle = BorderBeveled
		case s == "I":
			mk.BorderStyle = BorderInset
		case s == "U":
			mk.BorderStyle = BorderUnderline
		}

		switch a.Subtype {
		case "Highlight", "Underline", "Squiggly", "StrikeOut", "Redact":
			for _, q := range d.quadPoints(a) {
				mk.Quads = append(mk.Quads, q.Transform(m))
			}
		case "Ink":
			strokes, _ := d.resolve(ad.Get("InkList")).Array()
			for _, s := range strokes {
				if pts := points(s); len(pts) > 0 {
					mk.Ink = append(mk.Ink, pts)
				}
			}
		case "Polygon", "PolyLine":
			mk.Vertices = points(ad.Get("Vertices"))
		case "Line":
			if l := points(ad.Get("L")); len(l) == 2 {
				mk.Line = l
			}
		case "FreeText":
			if cl := points(ad.Get("CL")); len(cl) == 2 || len(cl) == 3 {
				mk.Callout = cl
			}
			da, _ := d.resolve(ad.Get("DA")).Str()
			mk.DefaultAppearance = string(da)
			if q, ok := d.integer(ad.Get("Q")); ok && q >= 0 && q <= 2 {
				mk.Align = Align(q)
			}
		case "Text", "Stamp", "FileAttachment", "Sound":
			mk.Icon = name("Name")
		}
		switch le := d.resolve(ad.Get("LE")); a.Subtype {
		case "Line", "PolyLine":
			ends, _ := le.Array()
			for i := 0; i < len(ends) && i < 2; i++ {
				n, _ := d.name(ends[i])
				mk.LineEndings[i] = string(n)
			}
		case "FreeText":
			n, _ := le.Name()
			mk.LineEndings[0] = string(n)
		}

		pk, ok := index(ad.Get("Popup"))
		if !ok || annots[pk].Subtype != "Popup" {
			pk, ok = popupOf[a.ref]
			ok = ok && a.ref != (pdf.Ref{})
		}
		if ok {
			open, _ := d.resolve(annots[pk].dict.Get("Open")).Bool()
			mk.Popup = &Popup{Index: annots[pk].Index, Bounds: bounds(annots[pk].Rect), Open: open}
		}
		if rk, ok := index(ad.Get("IRT")); ok && rk != k {
			mk.Reply = &Reply{To: annots[rk].Index, Group: name("RT") == "Group"}
		}
		a.Markup = mk
	}
}
