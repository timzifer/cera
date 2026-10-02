package cera

import (
	"bytes"
	"container/list"
	"image"
	"image/draw"
	"math"
	"unsafe"

	"github.com/go-images/jpeg"
	"github.com/go-images/jpeg2000"
	"github.com/go-pdfkit/reader"
	"github.com/tannevaled/gobig2"
	"github.com/timzifer/stilus"
)

// Decoding turns an image XObject or inline image into planes (see
// image.go): samples through /Decode and the colour space into a palette
// or premultiplied pixels, JPEG (DCTDecode), JPEG 2000 (JPXDecode) and
// JBIG2 through their codecs, CCITT faxes through the reader's filter, and
// the three kinds of mask: /SMask, a /Mask stream and a /Mask colour key.
// An image whose mask is named but cannot be read is not drawn: drawn
// whole it would cover what the mask was meant to show (the ink layer of
// a scanned page is a dark rectangle shaped by its mask).

const (
	// maxImageBytes bounds one decoded plane.
	maxImageBytes = 256 << 20
	// maxCodecPixels bounds the size a JPEG or JPEG 2000 codestream may
	// declare: its decoder allocates the whole picture (and more) first.
	maxCodecPixels = 64 << 20
	// imageCacheBytes bounds the decoded images a document keeps.
	imageCacheBytes = 256 << 20
)

// imageResult is a decoded image, or why there is none.
type imageResult struct {
	img *Image
	// unsupported names a feature that kept the image from being drawn;
	// empty with img nil means the image is broken.
	unsupported string
	// approx names a feature that is only approximated (a colour space).
	approx string
	// recovered reports that the data was damaged and the image may be
	// partial.
	recovered bool
}

// planeUse is what the samples of a plane are read as.
type planeUse uint8

const (
	useColor   planeUse = iota // colours in the image's colour space
	useAlpha                   // DeviceGray levels as alpha (soft masks)
	useStencil                 // one bit a pixel, 0 opaque (image and stencil masks)
)

// imageDecoder decodes one image and the masks it names.
type imageDecoder struct {
	d   *Document
	res reader.Dict
	out imageResult
}

// image returns the decoded image XObject s, from the document's cache
// when ref (its object) has been decoded before.
func (d *Document) image(ref reader.Ref, s *reader.Stream, res reader.Dict) imageResult {
	cache := ref != (reader.Ref{})
	if cache {
		d.imgMu.Lock()
		if e, ok := d.imgs[ref]; ok {
			d.imgLRU.MoveToFront(e)
			r := e.Value.(*imageEntry).res
			d.imgMu.Unlock()
			return r
		}
		d.imgMu.Unlock()
	}
	r := d.decodeImage(s.Dict, s.Raw, res)
	if cache {
		d.cacheImage(ref, r)
	}
	return r
}

type imageEntry struct {
	ref  reader.Ref
	res  imageResult
	size int
}

func (d *Document) cacheImage(ref reader.Ref, r imageResult) {
	size := 64
	if r.img != nil {
		size += r.img.size
	}
	if size > imageCacheBytes {
		return
	}
	d.imgMu.Lock()
	defer d.imgMu.Unlock()
	if d.imgs == nil {
		d.imgs = map[reader.Ref]*list.Element{}
	}
	if _, ok := d.imgs[ref]; ok {
		return
	}
	d.imgs[ref] = d.imgLRU.PushFront(&imageEntry{ref: ref, res: r, size: size})
	d.imgBytes += size
	for d.imgBytes > imageCacheBytes {
		e := d.imgLRU.Back()
		old := e.Value.(*imageEntry)
		d.imgLRU.Remove(e)
		delete(d.imgs, old.ref)
		d.imgBytes -= old.size
	}
}

// decodeImage decodes an image from its (expanded) dictionary and its
// still encoded data. res resolves named colour spaces.
func (d *Document) decodeImage(dict reader.Dict, raw []byte, res reader.Dict) imageResult {
	dec := imageDecoder{d: d, res: res}
	dec.decode(dict, raw)
	return dec.out
}

