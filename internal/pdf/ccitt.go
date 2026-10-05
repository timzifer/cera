// Ported from github.com/go-pdfkit/reader v0.6.0 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/reader authors); see LICENSE-go-pdfkit.

package pdf

import "fmt"

// CCITT Group 3 and Group 4 fax decoding, as used by /CCITTFaxDecode.
//
// WHY THIS IS HERE
//
// A scanned form is a fax. 67 of the 1 633 real forms in the corpus — the
// eleven issuing bodies, not the vendor test suites — carry a CCITT-encoded
// image, 263 images between them, and 6 of their pages have nothing else on
// them at all. Those six pages drew entirely blank, and a blank page is the
// failure a reader notices before any other.
//
// WHY IT BELONGS IN THE READER RATHER THAN IN A RENDERER
//
// The other filters this package stops at — DCT, JPX, JBIG2 — carry an image
// with its own idea of how many components it has and how deep they are. This
// one does not: it produces bilevel samples, one bit a pixel, rows padded to a
// byte, and the stream dictionary says what they mean. That is a byte stream,
// so it is a filter, and every caller gets it rather than each writing its own.
//
// THE REFERENCE READ BEFORE WRITING THIS
//
// ITU-T Recommendations T.4 (Group 3) and T.6 (Group 4), by way of the tables
// and the changing-element algorithm in golang.org/x/image/ccitt — read, not
// imported, because this package has no dependencies outside the standard
// library and gains none. The code tables below were extracted mechanically
// from that package's gen.go rather than retyped, because a table of 218
// variable-length codes transcribed by hand is a table with a mistake in it.
//
// The algorithm is the one T.6 Figure 1 describes. A row is decoded into one
// byte a pixel — 0xFF white, 0x00 black — and the row before it is the
// reference against which the two-dimensional modes are resolved:
//
//	          b1 b2
//	          v  v
//	prev: BBBBBwwwwwBBBwwwww
//	curr: BBBwwwwwBBBBBBwwww
//	         ^    ^     ^
//	         a0   a1    a2
//
// a0 is where the pen is; a1 the next colour change to its right on this row;
// b1 the first change on the row above that is to the right of a0 and of the
// opposite colour to it; b2 the next change after b1. Pass mode says a1 is at
// or beyond b2; horizontal mode reads two runs and ignores the row above;
// vertical mode puts a1 at b1 plus an offset between -3 and +3.

// The two-dimensional modes of T.4 Table 1.
const (
	modePass = iota
	modeH
	modeV0
	modeVR1
	modeVR2
	modeVR3
	modeVL1
	modeVL2
	modeVL3
	modeExt
)

// A ccittCode is one row of a code table: the value a code names, and the bits
// that name it, most significant first.
type ccittCode struct {
	value int
	bits  string
}

// ccittModeCodes is Table 1 of ITU-T T.4: the two-dimensional modes.
var ccittModeCodes = []ccittCode{
	{modePass, "0001"}, {modeH, "001"}, {modeV0, "1"}, {modeVR1, "011"},
	{modeVR2, "000011"}, {modeVR3, "0000011"}, {modeVL1, "010"}, {modeVL2, "000010"},
	{modeVL3, "0000010"}, {modeExt, "0000001"},
}

