package cera

import (
	"github.com/go-opentype/opentype"
	"github.com/go-pdfkit/reader"

	"github.com/timzifer/cera/internal/cmap"
)

// Composite fonts read their strings through a CMap (PDF 2.0 9.7.5): which
// bytes make up a code, and which CID each code selects. The CID picks the
// widths, the vertical metrics and, through CIDToGIDMap or the program's
// charset, the glyph. The predefined CMaps are compiled into
// internal/cmap; embedded ones are parsed once per font.

// readCMap sets up the codes, writing mode and character collection of a
// composite font.
func (f *Font) readCMap(d *Document, dict reader.Dict) {
	enc := d.resolve(dict["Encoding"])
	switch e := enc.(type) {
	case reader.Name:
		f.cmap = cmap.Predefined(string(e))
	case *reader.Stream:
		f.cmap = d.embeddedCMap(e, 0)
	}
	if f.cmap == nil {
		// Unknown or unreadable: two-byte codes that are their own CIDs,
		// which is what nearly every composite font uses anyway.
		f.cmap = cmap.Identity(false)
		f.cmapMissing = enc != nil
	}
	f.vertical = f.cmap.WMode == 1
	kid := d.descendant(dict)
	if info := d.dict(kid["CIDSystemInfo"]); info != nil {
		if s, ok := reader.ToString(d.resolve(info["Ordering"])); ok {
			f.ordering = string(s)
		}
		if s, ok := reader.ToString(d.resolve(info["Registry"])); ok {
			f.registry = string(s)
		}
		if v, ok := d.num(info["Supplement"]); ok {
			f.supplement = int(v)
		}
	}
	if f.ordering == "" || f.ordering == "Identity" {
		if o := f.cmap.Ordering; o != "" && o != "Identity" {
			f.registry, f.ordering, f.supplement = f.cmap.Registry, o, f.cmap.Supplement
		}
	}
	if f.vertical {
		f.readVerticalMetrics(d, kid)
	}
	f.pdf.SetFallback(func(code int) (string, bool) {
		if r, ok := cmap.ToUnicode(f.ordering, f.cmap.Lookup(uint32(code))); ok {
			return string(r), true
		}
		return "", false
	})
}

// embeddedCMap reads a CMap stream; /UseCMap names its parent, a
// predefined CMap or another stream.
func (d *Document) embeddedCMap(s *reader.Stream, depth int) *cmap.CMap {
	if depth > 4 {
		return nil
	}
	data, _, err := d.r.DecodeStream(s)
	if err != nil {
		return nil
	}
	var parent *cmap.CMap
	switch u := d.resolve(s.Dict["UseCMap"]).(type) {
	case reader.Name:
		parent = cmap.Predefined(string(u))
	case *reader.Stream:
		if u != s {
			parent = d.embeddedCMap(u, depth+1)
		}
	}
	c, err := cmap.Parse(data, func(name string) *cmap.CMap {
		if parent != nil && (name == parent.Name || parent.Name == "") {
			return parent
		}
		return cmap.Predefined(name)
	})
	if err != nil {
		return nil
	}
	if parent != nil && c.Parent() == nil {
		c = c.WithParent(parent)
	}
	if v, ok := d.num(s.Dict["WMode"]); ok {
		c.WMode = int(v) & 1
	}
	return c
}

// descendant returns the CIDFont of a composite font.
func (d *Document) descendant(dict reader.Dict) reader.Dict {
	if a, ok := d.resolve(dict["DescendantFonts"]).(reader.Array); ok && len(a) > 0 {
		return d.dict(a[0])
	}
	return nil
}

// vmetric is a CID's vertical metrics in thousandths of an em: the
// vertical displacement w1 (negative: down) and the position vector v from
// the horizontal origin to the vertical one. vx < 0 means half the
// horizontal width (the default).
type vmetric struct{ w1, vx, vy float32 }

