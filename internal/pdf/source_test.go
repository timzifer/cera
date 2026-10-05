package pdf

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// dump lists everything a document says: trailer, pages with inherited
// attributes, their contents, and every object reachable from them.
func dump(d *Document) string {
	var b strings.Builder
	fmt.Fprintf(&b, "version %s repaired %v pages %d\ntrailer %v\n", d.Version(), d.Repaired(), d.PageCount(), d.Trailer())
	seen := map[Ref]bool{}
	var queue []Ref
	var visit func(o Object)
	visit = func(o Object) {
		switch o.Kind() {
		case KindRef:
			r, _ := o.Ref()
			if !seen[r] {
				seen[r] = true
				queue = append(queue, r)
			}
		case KindArray:
			a, _ := o.Array()
			for _, e := range a {
				visit(e)
			}
		case KindDict, KindStream:
			dict, _ := o.Dict()
			for _, e := range dict.Entries() {
				visit(e.Val)
			}
		}
	}
	visit(d.Trailer().Object())
	for i := range d.PageCount() {
		p, err := d.Page(i + 1)
		c, _ := d.PageContents(i + 1)
		fmt.Fprintf(&b, "page %d %v %v contents %q\n", i, p, err, c.Data)
		visit(p.Object())
	}
	var objs []string
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		o, err := d.Get(r)
		s := fmt.Sprintf("%v %v %v", r, o, err)
		if st, ok := o.Stream(); ok {
			dec := d.Decode(st)
			s += fmt.Sprintf(" raw %q data %q recovered %v", d.Raw(st), dec.Data, dec.Recovered)
		}
		objs = append(objs, s)
		visit(o)
	}
	slices.Sort(objs)
	b.WriteString(strings.Join(objs, "\n"))
	return b.String()
}

// TestReaderAtAgrees opens every test document both from memory and through
// an io.ReaderAt with windows of a few bytes, which every object outgrows.
func TestReaderAtAgrees(t *testing.T) {
	savedW, savedC := firstWindow, scanChunk
	firstWindow, scanChunk = 16, 600
	defer func() { firstWindow, scanChunk = savedW, savedC }()
	objs := onePage("0 0 m 100 100 l S")
	objs[5] = "<</Name (a string \\(with\\) escapes) /Hex <414243> /N 12345.678 /Arr [1 2 3 /Names #20 4 0 R]>>"
	objs[6] = stream("/Filter /FlateDecode", deflate([]byte(strings.Repeat("big content ", 500))))
	files := map[string][]byte{
		"simple":  file{objs: objs, trailer: "/Root 1 0 R"}.bytes(),
		"shifted": file{objs: objs, trailer: "/Root 1 0 R", shift: 9}.bytes(),
		"no xref": file{objs: objs, trailer: "/Root 1 0 R", noXref: true}.bytes(),
		"objstm":  objStmFile(t, false),
		"hybrid":  objStmFile(t, true),
		"wrong /Length": func() []byte {
			o := onePage("")
			o[4] = "<</Length 3>>\nstream\n" + strings.Repeat("x", 3000) + "\nendstream"
			return file{objs: o, trailer: "/Root 1 0 R"}.bytes()
		}(),
	}
	for name, data := range files {
		mem, err := Open(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ra, err := OpenReaderAt(bytes.NewReader(data), int64(len(data)), Options{})
		if err != nil {
			t.Fatalf("%s through ReaderAt: %v", name, err)
		}
		if a, b := dump(mem), dump(ra); a != b {
			t.Errorf("%s: the readers disagree\nmemory:\n%s\nReaderAt:\n%s", name, a, b)
		}
	}
}
