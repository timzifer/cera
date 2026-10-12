package pdf

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDecodeIsCachedAndShared(t *testing.T) {
	d := mustOpen(t, file{objs: onePage("0 0 m 1 1 l S"), trailer: "/Root 1 0 R"}.bytes())
	s, _ := mustGet(t, d, 4).Stream()
	var wg sync.WaitGroup
	got := make([][]byte, 16)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i] = d.Decode(s).Data
		}()
	}
	wg.Wait()
	for _, g := range got {
		if &g[0] != &got[0][0] {
			t.Fatal("concurrent decodes of one stream did not share the result")
		}
	}
	if n := testing.AllocsPerRun(100, func() { d.Decode(s) }); n != 0 && !raceEnabled {
		t.Errorf("%v allocations for a cached decode", n)
	}
}

func TestStreamCacheSingleFlight(t *testing.T) {
	c := NewStreamCache(1 << 20)
	s := &Stream{Ref: Ref{Num: 3}}
	var calls atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.get(1, s, func() Decoded {
				calls.Add(1)
				<-release
				return Decoded{Data: []byte("x")}
			})
		}()
	}
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Errorf("decoded %d times", n)
	}
}

func TestStreamCacheBound(t *testing.T) {
	c := NewStreamCache(cacheShards * 10_000)
	for i := range 1000 {
		s := &Stream{Ref: Ref{Num: int32(i)}}
		c.get(1, s, func() Decoded { return Decoded{Data: make([]byte, 1000)} })
	}
	for i := range c.shards {
		sh := &c.shards[i]
		if sh.bytes > c.max {
			t.Errorf("shard %d holds %d bytes, bound %d", i, sh.bytes, c.max)
		}
		n := 0
		for e := sh.lru.next; e != &sh.lru; e = e.next {
			n++
		}
		if n != len(sh.m) {
			t.Errorf("shard %d: %d in the list, %d in the map", i, n, len(sh.m))
		}
	}
	// Off: nothing is kept.
	off := NewStreamCache(-1)
	calls := 0
	for range 3 {
		off.get(1, &Stream{}, func() Decoded { calls++; return Decoded{} })
	}
	if calls != 3 {
		t.Error("a disabled cache cached")
	}
}

func TestPageContentsArray(t *testing.T) {
	objs := onePage("")
	objs[3] = "<</Type /Page /Parent 2 0 R /Contents [4 0 R 5 0 R 6 0 R] /Resources <<>>>>"
	objs[4] = stream("/Filter /FlateDecode", deflate([]byte("q")))
	objs[5] = stream("/Filter /DCTDecode", []byte("\xff\xd8junk"))
	objs[6] = stream("", []byte("Q"))
	d := mustOpen(t, file{objs: objs, trailer: "/Root 1 0 R"}.bytes())
	c, err := d.PageContents(1)
	if err != nil {
		t.Fatal(err)
	}
	if string(c.Data) != "q\nQ" || !c.Recovered || c.Filter != "DCTDecode" {
		t.Errorf("got %q recovered %v filter %s", c.Data, c.Recovered, c.Filter)
	}
	again, _ := d.PageContents(1)
	if !bytes.Equal(again.Data, c.Data) || &again.Data[0] != &c.Data[0] {
		t.Error("joined contents not cached")
	}
}

func TestPageIsBuiltOnce(t *testing.T) {
	d := mustOpen(t, file{objs: onePage("q Q"), trailer: "/Root 1 0 R"}.bytes())
	p1, _ := d.Page(1)
	if n := testing.AllocsPerRun(100, func() { _, _ = d.Page(1) }); n != 0 && !raceEnabled {
		t.Errorf("%v allocations per Page", n)
	}
	p2, _ := d.Page(1)
	if fmt.Sprint(p1) != fmt.Sprint(p2) {
		t.Error("page changed")
	}
}

// cached returns the number of streams c holds and their bytes.
func (c *StreamCache) cached() (n, bytes int) {
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.Lock()
		n += len(sh.m)
		bytes += sh.bytes
		sh.mu.Unlock()
	}
	return n, bytes
}

func TestStreamCacheShared(t *testing.T) {
	c := NewStreamCache(1 << 20)
	data := file{objs: onePage("0 0 m 1 1 l S"), trailer: "/Root 1 0 R"}.bytes()
	open := func() *Document {
		d, err := OpenWith(data, Options{StreamCache: c, StreamCacheBytes: -1})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	a, b := open(), open()
	sa, _ := mustGet(t, a, 4).Stream()
	sb, _ := mustGet(t, b, 4).Stream()
	da := a.Decode(sa).Data
	b.Decode(sb)
	if n, _ := c.cached(); n != 2 {
		t.Fatalf("%d streams of two documents cached", n)
	}
	// One document trimmed: the other keeps what it has.
	a.TrimCache()
	if n, _ := c.cached(); n != 1 {
		t.Errorf("%d streams cached after one document was trimmed", n)
	}
	if again := b.Decode(sb).Data; string(again) != string(da) {
		t.Errorf("decoded %q", again)
	}
	if again := a.Decode(sa).Data; string(again) != string(da) {
		t.Errorf("a trimmed stream decodes to %q", again)
	}
	if n, _ := c.cached(); n != 2 {
		t.Errorf("%d streams cached after the trimmed one was decoded again", n)
	}
	c.Trim()
	if n, bytes := c.cached(); n != 0 || bytes != 0 {
		t.Errorf("%d streams, %d bytes after Trim", n, bytes)
	}
	// A stream being decoded while the cache is trimmed is kept.
	trimmed := make(chan struct{})
	c.get(7, &Stream{Ref: Ref{Num: 9}}, func() Decoded {
		c.Trim()
		close(trimmed)
		return Decoded{Data: []byte("x")}
	})
	<-trimmed
	if n, bytes := c.cached(); n != 1 || bytes == 0 {
		t.Errorf("%d streams, %d bytes after a decode across Trim", n, bytes)
	}
}
