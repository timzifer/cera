package pdf

import (
	"sync"
	"sync/atomic"
)

// DefaultStreamCacheBytes is how many bytes of decoded streams a Document
// keeps unless Options says otherwise.
const DefaultStreamCacheBytes = 64 << 20

// MaxCachedStream is the largest decoded stream the cache keeps; larger
// ones are decoded on every use rather than evict everything else.
const MaxCachedStream = 8 << 20

const cacheShards = 8

// A StreamCache keeps decoded streams, least recently used evicted first,
// bounded in bytes. Concurrent requests for the same stream decode it once:
// the first one decodes, the others wait for it. A Document has its own
// unless Options.StreamCache gives it one to share with others, which then
// bounds what all of them keep.
//
// Its accounting is soft: an evicted Decoded stays valid for whoever still
// holds it.
type StreamCache struct {
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
	owner      uint64 // the document of the stream
	dec        Decoded
	size       int
	ready      chan struct{} // closed once dec is set
	prev, next *cacheEntry
}

// NewStreamCache returns a cache of maxBytes of decoded streams; one that
// keeps nothing when maxBytes is not positive.
func NewStreamCache(maxBytes int) *StreamCache {
	c := &StreamCache{max: max(maxBytes, 0) / cacheShards}
	for i := range c.shards {
		s := &c.shards[i]
		s.m = map[*Stream]*cacheEntry{}
		s.lru.next, s.lru.prev = &s.lru, &s.lru
	}
	return c
}

// Trim drops what the cache holds.
func (c *StreamCache) Trim() { c.drop(0) }

// drop drops the streams of the document owner, of all documents for 0. A
// stream that is being decoded is kept when it is done.
func (c *StreamCache) drop(owner uint64) {
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.Lock()
		for e := sh.lru.next; e != &sh.lru; {
			next := e.next
			if owner == 0 || e.owner == owner {
				sh.unlink(e)
				delete(sh.m, e.key)
				sh.bytes -= e.size
			}
			e = next
		}
		sh.mu.Unlock()
	}
}

// owners numbers the documents, from 1.
var owners atomic.Uint64

// shard picks a shard by the stream's object number.
func (c *StreamCache) shard(s *Stream) *cacheShard {
	return &c.shards[uint32(s.Ref.Num)%cacheShards]
}

// get returns the decoded stream s of the document owner, decoding it with
// decode on a miss. decode must not use the cache.
func (c *StreamCache) get(owner uint64, s *Stream, decode func() Decoded) Decoded {
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
	e := &cacheEntry{key: s, owner: owner, ready: make(chan struct{})}
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
