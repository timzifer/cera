package cera

import (
	"bytes"
	"context"
	"image"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
)

// cachePDF is a page with two images, 8×8 and 16×16 RGB.
func cachePDF() []byte {
	im := func(side int) string {
		return stream("/Type /XObject /Subtype /Image /Width "+itoa(side)+" /Height "+itoa(side)+
			" /ColorSpace /DeviceRGB /BitsPerComponent 8", string(bytes.Repeat([]byte{200, 30, 30}, side*side)))
	}
	return imagePDF("q 40 0 0 40 10 10 cm /Im0 Do Q q 40 0 0 40 60 10 cm /Im1 Do Q", im(8), im(16))
}

// cachedSize returns the bytes the image that is object num of doc is
// cached with.
func cachedSize(t *testing.T, doc *Document, num int32) int {
	t.Helper()
	e, ok := doc.imgs.m[imageKey{doc.id, pdf.Ref{Num: num}}]
	if !ok {
		t.Fatalf("object %d is not cached", num)
	}
	return e.Value.(*imageEntry).size
}

func renderFirst(t *testing.T, doc *Document) *image.RGBA {
	t.Helper()
	p, err := doc.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	// Recorded anew each time, so that the images are asked for.
	defer p.Release()
	dst := image.NewRGBA(p.Bounds(1))
	if err := p.Render(context.Background(), dst, RenderOptions{}); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestImageCacheBytes(t *testing.T) {
	data := cachePDF()
	open := func(opt OpenOptions) *Document {
		doc, err := OpenWith(data, opt)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	doc := open(OpenOptions{})
	want := renderFirst(t, doc)
	if n, _ := doc.imgs.len(); n != 2 {
		t.Fatalf("%d images cached by default", n)
	}
	small, large := cachedSize(t, doc, 100), cachedSize(t, doc, 101)
	if small >= large {
		t.Fatalf("the images are cached with %d and %d bytes", small, large)
	}
	for _, c := range []struct {
		name  string
		bytes int64
		n     int
	}{
		{"none", -1, 0},
		{"one byte", 1, 0},
		{"the small image", int64(small), 1},
		{"one of them", int64(large), 1},
		{"both", int64(small + large), 2},
	} {
		doc := open(OpenOptions{ImageCacheBytes: c.bytes, StreamCacheBytes: c.bytes})
		for range 2 {
			if got := renderFirst(t, doc); !bytes.Equal(got.Pix, want.Pix) {
				t.Errorf("%s: the page is drawn otherwise", c.name)
			}
		}
		if n, bytes := doc.imgs.len(); n != c.n || bytes > int(max(c.bytes, 0)) {
			t.Errorf("%s: %d images, %d bytes cached, want %d images", c.name, n, bytes, c.n)
		}
	}
}

func TestDocumentTrim(t *testing.T) {
	doc, err := Open(cachePDF())
	if err != nil {
		t.Fatal(err)
	}
	want := renderFirst(t, doc)
	doc.Trim()
	if n, bytes := doc.imgs.len(); n != 0 || bytes != 0 {
		t.Errorf("%d images, %d bytes after Trim", n, bytes)
	}
	if got := renderFirst(t, doc); !bytes.Equal(got.Pix, want.Pix) {
		t.Error("the page is drawn otherwise after Trim")
	}
	if n, _ := doc.imgs.len(); n != 2 {
		t.Errorf("%d images cached again", n)
	}
}

func TestSharedCache(t *testing.T) {
	data := cachePDF()
	c := NewCache(1<<20, 1<<20)
	open := func() *Document {
		doc, err := OpenWith(data, OpenOptions{Cache: c, ImageCacheBytes: -1})
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	a, b := open(), open()
	want := renderFirst(t, a)
	renderFirst(t, b)
	if n, _ := c.images.len(); n != 4 {
		t.Fatalf("%d images of two documents in the cache", n)
	}
	large := cachedSize(t, a, 101)
	// One document trimmed: the other keeps its images.
	a.Trim()
	if n, _ := c.images.len(); n != 2 {
		t.Errorf("%d images after one document was trimmed", n)
	}
	if _, ok := c.images.get(imageKey{b.id, pdf.Ref{Num: 100}}); !ok {
		t.Error("the other document lost its image")
	}
	if got := renderFirst(t, a); !bytes.Equal(got.Pix, want.Pix) {
		t.Error("the page is drawn otherwise after Trim")
	}
	c.Trim()
	if n, bytes := c.images.len(); n != 0 || bytes != 0 {
		t.Errorf("%d images, %d bytes after Cache.Trim", n, bytes)
	}

	// One bound for all: of many documents no more is kept than of one.
	small := NewCache(int64(large), 0)
	for range 8 {
		doc, err := OpenWith(data, OpenOptions{Cache: small})
		if err != nil {
			t.Fatal(err)
		}
		renderFirst(t, doc)
	}
	if n, bytes := small.images.len(); n == 0 || bytes > small.images.max {
		t.Errorf("%d images, %d bytes in a cache of %d", n, bytes, small.images.max)
	}
}

func TestSetPoolLimits(t *testing.T) {
	defer SetPoolLimits(PoolLimits{})
	doc, err := Open(cachePDF())
	if err != nil {
		t.Fatal(err)
	}
	want := renderFirst(t, doc)
	idle := func() int {
		glyphCaches.Lock()
		defer glyphCaches.Unlock()
		return len(glyphCaches.free)
	}
	if idle() == 0 {
		t.Fatal("no glyph cache kept after a render")
	}
	SetPoolLimits(PoolLimits{GlyphCaches: -1, LayerBytes: -1})
	if n := idle(); n != 0 {
		t.Errorf("%d glyph caches kept with none allowed", n)
	}
	if got := renderFirst(t, doc); !bytes.Equal(got.Pix, want.Pix) {
		t.Error("the page is drawn otherwise without pools")
	}
	if n := idle(); n != 0 {
		t.Errorf("%d glyph caches kept after a render with none allowed", n)
	}
	var l layers
	l.release(make([]byte, 64))
	if len(l.free) != 0 {
		t.Error("a layer buffer kept with none allowed")
	}
	SetPoolLimits(PoolLimits{})
	l.release(make([]byte, 64))
	if len(l.free) != 1 || l.freeBytes != 64 {
		t.Errorf("%d layer buffers, %d bytes kept by default", len(l.free), l.freeBytes)
	}
	SetPoolLimits(PoolLimits{LayerBytes: 16})
	l.reset(nil, image.Rectangle{})
	if len(l.free) != 0 || l.freeBytes != 0 {
		t.Errorf("%d layer buffers, %d bytes kept past a lowered bound", len(l.free), l.freeBytes)
	}
}