// ccittWhiteCodes is Tables 2 and 3 of ITU-T T.4 for a white run: the
// terminating codes 0 to 63, then the make-up codes in steps of 64.
var ccittWhiteCodes = []ccittCode{
	{0, "00110101"}, {1, "000111"}, {2, "0111"}, {3, "1000"},
	{4, "1011"}, {5, "1100"}, {6, "1110"}, {7, "1111"},
	{8, "10011"}, {9, "10100"}, {10, "00111"}, {11, "01000"},
	{12, "001000"}, {13, "000011"}, {14, "110100"}, {15, "110101"},
	{16, "101010"}, {17, "101011"}, {18, "0100111"}, {19, "0001100"},
	{20, "0001000"}, {21, "0010111"}, {22, "0000011"}, {23, "0000100"},
	{24, "0101000"}, {25, "0101011"}, {26, "0010011"}, {27, "0100100"},
	{28, "0011000"}, {29, "00000010"}, {30, "00000011"}, {31, "00011010"},
	{32, "00011011"}, {33, "00010010"}, {34, "00010011"}, {35, "00010100"},
	{36, "00010101"}, {37, "00010110"}, {38, "00010111"}, {39, "00101000"},
	{40, "00101001"}, {41, "00101010"}, {42, "00101011"}, {43, "00101100"},
	{44, "00101101"}, {45, "00000100"}, {46, "00000101"}, {47, "00001010"},
	{48, "00001011"}, {49, "01010010"}, {50, "01010011"}, {51, "01010100"},
	{52, "01010101"}, {53, "00100100"}, {54, "00100101"}, {55, "01011000"},
	{56, "01011001"}, {57, "01011010"}, {58, "01011011"}, {59, "01001010"},
	{60, "01001011"}, {61, "00110010"}, {62, "00110011"}, {63, "00110100"},
	{64, "11011"}, {128, "10010"}, {192, "010111"}, {256, "0110111"},
	{320, "00110110"}, {384, "00110111"}, {448, "01100100"}, {512, "01100101"},
	{576, "01101000"}, {640, "01100111"}, {704, "011001100"}, {768, "011001101"},
	{832, "011010010"}, {896, "011010011"}, {960, "011010100"}, {1024, "011010101"},
	{1088, "011010110"}, {1152, "011010111"}, {1216, "011011000"}, {1280, "011011001"},
	{1344, "011011010"}, {1408, "011011011"}, {1472, "010011000"}, {1536, "010011001"},
	{1600, "010011010"}, {1664, "011000"}, {1728, "010011011"}, {1792, "00000001000"},
	{1856, "00000001100"}, {1920, "00000001101"}, {1984, "000000010010"}, {2048, "000000010011"},
	{2112, "000000010100"}, {2176, "000000010101"}, {2240, "000000010110"}, {2304, "000000010111"},
	{2368, "000000011100"}, {2432, "000000011101"}, {2496, "000000011110"}, {2560, "000000011111"},
}

// ccittBlackCodes is Tables 2 and 3 of ITU-T T.4 for a black run.
var ccittBlackCodes = []ccittCode{
	{0, "0000110111"}, {1, "010"}, {2, "11"}, {3, "10"},
	{4, "011"}, {5, "0011"}, {6, "0010"}, {7, "00011"},
	{8, "000101"}, {9, "000100"}, {10, "0000100"}, {11, "0000101"},
	{12, "0000111"}, {13, "00000100"}, {14, "00000111"}, {15, "000011000"},
	{16, "0000010111"}, {17, "0000011000"}, {18, "0000001000"}, {19, "00001100111"},
	{20, "00001101000"}, {21, "00001101100"}, {22, "00000110111"}, {23, "00000101000"},
	{24, "00000010111"}, {25, "00000011000"}, {26, "000011001010"}, {27, "000011001011"},
	{28, "000011001100"}, {29, "000011001101"}, {30, "000001101000"}, {31, "000001101001"},
	{32, "000001101010"}, {33, "000001101011"}, {34, "000011010010"}, {35, "000011010011"},
	{36, "000011010100"}, {37, "000011010101"}, {38, "000011010110"}, {39, "000011010111"},
	{40, "000001101100"}, {41, "000001101101"}, {42, "000011011010"}, {43, "000011011011"},
	{44, "000001010100"}, {45, "000001010101"}, {46, "000001010110"}, {47, "000001010111"},
	{48, "000001100100"}, {49, "000001100101"}, {50, "000001010010"}, {51, "000001010011"},
	{52, "000000100100"}, {53, "000000110111"}, {54, "000000111000"}, {55, "000000100111"},
	{56, "000000101000"}, {57, "000001011000"}, {58, "000001011001"}, {59, "000000101011"},
	{60, "000000101100"}, {61, "000001011010"}, {62, "000001100110"}, {63, "000001100111"},
	{64, "0000001111"}, {128, "000011001000"}, {192, "000011001001"}, {256, "000001011011"},
	{320, "000000110011"}, {384, "000000110100"}, {448, "000000110101"}, {512, "0000001101100"},
	{576, "0000001101101"}, {640, "0000001001010"}, {704, "0000001001011"}, {768, "0000001001100"},
	{832, "0000001001101"}, {896, "0000001110010"}, {960, "0000001110011"}, {1024, "0000001110100"},
	{1088, "0000001110101"}, {1152, "0000001110110"}, {1216, "0000001110111"}, {1280, "0000001010010"},
	{1344, "0000001010011"}, {1408, "0000001010100"}, {1472, "0000001010101"}, {1536, "0000001011010"},
	{1600, "0000001011011"}, {1664, "0000001100100"}, {1728, "0000001100101"}, {1792, "00000001000"},
	{1856, "00000001100"}, {1920, "00000001101"}, {1984, "000000010010"}, {2048, "000000010011"},
	{2112, "000000010100"}, {2176, "000000010101"}, {2240, "000000010110"}, {2304, "000000010111"},
	{2368, "000000011100"}, {2432, "000000011101"}, {2496, "000000011110"}, {2560, "000000011111"},
}

