package cera

import (
	"encoding/binary"
	"slices"
	"strconv"
)

// repairSFNT repairs the defects of embedded TrueType and OpenType
// programs that opentype.Parse rejects while PDFium, MuPDF and pdf.js draw
// the font. Writers of PDF subsets get the bookkeeping of a program wrong
// far more often than its outlines, so each repair makes the tables agree
// with each other again and leaves the outlines as they are:
//
//   - directory entries that point outside the program, or repeat a tag,
//     are dropped, and a table that runs past the end is cut at it;
//   - head, maxp, hhea and vhea are looked for a few bytes around their
//     offset when their version or magic number is not where it says;
//   - an indexToLocFormat other than 0 or 1 is read from the loca table,
//     and a unitsPerEm outside 16–16384 becomes 1000, as in pdf.js; a loca
//     table whose offsets do not grow within glyf is looked for likewise;
//   - a loca table too short for numGlyphs: when hmtx stops there too, the
//     glyph count is lowered to what both describe, otherwise the missing
//     glyphs are added as empty ones;
//   - more metric pairs than glyphs are cut to the glyph count, and an hmtx
//     or vmtx table that stops after its (advance, side bearing) pairs gets
//     the missing side bearings as zero (Windows' printer drivers embed such
//     subsets, TTE…t00, and Arial or Times New Roman under their own names);
//   - a format 6 cmap subtable is cut to the entries its table holds.
//
// Repaired tables are copied to the end of the program; cera never writes
// the program out again. A program whose glyph headers do not look like
// glyphs after the repair (outlinesPlausible) has lost bytes inside glyf,
// and is left to a stand-in, as PDFium and pdf.js leave it. It returns
// false when there is nothing it can repair.
func repairSFNT(data []byte) ([]byte, bool) {
	if len(data) < 12 {
		return nil, false
	}
	s := &sfnt{data: append([]byte(nil), data...)}
	s.readDirectory()
	s.findTables()
	s.fixHead()
	s.findLoca()
	s.fixGlyphCount()
	s.fixMetrics("hhea", "hmtx")
	s.fixMetrics("vhea", "vmtx")
	s.fixCmap()
	if !s.changed || len(s.tables) == 0 || !s.outlinesPlausible() {
		return nil, false
	}
	s.writeDirectory()
	return s.data, true
}

// sfnt is a program being repaired: its bytes and its table directory.
type sfnt struct {
	data    []byte
	tables  []sfntTable
	changed bool
}

type sfntTable struct {
	tag    string
	off, n int
}

func (s *sfnt) u16(at int) int    { return int(binary.BigEndian.Uint16(s.data[at:])) }
func (s *sfnt) u32(at int) uint32 { return binary.BigEndian.Uint32(s.data[at:]) }

// table returns the directory entry of tag, or nil.
func (s *sfnt) table(tag string) *sfntTable {
	for i := range s.tables {
		if s.tables[i].tag == tag {
			return &s.tables[i]
		}
	}
	return nil
}

// readDirectory reads the table directory, dropping entries that are not
// in the program and cutting tables that run past its end.
func (s *sfnt) readDirectory() {
	for i := range s.u16(4) {
		d := 12 + 16*i
		if d+16 > len(s.data) {
			s.changed = true
			break
		}
		tag := string(s.data[d : d+4])
		off, n := int64(s.u32(d+8)), int64(s.u32(d+12))
		if !printable(tag) || off > int64(len(s.data)) || s.table(tag) != nil {
			s.changed = true
			continue
		}
		if off+n > int64(len(s.data)) {
			n = int64(len(s.data)) - off
			s.changed = true
		}
		s.tables = append(s.tables, sfntTable{tag, int(off), int(n)})
	}
}

// printable reports a table tag of printable ASCII, as the spec requires.
func printable(tag string) bool {
	for i := range len(tag) {
		if tag[i] < 0x20 || tag[i] > 0x7E {
			return false
		}
	}
	return true
}

