package cera

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"strings"
	"sync"
	"testing"
	"time"
)

// drawingPDF is a page with most of what the interpreter draws: fills,
// dashed and wide strokes, rectangle and path clips, a form XObject.
func drawingPDF() []byte {
	var c strings.Builder
	c.WriteString("0.2 0.4 0.6 rg 5 5 m 190 20 l 100 95 l f\n")
	c.WriteString("q 20 20 m 180 30 l 100 90 l h W n 1 0 0 rg 0 0 200 100 re f Q\n")
	c.WriteString("q [4 2] 0 d 3 w 0 0 1 RG 0 50 m 200 55 l S Q\n")
	c.WriteString("q 150 0 50 100 re W n 0 G 0.1 w\n")
	for i := range 60 {
		fmt.Fprintf(&c, "%d 0 m %d 100 l S\n", 150+i, 100+i)
	}
	c.WriteString("Q q 0.5 0 0 0.5 10 10 cm /F1 Do Q\n")
	c.WriteString("q 0 0 0 0 re W n 0 g 0 0 200 100 re f Q\n") // empty clip
	form := "<< /Type /XObject /Subtype /Form /BBox [0 0 40 40] /Length 29 >>\nstream\n0 1 0 rg 0 0 60 60 re f 0 g\nendstream"
	return buildPDF([]string{c.String()}, "/Resources << /XObject << /F1 100 0 R >> >>", form)
}

func maxDiff(t *testing.T, a, b *image.RGBA, r image.Rectangle) int {
	t.Helper()
	d := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			d = max(d, diff(a.RGBAAt(x, y), b.RGBAAt(x, y)))
		}
	}
	return d
}

func TestWorkersAgree(t *testing.T) {
	doc, err := Open(drawingPDF())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := doc.Page(0)
	const scale = 7 // 1400×700 px: several bands
	render := func(workers int) *image.RGBA {
		dst := image.NewRGBA(p.Bounds(scale))
		if err := p.Render(context.Background(), dst, RenderOptions{Scale: scale, Background: white, Workers: workers}); err != nil {
			t.Fatal(err)
		}
		return dst
	}
	one, two, four := render(1), render(2), render(4)
	if n := p.dl.numBands(); n < 4 {
		t.Fatalf("%d bands", n)
	}
	if d := maxDiff(t, two, four, two.Rect); d != 0 {
		t.Errorf("2 and 4 workers differ by %d", d)
	}
	if d := maxDiff(t, one, four, one.Rect); d > 2 {
		t.Errorf("1 and 4 workers differ by %d", d)
	}
	// Something was drawn in every part tested.
	assertPixel(t, one, 20*scale, 20*scale, white)        // inside the empty clip only
	if c := one.RGBAAt(100*scale, 60*scale); c == white { // clipped red
		t.Errorf("clipped fill missing: %v", c)
	}
}

func TestTilesFromTheCache(t *testing.T) {
	doc, _ := Open(drawingPDF())
	p, _ := doc.Page(0)
	const scale = 3
	full := image.NewRGBA(p.Bounds(scale))
	var st Stats
	opt := RenderOptions{Scale: scale, Background: white, Workers: 1, Stats: &st}
	if err := p.Render(context.Background(), full, opt); err != nil {
		t.Fatal(err)
	}
	if st.Reused || st.Fills == 0 {
		t.Fatalf("first render: %+v", st)
	}
	first := st
	for _, tile := range []image.Rectangle{
		image.Rect(0, 0, 256, 256), image.Rect(256, 0, 600, 300), image.Rect(100, 100, 356, 356),
	} {
		for _, workers := range []int{1, 3} {
			dst := image.NewRGBA(tile)
			opt.Workers = workers
			if err := p.Render(context.Background(), dst, opt); err != nil {
				t.Fatal(err)
			}
			if !st.Reused || st.Fills != first.Fills || st.Strokes != first.Strokes || st.Ops != first.Ops {
				t.Errorf("tile %v: stats %+v, first %+v", tile, st, first)
			}
			if d := maxDiff(t, full, dst, tile.Intersect(full.Rect)); d > 2 {
				t.Errorf("tile %v with %d workers differs by %d", tile, workers, d)
			}
		}
	}
	// Another scale records again.
	if err := p.Render(context.Background(), image.NewRGBA(p.Bounds(2)), RenderOptions{Scale: 2, Stats: &st}); err != nil || st.Reused {
		t.Errorf("new scale: err %v, reused %v", err, st.Reused)
	}
	p.Release()
	if p.dl != nil {
		t.Error("Release kept the list")
	}
	if err := p.Render(context.Background(), full, RenderOptions{Scale: scale, Stats: &st}); err != nil || st.Reused {
		t.Errorf("after Release: err %v, reused %v", err, st.Reused)
	}
}

