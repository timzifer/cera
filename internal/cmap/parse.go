package cmap

import (
	"errors"
	"strconv"
)

// Parse reads a CMap program, as embedded in a document or as one of
// Adobe's CMap resources. parent resolves a usecmap name (nil: only the
// predefined CMaps). Parse never panics; a program it cannot read at all is
// an error, one it reads in part gives what it read.
func Parse(data []byte, parent func(name string) *CMap) (*CMap, error) {
	return parse(data, parent, 0)
}

var errEmpty = errors.New("cmap: no mappings or codespace")

func parse(data []byte, parent func(name string) *CMap, depth int) (*CMap, error) {
	if parent == nil {
		parent = Predefined
	}
	c := &CMap{}
	l := lexer{s: data}
	// The last few operands, enough for "<lo> <hi> cid" and "/Key value def".
	var ops [3]token
	nops := 0
	pushOp := func(t token) {
		if nops == len(ops) {
			copy(ops[:], ops[1:])
			nops--
		}
		ops[nops] = t
		nops++
	}
	var spans, notdef []span
	for {
		t, ok := l.next()
		if !ok {
			break
		}
		if t.kind != tokKeyword {
			if nops > 0 && ops[nops-1].kind == tokName {
				// "/Key value def", or a pair of an inline dictionary.
				c.set(string(ops[nops-1].s), t)
			}
			pushOp(t)
			continue
		}
		switch string(t.s) {
		case "begincodespacerange":
			for {
				lo, ok1 := l.next()
				if !ok1 || lo.kind != tokHex {
					break
				}
				hi, ok2 := l.next()
				if !ok2 || hi.kind != tokHex {
					break
				}
				if n := len(lo.s); n >= 1 && n <= 4 && len(hi.s) == n && len(c.spaces) < maxSpaces {
					sp := space{n: uint8(n)}
					copy(sp.lo[:], lo.s)
					copy(sp.hi[:], hi.s)
					c.spaces = append(c.spaces, sp)
				}
			}
		case "begincidrange", "beginnotdefrange":
			isNotdef := string(t.s) == "beginnotdefrange"
			for {
				lo, ok1 := l.next()
				if !ok1 || lo.kind != tokHex {
					break
				}
				hi, ok2 := l.next()
				cid, ok3 := l.next()
				if !ok2 || !ok3 || hi.kind != tokHex || cid.kind != tokNumber {
					break
				}
				s, ok := makeSpan(lo.s, hi.s, cid.num)
				if !ok {
					continue
				}
				if isNotdef {
					if len(notdef) < maxSpans {
						notdef = append(notdef, s)
					}
				} else if len(spans) < maxSpans {
					s.step = 1
					spans = append(spans, s)
				}
			}
		case "begincidchar":
			for {
				code, ok1 := l.next()
				if !ok1 || code.kind != tokHex {
					break
				}
				cid, ok2 := l.next()
				if !ok2 || cid.kind != tokNumber {
					break
				}
				if s, ok := makeSpan(code.s, code.s, cid.num); ok && len(spans) < maxSpans {
					s.step = 1
					spans = append(spans, s)
				}
			}
		case "usecmap":
			if nops > 0 && ops[nops-1].kind == tokName && depth < maxDepth {
				name := string(ops[nops-1].s)
				if p := parent(name); p != nil && p != c {
					c.parent = p
				}
			}
		}
		nops = 0
	}
	if c.parent != nil && c.Ordering == "" {
		c.Registry, c.Ordering, c.Supplement = c.parent.Registry, c.parent.Ordering, c.parent.Supplement
	}
	c.ranges = sortSpans(spans)
	c.notdef = sortSpans(notdef)
	if len(c.ranges) == 0 && len(c.spaces) == 0 && c.parent == nil {
		return nil, errEmpty
	}
	return c, nil
}

