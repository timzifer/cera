package cera

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"slices"
	"sync"
	"testing"
)

// bandLog records the parts RenderOptions.Band reports, each with a copy
// of its pixels as they were when it was reported.
type bandLog struct {
	mu    sync.Mutex
	dst   *image.RGBA
	parts []image.Rectangle
	pix   []*image.RGBA
}

func (b *bandLog) band(r image.Rectangle) {
	c := image.NewRGBA(r)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(c.Pix[c.PixOffset(r.Min.X, y):][:4*r.Dx()], b.dst.Pix[b.dst.PixOffset(r.Min.X, y):])
	}
	b.mu.Lock()
	b.parts = append(b.parts, r)
	b.pix = append(b.pix, c)
	b.mu.Unlock()
}

// check holds the parts to the contract of Band: strips across the
// region, not overlapping, covering it, final when reported.
func (b *bandLog) check(t *testing.T, name string, region image.Rectangle) {
	t.Helper()
	parts := slices.Clone(b.parts)
	slices.SortFunc(parts, func(p, q image.Rectangle) int { return p.Min.Y - q.Min.Y })
	y := region.Min.Y
	for _, r := range parts {
		if r.Min.X != region.Min.X || r.Max.X != region.Max.X || r.Empty() {
			t.Fatalf("%s: part %v does not span region %v", name, r, region)
		}
		if r.Min.Y != y {
			t.Fatalf("%s: parts %v leave a gap or overlap at row %d (region %v)", name, parts, y, region)
		}
		y = r.Max.Y
	}
	if y != region.Max.Y {
		t.Fatalf("%s: parts %v end at row %d, region %v", name, parts, y, region)
	}
	for i, r := range b.parts {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			got := b.pix[i].Pix[b.pix[i].PixOffset(r.Min.X, y):][:4*r.Dx()]
			want := b.dst.Pix[b.dst.PixOffset(r.Min.X, y):][:4*r.Dx()]
			if !slices.Equal(got, want) {
				t.Fatalf("%s: row %d of part %v changed after it was reported", name, y, r)
			}
		}
	}
}

func TestRenderBand(t *testing.T) {
	adaptWorkers = true
	defer func() { adaptWorkers = false }()
	white := color.RGBA{255, 255, 255, 255}
	const scale = 8 // 1600×800 px
	for _, tc := range []struct {
		name    string
		content string
		workers int
	}{
		{"cheap", "0 g 10 10 20 20 re f", 1},
		{"page-wide, one worker", pageFills(300), 1},
		{"page-wide, four workers", pageFills(300), 4},
		{"stripes, one worker", stripes(2000), 1},
		{"translucent", stripes(500) + "/H gs 1 0 0 rg 20 10 160 80 re f\n", 4},
	} {
		doc, err := Open(buildPDF([]string{tc.content}, "/Resources << /ExtGState << /H << /ca 0.5 >> >> >>"))
		if err != nil {
			t.Fatal(err)
		}
		p, _ := doc.Page(0)
		page := p.Bounds(scale)
		full := &errAfter{Context: context.Background(), n: -1}
		if err := p.Render(full, image.NewRGBA(page), RenderOptions{Scale: scale, Workers: tc.workers}); err != nil {
			t.Fatal(err)
		}
		calls := full.calls.Load()
		for _, d := range []struct {
			name      string
			dst, view image.Rectangle
			n         int64
		}{
			{"page", page, image.Rectangle{}, -1},
			{"beside the page", page.Inset(-10), image.Rectangle{}, -1},
			{"viewport", page, image.Rect(100, 150, 900, 520), -1},
			{"cancelled at once", page, image.Rectangle{}, 0},
			{"cancelled midway", page, image.Rectangle{}, calls / 2},
		} {
			name := fmt.Sprintf("%s, %s", tc.name, d.name)
			log := &bandLog{dst: image.NewRGBA(d.dst)}
			opt := RenderOptions{Scale: scale, Workers: tc.workers, Background: white, Region: d.view, Band: log.band}
			_ = p.Render(&errAfter{Context: context.Background(), n: d.n}, log.dst, opt)
			region := d.dst
			if !d.view.Empty() {
				region = d.view
			}
			log.check(t, name, region)
		}
	}
}

// With form values drawn over the page, the region is reported once,
// after them.
func TestRenderBandForm(t *testing.T) {
	_, p, f := openForm(t, sampleForm(""))
	s := f.NewState()
	if err := s.SetValue(f.Field("name"), TextValue("Band")); err != nil {
		t.Fatal(err)
	}
	log := &bandLog{dst: image.NewRGBA(p.Bounds(4))}
	if err := p.Render(context.Background(), log.dst, RenderOptions{Scale: 4, Background: color.RGBA{255, 255, 255, 255}, Form: s, Workers: 4, Band: log.band}); err != nil {
		t.Fatal(err)
	}
	if len(log.parts) != 1 {
		t.Errorf("%d parts, want the region once", len(log.parts))
	}
	log.check(t, "form", log.dst.Rect)
}
