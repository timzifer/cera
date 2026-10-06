// Ported from github.com/go-pdfkit/reader v0.6.0 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/reader authors); see LICENSE-go-pdfkit.
// The object representation is cera's own (ADR 0012).

package pdf

import (
	"fmt"
	"math"
	"strconv"
	"unsafe"
)

// Kind names the eight basic PDF object types plus streams and references.
type Kind uint8

// The object kinds.
const (
	KindNull Kind = iota
	KindBool
	KindInteger
	KindReal
	KindString
	KindName
	KindArray
	KindDict
	KindStream
	KindRef
)

var kindNames = [...]string{
	KindNull:    "null",
	KindBool:    "boolean",
	KindInteger: "integer",
	KindReal:    "real",
	KindString:  "string",
	KindName:    "name",
	KindArray:   "array",
	KindDict:    "dictionary",
	KindStream:  "stream",
	KindRef:     "reference",
}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return fmt.Sprintf("Kind(%d)", uint8(k))
}

// An Object is a PDF value. The zero Object is null.
//
// Objects are values of 24 bytes: numbers, booleans and references are held
// inline, strings, names, arrays, dictionaries and streams point at their
// data. Objects a Document returns are immutable and may be shared between
// goroutines; the slices their accessors return must not be written to.
//
// Two Objects compare equal with == when they are the same number, boolean,
// reference or null, or point at the same data; equal contents at different
// places compare unequal.
type Object struct {
	p     unsafe.Pointer // string or name bytes, *Object, *Entry, *Stream
	v     uint64         // integer, float bits, bool, reference number and generation
	n     uint32         // length of a string, name, array or dictionary
	kind  Kind
	flags uint8 // builder state, see parse.go
}

// Null is the null object, the value of every absent entry.
var Null Object

// Integer returns an integer object.
func Integer(v int64) Object { return Object{kind: KindInteger, v: uint64(v)} }

// Real returns a real object.
func Real(f float64) Object { return Object{kind: KindReal, v: math.Float64bits(f)} }

// Boolean returns a boolean object.
func Boolean(b bool) Object {
	o := Object{kind: KindBool}
	if b {
		o.v = 1
	}
	return o
}

// String returns a string object holding b, which it does not copy.
func String(b []byte) Object {
	return Object{kind: KindString, p: unsafe.Pointer(unsafe.SliceData(b)), n: uint32(len(b))}
}

// Object returns the name as an object.
func (n Name) Object() Object {
	return Object{kind: KindName, p: unsafe.Pointer(unsafe.StringData(string(n))), n: uint32(len(n))}
}

// Object returns the reference as an object.
func (r Ref) Object() Object {
	return Object{kind: KindRef, v: uint64(uint32(r.Num)) | uint64(uint32(r.Gen))<<32}
}

// Object returns the array as an object, without copying it.
func (a Array) Object() Object {
	p := unsafe.Pointer(unsafe.SliceData(a))
	if p == nil {
		p = unsafe.Pointer(&emptyObjects)
	}
	return Object{kind: KindArray, p: p, n: uint32(len(a))}
}

// Object returns the dictionary as an object, without copying it. The zero
// Dict becomes null.
func (d Dict) Object() Object {
	if d.e == nil {
		return Null
	}
	p := unsafe.Pointer(unsafe.SliceData(d.e))
	return Object{kind: KindDict, p: p, n: uint32(len(d.e))}
}

// Object returns the stream as an object.
func (s *Stream) Object() Object {
	if s == nil {
		return Null
	}
	return Object{kind: KindStream, p: unsafe.Pointer(s)}
}

var (
	emptyObjects [1]Object
	emptyEntries [1]Entry
)

// Kind reports the object's type.
func (o Object) Kind() Kind { return o.kind }

// IsNull reports whether o is null.
func (o Object) IsNull() bool { return o.kind == KindNull }

// Bool reports the value of a boolean.
func (o Object) Bool() (bool, bool) { return o.v != 0, o.kind == KindBool }

// Int reports the value of an integer, and of a real whose value is a whole
// number — files do write "3.0" where an integer belongs.
func (o Object) Int() (int64, bool) {
	switch o.kind {
	case KindInteger:
		return int64(o.v), true
	case KindReal:
		f := math.Float64frombits(o.v)
		if float64(int64(f)) == f {
			return int64(f), true
		}
	}
	return 0, false
}

