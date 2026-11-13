// Package gpu draws cera pages on the GPU: a Surface over a texture of
// github.com/timzifer/stilus/gpu, which Page.RenderSurface draws onto
// (ADR 0011). Rasterization stays on the CPU, as for Page.Render; the GPU
// composites the spans, with gradients, images, meshes, glyph masks and
// transparency groups computed there.
//
//	import _ "github.com/gogpu/wgpu/hal/allbackends"
//
//	dev, _ := sgpu.Open(nil)
//	t, _ := dev.NewTarget(page.Bounds(scale).Dx(), page.Bounds(scale).Dy())
//	s := gpu.NewSurface(dev, t)
//	defer s.Close()
//	err := page.RenderSurface(ctx, s, cera.RenderOptions{Scale: scale, Background: white})
//	err = s.Flush() // or draw t with a sgpu.Presenter, which flushes it
//
// The surface's pixels are those of the page at its origin: a target as
// large as the page, or the part of it from (0, 0) that fits.
package gpu

import (
	"errors"
	"fmt"
	"image"
	"image/color"

	"github.com/gogpu/wgpu"
	"github.com/timzifer/cera"
	"github.com/timzifer/stilus"
	sgpu "github.com/timzifer/stilus/gpu"
)

// Surface is a cera.Surface over a stilus GPU target. Layers of groups are
// targets of the same size, kept by the surface for the next render.
//
// A Surface is not safe for concurrent use.
type Surface struct {
	dev  *sgpu.Device
	t    *sgpu.Target
	root *Surface // the surface whose layer this is; nil for the root
	// onto is the surface a layer was composited onto since NewLayer,
	// which reads it when it is flushed.
	onto *Surface

	// Of the root:
	free  []*Surface   // released layers
	made  []*Surface   // every layer, to close
	layer sgpu.Layer   // Composite's shader
	fill  image.RGBA   // Fill's pixels
	err   error        // the first error of a flush in Release
	stage []byte       // Write's rows, aligned
	buf   *wgpu.Buffer // Write's buffer
	recs  []*sgpu.Recorder
	comps []stilus.Compositor
}

var _ cera.BandSurface = (*Surface)(nil)

// NewSurface returns a surface over t, a target of dev. The surface does
// not own t.
func NewSurface(dev *sgpu.Device, t *sgpu.Target) *Surface {
	return &Surface{dev: dev, t: t}
}

// Target returns the target the surface draws onto.
func (s *Surface) Target() *sgpu.Target { return s.t }

// Blitter implements stilus.Compositor.
func (s *Surface) Blitter(p *stilus.Paint) stilus.Blitter { return s.t.Blitter(p) }

// Bounds implements cera.Surface.
func (s *Surface) Bounds() image.Rectangle { return s.t.Bounds() }

