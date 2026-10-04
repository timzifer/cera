package cera

import (
	"math"
	"strconv"
)

// A CID-keyed CFF program (FontFile3 /CIDFontType0C) may give its
// FontMatrix in the Font DICTs of its FDArray rather than in the Top DICT
// (Technical Note #5176, CIDFonts), so the size of its units can differ
// per Font DICT, and with it per glyph through FDSelect. go-opentype reads
// the Top DICT's matrix only and scales every glyph by 1/1000 when it has
// none; cffFDScales reads the rest, so a program of 2048 units per em is
// not drawn twice too large.

// cffFDScales is the em scale of each glyph of a CID-keyed CFF program:
// fd holds x and y per Font DICT, sel the Font DICT of each glyph.
type cffFDScales struct {
	fd  [][2]float64
	sel []uint8 // nil: every glyph uses fd[0]
}

// scale returns the factors from font units to em of glyph gid.
func (s *cffFDScales) scale(gid int) (sx, sy float64) {
	i := 0
	if s.sel != nil && gid >= 0 && gid < len(s.sel) {
		i = int(s.sel[gid])
	}
	if i >= len(s.fd) {
		i = 0
	}
	return s.fd[i][0], s.fd[i][1]
}

// readCFFFDScales returns the per-glyph scales of a bare CID-keyed CFF
// program whose FDArray carries a FontMatrix, or nil when the program is
// anything else or no Font DICT has one (the Top DICT's matrix, which
// go-opentype applies, is then all there is).
func readCFFFDScales(b []byte) *cffFDScales {
	if len(b) < 4 || b[0] != 1 {
		return nil
	}
	p := int(b[2])             // header size
	_, p, ok := cffIndex(b, p) // Name INDEX
	if !ok {
		return nil
	}
	tops, _, ok := cffIndex(b, p)
	if !ok || len(tops) == 0 {
		return nil
	}
	top := cffDict(tops[0])
	if _, cid := top[1230]; !cid { // ROS
		return nil
	}
	cs, ok := cffOffset(top, 17, b) // CharStrings
	if !ok {
		return nil
	}
	glyphs, _, ok := cffIndex(b, cs)
	if !ok || len(glyphs) == 0 {
		return nil
	}
	fdOff, ok := cffOffset(top, 1236, b) // FDArray
	if !ok {
		return nil
	}
	fds, _, ok := cffIndex(b, fdOff)
	if !ok || len(fds) == 0 || len(fds) > 256 {
		return nil
	}
	topM, hasTop := cffMatrix(top)
	s := &cffFDScales{fd: make([][2]float64, len(fds))}
	found := false
	for i, d := range fds {
		m, ok := cffMatrix(cffDict(d))
		switch {
		case ok && hasTop:
			// Concatenated, as FreeType and pdf.js do; a font that gives
			// a thousandth in both means it once.
			if c := cffMul(m, topM); plausibleScale(c[0]) && plausibleScale(c[3]) {
				m = c
			}
			found = true
		case ok:
			found = true
		case hasTop:
			m = topM
		default:
			m = [6]float64{0.001, 0, 0, 0.001, 0, 0}
		}
		s.fd[i] = [2]float64{m[0], m[3]}
		if !plausibleScale(m[0]) || !plausibleScale(m[3]) {
			s.fd[i] = [2]float64{0.001, 0.001}
		}
	}
	if !found {
		return nil
	}
	if len(fds) > 1 {
		if off, ok := cffOffset(top, 1237, b); ok { // FDSelect
			s.sel = cffFDSelect(b, off, len(glyphs))
		}
	}
	return s
}

// plausibleScale bounds an em scale to the units per em a font may have
// (16 to 16384, the range of OpenType's head table) in either direction.
func plausibleScale(v float64) bool {
	a := math.Abs(v)
	return a >= 1.0/16384 && a <= 1.0/16
}

// cffMatrix reads a DICT's FontMatrix.
func cffMatrix(d map[int][]float64) ([6]float64, bool) {
	v, ok := d[1207]
	if !ok || len(v) < 6 || v[0] == 0 || v[3] == 0 {
		return [6]float64{}, false
	}
	return [6]float64(v[:6]), true
}

// cffMul is the matrix m then n.
func cffMul(m, n [6]float64) [6]float64 {
	return [6]float64{
		m[0]*n[0] + m[1]*n[2], m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2], m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4], m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

// cffOffset reads an offset operand of a DICT that must point into b.
func cffOffset(d map[int][]float64, op int, b []byte) (int, bool) {
	v, ok := d[op]
	if !ok || len(v) == 0 {
		return 0, false
	}
	off := int(v[len(v)-1])
	return off, off > 0 && off < len(b)
}

