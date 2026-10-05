// Ported from github.com/go-pdfkit/reader v0.6.0 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/reader authors); see LICENSE-go-pdfkit.
// Rewritten to return spans of the input instead of copies.

package pdf

import (
	"fmt"

	"github.com/timzifer/cera/internal/pdfsyntax"
)

// A SyntaxError reports malformed PDF syntax and where it was found.
type SyntaxError struct {
	Offset int
	Msg    string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("pdf: %s at offset %d", e.Msg, e.Offset)
}

func isSpace(c byte) bool   { return pdfsyntax.IsSpace(c) }
func isRegular(c byte) bool { return pdfsyntax.IsRegular(c) }

type tokKind uint8

const (
	tokEOF tokKind = iota
	tokInteger
	tokReal
	tokString
	tokName
	tokArrayOpen
	tokArrayClose
	tokDictOpen
	tokDictClose
	tokBraceOpen
	tokBraceClose
	tokKeyword
)

// A token is one lexical unit. It does not copy the input: a string, name
// or keyword is the span [start, end) of it — for a string the bytes
// between the delimiters, for a name the bytes after the slash — and esc
// says whether that span must be decoded first.
type token struct {
	kind       tokKind
	esc        bool // literal string with escapes, name with #xx
	hex        bool // hex string
	pos        int
	start, end int
	num        pdfsyntax.Number
}

// A lexer walks the PDF object syntax over a byte slice.
type lexer struct {
	buf []byte
	pos int
}

func (l *lexer) skipSpace() { l.pos = pdfsyntax.SkipSpace(l.buf, l.pos) }

// text returns the token's span.
func (l *lexer) text(t token) []byte { return l.buf[t.start:t.end] }

// isKeyword reports whether t is the keyword kw.
func (l *lexer) isKeyword(t token, kw string) bool {
	return t.kind == tokKeyword && string(l.buf[t.start:t.end]) == kw
}

// next returns the token at the current position and advances past it.
func (l *lexer) next() (token, error) {
	l.skipSpace()
	start := l.pos
	b := l.buf
	if l.pos >= len(b) {
		return token{kind: tokEOF, pos: start}, nil
	}
	switch c := b[l.pos]; {
	case c == '[':
		l.pos++
		return token{kind: tokArrayOpen, pos: start}, nil
	case c == ']':
		l.pos++
		return token{kind: tokArrayClose, pos: start}, nil
	case c == '{':
		l.pos++
		return token{kind: tokBraceOpen, pos: start}, nil
	case c == '}':
		l.pos++
		return token{kind: tokBraceClose, pos: start}, nil
	case c == '<':
		if l.pos+1 < len(b) && b[l.pos+1] == '<' {
			l.pos += 2
			return token{kind: tokDictOpen, pos: start}, nil
		}
		for p := l.pos + 1; p < len(b); p++ {
			if b[p] == '>' {
				l.pos = p + 1
				return token{kind: tokString, hex: true, esc: true, pos: start, start: start + 1, end: p}, nil
			}
		}
		l.pos = len(b)
		return token{}, &SyntaxError{start, "unterminated hex string"}
	case c == '>':
		if l.pos+1 < len(b) && b[l.pos+1] == '>' {
			l.pos += 2
			return token{kind: tokDictClose, pos: start}, nil
		}
		l.pos++
		return token{}, &SyntaxError{start, "stray '>'"}
	case c == '(':
		end, esc := pdfsyntax.LiteralEnd(b, l.pos)
		if end < 0 {
			l.pos = len(b)
			return token{}, &SyntaxError{start, "unterminated string"}
		}
		l.pos = end
		return token{kind: tokString, esc: esc, pos: start, start: start + 1, end: end - 1}, nil
	case c == ')':
		l.pos++
		return token{}, &SyntaxError{start, "stray ')'"}
	case c == '/':
		return l.name(start)
	case pdfsyntax.IsNumStart(c):
		n, end, ok := pdfsyntax.ParseNumber(b, l.pos)
		if !ok {
			l.pos = end
			return token{}, &SyntaxError{start, fmt.Sprintf("malformed number %q", b[start:end])}
		}
		l.pos = end
		if n.IsInt {
			return token{kind: tokInteger, num: n, pos: start}, nil
		}
		return token{kind: tokReal, num: n, pos: start}, nil
	default:
		p := l.pos
		for p < len(b) && isRegular(b[p]) {
			p++
		}
		l.pos = p
		return token{kind: tokKeyword, pos: start, start: start, end: p}, nil
	}
}

// name reads a /Name. A #xx escape must be two hex digits.
func (l *lexer) name(start int) (token, error) {
	b := l.buf
	p := l.pos + 1
	esc := false
	for p < len(b) && isRegular(b[p]) {
		if b[p] != '#' {
			p++
			continue
		}
		if p+2 >= len(b) {
			l.pos = len(b)
			return token{}, &SyntaxError{start, "truncated #xx escape in name"}
		}
		if pdfsyntax.HexVal(b[p+1]) < 0 || pdfsyntax.HexVal(b[p+2]) < 0 {
			l.pos = p + 1
			return token{}, &SyntaxError{start, "invalid #xx escape in name"}
		}
		esc = true
		p += 3
	}
	l.pos = p
	return token{kind: tokName, esc: esc, pos: start, start: start + 1, end: p}, nil
}
