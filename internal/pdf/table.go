package pdf

import (
	"slices"
	"sync"
	"sync/atomic"
)

// MaxRefChain bounds a chain of objects whose loading needs another object:
// a stream's indirect /Length, an object stream's own entries, references
// pointing at references. A file can make that chain a cycle.
const MaxRefChain = 64

// An xentry says where object N lives and holds it once loaded.
type xentry struct {
	off  int64 // 'n': byte offset; 'o': the object stream's number
	gen  int32
	kind byte // 'n' at an offset, 'o' inside an object stream, 'f' free, 0 none
	// obj is the loaded object. It is published once, by whichever
	// goroutine finishes parsing first; all others take that one.
	obj atomic.Pointer[Object]
}

// A rawEntry is an xentry while the tables are read.
type rawEntry struct {
	off   int64
	gen   int32
	kind  byte
	table bool  // from a classic table, not a stream
	sec   int32 // the section that defined it
}

// An xrefAcc collects entries by object number while the tables are read:
// densely for the numbers files use, in a map for the few huge ones some
// files name.
type xrefAcc struct {
	dense  []rawEntry
	sparse map[int32]rawEntry
	count  int
}

func (a *xrefAcc) get(num int32) rawEntry {
	if int(num) < len(a.dense) {
		return a.dense[num]
	}
	return a.sparse[num]
}

func (a *xrefAcc) put(num int32, e rawEntry) {
	if num < 0 {
		return
	}
	if int(num) >= len(a.dense) && int64(num) <= 2*int64(a.count)+65536 {
		n := max(int(num)+1, 2*len(a.dense))
		nd := make([]rawEntry, n)
		copy(nd, a.dense)
		for k, v := range a.sparse {
			if int(k) < n {
				nd[k] = v
				delete(a.sparse, k)
			}
		}
		a.dense = nd
	}
	var old rawEntry
	if int(num) < len(a.dense) {
		old = a.dense[num]
		a.dense[num] = e
	} else {
		if a.sparse == nil {
			a.sparse = map[int32]rawEntry{}
		}
		old = a.sparse[num]
		a.sparse[num] = e
	}
	if old.kind == 0 {
		a.count++
	}
}

// A table is one reading of the file's cross-reference information: where
// every object is, the trailer, and the objects loaded through it. A
// repair builds a new table and the document swaps it in; objects already
// handed out stay valid.
type table struct {
	dense  []xentry          // object number < len(dense)
	sparse map[int32]*xentry // all others
	nums   []int32           // every object number with an entry, ascending

	trailer  Dict
	repaired bool
	// startxref is the offset of the newest cross-reference section and
	// xrefStream whether it is a stream; unset when repaired.
	startxref  int64
	xrefStream bool
	dec        *decryptor

	stmMu   sync.Mutex
	objStms map[int32]map[int32]*Object // parsed object streams

	pagesOnce sync.Once
	pages     []Ref
	pageInfo  []atomic.Pointer[pageInfo]
}

// newTable lays out the entries read from the tables.
func newTable(a *xrefAcc) *table {
	t := &table{objStms: map[int32]map[int32]*Object{}}
	n := len(a.dense)
	for n > 0 && a.dense[n-1].kind == 0 {
		n--
	}
	t.dense = make([]xentry, n)
	t.nums = make([]int32, 0, a.count)
	for i, e := range a.dense[:n] {
		if e.kind == 0 {
			continue
		}
		x := &t.dense[i]
		x.off, x.gen, x.kind = e.off, e.gen, e.kind
		t.nums = append(t.nums, int32(i))
	}
	if len(a.sparse) > 0 {
		t.sparse = make(map[int32]*xentry, len(a.sparse))
		start := len(t.nums)
		for num, e := range a.sparse {
			t.sparse[num] = &xentry{off: e.off, gen: e.gen, kind: e.kind}
			t.nums = append(t.nums, num)
		}
		slices.Sort(t.nums[start:])
	}
	return t
}

// slot returns the entry for num, creating it; only while the table is
// built.
func (t *table) slot(num int32) *xentry {
	if num >= 0 && int(num) < len(t.dense) {
		return &t.dense[num]
	}
	if t.sparse == nil {
		t.sparse = map[int32]*xentry{}
	}
	x := t.sparse[num]
	if x == nil {
		x = new(xentry)
		t.sparse[num] = x
	}
	return x
}

// entry returns the entry for num, nil when there is none.
func (t *table) entry(num int32) *xentry {
	if num >= 0 && int(num) < len(t.dense) {
		if x := &t.dense[num]; x.kind != 0 {
			return x
		}
		return nil
	}
	return t.sparse[num]
}

// add records an object found in an object stream during a repair.
func (t *table) add(num, stm int32, obj *Object) {
	x := t.slot(num)
	x.kind, x.off = 'o', int64(stm)
	x.obj.Store(obj)
	i, _ := slices.BinarySearch(t.nums, num)
	t.nums = slices.Insert(t.nums, i, num)
}

// resetCache forgets every loaded object: what was read before the file key
// was known was read wrong. Only while the table is not yet shared.
func (t *table) resetCache() {
	for i := range t.dense {
		t.dense[i].obj.Store(nil)
	}
	for _, x := range t.sparse {
		x.obj.Store(nil)
	}
	t.objStms = map[int32]map[int32]*Object{}
	t.pagesOnce = sync.Once{}
	t.pages, t.pageInfo = nil, nil
}

// A loadCtx is the chain of objects one call is loading, to cut cycles.
type loadCtx struct {
	stack [MaxRefChain]int64
	n     int
}

var ctxPool = sync.Pool{New: func() any { return new(loadCtx) }}

func getCtx() *loadCtx { return ctxPool.Get().(*loadCtx) }

func putCtx(c *loadCtx) {
	c.n = 0
	ctxPool.Put(c)
}

// enter records that key is being loaded; it fails when it already is, or
// the chain is too long.
func (c *loadCtx) enter(key int64) bool {
	if c.n == len(c.stack) {
		return false
	}
	for _, k := range c.stack[:c.n] {
		if k == key {
			return false
		}
	}
	c.stack[c.n] = key
	c.n++
	return true
}

func (c *loadCtx) leave() { c.n-- }

// Keys of a loadCtx: an object, or the parse of an object stream.
func objKey(num int32) int64 { return int64(num) }
func stmKey(num int32) int64 { return int64(num) | 1<<40 }