func (dc *imageDecoder) decode(dict reader.Dict, raw []byte) {
	d := dc.d
	img := &Image{}
	img.Stencil = d.boolean(dict["ImageMask"])
	img.Interpolate = d.boolean(dict["Interpolate"])
	use := useColor
	if img.Stencil {
		use = useStencil
	}
	p, key, ok := dc.plane(dict, raw, use)
	if !ok {
		return
	}
	img.W, img.H = p.W, p.H
	if img.Stencil {
		img.mask = stilus.NewTexture(p)
	} else {
		img.color = stilus.NewTexture(p)
		switch {
		case d.stream(dict["SMask"]) != nil:
			s := d.stream(dict["SMask"])
			if _, ok := s.Dict["Matte"]; ok {
				dc.out.approx = "smask-matte"
			}
			m, _, ok := dc.plane(s.Dict, s.Raw, useAlpha)
			if !ok {
				return
			}
			img.mask = stilus.NewTexture(m)
		case d.stream(dict["Mask"]) != nil:
			s := d.stream(dict["Mask"])
			m, _, ok := dc.plane(s.Dict, s.Raw, useStencil)
			if !ok {
				return
			}
			img.mask = stilus.NewTexture(m)
		case key != nil:
			img.mask = stilus.NewTexture(*key)
		}
	}
	for _, t := range [2]*texture{img.color, img.mask} {
		if t != nil {
			img.size += t.Base().Bytes() + t.MipBytes()
		}
	}
	dc.out.img = img
}

// fail records why an image cannot be drawn; an empty feature means the
// image is broken.
func (dc *imageDecoder) fail(feature string) (plane, *plane, bool) {
	if dc.out.unsupported == "" {
		dc.out.unsupported = feature
	}
	return plane{}, nil, false
}

// plane decodes the samples of an image stream. For a colour image with a
// colour key whose samples are not palette indexes, key is the mask it
// makes.
func (dc *imageDecoder) plane(dict reader.Dict, raw []byte, use planeUse) (p plane, key *plane, ok bool) {
	d := dc.d
	w, _ := d.integer(dict["Width"])
	h, _ := d.integer(dict["Height"])
	if w <= 0 || h <= 0 || w > 1<<24 || h > 1<<24 {
		return dc.fail("")
	}
	dec := reader.DecodeRecovering(dict, raw, d.r.Resolver())
	if dec.Recovered {
		dc.out.recovered = true
	}
	data := dec.Data
	if len(data) == 0 {
		if !knownFilter(dec.Filter) {
			dc.out.recovered = false
			return dc.fail("image-filter")
		}
		return dc.fail("")
	}
	sp := sampleSpec{w: w, h: h, use: use}
	bpc, _ := d.integer(dict["BitsPerComponent"])
	sp.bpc = bpc
	if use == useStencil {
		sp.bpc = 1
	}
	switch dec.Image {
	case "":
	case "JBIG2Decode":
		pp, err := dc.jbig2(dict, data)
		if err != nil {
			return dc.fail("")
		}
		data, sp.w, sp.h, sp.bpc, sp.stride = pp.Data, pp.Width, pp.Height, 1, pp.Stride
		if use == useColor {
			// A JBIG2 image is one-bit grey whatever its dictionary says.
			sp.cs, sp.n = spaceGray, 1
		}
	case "DCTDecode", "DCT":
		if use == useStencil {
			return dc.fail("image-filter")
		}
		return dc.jpeg(dict, data, &sp)
	case "JPXDecode":
		if use == useStencil {
			return dc.fail("image-filter")
		}
		return dc.jpx(dict, data, &sp)
	default:
		return dc.fail("image-filter")
	}
	if use == useStencil {
		sp.n = 1
		sp.decode = dc.decodeArray(dict, 1)
		return dc.samples(data, &sp)
	}
	if sp.cs == nil {
		cs, ok := dc.colorSpace(dict, use)
		if !ok {
			return dc.fail("")
		}
		sp.cs, sp.n = cs, cs.n
		if use == useAlpha {
			sp.n = 1
		}
	}
	if sp.bpc != 1 && sp.bpc != 2 && sp.bpc != 4 && sp.bpc != 8 && sp.bpc != 16 {
		return dc.fail("")
	}
	sp.decode = dc.decodeArray(dict, sp.n)
	if use == useColor {
		sp.key = dc.colorKey(dict, sp.n)
	}
	return dc.samples(data, &sp)
}

