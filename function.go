package cera

import (
	"math"
	"strconv"

	"github.com/go-pdfkit/reader"
)

// PDF functions (PDF 2.0, 7.10): sampled (type 0), exponential (2),
// stitching (3) and PostScript calculator (4), and arrays of functions with
// one output each. Ported from the go-pdfkit/render fork (BSD-3). They are
// evaluated while interpreting, to fill lookup tables: soft-mask transfer
// functions now, shadings and tint transforms with M7.

type function interface {
	// eval maps in to the outputs, clipped to the domain and range.
	eval(in []float64) []float64
	outputs() int
}

// maxFunctionDepth bounds stitching functions naming functions.
const maxFunctionDepth = 8

// function reads a function or an array of functions, nil if it cannot.
func (d *Document) function(o reader.Object, depth int) function {
	if arr, ok := reader.ToArray(d.resolve(o)); ok {
		parts := make([]function, 0, len(arr))
		for _, e := range arr {
			f := d.oneFunction(e, depth)
			if f == nil {
				return nil
			}
			parts = append(parts, f)
		}
		if len(parts) == 0 {
			return nil
		}
		return &fnArray{parts: parts}
	}
	return d.oneFunction(o, depth)
}

// fnArray is an array of functions, each contributing its outputs.
type fnArray struct {
	parts []function
	n     int
}

func (f *fnArray) outputs() int {
	if f.n == 0 {
		for _, p := range f.parts {
			f.n += p.outputs()
		}
	}
	return f.n
}

func (f *fnArray) eval(in []float64) []float64 {
	out := make([]float64, 0, f.outputs())
	for _, p := range f.parts {
		out = append(out, p.eval(in)...)
	}
	return out
}

func (d *Document) oneFunction(o reader.Object, depth int) function {
	if depth > maxFunctionDepth {
		return nil
	}
	o = d.resolve(o)
	dict, ok := reader.ToDict(o)
	var s *reader.Stream
	if st, isStream := reader.ToStream(o); isStream {
		dict, ok, s = st.Dict, true, st
	}
	if !ok {
		return nil
	}
	kind, ok := d.integer(dict["FunctionType"])
	if !ok {
		return nil
	}
	base := fnBase{domain: d.floats(dict["Domain"]), rng: d.floats(dict["Range"])}
	if len(base.domain) < 2 {
		return nil
	}
	switch kind {
	case 0:
		return d.sampledFunction(base, dict, s)
	case 2:
		return d.expFunction(base, dict)
	case 3:
		return d.stitchingFunction(base, dict, depth)
	case 4:
		return d.calculatorFunction(base, s)
	}
	return nil
}

// fnBase is the domain and range every function has (the range is
// optional for types 2 and 3).
type fnBase struct {
	domain, rng []float64
}

// floats reads an array of numbers, nil if it is not one.
func (d *Document) floats(o reader.Object) []float64 {
	arr, ok := reader.ToArray(d.resolve(o))
	if !ok {
		return nil
	}
	out := make([]float64, 0, len(arr))
	for _, e := range arr {
		v, ok := d.num(e)
		if !ok {
			return nil
		}
		out = append(out, v)
	}
	return out
}

func (b fnBase) clipDomain(in []float64) []float64 {
	out := make([]float64, len(b.domain)/2)
	for i := range out {
		v := 0.0
		if i < len(in) {
			v = in[i]
		}
		out[i] = clampTo(v, b.domain[2*i], b.domain[2*i+1])
	}
	return out
}

func (b fnBase) clipRange(out []float64) []float64 {
	if len(b.rng) < 2*len(out) {
		return out
	}
	for i := range out {
		out[i] = clampTo(out[i], b.rng[2*i], b.rng[2*i+1])
	}
	return out
}

// clampTo clamps v to [lo, hi] in whichever order they come; NaN becomes lo.
func clampTo(v, lo, hi float64) float64 {
	if lo > hi {
		lo, hi = hi, lo
	}
	if !(v > lo) {
		return lo
	}
	return min(v, hi)
}

// interpolate maps x from [xmin, xmax] to [ymin, ymax].
func interpolate(x, xmin, xmax, ymin, ymax float64) float64 {
	if xmax == xmin {
		return ymin
	}
	return ymin + (x-xmin)*(ymax-ymin)/(xmax-xmin)
}

// sampledFn is a type 0 function: a grid of samples, interpolated
// multilinearly.
type sampledFn struct {
	fnBase
	size           []int
	bps            int
	encode, decode []float64
	n              int // outputs per sample
	samples        []byte
}

func (f *sampledFn) outputs() int { return f.n }

