package cera

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// Making images smaller (SaveOptions.Images): the part of a smaller file
// that loses. The resolution of an image is not in it: it is its samples
// over the size it is drawn at. The pages are run for the matrix of every
// image they draw — in forms, patterns and the appearances of annotations
// too — and an image takes the largest size it is drawn at anywhere.
//
// An image is decoded to its samples in its own colour space, resampled
// where it is drawn finer than asked, and stored the smallest way of
// those that keep it: Flate, JPEG for a photograph in grey or RGB, CCITT
// Group 4 for one bit a pixel. It keeps its colour space, its /Decode and
// its masks, which are images of their own and made smaller as such.

// ImageOptions say how [Editor.SaveWith] makes images smaller.
type ImageOptions struct {
	// MaxPPI is the resolution, in pixels per inch where an image is
	// drawn largest, above which it is resampled to TargetPPI (MaxPPI
	// when 0); 0 resamples none. Pixels are averaged; of an image of
	// palette indices the middle one is taken. An image of two or four
	// bits a sample that is resampled gets eight; images of one bit a
	// pixel, stencil masks among them, are not resampled. An image is
	// replaced only by one that is stored smaller.
	MaxPPI, TargetPPI float64
	// JPEGQuality, from 1 to 100, stores photographs in grey or RGB as
	// JPEG at that quality: images of many colours, eight bits a sample,
	// without /Decode. Line art and pictures of few colours stay
	// lossless. 0 stores none as JPEG.
	JPEGQuality int
	// Bilevel stores images of one bit a pixel as CCITT Group 4, which
	// loses nothing.
	Bilevel bool
}

// Images of these filters are stored lossily already, or in a coding
// cera does not write: they are left as they are.
var keptImageFilters = map[pdf.Name]bool{"DCTDecode": true, "DCT": true, "JPXDecode": true, "JBIG2Decode": true}

// imageSeer is a device that is told of the images drawn rather than
// given them: the interpreter decodes nothing.
type imageSeer interface {
	seeImage(d *Document, ref pdf.Ref, s *pdf.Stream, ctm Matrix)
}

// imageMeter measures the images pages draw.
type imageMeter struct {
	nullDevice
	uses map[srcObj]*imageUse
}

type srcObj struct {
	r   *pdf.Document
	num int32
}

// imageUse is an image and the finest it is drawn at: samples per point
// along its width and its height where it is drawn largest.
type imageUse struct {
	d          *Document
	s          *pdf.Stream
	perX, perY float64
}

func (m *imageMeter) seeImage(d *Document, ref pdf.Ref, s *pdf.Stream, ctm Matrix) {
	if ref.Num <= 0 {
		return
	}
	m.see(d, ref, s, ctm, 0)
}

func (m *imageMeter) see(d *Document, ref pdf.Ref, s *pdf.Stream, ctm Matrix, depth int) {
	w, _ := d.integer(s.Dict.Get("Width"))
	h, _ := d.integer(s.Dict.Get("Height"))
	dx, dy := math.Hypot(ctm[0], ctm[1]), math.Hypot(ctm[2], ctm[3])
	if w <= 0 || h <= 0 || !(dx > 1e-6) || !(dy > 1e-6) {
		return
	}
	k := srcObj{d.r, ref.Num}
	u := m.uses[k]
	if u == nil {
		u = &imageUse{d: d, s: s, perX: math.Inf(1), perY: math.Inf(1)}
		m.uses[k] = u
	}
	u.perX, u.perY = min(u.perX, float64(w)/dx), min(u.perY, float64(h)/dy)
	if depth > 0 {
		return
	}
	// Its masks are drawn where it is.
	for _, key := range []pdf.Name{"SMask", "Mask"} {
		o := s.Dict.Get(key)
		if r, ok := o.Ref(); ok {
			if ms, ok := d.resolve(o).Stream(); ok {
				m.see(d, r, ms, ctm, depth+1)
			}
		}
	}
}

// imagePlan replaces the images of the pages of doc as opt says.
func (e *Editor) imagePlan(doc *pdfedit.Doc, opt *ImageOptions) error {
	target := opt.TargetPPI
	if target == 0 {
		target = opt.MaxPPI
	}
	switch {
	case opt.MaxPPI < 0 || target < 0 || math.IsNaN(opt.MaxPPI) || math.IsNaN(target):
		return errors.New("cera: image resolutions are not negative")
	case target > opt.MaxPPI:
		return fmt.Errorf("cera: target resolution %g above the most %g", target, opt.MaxPPI)
	case opt.JPEGQuality < 0 || opt.JPEGQuality > 100:
		return fmt.Errorf("cera: JPEG quality %d: want 1 to 100, or 0", opt.JPEGQuality)
	}
	m := &imageMeter{uses: map[srcObj]*imageUse{}}
	seen := map[srcObj]bool{}
	for _, ep := range e.pages {
		if ep.blank {
			continue
		}
		pk := srcObj{ep.src.r, int32(ep.index)}
		if seen[pk] {
			continue
		}
		seen[pk] = true
		p, err := ep.src.Page(ep.index)
		if err != nil {
			continue
		}
		// At the scale of points of the page, all layers shown: what a
		// hidden layer draws is in the file too, and may be shown.
		if err := p.RunWith(context.Background(), m, RunOptions{Scale: 1, Layers: &Visibility{}}); err != nil {
			return err
		}
	}
	for k, u := range m.uses {
		o, ok := u.smaller(opt, target)
		if !ok {
			continue
		}
		if doc.Replace == nil {
			doc.Replace = map[*pdf.Document]map[int32]pdf.Object{}
		}
		if doc.Replace[k.r] == nil {
			doc.Replace[k.r] = map[int32]pdf.Object{}
		}
		doc.Replace[k.r][k.num] = o
	}
	return nil
}