// knownFilter reports whether the reader implements filter f (or f is
// empty): a filter it does not know leaves the data undecoded.
func knownFilter(f reader.Name) bool {
	switch f {
	case "", "FlateDecode", "Fl", "LZWDecode", "LZW", "ASCIIHexDecode", "AHx", "ASCII85Decode", "A85",
		"RunLengthDecode", "RL", "CCITTFaxDecode", "CCF", "Crypt":
		return true
	}
	return false
}

// colorSpace returns the colour space of an image's samples.
func (dc *imageDecoder) colorSpace(dict reader.Dict, use planeUse) (*colorSpace, bool) {
	if use == useAlpha {
		return spaceGray, true
	}
	cs, approx := dc.d.colorSpace(dict["ColorSpace"], dc.res, 0)
	if cs == nil || cs.kind == csPattern {
		return nil, false
	}
	if approx != "" {
		dc.out.approx = approx
	}
	return cs, true
}

// decodeArray returns /Decode if it has 2n numbers.
func (dc *imageDecoder) decodeArray(dict reader.Dict, n int) []float64 {
	a, ok := reader.ToArray(dc.d.resolve(dict["Decode"]))
	if !ok || len(a) < 2*n {
		return nil
	}
	out := make([]float64, 2*n)
	for i := range out {
		if out[i], ok = dc.d.num(a[i]); !ok {
			return nil
		}
	}
	return out
}

// colorKey returns a /Mask colour key: 2n sample ranges.
func (dc *imageDecoder) colorKey(dict reader.Dict, n int) []int {
	a, ok := reader.ToArray(dc.d.resolve(dict["Mask"]))
	if !ok || len(a) < 2*n {
		return nil
	}
	out := make([]int, 2*n)
	for i := range out {
		if out[i], ok = dc.d.integer(a[i]); !ok {
			return nil
		}
	}
	return out
}

// sampleSpec describes packed samples.
type sampleSpec struct {
	w, h   int
	n, bpc int
	stride int // bytes per row; 0 means packed rows
	cs     *colorSpace
	decode []float64 // 2n, or nil for the default
	key    []int     // colour key, 2n raw sample ranges, or nil
	use    planeUse
}

// samples turns packed samples into a plane: one of at most eight bits
// into a palette (one bit a pixel stays one bit a pixel), more into
// premultiplied pixels. Missing data reads as zero samples, or as
// transparent for a stencil.
func (dc *imageDecoder) samples(data []byte, sp *sampleSpec) (plane, *plane, bool) {
	w, h, n := sp.w, sp.h, sp.n
	if n <= 0 || n > maxComps {
		return dc.fail("")
	}
	// Bit offsets in a row and byte offsets in the data must fit an int
	// on 32-bit targets.
	if int64(w)*int64(n)*int64(sp.bpc) >= 1<<31 || int64(max(sp.stride, 1))*int64(h) >= 1<<31 {
		return dc.fail("image-too-large")
	}
	if sp.stride == 0 {
		sp.stride = int((int64(w)*int64(n)*int64(sp.bpc) + 7) / 8)
		if int64(sp.stride)*int64(h) >= 1<<31 {
			return dc.fail("image-too-large")
		}
	}
	if sp.bpc == 16 {
		data, sp.stride = narrow16(data, sp.stride, w*n, h), w*n
		sp.bpc = 8
		for i := range sp.key {
			sp.key[i] >>= 8
		}
	}
	if n == 1 {
		p, ok := dc.palettePlane(data, sp)
		if !ok {
			return dc.fail("image-too-large")
		}
		return p, nil, true
	}
	if int64(w)*int64(h)*4 > maxImageBytes {
		return dc.fail("image-too-large")
	}
	return dc.rgbaPlane(data, sp)
}