// findTables moves the tables whose start can be recognised (a version, a
// magic number, a header that describes the table) to where that start
// is, when it is a few bytes from where the directory says.
func (s *sfnt) findTables() {
	version := func(at int, want ...uint32) func(off, n int) bool {
		return func(off, n int) bool { return n >= at+4 && slices.Contains(want, s.u32(off+at)) }
	}
	for _, c := range [...]struct {
		tag   string
		valid func(off, n int) bool
	}{
		{"head", version(12, 0x5F0F3CF5)},
		{"maxp", version(0, 0x00005000, 0x00010000)},
		{"hhea", version(0, 0x00010000)},
		{"vhea", version(0, 0x00010000, 0x00011000)},
		{"post", version(0, 0x00010000, 0x00020000, 0x00025000, 0x00030000)},
		{"cmap", s.cmapAt},
		{"name", s.nameAt},
	} {
		t := s.table(c.tag)
		if t == nil {
			continue
		}
		ok := func(off int) bool {
			return off >= 0 && off <= len(s.data) && c.valid(off, min(t.n, len(s.data)-off))
		}
		if ok(t.off) {
			continue
		}
		for _, d := range [...]int{-1, 1, -2, 2, -3, 3, -4, 4} {
			if ok(t.off + d) {
				t.off += d
				t.n = min(t.n, len(s.data)-t.off)
				s.changed = true
				break
			}
		}
	}
}

// cmapAt reports a cmap table at off: version 0 and encoding records that
// point into the table at subtables of a known format.
func (s *sfnt) cmapAt(off, n int) bool {
	if n < 4 || s.u16(off) != 0 {
		return false
	}
	num := s.u16(off + 2)
	if num == 0 || 4+8*num > n {
		return false
	}
	for i := range num {
		sub := int64(s.u32(off + 4 + 8*i + 4))
		if sub+2 > int64(n) || s.u16(off+int(sub)) > 14 {
			return false
		}
	}
	return true
}

// nameAt reports a name table at off: format 0 or 1 and its strings right
// after its records.
func (s *sfnt) nameAt(off, n int) bool {
	if n < 6 || s.u16(off) > 1 {
		return false
	}
	count, storage := s.u16(off+2), s.u16(off+4)
	return storage >= 6+12*count && storage <= n
}

// numGlyphs is the glyph count of maxp, 0 when it cannot be read.
func (s *sfnt) numGlyphs() int {
	if m := s.table("maxp"); m != nil && m.n >= 6 {
		return s.u16(m.off + 4)
	}
	return 0
}

// fixHead reads a missing index format from the loca table and replaces a
// unitsPerEm no renderer takes.
func (s *sfnt) fixHead() {
	h := s.table("head")
	if h == nil || h.n < 54 {
		return
	}
	if upem := s.u16(h.off + 18); upem < 16 || upem > 16384 {
		binary.BigEndian.PutUint16(s.data[h.off+18:], 1000)
		s.changed = true
	}
	if f := s.u16(h.off + 50); f == 0 || f == 1 {
		return
	}
	format := 1
	if !s.locaFits(1) && s.locaFits(0) {
		format = 0
	}
	binary.BigEndian.PutUint16(s.data[h.off+50:], uint16(format))
	s.changed = true
}

// findLoca moves a loca table whose offsets do not grow within glyf to a
// few bytes away where they do, as findTables does for tables with a
// header.
func (s *sfnt) findLoca() {
	l, h := s.table("loca"), s.table("head")
	if l == nil || h == nil || h.n < 54 {
		return
	}
	format := s.u16(h.off + 50)
	if format > 1 || s.locaAt(l.off, l.n, format) {
		return
	}
	for _, d := range [...]int{-1, 1, -2, 2, -3, 3, -4, 4} {
		if n := min(l.n, len(s.data)-l.off-d); s.locaAt(l.off+d, n, format) {
			l.off += d
			l.n = n
			s.changed = true
			return
		}
	}
}

// locaFits reports whether the loca table, read in format (0 short, 1
// long), gives offsets that grow and stay within glyf.
func (s *sfnt) locaFits(format int) bool {
	l := s.table("loca")
	return l != nil && s.locaAt(l.off, l.n, format)
}

// locaAt reports whether n bytes at off, read as a loca table in format,
// give offsets that grow and stay within glyf.
func (s *sfnt) locaAt(off, n, format int) bool {
	g, glyphs := s.table("glyf"), s.numGlyphs()
	if g == nil || glyphs == 0 || off < 0 || off+n > len(s.data) {
		return false
	}
	size := 2 << format
	count := min(glyphs+1, n/size)
	if count < 2 {
		return false
	}
	prev := 0
	for i := range count {
		o := 2 * s.u16(off+i*size)
		if format == 1 {
			o = int(s.u32(off + i*size))
		}
		if o < prev || o > g.n {
			return false
		}
		prev = o
	}
	return true
}