// readVerticalMetrics reads /DW2 and /W2.
func (f *Font) readVerticalMetrics(d *Document, kid reader.Dict) {
	f.dw2 = vmetric{w1: -1000, vx: -1, vy: 880}
	if a, ok := d.resolve(kid["DW2"]).(reader.Array); ok && len(a) == 2 {
		vy, ok1 := d.num(a[0])
		w1, ok2 := d.num(a[1])
		if ok1 && ok2 {
			f.dw2 = vmetric{w1: float32(w1), vx: -1, vy: float32(vy)}
		}
	}
	a, ok := d.resolve(kid["W2"]).(reader.Array)
	if !ok {
		return
	}
	const budget = 1 << 16 // CIDs with their own vertical metrics
	for i := 0; i < len(a) && len(f.w2) < budget; {
		first, ok := d.num(a[i])
		if !ok || i+1 >= len(a) {
			return
		}
		if list, ok := d.resolve(a[i+1]).(reader.Array); ok {
			for k := 0; k+2 < len(list) && len(f.w2) < budget; k += 3 {
				w1, ok1 := d.num(list[k])
				vx, ok2 := d.num(list[k+1])
				vy, ok3 := d.num(list[k+2])
				if ok1 && ok2 && ok3 {
					f.setW2(int(first)+k/3, vmetric{float32(w1), float32(vx), float32(vy)})
				}
			}
			i += 2
			continue
		}
		if i+4 >= len(a) {
			return
		}
		last, ok0 := d.num(a[i+1])
		w1, ok1 := d.num(a[i+2])
		vx, ok2 := d.num(a[i+3])
		vy, ok3 := d.num(a[i+4])
		if ok0 && ok1 && ok2 && ok3 && last >= first && last-first < budget {
			for c := int(first); c <= int(last) && len(f.w2) < budget; c++ {
				f.setW2(c, vmetric{float32(w1), float32(vx), float32(vy)})
			}
		}
		i += 5
	}
}

func (f *Font) setW2(cid int, m vmetric) {
	if f.w2 == nil {
		f.w2 = map[int]vmetric{}
	}
	f.w2[cid] = m
}

// verticalMetrics returns the vertical displacement (negative) and the
// position vector of a CID, in em; w0 is its horizontal width.
func (f *Font) verticalMetrics(cid int, w0 float64) (w1, vx, vy float64) {
	m, ok := f.w2[cid]
	if !ok {
		m = f.dw2
	}
	vx = float64(m.vx) / 1000
	if m.vx < 0 && !ok {
		vx = w0 / 2
	}
	return float64(m.w1) / 1000, vx, float64(m.vy) / 1000
}

// verticalForm returns the vertical form of a glyph when the program's
// GSUB table has one ('vert'). f.mu is held.
func (f *Font) verticalForm(gid opentype.GlyphIndex) opentype.GlyphIndex {
	if f.gsub == nil || gid == 0 {
		return gid
	}
	if v, ok := f.vert[gid]; ok {
		return v
	}
	v := gid
	if out := f.gsub.Apply([]opentype.GlyphIndex{gid}, "vert"); len(out) == 1 {
		v = out[0]
	}
	if f.vert == nil {
		f.vert = map[opentype.GlyphIndex]opentype.GlyphIndex{}
	}
	f.vert[gid] = v
	return v
}

// cidGlyph returns the glyph index of a CID. f.mu is held.
func (f *Font) cidGlyph(cid int) opentype.GlyphIndex {
	var gid opentype.GlyphIndex
	switch {
	case f.byUnicode:
		// A Unicode-keyed stand-in: the character the CID stands for.
		r, ok := cmap.ToUnicode(f.ordering, cid)
		if !ok && f.cmap.IsIdentity() {
			// Identity codes are CIDs; the document's ToUnicode may say
			// which character they show.
			if s, ok2 := f.pdf.Text(cid); ok2 {
				for _, c := range s {
					r, ok = c, true
					break
				}
			}
		}
		if ok {
			gid, _ = f.program.GlyphIndex(r)
		}
	case f.program.IsCIDKeyed():
		// A CID-keyed CFF program maps identifiers through its own
		// charset; CIDToGIDMap is defined only for TrueType-based ones.
		gid, _ = f.program.GlyphIndexByCID(cid)
	default:
		g, _ := f.pdf.CIDToGID(cid)
		gid = opentype.GlyphIndex(g)
	}
	if f.vertical {
		gid = f.verticalForm(gid)
	}
	return gid
}