// cffFDSelect reads an FDSelect (formats 0 and 3) of n glyphs; nil when it
// is malformed.
func cffFDSelect(b []byte, p, n int) []uint8 {
	sel := make([]uint8, n)
	switch b[p] {
	case 0:
		if p+1+n > len(b) {
			return nil
		}
		copy(sel, b[p+1:p+1+n])
	case 3:
		if p+3 > len(b) {
			return nil
		}
		ranges := int(b[p+1])<<8 | int(b[p+2])
		q := p + 3
		if q+3*ranges+2 > len(b) {
			return nil
		}
		for r := range ranges {
			first := int(b[q])<<8 | int(b[q+1])
			fd := b[q+2]
			next := int(b[q+3])<<8 | int(b[q+4])
			if r == ranges-1 {
				next = min(next, n)
			}
			for g := max(first, 0); g < min(next, n); g++ {
				sel[g] = fd
			}
			q += 3
		}
	default:
		return nil
	}
	return sel
}

// cffIndex reads the INDEX at p and returns its objects and the offset
// past it.
func cffIndex(b []byte, p int) ([][]byte, int, bool) {
	if p < 0 || p+2 > len(b) {
		return nil, 0, false
	}
	count := int(b[p])<<8 | int(b[p+1])
	p += 2
	if count == 0 {
		return nil, p, true
	}
	if p >= len(b) {
		return nil, 0, false
	}
	size := int(b[p])
	p++
	if size < 1 || size > 4 || p+(count+1)*size > len(b) {
		return nil, 0, false
	}
	off := func(i int) int {
		v := 0
		for _, c := range b[p+i*size : p+(i+1)*size] {
			v = v<<8 | int(c)
		}
		return v
	}
	data := p + (count+1)*size - 1 // offsets count from 1
	out := make([][]byte, count)
	for i := range out {
		lo, hi := data+off(i), data+off(i+1)
		if lo <= data || hi < lo || hi > len(b) {
			return nil, 0, false
		}
		out[i] = b[lo:hi]
	}
	return out, data + off(count), true
}

// cffDict reads a DICT into its operands by operator (escaped operators as
// 1200 + the second byte); it stops at the first malformed byte.
func cffDict(b []byte) map[int][]float64 {
	d := map[int][]float64{}
	var ops []float64
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c <= 21:
			op := int(c)
			i++
			if c == 12 {
				if i >= len(b) {
					return d
				}
				op = 1200 + int(b[i])
				i++
			}
			d[op] = ops
			ops = nil
			continue
		case c == 28 && i+3 <= len(b):
			ops = append(ops, float64(int16(uint16(b[i+1])<<8|uint16(b[i+2]))))
			i += 3
		case c == 29 && i+5 <= len(b):
			ops = append(ops, float64(int32(uint32(b[i+1])<<24|uint32(b[i+2])<<16|uint32(b[i+3])<<8|uint32(b[i+4]))))
			i += 5
		case c == 30:
			v, n := cffReal(b[i+1:])
			if n == 0 {
				return d
			}
			ops = append(ops, v)
			i += 1 + n
		case c >= 32 && c <= 246:
			ops = append(ops, float64(int(c)-139))
			i++
		case c >= 247 && c <= 250 && i+2 <= len(b):
			ops = append(ops, float64((int(c)-247)*256+int(b[i+1])+108))
			i += 2
		case c >= 251 && c <= 254 && i+2 <= len(b):
			ops = append(ops, float64(-(int(c)-251)*256-int(b[i+1])-108))
			i += 2
		default:
			return d
		}
		if len(ops) > 48 {
			return d
		}
	}
	return d
}

// cffReal decodes the nibbles of a real operand; n is the bytes read, 0
// when it does not end.
func cffReal(b []byte) (v float64, n int) {
	var s []byte
	for i, c := range b {
		for _, nib := range [2]byte{c >> 4, c & 15} {
			switch {
			case nib <= 9:
				s = append(s, '0'+nib)
			case nib == 10:
				s = append(s, '.')
			case nib == 11:
				s = append(s, 'E')
			case nib == 12:
				s = append(s, 'E', '-')
			case nib == 14:
				s = append(s, '-')
			case nib == 15:
				v, _ = strconv.ParseFloat(string(s), 64)
				return v, i + 1
			}
		}
		if len(s) > 64 {
			return 0, 0
		}
	}
	return 0, 0
}