// narrow16 keeps the high byte of 16-bit samples.
func narrow16(data []byte, stride, n, h int) []byte {
	out := make([]byte, n*h)
	for y := range h {
		row := rowOf(data, y, stride)
		for i := range n {
			if 2*i < len(row) {
				out[y*n+i] = row[2*i]
			}
		}
	}
	return out
}

// rowOf returns row y of packed rows (shorter or empty past the data).
func rowOf(data []byte, y, stride int) []byte {
	o := y * stride
	if o >= len(data) {
		return nil
	}
	return data[o:min(o+stride, len(data))]
}

// decodeRange returns the default /Decode range of component c.
func decodeRange(cs *colorSpace, c, bpc int) (lo, hi float64) {
	switch {
	case cs == nil:
	case cs.kind == csIndexed:
		return 0, float64(int(1)<<bpc - 1)
	case cs.kind == csCIE && cs.cie.lab && c == 0:
		return 0, 100
	case cs.kind == csCIE && cs.cie.lab && c <= 2:
		return cs.cie.rng[2*c-2], cs.cie.rng[2*c-1]
	}
	return 0, 1
}

// value maps raw sample of component c through /Decode.
func (sp *sampleSpec) value(c int, raw uint32) float64 {
	maxv := float64(int(1)<<sp.bpc - 1)
	lo, hi := decodeRange(sp.cs, c, sp.bpc)
	if sp.decode != nil {
		lo, hi = sp.decode[2*c], sp.decode[2*c+1]
	}
	return lo + float64(raw)*(hi-lo)/maxv
}

// keyed reports whether raw sample of component c is inside the colour
// key.
func (sp *sampleSpec) keyed(c int, raw uint32) bool {
	return sp.key != nil && int(raw) >= sp.key[2*c] && int(raw) <= sp.key[2*c+1]
}

// palettePlane reads one-component samples as indexes into a palette of
// their 2^bpc colours.
func (dc *imageDecoder) palettePlane(data []byte, sp *sampleSpec) (plane, bool) {
	w, h, bpc := sp.w, sp.h, sp.bpc
	pal := new(palette)
	var v [1]float64
	for raw := range uint32(1) << bpc {
		x := sp.value(0, raw)
		switch sp.use {
		case useColor:
			v[0] = x
			r, g, b := sp.cs.rgb(v[:])
			pal[raw] = pack(unit8(r), unit8(g), unit8(b), 255)
			if sp.keyed(0, raw) {
				pal[raw] = 0
			}
		case useAlpha:
			a := unit8(x)
			pal[raw] = pack(a, a, a, a)
		case useStencil:
			// A sample that decodes to 0 paints.
			if x < 0.5 {
				pal[raw] = pack(255, 255, 255, 255)
			}
		}
	}
	if bpc == 1 {
		stride := (w + 7) / 8
		if int64(stride)*int64(h) > maxImageBytes {
			return plane{}, false
		}
		pix := make([]uint8, stride*h)
		if sp.use == useStencil && pal[1] == 0 {
			// Past the data nothing is painted.
			for i := range pix {
				pix[i] = 0xff
			}
		}
		for y := range h {
			copy(pix[y*stride:][:stride], rowOf(data, y, sp.stride))
		}
		return plane{Kind: stilus.PlaneBits, W: w, H: h, Stride: stride, Pix8: pix, Pal: pal}, true
	}
	if int64(w)*int64(h) > maxImageBytes {
		return plane{}, false
	}
	pix := make([]uint8, w*h)
	for y := range h {
		row := rowOf(data, y, sp.stride)
		dst := pix[y*w:][:w]
		if bpc == 8 {
			copy(dst, row)
			continue
		}
		for x := range dst {
			dst[x] = uint8(sampleAt(row, x*bpc, bpc))
		}
	}
	return plane{Kind: stilus.PlaneIndex, W: w, H: h, Stride: w, Pix8: pix, Pal: pal}, true
}