// A ccittTable decodes one of the code tables. Codes are grouped by their
// length, which is the whole of the decoding: read a bit, lengthen the code by
// one, and ask whether a code of that length says anything. A prefix-free code
// cannot answer twice, so the first answer is the only one.
type ccittTable struct {
	byLength []map[uint32]int
}

// newCCITTTable groups a table's codes by length. The bits are read from the
// strings the tables are written in, so what the code says and what the
// specification says are the same text.
func newCCITTTable(codes []ccittCode) *ccittTable {
	longest := 0
	for _, c := range codes {
		if len(c.bits) > longest {
			longest = len(c.bits)
		}
	}
	t := &ccittTable{byLength: make([]map[uint32]int, longest+1)}
	for _, c := range codes {
		var v uint32
		for _, b := range c.bits {
			v <<= 1
			if b == '1' {
				v |= 1
			}
		}
		n := len(c.bits)
		if t.byLength[n] == nil {
			t.byLength[n] = map[uint32]int{}
		}
		t.byLength[n][v] = c.value
	}
	return t
}

var (
	ccittModes = newCCITTTable(ccittModeCodes)
	ccittWhite = newCCITTTable(ccittWhiteCodes)
	ccittBlack = newCCITTTable(ccittBlackCodes)
)

// A ccittBits reads one bit at a time, most significant first, which is the
// order a fax is written in.
type ccittBits struct {
	data []byte
	pos  int // in bits
}

func (b *ccittBits) next() (uint32, bool) {
	if b.pos >= 8*len(b.data) {
		return 0, false
	}
	bit := (b.data[b.pos/8] >> (7 - uint(b.pos%8))) & 1
	b.pos++
	return uint32(bit), true
}

// align moves to the next byte boundary, which /EncodedByteAlign asks for at
// the start of every row.
func (b *ccittBits) align() { b.pos = (b.pos + 7) &^ 7 }

func (b *ccittBits) exhausted() bool { return b.pos >= 8*len(b.data) }

// restIsFill says whether nothing but zero bits remain. A fax is padded to a
// byte, and often to rather more than a byte, and zeros decode as perfectly
// good two-dimensional modes: pass mode is 0001 and vertical-left-3 is 0000010,
// so a decoder that does not stop here goes on inventing rows that look like
// the last real one. One French form gave 1 636 rows for a 208-row image that
// way, and every one of the surplus rows had plausible ink on it.
func (b *ccittBits) restIsFill() bool {
	for i := b.pos; i < 8*len(b.data); i++ {
		if b.data[i/8]&(1<<(7-uint(i%8))) != 0 {
			return false
		}
	}
	return true
}