// Fill implements cera.Surface. Filling the whole target clears it.
func (s *Surface) Fill(r image.Rectangle, c color.RGBA) error {
	if r == s.t.Bounds() {
		s.t.Clear(c)
		return nil
	}
	if r.Empty() {
		return nil
	}
	f := &s.top().fill
	n := 4 * r.Dx() * r.Dy()
	if cap(f.Pix) < n {
		f.Pix = make([]uint8, n)
	}
	*f = image.RGBA{Pix: f.Pix[:n], Stride: 4 * r.Dx(), Rect: r}
	for i := 0; i < n; i += 4 {
		f.Pix[i], f.Pix[i+1], f.Pix[i+2], f.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return s.Write(f)
}

// Read implements cera.Surface.
func (s *Surface) Read(dst *image.RGBA) error { return s.t.ReadRGBA(dst) }

// Write implements cera.Surface: the target is flushed and src written
// into its texture.
func (s *Surface) Write(src *image.RGBA) error {
	r := src.Rect
	if !r.In(s.t.Bounds()) {
		return errors.New("cera/gpu: Write outside the target")
	}
	if r.Empty() {
		return nil
	}
	if err := s.t.Flush(); err != nil {
		return err
	}
	// Through a buffer and a copy of our own: Queue.WriteTexture right
	// after the pass that clears the target was lost on Metal.
	root := s.top()
	bpr := (4*r.Dx() + 255) &^ 255
	size := bpr * r.Dy()
	if cap(root.stage) < size {
		root.stage = make([]byte, size)
	}
	stage := root.stage[:size]
	for y := range r.Dy() {
		copy(stage[y*bpr:][:4*r.Dx()], src.Pix[src.PixOffset(r.Min.X, r.Min.Y+y):][:4*r.Dx()])
	}
	if root.buf == nil || root.buf.Size() < uint64(size) {
		if root.buf != nil {
			root.buf.Release()
		}
		var err error
		root.buf, err = s.dev.WGPU().CreateBuffer(&wgpu.BufferDescriptor{
			Label: "cera-write", Size: uint64(size), Usage: wgpu.BufferUsageCopySrc | wgpu.BufferUsageCopyDst,
		})
		if err != nil {
			root.buf = nil
			return fmt.Errorf("cera/gpu: write buffer: %w", err)
		}
	}
	q := s.dev.WGPU().Queue()
	if err := q.WriteBuffer(root.buf, 0, stage); err != nil {
		return fmt.Errorf("cera/gpu: write: %w", err)
	}
	enc, err := s.dev.WGPU().CreateCommandEncoder(&wgpu.CommandEncoderDescriptor{Label: "cera-write"})
	if err != nil {
		return fmt.Errorf("cera/gpu: encoder: %w", err)
	}
	enc.CopyBufferToTexture(root.buf, s.t.Texture(), []wgpu.BufferTextureCopy{{
		BufferLayout: wgpu.ImageDataLayout{BytesPerRow: uint32(bpr), RowsPerImage: uint32(r.Dy())},
		TextureBase: wgpu.ImageCopyTexture{
			Texture: s.t.Texture(),
			Origin:  wgpu.Origin3D{X: uint32(r.Min.X), Y: uint32(r.Min.Y)},
		},
		Size: wgpu.Extent3D{Width: uint32(r.Dx()), Height: uint32(r.Dy()), DepthOrArrayLayers: 1},
	}})
	cmd, err := enc.Finish()
	if err != nil {
		return fmt.Errorf("cera/gpu: finish: %w", err)
	}
	if _, err := q.Submit(cmd); err != nil {
		return fmt.Errorf("cera/gpu: submit: %w", err)
	}
	return nil
}

func (s *Surface) top() *Surface {
	if s.root != nil {
		return s.root
	}
	return s
}

// NewLayer implements cera.Surface: a transparent target of the size of
// the surface's.
func (s *Surface) NewLayer() (cera.Surface, error) {
	root := s.top()
	if n := len(root.free); n > 0 {
		l := root.free[n-1]
		root.free = root.free[:n-1]
		l.t.Clear(color.RGBA{})
		return l, nil
	}
	b := root.t.Bounds()
	t, err := root.dev.NewTarget(b.Dx(), b.Dy())
	if err != nil {
		return nil, err
	}
	l := &Surface{dev: root.dev, t: t, root: root}
	root.made = append(root.made, l)
	return l, nil
}

// Release implements cera.Surface. The layer is kept for the next
// NewLayer. A layer composited onto a target is read when that target is
// flushed; the target is flushed now, before the layer is drawn again.
func (s *Surface) Release(layer cera.Surface) {
	root := s.top()
	l, ok := layer.(*Surface)
	if !ok || l.root != root {
		return
	}
	if l.onto != nil {
		if err := l.onto.t.Flush(); err != nil && root.err == nil {
			root.err = err
		}
		l.onto = nil
	}
	root.free = append(root.free, l)
}

// Composite implements cera.Surface with a sgpu.Layer.
func (s *Surface) Composite(c *cera.Composite) stilus.Shader {
	l := &s.top().layer
	*l = sgpu.Layer{Alpha: c.Alpha, Blend: c.Blend, Subtractive: c.Subtractive, Black: c.Black}
	if src, ok := c.Layer.(*Surface); ok {
		l.Src = src.t
		src.onto = s
	}
	return l
}

// Bands implements cera.BandSurface with recorders of the target, kept
// for the next render. A layer has no bands.
func (s *Surface) Bands(n int) ([]stilus.Compositor, error) {
	if s.root != nil {
		return nil, errors.New("cera/gpu: a layer has no bands")
	}
	for len(s.recs) < n {
		r, err := s.t.NewRecorder()
		if err != nil {
			return nil, err
		}
		s.recs = append(s.recs, r)
		s.comps = append(s.comps, r)
	}
	return s.comps[:n], nil
}

// Flush implements cera.Surface: it draws what was recorded onto the
// target and reports the first layer that could not be drawn. Flush the
// surface RenderSurface drew onto before reading or presenting its target.
func (s *Surface) Flush() error {
	if err := s.t.Flush(); err != nil {
		return err
	}
	if s.err != nil {
		return s.err
	}
	if err := s.t.Err(); err != nil {
		return err
	}
	for _, l := range s.made {
		if err := l.t.Err(); err != nil {
			return err
		}
	}
	for _, r := range s.recs {
		if err := r.Err(); err != nil {
			return err
		}
	}
	return nil
}

// Close releases the targets of the surface's layers and its recorders;
// the surface's own target is the caller's.
func (s *Surface) Close() {
	for _, l := range s.made {
		l.t.Close()
	}
	for _, r := range s.recs {
		r.Close()
	}
	s.made, s.free, s.err, s.recs, s.comps = nil, nil, nil, nil, nil
	if s.buf != nil {
		s.buf.Release()
		s.buf = nil
	}
}
