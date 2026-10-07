package cera

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/timzifer/cera/internal/corpus"
)

// sharedResourcesPDF has pages that draw the same font, image, form and
// shading, so rendering them concurrently loads the same objects from many
// goroutines at once.
func sharedResourcesPDF(pages int) []byte {
	var contents []string
	for i := range pages {
		contents = append(contents, fmt.Sprintf(
			"q 0.%d 0 0 rg BT /F1 %d Tf 10 70 Td (Page %d: shared resources) Tj ET Q "+
				"q 40 0 0 30 120 10 cm /Im0 Do Q q 0.5 0 0 0.5 %d 20 cm /Fm0 Do Q "+
				"q 10 10 60 40 re W n /Sh0 sh Q", i%10, 8+i%5, i, 5*i))
	}
	font := "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"
	img := streamObj("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8",
		[]byte{255, 0, 0, 0, 255, 0, 0, 0, 255, 255, 255, 0})
	form := streamObj("/Type /XObject /Subtype /Form /BBox [0 0 60 60] /Resources << /Font << /F1 100 0 R >> >>",
		[]byte("0 0 1 rg 0 0 60 60 re f BT /F1 9 Tf 2 2 Td (form) Tj ET"))
	shading := "<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [10 0 70 0] " +
		"/Function << /FunctionType 2 /Domain [0 1] /C0 [1 1 0] /C1 [0 0.5 1] /N 1 >> >>"
	return buildPDF(contents,
		"/Resources << /Font << /F1 100 0 R >> /XObject << /Im0 101 0 R /Fm0 102 0 R >> /Shading << /Sh0 103 0 R >> >>",
		font, img, form, shading)
}

// TestConcurrentPages renders every page of one document from its own
// goroutine and requires the pixels a sequential render gives.
func TestConcurrentPages(t *testing.T) {
	const pages = 12
	data := sharedResourcesPDF(pages)
	render := func(doc *Document, i int) []byte {
		p, err := doc.Page(i)
		if err != nil {
			t.Error(err)
			return nil
		}
		dst := image.NewRGBA(p.Bounds(1.5))
		if err := p.Render(context.Background(), dst, RenderOptions{Scale: 1.5}); err != nil {
			t.Error(err)
		}
		return dst.Pix
	}
	seq, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	want := make([][]byte, pages)
	for i := range pages {
		want[i] = render(seq, i)
		if !bytes.ContainsFunc(want[i], func(r rune) bool { return r != 0xFF }) {
			t.Fatalf("page %d is blank", i)
		}
	}

	par, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	got := make([][]byte, pages)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range pages {
		wg.Go(func() {
			<-start
			got[i] = render(par, i)
		})
	}
	close(start)
	wg.Wait()
	for i := range pages {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("page %d differs when pages are rendered concurrently", i)
		}
	}
}

// TestOpenReaderAt renders pages of a document read through an io.ReaderAt,
// concurrently, as they render from memory.
func TestOpenReaderAt(t *testing.T) {
	data := sharedResourcesPDF(4)
	mem, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	ra, err := OpenReaderAt(bytes.NewReader(data), int64(len(data)), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	render := func(doc *Document, i int) []byte {
		p, err := doc.Page(i)
		if err != nil {
			t.Fatal(err)
		}
		dst := image.NewRGBA(p.Bounds(1))
		if err := p.Render(context.Background(), dst, RenderOptions{Scale: 1}); err != nil {
			t.Error(err)
		}
		return dst.Pix
	}
	got := make([][]byte, 4)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() { got[i] = render(ra, i) })
	}
	wg.Wait()
	for i := range 4 {
		if !bytes.Equal(got[i], render(mem, i)) {
			t.Errorf("page %d differs read through an io.ReaderAt", i)
		}
	}
}