// samples are the decoded samples of an image: rows of w pixels of n
// components of bpc bits, each row padded to a byte.
type samples struct {
	w, h, n, bpc int
	data         []byte
}

func (s *samples) stride() int { return (s.w*s.n*s.bpc + 7) / 8 }

func (s *samples) at(x, y, c int) int {
	row := s.data[y*s.stride():]
	i := x*s.n + c
	switch s.bpc {
	case 8:
		return int(row[i])
	case 16:
		return int(row[2*i])<<8 | int(row[2*i+1])
	}
	bit := i * s.bpc
	return int(row[bit/8]>>(8-s.bpc-bit%8)) & (1<<s.bpc - 1)
}

func (s *samples) set(x, y, c, v int) {
	row := s.data[y*s.stride():]
	i := x*s.n + c
	switch s.bpc {
	case 8:
		row[i] = byte(v)
		return
	case 16:
		row[2*i], row[2*i+1] = byte(v>>8), byte(v)
		return
	}
	bit := i * s.bpc
	shift := 8 - s.bpc - bit%8
	mask := byte(1<<s.bpc-1) << shift
	row[bit/8] = row[bit/8]&^mask | byte(v)<<shift&mask
}

// smaller returns the image stored smaller as opt says, false where it
// stays as it is.
func (u *imageUse) smaller(opt *ImageOptions, target float64) (pdf.Object, bool) {
	d, s := u.d, u.s
	filters := d.resolve(s.Dict.Get("Filter"))
	if a, ok := filters.Array(); ok {
		for _, f := range a {
			if n, _ := d.name(f); keptImageFilters[n] {
				return pdf.Null, false
			}
		}
	} else if n, _ := d.name(filters); keptImageFilters[n] {
		return pdf.Null, false
	}
	if _, colourKey := d.resolve(s.Dict.Get("Mask")).Array(); colourKey {
		return pdf.Null, false // exact colours that resampling and JPEG change
	}
	mask := d.boolean(s.Dict.Get("ImageMask"))
	w, _ := d.integer(s.Dict.Get("Width"))
	h, _ := d.integer(s.Dict.Get("Height"))
	bpc, _ := d.integer(s.Dict.Get("BitsPerComponent"))
	n, indexed := 1, false
	if mask {
		bpc = 1
	} else {
		cs, _ := d.colorSpace(s.Dict.Get("ColorSpace"), pdf.Dict{}, 1)
		if cs == nil || cs.kind == csPattern {
			return pdf.Null, false
		}
		n, indexed = cs.n, cs.kind == csIndexed
	}
	switch bpc {
	case 1, 2, 4, 8, 16:
	default:
		return pdf.Null, false
	}
	if w <= 0 || h <= 0 || w*h > 1<<28 {
		return pdf.Null, false
	}
	dec := d.r.Decode(s)
	src := samples{w: w, h: h, n: n, bpc: bpc, data: dec.Data}
	if dec.Recovered || dec.Image != "" || len(src.data) < src.stride()*h {
		return pdf.Null, false
	}

	// Resampled where it is drawn finer than the most asked for.
	img := src
	// Of one bit a pixel nothing is resampled: thin lines would go, and
	// CCITT stores them small as they are.
	if opt.MaxPPI > 0 && bpc > 1 {
		nw, nh := w, h
		if ppi := u.perX * 72; ppi > opt.MaxPPI {
			nw = max(1, int(math.Ceil(float64(w)*target/ppi)))
		}
		if ppi := u.perY * 72; ppi > opt.MaxPPI {
			nh = max(1, int(math.Ceil(float64(h)*target/ppi)))
		}
		if nw < w || nh < h {
			img = resample(src, nw, nh, indexed || mask)
		}
	}

	dict := s.Dict
	for _, k := range []pdf.Name{"Filter", "DecodeParms", "Length", "DL"} {
		dict = without(dict, k)
	}
	dict = dict.With("Width", pdf.Integer(int64(img.w))).With("Height", pdf.Integer(int64(img.h)))
	if !mask {
		dict = dict.With("BitsPerComponent", pdf.Integer(int64(img.bpc)))
	}
	raw := img.data[:img.stride()*img.h]
	stored := func(st *pdf.Stream) int {
		b, _ := st.Unencrypted()
		return len(b)
	}
	best := pdfwrite.Flate(dict, raw)
	size := stored(best)
	consider := func(st *pdf.Stream) {
		if st != nil && stored(st) < size {
			best, size = st, stored(st)
		}
	}
	if opt.Bilevel && img.bpc == 1 && img.n == 1 {
		g4 := pdf.EncodeCCITTG4(raw, img.w, img.h)
		consider(pdf.NewStream(dict.With("Filter", pdf.Name("CCITTFaxDecode").Object()).With("DecodeParms", pdf.NewDict(
			pdf.Entry{Key: "K", Val: pdf.Integer(-1)},
			pdf.Entry{Key: "Columns", Val: pdf.Integer(int64(img.w))},
			pdf.Entry{Key: "Rows", Val: pdf.Integer(int64(img.h))},
		).Object()), g4))
	}
	if opt.JPEGQuality > 0 && !mask && !indexed && img.bpc == 8 && (img.n == 1 || img.n == 3) &&
		s.Dict.Get("Decode").IsNull() && jpegSpace(d, s.Dict.Get("ColorSpace")) && photograph(&img) {
		consider(jpegStream(dict, &img, opt.JPEGQuality))
	}
	// What it saves must pay for what the dictionary says more.
	if size+64 > len(d.r.Raw(s)) {
		return pdf.Null, false
	}
	return best.Object(), true
}