// fixGlyphCount makes maxp and a loca table too short for it agree.
func (s *sfnt) fixGlyphCount() {
	l, g, h, m := s.table("loca"), s.table("glyf"), s.table("head"), s.table("maxp")
	n := s.numGlyphs()
	if l == nil || g == nil || h == nil || h.n < 54 || m == nil || n == 0 {
		return
	}
	format := s.u16(h.off + 50)
	if format > 1 {
		return
	}
	size := 2 << format
	have := l.n/size - 1 // glyphs the loca table describes
	if have >= n || have < 1 {
		return
	}
	if hm := s.metricGlyphs("hhea", "hmtx"); hm >= 0 && hm <= have {
		binary.BigEndian.PutUint16(s.data[m.off+4:], uint16(have))
		s.changed = true
		return
	}
	// The missing glyphs are empty: each starts where glyf ends.
	loca := make([]byte, (n+1)*size)
	copy(loca, s.data[l.off:l.off+(have+1)*size])
	for i := have + 1; i <= n; i++ {
		if format == 1 {
			binary.BigEndian.PutUint32(loca[i*size:], uint32(g.n))
		} else {
			binary.BigEndian.PutUint16(loca[i*size:], uint16(g.n/2))
		}
	}
	s.place(l, loca)
}

// outlinesPlausible reports whether the glyphs loca points at start with
// a header a glyph has (a contour count from -1, a bounding box whose
// minimum is not past its maximum) for all but one in twenty of them. A
// program without glyf and loca has nothing to check.
func (s *sfnt) outlinesPlausible() bool {
	l, g, h, n := s.table("loca"), s.table("glyf"), s.table("head"), s.numGlyphs()
	if l == nil || g == nil || h == nil || h.n < 54 || n == 0 {
		return true
	}
	format := s.u16(h.off + 50)
	if format > 1 {
		return false
	}
	size := 2 << format
	at := func(i int) int {
		if format == 1 {
			return int(s.u32(l.off + i*size))
		}
		return 2 * s.u16(l.off+i*size)
	}
	count := min(n, l.n/size-1)
	glyphs, bad := 0, 0
	for i := range count {
		lo, hi := at(i), at(i+1)
		if hi <= lo {
			continue
		}
		glyphs++
		if hi > g.n || hi-lo < 10 {
			bad++
			continue
		}
		p := g.off + lo
		contours := int16(s.u16(p))
		x0, y0 := int16(s.u16(p+2)), int16(s.u16(p+4))
		x1, y1 := int16(s.u16(p+6)), int16(s.u16(p+8))
		if contours < -1 || x0 > x1 || y0 > y1 {
			bad++
		}
	}
	return bad*20 <= glyphs
}

// metricGlyphs is how many glyphs a metrics table describes (its pairs and
// the side bearings after them), -1 when it cannot be read.
func (s *sfnt) metricGlyphs(header, metrics string) int {
	hh, mt := s.table(header), s.table(metrics)
	if hh == nil || mt == nil || hh.n < 36 {
		return -1
	}
	pairs := s.u16(hh.off + 34)
	if mt.n < 4*pairs {
		return -1
	}
	return pairs + (mt.n-4*pairs)/2
}

// fixMetrics cuts more metric pairs than glyphs to the glyph count, and
// completes a metrics table that stops after its pairs.
func (s *sfnt) fixMetrics(header, metrics string) {
	hh, mt, n := s.table(header), s.table(metrics), s.numGlyphs()
	if hh == nil || mt == nil || hh.n < 36 || n == 0 {
		return
	}
	pairs := s.u16(hh.off + 34)
	if pairs > n {
		pairs = n
		binary.BigEndian.PutUint16(s.data[hh.off+34:], uint16(pairs))
		s.changed = true
	}
	need := 4*pairs + 2*(n-pairs)
	if pairs == 0 || mt.n < 4*pairs || mt.n >= need {
		return
	}
	full := make([]byte, need)
	copy(full, s.data[mt.off:mt.off+mt.n])
	s.place(mt, full)
}

