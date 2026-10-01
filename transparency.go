package cera

import (
	"image"
	"image/color"

	"github.com/timzifer/stilus"
)

// A raster device draws a group or a soft mask into a layer of its own: an
// RGBA image over the part of the device the group can touch, within the
// clips around it. The canvas is retargeted at the layer with an empty clip
// stack, since the clips around a group apply once, when it is composited;
// when the canvas returns to the layer below, the clips open there are
// replayed.
//
// An isolated group starts transparent. EndGroup fills the layer's
// rectangle below with a shader that reads the layer and the backdrop: so
// the group is composited through the clip stack, antialiased at its
// edges, like any fill (stilus.LayerShader, which keeps blend modes exact
// under partial coverage).
//
// A non-isolated group starts as a copy of its backdrop, so that blend
// modes inside it see what is below, and is composited by interpolating
// from the backdrop to the layer by opacity, soft mask and clip coverage;
// this is exact for the Normal blend mode (PDF 2.0, 11.4.8). The display
// list marks non-isolated groups without blend modes inside as isolated,
// which composites the same.
//
// A knockout group has two more images: a scratch the canvas draws every
// object into, prepared with the group's initial backdrop, and a shape the
// object is drawn into a second time, opaque. The object's result S then
// replaces the layer K in proportion to its shape f:
// K' = S + (1−f)·(K − B0), B0 being the initial backdrop. A group inside a
// knockout group is one object, drawn isolated, whose shape is taken from
// its alpha over the greatest opacity drawn into it.
//
// Layer buffers are kept by the device and reused.

// maxFreeLayerBytes bounds the layer buffers a device keeps for reuse.
const maxFreeLayerBytes = 64 << 20

// layers is the transparency state of a raster device.
type layers struct {
	base   *image.RGBA // nil: no Reset, groups are drawn directly
	region image.Rectangle
	stack  []layer
	done   layer // the layer being composited, popped
	// clips are the clips open in every layer, to replay them when the
	// canvas returns to a layer; paths are copied into verbs and points.
	clips  []clipRec
	verbs  []stilus.Verb
	points []stilus.Point
	// pending is the mask made by the last EndMask, for the next group.
	pending softPlane
	// knockout says that the canvas draws into a knockout layer's scratch.
	knockout bool
	cover    image.RGBA // clip coverage of a non-isolated group

	free      [][]byte
	freeBytes int
	err       error // of the canvas before a retarget

	comp  stilus.LayerShader
	alpha image.Alpha // the soft mask of comp
	rect  Path
	paint Paint
}

type clipRec struct {
	rect           bool
	r              Rect
	m              Matrix
	rule           FillRule
	v0, v1, p0, p1 int32
}

type layer struct {
	img  image.RGBA
	area image.Rectangle
	g    Group
	iso  bool      // drawn isolated
	mask softPlane // Masked: the soft mask, nil pixels if there is none
	// ko and shape are the scratch and shape of a knockout group.
	ko, shape image.RGBA
	// ink is the greatest opacity drawn into the layer.
	ink uint8
	// isMask marks a soft mask being drawn, with sm its parameters.
	isMask bool
	sm     SoftMask
	pass   bool // drawn without a layer (no Reset)
	clips  int  // clips open when the layer began
}

func (l *layer) knockout() bool { return l.g.Knockout && !l.isMask }

// softPlane is a soft mask: one byte per pixel of area.
type softPlane struct {
	pix  []byte
	area image.Rectangle
}

var opaque = Paint{Color: color.RGBA{255, 255, 255, 255}}

func (t *layers) reset(dst *image.RGBA, region image.Rectangle) {
	for i := range t.stack {
		t.drop(&t.stack[i])
	}
	clear(t.stack)
	t.stack = t.stack[:0]
	t.clips = t.clips[:0]
	t.verbs, t.points = t.verbs[:0], t.points[:0]
	t.release(t.pending.pix)
	t.pending = softPlane{}
	t.knockout = false
	t.err = nil
	t.base, t.region = dst, region
}

func (t *layers) drop(l *layer) {
	t.release(l.img.Pix)
	t.release(l.ko.Pix)
	t.release(l.shape.Pix)
	t.release(l.mask.pix)
	*l = layer{}
}