// read decodes one code. It returns false at the end of the data or on a code
// the table does not name — the caller decides which of those is fatal, since a
// truncated fax is common and worth showing as far as it goes.
func (t *ccittTable) read(b *ccittBits) (int, bool) {
	var code uint32
	for n := 1; n < len(t.byLength); n++ {
		bit, ok := b.next()
		if !ok {
			return 0, false
		}
		code = code<<1 | bit
		if v, ok := t.byLength[n][code]; ok {
			return v, true
		}
	}
	return 0, false
}

// skipEOL consumes an end-of-line code, 000000000001, if one is next, and any
// fill bits before it. A Group 3 fax may have one at the end of every row, may
// have none at all, and may have several together at the end; all three are
// found in the corpus, so none of them may be an error.
func (b *ccittBits) skipEOL() bool {
	start := b.pos
	zeros := 0
	for {
		bit, ok := b.next()
		if !ok {
			b.pos = start
			return false
		}
		if bit == 0 {
			zeros++
			continue
		}
		if zeros >= 11 {
			return true
		}
		b.pos = start
		return false
	}
}

// ccittParams are the decode parameters of /CCITTFaxDecode, with the defaults
// the specification gives.
type ccittParams struct {
	k                int  // <0 Group 4, 0 Group 3 one-dimensional, >0 Group 3 mixed
	columns          int  // default 1728
	rows             int  // 0 means as many as the data holds
	blackIs1         bool // false: a 0 bit is black
	encodedByteAlign bool
	endOfBlock       bool // default true: the data ends with a block marker
}

func ccittParamsOf(parm Dict, r func(Object) Object) ccittParams {
	return ccittParams{
		k:                intParm(parm, "K", 0, r),
		columns:          intParm(parm, "Columns", 1728, r),
		rows:             intParm(parm, "Rows", 0, r),
		blackIs1:         boolParm(parm, "BlackIs1", false, r),
		encodedByteAlign: boolParm(parm, "EncodedByteAlign", false, r),
		endOfBlock:       boolParm(parm, "EndOfBlock", true, r),
	}
}

// endOfBlockEOLs is how many end-of-line codes in a row mean the data has
// ended: two for Group 4, which has none between its rows, and six for Group 3,
// where every row may legitimately be followed by one.
func endOfBlockEOLs(k int) int {
	if k < 0 {
		return 2
	}
	return 6
}

// maxCCITTPixels bounds what one image may decode to, the way maxDecodedSize
// bounds a Flate stream. A fax names its own width and height in the decode
// parameters, so a file can ask for an image of any size it likes; 2^28 pixels
// is more than four times A4 at 600 dots to the inch.
var maxCCITTPixels = 1 << 28

