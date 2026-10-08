// Command ceraworker is cera's side of the bench worker protocol (see
// bench/protocol.md). It is built natively and for GOOS=wasip1, so the
// same code is measured in both.
package main

import (
	"bufio"
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/timzifer/cera/bench/internal/clock"

	"github.com/timzifer/cera"
)

var white = color.RGBA{255, 255, 255, 255}

// fresh, set at build time (bench -cera-fresh), gives every render a new
// bitmap instead of one drawn before.
var fresh string

// bitmaps are bitmaps drawn into before, to draw into again, as a viewer
// draws into its framebuffer: Render fills the page with the background,
// so a bitmap needs no clearing. A new one each page is zeroed by the
// allocator, then filled, and on all cores its garbage collections cost
// more than a page of tens of microseconds (#63).
var bitmaps struct {
	sync.Mutex
	free []*image.RGBA
}

func getBitmap(r image.Rectangle) *image.RGBA {
	if fresh == "" {
		bitmaps.Lock()
		defer bitmaps.Unlock()
		n := 4 * r.Dx() * r.Dy()
		for k, b := range bitmaps.free {
			if cap(b.Pix) >= n {
				bitmaps.free = append(bitmaps.free[:k], bitmaps.free[k+1:]...)
				return &image.RGBA{Pix: b.Pix[:n], Stride: 4 * r.Dx(), Rect: r}
			}
		}
	}
	return image.NewRGBA(r)
}

func putBitmap(b *image.RGBA) {
	if fresh == "" && b != nil {
		bitmaps.Lock()
		bitmaps.free = append(bitmaps.free, b)
		bitmaps.Unlock()
	}
}

// worker holds the open document. Commands come one at a time.
type worker struct {
	doc *cera.Document
	out *bufio.Writer
}

func main() {
	runtime.GOMAXPROCS(1)
	w := &worker{out: bufio.NewWriter(os.Stdout)}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 1<<20)
	for in.Scan() {
		f := strings.Split(in.Text(), "\t")
		if f[0] == "quit" {
			return
		}
		w.reply(w.do(f)...)
	}
}

func (w *worker) reply(fields ...string) {
	w.out.WriteString(strings.Join(fields, "\t"))
	w.out.WriteByte('\n')
	w.out.Flush()
}

func (w *worker) do(f []string) (r []string) {
	defer func() {
		if e := recover(); e != nil {
			r = errReply(fmt.Errorf("panic: %v", e))
		}
	}()
	switch f[0] {
	case "version":
		return []string{"ok", "cera"}
	case "open":
		data, err := os.ReadFile(f[1])
		if err != nil {
			return errReply(err)
		}
		pw := ""
		if len(f) > 2 {
			pw = f[2]
		}
		t0 := clock.Now()
		doc, err := cera.OpenWith(data, cera.OpenOptions{Password: pw})
		ns := clock.Since(t0)
		if err != nil {
			return errReply(err)
		}
		w.doc = doc
		return []string{"ok", itoa(int64(ns)), strconv.Itoa(doc.NumPages())}
	case "render":
		i, scale := atoi(f[1]), atof(f[2])
		runtime.GOMAXPROCS(1)
		ns, img, err := w.render(i, scale, 1)
		if err != nil {
			return errReply(err)
		}
		r := []string{"ok", itoa(ns), strconv.Itoa(img.Rect.Dx()), strconv.Itoa(img.Rect.Dy())}
		if len(f) > 3 && f[3] == "1" {
			r = append(r, strconv.FormatFloat(ink(img), 'f', 5, 64))
		}
		putBitmap(img)
		return r
	case "again":
		// A page rendered again from its display list (a scrolled
		// viewport, another tile): cera's cache, timed apart.
		i, scale := atoi(f[1]), atof(f[2])
		runtime.GOMAXPROCS(1)
		p, err := w.doc.Page(i)
		if err != nil {
			return errReply(err)
		}
		defer p.Release()
		dst := image.NewRGBA(p.Bounds(scale))
		opt := cera.RenderOptions{Scale: scale, Background: white, Workers: 1}
		t0 := clock.Now()
		err = p.Render(context.Background(), dst, opt)
		first := clock.Since(t0)
		if err != nil {
			return errReply(err)
		}
		dst = image.NewRGBA(p.Bounds(scale))
		t0 = clock.Now()
		err = p.Render(context.Background(), dst, opt)
		again := clock.Since(t0)
		if err != nil {
			return errReply(err)
		}
		return []string{"ok", itoa(int64(first)), itoa(int64(again))}
	case "allocs":
		i, scale := atoi(f[1]), atof(f[2])
		runtime.GOMAXPROCS(1)
		p, err := w.doc.Page(i)
		if err != nil {
			return errReply(err)
		}
		// As cmd/corpus counts them: the page drawn once, released, and
		// drawn again from scratch into the same bitmap.
		dst := image.NewRGBA(p.Bounds(scale))
		opt := cera.RenderOptions{Scale: scale, Background: white, Workers: 1}
		if err := p.Render(context.Background(), dst, opt); err != nil {
			return errReply(err)
		}
		p.Release()
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		err = p.Render(context.Background(), dst, opt)
		runtime.ReadMemStats(&m1)
		p.Release()
		if err != nil {
			return errReply(err)
		}
		// The bitmap is the caller's, allocated before: it is not counted.
		return []string{"ok", itoa(int64(m1.Mallocs - m0.Mallocs)), itoa(int64(m1.TotalAlloc - m0.TotalAlloc))}
	case "all":
		scale, threads := atof(f[1]), atoi(f[2])
		return w.all(scale, threads)
	}
	return []string{"unsupported"}
}