func (d *Document) sampledFunction(base fnBase, dict reader.Dict, s *reader.Stream) function {
	if s == nil || len(base.rng) < 2 {
		return nil
	}
	sizes := d.floats(dict["Size"])
	if len(sizes) == 0 || len(sizes) != len(base.domain)/2 || len(sizes) > 8 {
		return nil
	}
	f := &sampledFn{fnBase: base, n: len(base.rng) / 2, samples: d.r.DecodeStreamRecovering(s).Data}
	total := 1
	for _, v := range sizes {
		if !(v >= 1 && v <= 1<<20) {
			return nil
		}
		f.size = append(f.size, int(v))
		if total *= int(v); total > 1<<24 {
			return nil
		}
	}
	bps, _ := d.integer(dict["BitsPerSample"])
	switch bps {
	case 1, 2, 4, 8, 12, 16, 24, 32:
		f.bps = bps
	default:
		return nil
	}
	f.encode = d.floats(dict["Encode"])
	if len(f.encode) != 2*len(f.size) {
		f.encode = nil
		for _, v := range f.size {
			f.encode = append(f.encode, 0, float64(v-1))
		}
	}
	f.decode = d.floats(dict["Decode"])
	if len(f.decode) != len(base.rng) {
		f.decode = base.rng
	}
	return f
}

func (f *sampledFn) eval(in []float64) []float64 {
	x := f.clipDomain(in)
	idx := make([]int, len(x))
	frac := make([]float64, len(x))
	for i, v := range x {
		e := interpolate(v, f.domain[2*i], f.domain[2*i+1], f.encode[2*i], f.encode[2*i+1])
		e = clampTo(e, 0, float64(f.size[i]-1))
		idx[i] = min(int(math.Floor(e)), max(f.size[i]-2, 0))
		frac[i] = e - float64(idx[i])
	}
	out := make([]float64, f.n)
	maxValue := float64(uint64(1)<<uint(f.bps) - 1)
	at := make([]int, len(x))
	for c := range 1 << len(x) {
		w := 1.0
		for i := range x {
			if c&(1<<i) != 0 {
				at[i] = min(idx[i]+1, f.size[i]-1)
				w *= frac[i]
			} else {
				at[i] = idx[i]
				w *= 1 - frac[i]
			}
		}
		if w == 0 {
			continue
		}
		off, stride := 0, 1
		for i := range at {
			off += at[i] * stride
			stride *= f.size[i]
		}
		for j := range f.n {
			out[j] += w * float64(f.sample(off*f.n+j))
		}
	}
	for j := range f.n {
		out[j] = interpolate(out[j], 0, maxValue, f.decode[2*j], f.decode[2*j+1])
	}
	return f.clipRange(out)
}

// sample reads sample i of the packed grid; missing data reads as zeros.
func (f *sampledFn) sample(i int) uint64 {
	bit := i * f.bps
	var v uint64
	for k := range f.bps {
		b := bit + k
		j := b / 8
		if j >= len(f.samples) {
			return v << uint(f.bps-k)
		}
		v = v<<1 | uint64(f.samples[j]>>(7-b%8)&1)
	}
	return v
}

// expFn is a type 2 function: C0 + x^N · (C1 − C0).
type expFn struct {
	fnBase
	c0, c1 []float64
	n      float64
}

func (f *expFn) outputs() int { return len(f.c0) }

func (d *Document) expFunction(base fnBase, dict reader.Dict) function {
	f := &expFn{fnBase: base, n: 1}
	if v, ok := d.num(dict["N"]); ok {
		f.n = v
	}
	f.c0, f.c1 = d.floats(dict["C0"]), d.floats(dict["C1"])
	if f.c0 == nil {
		f.c0 = []float64{0}
	}
	if f.c1 == nil {
		f.c1 = []float64{1}
	}
	if len(f.c0) != len(f.c1) {
		return nil
	}
	return f
}

func (f *expFn) eval(in []float64) []float64 {
	t := f.clipDomain(in)[0]
	p := t
	if f.n != 1 {
		p = math.Pow(max(t, 0), f.n)
	}
	out := make([]float64, len(f.c0))
	for i := range out {
		out[i] = f.c0[i] + p*(f.c1[i]-f.c0[i])
	}
	return f.clipRange(out)
}

// stitchingFn is a type 3 function: functions laid end to end.
type stitchingFn struct {
	fnBase
	parts          []function
	bounds, encode []float64
}

func (f *stitchingFn) outputs() int {
	if len(f.rng) >= 2 {
		return len(f.rng) / 2
	}
	return f.parts[0].outputs()
}

func (d *Document) stitchingFunction(base fnBase, dict reader.Dict, depth int) function {
	arr, ok := reader.ToArray(d.resolve(dict["Functions"]))
	if !ok || len(arr) == 0 {
		return nil
	}
	f := &stitchingFn{fnBase: base}
	for _, e := range arr {
		p := d.oneFunction(e, depth+1)
		if p == nil {
			return nil
		}
		f.parts = append(f.parts, p)
	}
	f.bounds, f.encode = d.floats(dict["Bounds"]), d.floats(dict["Encode"])
	if len(f.bounds) != len(f.parts)-1 || len(f.encode) != 2*len(f.parts) {
		return nil
	}
	return f
}

