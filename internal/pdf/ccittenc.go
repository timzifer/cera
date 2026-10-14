package pdf

// CCITT Group 4 encoding (ITU-T T.6), the other way of ccittDecode: what
// cera writes bilevel images with when a file is made smaller. The modes
// and the codes are those of the tables above; the coding follows T.6
// Figure 1, with the changing elements a0, a1, a2 on the row being coded
// and b1, b2 on the row above.

// EncodeCCITTG4 encodes bilevel samples — one bit a pixel, w to a row,
// each row padded to a byte, a 1 bit white — as CCITT Group 4 (/K -1,
// /BlackIs1 false), ending with the end-of-facsimile-block code.
func EncodeCCITTG4(data []byte, w, h int) []byte {
	stride := (w + 7) / 8
	var bw ccittWriter
	if w <= 0 || h <= 0 || len(data) < stride*h {
		return nil
	}
	codes := map[*[]ccittCode]map[int]string{}
	lookup := func(t *[]ccittCode) map[int]string {
		if m, ok := codes[t]; ok {
			return m
		}
		m := map[int]string{}
		for _, c := range *t {
			m[c.value] = c.bits
		}
		codes[t] = m
		return m
	}
	modes := lookup(&ccittModeCodes)
	white, black := lookup(&ccittWhiteCodes), lookup(&ccittBlackCodes)
	run := func(n int, isWhite bool) {
		t := black
		if isWhite {
			t = white
		}
		for n >= 2560 {
			bw.put(t[2560])
			n -= 2560
		}
		if n >= 64 {
			bw.put(t[n/64*64])
			n %= 64
		}
		bw.put(t[n])
	}
	// The colours of the pixels of a row, 1 for black; the row above the
	// first is white.
	ref := make([]byte, w)
	cur := make([]byte, w)
	for y := range h {
		row := data[y*stride : (y+1)*stride]
		for x := range w {
			cur[x] = ^row[x/8] >> (7 - uint(x%8)) & 1
		}
		// next returns the first x after at whose colour is not c, w for
		// none.
		next := func(line []byte, at int, c byte) int {
			for x := max(at+1, 0); x < w; x++ {
				if line[x] != c {
					return x
				}
			}
			return w
		}
		// b1 is the first changing element of the row above after a0
		// of the colour other than that of a0; colour at -1 is white.
		findB1 := func(a0 int, c byte) int {
			for x := max(a0+1, 0); x < w; x++ {
				prev := byte(0)
				if x > 0 {
					prev = ref[x-1]
				}
				if ref[x] != prev && ref[x] != c {
					return x
				}
			}
			return w
		}
		a0, c := -1, byte(0)
		for a0 < w {
			a1 := next(cur, a0, c)
			b1 := findB1(a0, c)
			b2 := w
			if b1 < w {
				b2 = next(ref, b1, ref[b1])
			}
			switch {
			case b2 < a1:
				bw.put(modes[modePass])
				a0 = b2
			case a1-b1 >= -3 && a1-b1 <= 3:
				bw.put(modes[[7]int{modeVL3, modeVL2, modeVL1, modeV0, modeVR1, modeVR2, modeVR3}[a1-b1+3]])
				a0, c = a1, 1-c
			default:
				a2 := w
				if a1 < w {
					a2 = next(cur, a1, 1-c)
				}
				bw.put(modes[modeH])
				run(a1-max(a0, 0), c == 0)
				run(a2-a1, c != 0)
				a0 = a2
			}
		}
		ref, cur = cur, ref
	}
	// End of facsimile block: two EOL codes.
	bw.put("000000000001")
	bw.put("000000000001")
	return bw.bytes()
}

// ccittWriter writes codes bit by bit, most significant first.
type ccittWriter struct {
	out  []byte
	acc  byte
	bits int
}

func (b *ccittWriter) put(code string) {
	for i := 0; i < len(code); i++ {
		b.acc = b.acc<<1 | (code[i] - '0')
		if b.bits++; b.bits == 8 {
			b.out = append(b.out, b.acc)
			b.acc, b.bits = 0, 0
		}
	}
}

func (b *ccittWriter) bytes() []byte {
	if b.bits > 0 {
		b.out = append(b.out, b.acc<<(8-b.bits))
		b.acc, b.bits = 0, 0
	}
	return b.out
}
