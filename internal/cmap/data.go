package cmap

import (
	"bytes"
	"compress/flate"
	_ "embed"
	"encoding/binary"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
)

// The predefined CMaps of PDF 2.0 Table 116 and the CID-to-Unicode tables
// of their character collections are compiled by ./gen from Adobe's CMap
// resources (https://github.com/adobe-type-tools/cmap-resources, BSD
// licence, see LICENSE-cmap-resources) into data.bin: an index of entries,
// each deflated on its own and inflated on first use. Run
//
//	go run ./internal/cmap/gen -src path/to/cmap-resources
//
// to rebuild it.

//go:embed data.bin
var dataBin []byte

const magic = "CMAP1"

type entry struct {
	off, n int // deflated bytes in dataBin
}

var (
	indexOnce sync.Once
	index     map[string]entry

	cacheMu sync.Mutex
	cmaps   map[string]*CMap
	ucs     map[string][]run
)

func readIndex() {
	index = map[string]entry{}
	if !bytes.HasPrefix(dataBin, []byte(magic)) {
		return
	}
	r := &reader{b: dataBin[len(magic):]}
	count := r.uvarint()
	type pending struct {
		name string
		n    int
	}
	var list []pending
	for i := uint64(0); i < count && r.err == nil; i++ {
		name := r.str()
		list = append(list, pending{name, int(r.uvarint())})
	}
	off := len(dataBin) - len(r.b)
	for _, p := range list {
		if r.err != nil || off+p.n > len(dataBin) {
			return
		}
		index[p.name] = entry{off, p.n}
		off += p.n
	}
}

func inflate(name string) []byte {
	indexOnce.Do(readIndex)
	e, ok := index[name]
	if !ok {
		return nil
	}
	b, err := io.ReadAll(flate.NewReader(bytes.NewReader(dataBin[e.off : e.off+e.n])))
	if err != nil {
		return nil
	}
	return b
}

// Predefined returns the predefined CMap of that name (Identity-H and
// Identity-V included), nil if there is none.
func Predefined(name string) *CMap {
	switch name {
	case "Identity-H":
		return identityH
	case "Identity-V":
		return identityV
	}
	return predefined(name, 0)
}

func predefined(name string, depth int) *CMap {
	cacheMu.Lock()
	c, ok := cmaps[name]
	cacheMu.Unlock()
	if ok {
		return c
	}
	if depth > maxDepth {
		return nil
	}
	b := inflate(name)
	if b != nil {
		var parent string
		c, parent = unmarshal(b)
		if c != nil {
			c.Name = name
			if parent != "" {
				c.parent = predefined(parent, depth+1)
			}
		}
	}
	cacheMu.Lock()
	if cmaps == nil {
		cmaps = map[string]*CMap{}
	}
	if d, ok := cmaps[name]; ok {
		c = d
	} else {
		cmaps[name] = c
	}
	cacheMu.Unlock()
	return c
}

// run maps the CIDs cid..cid+n-1 to the code points uni..uni+n-1.
type run struct {
	cid, uni, n uint32
}

// ToUnicode returns the character a CID of a character collection stands
// for: Adobe's CJK collections (ordering Japan1, GB1, CNS1, Korea1), and
// Identity for nothing. ok is false when the collection is unknown or does
// not map the CID.
func ToUnicode(ordering string, cid int) (rune, bool) {
	runs := unicodeRuns(ordering)
	if cid < 0 || len(runs) == 0 {
		return 0, false
	}
	c := uint32(cid)
	i := sort.Search(len(runs), func(i int) bool { return runs[i].cid+runs[i].n > c })
	if i < len(runs) && runs[i].cid <= c {
		return rune(runs[i].uni + (c - runs[i].cid)), true
	}
	return 0, false
}

// Collection names a character collection by its ordering ("Japan1"):
// the one ToUnicode knows, folding Adobe-KR into Korea1's slot for nothing.
func Collection(ordering string) string {
	switch strings.ToLower(ordering) {
	case "japan1":
		return "Japan1"
	case "gb1":
		return "GB1"
	case "cns1":
		return "CNS1"
	case "korea1":
		return "Korea1"
	}
	return ""
}

func unicodeRuns(ordering string) []run {
	ordering = Collection(ordering)
	if ordering == "" {
		return nil
	}
	cacheMu.Lock()
	runs, ok := ucs[ordering]
	cacheMu.Unlock()
	if ok {
		return runs
	}
	if b := inflate("ucs:" + ordering); b != nil {
		r := &reader{b: b}
		n := r.uvarint()
		var cid, uni uint32
		for i := uint64(0); i < n && r.err == nil; i++ {
			cid += uint32(r.uvarint())
			uni = uint32(int64(uni) + r.varint())
			k := uint32(r.uvarint()) + 1
			runs = append(runs, run{cid, uni, k})
			cid += k
			uni += k
		}
		if r.err != nil {
			runs = nil
		}
	}
	cacheMu.Lock()
	if ucs == nil {
		ucs = map[string][]run{}
	}
	ucs[ordering] = runs
	cacheMu.Unlock()
	return runs
}

// The encoding of one CMap, before deflating:
//
//	wmode, registry, ordering, supplement, usecmap name
//	codespace ranges: count, then n, lo[n], hi[n] each
//	cid ranges, notdef ranges: count, then per span
//	  n, lo (delta from the previous hi+1 of the same n, else absolute),
//	  hi-lo, cid (signed delta from the previous span's next CID)
//
// with unsigned and signed varints and length-prefixed strings.

