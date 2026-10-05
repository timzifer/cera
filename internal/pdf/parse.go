// Ported from github.com/go-pdfkit/reader v0.6.0 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/reader authors); see LICENSE-go-pdfkit.
// The builder that lays an object out in slabs is cera's own (ADR 0012).

package pdf

import (
	"bytes"
	"math"
	"sync"
	"unsafe"

	"github.com/timzifer/cera/internal/pdfsyntax"
)

// MaxNesting bounds how deeply arrays and dictionaries may nest in one
// object. Deeper nesting is a syntax error rather than a stack overflow.
const MaxNesting = 128

// fPend marks an object the builder has not laid out yet: its data is at
// index v of the parser's scratch (fin for an array, ent for a dictionary,
// bytes for a string) instead of behind p.
const fPend = 1

// A parser turns tokens into objects. While it parses, arrays, dictionaries
// and decoded strings collect in scratch slices; finish copies them into
// one slab each, so an indirect object costs a few allocations however many
// values it holds.
type parser struct {
	lex lexer
	// length resolves an indirect /Length; nil resolves nothing.
	length func(Ref) Object
	// copyStrings decodes every string into the scratch bytes, so crypt
	// can decrypt it in place.
	copyStrings bool
	// crypt decrypts every string in place and returns what is left of
	// it; nil when strings are not encrypted.
	crypt func([]byte) []byte

	stack []Object // values being parsed
	fin   []Object // elements of closed arrays
	ent   []Entry  // entries of closed dictionaries
	bytes []byte   // decoded strings
	depth int
}

var parserPool = sync.Pool{New: func() any { return new(parser) }}

func newParser(buf []byte, pos int) *parser {
	p := parserPool.Get().(*parser)
	p.lex = lexer{buf: buf, pos: pos}
	return p
}

// free returns the parser to the pool, dropping its references into the
// objects it built.
func (p *parser) free() {
	// Only the used part holds references: finish clears what it lays out.
	clear(p.stack)
	clear(p.fin)
	clear(p.ent)
	p.stack, p.fin, p.ent, p.bytes = p.stack[:0], p.fin[:0], p.ent[:0], p.bytes[:0]
	p.lex, p.length, p.crypt, p.copyStrings, p.depth = lexer{}, nil, nil, false, 0
	parserPool.Put(p)
}

// ParseObject parses one direct object from the start of b and reports how
// many bytes it consumed. Strings and names may alias b.
func ParseObject(b []byte) (Object, int, error) {
	p := newParser(b, 0)
	defer p.free()
	o, err := p.object()
	if err != nil {
		return Null, 0, err
	}
	return o, p.lex.pos, nil
}

// object parses one object and lays it out.
func (p *parser) object() (Object, error) {
	t, err := p.lex.next()
	if err != nil {
		return Null, err
	}
	if err := p.value(t); err != nil {
		return Null, err
	}
	return *p.finish(nil), nil
}

// expectUint reads a non-negative integer.
func (p *parser) expectUint(what string) (int64, error) {
	t, err := p.lex.next()
	if err != nil {
		return 0, err
	}
	if t.kind != tokInteger || t.num.Int < 0 {
		return 0, &SyntaxError{t.pos, what + " expected"}
	}
	return t.num.Int, nil
}

// accept consumes the keyword if it is next, and reports whether it was.
func (p *parser) accept(kw string) bool {
	save := p.lex.pos
	t, err := p.lex.next()
	if err == nil && p.lex.isKeyword(t, kw) {
		return true
	}
	p.lex.pos = save
	return false
}