// fixCmap cuts format 6 subtables to the entries the cmap table holds.
func (s *sfnt) fixCmap() {
	c := s.table("cmap")
	if c == nil || c.n < 4 {
		return
	}
	for i := range s.u16(c.off + 2) {
		rec := 4 + 8*i
		if rec+8 > c.n {
			return
		}
		sub := int64(s.u32(c.off + rec + 4))
		if sub+10 > int64(c.n) || s.u16(c.off+int(sub)) != 6 {
			continue
		}
		at := c.off + int(sub)
		if most := (c.n - int(sub) - 10) / 2; s.u16(at+8) > most {
			binary.BigEndian.PutUint16(s.data[at+8:], uint16(most))
			s.changed = true
		}
	}
}

// place puts a table's new content at the end of the program.
func (s *sfnt) place(t *sfntTable, content []byte) {
	start := (len(s.data) + 3) &^ 3
	s.data = append(s.data, make([]byte, start-len(s.data))...)
	s.data = append(s.data, content...)
	t.off, t.n = start, len(content)
	s.changed = true
}

// writeDirectory writes the table directory back; it is never longer than
// it was.
func (s *sfnt) writeDirectory() {
	binary.BigEndian.PutUint16(s.data[4:], uint16(len(s.tables)))
	for i, t := range s.tables {
		d := 12 + 16*i
		copy(s.data[d:], t.tag)
		binary.BigEndian.PutUint32(s.data[d+8:], uint32(t.off))
		binary.BigEndian.PutUint32(s.data[d+12:], uint32(t.n))
	}
}

// firstOfCollection returns the first font of a TrueType collection
// ("ttcf") as a program of its own. Some writers embed a whole .ttc as
// FontFile2; opentype.Parse reads only single fonts. The tables of a
// collection are placed from the start of the file, so the first font's
// table directory is written in front of the whole collection with its
// offsets moved past itself. It returns false when data is no collection
// or its first directory does not fit.
func firstOfCollection(data []byte) ([]byte, bool) {
	if len(data) < 16 || string(data[:4]) != "ttcf" || binary.BigEndian.Uint32(data[8:]) == 0 {
		return nil, false
	}
	off := int64(binary.BigEndian.Uint32(data[12:]))
	if off+12 > int64(len(data)) {
		return nil, false
	}
	num := int64(binary.BigEndian.Uint16(data[off+4:]))
	size := 12 + 16*num
	if off+size > int64(len(data)) {
		return nil, false
	}
	out := make([]byte, size+int64(len(data)))
	copy(out, data[off:off+size])
	copy(out[size:], data)
	for i := range num {
		e := 12 + 16*i + 8
		t := int64(binary.BigEndian.Uint32(out[e:])) + size
		if t > int64(len(data))+size {
			return nil, false
		}
		binary.BigEndian.PutUint32(out[e:], uint32(t))
	}
	return out, true
}

// repairCFF rewrites the real operands of a CFF program's DICTs that do
// not read as a number (an empty one, a lone minus sign) as zero, the value
// FreeType and pdf.js give them, and drops the bytes of Private DICTs that
// start no operand (dropReserved); opentype.ParseCFF rejects the whole
// program for either. Each DICT keeps its length, so no offset moves. The
// DICTs are the Top DICT, the Font DICTs of a CID-keyed program and their
// Private DICTs. It returns false when there is nothing to rewrite.
func repairCFF(data []byte) ([]byte, bool) {
	if len(data) < 4 || data[0] != 1 {
		return nil, false
	}
	b := append([]byte(nil), data...)
	_, p, ok := cffIndex(b, int(b[2])) // Name INDEX
	if !ok {
		return nil, false
	}
	tops, _, ok := cffIndex(b, p)
	if !ok || len(tops) == 0 {
		return nil, false
	}
	changed := false
	fix := func(dict []byte) {
		if zeroBadReals(dict) {
			changed = true
		}
		// The Private DICT: size and offset.
		if v := cffDict(dict)[18]; len(v) == 2 {
			size, off := int(v[0]), int(v[1])
			if size > 0 && off > 0 && off+size <= len(b) {
				priv := b[off : off+size]
				if dropReserved(priv) {
					changed = true
				}
				if zeroBadReals(priv) {
					changed = true
				}
			}
		}
	}
	fix(tops[0])
	if off, ok := cffOffset(cffDict(tops[0]), 1236, b); ok { // FDArray
		if fds, _, ok := cffIndex(b, off); ok {
			for _, fd := range fds {
				fix(fd)
			}
		}
	}
	return b, changed
}

