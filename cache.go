package cera

import (
	"container/list"
	"math"
	"sync"
	"sync/atomic"

	"github.com/timzifer/cera/internal/pdf"
)

// A document keeps what it decodes for the next page that needs it: its
// images, bounded by OpenOptions.ImageCacheBytes, and its streams — the
// content of pages, forms, Type 3 glyphs, functions — bounded by
// OpenOptions.StreamCacheBytes. The bounds are per document, so the sum of
// many open documents has none; a Cache, shared by the documents opened
// with it, is one bound for all of them. Past a bound the least recently
// used entries are dropped, and drawing does not change.

// Cache holds the decoded images and streams of the documents opened with
// it (OpenOptions.Cache), least recently used dropped first: one bound for
// a server with many documents open, where a bound per document is none.
// It is safe for concurrent use.
//
// What a document has in it leaves with [Document.Trim] or when the
// document is collected, and otherwise as other entries come in.
type Cache struct {
	images  *imageCache
	streams *pdf.StreamCache
}

// NewCache returns a cache of imageBytes of decoded images and streamBytes
// of decoded streams. As for OpenOptions.ImageCacheBytes and
// StreamCacheBytes, 0 means the default of one document (256 MB, 64 MB) and
// a negative number none.
func NewCache(imageBytes, streamBytes int64) *Cache {
	return &Cache{
		images:  newImageCache(imageBytes),
		streams: pdf.NewStreamCache(cacheBytes(streamBytes, pdf.DefaultStreamCacheBytes)),
	}
}

// Trim drops what the cache holds, of all its documents. They stay usable.
func (c *Cache) Trim() {
	c.images.drop(0)
	c.streams.Trim()
}

// Trim drops the decoded images and streams the document keeps, in its own
// caches or in the Cache it was opened with. The document and its pages
// stay usable: what they show is decoded again when it is next drawn. The
// display list of a page is freed by [Page.Release].
func (d *Document) Trim() {
	d.imgs.drop(d.id)
	d.viewMu.Lock()
	for _, v := range d.views {
		d.imgs.drop(v.id)
	}
	d.viewMu.Unlock()
	d.r.TrimCache()
}

// cacheBytes returns the bound of a cache asked for as v: def for 0,
// nothing for a negative number.
func cacheBytes(v int64, def int) int {
	switch {
	case v == 0:
		return def
	case v < 0:
		return 0
	}
	return int(min(v, math.MaxInt))
}

// docIDs numbers the documents, from 1: what a shared cache knows the
// images of a document by, so that it keeps no document alive.
var docIDs atomic.Uint64

// imageCache holds decoded images by document and reference, least
// recently used last, bounded in bytes. A nil imageCache holds nothing.
type imageCache struct {
	mu    sync.Mutex
	max   int
	m     map[imageKey]*list.Element
	lru   list.List
	bytes int
}

type imageKey struct {
	doc uint64
	ref pdf.Ref
}

type imageEntry struct {
	key  imageKey
	res  imageResult
	size int
}

func newImageCache(maxBytes int64) *imageCache {
	return &imageCache{max: cacheBytes(maxBytes, imageCacheBytes), m: map[imageKey]*list.Element{}}
}

func (c *imageCache) get(k imageKey) (imageResult, bool) {
	if c == nil {
		return imageResult{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok {
		return imageResult{}, false
	}
	c.lru.MoveToFront(e)
	return e.Value.(*imageEntry).res, true
}

func (c *imageCache) put(k imageKey, r imageResult) {
	size := 64
	if r.img != nil {
		size += r.img.size
	}
	if c == nil || size > c.max {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[k]; ok {
		return
	}
	c.m[k] = c.lru.PushFront(&imageEntry{key: k, res: r, size: size})
	c.bytes += size
	for c.bytes > c.max {
		c.remove(c.lru.Back())
	}
}

func (c *imageCache) remove(e *list.Element) {
	old := c.lru.Remove(e).(*imageEntry)
	delete(c.m, old.key)
	c.bytes -= old.size
}

// drop drops the images of the document doc, of all documents for 0.
func (c *imageCache) drop(doc uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for e := c.lru.Front(); e != nil; {
		next := e.Next()
		if doc == 0 || e.Value.(*imageEntry).key.doc == doc {
			c.remove(e)
		}
		e = next
	}
}

// len returns the number of images held, and their bytes.
func (c *imageCache) len() (n, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m), c.bytes
}

// PoolLimits bound what the process keeps between renders for the next
// one, whatever the document.
type PoolLimits struct {
	// GlyphCaches is how many glyph mask caches of idle renderers are
	// kept, each of up to 4 MB: 0 means 64, a negative number none. A
	// render takes one per goroutine that draws.
	GlyphCaches int
	// LayerBytes bounds the buffers of transparency groups and soft masks
	// a raster device keeps for its next group: 0 means 64 MB, a negative
	// number none.
	LayerBytes int64
}

// SetPoolLimits sets the bounds of the pools of the process. Glyph caches
// past the new bound are dropped at once, layer buffers as the devices
// that hold them are next used or collected. It is safe to call while
// pages are rendered.
func SetPoolLimits(l PoolLimits) {
	idleGlyphCaches.Store(int64(cacheBytes(int64(l.GlyphCaches), maxIdleGlyphCaches)))
	freeLayerBytes.Store(int64(cacheBytes(l.LayerBytes, maxFreeLayerBytes)))
	gcs := &glyphCaches
	gcs.Lock()
	if n := int(idleGlyphCaches.Load()); len(gcs.free) > n {
		clear(gcs.free[n:])
		gcs.free = gcs.free[:n]
	}
	gcs.Unlock()
}

// The bounds SetPoolLimits sets.
var idleGlyphCaches, freeLayerBytes atomic.Int64

func init() {
	idleGlyphCaches.Store(maxIdleGlyphCaches)
	freeLayerBytes.Store(maxFreeLayerBytes)
}
