// Package content scans PDF content streams without allocating per operand.
//
// A Scanner walks a decoded content stream one operation at a time. Operands
// are kept in a buffer the Scanner reuses: numbers are parsed in place,
// names and strings are slices of the stream (escapes are resolved on
// demand), and arrays and dictionaries are flattened into the same buffer.
// Operands are therefore only valid until the next call to Next.
//
// Like go-pdfkit/reader's ContentScanner, broken content does not stop the
// scan: a malformed token is stepped over, counted, and the operations
// around it are still returned.
package content

import (
	"bytes"
	"math"
)

// Kind is the type of an operand.
type Kind uint8

const (
	Number Kind = iota
	Name
	String    // (literal); Raw holds the bytes between the parentheses
	HexString // <hex>; Raw holds the bytes between the angle brackets
	Array
	Dict
	Bool
	Null
)

// Operand is one operand of an operation, or an element of an array or a
// dictionary operand.
type Operand struct {
	Kind Kind
	// Int reports that Num was written without a decimal point.
	Int bool
	// Num is the value of a Number, 1 or 0 for a Bool.
	Num float64
	// Raw is the unresolved text of a Name (without the slash), String or
	// HexString. It points into the content stream.
	Raw []byte
	// idx is the operand's index in the scanner's buffer and end the index
	// after its subtree; the elements of an Array or Dict follow it.
	idx, end int32
}

// MaxNesting bounds nested arrays and dictionaries in one operand.
const MaxNesting = 32

// MaxOperands bounds the operands kept for one operator; producers that
// emit more (a runaway TJ array is one operand) are truncated from the
// front, since operators take their operands from the end.
const MaxOperands = 1 << 12

// Scanner walks a content stream.
type Scanner struct {
	buf  []byte
	pos  int
	vals []Operand // flattened operands of the current operation
	top  []int32   // indexes of the top-level operands in vals
	op   []byte

	// Inline image of the current BI operation.
	imgDict int32 // index of its dictionary in vals, or -1
	imgData []byte

	errs int
	tmp  []byte
}

// Reset starts scanning data, keeping the buffers.
func (s *Scanner) Reset(data []byte) {
	s.buf, s.pos = data, 0
	s.vals, s.top = s.vals[:0], s.top[:0]
	s.op, s.imgData, s.imgDict = nil, nil, -1
	s.errs = 0
}

// Errors returns the number of malformed tokens stepped over so far.
func (s *Scanner) Errors() int { return s.errs }

// Next advances to the next operation and returns its operator, or false at
// the end of the stream. The operator points into the stream.
func (s *Scanner) Next() (op []byte, ok bool) {
	s.vals, s.top = s.vals[:0], s.top[:0]
	s.imgData, s.imgDict = nil, -1
	for {
		s.skipSpace()
		if s.pos >= len(s.buf) {
			s.op = nil
			return nil, false
		}
		c := s.buf[s.pos]
		if isRegular(c) && !isNumStart(c) {
			start := s.pos
			for s.pos < len(s.buf) && isRegular(s.buf[s.pos]) {
				s.pos++
			}
			kw := s.buf[start:s.pos]
			switch string(kw) {
			case "true", "false", "null":
				s.pushTop()
				s.keywordOperand(kw)
				continue
			case "BI":
				s.inlineImage()
			}
			s.op = kw
			return kw, true
		}
		switch c {
		case ')', '>', ']', '}', '{':
			// Stray closing delimiters (and PostScript braces, which only
			// type 4 functions use) are not operands.
			s.errs++
			s.pos++
			continue
		}
		s.pushTop()
		if !s.operand(0) {
			s.dropTop()
		}
	}
}

// Op returns the operator of the current operation.
func (s *Scanner) Op() []byte { return s.op }

// Len returns the number of operands of the current operation.
func (s *Scanner) Len() int { return len(s.top) }

// Arg returns operand i of the current operation.
func (s *Scanner) Arg(i int) *Operand { return &s.vals[s.top[i]] }

// Nums reads the last len(v) operands, which must all be finite numbers,
// into v. Operators take their operands from the end, so surplus operands
// in front are ignored.
func (s *Scanner) Nums(v []float64) bool {
	n := len(s.top)
	if n < len(v) {
		return false
	}
	for i := range v {
		o := &s.vals[s.top[n-len(v)+i]]
		if o.Kind != Number || math.IsNaN(o.Num) || math.IsInf(o.Num, 0) {
			return false
		}
		v[i] = o.Num
	}
	return true
}

