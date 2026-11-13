package cera

import (
	"context"
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/corpus"
	"github.com/timzifer/cera/internal/testpdf"
	"github.com/timzifer/stilus"
)

// imageSurface is a Surface over an image, composited by stilus's own
// blitters: RenderSurface onto it draws what Render draws.
type imageSurface struct {
	img    *image.RGBA
	root   *imageSurface // nil for the root
	layers int           // made by NewLayer and not released
	made   int           // made by NewLayer
	reads  int
	writes int
	bands  int // the most compositors Bands made
	ls     stilus.LayerShader
}

func newImageSurface(r image.Rectangle) *imageSurface {
	return &imageSurface{img: image.NewRGBA(r)}
}

func (s *imageSurface) top() *imageSurface {
	if s.root != nil {
		return s.root
	}
	return s
}

func (s *imageSurface) Blitter(p *stilus.Paint) stilus.Blitter {
	if p.Shader == nil {
		return stilus.NewSolidBlitter(s.img, p.Color)
	}
	sh := p.Shader
	if ls, ok := sh.(*stilus.LayerShader); ok && ls.Dst == nil {
		// The backdrop of a layer composited onto a surface is the
		// surface.
		c := *ls
		c.Dst = s.img
		sh = &c
	}
	return stilus.NewShaderBlitter(s.img, sh)
}

func (s *imageSurface) Bounds() image.Rectangle { return s.img.Rect }

func (s *imageSurface) Fill(r image.Rectangle, c color.RGBA) error {
	fillRegion(s.img, r, c)
	return nil
}

func (s *imageSurface) Read(dst *image.RGBA) error {
	s.top().reads++
	copyRect(dst, s.img, dst.Rect)
	return nil
}

func (s *imageSurface) Write(src *image.RGBA) error {
	s.top().writes++
	copyRect(s.img, src, src.Rect)
	return nil
}

func (s *imageSurface) Flush() error { return nil }

// Bands implements BandSurface: compositors drawing into the image, one
// band each.
func (s *imageSurface) Bands(n int) ([]stilus.Compositor, error) {
	s.bands = max(s.bands, n)
	c := make([]stilus.Compositor, n)
	for i := range c {
		c[i] = imageBand{s.img}
	}
	return c, nil
}

type imageBand struct{ img *image.RGBA }

func (b imageBand) Blitter(p *stilus.Paint) stilus.Blitter {
	if p.Shader == nil {
		return stilus.NewSolidBlitter(b.img, p.Color)
	}
	return stilus.NewShaderBlitter(b.img, p.Shader)
}

func (s *imageSurface) NewLayer() (Surface, error) {
	t := s.top()
	t.layers++
	t.made++
	return &imageSurface{img: image.NewRGBA(s.img.Rect), root: t}, nil
}

func (s *imageSurface) Release(l Surface) { s.top().layers-- }

func (s *imageSurface) Composite(c *Composite) stilus.Shader {
	s.ls = stilus.LayerShader{Src: c.Layer.(*imageSurface).img, Dst: s.img, Alpha: c.Alpha, Blend: c.Blend,
		Subtractive: c.Subtractive, Black: c.Black}
	return &s.ls
}

// surfacePages are pages with every kind of group the device draws onto
// a surface or, with its backdrop read and written, into images, and the
// synthetic scenes.
func surfacePages() map[string][]byte {
	pages := testpdf.Groups()
	for _, s := range corpus.Scenes() {
		var b strings.Builder
		s.Content(&b)
		pages[s.Name] = corpus.PDF(1190.55, 841.89, b.String())
	}
	return pages
}

func TestRenderSurfaceMatchesRender(t *testing.T) {
	for name, data := range surfacePages() {
		t.Run(name, func(t *testing.T) {
			doc, err := Open(data)
			if err != nil {
				t.Fatal(err)
			}
			p, _ := doc.Page(0)
			scale := 3.0
			if p.Box.X1 > 1000 {
				scale = 0.5 // a scene
			}
			for _, bg := range []color.RGBA{white, {}} {
				full := p.Bounds(scale)
				want := image.NewRGBA(full)
				opt := RenderOptions{Scale: scale, Background: bg, Workers: 1}
				if err := p.Render(context.Background(), want, opt); err != nil {
					t.Fatal(err)
				}
				s := newImageSurface(full)
				var st Stats
				opt.Stats = &st
				if err := p.RenderSurface(context.Background(), s, opt); err != nil {
					t.Fatal(err)
				}
				if d := maxDiff(t, want, s.img, full); d > 1 {
					t.Errorf("background %v: RenderSurface differs from Render by %d", bg, d)
				}
				if s.layers != 0 {
					t.Errorf("%d layers not released", s.layers)
				}
				if st.Groups > 0 {
					t.Logf("background %v: %d groups, %d layers, %d reads, %d writes", bg, st.Groups, s.made, s.reads, s.writes)
				}
			}
		})
	}
}

// Isolated groups are drawn into layers of the surface; a non-isolated
// group reads its backdrop from the surface and writes it back.
func TestRenderSurfaceLayers(t *testing.T) {
	pages := surfacePages()
	for _, c := range []struct {
		name          string
		layers        int // at least; the page that blends is one
		reads, writes int
	}{
		{"isolated-nested", 3, 0, 0},
		{"non-isolated", 1, 1, 1},
	} {
		doc, _ := Open(pages[c.name])
		p, _ := doc.Page(0)
		s := newImageSurface(p.Bounds(2))
		if err := p.RenderSurface(context.Background(), s, RenderOptions{Scale: 2, Background: white}); err != nil {
			t.Fatal(err)
		}
		if s.made < c.layers || s.reads != c.reads || s.writes != c.writes {
			t.Errorf("%s: %d layers, %d reads, %d writes", c.name, s.made, s.reads, s.writes)
		}
	}
}

// A page without groups is drawn onto a BandSurface in bands, by several
// workers at once; one with groups in one pass.
func TestRenderSurfaceBands(t *testing.T) {
	pages := surfacePages()
	for _, c := range []struct {
		name  string
		bands bool
	}{{"contours-3000", true}, {"joins-caps", true}, {"short-20000-0.35mm", true}, {"mixed", false}, {"paints", false}} {
		doc, _ := Open(pages[c.name])
		p, _ := doc.Page(0)
		scale := 3.0
		if p.Box.X1 > 1000 {
			scale = 1
		}
		full := p.Bounds(scale)
		region := image.Rect(full.Dx()/5, full.Dy()/4, full.Dx()*3/4, full.Dy()*4/5)
		for _, r := range []image.Rectangle{{}, region} {
			opt := RenderOptions{Scale: scale, Background: white, Region: r, Workers: 4}
			want := image.NewRGBA(full)
			if err := p.Render(context.Background(), want, opt); err != nil {
				t.Fatal(err)
			}
			s := newImageSurface(full)
			if err := p.RenderSurface(context.Background(), s, opt); err != nil {
				t.Fatal(err)
			}
			if (s.bands > 1) != c.bands {
				t.Errorf("%s: %d bands", c.name, s.bands)
			}
			if d := maxDiff(t, want, s.img, full); d > 2 {
				t.Errorf("%s, region %v: RenderSurface in bands differs from Render by %d", c.name, r, d)
			}
		}
	}
}