// sampleAt reads the bpc-bit sample at bit offset off of row (0 past its
// end).
func sampleAt(row []byte, off, bpc int) uint32 {
	i := off >> 3
	if i >= len(row) {
		return 0
	}
	if bpc == 8 {
		return uint32(row[i])
	}
	return uint32(row[i]) >> (8 - uint(bpc) - uint(off&7)) & (1<<bpc - 1)
}

// unit8 maps [0, 1] to a byte, rounded.
func unit8(v float64) uint8 { return uint8(clamp01(v)*255 + 0.5) }

// rgbaPlane converts samples of several components to opaque pixels.
func (dc *imageDecoder) rgbaPlane(data []byte, sp *sampleSpec) (plane, *plane, bool) {
	w, h, n, bpc := sp.w, sp.h, sp.n, sp.bpc
	p := plane{Kind: stilus.PlaneRGBA, W: w, H: h, Stride: w, Pix32: make([]uint32, w*h)}
	var keyMask *plane
	if sp.key != nil {
		stride := (w + 7) / 8
		keyMask = &plane{Kind: stilus.PlaneBits, W: w, H: h, Stride: stride, Pix8: make([]uint8, stride*h), Pal: stencilPal()}
	}
	// Decoded values per component and raw sample.
	lut := make([]float64, n<<bpc)
	for c := range n {
		for raw := range uint32(1) << bpc {
			lut[c<<bpc+int(raw)] = sp.value(c, raw)
		}
	}
	plain := sp.decode == nil && bpc == 8
	var v [maxComps]float64
	for y := range h {
		row := rowOf(data, y, sp.stride)
		dst := p.Pix32[y*w:][:w]
		switch {
		case plain && sp.cs == spaceRGB && len(row) == 3*w:
			for x := range dst {
				s := row[3*x:][:3]
				dst[x] = pack(s[0], s[1], s[2], 255)
			}
		case plain && sp.cs == spaceCMYK && len(row) == 4*w:
			for x := range dst {
				s := row[4*x:][:4]
				k := 255 - uint32(s[3])
				dst[x] = pack(inkOff(s[0], k), inkOff(s[1], k), inkOff(s[2], k), 255)
			}
		case plain && sp.cs.kind == csCIE && n == 3 && !sp.cs.cie.lab && len(row) == 3*w:
			for x := range dst {
				s := row[3*x:][:3]
				r, g, b := sp.cs.cie.rgb8(s[0], s[1], s[2])
				dst[x] = pack(r, g, b, 255)
			}
		default:
			// Conversions through functions or CIE formulas are slow:
			// the last colour is remembered, since neighbours repeat.
			var last [maxComps]uint32
			var lastC uint32
			have := false
			for x := range dst {
				same := have
				for c := range n {
					raw := sampleAt(row, (x*n+c)*bpc, bpc)
					same = same && raw == last[c]
					last[c] = raw
					v[c] = lut[c<<bpc+int(raw)]
				}
				if !same {
					r, g, b := sp.cs.rgb(v[:n])
					lastC, have = pack(unit8(r), unit8(g), unit8(b), 255), true
				}
				dst[x] = lastC
			}
		}
		if keyMask != nil {
			krow := keyMask.Pix8[y*keyMask.Stride:]
			for x := range w {
				in := true
				for c := 0; c < n && in; c++ {
					in = sp.keyed(c, sampleAt(row, (x*n+c)*bpc, bpc))
				}
				if in {
					krow[x>>3] |= 0x80 >> (uint(x) & 7)
				}
			}
		}
	}
	return p, keyMask, true
}

// inkOff returns (255-c)·k/255 rounded: DeviceCMYK to RGB without a
// profile, k being 255 minus black.
func inkOff(c uint8, k uint32) uint8 {
	x := (255-uint32(c))*k + 128
	return uint8((x + x>>8) >> 8)
}