func (c *CMap) set(k string, v token) {
	switch {
	case k == "CMapName" && v.kind == tokName:
		c.Name = string(v.s)
	case k == "WMode" && v.kind == tokNumber:
		c.WMode = int(v.num) & 1
	case k == "Registry" && v.kind == tokString:
		c.Registry = string(v.s)
	case k == "Ordering" && v.kind == tokString:
		c.Ordering = string(v.s)
	case k == "Supplement" && v.kind == tokNumber:
		c.Supplement = int(v.num)
	}
}

// makeSpan reads one range of codes. Both ends must have the same length.
func makeSpan(lo, hi []byte, cid float64) (span, bool) {
	n := len(lo)
	if n < 1 || n > 4 || len(hi) != n || !(cid >= 0 && cid < 1<<24) {
		return span{}, false
	}
	a, b := value(lo), value(hi)
	if b < a {
		return span{}, false
	}
	return span{lo: a, hi: b, cid: uint32(cid), n: uint8(n)}, true
}

// The lexer reads just enough PostScript for CMap programs: hex strings,
// literal strings, names, numbers and keywords. Dictionaries and arrays
// are skipped token by token.

type tokKind uint8

const (
	tokKeyword tokKind = iota
	tokName
	tokNumber
	tokHex
	tokString
	tokOther
)

type token struct {
	kind tokKind
	s    []byte // keyword, name, decoded hex or literal string
	num  float64
}

type lexer struct {
	s   []byte
	pos int
	buf []byte
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == 0
}

func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func (l *lexer) next() (token, bool) {
	s := l.s
	for l.pos < len(s) {
		c := s[l.pos]
		switch {
		case isSpace(c):
			l.pos++
		case c == '%':
			for l.pos < len(s) && s[l.pos] != '\n' && s[l.pos] != '\r' {
				l.pos++
			}
		case c == '/':
			l.pos++
			start := l.pos
			for l.pos < len(s) && !isSpace(s[l.pos]) && !isDelim(s[l.pos]) {
				l.pos++
			}
			return token{kind: tokName, s: s[start:l.pos]}, true
		case c == '<':
			if l.pos+1 < len(s) && s[l.pos+1] == '<' {
				l.pos += 2
				return token{kind: tokOther}, true
			}
			l.pos++
			return l.hex(), true
		case c == '>':
			l.pos++
			if l.pos < len(s) && s[l.pos] == '>' {
				l.pos++
			}
			return token{kind: tokOther}, true
		case c == '(':
			return l.literal(), true
		case c == '[' || c == ']' || c == '{' || c == '}' || c == ')':
			l.pos++
			return token{kind: tokOther}, true
		default:
			start := l.pos
			for l.pos < len(s) && !isSpace(s[l.pos]) && !isDelim(s[l.pos]) {
				l.pos++
			}
			w := s[start:l.pos]
			if v, err := strconv.ParseFloat(string(w), 64); err == nil {
				return token{kind: tokNumber, num: v}, true
			}
			return token{kind: tokKeyword, s: w}, true
		}
	}
	return token{}, false
}

// hex reads a hex string after its '<'. The bytes are valid until the next
// call.
func (l *lexer) hex() token {
	l.buf = l.buf[:0]
	hi, half := byte(0), false
	for l.pos < len(l.s) {
		c := l.s[l.pos]
		l.pos++
		if c == '>' {
			break
		}
		var v byte
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			continue
		}
		if half {
			l.buf = append(l.buf, hi<<4|v)
		} else {
			hi = v
		}
		half = !half
	}
	if half {
		l.buf = append(l.buf, hi<<4)
	}
	// Copy: a range reads two hex strings before using either.
	return token{kind: tokHex, s: append([]byte(nil), l.buf...)}
}

// literal reads a literal string, with balanced parentheses; escapes are
// kept as written (CMap programs use them only in names of no interest).
func (l *lexer) literal() token {
	l.pos++ // (
	start, depth := l.pos, 1
	for l.pos < len(l.s) {
		c := l.s[l.pos]
		l.pos++
		switch c {
		case '\\':
			l.pos++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return token{kind: tokString, s: l.s[start : l.pos-1]}
			}
		}
	}
	return token{kind: tokString, s: l.s[start:min(l.pos, len(l.s))]}
}
