package cera

import "math"

// overlay returns the text redaction annotation a asks to be written over
// its area, nil for none.
func (e *Editor) overlay(a Annotation) *redOverlay {
	d := e.base
	s := textString(d.resolve(a.dict.Get("OverlayText")))
	if s == "" {
		return nil
	}
	da := "/Helv 0 Tf 0 g"
	if b, ok := d.resolve(a.dict.Get("DA")).Str(); ok {
		da = string(b)
	}
	p := parseDA(da)
	q, _ := d.integer(a.dict.Get("Q"))
	return &redOverlay{
		text: s, rect: a.Rect, size: p.size, comps: p.comps,
		align: min(max(q, 0), 2), repeat: d.boolean(a.dict.Get("Repeat")),
	}
}

// write writes the overlay text with e, clipped to its rectangle.
func (ov *redOverlay) write(w *csw, e *encoder) {
	const pad = 1.0
	r := ov.rect
	asc, desc := e.f.Metrics()
	if asc-desc <= 0 {
		asc, desc = 0.8, -0.2
	}
	one := e.width(ov.text) // at size 1
	size := ov.size
	if !(size > 0) {
		// As large as fits: one line across, or lines of 12 points at
		// most where they repeat.
		size = (r.Dy() - 2*pad) / (asc - desc)
		if ov.repeat {
			size = min(size, 12)
		} else if one > 0 {
			size = min(size, (r.Dx()-2*pad)/one)
		}
	}
	if !(size > 0) || math.IsInf(size, 0) || r.Dx() <= 0 || r.Dy() <= 0 {
		return
	}
	w.op("q")
	w.num(r.X0)
	w.num(r.Y0)
	w.num(r.Dx())
	w.num(r.Dy())
	w.op("re W n")
	w.color(ov.comps, true)
	w.op("BT")
	e.size = size
	e.selectFont(w, nil)
	line := func(s string, y float64) {
		lw := e.width(s) * size
		x := r.X0 + pad
		switch {
		case ov.repeat:
		case ov.align == 1:
			x = r.X0 + (r.Dx()-lw)/2
		case ov.align == 2:
			x = r.X1 - pad - lw
		}
		w.matrix(Matrix{1, 0, 0, 1, x, y})
		w.op("Tm")
		show(w, e, s, 0, 0)
	}
	if !ov.repeat {
		line(ov.text, r.Y0+(r.Dy()-(asc-desc)*size)/2-desc*size)
	} else {
		// The text over and over, line by line from the top, to fill the
		// rectangle.
		s := ov.text
		for n := 0; one > 0 && e.width(s)*size < r.Dx() && n < 1000; n++ {
			s += " " + ov.text
		}
		for y, n := r.Y1-pad-asc*size, 0; y+asc*size > r.Y0 && n < 1000; y, n = y-(asc-desc)*size, n+1 {
			line(s, y)
		}
	}
	w.op("ET")
	w.op("Q")
}
