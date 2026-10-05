// Ported from github.com/go-pdfkit/reader v0.6.0 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/reader authors); see LICENSE-go-pdfkit.
// Changed: every filter is capped by MaxStreamBytes, Flate reuses its
// decompressors and ignores the Adler-32 checksum (as pdf.js and MuPDF do).

package pdf

import (
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/timzifer/cera/internal/pdfsyntax"
)

// MaxStreamBytes caps what one filter may expand a stream to. A few hundred
// bytes of Flate can name gigabytes, and a reader that runs in a browser tab
// must refuse rather than fill the heap. Tests lower it.
var MaxStreamBytes = 256 << 20

// MaxFilters bounds the length of a filter chain.
const MaxFilters = 8

// ErrTooLarge is the cause of a decode that would exceed MaxStreamBytes.
var ErrTooLarge = errors.New("pdf: decoded stream exceeds the size limit")

// ImageFilter reports whether a filter yields an encoded image rather than a
// byte stream. Decoding stops at one of these and hands over the still
// encoded bytes, because decoding them is an image decoder's job.
func ImageFilter(n Name) bool {
	switch n {
	case "DCTDecode", "DCT", "JPXDecode", "JBIG2Decode":
		return true
	}
	return false
}

// A Decoded is the outcome of applying a stream's filter chain, including
// the outcome of a chain that could not be finished. Its slices may be
// shared with the document's cache and must not be written to.
type Decoded struct {
	// Data is what the chain decoded: never bytes no filter has decoded,
	// except the still encoded bytes of an image filter, which Image names.
	Data []byte
	// Undecoded holds the bytes the chain could not get past, still in the
	// encoding Filter names, when the filter that failed produced nothing.
	Undecoded []byte
	// Image names the image filter the chain stopped at.
	Image Name
	// Recovered says the chain could not be run to the end.
	Recovered bool
	// Cause says why the chain stopped; set exactly when Recovered is.
	Cause error
	// Filter names the filter that could not be applied.
	Filter Name
}

// decodeChain applies a stream dictionary's filter chain to raw and never
// fails: a filter that cannot be applied ends the chain, and what the
// filters before it produced is returned with Recovered set. resolve follows
// indirect references.
func decodeChain(d Dict, raw []byte, resolve func(Object) Object) Decoded {
	filters, parms, err := filterChain(d, resolve)
	if err != nil {
		return Decoded{Undecoded: raw, Recovered: true, Cause: err}
	}
	data := raw
	for i, f := range filters {
		if ImageFilter(f) {
			return Decoded{Data: data, Image: f}
		}
		out, err := applyFilter(f, data, parms[i], resolve)
		if err != nil {
			if len(out) == 0 {
				return Decoded{Undecoded: data, Recovered: true, Cause: err, Filter: f}
			}
			return Decoded{Data: out, Recovered: true, Cause: err, Filter: f}
		}
		data = out
	}
	return Decoded{Data: data}
}

// DecodeRecovering applies a filter chain to raw with the abbreviations
// inline images use; references in it resolve to null.
func DecodeRecovering(d Dict, raw []byte) Decoded {
	return decodeChain(d, raw, func(o Object) Object {
		if o.kind == KindRef {
			return Null
		}
		return o
	})
}

// filterChain reads /Filter and /DecodeParms, each of which may be a single
// value or an array, and returns them aligned.
func filterChain(d Dict, resolve func(Object) Object) ([]Name, []Dict, error) {
	fo := resolve(d.Get("Filter"))
	var names []Name
	switch fo.kind {
	case KindNull:
	case KindName:
		n, _ := fo.Name()
		names = []Name{n}
	case KindArray:
		a, _ := fo.Array()
		for _, e := range a {
			n, ok := resolve(e).Name()
			if !ok {
				return nil, nil, fmt.Errorf("pdf: /Filter array holds a %s, not a name", resolve(e).kind)
			}
			names = append(names, n)
		}
	default:
		return nil, nil, fmt.Errorf("pdf: /Filter is a %s, not a name or an array", fo.kind)
	}
	if len(names) > MaxFilters {
		return nil, nil, fmt.Errorf("pdf: a chain of %d filters", len(names))
	}
	parms := make([]Dict, len(names))
	po := resolve(d.Get("DecodeParms"))
	switch po.kind {
	case KindNull:
	case KindDict:
		if len(parms) > 0 {
			parms[0], _ = po.Dict()
		}
	case KindArray:
		a, _ := po.Array()
		for i, e := range a {
			if i >= len(parms) {
				break
			}
			if pd, ok := resolve(e).Dict(); ok {
				parms[i] = pd
			}
		}
	default:
		return nil, nil, fmt.Errorf("pdf: /DecodeParms is a %s, not a dictionary or an array", po.kind)
	}
	return names, parms, nil
}