// indirect parses "N G obj … endobj" at the parser's position. A stream's
// raw bytes alias the input. Before the object is laid out, crypt (when
// not nil) is asked for the function that decrypts its strings, given the
// object's reference, whether it is a stream and its dictionary's entries.
func (p *parser) indirect(crypt func(r Ref, stream bool, d []Entry) func([]byte) []byte) (Ref, *Object, error) {
	num, err := p.expectUint("object number")
	if err != nil {
		return Ref{}, nil, err
	}
	gen, err := p.expectUint("generation number")
	if err != nil {
		return Ref{}, nil, err
	}
	t, err := p.lex.next()
	if err != nil {
		return Ref{}, nil, err
	}
	if !p.lex.isKeyword(t, "obj") {
		return Ref{}, nil, &SyntaxError{t.pos, `"obj" expected`}
	}
	ref := Ref{Num: clamp32(num), Gen: clamp32(gen)}
	// An indirect object may have no value at all — "7 0 obj endobj" —
	// which files do contain. Its value is null.
	if p.accept("endobj") {
		return ref, p.finishNull(), nil
	}
	if t, err = p.lex.next(); err != nil {
		return ref, nil, err
	}
	if err := p.value(t); err != nil {
		return ref, nil, err
	}
	var st *Stream
	save := p.lex.pos
	if t, err := p.lex.next(); err == nil && p.lex.isKeyword(t, "stream") {
		top := p.stack[len(p.stack)-1]
		if top.kind != KindDict {
			return ref, nil, &SyntaxError{t.pos, "stream keyword after a " + top.kind.String()}
		}
		raw, err := p.streamData(top, t.pos)
		if err != nil {
			return ref, nil, err
		}
		st = &Stream{Ref: ref, raw: raw}
	} else {
		p.lex.pos = save
	}
	p.accept("endobj")
	if crypt != nil {
		top := p.stack[len(p.stack)-1]
		var d []Entry
		if top.kind == KindDict {
			d = p.pendingEntries(top)
		}
		p.crypt = crypt(ref, st != nil, d)
	}
	root := p.finish(st)
	return ref, root, nil
}

func clamp32(v int64) int32 {
	if v > math.MaxInt32 {
		return -1 // never resolves
	}
	return int32(v)
}

// pendingEntries returns the entries of a dictionary the builder has
// closed but not laid out.
func (p *parser) pendingEntries(o Object) []Entry {
	if o.flags&fPend == 0 {
		d, _ := o.Dict()
		return d.e
	}
	return p.ent[o.v : o.v+uint64(o.n)]
}

// streamData extracts the bytes between stream and endstream. The declared
// /Length is trusted only when endstream really does follow it — producers
// get it wrong often enough that the scan below is a common path.
func (p *parser) streamData(dict Object, kwPos int) ([]byte, error) {
	b := p.lex.buf
	i := p.lex.pos
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	if i < len(b) && b[i] == '\r' {
		i++
	}
	if i < len(b) && b[i] == '\n' {
		i++
	}
	start := i
	if n, ok := p.streamLength(dict); ok && n <= int64(len(b)-start) {
		if end := endstreamAt(b, start+int(n)); end >= 0 {
			p.lex.pos = end
			return b[start : start+int(n) : start+int(n)], nil
		}
	}
	j := bytes.Index(b[start:], []byte("endstream"))
	if j < 0 {
		return nil, &SyntaxError{kwPos, "unterminated stream"}
	}
	end := start + j
	p.lex.pos = end + len("endstream")
	// The end-of-line that precedes endstream belongs to the file, not to
	// the stream's data.
	if end > start && b[end-1] == '\n' {
		end--
	}
	if end > start && b[end-1] == '\r' {
		end--
	}
	return b[start:end:end], nil
}

// streamLength reads /Length, following an indirect reference when it can.
func (p *parser) streamLength(dict Object) (int64, bool) {
	var o Object
	for _, e := range p.pendingEntries(dict) {
		if e.Key == "Length" {
			o = e.Val
		}
	}
	if r, ok := o.Ref(); ok {
		if p.length == nil {
			return 0, false
		}
		o = p.length(r)
	}
	n, ok := o.Int()
	return n, ok && n >= 0
}

// endstreamAt reports the offset just past an endstream keyword that follows
// white-space at i, or -1 when something else is there.
func endstreamAt(b []byte, i int) int {
	for i < len(b) && isSpace(b[i]) {
		i++
	}
	if bytes.HasPrefix(b[i:], []byte("endstream")) {
		return i + len("endstream")
	}
	return -1
}