// buf returns a zeroed buffer of n bytes.
func (t *layers) buf(n int) []byte {
	best := -1
	for i, b := range t.free {
		if cap(b) >= n && (best < 0 || cap(b) < cap(t.free[best])) {
			best = i
		}
	}
	if best < 0 {
		return make([]byte, n)
	}
	b := t.free[best][:n]
	last := len(t.free) - 1
	t.free[best] = t.free[last]
	t.free[last] = nil
	t.free = t.free[:last]
	t.freeBytes -= cap(b)
	clear(b)
	return b
}

func (t *layers) release(b []byte) {
	if cap(b) == 0 || t.freeBytes+cap(b) > maxFreeLayerBytes {
		return
	}
	t.free = append(t.free, b[:0])
	t.freeBytes += cap(b)
}

// image returns a transparent image over area.
func (t *layers) image(area image.Rectangle) image.RGBA {
	if area.Empty() {
		return image.RGBA{Rect: area}
	}
	return image.RGBA{Pix: t.buf(4 * area.Dx() * area.Dy()), Stride: 4 * area.Dx(), Rect: area}
}

func (t *layers) pushClip(p *Path, r Rect, m Matrix, rule FillRule, rect bool) {
	if t.base == nil {
		return
	}
	c := clipRec{rect: rect, r: r, m: m, rule: rule}
	c.v0, c.p0 = int32(len(t.verbs)), int32(len(t.points))
	if p != nil {
		t.verbs = append(t.verbs, p.Verbs...)
		t.points = append(t.points, p.Points...)
	}
	c.v1, c.p1 = int32(len(t.verbs)), int32(len(t.points))
	t.clips = append(t.clips, c)
}

func (t *layers) popClip() {
	n := len(t.clips)
	if t.base == nil || n == 0 || (len(t.stack) > 0 && n <= t.stack[len(t.stack)-1].clips) {
		return
	}
	t.truncClips(n - 1)
}

func (t *layers) truncClips(n int) {
	if n < len(t.clips) {
		c := &t.clips[n]
		t.verbs, t.points = t.verbs[:c.v0], t.points[:c.p0]
		t.clips = t.clips[:n]
	}
}

// target returns what the canvas draws into for layer i (-1: the base).
func (t *layers) target(i int) *image.RGBA {
	if i < 0 {
		return t.base
	}
	l := &t.stack[i]
	if l.knockout() {
		return &l.ko
	}
	return &l.img
}

// clipsOf returns the index of the first clip opened in layer i.
func (t *layers) clipsOf(i int) int {
	if i < 0 {
		return 0
	}
	return t.stack[i].clips
}

// inked notes that something of opacity a was drawn into the top layer.
func (t *layers) inked(a uint8) {
	if n := len(t.stack); n > 0 {
		t.stack[n-1].ink = max(t.stack[n-1].ink, a)
	}
}

// retarget points the canvas at layer i (-1: the base) and replays the
// clips open in it.
func (d *RasterDevice) retarget(i int) {
	d.retargetOn(i, d.t.target(i))
	d.t.knockout = i >= 0 && d.t.stack[i].knockout()
}

// retargetOn points the canvas at dst, over the region of layer i, with
// the clips open in layer i.
func (d *RasterDevice) retargetOn(i int, dst *image.RGBA) {
	t := &d.t
	if err := d.C.Err(); err != nil && t.err == nil {
		t.err = err
	}
	region := t.region
	if i >= 0 {
		region = t.stack[i].area
	}
	d.C.Reset(dst, region)
	for k := t.clipsOf(i); k < len(t.clips); k++ {
		c := &t.clips[k]
		if c.rect {
			d.C.ClipRect(stilus.Rect{X0: c.r.X0, Y0: c.r.Y0, X1: c.r.X1, Y1: c.r.Y1}, c.m)
		} else {
			p := Path{Verbs: t.verbs[c.v0:c.v1:c.v1], Points: t.points[c.p0:c.p1:c.p1]}
			d.C.ClipPath(&p, c.m, c.rule)
		}
	}
}

