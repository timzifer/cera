package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// A value is a PDF object in a form both readers convert to, so the text
// written for it does not depend on which reader produced it. Integers and
// integral reals are both numbers: readers may disagree on how a number was
// written without disagreeing on what it is.
type value struct {
	kind   string // null, bool, num, str, name, ref, array, dict, stream
	num    float64
	bool   bool
	str    []byte // str, name
	ref    [2]int // num, gen
	elems  []value
	keys   []string // dict and stream, sorted
	vals   []value
	stream *streamInfo
}

// streamInfo is what a reader made of a stream's bytes: the decrypted, still
// filtered data and the result of decoding it with recovery.
type streamInfo struct {
	raw       []byte
	data      []byte
	undecoded []byte
	image     string
	filter    string
	recovered bool
	failed    bool // decoding returned a cause
}

// text writes v in a canonical form. Streams write their dictionary and a
// hash of the raw bytes; what decoding made of them is a separate entry.
func (v value) text() string {
	var b strings.Builder
	v.write(&b)
	return b.String()
}

func (v value) write(b *strings.Builder) {
	switch v.kind {
	case "null":
		b.WriteString("null")
	case "bool":
		b.WriteString(strconv.FormatBool(v.bool))
	case "num":
		b.WriteString(strconv.FormatFloat(v.num, 'g', -1, 64))
	case "str":
		b.WriteString("<" + hex.EncodeToString(v.str) + ">")
	case "name":
		b.WriteString("/" + string(v.str))
	case "ref":
		fmt.Fprintf(b, "%d %d R", v.ref[0], v.ref[1])
	case "array":
		b.WriteByte('[')
		for i, e := range v.elems {
			if i > 0 {
				b.WriteByte(' ')
			}
			e.write(b)
		}
		b.WriteByte(']')
	case "dict", "stream":
		b.WriteString("<<")
		for i, k := range v.keys {
			b.WriteString("/" + k + " ")
			v.vals[i].write(b)
			if i < len(v.keys)-1 {
				b.WriteByte(' ')
			}
		}
		b.WriteString(">>")
		if v.stream != nil {
			fmt.Fprintf(b, " stream %d %s", len(v.stream.raw), hash(v.stream.raw))
		}
	default:
		b.WriteString("?" + v.kind)
	}
}

// decodedText describes what decoding made of a stream.
func (s *streamInfo) decodedText() string {
	return fmt.Sprintf("data %d %s undecoded %d %s image=%s filter=%s recovered=%t failed=%t",
		len(s.data), hash(s.data), len(s.undecoded), hash(s.undecoded),
		s.image, s.filter, s.recovered, s.failed)
}

// sortDict orders a dictionary's keys and values by key and drops null
// values, which the PDF format treats as absent entries.
func sortDict(keys []string, vals []value) ([]string, []value) {
	idx := make([]int, 0, len(keys))
	for i := range keys {
		if vals[i].kind != "null" {
			idx = append(idx, i)
		}
	}
	slices.SortFunc(idx, func(a, b int) int { return strings.Compare(keys[a], keys[b]) })
	k := make([]string, len(idx))
	v := make([]value, len(idx))
	for i, j := range idx {
		k[i], v[i] = keys[j], vals[j]
	}
	return k, v
}

func hash(b []byte) string {
	if len(b) == 0 {
		return "-"
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// A snapshot is everything one reader made of one file, keyed so two
// snapshots can be compared entry by entry:
//
//	open             "ok" or the error
//	version          header version
//	repaired         whether the cross-reference table was rebuilt
//	trailer          the trailer dictionary
//	pages            page count
//	page/<i>/ref     reference of page i
//	page/<i>         page i with inherited attributes
//	obj/<n> <g>      every object reachable from the trailer and the pages
//	decoded/<n> <g>  what decoding made of a stream object
type snapshot struct {
	keys    []string
	entries map[string]string
}

func (s *snapshot) set(k, v string) {
	if s.entries == nil {
		s.entries = map[string]string{}
	}
	if _, ok := s.entries[k]; !ok {
		s.keys = append(s.keys, k)
	}
	s.entries[k] = v
}

// A backend reads one file into a snapshot.
type backend interface {
	name() string
	snapshot(data []byte, password string) *snapshot
}

var backends = map[string]backend{}

func register(b backend) { backends[b.name()] = b }

// A walker visits every object reachable from the trailer and the pages,
// in a deterministic order, through a reader-specific resolve function.
type walker struct {
	snap    *snapshot
	seen    map[[2]int]bool
	pending [][2]int
	resolve func(ref [2]int) (value, error)
}

func (w *walker) visit(v value) {
	switch v.kind {
	case "ref":
		if !w.seen[v.ref] {
			w.seen[v.ref] = true
			w.pending = append(w.pending, v.ref)
		}
	case "array":
		for _, e := range v.elems {
			w.visit(e)
		}
	case "dict", "stream":
		for _, e := range v.vals {
			w.visit(e)
		}
	}
}

// run resolves pending references until none are left, recording each.
func (w *walker) run() {
	for len(w.pending) > 0 {
		r := w.pending[0]
		w.pending = w.pending[1:]
		key := fmt.Sprintf("obj/%d %d", r[0], r[1])
		v, err := w.resolve(r)
		if err != nil {
			w.snap.set(key, "error")
			continue
		}
		w.snap.set(key, v.text())
		if v.stream != nil {
			w.snap.set(fmt.Sprintf("decoded/%d %d", r[0], r[1]), v.stream.decodedText())
		}
		w.visit(v)
	}
}