// without returns d without the entry k.
func without(d pdf.Dict, k pdf.Name) pdf.Dict {
	if !d.Has(k) {
		return d
	}
	var e []pdf.Entry
	for key, v := range d.All() {
		if key != k {
			e = append(e, pdf.Entry{Key: key, Val: v})
		}
	}
	return pdf.NewDict(e...)
}

// resample returns src at w by h: each pixel the average of those it
// covers, or with pick the one in its middle. Fewer than eight bits a
// sample become eight where they are averaged.
func resample(src samples, w, h int, pick bool) samples {
	out := samples{w: w, h: h, n: src.n, bpc: src.bpc}
	maxIn := 1<<src.bpc - 1
	scale := 1.0
	if !pick && src.bpc < 8 {
		out.bpc = 8
		scale = 255 / float64(maxIn)
	}
	out.data = make([]byte, out.stride()*h)
	for y := range h {
		y0, y1 := y*src.h/h, max((y+1)*src.h/h, y*src.h/h+1)
		for x := range w {
			x0, x1 := x*src.w/w, max((x+1)*src.w/w, x*src.w/w+1)
			for c := range src.n {
				if pick {
					out.set(x, y, c, src.at((x0+x1)/2, (y0+y1)/2, c))
					continue
				}
				sum := 0
				for yy := y0; yy < y1; yy++ {
					for xx := x0; xx < x1; xx++ {
						sum += src.at(xx, yy, c)
					}
				}
				v := float64(sum) / float64((y1-y0)*(x1-x0)) * scale
				out.set(x, y, c, int(math.Round(v)))
			}
		}
	}
	return out
}

// jpegSpace reports whether a colour space is one JPEG keeps: grey and
// RGB, device, calibrated or ICC-based, and a separation.
func jpegSpace(d *Document, o pdf.Object) bool {
	o = d.resolve(o)
	if n, ok := o.Name(); ok {
		return n == "DeviceGray" || n == "DeviceRGB" || n == "G" || n == "RGB"
	}
	a, _ := o.Array()
	if len(a) == 0 {
		return false
	}
	switch fam, _ := d.name(a[0]); fam {
	case "ICCBased", "CalGray", "CalRGB", "Separation":
		return true
	}
	return false
}

// photograph tells a photograph from line art: of the pixels on a grid
// over it, a photograph has many colours.
func photograph(s *samples) bool {
	if s.w*s.h < 32*32 {
		return false
	}
	colours := map[[3]int]bool{}
	step := max(1, int(math.Sqrt(float64(s.w*s.h)/4096)))
	for y := 0; y < s.h; y += step {
		for x := 0; x < s.w; x += step {
			var c [3]int
			for k := range s.n {
				c[k] = s.at(x, y, k)
			}
			colours[c] = true
		}
	}
	want := 256
	if s.n == 1 {
		want = 64
	}
	return len(colours) >= want
}

// jpegStream encodes s, grey or RGB of eight bits, as JPEG.
func jpegStream(dict pdf.Dict, s *samples, quality int) *pdf.Stream {
	var img image.Image
	if s.n == 1 {
		g := image.NewGray(image.Rect(0, 0, s.w, s.h))
		for y := range s.h {
			copy(g.Pix[y*g.Stride:y*g.Stride+s.w], s.data[y*s.stride():])
		}
		img = g
	} else {
		c := image.NewRGBA(image.Rect(0, 0, s.w, s.h))
		for y := range s.h {
			row := s.data[y*s.stride():]
			for x := range s.w {
				i := y*c.Stride + 4*x
				c.Pix[i], c.Pix[i+1], c.Pix[i+2], c.Pix[i+3] = row[3*x], row[3*x+1], row[3*x+2], 255
			}
		}
		img = c
	}
	var b bytes.Buffer
	if jpeg.Encode(&b, img, &jpeg.Options{Quality: quality}) != nil {
		return nil
	}
	return pdf.NewStream(dict.With("Filter", pdf.Name("DCTDecode").Object()), b.Bytes())
}
