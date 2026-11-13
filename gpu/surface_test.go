package gpu

import (
	"context"
	"image"
	"image/color"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/gogpu/wgpu"
	_ "github.com/gogpu/wgpu/hal/allbackends"
	"github.com/timzifer/cera"
	"github.com/timzifer/cera/internal/corpus"
	"github.com/timzifer/cera/internal/testpdf"
	sgpu "github.com/timzifer/stilus/gpu"
)

var (
	devOnce sync.Once
	dev     *sgpu.Device
	devErr  error
)

// device opens one device for the tests and skips the test when there is
// none, as stilus's gpu tests do: CERA_GPU_FALLBACK=1 requests wgpu's
// software adapter, CERA_GPU_BACKEND=vulkan or gl a backend, and
// CERA_GPU_REQUIRE=1 fails instead of skipping.
func device(t testing.TB) *sgpu.Device {
	devOnce.Do(func() {
		opts := &sgpu.Options{Fallback: os.Getenv("CERA_GPU_FALLBACK") == "1"}
		switch os.Getenv("CERA_GPU_BACKEND") {
		case "vulkan":
			opts.Backends = wgpu.BackendsVulkan
		case "gl":
			opts.Backends = wgpu.BackendsGL
		}
		dev, devErr = sgpu.Open(opts)
		if devErr == nil {
			t.Logf("adapter: %s", dev.AdapterName())
		}
	})
	if devErr != nil {
		if os.Getenv("CERA_GPU_REQUIRE") == "1" {
			t.Fatalf("no GPU device: %v", devErr)
		}
		t.Skipf("no GPU device: %v", devErr)
	}
	return dev
}

var white = color.RGBA{255, 255, 255, 255}

// pages are the pages of every kind of group, and the synthetic scenes
// unless short.
func pages(short bool) map[string][]byte {
	p := testpdf.Groups()
	if !short {
		for _, s := range corpus.Scenes() {
			var b strings.Builder
			s.Content(&b)
			p[s.Name] = corpus.PDF(1190.55, 841.89, b.String())
		}
	}
	return p
}

// maxDiff returns the largest difference of a channel between a and b.
func maxDiff(a, b *image.RGBA) int {
	d := 0
	for i := range a.Pix {
		d = max(d, int(a.Pix[i])-int(b.Pix[i]), int(b.Pix[i])-int(a.Pix[i]))
	}
	return d
}

// The GPU draws what the CPU draws, within 2/255 (stilus's GPU path
// computes some blend modes in float32), with every background and onto
// a region.
func TestRenderSurfaceMatchesRender(t *testing.T) {
	d := device(t)
	// wgpu's software adapter interprets the shaders: small pages only.
	soft := os.Getenv("CERA_GPU_FALLBACK") == "1"
	all := pages(testing.Short() || soft)
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			doc, err := cera.Open(all[name])
			if err != nil {
				t.Fatal(err)
			}
			p, _ := doc.Page(0)
			scale := 3.0
			if soft {
				scale = 1
			}
			if p.Box.X1 > 1000 {
				scale = 0.5 // a scene
			}
			full := p.Bounds(scale)
			tg, err := d.NewTarget(full.Dx(), full.Dy())
			if err != nil {
				t.Fatal(err)
			}
			defer tg.Close()
			s := NewSurface(d, tg)
			defer s.Close()
			region := image.Rect(full.Dx()/5, full.Dy()/4, full.Dx()*3/4, full.Dy()*4/5)
			for _, c := range []struct {
				bg     color.RGBA
				region image.Rectangle
			}{{white, image.Rectangle{}}, {color.RGBA{}, image.Rectangle{}}, {white, region}} {
				opt := cera.RenderOptions{Scale: scale, Background: c.bg, Region: c.region, Workers: 1}
				want := image.NewRGBA(full)
				if err := p.Render(context.Background(), want, opt); err != nil {
					t.Fatal(err)
				}
				// What the region leaves is the surface's: red.
				if err := s.Fill(full, color.RGBA{255, 0, 0, 255}); err != nil {
					t.Fatal(err)
				}
				if !c.region.Empty() {
					for y := full.Min.Y; y < full.Max.Y; y++ {
						for x := full.Min.X; x < full.Max.X; x++ {
							if !image.Pt(x, y).In(c.region) {
								want.SetRGBA(x, y, color.RGBA{255, 0, 0, 255})
							}
						}
					}
				}
				if err := p.RenderSurface(context.Background(), s, opt); err != nil {
					t.Fatal(err)
				}
				if err := s.Flush(); err != nil {
					t.Fatal(err)
				}
				got := image.NewRGBA(full)
				if err := s.Read(got); err != nil {
					t.Fatal(err)
				}
				if diff := maxDiff(want, got); diff > 2 {
					t.Errorf("background %v, region %v: the GPU differs from the CPU by %d", c.bg, c.region, diff)
				}
			}
		})
	}
}