// Marshal encodes a CMap for data.bin; its usecmap parent is written by
// name.
func Marshal(c *CMap) []byte {
	var w writer
	w.uvarint(uint64(c.WMode))
	w.str(c.Registry)
	w.str(c.Ordering)
	w.uvarint(uint64(c.Supplement))
	parent := ""
	if c.parent != nil {
		parent = c.parent.Name
	}
	w.str(parent)
	w.uvarint(uint64(len(c.spaces)))
	for _, sp := range c.spaces {
		w.b = append(w.b, sp.n)
		w.b = append(w.b, sp.lo[:sp.n]...)
		w.b = append(w.b, sp.hi[:sp.n]...)
	}
	for _, list := range [2][]span{c.ranges, c.notdef} {
		w.uvarint(uint64(len(list)))
		var prevN uint8
		var next, nextCID uint32
		for _, s := range list {
			w.b = append(w.b, s.n)
			if s.n != prevN {
				next = 0
			}
			w.uvarint(uint64(s.lo - next))
			w.uvarint(uint64(s.hi - s.lo))
			w.varint(int64(s.cid) - int64(nextCID))
			prevN, next = s.n, s.hi+1
			nextCID = s.cid + (s.hi-s.lo+1)*uint32(s.step)
		}
	}
	return w.b
}

// MarshalUnicode encodes a CID-to-Unicode table: runs of consecutive CIDs
// and code points, from m (CID to code point).
func MarshalUnicode(m map[uint32]rune) []byte {
	cids := make([]uint32, 0, len(m))
	for cid := range m {
		cids = append(cids, cid)
	}
	sort.Slice(cids, func(i, j int) bool { return cids[i] < cids[j] })
	var runs []run
	for _, cid := range cids {
		u := uint32(m[cid])
		if l := len(runs) - 1; l >= 0 && runs[l].cid+runs[l].n == cid && runs[l].uni+runs[l].n == u {
			runs[l].n++
			continue
		}
		runs = append(runs, run{cid, u, 1})
	}
	var w writer
	w.uvarint(uint64(len(runs)))
	var cid, uni uint32
	for _, r := range runs {
		w.uvarint(uint64(r.cid - cid))
		w.varint(int64(r.uni) - int64(uni))
		w.uvarint(uint64(r.n - 1))
		cid, uni = r.cid+r.n, r.uni+r.n
	}
	return w.b
}

// Bundle writes data.bin from named entries, deflating each.
func Bundle(entries map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	var head writer
	head.b = append(head.b, magic...)
	head.uvarint(uint64(len(names)))
	var body bytes.Buffer
	for _, n := range names {
		var z bytes.Buffer
		fw, err := flate.NewWriter(&z, flate.BestCompression)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(entries[n]); err != nil {
			return nil, err
		}
		if err := fw.Close(); err != nil {
			return nil, err
		}
		head.str(n)
		head.uvarint(uint64(z.Len()))
		body.Write(z.Bytes())
	}
	return append(head.b, body.Bytes()...), nil
}

func unmarshal(b []byte) (*CMap, string) {
	r := &reader{b: b}
	c := &CMap{}
	c.WMode = int(r.uvarint()) & 1
	c.Registry = r.str()
	c.Ordering = r.str()
	c.Supplement = int(r.uvarint())
	parent := r.str()
	ns := r.uvarint()
	for i := uint64(0); i < ns && r.err == nil && i < maxSpaces; i++ {
		var sp space
		sp.n = r.byte()
		if sp.n < 1 || sp.n > 4 {
			r.err = errCorrupt
			break
		}
		copy(sp.lo[:], r.bytes(int(sp.n)))
		copy(sp.hi[:], r.bytes(int(sp.n)))
		c.spaces = append(c.spaces, sp)
	}
	for k := range 2 {
		count := r.uvarint()
		if count > maxSpans {
			r.err = errCorrupt
		}
		var list []span
		var prevN uint8
		var next, nextCID uint32
		step := uint8(1)
		if k == 1 {
			step = 0
		}
		for i := uint64(0); i < count && r.err == nil; i++ {
			n := r.byte()
			if n != prevN {
				next = 0
			}
			lo := next + uint32(r.uvarint())
			hi := lo + uint32(r.uvarint())
			cid := uint32(int64(nextCID) + r.varint())
			list = append(list, span{lo: lo, hi: hi, cid: cid, n: n, step: step})
			prevN, next = n, hi+1
			nextCID = cid + (hi-lo+1)*uint32(step)
		}
		if k == 0 {
			c.ranges = list
		} else {
			c.notdef = list
		}
	}
	if r.err != nil {
		return nil, ""
	}
	return c, parent
}

var errCorrupt = errors.New("cmap: corrupt data")

type writer struct{ b []byte }

func (w *writer) uvarint(v uint64) { w.b = binary.AppendUvarint(w.b, v) }
func (w *writer) varint(v int64)   { w.b = binary.AppendVarint(w.b, v) }
func (w *writer) str(s string) {
	w.uvarint(uint64(len(s)))
	w.b = append(w.b, s...)
}

type reader struct {
	b   []byte
	err error
}

func (r *reader) uvarint() uint64 {
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.err = errCorrupt
		r.b = nil
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) varint() int64 {
	v, n := binary.Varint(r.b)
	if n <= 0 {
		r.err = errCorrupt
		r.b = nil
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) byte() byte {
	if len(r.b) == 0 {
		r.err = errCorrupt
		return 0
	}
	c := r.b[0]
	r.b = r.b[1:]
	return c
}

func (r *reader) bytes(n int) []byte {
	if n > len(r.b) {
		r.err = errCorrupt
		r.b = nil
		return nil
	}
	b := r.b[:n]
	r.b = r.b[n:]
	return b
}

func (r *reader) str() string {
	n := r.uvarint()
	if n > uint64(len(r.b)) {
		r.err = errCorrupt
		return ""
	}
	return string(r.bytes(int(n)))
}