// TestConcurrentCorpusPages is TestConcurrentPages over every PDF below
// $CERA_CORPUS (for example testdata/corpus), at most 8 pages a file: real
// files bring embedded, composite and Type 3 fonts, images and patterns.
// Run it under the race detector.
func TestConcurrentCorpusPages(t *testing.T) {
	dir := os.Getenv("CERA_CORPUS")
	if dir == "" {
		t.Skip("CERA_CORPUS not set")
	}
	var files []string
	_ = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && strings.EqualFold(filepath.Ext(p), ".pdf") {
			files = append(files, p)
		}
		return nil
	})
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		pw, _ := corpus.Password(f)
		render := func(doc *Document, i int) []byte {
			p, err := doc.Page(i)
			if err != nil {
				return nil
			}
			dst := image.NewRGBA(p.Bounds(0.5))
			_ = p.Render(context.Background(), dst, RenderOptions{Scale: 0.5})
			return dst.Pix
		}
		seq, err := OpenWithPassword(data, pw)
		if err != nil {
			continue
		}
		n := min(seq.NumPages(), 8)
		want := make([][]byte, n)
		for i := range n {
			want[i] = render(seq, i)
		}
		par, _ := OpenWithPassword(data, pw)
		got := make([][]byte, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() { got[i] = render(par, i) })
		}
		wg.Wait()
		for i := range n {
			if !bytes.Equal(got[i], want[i]) {
				t.Errorf("%s page %d differs when pages are rendered concurrently", f, i)
			}
		}
	}
}

// bigListPDF is a page whose recording takes a while.
func bigListPDF(n int) []byte {
	return buildPDF([]string{strings.Repeat("0 g 0 0 1 1 re f 0.5 g 0 0 1 1 re f\n", n)}, "")
}

// flight returns the recording of p at key in progress, if any, and the
// renders waiting for it.
func (p *Page) flight(key listKey) (*listFlight, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.flights[key]
	if f == nil {
		return nil, 0
	}
	return f, f.waiters
}

// waitFor polls cond until it holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); !cond(); {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Microsecond)
	}
}

func TestColdConcurrentRecording(t *testing.T) {
	doc, _ := Open(bigListPDF(10000))
	p, _ := doc.Page(0)
	const n = 8
	var (
		lists  [n]*displayList
		reused [n]bool
		start  = make(chan struct{})
		wg     sync.WaitGroup
	)
	for i := range n {
		wg.Go(func() {
			<-start
			lists[i], reused[i] = p.list(1, false, &limit{ctx: context.Background()})
		})
	}
	close(start)
	wg.Wait()
	recorded := 0
	for i := range n {
		if lists[i] != lists[0] || !lists[i].complete {
			t.Errorf("request %d: list %p (complete %v), request 0 %p", i, lists[i], lists[i].complete, lists[0])
		}
		if !reused[i] {
			recorded++
		}
	}
	if recorded != 1 {
		t.Errorf("recorded %d times", recorded)
	}
	if p.dl != lists[0] || p.dl.refs != n || len(p.flights) != 0 {
		t.Errorf("cached %p with %d refs, %d recordings pending", p.dl, p.dl.refs, len(p.flights))
	}
	for _, dl := range lists {
		p.done(dl)
	}
	if p.dl.refs != 0 {
		t.Errorf("%d refs left", p.dl.refs)
	}
	// Other keys are recorded on their own.
	var other [3]*displayList
	for i, k := range []listKey{{2, false}, {1, true}, {2, false}} {
		wg.Go(func() { other[i], _ = p.list(k.scale, k.overprint, &limit{ctx: context.Background()}) })
	}
	wg.Wait()
	if other[0] != other[2] || other[0] == other[1] || other[0].scale != 2 || !other[1].overprint {
		t.Errorf("lists for other keys: %p %p %p", other[0], other[1], other[2])
	}
	for _, dl := range other {
		p.done(dl)
	}
}