// area returns the device pixels r under m can touch within the clip.
func (d *RasterDevice) area(r Rect, m Matrix) image.Rectangle {
	corners := [2]stilus.Point{{X: float32(r.X0), Y: float32(r.Y0)}, {X: float32(r.X1), Y: float32(r.Y1)}}
	return deviceBoxPoints(corners[:], m, 1).Intersect(d.C.Clip())
}

func (t *layers) push() *layer {
	t.stack = append(t.stack, layer{})
	return &t.stack[len(t.stack)-1]
}

// pop moves the top layer to t.done.
func (t *layers) pop() *layer {
	n := len(t.stack) - 1
	t.done = t.stack[n]
	t.stack[n] = layer{}
	t.stack = t.stack[:n]
	t.truncClips(t.done.clips) // clips left open inside
	return &t.done
}

func (d *RasterDevice) BeginGroup(r Rect, m Matrix, g *Group) {
	t := &d.t
	area := d.area(r, m)
	parent := len(t.stack) - 1
	inKnockout := t.knockout
	if inKnockout {
		// The group is one object of the knockout group below.
		d.koPrepare(area, false)
	}
	l := t.push()
	l.g, l.area, l.clips = *g, area, len(t.clips)
	l.iso = g.Isolated || inKnockout
	if t.base == nil {
		l.pass = true
		return
	}
	l.img = t.image(area)
	if !l.iso {
		copyRect(&l.img, t.target(parent), area)
	}
	if g.Knockout {
		l.ko, l.shape = t.image(area), t.image(area)
	}
	if g.Masked {
		l.mask, t.pending = t.pending, softPlane{}
	}
	d.retarget(len(t.stack) - 1)
}

func (d *RasterDevice) EndGroup() {
	t := &d.t
	n := len(t.stack)
	if n == 0 || t.stack[n-1].isMask {
		return // unbalanced
	}
	l := t.pop()
	if l.pass {
		*l = layer{}
		return
	}
	parent := n - 2
	d.retarget(parent)
	if !l.area.Empty() && l.g.Alpha != 0 && l.ink != 0 && (!l.g.Masked || l.mask.pix != nil) {
		dst := t.target(parent)
		if l.iso {
			d.composite(l, dst)
		} else {
			d.interpolate(l, dst, parent)
		}
		if t.knockout {
			d.koMerge(l.area, &l.img, l.ink)
		}
		t.inked(mulByte(l.ink, uint32(l.g.Alpha)))
	}
	t.drop(l)
}

func (d *RasterDevice) BeginMask(r Rect, m Matrix, sm *SoftMask) {
	t := &d.t
	area := d.area(r, m)
	l := t.push()
	l.isMask, l.sm, l.area, l.clips, l.iso = true, *sm, area, len(t.clips), true
	if t.base == nil {
		// Mask content must not reach the canvas.
		l.pass = true
		d.C.ClipRect(stilus.Rect{}, stilus.Identity)
		return
	}
	l.img = t.image(area)
	if sm.Luminosity && !area.Empty() {
		b := sm.Backdrop
		fillRegion(&l.img, area, color.RGBA{b.R, b.G, b.B, 255})
	}
	d.retarget(len(t.stack) - 1)
}

func (d *RasterDevice) EndMask() {
	t := &d.t
	n := len(t.stack)
	if n == 0 || !t.stack[n-1].isMask {
		return
	}
	l := t.pop()
	if l.pass {
		*l = layer{}
		d.C.PopClip()
		return
	}
	d.retarget(n - 2)
	t.release(t.pending.pix)
	t.pending = softPlane{area: l.area}
	if !l.area.Empty() {
		w, h := l.area.Dx(), l.area.Dy()
		pix := t.buf(w * h)
		src, tr := l.img.Pix, l.sm.Transfer
		for i := range pix {
			p := src[4*i : 4*i+4 : 4*i+4]
			var v uint8
			if l.sm.Luminosity {
				// 0.30, 0.59 and 0.11 in 1/256 (the layer is opaque).
				v = uint8((77*uint32(p[0]) + 151*uint32(p[1]) + 28*uint32(p[2]) + 128) >> 8)
			} else {
				v = p[3]
			}
			if tr != nil {
				v = tr[v]
			}
			pix[i] = v
		}
		t.pending.pix = pix
	}
	t.drop(l)
}