// ccittDecode turns a fax into bilevel samples: one bit a pixel, each row
// padded to a byte boundary, which is what every other image filter in this
// package produces and what a stream dictionary's /Width, /Height and
// /BitsPerComponent describe.
func ccittDecode(data []byte, p ccittParams) ([]byte, error) {
	if p.columns <= 0 {
		return nil, fmt.Errorf("pdf: /CCITTFaxDecode names %d columns", p.columns)
	}
	if p.rows < 0 {
		return nil, fmt.Errorf("pdf: /CCITTFaxDecode names %d rows", p.rows)
	}
	if p.rows > 0 && p.columns > maxCCITTPixels/p.rows {
		return nil, fmt.Errorf("pdf: /CCITTFaxDecode names %d by %d pixels, "+
			"past the limit of %d", p.columns, p.rows, maxCCITTPixels)
	}
	b := &ccittBits{data: data}
	stride := (p.columns + 7) / 8
	curr := make([]byte, p.columns)
	prev := []byte(nil)
	out := []byte(nil)
	for row := 0; p.rows == 0 || row < p.rows; row++ {
		if p.rows == 0 && len(out)/stride*p.columns >= maxCCITTPixels {
			return nil, fmt.Errorf("pdf: /CCITTFaxDecode ran past %d pixels "+
				"without naming how many rows it has", maxCCITTPixels)
		}
		if p.encodedByteAlign {
			b.align()
		}
		// A Group 3 row may be introduced by an end-of-line code. Consuming it
		// here rather than after the row means a file that has them and a file
		// that does not are read the same way — and counting them is how the
		// end of the data announces itself, since the block marker is nothing
		// but end-of-line codes in a row.
		eols := 0
		for b.skipEOL() {
			eols++
		}
		if p.endOfBlock && eols >= endOfBlockEOLs(p.k) {
			break
		}
		if b.exhausted() || b.restIsFill() {
			break
		}
		twoDimensional := p.k < 0
		if p.k > 0 {
			// In mixed mode a bit after the end-of-line says which the row is:
			// 1 for one-dimensional, 0 for two.
			// The data was checked for bits a moment ago, so this cannot
			// fail; the value is taken rather than the error handled, because
			// a branch that cannot be reached cannot be tested.
			bit, _ := b.next()
			twoDimensional = bit == 0
		}
		if err := ccittRow(b, curr, prev, twoDimensional); err != nil {
			if len(out) == 0 {
				return nil, err
			}
			// A truncated fax is worth showing as far as it got: the rows
			// already decoded are real, and refusing them turns a damaged
			// scan into a blank page. Leaving the loop rather than returning
			// here matters — the padding below is what makes the answer as
			// long as /Rows promised, and a fuzz case of two bytes found that
			// returning early handed the caller one row where it had asked
			// for four, with no error to say so.
			break
		}
		out = append(out, ccittPack(curr, stride, p.blackIs1)...)
		if prev == nil {
			prev = make([]byte, p.columns)
		}
		copy(prev, curr)
	}
	// A file that says how many rows it has gets that many, padded with white
	// if the data ran out, so the samples match the /Height the dictionary
	// promises rather than being short by a row.
	if p.rows > 0 {
		white := make([]byte, stride)
		if !p.blackIs1 {
			for i := range white {
				white[i] = 0xFF
			}
		}
		for len(out) < p.rows*stride {
			out = append(out, white...)
		}
	}
	return out, nil
}

// ccittPack turns a row of one byte a pixel into one bit a pixel. With
// /BlackIs1 false — the default — a 0 bit is black, so white becomes a 1.
func ccittPack(row []byte, stride int, blackIs1 bool) []byte {
	out := make([]byte, stride)
	for x, v := range row {
		white := v != 0
		if white != blackIs1 {
			out[x/8] |= 1 << (7 - uint(x%8))
		}
	}
	return out
}

