// Ported from github.com/go-pdfkit/reader v0.6.0 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/reader authors); see LICENSE-go-pdfkit.
// Rewritten on prefix tables: the table costs no allocations and every
// string is written straight into the output.

package pdf

import "fmt"

// LZW code points that are not table entries.
const (
	lzwClear = 256
	lzwEOD   = 257
	lzwFirst = 258
	lzwMax   = 4096
)

// lzwDecode expands PDF's variable-code-width LZW.
//
// The standard library's compress/lzw cannot be used: PDF's /EarlyChange
// defaults to 1, which widens the code one entry before the table fills.
func lzwDecode(data []byte, early bool) ([]byte, error) {
	// Entry c is the string of entry prefix[c] followed by suffix[c]; it is
	// length[c] bytes long and starts with first[c].
	var (
		prefix [lzwMax]uint16
		suffix [lzwMax]byte
		length [lzwMax]uint16
		first  [lzwMax]byte
	)
	for i := range 256 {
		suffix[i], first[i], length[i] = byte(i), byte(i), 1
	}
	next, width := lzwFirst, 9
	prev := -1
	out := make([]byte, 0, min(3*len(data)+16, MaxStreamBytes))
	nbits := len(data) * 8
	for bit := 0; ; {
		if bit+width > nbits {
			// A truncated stream keeps what it managed to say.
			return out, nil
		}
		code := 0
		for k := 0; k < width; k++ {
			code = code<<1 | int(data[(bit+k)>>3]>>(7-(bit+k)&7)&1)
		}
		bit += width

		switch code {
		case lzwEOD:
			return out, nil
		case lzwClear:
			next, width, prev = lzwFirst, 9, -1
			continue
		}

		var head byte
		switch {
		case code < next:
			n := int(length[code])
			base := len(out)
			out = grow(out, n)
			c := code
			for i := n - 1; i >= 0; i-- {
				out[base+i] = suffix[c]
				c = int(prefix[c])
			}
			head = first[code]
		case code == next && prev >= 0:
			// The encoder may name the entry it is about to define: the
			// previous string and its own first byte.
			n := int(length[prev]) + 1
			base := len(out)
			out = grow(out, n)
			out[base+n-1] = first[prev]
			c := prev
			for i := n - 2; i >= 0; i-- {
				out[base+i] = suffix[c]
				c = int(prefix[c])
			}
			head = first[prev]
		default:
			return out, fmt.Errorf("pdf: LZWDecode: code %d is not in the table", code)
		}
		if len(out) > MaxStreamBytes {
			return nil, ErrTooLarge
		}

		if prev >= 0 && next < lzwMax {
			prefix[next] = uint16(prev)
			suffix[next] = head
			length[next] = length[prev] + 1
			first[next] = first[prev]
			next++
		}
		prev = code

		// With EarlyChange the width grows one entry sooner.
		count := next
		if early {
			count++
		}
		switch {
		case count >= 2048:
			width = 12
		case count >= 1024:
			width = 11
		case count >= 512:
			width = 10
		default:
			width = 9
		}
	}
}

// grow extends b by n bytes.
func grow(b []byte, n int) []byte {
	if len(b)+n > cap(b) {
		nb := make([]byte, len(b), 2*cap(b)+n)
		copy(nb, b)
		b = nb
	}
	return b[:len(b)+n]
}