// copyRect copies the pixels of r from src to dst.
func copyRect(dst, src *image.RGBA, r image.Rectangle) {
	r = r.Intersect(src.Rect).Intersect(dst.Rect)
	if r.Empty() {
		return
	}
	n := 4 * r.Dx()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(dst.Pix[dst.PixOffset(r.Min.X, y):][:n], src.Pix[src.PixOffset(r.Min.X, y):][:n])
	}
}

// composite draws isolated layer l onto dst, the canvas's target, through
// the canvas's clips.
func (d *RasterDevice) composite(l *layer, dst *image.RGBA) {
	t := &d.t
	c := &t.comp
	*c = stilus.LayerShader{Src: &l.img, Dst: dst, Alpha: l.g.Alpha, Blend: l.g.Blend}
	if l.g.Masked {
		t.alpha = *l.mask.alpha()
		c.Mask = &t.alpha
	}
	d.fillArea(l.area, c)
	*c = stilus.LayerShader{}
	t.alpha = image.Alpha{}
}

// fillArea fills the pixels of a with shader s through the canvas.
func (d *RasterDevice) fillArea(a image.Rectangle, s stilus.Shader) {
	t := &d.t
	t.paint.Shader = s
	d.C.Fill(t.rectOf(a), stilus.Identity, NonZero, &t.paint)
	t.paint.Shader = nil
}

// interpolate composites non-isolated layer l onto dst, the target of
// layer parent: dst + k·(l − dst), k being the group's opacity times its
// soft mask and the coverage of the clips open in parent.
func (d *RasterDevice) interpolate(l *layer, dst *image.RGBA, parent int) {
	t := &d.t
	a := l.area
	var cover *image.RGBA
	if !t.clipsCover(parent, a) {
		// Clip coverage: the area filled through the clips.
		t.cover = t.image(a)
		cover = &t.cover
		d.retargetOn(parent, cover)
		d.C.Fill(t.rectOf(a), stilus.Identity, NonZero, &opaque)
		d.retarget(parent)
	}
	n := 4 * a.Dx()
	for y := a.Min.Y; y < a.Max.Y; y++ {
		s := l.img.Pix[l.img.PixOffset(a.Min.X, y):][:n:n]
		b := dst.Pix[dst.PixOffset(a.Min.X, y):][:n:n]
		var cv []byte
		if cover != nil {
			cv = cover.Pix[cover.PixOffset(a.Min.X, y):][:n:n]
		}
		for i := 0; i < n; i += 4 {
			k := uint32(l.g.Alpha)
			if l.g.Masked {
				k = div255(k * uint32(l.mask.at(a.Min.X+i/4, y)))
			}
			if cv != nil {
				k = div255(k * uint32(cv[i+3]))
			}
			if k == 0 {
				continue
			}
			for c := i; c < i+4; c++ {
				b[c] = uint8(int32(b[c]) + mulSigned(int32(s[c])-int32(b[c]), k))
			}
		}
	}
	if cover != nil {
		t.release(t.cover.Pix)
		t.cover = image.RGBA{}
	}
}

// clipsCover reports whether the clips open in layer i cover all of a:
// they are rectangles containing it.
func (t *layers) clipsCover(i int, a image.Rectangle) bool {
	for k := t.clipsOf(i); k < len(t.clips); k++ {
		c := &t.clips[k]
		if !c.rect || c.m[1] != 0 || c.m[2] != 0 {
			return false
		}
		x0, y0 := c.m.Apply(c.r.X0, c.r.Y0)
		x1, y1 := c.m.Apply(c.r.X1, c.r.Y1)
		if !(min(x0, x1) <= float64(a.Min.X) && max(x0, x1) >= float64(a.Max.X) &&
			min(y0, y1) <= float64(a.Min.Y) && max(y0, y1) >= float64(a.Max.Y)) {
			return false
		}
	}
	return true
}

// rectOf returns the path of a.
func (t *layers) rectOf(a image.Rectangle) *Path {
	t.rect.Reset()
	t.rect.Rect(float32(a.Min.X), float32(a.Min.Y), float32(a.Dx()), float32(a.Dy()))
	return &t.rect
}