// ccittRow decodes one row into curr, one byte a pixel: 0xFF white, 0x00 black.
func ccittRow(b *ccittBits, curr, prev []byte, twoDimensional bool) error {
	for i := range curr {
		curr[i] = 0
	}
	at, white, first := 0, true, true
	for at < len(curr) {
		if !twoDimensional {
			n, err := ccittRun(b, white)
			if err != nil {
				return err
			}
			if at+n > len(curr) {
				return fmt.Errorf("pdf: a fax row of %d pixels named %d",
					len(curr), at+n)
			}
			fill(curr[at:at+n], white)
			at += n
			white = !white
			first = false
			continue
		}
		mode, ok := ccittModes.read(b)
		if !ok {
			return fmt.Errorf("pdf: a fax row named no mode this reader knows")
		}
		switch mode {
		case modePass:
			// ccittFindB starts where the pen is and only walks forward,
			// stopping at the end of the row, so b2 is always a pixel of this
			// row at or after the pen. There is nothing to check.
			b2 := ccittFindB(prev, curr, at, white, first, true)
			fill(curr[at:b2], white)
			at = b2
		case modeH:
			// Two runs, of the current colour and then the other, neither of
			// them looking at the row above.
			for i := 0; i < 2; i++ {
				n, err := ccittRun(b, white)
				if err != nil {
					return err
				}
				if at+n > len(curr) {
					return fmt.Errorf("pdf: a fax row of %d pixels named %d",
						len(curr), at+n)
				}
				fill(curr[at:at+n], white)
				at += n
				white = !white
			}
			// Horizontal mode reads a pair, so the two flips above have put
			// the colour back where it started; there is nothing to undo.
		case modeV0, modeVR1, modeVR2, modeVR3, modeVL1, modeVL2, modeVL3:
			a1 := ccittFindB(prev, curr, at, white, first, false) + ccittOffset(mode)
			if a1 < at || a1 > len(curr) {
				return fmt.Errorf("pdf: a fax vertical mode named pixel %d of %d",
					a1, len(curr))
			}
			fill(curr[at:a1], white)
			at = a1
			white = !white
		default:
			return fmt.Errorf("pdf: a fax named an extension mode, which " +
				"this reader does not decode")
		}
		first = false
	}
	return nil
}

// ccittOffset is how far a vertical mode moves a1 from b1.
func ccittOffset(mode int) int {
	switch mode {
	case modeVR1:
		return 1
	case modeVR2:
		return 2
	case modeVR3:
		return 3
	case modeVL1:
		return -1
	case modeVL2:
		return -2
	case modeVL3:
		return -3
	}
	return 0
}

// ccittRun reads one run length, which is a sequence of make-up codes ending in
// a terminating code below 64.
func ccittRun(b *ccittBits, white bool) (int, error) {
	table := ccittBlack
	if white {
		table = ccittWhite
	}
	total := 0
	for {
		n, ok := table.read(b)
		if !ok {
			return 0, fmt.Errorf("pdf: a fax run named no length this reader knows")
		}
		total += n
		if total > maxCCITTPixels {
			return 0, fmt.Errorf("pdf: a fax named a run of %d pixels", total)
		}
		if n <= 63 {
			return total, nil
		}
	}
}

// ccittFindB finds b1, or b2 when second is set: the changing elements on the
// row above, as T.6 Figure 1 defines them.
func ccittFindB(prev, curr []byte, at int, white, first, second bool) int {
	// The row above the first row is implicitly all white, so it has no
	// changing elements and both b values are at the end of the row.
	if prev == nil {
		return len(curr)
	}
	i := at
	if first {
		// a0 is implicitly one pixel before the row, on white. b1 is the first
		// black pixel above; b2 the first white pixel after that.
		for i < len(prev) && prev[i] != 0 {
			i++
		}
		if second {
			for i < len(prev) && prev[i] == 0 {
				i++
			}
		}
		return i
	}
	// Walk past the run above that is of the opposite colour to the pen, then
	// past the run of the pen's own colour: what follows is b1.
	opposite := byte(0xFF)
	if white {
		opposite = 0
	}
	for i < len(prev) && prev[i] == opposite {
		i++
	}
	same := ^opposite
	for i < len(prev) && prev[i] == same {
		i++
	}
	if second {
		for i < len(prev) && prev[i] == opposite {
			i++
		}
	}
	return i
}

// boolParm reads a boolean decode parameter, falling back to its default.
func boolParm(parm Dict, key Name, def bool, r func(Object) Object) bool {
	if parm.IsZero() {
		return def
	}
	if b, ok := r(parm.Get(key)).Bool(); ok {
		return b
	}
	return def
}

// fill paints a run of pixels, one byte each.
func fill(row []byte, white bool) {
	v := byte(0)
	if white {
		v = 0xFF
	}
	for i := range row {
		row[i] = v
	}
}
