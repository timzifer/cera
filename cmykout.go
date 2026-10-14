package cera

import (
	"context"
	"image"
	"image/color"
	"sync"
)

// Drawing in the CMYK of the document (Page.RenderCMYK). A page is drawn
// twice through the pipeline of RGBA paints, with every colour projected
// as it is read: once to its cyan, magenta and yellow inks, in place of
// red, green and blue, once to its black in all three. Compositing works
// on each channel alike, so that the two passes are the four inks of the
// page, premultiplied by alpha over paper that has none; blend modes mix
// inks on their complements (stilus.LayerShader.Subtractive), and the
// non-separable ones take black from the backdrop or the source
// (LayerShader.Black), as PDF 2.0, 11.3.5 has it.
//
// Colours convert to the document's CMYK — its DeviceCMYK: the profile of
// its output intent, the caller's, or the bundled SWOP — as PDF 2.0,
// 10.4.2 describes: DeviceCMYK and ICCBased CMYK keep their values, grey
// is black alone, Separation and DeviceN colorants named Cyan, Magenta,
// Yellow and Black are those inks, others go through their alternate
// space. RGB and CIE colours go through the colour-to-ink table of the
// profile (B2A) where it has one, and else as PDF converts DeviceRGB, with
// black generation and full undercolour removal: k = min(1−r, 1−g, 1−b).
//
// A luminosity soft mask is drawn in a third projection, to the grey of
// the CMYK colour, 1 − min(1, 0.3c + 0.59m + 0.11y + k) (PDF 2.0,
// 10.4.2.3), the same in both passes; as compositing is linear, this is
// the luminosity of the mask group drawn in CMYK but where the sum is
// clipped at 1.

// proj is what colours are projected to as a view of a document reads
// them.
type proj uint8

const (
	projRGB  proj = iota // sRGB, as Render draws
	projCMY              // cyan, magenta, yellow inks in red, green, blue
	projK                // black ink in all three
	projGray             // the grey of the CMYK colour, for luminosity masks
)

// inked reports a projection to inks, whose groups blend subtractively.
func (p proj) inked() bool { return p == projCMY || p == projK }

// viewKey identifies a view of a document: its DeviceCMYK and what it
// projects colours to.
type viewKey struct {
	cs     *colorSpace
	approx string
	proj   proj
}

// projected returns the view of d that projects colours to p.
func (d *Document) projected(p proj) *Document {
	if d.proj == p {
		return d
	}
	return d.view(d.devCMYK, d.devApprox, p)
}

// view returns the view of d that converts DeviceCMYK through cs (nil: as
// the document says), counted as approx, projecting colours to p.
func (d *Document) view(cs *colorSpace, approx string, p proj) *Document {
	d.viewMu.Lock()
	defer d.viewMu.Unlock()
	k := viewKey{cs, approx, p}
	if v, ok := d.views[k]; ok {
		return v
	}
	v := &Document{docShared: d.docShared, cmykView: cmykView{devCMYK: cs, devApprox: approx, proj: p, id: docIDs.Add(1)}}
	v.dropWithCache()
	if d.views == nil {
		d.views = map[viewKey]*Document{}
	}
	d.views[k] = v
	return v
}

// maskDoc returns the view a luminosity soft mask is drawn with: grey of
// the CMYK colours where d projects to inks.
func (d *Document) maskDoc() *Document {
	if d.proj == projRGB {
		return d
	}
	return d.projected(projGray)
}

// viewFor returns the document page p is drawn with, projecting colours
// to pr.
func (p *Page) viewFor(pr proj) *Document { return p.cmykDoc().projected(pr) }

// colorOf converts a colour value of cs to the components the view draws
// with, straight, in [0, 1]: sRGB, or projected inks.
func (d *Document) colorOf(cs *colorSpace, v []float64) (r, g, b float64) {
	if d.proj == projRGB {
		return cs.rgb(v)
	}
	c := d.inks(cs, v)
	switch d.proj {
	case projCMY:
		return c[0], c[1], c[2]
	case projK:
		return c[3], c[3], c[3]
	}
	l := 1 - min(1, 0.3*c[0]+0.59*c[1]+0.11*c[2]+c[3])
	return l, l, l
}

// inks converts a colour value of cs to the inks of the document's CMYK.
func (d *Document) inks(cs *colorSpace, v []float64) [4]float64 {
	switch cs.kind {
	case csCMYK:
		return [4]float64{clamp01(v[0]), clamp01(v[1]), clamp01(v[2]), clamp01(v[3])}
	case csGray:
		return [4]float64{0, 0, 0, 1 - clamp01(v[0])}
	case csRGB:
		return d.rgbInks(clamp01(v[0]), clamp01(v[1]), clamp01(v[2]))
	case csCIE:
		r, g, b := cs.rgb(v)
		if cs.n == 1 {
			return [4]float64{0, 0, 0, 1 - r}
		}
		return d.rgbInks(r, g, b)
	case csIndexed:
		var c [maxComps]float64
		n := cs.indexed(v, c[:])
		return d.inks(cs.base, c[:n])
	case csTint:
		return d.tintInks(cs, v)
	}
	return [4]float64{}
}