// render draws page i as a viewer showing the page for the first time
// does, into a bitmap drawn into before (getBitmap): the page is released
// afterwards, so the next render interprets it again.
func (w *worker) render(i int, scale float64, workers int) (int64, *image.RGBA, error) {
	t0 := clock.Now()
	p, err := w.doc.Page(i)
	if err != nil {
		return 0, nil, err
	}
	dst := getBitmap(p.Bounds(scale))
	err = p.Render(context.Background(), dst, cera.RenderOptions{Scale: scale, Background: white, Workers: workers})
	ns := clock.Since(t0)
	p.Release()
	return int64(ns), dst, err
}

// all renders every page on threads cores: pages concurrently, each with
// the default workers (all cores), so the bands of the pages still being
// drawn take the cores of those done. Each page draws on fewer workers
// when it is cheap (RenderOptions.Workers).
func (w *worker) all(scale float64, threads int) []string {
	n := w.doc.NumPages()
	g := min(threads, n)
	runtime.GOMAXPROCS(threads)
	defer runtime.GOMAXPROCS(1)
	settle()
	next := make(chan int, n)
	for i := range n {
		next <- i
	}
	close(next)
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	pages := func() {
		for i := range next {
			_, img, err := w.render(i, scale, 0)
			putBitmap(img)
			if err != nil {
				mu.Lock()
				first = cmpErr(first, err)
				mu.Unlock()
			}
		}
	}
	t0 := clock.Now()
	// The calling goroutine draws pages too, as a program would: starting
	// and waiting for one goroutine per page cost about as much as a page
	// of tens of microseconds.
	for range g - 1 {
		wg.Go(pages)
	}
	pages()
	wg.Wait()
	ns := clock.Since(t0)
	if first != nil {
		return errReply(first)
	}
	return []string{"ok", itoa(int64(ns))}
}

// settle lets the runtime come to rest after GOMAXPROCS changed, before
// a timed render: the threads it wakes for the new Ps look for work for
// a while, on the cores the render runs on.
func settle() {
	time.Sleep(10 * time.Millisecond)
}

func cmpErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// ink is the share of colour channel values below paper white, a cheap
// check that a page drew something.
func ink(img *image.RGBA) float64 {
	n := 0
	for i := 0; i+3 < len(img.Pix); i += 4 {
		for _, v := range img.Pix[i : i+3] {
			if v != 255 {
				n++
			}
		}
	}
	return float64(n) / float64(len(img.Pix)/4*3)
}

func errReply(err error) []string {
	return []string{"err", strings.NewReplacer("\t", " ", "\n", " ").Replace(err.Error())}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func atoi(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}

func atof(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
