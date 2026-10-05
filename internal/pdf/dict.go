package pdf

import (
	"iter"
	"slices"
	"strings"
)

// An Entry is one key and value of a dictionary.
type Entry struct {
	Key Name
	Val Object
}

// A Dict is a PDF dictionary: a flat list of entries, unique by key. Small
// dictionaries are searched in order; those with more than linearMax
// entries are kept sorted by key and searched by halving.
//
// The zero Dict is no dictionary at all ([Dict.IsZero]); an empty
// dictionary written in a file is a Dict with no entries.
type Dict struct{ e []Entry }

// linearMax is the largest dictionary searched in order. Above it the
// entries are sorted.
const linearMax = 12

// NewDict returns a dictionary of the entries, which it may reorder. A key
// given twice keeps its last value.
func NewDict(entries ...Entry) Dict {
	if entries == nil {
		entries = emptyEntries[:0]
	}
	return Dict{normalise(entries)}
}

// normalise removes duplicate keys, the last value winning, and sorts a
// dictionary too large to search in order. It works in place.
func normalise(e []Entry) []Entry {
	if len(e) <= linearMax {
		for i := len(e) - 1; i > 0; i-- {
			for j := i - 1; j >= 0; j-- {
				if e[j].Key == e[i].Key {
					// e[i] is later and wins: drop e[j].
					e = slices.Delete(e, j, j+1)
					i--
				}
			}
		}
		return e
	}
	slices.SortStableFunc(e, func(a, b Entry) int { return strings.Compare(string(a.Key), string(b.Key)) })
	out := e[:0]
	for i := range e {
		if i+1 < len(e) && e[i+1].Key == e[i].Key {
			continue
		}
		out = append(out, e[i])
	}
	return out
}

// IsZero reports whether d is no dictionary at all.
func (d Dict) IsZero() bool { return d.e == nil }

// Len returns the number of entries.
func (d Dict) Len() int { return len(d.e) }

// Get returns the value of key k, or [Null] when it is absent. The value
// may be a reference; [Document.Resolve] follows it.
func (d Dict) Get(k Name) Object {
	if i := d.index(string(k)); i >= 0 {
		return d.e[i].Val
	}
	return Null
}

// Has reports whether the dictionary has an entry for k, even a null one.
func (d Dict) Has(k Name) bool { return d.index(string(k)) >= 0 }

// GetBytes is Get with the key as bytes, which it does not convert.
func (d Dict) GetBytes(k []byte) Object {
	var i int
	if len(d.e) <= linearMax {
		i = -1
		for j := range d.e {
			if string(d.e[j].Key) == string(k) {
				i = j
				break
			}
		}
	} else {
		i = d.search(string(k))
	}
	if i >= 0 {
		return d.e[i].Val
	}
	return Null
}

func (d Dict) index(k string) int {
	if len(d.e) <= linearMax {
		for i := range d.e {
			if string(d.e[i].Key) == k {
				return i
			}
		}
		return -1
	}
	return d.search(k)
}

func (d Dict) search(k string) int {
	lo, hi := 0, len(d.e)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if string(d.e[m].Key) < k {
			lo = m + 1
		} else {
			hi = m
		}
	}
	if lo < len(d.e) && string(d.e[lo].Key) == k {
		return lo
	}
	return -1
}

// Entries returns the entries. They must not be written to.
func (d Dict) Entries() []Entry { return d.e[:len(d.e):len(d.e)] }

// All iterates over the keys and values.
func (d Dict) All() iter.Seq2[Name, Object] {
	return func(yield func(Name, Object) bool) {
		for _, e := range d.e {
			if !yield(e.Key, e.Val) {
				return
			}
		}
	}
}

// With returns a copy of d with key k set to v.
func (d Dict) With(k Name, v Object) Dict {
	e := make([]Entry, len(d.e), len(d.e)+1)
	copy(e, d.e)
	return Dict{normalise(append(e, Entry{k, v}))}
}

// String writes the dictionary roughly as it would appear in a file.
func (d Dict) String() string {
	var b strings.Builder
	b.WriteString("<<")
	for i, e := range d.e {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString("/" + string(e.Key) + " " + e.Val.String())
	}
	b.WriteString(">>")
	return b.String()
}
