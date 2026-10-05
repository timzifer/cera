package pdf

import (
	"bytes"
	"testing"
)

func FuzzParseObject(f *testing.F) {
	for _, s := range []string{
		"<</A [1 2 0 R (x) <41>] /B <</C /D#20>>>>", "[1 -2.5 .3 --4 true null]",
		"(a\\(b\\)\\101\r\n)", "12 0 R", strings1000("["),
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		o, n, err := ParseObject(b)
		if err != nil {
			return
		}
		if n < 0 || n > len(b) {
			t.Fatalf("consumed %d of %d bytes", n, len(b))
		}
		_ = o.String()
	})
}

func strings1000(s string) string {
	b := make([]byte, 0, 1000*len(s))
	for range 1000 {
		b = append(b, s...)
	}
	return string(b)
}

// FuzzOpen opens a file, walks every reachable object, decodes every stream
// and reads every page: none of it may panic or run away.
func FuzzOpen(f *testing.F) {
	f.Add(file{objs: onePage("0 0 m 1 1 l S"), trailer: "/Root 1 0 R"}.bytes())
	f.Add(file{objs: onePage("q Q"), trailer: "/Root 1 0 R", shift: 5}.bytes())
	f.Add(file{objs: onePage("q Q"), noXref: true}.bytes())
	f.Add(objStmFile(&testing.T{}, false))
	f.Add(objStmFile(&testing.T{}, true))
	saved := MaxStreamBytes
	MaxStreamBytes = 1 << 20
	defer func() { MaxStreamBytes = saved }()
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := Open(b)
		if err != nil {
			return
		}
		seen := map[Ref]bool{}
		var queue []Ref
		var visit func(o Object, depth int)
		visit = func(o Object, depth int) {
			if depth > 100 {
				return
			}
			switch o.Kind() {
			case KindRef:
				r, _ := o.Ref()
				if !seen[r] && len(seen) < 5000 {
					seen[r] = true
					queue = append(queue, r)
				}
			case KindArray:
				a, _ := o.Array()
				for _, e := range a {
					visit(e, depth+1)
				}
			case KindDict, KindStream:
				dict, _ := o.Dict()
				for _, e := range dict.Entries() {
					visit(e.Val, depth+1)
				}
				if s, ok := o.Stream(); ok {
					d.Decode(s)
				}
			}
		}
		visit(d.Trailer().Object(), 0)
		for i := range min(d.PageCount(), 100) {
			if p, err := d.Page(i + 1); err == nil {
				visit(p.Object(), 0)
			}
		}
		for len(queue) > 0 {
			r := queue[0]
			queue = queue[1:]
			o, _ := d.Get(r)
			visit(o, 0)
		}
	})
}

// FuzzFilters runs every filter, with parameters from the input.
func FuzzFilters(f *testing.F) {
	f.Add(byte(0), byte(12), byte(3), []byte{2, 1, 2, 3, 1, 4, 5, 6})
	f.Add(byte(1), byte(0), byte(0), []byte{0x80, 0x0B, 0x60, 0x50, 0x22, 0x0C, 0x0C, 0x85, 0x01})
	f.Add(byte(2), byte(0), byte(0), []byte("<~87cURD]i,\"Ebo80~>"))
	f.Add(byte(5), byte(0), byte(0), []byte{0x00, 0x10, 0x01})
	saved := MaxStreamBytes
	MaxStreamBytes = 1 << 20
	defer func() { MaxStreamBytes = saved }()
	names := []Name{"FlateDecode", "LZWDecode", "ASCII85Decode", "ASCIIHexDecode", "RunLengthDecode", "CCITTFaxDecode", "Crypt"}
	none := func(o Object) Object { return o }
	f.Fuzz(func(t *testing.T, which, pred, cols byte, data []byte) {
		parm := NewDict(
			Entry{"Predictor", Integer(int64(pred % 16))},
			Entry{"Columns", Integer(int64(cols) + 1)},
			Entry{"Colors", Integer(int64(pred>>4) + 1)},
			Entry{"K", Integer(int64(int8(cols)) % 3)},
		)
		f := names[int(which)%len(names)]
		out, _ := applyFilter(f, data, parm, none)
		if len(out) > MaxStreamBytes+1<<16 {
			t.Fatalf("%s made %d bytes", f, len(out))
		}
	})
}

// FuzzReaderAt opens a file from memory and through an io.ReaderAt with
// small windows: both must read the same.
func FuzzReaderAt(f *testing.F) {
	f.Add(file{objs: onePage("0 0 m 1 1 l S"), trailer: "/Root 1 0 R"}.bytes())
	f.Add(file{objs: onePage("q Q"), trailer: "/Root 1 0 R", shift: 5}.bytes())
	f.Add(file{objs: onePage("q Q"), noXref: true}.bytes())
	f.Add(objStmFile(&testing.T{}, true))
	savedW, savedC, savedS := firstWindow, scanChunk, MaxStreamBytes
	firstWindow, scanChunk, MaxStreamBytes = 16, 600, 1<<20
	defer func() { firstWindow, scanChunk, MaxStreamBytes = savedW, savedC, savedS }()
	f.Fuzz(func(t *testing.T, b []byte) {
		mem, err1 := Open(b)
		ra, err2 := OpenReaderAt(bytes.NewReader(b), int64(len(b)), Options{})
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("memory: %v, ReaderAt: %v", err1, err2)
		}
		if err1 != nil {
			return
		}
		if x, y := dump(mem), dump(ra); x != y {
			t.Fatalf("readers disagree\nmemory:\n%s\nReaderAt:\n%s", x, y)
		}
	})
}
