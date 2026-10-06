package pdf

import (
	"sync"
)

// DefaultStreamCacheBytes is how many bytes of decoded streams a Document
// keeps unless Options says otherwise.
const DefaultStreamCacheBytes = 64 << 20

// MaxCachedStream is the largest decoded stream the cache keeps; larger
// ones are decoded on every use rather than evict everything else.
const MaxCachedStream = 8 << 20

const cacheShards = 8

// A streamCache keeps decoded streams, least recently used evicted first,
// bounded in bytes. Concurrent requests for the same stream decode it once:
// the first one decodes, the others wait for it.
//
// Its accounting is soft: an evicted Decoded stays valid for whoever still
// holds it.
type streamCache struct {
	max    int // bytes per shard
	shards [cacheShards]cacheShard
}

type cacheShard struct {
	mu    sync.Mutex
	m     map[*Stream]*cacheEntry
	lru   cacheEntry // ring sentinel: lru.next is the most recent
	bytes int
}

type cacheEntry struct {
	key        *Stream
	dec        Decoded
	size       int
	ready      chan struct{} // closed once dec is set
	prev, next *cacheEntry
}

func (c *streamCache) init(maxBytes int) {
	c.max = maxBytes / cacheShards
	for i := range c.shards {
		s := &c.shards[i]
		s.m = map[*Stream]*cacheEntry{}
		s.lru.next, s.lru.prev = &s.lru, &s.lru
	}
}

// shard picks a shard by the stream's object number.
func (c *streamCache) shard(s *Stream) *cacheShard {
	return &c.shards[uint32(s.Ref.Num)%cacheShards]
}

// get returns the decoded stream, decoding it with decode on a miss.
// decode must not use the cache.
func (c *streamCache) get(s *Stream, decode func() Decoded) Decoded {
	if c.max <= 0 {
		return decode()
	}
	sh := c.shard(s)
	sh.mu.Lock()
	if e, ok := sh.m[s]; ok {
		if e.prev != nil {
			sh.unlink(e)
			sh.pushFront(e)
		}
		sh.mu.Unlock()
		<-e.ready
		return e.dec
	}
	e := &cacheEntry{key: s, ready: make(chan struct{})}
	sh.m[s] = e
	sh.mu.Unlock()

	e.dec = decode()
	e.size = len(e.dec.Data) + len(e.dec.Undecoded) + 128
	close(e.ready)

	sh.mu.Lock()
	if e.size > MaxCachedStream || e.size > c.max {
		delete(sh.m, s)
	} else {
		sh.pushFront(e)
		sh.bytes += e.size
		for sh.bytes > c.max {
			old := sh.lru.prev
			sh.unlink(old)
			delete(sh.m, old.key)
			sh.bytes -= old.size
		}
	}
	sh.mu.Unlock()
	return e.dec
}

func (sh *cacheShard) unlink(e *cacheEntry) {
	e.prev.next, e.next.prev = e.next, e.prev
	e.prev, e.next = nil, nil
}

func (sh *cacheShard) pushFront(e *cacheEntry) {
	e.prev, e.next = &sh.lru, sh.lru.next
	sh.lru.next.prev = e
	sh.lru.next = e
}
