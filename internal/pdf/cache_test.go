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
	var c streamCache
	c.init(1 << 20)
	s := &Stream{Ref: Ref{Num: 3}}
	var calls atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.get(s, func() Decoded {
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
	var c streamCache
	c.init(cacheShards * 10_000)
	for i := range 1000 {
		s := &Stream{Ref: Ref{Num: int32(i)}}
		c.get(s, func() Decoded { return Decoded{Data: make([]byte, 1000)} })
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
	var off streamCache
	calls := 0
	for range 3 {
		off.get(&Stream{}, func() Decoded { calls++; return Decoded{} })
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