func TestEmptyClipDropsContent(t *testing.T) {
	c := "q 0 0 0 0 re W n q 0 0 10 10 re W n 0 g 0 0 200 100 re f Q 0 0 200 100 re f Q 1 0 0 rg 0 0 10 10 re f"
	doc, _ := Open(buildPDF([]string{c}, ""))
	p, _ := doc.Page(0)
	dst := image.NewRGBA(p.Bounds(1))
	if err := p.Render(context.Background(), dst, RenderOptions{Background: white}); err != nil {
		t.Fatal(err)
	}
	// The page clip, the red fill, the page clip's end; nothing of the
	// empty clip.
	if n := len(p.dl.items); n != 3 {
		t.Errorf("%d items", n)
	}
	assertPixel(t, dst, 100, 50, white)
	assertPixel(t, dst, 5, 95, rgba(255, 0, 0, 255))
}

func TestOffPageContentIsCulled(t *testing.T) {
	c := "0 g 300 300 10 10 re f -50 -50 m -40 -40 l S 10 10 10 10 re f"
	doc, _ := Open(buildPDF([]string{c}, ""))
	p, _ := doc.Page(0)
	if err := p.Render(context.Background(), image.NewRGBA(p.Bounds(1)), RenderOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := len(p.dl.items); n != 3 {
		t.Errorf("%d items, want the page clip, one fill and the clip's end", n)
	}
}

func TestDeadlineIsNotCached(t *testing.T) {
	c := strings.Repeat("0 0 1 1 re f\n", 2000)
	doc, _ := Open(buildPDF([]string{c}, ""))
	p, _ := doc.Page(0)
	dst := image.NewRGBA(p.Bounds(1))
	err := p.Render(context.Background(), dst, RenderOptions{Deadline: time.Now().Add(-time.Second)})
	if !errors.Is(err, ErrDeadline) {
		t.Fatalf("err = %v", err)
	}
	if p.dl != nil {
		t.Error("partial list cached")
	}
	var st Stats
	if err := p.Render(context.Background(), dst, RenderOptions{Stats: &st}); err != nil || st.Fills != 2000 {
		t.Errorf("err %v, fills %d", err, st.Fills)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Render(ctx, dst, RenderOptions{Workers: 4}); !errors.Is(err, ErrDeadline) {
		t.Errorf("cancelled replay: err = %v", err)
	}
}

func TestConcurrentRenders(t *testing.T) {
	doc, _ := Open(drawingPDF())
	p, _ := doc.Page(0)
	// Recording reads the document, which is not safe for concurrent use:
	// record first, then draw tiles and scales concurrently.
	if err := p.Render(context.Background(), image.NewRGBA(p.Bounds(2)), RenderOptions{Scale: 2}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			dst := image.NewRGBA(image.Rect(i*40, 0, i*40+64, 200))
			var st Stats
			if err := p.Render(context.Background(), dst, RenderOptions{Scale: 2, Workers: 2, Stats: &st}); err != nil || !st.Reused {
				t.Errorf("tile %d: err %v, reused %v", i, err, st.Reused)
			}
		})
	}
	wg.Wait()
	// Replacing the cached list while it is drawn keeps the old one alive.
	var mu sync.Mutex
	for i := range 4 {
		wg.Go(func() {
			scale := 1 + float64(i%2)
			mu.Lock() // one recording at a time (the document)
			defer mu.Unlock()
			if err := p.Render(context.Background(), image.NewRGBA(p.Bounds(scale)), RenderOptions{Scale: scale}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	p.Release()
}

func TestSteadyStateAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("sync.Pool drops items under the race detector")
	}
	doc, _ := Open(drawingPDF())
	p, _ := doc.Page(0)
	dst := image.NewRGBA(p.Bounds(2))
	var st Stats
	opt := RenderOptions{Scale: 2, Workers: 1, Stats: &st}
	if err := p.Render(context.Background(), dst, opt); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(20, func() {
		if err := p.Render(context.Background(), dst, opt); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 1 {
		t.Errorf("%v allocations per cached render", allocs)
	}
}

func TestSigmaMax(t *testing.T) {
	for _, c := range []struct {
		m    Matrix
		want float64
	}{
		{Matrix{2, 0, 0, 3, 5, 5}, 3},
		{Matrix{0, -4, 1, 0, 0, 0}, 4},
		{Matrix{1, 1, -1, 1, 0, 0}, 1.4142135623730951},
		{Matrix{}, 0},
	} {
		if got := sigmaMax(c.m); got < c.want-1e-9 || got > c.want+1e-9 {
			t.Errorf("sigmaMax(%v) = %v, want %v", c.m, got, c.want)
		}
	}
}

func rgba(r, g, b, a uint8) color.RGBA { return color.RGBA{r, g, b, a} }
