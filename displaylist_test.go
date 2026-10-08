package cera

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timzifer/stilus"
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
	// Two colours, so that the fills stay separate items.
	c := strings.Repeat("0 g 0 0 1 1 re f 0.5 g 0 0 1 1 re f\n", 1000)
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

// TestShortPageDeadline checks that a page too short for the periodic
// checks still stops at an ended context or a passed deadline, recorded
// or replayed, empty or not, with one worker or several.
func TestShortPageDeadline(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	ends := []struct {
		name string
		ctx  context.Context
		opt  RenderOptions
	}{
		{"cancelled", cancelled, RenderOptions{}},
		{"deadline", context.Background(), RenderOptions{Deadline: time.Now().Add(-time.Second)}},
	}
	for _, content := range []string{"0 g 10 10 20 20 re f", ""} {
		for _, end := range ends {
			for _, workers := range []int{1, 4} {
				for _, cached := range []bool{false, true} {
					name := fmt.Sprintf("%q/%s/workers=%d/cached=%v", content, end.name, workers, cached)
					doc, _ := Open(buildPDF([]string{content}, ""))
					p, _ := doc.Page(0)
					dst := image.NewRGBA(p.Bounds(1))
					if cached {
						if err := p.Render(context.Background(), dst, RenderOptions{}); err != nil {
							t.Fatal(err)
						}
					}
					opt := end.opt
					opt.Workers = workers
					opt.Background = color.RGBA{255, 255, 255, 255}
					if err := p.Render(end.ctx, dst, opt); !errors.Is(err, ErrDeadline) {
						t.Errorf("%s: err = %v, want ErrDeadline", name, err)
					}
					if c := dst.RGBAAt(20, p.Bounds(1).Dy()-20); c.R != 255 {
						t.Errorf("%s: shape drawn: %v", name, c)
					}
					if !cached && p.dl != nil {
						t.Errorf("%s: list cached", name)
					}
				}
			}
		}
	}
}

func TestShortPageRunCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	doc, _ := Open(buildPDF([]string{"BT /F1 12 Tf 10 10 Td (Hi) Tj ET"}, "/Resources << /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >> >>"))
	p, _ := doc.Page(0)
	if _, err := p.Text(context.Background()); err != nil {
		t.Fatal(err)
	}
	var td textDevice
	if err := p.RunWith(ctx, &td, RunOptions{}); !errors.Is(err, ErrDeadline) {
		t.Errorf("RunWith: err = %v, want ErrDeadline", err)
	}
	if len(td.chars) != 0 {
		t.Errorf("RunWith: %d chars after cancellation", len(td.chars))
	}
	if txt, err := p.Text(ctx); !errors.Is(err, ErrDeadline) || len(txt.Chars) != 0 {
		t.Errorf("Text: err = %v, %d chars", err, len(txt.Chars))
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

// TestUnionOfOnePen draws strokes and fills of one colour that meet inside
// a pixel row: half of row 14 is covered by each, so the row must be
// covered fully, not 3/4 as two separate composites leave it (#21). What
// prevents joining keeps them apart.
func TestUnionOfOnePen(t *testing.T) {
	// Device y = 100 - y: the strokes cover rows 10..14.5 and 14.5..19.
	strokes := "0 G 4.5 w 20 87.75 m 180 87.75 l S 20 83.25 m 180 83.25 l S"
	// The rectangles cover rows 10..14.5 and 14.5..19, wound both ways.
	fills := "0 g 20 85.5 160 4.5 re f 180 81 -160 4.5 re f"
	for _, c := range []struct {
		name, content string
		union         bool
	}{
		{"strokes", strokes, true},
		// At 45°, 3√2 wide, 3 apart in x and y: they meet on y = x - 3,
		// which halves the pixel at (50.5, 47.5), device row 52, col 50.
		{"diagonal strokes", "0 G 4.2426 w 20 20 m 80 80 l S 23 17 m 83 77 l S", true},
		{"diagonal, two colours", "0 G 4.2426 w 20 20 m 80 80 l S 0.02 G 23 17 m 83 77 l S", false},
		{"dashed strokes", "[30 2] 0 d " + strokes, true},
		{"fills", fills, true},
		{"stroke and fill", "0 G 0 g 4.5 w 20 87.75 m 180 87.75 l S 20 81 160 4.5 re f", true},
		{"two widths", "0 G 4.5 w 20 87.75 m 180 87.75 l S 2 w 20 84.5 m 180 84.5 l S", true},
		{"two matrices", "0 G 4.5 w 20 87.75 m 180 87.75 l S q 1 0 0 1 0 -4.5 cm 20 87.75 m 180 87.75 l S Q", true},
		{"fills one way", "0 g 20 85.5 160 4.5 re f 20 81 160 4.5 re f", true},
		{"curved fills", "0 g 20 85.5 m 180 85.5 l 180 90 l 100 90 20 90 20 90 c h f 180 81 -160 4.5 re f", true},
		{"two colours", "0 G 4.5 w 20 87.75 m 180 87.75 l S 0.02 G 20 83.25 m 180 83.25 l S", false},
		{"translucent", "/A gs 0 G 4.5 w 20 87.75 m 180 87.75 l S 20 83.25 m 180 83.25 l S", false},
		{"knockout", "/K Do", false},
		{"even-odd", "0 g 20 85.5 160 4.5 re f* 20 81 160 4.5 re f*", false},
		{"clip between", "0 G 4.5 w 20 87.75 m 180 87.75 l S 0 0 200 100 re W n 20 83.25 m 180 83.25 l S", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ko := "<< /Type /XObject /Subtype /Form /BBox [0 0 200 100] /Group << /S /Transparency /K true >> /Length " +
				fmt.Sprint(len(strokes)) + " >>\nstream\n" + strokes + "\nendstream"
			data := buildPDF([]string{c.content}, "/Resources << /ExtGState << /A << /CA 0.98 >> >> /XObject << /K 100 0 R >> >>", ko)
			img, _, err := renderPage(t, data, 0, RenderOptions{Background: white})
			if err != nil {
				t.Fatal(err)
			}
			at, in := image.Pt(100, 14), image.Pt(100, 12) // where they meet, inside one
			if strings.HasPrefix(c.name, "diagonal") {
				at, in = image.Pt(50, 52), image.Pt(50, 49)
			}
			// Drawn separately, the pixel is 1/4 white: about 64.
			if got := img.RGBAAt(at.X, at.Y).R; c.union != (got <= 6) || !c.union && got < 40 {
				t.Errorf("at %v: %d, union %v", at, got, c.union)
			}
			if got := img.RGBAAt(in.X, in.Y).R; got > 6 {
				t.Errorf("at %v: %d, want about 0", in, got)
			}
		})
	}
}