// stencilPal is the palette of a one-bit mask whose 0 samples paint.
func stencilPal() *palette {
	p := new(palette)
	p[0] = pack(255, 255, 255, 255)
	return p
}

// jbig2 decodes a JBIG2 stream to packed rows of PDF samples: JBIG2 sets a
// bit for ink, a one-bit grey image and an image mask mean ink by 0.
func (dc *imageDecoder) jbig2(dict reader.Dict, data []byte) (gobig2.PackedPage, error) {
	dec, err := gobig2.NewDecoderEmbedded(bytes.NewReader(data), dc.jbig2Globals(dict))
	if err != nil {
		return gobig2.PackedPage{}, err
	}
	pp, err := dec.DecodePacked()
	if err != nil {
		return pp, err
	}
	out := make([]byte, len(pp.Data)) // pp.Data belongs to the decoder
	for i, b := range pp.Data {
		out[i] = ^b
	}
	pp.Data = out
	return pp, nil
}

// jbig2Globals returns the shared segments named by /DecodeParms, which
// runs parallel to /Filter.
func (dc *imageDecoder) jbig2Globals(dict reader.Dict) []byte {
	d := dc.d
	parms := d.resolve(dict["DecodeParms"])
	var list []reader.Object
	if a, ok := reader.ToArray(parms); ok {
		list = a
	} else {
		list = []reader.Object{parms}
	}
	for _, o := range list {
		if s := d.stream(d.dict(o)["JBIG2Globals"]); s != nil {
			if dec := d.r.DecodeStreamRecovering(s); !dec.Recovered {
				return dec.Data
			}
		}
	}
	return nil
}

// jpeg decodes a DCTDecode image. Its samples go through the colour space
// and /Decode like any others, except that a three-component picture in
// DeviceRGB is converted straight to pixels.
func (dc *imageDecoder) jpeg(dict reader.Dict, data []byte, sp *sampleSpec) (plane, *plane, bool) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return dc.fail("")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxCodecPixels {
		return dc.fail("image-too-large")
	}
	img, rows, err := jpeg.DecodePartial(bytes.NewReader(data))
	if img == nil {
		return dc.fail("")
	}
	if err != nil {
		dc.out.recovered = true
		clearRowsFrom(img, rows)
	}
	sp.bpc = 8
	cs, csOK := dc.colorSpace(dict, sp.use)
	sp.decode = nil
	b := img.Bounds()
	sp.w, sp.h = b.Dx(), b.Dy()
	var pix []byte
	switch im := img.(type) {
	case *image.Gray:
		pix, sp.stride, sp.n = im.Pix, im.Stride, 1
	case *image.YCbCr:
		if sp.use == useAlpha {
			pix, sp.stride, sp.n = im.Y, im.YStride, 1
			break
		}
		if !csOK || cs.n != 3 || cs == spaceRGB {
			if dc.decodeArray(dict, 3) == nil && dc.colorKey(dict, 3) == nil {
				return rgbaFrom(im), nil, true
			}
		}
		pix, sp.stride, sp.n = rgbBytes(im), 3*sp.w, 3
	case *image.CMYK:
		// Go's decoder turns the inverted ink of Adobe CMYK JPEGs back;
		// PDF files mean the stored samples, which /Decode may invert.
		for i := range im.Pix {
			im.Pix[i] = 255 - im.Pix[i]
		}
		pix, sp.stride, sp.n = im.Pix, im.Stride, 4
	default:
		pix, sp.stride, sp.n = rgbBytes(img), 3*sp.w, 3
	}
	if sp.use == useAlpha && sp.n != 1 {
		return dc.fail("")
	}
	sp.cs = deviceSpace(sp.n)
	if csOK && cs.n == sp.n {
		sp.cs = cs
	}
	if sp.use == useColor {
		sp.decode = dc.decodeArray(dict, sp.n)
		sp.key = dc.colorKey(dict, sp.n)
	}
	return dc.samples(pix, sp)
}