// processInk is the ink a colorant of that name is, -1 for none.
func processInk(name string) int {
	switch name {
	case "Cyan":
		return 0
	case "Magenta":
		return 1
	case "Yellow":
		return 2
	case "Black":
		return 3
	}
	return -1
}

// tintInks converts tints of a Separation or DeviceN space: colorants that
// are process inks are those inks, and the space goes through its
// alternate otherwise.
func (d *Document) tintInks(cs *colorSpace, v []float64) [4]float64 {
	if len(cs.colorants) == cs.n {
		var out [4]float64
		process := true
		for i, n := range cs.colorants {
			k := processInk(n)
			if k < 0 {
				process = false
				break
			}
			out[k] = max(out[k], clamp01(v[i]))
		}
		if process {
			return out
		}
	}
	if cs.fn == nil {
		t := 0.0
		for _, x := range v[:cs.n] {
			t = max(t, clamp01(x))
		}
		return [4]float64{0, 0, 0, t}
	}
	out := cs.fn.eval(v[:cs.n])
	var c [maxComps]float64
	copy(c[:cs.base.n], out)
	return d.inks(cs.base, c[:cs.base.n])
}

// rgbInks converts an sRGB colour to inks: through the colour-to-ink
// table of the document's CMYK profile, or as PDF converts DeviceRGB to
// DeviceCMYK.
func (d *Document) rgbInks(r, g, b float64) [4]float64 {
	if cs, _ := d.deviceCMYK(); cs != nil && cs.cmyk != nil {
		if prof := cs.cmyk.Profile(); prof != nil && prof.B2A != nil {
			lin := vec3{srgbDecode(r), srgbDecode(g), srgbDecode(b)}
			ink, _ := prof.Ink(srgbD50.apply(lin))
			return ink
		}
	}
	c, m, y := 1-r, 1-g, 1-b
	k := min(c, m, y)
	return [4]float64{c - k, m - k, y - k, k}
}

// cmykPixels is how many pixels of a page RenderCMYK draws at a time: 4 MB
// of RGBA.
const cmykPixels = 1 << 20

var cmykBands = sync.Pool{New: func() any { return new([]byte) }}

// RenderCMYK draws the page into dst in the CMYK of the document, as a
// press would get it: its DeviceCMYK values as they are — black text 0 0 0
// 1, a 40 % cyan tint 40 % cyan — and every other colour converted (see
// above). dst has the pixel coordinates Render's has, and opt means what
// it means there, but for Background: what is drawn on nothing is paper,
// no ink. Overprint is not simulated.
//
// The page is drawn twice, once for cyan, magenta and yellow and once for
// black, a band at a time, each through a display list of its own.
func (p *Page) RenderCMYK(ctx context.Context, dst *image.CMYK, opt RenderOptions) (err error) {
	region := dst.Bounds()
	if !opt.Region.Empty() {
		region = region.Intersect(opt.Region)
	}
	band, stats := opt.Band, opt.Stats
	opt.Band, opt.Region = nil, image.Rectangle{}
	opt.SimulateOverprint = false
	opt.Background = color.RGBA{} // paper
	var first Stats
	if stats != nil {
		opt.Stats = new(Stats)
	}
	buf := cmykBands.Get().(*[]byte)
	defer cmykBands.Put(buf)
	rows := max(1, min(cmykPixels/max(region.Dx(), 1), 4096))
	if region.Empty() {
		region = image.Rectangle{}
	}
	for pass, pr := range []proj{projCMY, projK} {
		for y, n := region.Min.Y, 0; y < region.Max.Y || n == 0; y, n = y+rows, n+1 {
			r := image.Rect(region.Min.X, y, region.Max.X, min(y+rows, region.Max.Y))
			if need := 4 * r.Dx() * r.Dy(); cap(*buf) < need {
				*buf = make([]byte, need)
			}
			tile := &image.RGBA{Pix: (*buf)[:4*r.Dx()*r.Dy()], Stride: 4 * r.Dx(), Rect: r}
			if rerr := p.render(ctx, tile, opt, pr); rerr != nil && err == nil {
				err = rerr
			}
			if pass == 0 && n == 0 && stats != nil {
				first.set(opt.Stats)
			}
			for yy := r.Min.Y; yy < r.Max.Y; yy++ {
				src := tile.Pix[tile.PixOffset(r.Min.X, yy):][:4*r.Dx()]
				out := dst.Pix[dst.PixOffset(r.Min.X, yy):][:4*r.Dx()]
				for x := 0; x < len(src); x += 4 {
					// Premultiplied inks over paper are the inks.
					if pr == projCMY {
						out[x], out[x+1], out[x+2] = src[x], src[x+1], src[x+2]
					} else {
						out[x+3] = src[x]
					}
				}
			}
			if pass == 1 && band != nil && !r.Empty() {
				band(r)
			}
			if r.Empty() {
				break
			}
		}
	}
	if stats != nil {
		stats.set(&first)
	}
	return err
}