// applyFilter runs one filter. The abbreviated names are the ones inline
// images use; regular streams may legally carry them too.
func applyFilter(f Name, data []byte, parm Dict, resolve func(Object) Object) ([]byte, error) {
	switch f {
	case "FlateDecode", "Fl":
		out, err := flateDecode(data, sizeHint(parm, len(data), resolve))
		if err != nil {
			return salvage(out, err, parm, resolve)
		}
		return applyPredictor(out, parm, resolve)
	case "LZWDecode", "LZW":
		early := intParm(parm, "EarlyChange", 1, resolve)
		out, err := lzwDecode(data, early != 0)
		if err != nil {
			return salvage(out, err, parm, resolve)
		}
		return applyPredictor(out, parm, resolve)
	case "ASCIIHexDecode", "AHx":
		return asciiHexDecode(data)
	case "ASCII85Decode", "A85":
		return ascii85Decode(data)
	case "RunLengthDecode", "RL":
		return runLengthDecode(data)
	case "CCITTFaxDecode", "CCF":
		return ccittDecode(data, ccittParamsOf(parm, resolve))
	case "Crypt":
		return cryptFilter(data, parm, resolve)
	}
	return nil, fmt.Errorf("pdf: unsupported filter /%s", f)
}

// cryptFilter applies the /Crypt filter, which transforms nothing: the
// document has dealt with encryption before the chain runs. A /Name other
// than /Identity is reported, because the bytes would be ciphertext.
func cryptFilter(data []byte, parm Dict, resolve func(Object) Object) ([]byte, error) {
	if n, ok := resolve(parm.Get("Name")).Name(); ok && n != "Identity" {
		return nil, fmt.Errorf("pdf: /Crypt filter names /%s, which this reader cannot apply", n)
	}
	return data, nil
}

// salvage finishes a filter that stopped part-way: the prefix it produced
// is still worth having, and the predictor still applies to it.
func salvage(out []byte, err error, parm Dict, resolve func(Object) Object) ([]byte, error) {
	if len(out) == 0 {
		return nil, err
	}
	if p, perr := applyPredictor(out, parm, resolve); perr == nil {
		return p, err
	}
	return out, err
}

// intParm reads an integer decode parameter, falling back to its default.
func intParm(parm Dict, key Name, def int, resolve func(Object) Object) int {
	if parm.IsZero() {
		return def
	}
	n, ok := resolve(parm.Get(key)).Int()
	if !ok {
		return def
	}
	return int(n)
}

// sizeHint guesses what a Flate stream inflates to, for the first
// allocation of its output.
func sizeHint(parm Dict, n int, resolve func(Object) Object) int {
	h := 4 * n
	return min(max(h, 512), 16<<20)
}

// flateDecoders are reused: a Flate decompressor's tables cost tens of
// kilobytes to allocate.
var flateDecoders sync.Pool

type inflater struct {
	r  io.ReadCloser
	br bytes.Reader
}

// inflate decompresses raw deflate data.
func inflate(data []byte, hint int) ([]byte, error) {
	f, _ := flateDecoders.Get().(*inflater)
	if f == nil {
		f = &inflater{}
		f.br.Reset(data)
		f.r = flate.NewReader(&f.br)
	} else {
		f.br.Reset(data)
		f.r.(flate.Resetter).Reset(&f.br, nil)
	}
	out, err := readCapped(f.r, hint)
	f.br.Reset(nil)
	flateDecoders.Put(f)
	return out, err
}