// value parses the object that starts with t and pushes it.
func (p *parser) value(t token) error {
	switch t.kind {
	case tokEOF:
		return &SyntaxError{t.pos, "unexpected end of input"}
	case tokInteger:
		p.maybeRef(t)
		return nil
	case tokReal:
		p.stack = append(p.stack, Real(t.num.Float))
		return nil
	case tokString:
		p.stack = append(p.stack, p.str(t))
		return nil
	case tokName:
		p.stack = append(p.stack, p.name(t).Object())
		return nil
	case tokArrayOpen:
		return p.array(t.pos)
	case tokDictOpen:
		return p.dict(t.pos)
	case tokKeyword:
		switch string(p.lex.text(t)) {
		case "true":
			p.stack = append(p.stack, Boolean(true))
			return nil
		case "false":
			p.stack = append(p.stack, Boolean(false))
			return nil
		case "null":
			p.stack = append(p.stack, Null)
			return nil
		}
		return &SyntaxError{t.pos, "unexpected keyword " + string(p.lex.text(t))}
	}
	return &SyntaxError{t.pos, "unexpected token"}
}

// maybeRef decides between the integer just read and the "N G R" that
// starts the same way.
func (p *parser) maybeRef(t token) {
	if t.num.Int >= 0 {
		save := p.lex.pos
		if t2, err := p.lex.next(); err == nil && t2.kind == tokInteger && t2.num.Int >= 0 {
			if t3, err := p.lex.next(); err == nil && p.lex.isKeyword(t3, "R") {
				p.stack = append(p.stack, Ref{clamp32(t.num.Int), clamp32(t2.num.Int)}.Object())
				return
			}
		}
		p.lex.pos = save
	}
	p.stack = append(p.stack, Integer(t.num.Int))
}

// str makes a string object. A string written without escapes aliases the
// input; any other is decoded into the scratch bytes.
func (p *parser) str(t token) Object {
	text := p.lex.text(t)
	if !t.esc && !p.copyStrings {
		return String(text)
	}
	off := len(p.bytes)
	switch {
	case t.hex:
		p.bytes = pdfsyntax.DecodeHex(p.bytes, text)
	case t.esc:
		p.bytes = pdfsyntax.DecodeLiteral(p.bytes, text)
	default:
		p.bytes = append(p.bytes, text...)
	}
	return Object{kind: KindString, flags: fPend, v: uint64(off), n: uint32(len(p.bytes) - off)}
}

// name makes a name: a shared one when it is well known, otherwise the
// input's bytes, and a fresh string only for a name with #xx escapes.
func (p *parser) name(t token) Name {
	text := p.lex.text(t)
	if t.esc {
		var buf [64]byte
		dec := pdfsyntax.DecodeName(buf[:0], text)
		if n, ok := intern(dec); ok {
			return n
		}
		return Name(dec)
	}
	if n, ok := intern(text); ok {
		return n
	}
	if len(text) == 0 {
		return ""
	}
	return Name(unsafe.String(&text[0], len(text)))
}

// array parses the body of an array, the bracket already consumed.
func (p *parser) array(pos int) error {
	if p.depth++; p.depth > MaxNesting {
		return &SyntaxError{pos, "objects nested too deeply"}
	}
	mark := len(p.stack)
	for {
		t, err := p.lex.next()
		if err != nil {
			return err
		}
		switch t.kind {
		case tokArrayClose:
			start := len(p.fin)
			p.fin = append(p.fin, p.stack[mark:]...)
			n := len(p.stack) - mark
			p.stack = append(p.stack[:mark], Object{kind: KindArray, flags: fPend, v: uint64(start), n: uint32(n)})
			p.depth--
			return nil
		case tokEOF:
			return &SyntaxError{t.pos, "unterminated array"}
		}
		if err := p.value(t); err != nil {
			return err
		}
	}
}

