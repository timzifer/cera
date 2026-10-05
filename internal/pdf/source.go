package pdf

import (
	"bytes"
	"io"
)

// MaxObjectWindow bounds how much of a file is read to parse one object
// when the file is read through an io.ReaderAt: an object, or a stream
// whose /Length is wrong, that does not end within it is not read.
const MaxObjectWindow = 64 << 20

// firstWindow is how much of a file read on demand is read first to parse
// one object, and scanChunk how much at a time to scan it; tests lower
// both to exercise the growing windows.
var (
	firstWindow = 4096
	scanChunk   = 4 << 20
)

// A source is the file: in memory, or read on demand.
type source interface {
	size() int64
	// window returns the file from off on, at least n bytes of it unless
	// the file ends first; mem says the slice aliases the whole file from
	// off to its end, so it never needs to grow.
	window(off int64, n int) (b []byte, mem bool)
}

// memSource is a file held in memory. Every window is the rest of the file.
type memSource []byte

func (m memSource) size() int64 { return int64(len(m)) }

func (m memSource) window(off int64, _ int) ([]byte, bool) {
	if off < 0 || off > int64(len(m)) {
		return nil, true
	}
	return m[off:], true
}

// readerSource reads windows of a file through an io.ReaderAt.
type readerSource struct {
	r io.ReaderAt
	n int64
}

func (s readerSource) size() int64 { return s.n }

func (s readerSource) window(off int64, n int) ([]byte, bool) {
	if off < 0 || off >= s.n {
		return nil, false
	}
	n = int(min(int64(n), s.n-off))
	b := make([]byte, n)
	k, _ := s.r.ReadAt(b, off)
	return b[:k], false
}

// whole reports whether a window reaches the end of the file.
func whole(src source, off int64, b []byte, mem bool) bool {
	return mem || off+int64(len(b)) >= src.size()
}

// scanChunks calls f on overlapping chunks of the whole file, in order;
// each chunk starts overlap bytes before the end of the previous one, so a
// keyword or a short header across a boundary is seen whole in one. f gets
// the chunk and its offset.
func scanChunks(src source, overlap int, f func(b []byte, off int64)) {
	if m, ok := src.(memSource); ok {
		f(m, 0)
		return
	}
	chunk := max(scanChunk, 4*overlap)
	for off := int64(0); off < src.size(); off += int64(chunk - overlap) {
		b, _ := src.window(off, chunk)
		f(b, off)
		if off+int64(len(b)) >= src.size() {
			return
		}
	}
}

// containsAnywhere reports whether the file holds sep.
func containsAnywhere(src source, sep []byte) bool {
	found := false
	scanChunks(src, len(sep), func(b []byte, _ int64) {
		if !found && bytes.Contains(b, sep) {
			found = true
		}
	})
	return found
}