// mulSigned returns v·k/255 rounded, for |v| ≤ 255.
func mulSigned(v int32, k uint32) int32 {
	if v < 0 {
		return -int32(div255(uint32(-v) * k))
	}
	return int32(div255(uint32(v) * k))
}

// koPrepare starts an object of the knockout group on top within box:
// its scratch gets the group's initial backdrop, and, if shape is set,
// its shape is cleared. It returns the box within the group.
func (d *RasterDevice) koPrepare(box image.Rectangle, shape bool) image.Rectangle {
	t := &d.t
	top := len(t.stack) - 1
	l := &t.stack[top]
	box = box.Intersect(l.area)
	if box.Empty() {
		return box
	}
	n := 4 * box.Dx()
	for y := box.Min.Y; y < box.Max.Y; y++ {
		row := l.ko.Pix[l.ko.PixOffset(box.Min.X, y):][:n:n]
		if l.iso {
			clear(row)
		} else {
			b := t.target(top - 1)
			copy(row, b.Pix[b.PixOffset(box.Min.X, y):][:n])
		}
		if shape {
			clear(l.shape.Pix[l.shape.PixOffset(box.Min.X, y):][:n:n])
		}
	}
	return box
}

// koShape points the canvas at the shape of the knockout group on top.
func (d *RasterDevice) koShape() {
	top := len(d.t.stack) - 1
	d.retargetOn(top, &d.t.stack[top].shape)
}

// koMerge ends an object of the knockout group on top: within box, what
// it drew into the scratch replaces the group's layer in proportion to its
// shape, the alpha of src over ink.
func (d *RasterDevice) koMerge(box image.Rectangle, src *image.RGBA, ink uint8) {
	t := &d.t
	top := len(t.stack) - 1
	l := &t.stack[top]
	box = box.Intersect(l.area)
	if box.Empty() || ink == 0 {
		return
	}
	var b0 *image.RGBA
	if !l.iso {
		b0 = t.target(top - 1)
	}
	n := 4 * box.Dx()
	in := uint32(ink)
	for y := box.Min.Y; y < box.Max.Y; y++ {
		s := l.ko.Pix[l.ko.PixOffset(box.Min.X, y):][:n:n]
		k := l.img.Pix[l.img.PixOffset(box.Min.X, y):][:n:n]
		f := src.Pix[src.PixOffset(box.Min.X, y):][:n:n]
		var b []byte
		if b0 != nil {
			b = b0.Pix[b0.PixOffset(box.Min.X, y):][:n:n]
		}
		for i := 0; i < n; i += 4 {
			fa := uint32(f[i+3])
			if fa == 0 {
				continue
			}
			inv := 255 - min((fa*255+in-1)/in, 255)
			for c := i; c < i+4; c++ {
				v := int32(k[c])
				if b != nil {
					v -= int32(b[c])
				}
				k[c] = uint8(min(max(int32(s[c])+mulSigned(v, inv), 0), 255))
			}
		}
	}
}

// An object drawn into a knockout group is drawn between koBegin and
// koEnd: into the scratch with its paint, then, after koShape, opaque into
// the shape.
func (d *RasterDevice) koBegin(box image.Rectangle) image.Rectangle {
	return d.koPrepare(box, true)
}

// koEnd merges an object drawn into the scratch and the shape and points
// the canvas back at the scratch.
func (d *RasterDevice) koEnd(box image.Rectangle) {
	top := len(d.t.stack) - 1
	d.koMerge(box, &d.t.stack[top].shape, 255)
	d.retarget(top)
}

// div255 divides by 255, rounded, for v ≤ 255·255.
func div255(v uint32) uint32 {
	v += 128
	return (v + v>>8) >> 8
}

func mulByte(v uint8, k uint32) uint8 { return uint8(div255(uint32(v) * k)) }

func (m *softPlane) at(x, y int) uint8 {
	if !image.Pt(x, y).In(m.area) {
		return 0
	}
	return m.pix[(y-m.area.Min.Y)*m.area.Dx()+x-m.area.Min.X]
}

// alpha returns the plane as an image for stilus.LayerShader.
func (m *softPlane) alpha() *image.Alpha {
	return &image.Alpha{Pix: m.pix, Stride: m.area.Dx(), Rect: m.area}
}