// TestRecordingLeaderCancelled cancels the render that records while
// others wait: they record again rather than share its partial list.
func TestRecordingLeaderCancelled(t *testing.T) {
	doc, _ := Open(bigListPDF(200000))
	p, _ := doc.Page(0)
	key := listKey{1, false}
	ctx, cancel := context.WithCancel(context.Background())
	var leader *displayList
	var wg sync.WaitGroup
	wg.Go(func() { leader, _ = p.list(1, false, &limit{ctx: ctx}) })
	waitFor(t, "the recording", func() bool { f, _ := p.flight(key); return f != nil })
	const n = 4
	var lists [n]*displayList
	for i := range n {
		wg.Go(func() { lists[i], _ = p.list(1, false, &limit{ctx: context.Background()}) })
	}
	waitFor(t, "the waiters", func() bool { _, w := p.flight(key); return w == n })
	cancel()
	wg.Wait()
	if leader.complete {
		t.Skip("recording ended before the cancellation")
	}
	for i := range n {
		if !lists[i].complete || lists[i] != lists[0] {
			t.Errorf("waiter %d: list %p (complete %v), waiter 0 %p", i, lists[i], lists[i].complete, lists[0])
		}
	}
	if p.dl != lists[0] {
		t.Error("the waiters' list is not cached")
	}
	p.done(leader)
	for _, dl := range lists {
		p.done(dl)
	}
	if p.dl.refs != 0 {
		t.Errorf("%d refs left", p.dl.refs)
	}
}

// TestRecordingWaiterGivesUp ends the limit of a render waiting for a
// recording: it returns at once, and the recording goes on.
func TestRecordingWaiterGivesUp(t *testing.T) {
	doc, _ := Open(bigListPDF(200000))
	p, _ := doc.Page(0)
	key := listKey{1, false}
	var leader *displayList
	var wg sync.WaitGroup
	wg.Go(func() { leader, _ = p.list(1, false, &limit{ctx: context.Background()}) })
	waitFor(t, "the recording", func() bool { f, _ := p.flight(key); return f != nil })
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan *displayList)
	go func() {
		dl, _ := p.list(1, false, &limit{ctx: ctx})
		got <- dl
	}()
	waitFor(t, "the waiter", func() bool { f, w := p.flight(key); return f == nil || w == 1 })
	deadline := make(chan *displayList)
	go func() {
		dl, _ := p.list(1, false, &limit{ctx: context.Background(), deadline: time.Now().Add(time.Millisecond)})
		deadline <- dl
	}()
	cancel()
	for _, c := range []chan *displayList{got, deadline} {
		dl := <-c
		if f, _ := p.flight(key); f == nil {
			t.Skip("recording ended before the waiters gave up")
		}
		if dl.complete || dl == leader {
			t.Errorf("a waiter that gave up got list %p (complete %v)", dl, dl.complete)
		}
		p.done(dl)
	}
	p.Release() // while recording
	wg.Wait()
	if !leader.complete || leader.refs != 1 {
		t.Errorf("recorded list: complete %v, %d refs", leader.complete, leader.refs)
	}
	if p.dl != leader {
		t.Error("the recorded list is not cached")
	}
	p.done(leader)
}

// BenchmarkColdConcurrentTiles opens a document and renders eight tiles of
// its page concurrently, as a viewer does when it first shows the page.
func BenchmarkColdConcurrentTiles(b *testing.B) {
	data := buildPDF([]string{strings.Repeat("0 g 0 0 1 1 re f 0.5 g 0 0 1 1 re f\n", 10000)}, "")
	b.ReportAllocs()
	var records atomic.Int64
	for b.Loop() {
		doc, _ := Open(data)
		p, _ := doc.Page(0)
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				dst := image.NewRGBA(image.Rect(i*25, 0, i*25+25, 100))
				var st Stats
				_ = p.Render(context.Background(), dst, RenderOptions{Workers: 1, Stats: &st})
				if !st.Reused {
					records.Add(1)
				}
			})
		}
		wg.Wait()
	}
	b.ReportMetric(float64(records.Load())/float64(b.N), "records/op")
}