// zeroBadReals rewrites, in place, the real operands of a DICT that do
// not read as a number as zeros of the same length.
func zeroBadReals(d []byte) bool {
	changed := false
	for i := 0; i < len(d); {
		c := d[i]
		switch {
		case c == 12:
			i += 2
		case c <= 21 || (c >= 32 && c <= 246):
			i++
		case c == 28:
			i += 3
		case c == 29:
			i += 5
		case c >= 247 && c <= 254:
			i += 2
		case c == 30:
			_, n := cffReal(d[i+1:])
			if n == 0 {
				return changed
			}
			if !realReads(d[i+1 : i+1+n]) {
				for k := i + 1; k < i+n; k++ {
					d[k] = 0x00
				}
				d[i+n] = 0x0f
				changed = true
			}
			i += 1 + n
		default:
			return changed
		}
	}
	return changed
}

// realReads reports whether the nibbles of a real operand read as a
// number.
func realReads(b []byte) bool {
	var s []byte
	for _, c := range b {
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
				_, err := strconv.ParseFloat(string(s), 64)
				return err == nil
			}
		}
	}
	return false
}

// repairPFB repairs a Type 1 program in PFB segments whose last segment
// runs past the end of the data (writers cut off the trailer of zeros and
// cleartomark): each segment is cut to the bytes there are, and the end
// marker added. It returns false for data that is no PFB or needs no
// repair.
func repairPFB(data []byte) ([]byte, bool) {
	var out []byte
	changed := false
	for p := 0; ; {
		if p+2 > len(data) || data[p] != 0x80 {
			return nil, false
		}
		kind := data[p+1]
		if kind == 3 {
			break
		}
		if kind != 1 && kind != 2 || p+6 > len(data) {
			return nil, false
		}
		n := int64(binary.LittleEndian.Uint32(data[p+2:]))
		end := false
		if avail := int64(len(data) - p - 6); n > avail {
			n, end, changed = avail, true, true
		}
		if n > 0 {
			seg := []byte{0x80, kind, 0, 0, 0, 0}
			binary.LittleEndian.PutUint32(seg[2:], uint32(n))
			out = append(append(out, seg...), data[p+6:p+6+int(n)]...)
		}
		p += 6 + int(n)
		if end || p == len(data) {
			changed = changed || p == len(data)
			break
		}
	}
	if !changed {
		return nil, false
	}
	return append(out, 0x80, 3), true
}

// isCFF reports data that starts with the header of a CFF program, as some
// writers embed one as FontFile instead of FontFile3.
func isCFF(data []byte) bool {
	return len(data) >= 4 && data[0] == 1 && data[1] == 0 && data[2] >= 4 && data[3] >= 1 && data[3] <= 4
}

// dropReserved rewrites, in place, a DICT that holds bytes no operand or
// operator starts with (22–27, 31, 255): it is read as pdf.js reads it,
// skipping them, and written again with filler entries of an operator no
// reader knows (escape 39) in front, so it keeps its length and the
// offsets that count from its start stay right. It returns false when the
// DICT has no such byte or the filler does not fit.
func dropReserved(d []byte) bool {
	var kept []byte
	dropped := false
	for i := 0; i < len(d); {
		c := d[i]
		n := 1
		switch {
		case c == 12:
			n = 2
		case c == 28:
			n = 3
		case c == 29:
			n = 5
		case c >= 247 && c <= 254:
			n = 2
		case c == 30:
			_, m := cffReal(d[i+1:])
			if m == 0 {
				return false
			}
			n = 1 + m
		case c <= 21 || (c >= 32 && c <= 246):
		default:
			dropped = true
			i++
			continue
		}
		if i+n > len(d) {
			return false
		}
		kept = append(kept, d[i:i+n]...)
		i += n
	}
	gap := len(d) - len(kept)
	if !dropped || gap < 3 {
		return false
	}
	var fill []byte
	for gap > 0 {
		switch gap {
		case 4:
			fill = append(fill, 247, 0, 12, 39)
			gap = 0
		case 5:
			fill = append(fill, 28, 0, 0, 12, 39)
			gap = 0
		default:
			fill = append(fill, 139, 12, 39)
			gap -= 3
		}
	}
	copy(d, fill)
	copy(d[len(fill):], kept)
	return true
}