func TestWindsOneWay(t *testing.T) {
	path := func(pts ...float32) *Path {
		p := new(Path)
		p.MoveTo(pts[0], pts[1])
		for i := 2; i < len(pts); i += 2 {
			p.LineTo(pts[i], pts[i+1])
		}
		p.Close()
		return p
	}
	two := func(a, b *Path) *Path {
		p := &Path{Verbs: append(slices.Clone(a.Verbs), b.Verbs...), Points: append(slices.Clone(a.Points), b.Points...)}
		return p
	}
	left, right := path(0, 0, 10, 0, 10, 10, 0, 10), path(20, 0, 20, 10, 30, 10, 30, 0)
	for _, c := range []struct {
		name string
		p    *Path
		want bool
	}{
		{"left", left, true},
		{"right", right, true},
		{"closed again", path(0, 0, 10, 0, 10, 10, 0, 10, 0, 0), true},
		{"repeated point", path(0, 0, 10, 0, 10, 0, 10, 10, 0, 10), true},
		{"star", path(0, 10, 6, -8, -9, 3, 9, 3, -6, -8), true},
		{"figure eight", path(0, 0, 10, 10, 10, 0, 0, 10), false},
		{"concave", path(0, 0, 10, 0, 5, 2, 10, 10, 0, 10), false},
		{"two one way", two(left, path(20, 0, 30, 0, 30, 10, 20, 10)), true},
		{"two both ways", two(left, right), false},
		{"a line", path(0, 0, 10, 0), false},
	} {
		if got := windsOneWay(c.p); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// TestViewportOneWorker draws viewports of one band and of several with
// one worker, which then replays the bands' items rather than the whole
// list: groups, masks, clips, optional content and annotations come out
// as in the whole page and as several workers draw them.
func TestViewportOneWorker(t *testing.T) {
	annots := buildPDFCatalog("/OCProperties << /OCGs [102 0 R] /D << /OFF [102 0 R] >> >>", []string{"0 0 1 rg 0 0 200 100 re f"},
		"/Annots [100 0 R 101 0 R]",
		"<< /Subtype /Stamp /Rect [0 0 20 20] /CA 0.5 /AP << /N 103 0 R >> >>",
		"<< /Subtype /Stamp /Rect [40 0 60 20] /OC 102 0 R /AP << /N 103 0 R >> >>",
		"<< /Type /OCG /Name (Stamps) >>",
		apSquare)
	for _, tc := range []struct {
		name string
		data []byte
		page int
	}{
		{"drawing", drawingPDF(), 0},
		{"transparency", transparencyPDF(), 0},
		{"layers", layersPDF(), 0},
		{"nested layers", layersPDF(), 1},
		{"annotations", annots, 0},
	} {
		doc, err := Open(tc.data)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := doc.Page(tc.page)
		const scale = 7 // 1400×700 px: bands of 64 rows
		render := func(workers int, r image.Rectangle) *image.RGBA {
			dst := image.NewRGBA(r)
			if err := p.Render(context.Background(), dst, RenderOptions{Scale: scale, Background: white, Workers: workers}); err != nil {
				t.Fatal(err)
			}
			return dst
		}
		full := render(1, p.Bounds(scale))
		bh := p.dl.bandH
		for _, vp := range []image.Rectangle{
			image.Rect(50, bh+5, 400, 2*bh-5),     // one band
			image.Rect(0, 2*bh-10, 1400, 2*bh+10), // two
			image.Rect(300, bh/2, 900, 5*bh),      // five
		} {
			one := render(1, vp)
			if d := maxDiff(t, full, one, vp); d > 2 {
				t.Errorf("%s %v: differs from the whole page by %d", tc.name, vp, d)
			}
			if d := maxDiff(t, render(4, vp), one, vp); d > 2 {
				t.Errorf("%s %v: one and 4 workers differ by %d", tc.name, vp, d)
			}
		}
	}
}

// TestViewportDrawsItemsOnce draws a translucent fill across bands in a
// viewport that merges the bands' items: drawn twice it would be darker.
func TestViewportDrawsItemsOnce(t *testing.T) {
	var c strings.Builder
	for i := range 400 {
		fmt.Fprintf(&c, "%d g %d 0 1 1 re f\n", i%2, i%200)
	}
	c.WriteString("/H gs 1 0 0 rg 0 0 200 100 re f\n")
	doc, _ := Open(buildPDF([]string{c.String()}, "/Resources << /ExtGState << /H << /ca 0.5 >> >> >>"))
	p, _ := doc.Page(0)
	const scale = 7
	if err := p.Render(context.Background(), image.NewRGBA(p.Bounds(scale)), RenderOptions{Scale: scale, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	l := p.dl
	vp := image.Rect(100, 10, 200, 4*l.bandH)
	b0, b1 := l.bandRange(vp)
	idx, _ := l.regionItems(nil, b0, b1)
	if len(idx) == len(l.allItems) || !slices.IsSorted(idx) || len(slices.Compact(slices.Clone(idx))) != len(idx) {
		t.Fatalf("bands %d–%d: %d of %d items, %v", b0, b1, len(idx), len(l.allItems), idx)
	}
	dst := image.NewRGBA(vp)
	if err := p.Render(context.Background(), dst, RenderOptions{Scale: scale, Background: white, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	for _, y := range []int{vp.Min.Y, l.bandH, 2*l.bandH + 1, vp.Max.Y - 1} {
		if c := dst.RGBAAt(150, y); diff(c, color.RGBA{255, 128, 128, 255}) > 1 {
			t.Errorf("row %d: %v", y, c)
		}
	}
}

// BenchmarkViewport draws a small viewport of a cached list with one
// worker. One rectangle is visible; the others lie outside the viewport,
// in other bands, and are what the benchmark varies.
func BenchmarkViewport(b *testing.B) {
	for _, offscreen := range []int{1000, 100000} {
		var c strings.Builder
		for i := range offscreen {
			fmt.Fprintf(&c, "%d g %d %d 4 4 re f\n", i%2, i%250, i%2900)
		}
		c.WriteString("0.5 g 10 4050 20 20 re f\n")
		doc, err := Open(buildPDF([]string{c.String()}, "/MediaBox [0 0 256 4096]"))
		if err != nil {
			b.Fatal(err)
		}
		p, _ := doc.Page(0)
		if err := p.Render(context.Background(), image.NewRGBA(p.Bounds(1)), RenderOptions{Workers: 1}); err != nil {
			b.Fatal(err)
		}
		for _, vp := range []image.Rectangle{image.Rect(0, 0, 64, 64), image.Rect(0, 0, 64, 700)} {
			dst := image.NewRGBA(vp)
			b.Run(fmt.Sprintf("offscreen=%d/rows=%d", offscreen, vp.Dy()), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if err := p.Render(context.Background(), dst, RenderOptions{Workers: 1}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// deviceBoxMinMax is deviceBoxPoints as it was, with min and max.
func deviceBoxMinMax(pts []stilus.Point, m Matrix, pad float64) image.Rectangle {
	if len(pts) == 0 {
		return image.Rectangle{}
	}
	x0, y0 := math.Inf(1), math.Inf(1)
	x1, y1 := math.Inf(-1), math.Inf(-1)
	for _, q := range pts {
		x, y := float64(q.X), float64(q.Y)
		x0, x1 = min(x0, x), max(x1, x)
		y0, y1 = min(y0, y), max(y1, y)
	}
	var bx0, by0 = math.Inf(1), math.Inf(1)
	var bx1, by1 = math.Inf(-1), math.Inf(-1)
	for _, c := range [4][2]float64{{x0, y0}, {x1, y0}, {x0, y1}, {x1, y1}} {
		x, y := m.Apply(c[0], c[1])
		bx0, bx1 = min(bx0, x), max(bx1, x)
		by0, by1 = min(by0, y), max(by1, y)
	}
	const lim = 1 << 30
	if !(bx0 >= -lim && by0 >= -lim && bx1 <= lim && by1 <= lim) {
		return image.Rect(-lim, -lim, lim, lim)
	}
	return image.Rect(
		int(math.Floor(bx0-pad)), int(math.Floor(by0-pad)),
		int(math.Ceil(bx1+pad)), int(math.Ceil(by1+pad)),
	)
}

func TestDeviceBoxPoints(t *testing.T) {
	nan, inf := float32(math.NaN()), float32(math.Inf(1))
	rng := rand.New(rand.NewPCG(1, 2))
	coord := func() float32 {
		switch rng.IntN(40) {
		case 0:
			return nan
		case 1:
			return inf
		case 2:
			return -inf
		case 3:
			return 3e9
		}
		return float32(rng.NormFloat64() * 300)
	}
	matrices := []Matrix{
		{1, 0, 0, 1, 0, 0}, {2, 0, 0, -2, 10, 800}, {0, 1.5, -1.5, 0, 300, 0},
		{0.7, 0.7, -0.7, 0.7, 5, 5}, {1e9, 0, 0, 1e9, 0, 0},
		{math.NaN(), 0, 0, 1, 0, 0}, {1, 0, 0, 1, math.Inf(1), 0}, {math.Inf(1), 0, 0, 1, 0, 0},
	}
	for range 20000 {
		pts := make([]stilus.Point, rng.IntN(6))
		for i := range pts {
			pts[i] = stilus.Point{X: coord(), Y: coord()}
		}
		m := matrices[rng.IntN(len(matrices))]
		pad := float64(rng.IntN(3))
		if got, want := deviceBoxPoints(pts, m, pad), deviceBoxMinMax(pts, m, pad); got != want {
			t.Fatalf("points %v, matrix %v: box %v, want %v", pts, m, got, want)
		}
	}
	// A NaN anywhere covers everything, as it did.
	all := image.Rect(-1<<30, -1<<30, 1<<30, 1<<30)
	for _, pts := range [][]stilus.Point{{{X: nan, Y: 0}}, {{X: 0, Y: 0}, {X: 5, Y: nan}}, {{X: 1, Y: 1}, {X: nan, Y: nan}, {X: 2, Y: 2}}} {
		if got := deviceBoxPoints(pts, Matrix{1, 0, 0, 1, 0, 0}, 1); got != all {
			t.Errorf("points %v: box %v", pts, got)
		}
	}
}

func BenchmarkDeviceBoxPoints(b *testing.B) {
	pts := make([]stilus.Point, 64)
	for i := range pts {
		pts[i] = stilus.Point{X: float32(i%13) * 7, Y: float32(i%7) * 11}
	}
	m := Matrix{2, 0, 0, -2, 10, 800}
	for b.Loop() {
		_ = deviceBoxPoints(pts, m, 1)
	}
}

// TestWorkersFor checks that a page estimated to draw quickly is drawn
// by one worker, and that the number of workers grows with the estimate
// and with the part of the page drawn.
func TestWorkersFor(t *testing.T) {
	render := func(content string, scale float64) *displayList {
		doc, err := Open(buildPDF([]string{content}, ""))
		if err != nil {
			t.Fatal(err)
		}
		p, _ := doc.Page(0)
		if err := p.Render(context.Background(), image.NewRGBA(p.Bounds(scale)), RenderOptions{Scale: scale, Workers: 1}); err != nil {
			t.Fatal(err)
		}
		return p.dl
	}
	small := render("0 g 10 10 20 20 re f", 2)
	if n := small.workersFor(small.bounds); n != 1 {
		t.Errorf("small fill: %d workers", n)
	}
	var c strings.Builder
	for i := range 200 {
		fmt.Fprintf(&c, "%d g 0 %d 200 3 re f\n", i%2, i%100)
	}
	big := render(c.String(), 8)
	all := big.workersFor(big.bounds)
	if all < 4 {
		t.Errorf("200 page-wide fills at 8×: %d workers (cost %.0f ns)", all, big.cost)
	}
	tile := big.workersFor(image.Rect(0, 0, 100, 100))
	if tile >= all {
		t.Errorf("a tile gets %d workers, the page %d", tile, all)
	}
	if n := big.workersFor(image.Rect(-100, -100, -1, -1)); n != 1 {
		t.Errorf("outside the page: %d workers", n)
	}
}
