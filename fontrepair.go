package cera

import "encoding/binary"

// padHmtx repairs a TrueType program whose hmtx table stops after its
// numberOfHMetrics (advance, left side bearing) pairs, without the left
// side bearings of the glyphs after them. Windows' printer drivers embed
// such subsets (TTE…t00, and Arial or Times New Roman under their own
// names); opentype.Parse rejects the whole program for it, while other
// renderers draw it. The table is copied to the end of the program with
// the missing bearings as zero, which changes nothing cera draws: outlines
// come from glyf, advances from the pairs, and cera never writes the
// program out again. It returns the repaired program, or false when the
// hmtx table is not short in this way.
func padHmtx(data []byte) ([]byte, bool) {
	if len(data) < 12 {
		return nil, false
	}
	type entry struct{ dir, off, n int }
	table := func(tag string) (entry, bool) {
		num := int(binary.BigEndian.Uint16(data[4:]))
		for i := range num {
			d := 12 + 16*i
			if d+16 > len(data) {
				break
			}
			if string(data[d:d+4]) != tag {
				continue
			}
			off, n := int(binary.BigEndian.Uint32(data[d+8:])), int(binary.BigEndian.Uint32(data[d+12:]))
			if off < 0 || n < 0 || off > len(data) || n > len(data)-off {
				return entry{}, false
			}
			return entry{d, off, n}, true
		}
		return entry{}, false
	}
	maxp, ok1 := table("maxp")
	hhea, ok2 := table("hhea")
	hmtx, ok3 := table("hmtx")
	if !ok1 || !ok2 || !ok3 || maxp.n < 6 || hhea.n < 36 {
		return nil, false
	}
	glyphs := int(binary.BigEndian.Uint16(data[maxp.off+4:]))
	metrics := int(binary.BigEndian.Uint16(data[hhea.off+34:]))
	need := 4*metrics + 2*(glyphs-metrics)
	if metrics == 0 || metrics > glyphs || hmtx.n < 4*metrics || hmtx.n >= need {
		return nil, false
	}
	start := (len(data) + 3) &^ 3
	out := make([]byte, start+(need+3)&^3)
	copy(out, data)
	copy(out[start:], data[hmtx.off:hmtx.off+hmtx.n])
	binary.BigEndian.PutUint32(out[hmtx.dir+8:], uint32(start))
	binary.BigEndian.PutUint32(out[hmtx.dir+12:], uint32(need))
	return out, true
}