// Float reports the value of any number.
func (o Object) Float() (float64, bool) {
	switch o.kind {
	case KindInteger:
		return float64(int64(o.v)), true
	case KindReal:
		return math.Float64frombits(o.v), true
	}
	return 0, false
}

// Str reports the bytes of a string. They must not be written to.
func (o Object) Str() ([]byte, bool) {
	if o.kind != KindString {
		return nil, false
	}
	if o.n == 0 {
		return []byte{}, true
	}
	b := unsafe.Slice((*byte)(o.p), o.n)
	return b[:o.n:o.n], true
}

// Name reports the value of a name.
func (o Object) Name() (Name, bool) {
	if o.kind != KindName {
		return "", false
	}
	return Name(unsafe.String((*byte)(o.p), o.n)), true
}

// Array reports the elements of an array. They must not be written to.
func (o Object) Array() (Array, bool) {
	if o.kind != KindArray {
		return nil, false
	}
	a := unsafe.Slice((*Object)(o.p), o.n)
	return a[:o.n:o.n], true
}

// Dict reports a dictionary, or a stream's own dictionary — a stream is a
// dictionary everywhere a dictionary is expected.
func (o Object) Dict() (Dict, bool) {
	switch o.kind {
	case KindDict:
		e := unsafe.Slice((*Entry)(o.p), o.n)
		return Dict{e[:o.n:o.n]}, true
	case KindStream:
		return (*Stream)(o.p).Dict, true
	}
	return Dict{}, false
}

// Stream reports a stream.
func (o Object) Stream() (*Stream, bool) {
	if o.kind != KindStream {
		return nil, false
	}
	return (*Stream)(o.p), true
}

// Ref reports an indirect reference.
func (o Object) Ref() (Ref, bool) {
	if o.kind != KindRef {
		return Ref{}, false
	}
	return Ref{Num: int32(uint32(o.v)), Gen: int32(uint32(o.v >> 32))}, true
}

// String writes o roughly the way it would appear in a file, for messages
// and tests.
func (o Object) String() string {
	switch o.kind {
	case KindNull:
		return "null"
	case KindBool:
		return strconv.FormatBool(o.v != 0)
	case KindInteger:
		return strconv.FormatInt(int64(o.v), 10)
	case KindReal:
		return strconv.FormatFloat(math.Float64frombits(o.v), 'g', -1, 64)
	case KindString:
		s, _ := o.Str()
		return strconv.Quote(string(s))
	case KindName:
		n, _ := o.Name()
		return "/" + string(n)
	case KindRef:
		r, _ := o.Ref()
		return r.String()
	case KindArray:
		a, _ := o.Array()
		s := "["
		for i, e := range a {
			if i > 0 {
				s += " "
			}
			s += e.String()
		}
		return s + "]"
	case KindDict:
		d, _ := o.Dict()
		return d.String()
	case KindStream:
		st, _ := o.Stream()
		return st.Dict.String() + " stream"
	}
	return "?"
}

// Name is a PDF name with its #xx escapes resolved and the leading slash
// dropped, so /Type is Name("Type").
type Name string

// Ref is an indirect reference: the "12 0 R" that stands in for an object
// stored elsewhere in the file. A negative Num never resolves; callers may
// use them for objects of their own.
type Ref struct {
	Num, Gen int32
}

// String renders the reference the way it appears in a file.
func (r Ref) String() string { return fmt.Sprintf("%d %d R", r.Num, r.Gen) }

// Array is a PDF array.
type Array []Object

// Stream is a PDF stream: a dictionary and the bytes between the stream and
// endstream keywords. [Document.Decode] applies the filter chain;
// [Document.Raw] returns the bytes before it.
type Stream struct {
	Dict Dict
	// Ref is the indirect object the stream is, the zero Ref for a stream
	// made by a caller.
	Ref Ref
	raw []byte     // as in the file: still encrypted, aliases the input
	dec *decryptor // decrypts raw; nil when it is not encrypted
}

// NewStream returns a stream with the given dictionary and raw bytes, which
// are not encrypted.
func NewStream(d Dict, raw []byte) *Stream { return &Stream{Dict: d, raw: raw} }