func (f *stitchingFn) eval(in []float64) []float64 {
	t := f.clipDomain(in)[0]
	k := 0
	for k < len(f.bounds) && t >= f.bounds[k] {
		k++
	}
	lo, hi := f.domain[0], f.domain[1]
	if k > 0 {
		lo = f.bounds[k-1]
	}
	if k < len(f.bounds) {
		hi = f.bounds[k]
	}
	e := interpolate(t, lo, hi, f.encode[2*k], f.encode[2*k+1])
	return f.clipRange(f.parts[k].eval([]float64{e}))
}

// calcFn is a type 4 function: a PostScript calculator program.
type calcFn struct {
	fnBase
	body []psOp
	n    int
}

func (f *calcFn) outputs() int { return f.n }

// psOp is a number, an operator, or a block for if and ifelse.
type psOp struct {
	num   float64
	name  string
	block []psOp
	isBlk bool
}

const maxPSStack = 100

func (d *Document) calculatorFunction(base fnBase, s *reader.Stream) function {
	if s == nil || len(base.rng) < 2 {
		return nil
	}
	toks := psTokens(d.r.DecodeStreamRecovering(s).Data)
	if len(toks) < 2 || toks[0] != "{" {
		return nil
	}
	body, rest, ok := psParse(toks[1:], 0)
	if !ok || len(rest) != 0 {
		return nil
	}
	return &calcFn{fnBase: base, body: body, n: len(base.rng) / 2}
}

// psTokens splits a program into braces, names and numbers, dropping
// comments.
func psTokens(b []byte) []string {
	var out []string
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == '%':
			for i < len(b) && b[i] != '\n' && b[i] != '\r' {
				i++
			}
		case c == '{' || c == '}':
			out = append(out, string(c))
			i++
		case c <= ' ':
			i++
		default:
			start := i
			for i < len(b) && b[i] > ' ' && b[i] != '{' && b[i] != '}' && b[i] != '%' {
				i++
			}
			out = append(out, string(b[start:i]))
		}
	}
	return out
}

// psParse parses tokens up to the brace closing the current block.
func psParse(toks []string, depth int) (body []psOp, rest []string, ok bool) {
	if depth > 32 {
		return nil, nil, false
	}
	for len(toks) > 0 {
		t := toks[0]
		toks = toks[1:]
		switch t {
		case "}":
			return body, toks, true
		case "{":
			inner, after, ok := psParse(toks, depth+1)
			if !ok {
				return nil, nil, false
			}
			body = append(body, psOp{block: inner, isBlk: true})
			toks = after
		default:
			if v, err := strconv.ParseFloat(t, 64); err == nil {
				body = append(body, psOp{num: v, name: "#"})
				continue
			}
			body = append(body, psOp{name: t})
		}
	}
	return nil, nil, false
}

func (f *calcFn) eval(in []float64) []float64 {
	st := psRun(f.body, append([]float64{}, f.clipDomain(in)...))
	out := make([]float64, f.n)
	for i := range out {
		if j := len(st) - f.n + i; j >= 0 && j < len(st) {
			out[i] = st[j]
		}
	}
	return f.clipRange(out)
}

// psRun executes a block. Operators short of operands are skipped.
func psRun(body []psOp, st []float64) []float64 {
	var blocks [][]psOp
	for _, op := range body {
		if op.isBlk {
			blocks = append(blocks, op.block)
			continue
		}
		switch op.name {
		case "#":
			st = psPush(st, op.num)
		case "if":
			if len(blocks) < 1 || len(st) < 1 {
				blocks = nil
				continue
			}
			cond := st[len(st)-1] != 0
			st = st[:len(st)-1]
			if cond {
				st = psRun(blocks[len(blocks)-1], st)
			}
			blocks = blocks[:len(blocks)-1]
		case "ifelse":
			if len(blocks) < 2 || len(st) < 1 {
				blocks = nil
				continue
			}
			cond := st[len(st)-1] != 0
			st = st[:len(st)-1]
			if cond {
				st = psRun(blocks[len(blocks)-2], st)
			} else {
				st = psRun(blocks[len(blocks)-1], st)
			}
			blocks = blocks[:len(blocks)-2]
		default:
			st = psApply(op.name, st)
		}
	}
	return st
}

func psPush(st []float64, v float64) []float64 {
	if len(st) >= maxPSStack {
		return st
	}
	return append(st, v)
}