// Last returns the last operand, or nil if there is none.
func (s *Scanner) Last() *Operand {
	if len(s.top) == 0 {
		return nil
	}
	return s.Arg(len(s.top) - 1)
}

// FromEnd returns operand i counted from the end (0 is the last), or nil.
func (s *Scanner) FromEnd(i int) *Operand {
	if i >= len(s.top) {
		return nil
	}
	return s.Arg(len(s.top) - 1 - i)
}

// Elems calls f for each element of an Array operand (or each key and value
// of a Dict operand, alternately) until f returns false.
func (s *Scanner) Elems(o *Operand, f func(e *Operand) bool) {
	if o.Kind != Array && o.Kind != Dict {
		return
	}
	i := o.idx + 1
	for i < o.end {
		e := &s.vals[i]
		if !f(e) {
			return
		}
		i = e.end
	}
}

// DictGet returns the value of key in a Dict operand, or nil.
func (s *Scanner) DictGet(o *Operand, key string) *Operand {
	if o == nil || o.Kind != Dict {
		return nil
	}
	i := o.idx + 1
	for i < o.end {
		k := &s.vals[i]
		if k.end >= o.end {
			return nil
		}
		v := &s.vals[k.end]
		if k.Kind == Name && s.nameIs(k, key) {
			return v
		}
		i = v.end
	}
	return nil
}

// Image returns the dictionary and the still-encoded data of the inline
// image of a BI operation.
func (s *Scanner) Image() (dict *Operand, data []byte) {
	if s.imgDict < 0 {
		return nil, nil
	}
	return &s.vals[s.imgDict], s.imgData
}

// NameIs reports whether o is the name n, with #xx escapes resolved.
func (s *Scanner) NameIs(o *Operand, n string) bool {
	return o != nil && o.Kind == Name && s.nameIs(o, n)
}

func (s *Scanner) nameIs(o *Operand, n string) bool {
	if bytes.IndexByte(o.Raw, '#') < 0 {
		return string(o.Raw) == n
	}
	return string(s.Text(o)) == n
}

// Text returns the resolved bytes of a Name, String or HexString: escapes
// and #xx sequences decoded. The result is only valid until the next call
// to Text or Next.
func (s *Scanner) Text(o *Operand) []byte {
	switch o.Kind {
	case Name:
		if bytes.IndexByte(o.Raw, '#') < 0 {
			return o.Raw
		}
		s.tmp = decodeName(s.tmp[:0], o.Raw)
	case String:
		if bytes.IndexByte(o.Raw, '\\') < 0 && bytes.IndexByte(o.Raw, '\r') < 0 {
			return o.Raw
		}
		s.tmp = decodeLiteral(s.tmp[:0], o.Raw)
	case HexString:
		s.tmp = decodeHex(s.tmp[:0], o.Raw)
	default:
		return nil
	}
	return s.tmp
}

func (s *Scanner) pushTop() {
	if len(s.top) >= MaxOperands {
		// Keep the most recent operands: shift the buffer down by the
		// first top-level operand.
		cut := s.vals[s.top[0]].end
		if len(s.top) > 1 {
			cut = s.top[1]
		}
		n := copy(s.vals, s.vals[cut:])
		s.vals = s.vals[:n]
		for i := range s.vals {
			s.vals[i].idx -= cut
			s.vals[i].end -= cut
		}
		copy(s.top, s.top[1:])
		s.top = s.top[:len(s.top)-1]
		for i := range s.top {
			s.top[i] -= cut
		}
	}
	s.top = append(s.top, int32(len(s.vals)))
}

func (s *Scanner) dropTop() {
	i := s.top[len(s.top)-1]
	s.top = s.top[:len(s.top)-1]
	s.vals = s.vals[:i]
}

func (s *Scanner) add(o Operand) {
	o.idx = int32(len(s.vals))
	o.end = o.idx + 1
	s.vals = append(s.vals, o)
}

func (s *Scanner) keywordOperand(kw []byte) {
	switch string(kw) {
	case "true":
		s.add(Operand{Kind: Bool, Num: 1})
	case "false":
		s.add(Operand{Kind: Bool})
	default:
		s.add(Operand{Kind: Null})
	}
}

