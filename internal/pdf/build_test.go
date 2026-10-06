package pdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"

	"github.com/andybalholm/brotli"
)

// file builds a PDF from numbered object bodies (the text between
// "N 0 obj" and "endobj"), with a classic cross-reference table and the
// given trailer entries. Object numbers are the keys of objs.
type file struct {
	objs    map[int]string
	trailer string
	// noXref leaves the table out, so the file must be repaired.
	noXref bool
	// shift moves every offset in the table by this many bytes.
	shift int
}

func (f file) bytes() []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	maxNum := 0
	for n := range f.objs {
		maxNum = max(maxNum, n)
	}
	offs := make([]int, maxNum+1)
	for n := 1; n <= maxNum; n++ {
		body, ok := f.objs[n]
		if !ok {
			continue
		}
		offs[n] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	if f.noXref {
		fmt.Fprintf(&b, "trailer\n<<%s>>\n%%%%EOF\n", f.trailer)
		return b.Bytes()
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", maxNum+1)
	for n := 1; n <= maxNum; n++ {
		if _, ok := f.objs[n]; !ok {
			b.WriteString("0000000000 65535 f \n")
			continue
		}
		fmt.Fprintf(&b, "%010d 00000 n \n", offs[n]+f.shift)
	}
	fmt.Fprintf(&b, "trailer\n<</Size %d %s>>\nstartxref\n%d\n%%%%EOF\n", maxNum+1, f.trailer, xref)
	return b.Bytes()
}

// stream writes a stream object body.
func stream(dict string, data []byte) string {
	return fmt.Sprintf("<<%s /Length %d>>\nstream\n%s\nendstream", dict, len(data), data)
}

func deflate(b []byte) []byte {
	var out bytes.Buffer
	w := zlib.NewWriter(&out)
	w.Write(b)
	w.Close()
	return out.Bytes()
}

func brotliCompress(b []byte) []byte {
	var out bytes.Buffer
	w := brotli.NewWriter(&out)
	w.Write(b)
	w.Close()
	return out.Bytes()
}

// onePage is a minimal valid document with one page whose content is c.
func onePage(c string) map[int]string {
	return map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R>>",
		2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 100]>>",
		3: "<</Type /Page /Parent 2 0 R /Contents 4 0 R /Resources <<>>>>",
		4: stream("", []byte(c)),
	}
}

func mustOpen(t interface {
	Helper()
	Fatal(...any)
}, b []byte) *Document {
	t.Helper()
	d, err := Open(b)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

var _ = strings.Repeat