// jpx decodes a JPXDecode image. Its /Decode is ignored and its colour
// space comes from the codestream unless the dictionary names one of as
// many components.
func (dc *imageDecoder) jpx(dict reader.Dict, data []byte, sp *sampleSpec) (plane, *plane, bool) {
	cfg, err := jpeg2000.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return dc.fail("")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxCodecPixels {
		return dc.fail("image-too-large")
	}
	img, err := jpeg2000.Decode(bytes.NewReader(data))
	if err != nil || img == nil {
		return dc.fail("")
	}
	sp.bpc = 8
	sp.decode = nil
	b := img.Bounds()
	sp.w, sp.h = b.Dx(), b.Dy()
	var pix []byte
	switch im := img.(type) {
	case *image.Gray:
		pix, sp.stride, sp.n = im.Pix[im.PixOffset(b.Min.X, b.Min.Y):], im.Stride, 1
	case *image.CMYK:
		pix, sp.stride, sp.n = im.Pix[im.PixOffset(b.Min.X, b.Min.Y):], im.Stride, 4
	default:
		if sp.use == useAlpha {
			return dc.fail("")
		}
		cs, ok := dc.colorSpace(dict, sp.use)
		if !ok || cs.n != 3 || cs == spaceRGB {
			// Pixels as decoded, with the alpha a codestream may carry
			// (SMaskInData).
			return rgbaFrom(img), nil, true
		}
		pix, sp.stride, sp.n = rgbBytes(img), 3*sp.w, 3
	}
	if sp.use == useAlpha && sp.n != 1 {
		return dc.fail("")
	}
	sp.cs = deviceSpace(sp.n)
	if cs, ok := dc.colorSpace(dict, sp.use); ok && cs.n == sp.n {
		sp.cs = cs
	}
	if sp.use == useColor {
		sp.key = dc.colorKey(dict, sp.n)
	}
	return dc.samples(pix, sp)
}

// deviceSpace returns the device colour space of n components.
func deviceSpace(n int) *colorSpace {
	switch n {
	case 3:
		return spaceRGB
	case 4:
		return spaceCMYK
	}
	return spaceGray
}

// rgbaFrom converts a decoded picture to premultiplied pixels.
func rgbaFrom(img image.Image) plane {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pix := make([]uint32, w*h)
	var bs []byte
	if len(pix) > 0 {
		bs = unsafe.Slice((*byte)(unsafe.Pointer(&pix[0])), 4*len(pix))
	}
	dst := &image.RGBA{Pix: bs, Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
	draw.Draw(dst, dst.Rect, img, b.Min, draw.Src)
	return plane{Kind: stilus.PlaneRGBA, W: w, H: h, Stride: w, Pix32: pix}
}

// rgbBytes returns the pixels of img as packed 8-bit RGB samples.
func rgbBytes(img image.Image) []byte {
	p := rgbaFrom(img)
	out := make([]byte, 3*len(p.Pix32))
	for i, c := range p.Pix32 {
		out[3*i], out[3*i+1], out[3*i+2], _ = unpack(c)
	}
	return out
}

// clearRowsFrom blanks the rows of a partially decoded JPEG that did not
// arrive (they hold whatever the decoder allocated).
func clearRowsFrom(img image.Image, rows int) {
	b := img.Bounds()
	for y := b.Min.Y + rows; y < b.Max.Y; y++ {
		switch im := img.(type) {
		case *image.Gray:
			clear(im.Pix[im.PixOffset(b.Min.X, y):][:b.Dx()])
		case *image.CMYK:
			clear(im.Pix[im.PixOffset(b.Min.X, y):][:4*b.Dx()])
		}
	}
}

func (d *Document) boolean(o reader.Object) bool {
	b, _ := reader.ToBool(d.resolve(o))
	return b
}

func (d *Document) integer(o reader.Object) (int, bool) {
	f, ok := d.num(o)
	if !ok || math.Abs(f) > 1<<30 {
		return 0, false
	}
	return int(f), true
}

func (d *Document) stream(o reader.Object) *reader.Stream {
	s, _ := reader.ToStream(d.resolve(o))
	return s
}