// operand parses one operand at s.pos into s.vals; false means nothing was
// added (a malformed token, which has been stepped over).
func (s *Scanner) operand(depth int) bool {
	b := s.buf
	c := b[s.pos]
	switch {
	case isNumStart(c):
		v, isInt, ok := s.number()
		if !ok {
			s.errs++
			return false
		}
		s.add(Operand{Kind: Number, Num: v, Int: isInt})
		return true
	case c == '/':
		s.pos++
		start := s.pos
		for s.pos < len(b) && isRegular(b[s.pos]) {
			s.pos++
		}
		s.add(Operand{Kind: Name, Raw: b[start:s.pos]})
		return true
	case c == '(':
		raw, ok := s.literal()
		if !ok {
			s.errs++
			return false
		}
		s.add(Operand{Kind: String, Raw: raw})
		return true
	case c == '<' && s.pos+1 < len(b) && b[s.pos+1] == '<':
		s.pos += 2
		return s.container(Dict, '>', depth)
	case c == '<':
		s.pos++
		start := s.pos
		end := bytes.IndexByte(b[start:], '>')
		if end < 0 {
			s.pos = len(b)
			s.errs++
			return false
		}
		s.pos = start + end + 1
		s.add(Operand{Kind: HexString, Raw: b[start : start+end]})
		return true
	case c == '[':
		s.pos++
		return s.container(Array, ']', depth)
	case isRegular(c):
		// A keyword inside an array or dictionary.
		start := s.pos
		for s.pos < len(b) && isRegular(b[s.pos]) {
			s.pos++
		}
		switch kw := b[start:s.pos]; string(kw) {
		case "true", "false", "null":
			s.keywordOperand(kw)
			return true
		}
		s.errs++
		return false
	}
	s.errs++
	s.pos++
	return false
}

// container parses the elements of an array or dictionary up to its
// closing delimiter; the opening one has been consumed.
func (s *Scanner) container(k Kind, close byte, depth int) bool {
	if depth >= MaxNesting {
		s.errs++
		s.skipContainer()
		return false
	}
	i := len(s.vals)
	s.vals = append(s.vals, Operand{Kind: k, idx: int32(i)})
	b := s.buf
	for {
		s.skipSpace()
		if s.pos >= len(b) {
			s.errs++
			break
		}
		c := b[s.pos]
		if c == close {
			s.pos++
			if close == '>' {
				if s.pos < len(b) && b[s.pos] == '>' {
					s.pos++
				} else {
					s.errs++
				}
			}
			break
		}
		if c == ')' || c == ']' || c == '>' || c == '}' || c == '{' {
			s.errs++
			s.pos++
			continue
		}
		if k == Array && isRegular(c) && !isNumStart(c) && !isKeywordValue(b[s.pos:]) {
			// An operator inside an array means the array was never
			// closed: end it here and let the operator be read.
			s.errs++
			break
		}
		s.operand(depth + 1)
	}
	s.vals[i].end = int32(len(s.vals))
	return true
}

// skipContainer steps over a too-deeply nested array or dictionary.
func (s *Scanner) skipContainer() {
	depth := 1
	b := s.buf
	for s.pos < len(b) && depth > 0 {
		switch b[s.pos] {
		case '[':
			depth++
		case ']':
			depth--
		case '(':
			s.literal()
			continue
		case '<':
			if s.pos+1 < len(b) && b[s.pos+1] == '<' {
				depth++
				s.pos++
			}
		case '>':
			if s.pos+1 < len(b) && b[s.pos+1] == '>' {
				depth--
				s.pos++
			}
		}
		s.pos++
	}
}

func isKeywordValue(b []byte) bool {
	return bytes.HasPrefix(b, []byte("true")) || bytes.HasPrefix(b, []byte("false")) || bytes.HasPrefix(b, []byte("null"))
}

// number parses a number at s.pos without allocating. It accepts what
// producers write and readers tolerate: a doubled sign ("--5", only the
// first counts), a leading or trailing point, and it stops at the first
// byte that cannot continue the number ("3.4-5" is two numbers).
func (s *Scanner) number() (v float64, isInt, ok bool) {
	b := s.buf
	p := s.pos
	neg := false
	if b[p] == '+' || b[p] == '-' {
		neg = b[p] == '-'
		p++
		for p < len(b) && (b[p] == '+' || b[p] == '-') {
			p++
		}
	}
	var mant uint64
	digits, exp, sig := 0, 0, 0
	dot := false
	for ; p < len(b); p++ {
		c := b[p]
		switch {
		case c >= '0' && c <= '9':
			digits++
			if sig < 19 {
				if mant != 0 || c != '0' {
					sig++
				}
				mant = mant*10 + uint64(c-'0')
				if dot {
					exp--
				}
			} else if !dot {
				exp++
			}
			continue
		case c == '.' && !dot:
			dot = true
			continue
		}
		break
	}
	s.pos = p
	if digits == 0 {
		return 0, false, false // a lone sign or point, stepped over
	}
	v = float64(mant)
	switch {
	case exp < 0 && exp >= -22:
		v /= pow10[-exp]
	case exp < 0:
		v /= math.Pow10(-exp)
	case exp > 0:
		v *= math.Pow10(exp)
	}
	if neg {
		v = -v
	}
	return v, !dot, true
}