func psApply(name string, st []float64) []float64 {
	switch name {
	case "add", "sub", "mul", "div", "idiv", "mod", "atan", "exp",
		"eq", "ne", "gt", "ge", "lt", "le", "and", "or", "xor", "bitshift":
		if len(st) < 2 {
			return st
		}
		a, b := st[len(st)-2], st[len(st)-1]
		return psBinary(name, a, b, st[:len(st)-2])
	case "neg", "abs", "sqrt", "sin", "cos", "ln", "log", "cvi", "cvr",
		"floor", "ceiling", "round", "truncate", "not":
		if len(st) < 1 {
			return st
		}
		return psUnary(name, st[len(st)-1], st[:len(st)-1])
	case "dup":
		if len(st) < 1 {
			return st
		}
		return psPush(st, st[len(st)-1])
	case "pop":
		if len(st) < 1 {
			return st
		}
		return st[:len(st)-1]
	case "exch":
		if len(st) < 2 {
			return st
		}
		st[len(st)-2], st[len(st)-1] = st[len(st)-1], st[len(st)-2]
		return st
	case "copy":
		if len(st) < 1 {
			return st
		}
		k := int(st[len(st)-1])
		st = st[:len(st)-1]
		if k <= 0 || k > len(st) || len(st)+k > maxPSStack {
			return st
		}
		return append(st, st[len(st)-k:]...)
	case "index":
		if len(st) < 1 {
			return st
		}
		k := int(st[len(st)-1])
		st = st[:len(st)-1]
		if k < 0 || k >= len(st) {
			return st
		}
		return psPush(st, st[len(st)-1-k])
	case "roll":
		return psRoll(st)
	case "true":
		return psPush(st, 1)
	case "false":
		return psPush(st, 0)
	}
	return st
}

func psBinary(name string, a, b float64, st []float64) []float64 {
	var v float64
	switch name {
	case "add":
		v = a + b
	case "sub":
		v = a - b
	case "mul":
		v = a * b
	case "div":
		v = a / b // ±Inf is clipped by the range
	case "idiv":
		if int(b) != 0 {
			v = float64(int(a) / int(b))
		}
	case "mod":
		if int(b) != 0 {
			v = float64(int(a) % int(b))
		}
	case "atan":
		v = math.Atan2(a, b) * 180 / math.Pi
		if v < 0 {
			v += 360
		}
	case "exp":
		v = math.Pow(a, b)
	case "eq":
		v = psBool(a == b)
	case "ne":
		v = psBool(a != b)
	case "gt":
		v = psBool(a > b)
	case "ge":
		v = psBool(a >= b)
	case "lt":
		v = psBool(a < b)
	case "le":
		v = psBool(a <= b)
	case "and":
		v = float64(int(a) & int(b))
	case "or":
		v = float64(int(a) | int(b))
	case "xor":
		v = float64(int(a) ^ int(b))
	case "bitshift":
		if k := int(b); k >= 0 {
			v = float64(int(a) << uint(min(k, 63)))
		} else {
			v = float64(int(a) >> uint(min(-k, 63)))
		}
	}
	return psPush(st, v)
}

func psUnary(name string, x float64, st []float64) []float64 {
	var v float64
	switch name {
	case "neg":
		v = -x
	case "abs":
		v = math.Abs(x)
	case "sqrt":
		if x >= 0 {
			v = math.Sqrt(x)
		}
	case "sin":
		v = math.Sin(x * math.Pi / 180)
	case "cos":
		v = math.Cos(x * math.Pi / 180)
	case "ln":
		if x > 0 {
			v = math.Log(x)
		}
	case "log":
		if x > 0 {
			v = math.Log10(x)
		}
	case "cvi", "truncate":
		v = math.Trunc(x)
	case "cvr":
		v = x
	case "floor":
		v = math.Floor(x)
	case "ceiling":
		v = math.Ceil(x)
	case "round":
		v = math.Round(x)
	case "not":
		if x == 0 || x == 1 {
			v = psBool(x == 0)
		} else {
			v = float64(^int(x))
		}
	}
	return psPush(st, v)
}

// psRoll rolls the top n elements by j places.
func psRoll(st []float64) []float64 {
	if len(st) < 2 {
		return st
	}
	j, n := int(st[len(st)-1]), int(st[len(st)-2])
	st = st[:len(st)-2]
	if n <= 0 || n > len(st) {
		return st
	}
	part := st[len(st)-n:]
	j = ((j % n) + n) % n
	rolled := append(append([]float64{}, part[n-j:]...), part[:n-j]...)
	copy(part, rolled)
	return st
}

func psBool(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// lut256 tabulates a one-in, one-out function over [0, 1] in 256 steps.
func lut256(f function) *[256]uint8 {
	if f == nil || f.outputs() < 1 {
		return nil
	}
	var t [256]uint8
	var in [1]float64
	for i := range t {
		in[0] = float64(i) / 255
		t[i] = unit8(f.eval(in[:])[0])
	}
	return &t
}