// dict parses the body of a dictionary, "<<" already consumed.
func (p *parser) dict(pos int) error {
	if p.depth++; p.depth > MaxNesting {
		return &SyntaxError{pos, "objects nested too deeply"}
	}
	mark := len(p.stack)
	for {
		t, err := p.lex.next()
		if err != nil {
			return err
		}
		switch t.kind {
		case tokDictClose:
			start := len(p.ent)
			for i := mark; i+1 < len(p.stack); i += 2 {
				k, _ := p.stack[i].Name()
				p.ent = append(p.ent, Entry{k, p.stack[i+1]})
			}
			e := normalise(p.ent[start:])
			p.ent = p.ent[:start+len(e)]
			p.stack = append(p.stack[:mark], Object{kind: KindDict, flags: fPend, v: uint64(start), n: uint32(len(e))})
			p.depth--
			return nil
		case tokEOF:
			return &SyntaxError{t.pos, "unterminated dictionary"}
		case tokName:
		default:
			return &SyntaxError{t.pos, "dictionary key is a " + t.kind.describe() + ", not a name"}
		}
		p.stack = append(p.stack, p.name(t).Object())
		v, err := p.lex.next()
		if err != nil {
			return err
		}
		if err := p.value(v); err != nil {
			return err
		}
	}
}

// describe names a token kind for an error message.
func (k tokKind) describe() string {
	switch k {
	case tokInteger, tokReal:
		return "number"
	case tokString:
		return "string"
	case tokArrayOpen, tokArrayClose:
		return "bracket"
	case tokDictOpen, tokDictClose:
		return "dictionary delimiter"
	case tokBraceOpen, tokBraceClose:
		return "brace"
	case tokKeyword:
		return "keyword"
	}
	return "token"
}

// finishNull returns a fresh null object for an object without a value.
func (p *parser) finishNull() *Object {
	o := new(Object)
	return o
}

// finish lays out the value on top of the stack: every array element in
// one slab with the root object after them, every dictionary entry in a
// second, every decoded string in a third. A stream takes the
// dictionary as its own and becomes the root.
func (p *parser) finish(st *Stream) *Object {
	root := p.stack[len(p.stack)-1]
	p.stack[len(p.stack)-1] = Object{}
	p.stack = p.stack[:len(p.stack)-1]

	objs := make([]Object, len(p.fin)+1)
	copy(objs, p.fin)
	var ents []Entry
	if len(p.ent) > 0 {
		ents = make([]Entry, len(p.ent))
		copy(ents, p.ent)
	}
	var bs []byte
	if len(p.bytes) > 0 {
		bs = make([]byte, len(p.bytes))
		copy(bs, p.bytes)
	}
	fix := func(o *Object) {
		if o.flags&fPend == 0 {
			return
		}
		switch o.kind {
		case KindArray:
			if o.n == 0 {
				o.p = unsafe.Pointer(&emptyObjects)
			} else {
				o.p = unsafe.Pointer(&objs[o.v])
			}
		case KindDict:
			if o.n == 0 {
				o.p = unsafe.Pointer(&emptyEntries)
			} else {
				o.p = unsafe.Pointer(&ents[o.v])
			}
		case KindString:
			s := bs[o.v : o.v+uint64(o.n)]
			if p.crypt != nil {
				s = p.crypt(s)
				o.n = uint32(len(s))
			}
			if len(s) > 0 {
				o.p = unsafe.Pointer(&s[0])
			} else {
				o.p = nil
			}
		}
		o.v, o.flags = 0, 0
	}
	for i := range objs[:len(objs)-1] {
		fix(&objs[i])
	}
	for i := range ents {
		fix(&ents[i].Val)
	}
	fix(&root)
	if st != nil {
		st.Dict, _ = root.Dict()
		root = st.Object()
	}
	objs[len(objs)-1] = root
	clear(p.fin)
	clear(p.ent)
	p.fin, p.ent, p.bytes = p.fin[:0], p.ent[:0], p.bytes[:0]
	return &objs[len(objs)-1]
}