var pow10 = [...]float64{1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10,
	1e11, 1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22}

// literal reads a (string) and returns its raw bytes.
func (s *Scanner) literal() ([]byte, bool) {
	b := s.buf
	s.pos++
	start := s.pos
	depth := 1
	for s.pos < len(b) {
		switch b[s.pos] {
		case '\\':
			s.pos++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				raw := b[start:s.pos]
				s.pos++
				return raw, true
			}
		}
		s.pos++
	}
	s.pos = len(b)
	return nil, false
}

func (s *Scanner) skipSpace() {
	b := s.buf
	for s.pos < len(b) {
		c := b[s.pos]
		if isSpace(c) {
			s.pos++
			continue
		}
		if c == '%' {
			for s.pos < len(b) && b[s.pos] != '\n' && b[s.pos] != '\r' {
				s.pos++
			}
			continue
		}
		return
	}
}

// inlineImage reads the dictionary after BI and the data after ID, and
// leaves the scanner after EI.
func (s *Scanner) inlineImage() {
	b := s.buf
	s.imgDict = int32(len(s.vals))
	s.vals = append(s.vals, Operand{Kind: Dict, idx: s.imgDict})
	for {
		s.skipSpace()
		if s.pos >= len(b) {
			s.errs++
			s.vals[s.imgDict].end = int32(len(s.vals))
			return
		}
		if b[s.pos] == 'I' && s.pos+1 < len(b) && b[s.pos+1] == 'D' && (s.pos+2 >= len(b) || !isRegular(b[s.pos+2])) {
			s.pos += 2
			break
		}
		if isRegular(b[s.pos]) && !isNumStart(b[s.pos]) && !isKeywordValue(b[s.pos:]) {
			// Some other operator: the image has no ID.
			s.errs++
			s.vals[s.imgDict].end = int32(len(s.vals))
			return
		}
		if !s.operand(0) {
			continue
		}
	}
	s.vals[s.imgDict].end = int32(len(s.vals))
	// Exactly one white-space byte separates ID from the data.
	if s.pos < len(b) && isSpace(b[s.pos]) {
		s.pos++
	}
	start := s.pos
	end := s.imageEnd(start)
	if end < 0 {
		s.errs++
		s.imgData = b[start:]
		s.pos = len(b)
		return
	}
	s.imgData = b[start:end]
	s.pos = end
	for s.pos < len(b) && isSpace(b[s.pos]) {
		s.pos++
	}
	s.pos = min(s.pos+2, len(b)) // EI
}

// imageEnd finds the end of inline image data starting at start: the
// declared length if an EI follows it, otherwise the first EI keyword
// preceded by white-space whose following bytes look like content again.
func (s *Scanner) imageEnd(start int) int {
	d := &s.vals[s.imgDict]
	// Lengths are compared before they are converted: a huge one would
	// wrap around to a negative int.
	rest := len(s.buf) - start
	if n, ok := s.sampleBytes(d); ok && n >= 0 && n <= rest && eiAt(s.buf, start+n) {
		return start + n
	}
	for _, key := range [...]string{"L", "Length"} {
		if l := s.DictGet(d, key); l != nil && l.Kind == Number && l.Num >= 0 && l.Num <= float64(rest) && eiAt(s.buf, start+int(l.Num)) {
			return start + int(l.Num)
		}
	}
	b := s.buf
	for i := start; i+1 < len(b); i++ {
		if b[i] != 'E' || b[i+1] != 'I' || (i+2 < len(b) && isRegular(b[i+2])) || i == start || !isSpace(b[i-1]) {
			continue
		}
		if plausibleAfter(b[i+2:]) {
			return i - 1
		}
	}
	return -1
}

