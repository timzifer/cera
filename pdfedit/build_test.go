package pdfedit

import (
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"crypto/rc4"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
)

// file builds a PDF from numbered object bodies (the text between
// "N 0 obj" and "endobj") and trailer entries. With objStm, every object
// that is not a stream goes into one object stream and the file has a
// cross-reference stream instead of a table.
type file struct {
	objs    map[int]string
	trailer string
	objStm  bool
}

func (f file) bytes() []byte {
	if f.objStm {
		return f.compressed()
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	maxNum := slices.Max(keys(f.objs))
	offs := make([]int, maxNum+1)
	for n := 1; n <= maxNum; n++ {
		body, ok := f.objs[n]
		if !ok {
			continue
		}
		offs[n] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", maxNum+1)
	for n := 1; n <= maxNum; n++ {
		if _, ok := f.objs[n]; !ok {
			b.WriteString("0000000000 65535 f \n")
			continue
		}
		fmt.Fprintf(&b, "%010d 00000 n \n", offs[n])
	}
	fmt.Fprintf(&b, "trailer\n<</Size %d %s>>\nstartxref\n%d\n%%%%EOF\n", maxNum+1, f.trailer, xref)
	return b.Bytes()
}

// compressed writes the file with an object stream and a cross-reference
// stream (PDF 1.5).
func (f file) compressed() []byte {
	nums := keys(f.objs)
	slices.Sort(nums)
	maxNum := nums[len(nums)-1]
	stm, xrefNum := maxNum+1, maxNum+2

	var head, body bytes.Buffer
	index := map[int]int{} // object → index in the object stream
	for _, n := range nums {
		if isStream(f.objs[n]) {
			continue
		}
		index[n] = len(index)
		fmt.Fprintf(&head, "%d %d ", n, body.Len())
		body.WriteString(f.objs[n])
		body.WriteByte('\n')
	}
	data := append(head.Bytes(), body.Bytes()...)
	packed := deflate(data)

	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offs := map[int]int{}
	for _, n := range nums {
		if _, ok := index[n]; ok {
			continue
		}
		offs[n] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", n, f.objs[n])
	}
	offs[stm] = b.Len()
	fmt.Fprintf(&b, "%d 0 obj\n<</Type /ObjStm /N %d /First %d /Filter /FlateDecode /Length %d>>\nstream\n%s\nendstream\nendobj\n",
		stm, len(index), head.Len(), len(packed), packed)

	offs[xrefNum] = b.Len()
	var rows []byte
	row := func(typ byte, a uint32, c uint16) {
		rows = append(rows, typ)
		rows = binary.BigEndian.AppendUint32(rows, a)
		rows = binary.BigEndian.AppendUint16(rows, c)
	}
	for n := 0; n <= xrefNum; n++ {
		if i, ok := index[n]; ok {
			row(2, uint32(stm), uint16(i))
		} else if off, ok := offs[n]; ok {
			row(1, uint32(off), 0)
		} else {
			row(0, 0, 0xFFFF)
		}
	}
	fmt.Fprintf(&b, "%d 0 obj\n<</Type /XRef /Size %d /W [1 4 2] %s /Length %d>>\nstream\n%s\nendstream\nendobj\n",
		xrefNum, xrefNum+1, f.trailer, len(rows), rows)
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", offs[xrefNum])
	return b.Bytes()
}

func isStream(body string) bool { return strings.Contains(body, "\nstream\n") }

func keys(m map[int]string) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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

// The pages of sample, by index: what each tests.
const (
	pageA = iota // inherits everything; links, annotations, a bead, a thumbnail
	pageB        // its own MediaBox and /Rotate 0 under a node with /Rotate 90
	pageC        // inherits /Rotate 90 and a CropBox from an inner node
	pageD        // its own Resources; content in a hidden layer
)

// sample is a four-page document with a nested page tree, inherited
// attributes, annotations of several kinds and document-level structures
// Extract drops.
func sample() map[int]string {
	img := deflate([]byte{255, 0, 0, 0, 255, 0, 0, 0, 255, 255, 255, 0})
	return map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R /Names <</Dests 30 0 R>> /Outlines 31 0 R " +
			"/OCProperties <</OCGs [40 0 R] /D <</OFF [40 0 R]>>>> /StructTreeRoot 32 0 R " +
			"/AcroForm <</Fields []>> /PageLabels <</Nums [0 <</S /r>>]>> /Lang (de)>>",
		2:  "<</Type /Pages /Kids [3 0 R 10 0 R] /Count 4 /MediaBox [0 0 200 100] /Resources 20 0 R>>",
		3:  "<</Type /Page /Parent 2 0 R /Contents 50 0 R /StructParents 0 /B [70 0 R] /Thumb 71 0 R /Annots [60 0 R 61 0 R 62 0 R 63 0 R 64 0 R 66 0 R 67 0 R]>>",
		10: "<</Type /Pages /Parent 2 0 R /Kids [4 0 R 5 0 R 6 0 R] /Count 3 /Rotate 90 /CropBox [10 5 190 95]>>",
		4:  "<</Type /Page /Parent 10 0 R /MediaBox [0 0 120 160] /CropBox [0 0 120 160] /Rotate 0 /Contents 51 0 R>>",
		5:  "<</Type /Page /Parent 10 0 R /Contents [52 0 R 54 0 R]>>",
		6:  "<</Type /Page /Parent 10 0 R /Rotate 0 /Resources <</XObject <</Im0 21 0 R>> /Properties <</oc1 40 0 R>>>> /Contents 53 0 R>>",
		20: "<</ExtGState <</GS0 <</Type /ExtGState /ca 0.5>>>> /XObject <</Im0 21 0 R>>>>",
		21: stream("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode", img),
		30: "<</Names [(toB) [4 0 R /Fit]]>>",
		31: "<</Type /Outlines /Count 0>>",
		32: "<</Type /StructTreeRoot>>",
		40: "<</Type /OCG /Name (Hidden)>>",
		50: stream("/Filter /FlateDecode", deflate([]byte(
			"1 0 0 rg 10 10 60 40 re f /GS0 gs 0 0 1 rg 40 30 60 40 re f q 30 0 0 30 140 40 cm /Im0 Do Q"))),
		51: stream("", []byte("0 0.6 0 rg 10 10 100 50 re f q 40 0 0 40 20 100 cm /Im0 Do Q")),
		52: stream("", []byte("0 0 0 RG 10 w 0 0 m 200 100 l S")),
		54: stream("", []byte("1 0 1 rg 120 20 50 50 re f")),
		53: stream("/Filter /FlateDecode", deflate([]byte(
			"/OC /oc1 BDC 1 0 0 rg 0 0 200 100 re f EMC 0 0 1 rg 20 20 30 30 re f q 50 0 0 50 100 20 cm /Im0 Do Q"))),
		60: "<</Type /Annot /Subtype /Link /Rect [0 0 20 20] /Border [0 0 0] /Dest [4 0 R /Fit]>>",
		61: "<</Type /Annot /Subtype /Link /Rect [20 0 40 20] /Border [0 0 0] /A <</S /GoTo /D [5 0 R /XYZ 0 0 null]>>>>",
		62: "<</Type /Annot /Subtype /Link /Rect [40 0 60 20] /Border [0 0 0] /Dest (toB)>>",
		63: "<</Type /Annot /Subtype /Link /Rect [60 0 80 20] /Border [0 0 0] /Dest [6 0 R /Fit]>>",
		64: "<</Type /Annot /Subtype /Square /Rect [150 60 180 90] /P 3 0 R /StructParent 1 /Popup 66 0 R /AP <</N 65 0 R>>>>",
		65: stream("/Type /XObject /Subtype /Form /BBox [0 0 30 30]", []byte("0 1 0 rg 0 0 30 30 re f")),
		66: "<</Type /Annot /Subtype /Popup /Rect [100 60 140 90] /Parent 64 0 R>>",
		67: "<</Type /Annot /Subtype /Link /Rect [80 0 100 20] /Border [0 0 0] /A <</S /URI /URI (https://example.com)>>>>",
		70: "<</Type /Bead /T 72 0 R /P 3 0 R /R [0 0 10 10]>>",
		71: stream("/Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode", img),
		72: "<</Type /Thread /F 70 0 R>>",
		80: "<</Title (Sample) /Producer (pdfedit test)>>",
	}
}

func sampleFile(objStm bool) []byte {
	return file{objs: sample(), trailer: "/Root 1 0 R /Info 80 0 R", objStm: objStm}.bytes()
}

// pad is the 32-byte string every pre-2.0 password is padded with.
var pad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56,
	0xFF, 0xFA, 0x01, 0x08, 0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80,
	0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

func rc4Bytes(key, b []byte) []byte {
	c, err := rc4.NewCipher(key)
	if err != nil {
		panic(err)
	}
	out := make([]byte, len(b))
	c.XORKeyStream(out, b)
	return out
}

// encrypted builds a two-page document encrypted with 128-bit RC4 (/V 2
// /R 3) under the user password, with the permissions perm.
func encrypted(password string, perm int32) []byte {
	id := []byte("0123456789abcdef")
	owner := bytes.Repeat([]byte("O"), 32)
	padded := append([]byte(password), pad...)[:32]
	// Algorithm 2: the file key.
	h := md5.New()
	h.Write(padded)
	h.Write(owner)
	h.Write(binary.LittleEndian.AppendUint32(nil, uint32(perm)))
	h.Write(id)
	key := h.Sum(nil)
	for range 50 {
		s := md5.Sum(key[:16])
		key = s[:]
	}
	// Algorithm 5: /U.
	s := md5.Sum(append(slices.Clone(pad), id...))
	u := rc4Bytes(key, s[:])
	for i := 1; i <= 19; i++ {
		k := slices.Clone(key)
		for j := range k {
			k[j] ^= byte(i)
		}
		u = rc4Bytes(k, u)
	}
	u = append(u, make([]byte, 16)...)
	enc := func(num int, b []byte) []byte {
		k := append(slices.Clone(key), byte(num), byte(num>>8), byte(num>>16), 0, 0)
		s := md5.Sum(k)
		return rc4Bytes(s[:], b)
	}
	hex := func(b []byte) string { return fmt.Sprintf("<%x>", b) }
	return file{
		objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R>>",
			2: "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 200 100] /Resources <<>>>>",
			3: "<</Type /Page /Parent 2 0 R /Contents 5 0 R>>",
			4: "<</Type /Page /Parent 2 0 R /Contents 6 0 R>>",
			5: stream("/Filter /FlateDecode", enc(5, deflate([]byte("1 0 0 rg 10 10 80 60 re f")))),
			6: stream("", enc(6, []byte("0 0 1 rg 50 20 120 70 re f"))),
			7: "<</Title " + hex(enc(7, []byte("Secret (title)"))) + ">>",
			8: fmt.Sprintf("<</Filter /Standard /V 2 /R 3 /Length 128 /P %d /O %s /U %s>>", perm, hex(owner), hex(u)),
		},
		trailer: "/Root 1 0 R /Info 7 0 R /Encrypt 8 0 R /ID [" + hex(id) + hex(id) + "]",
	}.bytes()
}