// readCapped reads r, refusing to grow past MaxStreamBytes.
func readCapped(r io.Reader, hint int) ([]byte, error) {
	out := make([]byte, 0, min(hint, MaxStreamBytes+1))
	for {
		if len(out) == cap(out) {
			out = append(out, 0)[:len(out)]
		}
		n, err := r.Read(out[len(out):cap(out)])
		out = out[:len(out)+n]
		if len(out) > MaxStreamBytes {
			return nil, ErrTooLarge
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// zlibHeader reports whether data starts with a zlib header this reader
// can use: deflate, a window it allows, a correct check and no preset
// dictionary.
func zlibHeader(data []byte) bool {
	if len(data) < 2 {
		return false
	}
	cmf, flg := data[0], data[1]
	return cmf&0x0f == 8 && cmf>>4 <= 7 && (uint16(cmf)<<8|uint16(flg))%31 == 0 && flg&0x20 == 0
}

// flateDecode inflates a stream. Producers emit both zlib-wrapped and bare
// deflate data, sometimes with leading white-space, and truncate the last
// stream in a damaged file. A prefix comes back with the error that ended
// it. The Adler-32 checksum after zlib data is not checked: producers get
// it wrong, and the data before it is what they meant.
func flateDecode(data []byte, hint int) ([]byte, error) {
	i := 0
	for i < len(data) && isSpace(data[i]) {
		i++
	}
	data = data[i:]
	if zlibHeader(data) {
		out, err := inflate(data[2:], hint)
		if err == nil {
			return out, nil
		}
		if len(out) > 0 || errors.Is(err, ErrTooLarge) {
			return out, fmt.Errorf("pdf: FlateDecode: %w", err)
		}
	}
	out, err := inflate(data, hint)
	if err != nil {
		return out, fmt.Errorf("pdf: FlateDecode: %w", err)
	}
	return out, nil
}

// asciiHexDecode reads hexadecimal digits up to the '>' terminator, padding
// a final odd digit with zero.
func asciiHexDecode(data []byte) ([]byte, error) {
	out := make([]byte, 0, len(data)/2)
	hi := -1
	for _, c := range data {
		if c == '>' {
			break
		}
		if isSpace(c) {
			continue
		}
		v := pdfsyntax.HexVal(c)
		if v < 0 {
			return out, fmt.Errorf("pdf: ASCIIHexDecode: invalid digit %q", rune(c))
		}
		if hi < 0 {
			hi = v
			continue
		}
		out = append(out, byte(hi<<4|v))
		hi = -1
	}
	if hi >= 0 {
		out = append(out, byte(hi<<4))
	}
	return out, nil
}

// ascii85Decode reads base-85 groups up to the "~>" terminator.
func ascii85Decode(data []byte) ([]byte, error) {
	if bytes.HasPrefix(data, []byte("<~")) {
		data = data[2:]
	}
	out := make([]byte, 0, len(data)*4/5+4)
	var group [5]byte
	n := 0
	for _, c := range data {
		if isSpace(c) {
			continue
		}
		if c == '~' {
			break
		}
		if c == 'z' && n == 0 {
			out = append(out, 0, 0, 0, 0)
			continue
		}
		if c < '!' || c > 'u' {
			return out, fmt.Errorf("pdf: ASCII85Decode: invalid character %q", rune(c))
		}
		group[n] = c
		n++
		if n == 5 {
			v := uint64(0)
			for _, g := range group {
				v = v*85 + uint64(g-'!')
			}
			if v > 0xFFFFFFFF {
				return out, fmt.Errorf("pdf: ASCII85Decode: group overflows 32 bits")
			}
			out = append(out, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
			n = 0
		}
	}
	if n == 1 {
		return out, fmt.Errorf("pdf: ASCII85Decode: truncated final group")
	}
	if n > 1 {
		for i := n; i < 5; i++ {
			group[i] = 'u'
		}
		v := uint32(0)
		for _, g := range group {
			v = v*85 + uint32(g-'!')
		}
		b := [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		out = append(out, b[:n-1]...)
	}
	return out, nil
}

// runLengthDecode expands the byte-oriented run-length encoding: a length
// byte below 128 introduces that many literal bytes plus one, above 128
// repeats the next byte, and 128 ends the data.
func runLengthDecode(data []byte) ([]byte, error) {
	out := make([]byte, 0, 2*len(data))
	for i := 0; i < len(data); {
		n := int(data[i])
		i++
		switch {
		case n == 128:
			return out, nil
		case n < 128:
			end := i + n + 1
			if end > len(data) {
				return out, fmt.Errorf("pdf: RunLengthDecode: truncated literal run")
			}
			out = append(out, data[i:end]...)
			i = end
		default:
			if i >= len(data) {
				return out, fmt.Errorf("pdf: RunLengthDecode: truncated repeat run")
			}
			c := data[i]
			for range 257 - n {
				out = append(out, c)
			}
			i++
		}
		if len(out) > MaxStreamBytes {
			return nil, ErrTooLarge
		}
	}
	return out, nil
}