// sampleBytes returns the size of unfiltered image data from the width,
// height, bits per component and colour space of the dictionary.
func (s *Scanner) sampleBytes(d *Operand) (int, bool) {
	get := func(a, b string) *Operand {
		if v := s.DictGet(d, a); v != nil {
			return v
		}
		return s.DictGet(d, b)
	}
	if f := get("F", "Filter"); f != nil && (f.Kind != Array || f.end != f.idx+1) {
		return 0, false
	}
	w, h := get("W", "Width"), get("H", "Height")
	if w == nil || h == nil || w.Kind != Number || h.Kind != Number || w.Num <= 0 || h.Num <= 0 || w.Num*h.Num > 1<<30 {
		return 0, false
	}
	bpc, comps := 1.0, 1.0
	if im := get("IM", "ImageMask"); im == nil || im.Num == 0 {
		v := get("BPC", "BitsPerComponent")
		if v == nil || v.Kind != Number {
			return 0, false
		}
		bpc = v.Num
		cs := get("CS", "ColorSpace")
		switch {
		case cs == nil:
			return 0, false
		case s.NameIs(cs, "RGB") || s.NameIs(cs, "DeviceRGB"):
			comps = 3
		case s.NameIs(cs, "CMYK") || s.NameIs(cs, "DeviceCMYK"):
			comps = 4
		case s.NameIs(cs, "G") || s.NameIs(cs, "DeviceGray") || cs.Kind == Array:
			// Indexed arrays have one component.
		default:
			return 0, false
		}
	}
	n := math.Ceil(w.Num*comps*bpc/8) * math.Floor(h.Num)
	if !(n >= 0 && n <= 1<<40) {
		return 0, false
	}
	return int(n), true
}

// eiAt reports whether an EI keyword stands at i after optional white-space.
func eiAt(b []byte, i int) bool {
	for i < len(b) && isSpace(b[i]) {
		i++
	}
	return bytes.HasPrefix(b[i:], []byte("EI")) && (i+2 >= len(b) || !isRegular(b[i+2]))
}

// plausibleAfter reports whether content after an EI candidate looks like
// content-stream syntax: the end, or printable tokens for a while.
func plausibleAfter(b []byte) bool {
	n := min(len(b), 32)
	for _, c := range b[:n] {
		if c >= 0x7f || (c < 0x20 && !isSpace(c)) {
			return false
		}
	}
	return true
}

func isSpace(c byte) bool {
	return c == 0 || c == 9 || c == 10 || c == 12 || c == 13 || c == 32
}

func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func isRegular(c byte) bool { return !isSpace(c) && !isDelim(c) }

func isNumStart(c byte) bool { return c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9') }

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func decodeName(dst, raw []byte) []byte {
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '#' && i+2 < len(raw) {
			if h1, h2 := hexVal(raw[i+1]), hexVal(raw[i+2]); h1 >= 0 && h2 >= 0 {
				dst = append(dst, byte(h1<<4|h2))
				i += 2
				continue
			}
		}
		dst = append(dst, c)
	}
	return dst
}

func decodeHex(dst, raw []byte) []byte {
	hi := -1
	for _, c := range raw {
		v := hexVal(c)
		if v < 0 {
			continue
		}
		if hi < 0 {
			hi = v
			continue
		}
		dst = append(dst, byte(hi<<4|v))
		hi = -1
	}
	if hi >= 0 {
		dst = append(dst, byte(hi<<4))
	}
	return dst
}

func decodeLiteral(dst, raw []byte) []byte {
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch c {
		case '\r':
			if i+1 < len(raw) && raw[i+1] == '\n' {
				i++
			}
			dst = append(dst, '\n')
			continue
		case '\\':
		default:
			dst = append(dst, c)
			continue
		}
		i++
		if i >= len(raw) {
			break
		}
		e := raw[i]
		switch {
		case e == 'n':
			dst = append(dst, '\n')
		case e == 'r':
			dst = append(dst, '\r')
		case e == 't':
			dst = append(dst, '\t')
		case e == 'b':
			dst = append(dst, '\b')
		case e == 'f':
			dst = append(dst, '\f')
		case e == '\r':
			if i+1 < len(raw) && raw[i+1] == '\n' {
				i++
			}
		case e == '\n':
		case e >= '0' && e <= '7':
			v := int(e - '0')
			for k := 0; k < 2 && i+1 < len(raw) && raw[i+1] >= '0' && raw[i+1] <= '7'; k++ {
				i++
				v = v*8 + int(raw[i]-'0')
			}
			dst = append(dst, byte(v))
		default:
			dst = append(dst, e)
		}
	}
	return dst
}
